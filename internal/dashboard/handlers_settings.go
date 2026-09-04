//go:build windows

package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/etwids"
)

// notifyTargetView is the write-only view of a notification target returned to
// the dashboard. Secrets are stripped — only a has_secret boolean is exposed
// so the UI can tell whether a saved credential exists.
type notifyTargetView struct {
	Type             string                           `json:"type"`
	URL              string                           `json:"url"`
	Triggers         []dc.Trigger                     `json:"triggers"`
	RepeatMinutes    int                              `json:"repeat_minutes,omitempty"`
	HasSecret        bool                             `json:"has_secret"`
	To               []string                         `json:"to,omitempty"`
	From             string                           `json:"from,omitempty"`
	Enabled          *bool                            `json:"enabled,omitempty"`
	ServerExclusions []dc.NotificationServerExclusion `json:"server_exclusions,omitempty"`
}

// notifyTargetWire is the inbound shape used by both the bulk PUT /settings and
// the per-target CRUD endpoints. ClearSecret is wire-only: when true, the
// existing on-disk secret is wiped regardless of the Secret field. We can't put
// it on dc.NotificationTarget because that struct is also the on-disk format.
type notifyTargetWire struct {
	dc.NotificationTarget
	ClearSecret bool `json:"clear_secret,omitempty"`
}

// makeNotifyTargetView projects an on-disk target into the redacted view.
func makeNotifyTargetView(t dc.NotificationTarget) notifyTargetView {
	return notifyTargetView{
		Type:             t.Type,
		URL:              t.URL,
		Triggers:         t.Triggers,
		RepeatMinutes:    t.RepeatMinutes,
		HasSecret:        t.Secret != "",
		To:               t.To,
		From:             t.From,
		Enabled:          t.Enabled,
		ServerExclusions: t.ServerExclusions,
	}
}

// makeNotifyTargetViews projects a slice with a non-nil empty fallback so JSON
// encoding produces [] rather than null.
func makeNotifyTargetViews(targets []dc.NotificationTarget) []notifyTargetView {
	views := make([]notifyTargetView, len(targets))
	for i, t := range targets {
		views[i] = makeNotifyTargetView(t)
	}
	return views
}

// validateNotificationTarget returns a 400-class error describing why the
// target is rejected, or nil if it passes structural validation. The idx is
// included only in error messages from the bulk PUT path; per-target endpoints
// pass -1 to suppress the prefix.
func validateNotificationTarget(t dc.NotificationTarget, idx int) error {
	prefix := ""
	if idx >= 0 {
		prefix = fmt.Sprintf("notifications[%d]: ", idx)
	}
	if t.Type != "webhook" && t.Type != "ntfy" && t.Type != "email" {
		return fmt.Errorf("%sunknown type %q (want \"webhook\", \"ntfy\", or \"email\")", prefix, t.Type)
	}
	if t.URL != "" {
		lower := strings.ToLower(t.URL)
		validScheme := strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") ||
			strings.HasPrefix(lower, "smtp://") || strings.HasPrefix(lower, "smtps://")
		if !validScheme {
			return fmt.Errorf("%sURL must use http, https, smtp, or smtps scheme", prefix)
		}
	}
	for _, tr := range t.Triggers {
		if !dc.ValidTriggers[tr] {
			return fmt.Errorf("%sunknown trigger %q", prefix, tr)
		}
	}
	seenServers := make(map[string]bool, len(t.ServerExclusions))
	for _, exclusion := range t.ServerExclusions {
		server := strings.TrimSpace(exclusion.Server)
		if server == "" {
			return fmt.Errorf("%sserver exclusion requires a server name", prefix)
		}
		serverKey := strings.ToLower(strings.TrimSuffix(server, "."))
		if seenServers[serverKey] {
			return fmt.Errorf("%sserver exclusion %q is duplicated", prefix, server)
		}
		seenServers[serverKey] = true
		if len(exclusion.Triggers) == 0 {
			return fmt.Errorf("%sserver exclusion %q requires at least one trigger", prefix, server)
		}
		for _, tr := range exclusion.Triggers {
			if !dc.ValidTriggers[tr] {
				return fmt.Errorf("%sserver exclusion %q has unknown trigger %q", prefix, server, tr)
			}
			if !t.HasTrigger(tr) {
				return fmt.Errorf("%sserver exclusion %q uses trigger %q not enabled for this target", prefix, server, tr)
			}
		}
	}
	if t.RepeatMinutes < 0 || t.RepeatMinutes > dc.MaxRepeatMinutes {
		return fmt.Errorf("%srepeat_minutes must be 0–%d", prefix, dc.MaxRepeatMinutes)
	}
	return nil
}

// applySecretPolicy implements the three-mode secret semantics shared between
// the bulk PUT and the per-target endpoints:
//
//	clearSecret=true  → wipe the saved secret (regardless of new.Secret)
//	new.Secret == ""  → preserve the existing secret
//	new.Secret != ""  → replace with the new value (DPAPI-encrypted by Validate)
//
// existingSecret is the secret loaded from disk for this target slot. Per-target
// endpoints pass it in by index (robust to URL renames). The bulk PUT path
// looks it up by Type+URL.
func applySecretPolicy(newTarget *dc.NotificationTarget, existingSecret string, clearSecret bool) {
	switch {
	case clearSecret:
		newTarget.Secret = ""
	case newTarget.Secret == "":
		newTarget.Secret = existingSecret
	}
}
func validatePerformanceConfig(p dc.PerformanceConfig) error {
	percentages := []struct {
		name  string
		value int
	}{
		{"cpu_warn_pct", p.CPUWarnPct},
		{"cpu_crit_pct", p.CPUCritPct},
		{"mem_warn_pct", p.MemWarnPct},
		{"mem_crit_pct", p.MemCritPct},
	}
	for _, field := range percentages {
		if field.value < -1 || field.value > 100 {
			return fmt.Errorf("%s must be -1 or 0–100", field.name)
		}
	}
	if p.InputDelayWarnMS < -1 {
		return fmt.Errorf("input_delay_warn_ms must be -1 or non-negative")
	}
	if p.InputDelayCritMS < -1 {
		return fmt.Errorf("input_delay_crit_ms must be -1 or non-negative")
	}
	if p.CPUWarnPct > 0 && p.CPUCritPct > 0 && p.CPUWarnPct >= p.CPUCritPct {
		return fmt.Errorf("cpu_warn_pct must be less than cpu_crit_pct")
	}
	// Memory is stored as percentage free, so the warning threshold must be
	// higher than the critical threshold (for example 20% free, then 10%).
	if p.MemWarnPct > 0 && p.MemCritPct > 0 && p.MemWarnPct <= p.MemCritPct {
		return fmt.Errorf("mem_warn_pct must be greater than mem_crit_pct")
	}
	if p.InputDelayWarnMS > 0 && p.InputDelayCritMS > 0 && p.InputDelayWarnMS >= p.InputDelayCritMS {
		return fmt.Errorf("input_delay_warn_ms must be less than input_delay_crit_ms")
	}
	return nil
}

// handleGetSettings returns the current dashboard settings as JSON.
func (ds *DashboardServer) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	var cfg *dc.Config
	var err error
	if ds.testLoadConfigFunc != nil {
		cfg, err = ds.testLoadConfigFunc()
	} else {
		cfg, err = dc.LoadConfig()
	}
	if err != nil {
		slog.Error("load config failed", "error", err)
		http.Error(w, "failed to load config", http.StatusInternalServerError)
		return
	}

	views := makeNotifyTargetViews(cfg.Notifications)
	// evtspike surface is intentionally narrow: the modal only exposes the
	// enabled toggle per FR-028. Other evtspike fields (thresholds, channel
	// lists, baseline_path, security-channel gate) stay admin-only in
	// config.json and are not leaked here.
	type evtspikeView struct {
		Enabled bool `json:"enabled"`
	}
	out := struct {
		Notifications           []notifyTargetView   `json:"notifications"`
		SessionWarningThreshold int                  `json:"session_warning_threshold"`
		GracePeriod             int                  `json:"grace_period"`
		PollInterval            int                  `json:"poll_interval"`
		Performance             dc.PerformanceConfig `json:"performance"`
		EvtSpike                evtspikeView         `json:"evtspike"`
		Update                  dc.UpdateConfig      `json:"update"`
	}{
		Notifications:           views,
		SessionWarningThreshold: cfg.SessionWarningThreshold,
		GracePeriod:             cfg.GracePeriod,
		PollInterval:            cfg.PollInterval,
		Performance:             cfg.Performance,
		EvtSpike:                evtspikeView{Enabled: cfg.EvtSpike.Enabled},
		Update:                  cfg.Update,
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

// handlePutSettings accepts JSON and updates dashboard settings atomically.
// All provided fields are written in a single config load+save cycle.
// Absent fields (not present in the JSON body) are left unchanged; to clear
// notifications send "notifications": [].
func (ds *DashboardServer) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8192))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// evtspikeInput narrows the modal's write surface to just the enabled
	// flag (FR-028). Admin-only evtspike fields are not accepted here; those
	// belong in config.json.
	type evtspikeInput struct {
		Enabled *bool `json:"enabled,omitempty"`
	}
	// Use pointer-to-slice so we can distinguish absent ("don't change") from
	// explicit empty array ("clear all notifications").
	var in struct {
		Notifications           *[]notifyTargetWire   `json:"notifications"`
		SessionWarningThreshold *int                  `json:"session_warning_threshold,omitempty"`
		GracePeriod             *int                  `json:"grace_period,omitempty"`
		PollInterval            *int                  `json:"poll_interval,omitempty"`
		Performance             *dc.PerformanceConfig `json:"performance,omitempty"`
		EvtSpike                *evtspikeInput        `json:"evtspike,omitempty"`
		Update                  *dc.UpdateConfig      `json:"update,omitempty"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Project the wire targets back into the on-disk type once we've consumed
	// the wire-only flags.
	var notifications *[]dc.NotificationTarget
	if in.Notifications != nil {
		flat := make([]dc.NotificationTarget, len(*in.Notifications))
		for i, w := range *in.Notifications {
			flat[i] = w.NotificationTarget
		}
		notifications = &flat
	}

	// Validate numeric fields before persisting; return 400 (client error) not 500.
	if in.SessionWarningThreshold != nil && (*in.SessionWarningThreshold < 0 || *in.SessionWarningThreshold > 100) {
		http.Error(w, "session_warning_threshold must be 0–100", http.StatusBadRequest)
		return
	}
	if in.GracePeriod != nil && (*in.GracePeriod < 1 || *in.GracePeriod > 1440) {
		http.Error(w, "grace_period must be 1–1440", http.StatusBadRequest)
		return
	}
	if in.PollInterval != nil && (*in.PollInterval < 10 || *in.PollInterval > dc.MaxPollInterval) {
		http.Error(w, fmt.Sprintf("poll_interval must be 10–%d", dc.MaxPollInterval), http.StatusBadRequest)
		return
	}

	// Validate notification targets so invalid entries are rejected with a clear
	// 400 instead of being silently stripped by Config.Validate() after save.
	if notifications != nil {
		for i, t := range *notifications {
			if err := validateNotificationTarget(t, i); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
	}

	if in.Performance != nil {
		if in.Performance.SampleIntervalSec != 0 && (in.Performance.SampleIntervalSec < 10 || in.Performance.SampleIntervalSec > 300) {
			http.Error(w, "sample_interval_sec must be 10–300", http.StatusBadRequest)
			return
		}
		if in.Performance.LoadAlertDelaySec < 0 {
			http.Error(w, "load_alert_delay_sec must be non-negative", http.StatusBadRequest)
			return
		}
		if in.Performance.InputDelayAlertDelaySec < 0 {
			http.Error(w, "input_delay_alert_delay_sec must be non-negative", http.StatusBadRequest)
			return
		}
		if err := validatePerformanceConfig(*in.Performance); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	if in.Update != nil {
		if in.Update.Channel != dc.ChannelStable && in.Update.Channel != dc.ChannelPrerelease {
			http.Error(w, `update.channel must be "stable" or "prerelease"`, http.StatusBadRequest)
			return
		}
		if interval := time.Duration(in.Update.PollInterval); interval < dc.MinUpdatePollInterval {
			http.Error(w, fmt.Sprintf("update.poll_interval must be at least %s", dc.MinUpdatePollInterval), http.StatusBadRequest)
			return
		}
	}

	// Apply the three-mode secret policy. Bulk PUT keys lookups by Type+URL,
	// which is fragile across renames — the per-target endpoints below address
	// this by index instead.
	if notifications != nil {
		existing, err := dc.LoadConfig()
		secretMap := map[string]string{}
		if err == nil {
			for _, t := range existing.Notifications {
				secretMap[t.Type+"\x00"+t.URL] = t.Secret
			}
		}
		for i := range *notifications {
			t := &(*notifications)[i]
			wire := (*in.Notifications)[i] // sibling slot in the wire-only slice
			applySecretPolicy(t, secretMap[t.Type+"\x00"+t.URL], wire.ClearSecret)
		}
	}

	if ds.testPutSettingsFunc != nil {
		if err := ds.testPutSettingsFunc(notifications, in.SessionWarningThreshold, in.GracePeriod, in.PollInterval, in.Performance); err != nil {
			slog.Error("update config failed (test hook)", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		if err := dc.UpdateNotifySettings(notifications, in.SessionWarningThreshold, in.GracePeriod, in.PollInterval, in.Performance); err != nil {
			slog.Error("update settings failed", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	var evtspikeEnabled *bool
	if in.EvtSpike != nil {
		evtspikeEnabled = in.EvtSpike.Enabled
	}
	if ds.testPutEvtSpikeEnabledFunc != nil {
		if err := ds.testPutEvtSpikeEnabledFunc(evtspikeEnabled); err != nil {
			slog.Error("update evtspike enabled failed (test hook)", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		if err := dc.UpdateEvtSpikeEnabled(evtspikeEnabled); err != nil {
			slog.Error("update evtspike enabled failed", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if ds.testPutUpdateConfigFunc != nil {
		if err := ds.testPutUpdateConfigFunc(in.Update); err != nil {
			slog.Error("update automatic-update config failed (test hook)", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		if err := dc.UpdateUpdateConfig(in.Update); err != nil {
			slog.Error("update automatic-update settings failed", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	auth := GetAuthInfo(r)
	user := ""
	if auth != nil {
		user = auth.Username
	}
	slog.Info("dashboard=settings-updated", slog.Int("event_id", etwids.EvtDashboardConfigChange), "user", user)

	// Broadcast settings change to connected browsers.
	ds.broadcastSettingsUpdate()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// notifyTargetsResponse is the body returned by the per-target CRUD endpoints.
// It carries the full updated targets list (with has_secret) so the frontend
// can replace its local copy in one round-trip — no follow-up GET required.
type notifyTargetsResponse struct {
	Notifications []notifyTargetView `json:"notifications"`
}

// updateTargetsAndRespond runs `mutate` under the config lock, writes the
// resulting notifications list, broadcasts the change, and renders the new
// view to the client. mutate may return an error to abort with a 4xx (the
// caller picks the status code by inspecting the error).
func (ds *DashboardServer) updateTargetsAndRespond(w http.ResponseWriter, r *http.Request, mutate func(targets []dc.NotificationTarget) ([]dc.NotificationTarget, error)) {
	var newTargets []dc.NotificationTarget
	err := dc.ReadModifyWriteNotifications(func(targets []dc.NotificationTarget) ([]dc.NotificationTarget, error) {
		out, mErr := mutate(targets)
		if mErr != nil {
			return nil, mErr
		}
		newTargets = out
		return out, nil
	})
	if err != nil {
		// Handler errors carry a wrapped HTTP status via httpStatusError.
		var httpErr *httpStatusError
		if errors.As(err, &httpErr) {
			http.Error(w, httpErr.msg, httpErr.status)
			return
		}
		slog.Error("update notification targets failed", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	auth := GetAuthInfo(r)
	user := ""
	if auth != nil {
		user = auth.Username
	}
	slog.Info("dashboard=settings-updated", slog.Int("event_id", etwids.EvtDashboardConfigChange), "user", user, "scope", "notifications")

	ds.broadcastSettingsUpdate()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(notifyTargetsResponse{Notifications: makeNotifyTargetViews(newTargets)})
}

// httpStatusError lets the per-target CRUD handlers signal a desired HTTP
// status from inside the readModifyWrite callback.
type httpStatusError struct {
	status int
	msg    string
}

func (e *httpStatusError) Error() string { return e.msg }

func badRequest(msg string) error { return &httpStatusError{status: http.StatusBadRequest, msg: msg} }
func notFound(msg string) error   { return &httpStatusError{status: http.StatusNotFound, msg: msg} }

// decodeNotifyTargetWire reads a single notifyTargetWire from the request body,
// rejecting payloads larger than 4 KB. Returns 400-class errors for the caller
// to surface to the client.
func decodeNotifyTargetWire(r *http.Request) (notifyTargetWire, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		return notifyTargetWire{}, badRequest("bad request")
	}
	var w notifyTargetWire
	if err := json.Unmarshal(body, &w); err != nil {
		return notifyTargetWire{}, badRequest("invalid JSON")
	}
	return w, nil
}

// handleAddNotificationTarget appends a new notification target to the saved
// list. The wire-only clear_secret flag is ignored on add — there is no
// existing secret to clear. Returns the full updated targets list.
func (ds *DashboardServer) handleAddNotificationTarget(w http.ResponseWriter, r *http.Request) {
	wire, err := decodeNotifyTargetWire(r)
	if err != nil {
		var httpErr *httpStatusError
		if errors.As(err, &httpErr) {
			http.Error(w, httpErr.msg, httpErr.status)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if vErr := validateNotificationTarget(wire.NotificationTarget, -1); vErr != nil {
		http.Error(w, vErr.Error(), http.StatusBadRequest)
		return
	}

	ds.updateTargetsAndRespond(w, r, func(targets []dc.NotificationTarget) ([]dc.NotificationTarget, error) {
		// clear_secret on add is a no-op (nothing to clear). New secret is taken
		// as-is; empty means no credential.
		return append(targets, wire.NotificationTarget), nil
	})
}

// handleUpdateNotificationTarget replaces the target at the given index with
// the request body. Secrets follow the standard three-mode policy, except the
// existing secret is looked up by INDEX (not Type+URL), so URL renames preserve
// the credential.
func (ds *DashboardServer) handleUpdateNotificationTarget(w http.ResponseWriter, r *http.Request) {
	idx, ok := parseTargetIndex(w, r)
	if !ok {
		return
	}
	wire, err := decodeNotifyTargetWire(r)
	if err != nil {
		var httpErr *httpStatusError
		if errors.As(err, &httpErr) {
			http.Error(w, httpErr.msg, httpErr.status)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if vErr := validateNotificationTarget(wire.NotificationTarget, -1); vErr != nil {
		http.Error(w, vErr.Error(), http.StatusBadRequest)
		return
	}

	ds.updateTargetsAndRespond(w, r, func(targets []dc.NotificationTarget) ([]dc.NotificationTarget, error) {
		if idx < 0 || idx >= len(targets) {
			return nil, notFound(fmt.Sprintf("notification target index %d not found (have %d)", idx, len(targets)))
		}
		updated := wire.NotificationTarget
		applySecretPolicy(&updated, targets[idx].Secret, wire.ClearSecret)
		out := make([]dc.NotificationTarget, len(targets))
		copy(out, targets)
		out[idx] = updated
		return out, nil
	})
}

// handleDeleteNotificationTarget removes the target at the given index.
func (ds *DashboardServer) handleDeleteNotificationTarget(w http.ResponseWriter, r *http.Request) {
	idx, ok := parseTargetIndex(w, r)
	if !ok {
		return
	}

	ds.updateTargetsAndRespond(w, r, func(targets []dc.NotificationTarget) ([]dc.NotificationTarget, error) {
		if idx < 0 || idx >= len(targets) {
			return nil, notFound(fmt.Sprintf("notification target index %d not found (have %d)", idx, len(targets)))
		}
		out := make([]dc.NotificationTarget, 0, len(targets)-1)
		out = append(out, targets[:idx]...)
		out = append(out, targets[idx+1:]...)
		return out, nil
	})
}

// parseTargetIndex extracts and validates the {idx} path parameter. Writes a
// 400 response and returns ok=false on failure.
func parseTargetIndex(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.PathValue("idx")
	idx, err := strconv.Atoi(raw)
	if err != nil || idx < 0 {
		http.Error(w, "idx must be a non-negative integer", http.StatusBadRequest)
		return 0, false
	}
	return idx, true
}

// handleNotifyTest sends a test notification.
// If the request body contains a JSON-encoded NotificationTarget, only that
// single target is tested (used by the target edit modal). Otherwise, all
// currently saved targets are tested (used by the config modal "Send Test").
func (ds *DashboardServer) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	auth := GetAuthInfo(r)
	user := ""
	if auth != nil {
		user = auth.Username
	}

	// Try to decode a single target from the body.
	// Webhook/ntfy targets are identified by a non-empty URL; email targets
	// have no URL (they use From/To instead) so we check Type == "email".
	var singleTarget *dc.NotificationTarget
	if r.Body != nil && r.ContentLength > 0 {
		var t dc.NotificationTarget
		if err := json.NewDecoder(r.Body).Decode(&t); err == nil && t.Type != "" && (t.URL != "" || t.Type == "email") {
			singleTarget = &t
		}
	}

	// Secrets are write-only — the browser never has them. If the secret
	// field is empty, look up the saved secret so the test uses real credentials.
	if singleTarget != nil && singleTarget.Secret == "" {
		if cfg, loadErr := dc.LoadConfig(); loadErr == nil {
			for _, saved := range cfg.Notifications {
				if saved.Type == singleTarget.Type && saved.URL == singleTarget.URL {
					singleTarget.Secret = saved.Secret
					break
				}
			}
		}
	}

	var (
		results []dc.TestNotificationResult
		err     error
	)
	if singleTarget != nil {
		slog.Info("dashboard=notify-test-target", "user", user, "type", singleTarget.Type, "url", singleTarget.URL)
		results, err = dc.SendTestNotification([]dc.NotificationTarget{*singleTarget})
	} else {
		slog.Info("dashboard=notify-test", "user", user)
		// testNotifyFunc can be injected in tests to avoid real config/network I/O.
		fn := ds.testNotifyFunc
		if fn == nil {
			fn = func() ([]dc.TestNotificationResult, error) {
				loadFn := ds.testLoadConfigFunc
				if loadFn == nil {
					loadFn = func() (*dc.Config, error) { return dc.LoadConfig() }
				}
				cfg, loadErr := loadFn()
				if loadErr != nil {
					return nil, fmt.Errorf("failed to load config: %w", loadErr)
				}
				return dc.SendTestNotification(cfg.Notifications)
			}
		}
		results, err = fn()
	}

	// Always return the per-target results (UI uses them to show which target
	// failed). HTTP status is 200 if everything succeeded, 207 (Multi-Status)
	// if some targets failed but at least one succeeded, 400 if all failed or
	// the request was malformed.
	w.Header().Set("Content-Type", "application/json")
	successes := 0
	for _, r := range results {
		if r.OK {
			successes++
		}
	}

	body := map[string]any{
		"ok":      err == nil,
		"results": results,
	}
	if err != nil {
		body["error"] = err.Error()
	}
	status := http.StatusOK
	if err != nil {
		if successes == 0 {
			status = http.StatusBadRequest
		} else {
			status = http.StatusMultiStatus
		}
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
