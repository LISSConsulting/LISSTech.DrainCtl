//go:build windows

package dashboard

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/etwids"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessionlimit"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// hostnameRE matches RFC 1123 hostnames: labels of alphanumerics and hyphens
// (hyphen not at start/end), separated by dots.
var hostnameRE = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)*$`)

// ServerView is the flattened JSON shape returned to the Svelte dashboard
// frontend from GET /api/v1/servers and GET /api/v1/servers/{host}.
// It unwraps ServerInfo + CheckResult so the frontend never navigates nested
// last_result fields. Status is normalised to lowercase frontend tokens.
type ServerView struct {
	Host                 string           `json:"host"`
	Status               string           `json:"status"` // "ok"/"grace"/"alert"/"off"
	DrainMode            string           `json:"drain_mode"`
	Sessions             int              `json:"sessions"`              // TotalSessions; 0 when unknown
	SessionsActive       int              `json:"sessions_active"`       // connected sessions; 0 when unknown
	SessionsDisconnected int              `json:"sessions_disconnected"` // disconnected sessions; 0 when unknown
	MaxSessions          int              `json:"max_sessions"`          // server capacity; 0 when unknown
	StateDurationSeconds *float64         `json:"state_duration_seconds"`
	StateChangedAt       *time.Time       `json:"state_changed_at,omitempty"` // when the current state began
	Version              string           `json:"version"`
	RegisteredAt         time.Time        `json:"registered_at"`
	LastSeen             time.Time        `json:"last_seen,omitempty"`
	ChangedBy            string           `json:"changed_by,omitempty"`
	GraceDeadline        *time.Time       `json:"grace_deadline"`
	Perf                 *dc.PerfSnapshot `json:"perf"`
}

// statusToken converts a CheckResult.Status value ("Healthy"/"Warning"/"Grace"/"Alert")
// to the lowercase frontend token ("ok"/"warning"/"grace"/"alert"/"off").
func statusToken(status string) string {
	switch status {
	case "Healthy":
		return "ok"
	case "Warning":
		return "warning"
	case "Grace":
		return "grace"
	case "Alert":
		return "alert"
	default:
		return "off"
	}
}

// toServerView converts a stored ServerInfo into the flattened ServerView
// the dashboard frontend expects.
func toServerView(info ServerInfo, staleAfter time.Duration) ServerView {
	v := ServerView{
		Host:         info.Hostname,
		Status:       "off",
		RegisteredAt: info.RegisteredAt,
		LastSeen:     info.LastSeen,
	}
	r := info.LastResult
	if r == nil {
		return v
	}
	if r.Host != "" {
		v.Host = r.Host
	}
	if !info.LastSeen.IsZero() && time.Since(info.LastSeen) >= staleAfter {
		v.Status = "off"
	} else {
		v.Status = statusToken(r.Status)
	}
	v.DrainMode = r.DrainModeLabel
	v.Version = r.Version
	v.ChangedBy = r.ChangedBy
	v.Perf = r.Performance
	v.StateDurationSeconds = r.StateDurationSeconds
	v.StateChangedAt = r.StateSince
	if r.Sessions != nil {
		v.Sessions = r.Sessions.TotalSessions
		v.SessionsActive = r.Sessions.ActiveSessions
		v.SessionsDisconnected = r.Sessions.DisconnectedSessions
		v.MaxSessions = r.Sessions.MaxSessions
	}
	// GraceDeadline: the moment when the grace window expires.
	// Computed from StateSince + GracePeriodSeconds so the frontend can display
	// a live countdown without knowing the raw grace period configuration.
	if r.Status == "Grace" && r.StateSince != nil && r.GracePeriodSeconds > 0 {
		deadline := r.StateSince.Add(time.Duration(r.GracePeriodSeconds) * time.Second)
		v.GraceDeadline = &deadline
	}
	return v
}

// isAuthorizedForHost checks whether the authenticated identity is allowed to
// register or report for the given hostname.
//   - Machine accounts ("DOMAIN\HOST$") can only act for their own hostname.
//   - Members of the dashboard admin group can act for any host.
//   - All other identities are rejected.
func isAuthorizedForHost(auth *AuthInfo, hostname, adminGroup string) bool {
	if auth == nil {
		return false
	}
	user := auth.Username
	if strings.HasSuffix(user, "$") {
		machineHost := user[:len(user)-1]
		if idx := strings.LastIndex(machineHost, `\`); idx >= 0 {
			machineHost = machineHost[idx+1:]
		}
		return strings.EqualFold(machineHost, hostname)
	}
	return isDashboardAdmin(auth, adminGroup)
}

func isDashboardAdmin(auth *AuthInfo, adminGroup string) bool {
	if auth == nil {
		return false
	}
	for _, g := range auth.Groups {
		if strings.EqualFold(g, adminGroup) {
			return true
		}
		parts := strings.SplitN(g, `\`, 2)
		if len(parts) == 2 && strings.EqualFold(parts[1], adminGroup) {
			return true
		}
	}
	return false
}

// handleRegister processes POST /api/v1/register.
// Expects JSON: {"hostname":"..."}
func (ds *DashboardServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hostname string `json:"hostname"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Hostname == "" {
		http.Error(w, "hostname required", http.StatusBadRequest)
		return
	}
	req.Hostname = telemetry.CanonicalHostname(req.Hostname)
	if req.Hostname == "" || len(req.Hostname) > 253 || !hostnameRE.MatchString(req.Hostname) {
		http.Error(w, "hostname invalid", http.StatusBadRequest)
		return
	}

	// Durable tombstone gate: refuse automatic re-registration of a
	// permanently-removed host. The operator must POST
	// /api/v1/servers/{host}/restore first. Returning 410 Gone (rather than
	// 403) signals the resource is permanently unavailable; agents that
	// understand 410 stop retrying and surface an actionable error to the
	// user, while agents that don't treat it as a hard reject.
	if ds.state.IsExcluded(req.Hostname) {
		slog.Info("dashboard: register refused — host is permanently removed",
			slog.Int("event_id", etwids.EvtAccessDenied),
			"host", req.Hostname,
		)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Permanently-Removed", "1")
		w.WriteHeader(http.StatusGone)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":  "host permanently removed",
			"host":   req.Hostname,
			"action": "restore via POST /api/v1/servers/{host}/restore",
		})
		return
	}

	// Verify the authenticated identity matches the claimed hostname.
	// Machine accounts are "DOMAIN\HOST$" — the hostname must match.
	// Domain admins (non-machine accounts) can register any host.
	auth := GetAuthInfo(r)
	user := ""
	if auth != nil {
		user = auth.Username
		if !isAuthorizedForHost(auth, req.Hostname, ds.cfg.Group) && !isLocalSystemForHost(r, auth, req.Hostname) {
			slog.Warn("dashboard: register rejected: identity mismatch",
				slog.Int("event_id", etwids.EvtAccessDenied), "user", user, "claimed_host", req.Hostname)
			http.Error(w, "identity does not match claimed hostname", http.StatusForbidden)
			return
		}
	}

	switch ds.state.Register(req.Hostname) {
	case registrationAccepted:
		slog.Info("dashboard=register", slog.Int("event_id", etwids.EvtServerRegistered), "host", req.Hostname, "user", user)
		ds.broadcastServerUpdate(req.Hostname)
	case registrationExcluded:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Permanently-Removed", "1")
		w.WriteHeader(http.StatusGone)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":  "host permanently removed",
			"host":   req.Hostname,
			"action": "restore via POST /api/v1/servers/{host}/restore",
		})
		return
	default:
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	resp := struct {
		OK             bool   `json:"ok"`
		TLSFingerprint string `json:"tls_fingerprint,omitempty"`
	}{OK: true, TLSFingerprint: ds.fingerprint}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// handleReport processes POST /api/v1/report.
// Expects JSON CheckResult. Rejects unregistered hosts with 403.
func (ds *DashboardServer) handleReport(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 65536))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	var result dc.CheckResult
	if err := json.Unmarshal(body, &result); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	result.Host = telemetry.CanonicalHostname(result.Host)
	if result.Host == "" {
		http.Error(w, "host field required", http.StatusBadRequest)
		return
	}

	if !ds.state.IsRegistered(result.Host) {
		http.Error(w, "host not registered", http.StatusForbidden)
		return
	}

	// Durable tombstone gate: refuse /report from agents on hosts that have
	// been permanently removed. Without this gate, a slow agent that was
	// sending reports before its host got removed could land a final report
	// AFTER the operator's permanent-remove, which would silently resurrect
	// the cached state. With it, the last-mile report rejection matches the
	// permanent-removal contract end-to-end.
	if ds.state.IsExcluded(result.Host) {
		slog.Info("dashboard: report refused — host is permanently removed",
			slog.Int("event_id", etwids.EvtAccessDenied),
			"host", result.Host,
		)
		w.Header().Set("X-Permanently-Removed", "1")
		w.WriteHeader(http.StatusGone)
		return
	}

	auth := GetAuthInfo(r)
	if auth != nil && !isAuthorizedForHost(auth, result.Host, ds.cfg.Group) && !isLocalSystemForHost(r, auth, result.Host) {
		slog.Warn("dashboard: report rejected: identity mismatch",
			slog.Int("event_id", etwids.EvtAccessDenied), "user", auth.Username, "claimed_host", result.Host)
		http.Error(w, "identity does not match claimed hostname", http.StatusForbidden)
		return
	}

	if result.Sessions != nil {
		if limit, ok := sessionlimit.Normalize(uint64(result.Sessions.MaxSessions)); ok {
			result.Sessions.MaxSessions = limit
		} else {
			result.Sessions.MaxSessions = 0
			result.Sessions.UtilizationPct = 0
		}
	}

	// Completion-only reports deliberately omit CheckResult fields. Do not
	// replace the host's last good heartbeat with that sparse envelope.
	completion, hasCompletion := extractForceUpdateCompletion(body)
	if !hasCompletion || result.Version != "" {
		ds.state.Update(result.Host, &result)

		// Propagate the remote agent's evtspike detector status. The broker dedups
		// by (host, state) so calling unconditionally every heartbeat is safe and
		// keeps the wire quiet on steady state. The cache backs the pull function
		// registered for non-local hosts in internal/svc/handler.go.
		if result.EvtSpikeStatus != nil {
			status := *result.EvtSpikeStatus
			status.Host = result.Host
			ds.remoteEvtSpikeStatusMu.Lock()
			ds.remoteEvtSpikeStatus[result.Host] = status
			ds.remoteEvtSpikeStatusMu.Unlock()
			ds.broker.PublishDetectorStatus(status)
		}
	}

	// Attach any queued force-update command to the report response. The
	// agent consumes the pending_command field on its side and runs the
	// updater immediately. Older agents (pre-26.9.17) that don't know the
	// field ignore the extra JSON member; rolling upgrade is safe in either
	// direction.
	//
	// Completion reports flow the opposite way: the agent posts its
	// updater decision as a top-level `force_update` field on the report
	// body (a separate wire envelope from CheckResult so we don't have to
	// modify the drainctl package). We re-parse the raw body here to
	// extract it; the result struct above already absorbed it (and ignored
	// it via Go's default zero value).
	if hasCompletion {
		ds.forceUpdates.Acknowledge(strings.ToLower(result.Host), completion.CommandID)
		ds.broadcastForceUpdateCompletion(result.Host, completion)
	}

	type reportResponse struct {
		OK             bool                `json:"ok"`
		PendingCommand *forceUpdateCommand `json:"pending_command,omitempty"`
	}
	resp := reportResponse{OK: true}
	if cmd := ds.consumePendingCommand(strings.ToLower(result.Host)); cmd != nil {
		resp.PendingCommand = cmd
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// extractForceUpdateCompletion parses the report body for an optional
// top-level `force_update` field. The field is a wire-side envelope
// distinct from CheckResult so adding it does not require changes to
// the drainctl package — agents that don't know about force-update
// simply omit the field, and we return ok=false.
//
// Returns ok=false for absent or malformed fields; the dashboard treats
// both as "no completion report this round" and continues with the
// normal report handling. Callers that need to differentiate "agent
// crashed mid-decision" from "agent succeeded" can rely on the next
// heartbeat's CheckResult.Status field.
func extractForceUpdateCompletion(body []byte) (ForceUpdateCompletionPayload, bool) {
	if len(body) == 0 {
		return ForceUpdateCompletionPayload{}, false
	}
	var wire struct {
		Host        string `json:"host"`
		ForceUpdate *struct {
			CommandID  string    `json:"command_id"`
			Outcome    string    `json:"outcome"`
			Reason     string    `json:"reason,omitempty"`
			OldVersion string    `json:"old_version,omitempty"`
			NewVersion string    `json:"new_version,omitempty"`
			DecidedAt  time.Time `json:"decided_at,omitempty"`
		} `json:"force_update,omitempty"`
	}
	if err := json.Unmarshal(body, &wire); err != nil || wire.ForceUpdate == nil {
		return ForceUpdateCompletionPayload{}, false
	}
	fu := wire.ForceUpdate
	if fu.CommandID == "" {
		return ForceUpdateCompletionPayload{}, false
	}
	out := ForceUpdateCompletionPayload{
		Host:       wire.Host,
		CommandID:  fu.CommandID,
		Outcome:    fu.Outcome,
		Reason:     fu.Reason,
		OldVersion: fu.OldVersion,
		NewVersion: fu.NewVersion,
	}
	if !fu.DecidedAt.IsZero() {
		out.DecidedAt = fu.DecidedAt.UTC().Format(time.RFC3339Nano)
	}
	return out, true
}

// RemoteEvtSpikeStatus returns the DetectorStatus most recently reported for
// the given host via /api/v1/report. Zero value (State="") for unknown hosts;
// callers translate that into the disabled state. Safe for concurrent use.
func (ds *DashboardServer) RemoteEvtSpikeStatus(host string) evtspike.DetectorStatus {
	ds.remoteEvtSpikeStatusMu.RLock()
	defer ds.remoteEvtSpikeStatusMu.RUnlock()
	return ds.remoteEvtSpikeStatus[host]
}

// handleServers returns GET /api/v1/servers as a JSON array of ServerView.
func (ds *DashboardServer) handleServers(w http.ResponseWriter, r *http.Request) {
	infos := ds.state.All()
	views := make([]ServerView, len(infos))
	for i, info := range infos {
		views[i] = toServerView(info, ds.staleAfter())
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(views)
}

// handleGetServer returns GET /api/v1/servers/{host} as a JSON ServerView.
// Returns 404 if the host is not registered.
func (ds *DashboardServer) handleGetServer(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	if host == "" {
		http.Error(w, "host parameter required", http.StatusBadRequest)
		return
	}

	info := ds.state.Get(host)
	if info == nil {
		http.Error(w, "host not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(toServerView(*info, ds.staleAfter()))
}

// handleDeleteServer processes DELETE /api/v1/servers/{host}.
func (ds *DashboardServer) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	if host == "" {
		http.Error(w, "host parameter required", http.StatusBadRequest)
		return
	}

	if !ds.state.Remove(host) {
		http.Error(w, "host not found", http.StatusNotFound)
		return
	}

	auth := GetAuthInfo(r)
	user := ""
	if auth != nil {
		user = auth.Username
	}
	slog.Info("dashboard=removed", slog.Int("event_id", etwids.EvtServerRemoved), "host", host, "user", user) //nolint:gosec // host is validated by the router pattern
	ds.broadcastServerDeleted(host, user)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// RemovedServer is the JSON shape returned by GET /api/v1/servers/removed
// and embedded inside server_permanently_removed / server_restored SSE event
// data. Field names are the dashboard wire contract — do not change without
// a coordinated frontend update.
type RemovedServer struct {
	Host       string    `json:"host"`
	RemovedAt  time.Time `json:"removed_at"`
	RemovedBy  string    `json:"removed_by,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	Permanent  bool      `json:"permanent"`
	WasPresent bool      `json:"was_present"` // latest known liveness at removal time
}

// toRemovedServer lifts a telemetry.RemovalEntry into the JSON shape the
// dashboard renders.
func toRemovedServer(e telemetry.RemovalEntry) RemovedServer {
	return RemovedServer{
		Host:      e.Hostname,
		RemovedAt: e.ExcludedAt,
		RemovedBy: e.ExcludedBy,
		Reason:    e.Reason,
		Permanent: true,
	}
}

// permanentRemoveActor extracts a stable identifier for the dashboard user
// performing the removal — admin-group memberships, dashboard username, or
// the TLS machine account. Falls back to empty string for unauthenticated
// internal callers (tests, dev mode).
func permanentRemoveActor(r *http.Request) string {
	auth := GetAuthInfo(r)
	if auth == nil {
		return ""
	}
	return auth.Username
}

// handlePermanentRemoveServer processes POST /api/v1/servers/{host}/permanent-remove.
//
// Marks hostname as permanently removed: deletes the live `servers` row and
// inserts a durable tombstone in server_exclusions. Both POST /register and
// POST /report refuse the host until an operator issues a restore.
//
// Idempotent: a re-marked tombstone returns 200 with the same body and the
// latest metadata; never 4xx on a repeat call.
//
// Errors:
//
//	400 invalid hostname
//	401 not authenticated
//	403 caller not in admin group
//	404 host was never registered (operators should DELETE transient ghosts first;
//	    silently tombstoning unknown hosts would hide typos in batch flows)
//	500 storage failure
func (ds *DashboardServer) handlePermanentRemoveServer(w http.ResponseWriter, r *http.Request) {
	host := telemetry.CanonicalHostname(r.PathValue("host"))
	if host == "" || len(host) > 253 || !hostnameRE.MatchString(host) {
		http.Error(w, "host invalid", http.StatusBadRequest)
		return
	}
	if auth := GetAuthInfo(r); auth == nil {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	} else if !isDashboardAdmin(auth, ds.cfg.Group) {
		http.Error(w, "admin group required", http.StatusForbidden)
		return
	}

	// Verify the host actually exists in the live roster. We refuse to
	// tombstone unknown hosts so a typo doesn't silently blacklist a
	// legitimate future hostname. Operators can call /servers/removed to
	// re-mark an existing tombstone (idempotent).
	if !ds.state.IsRegistered(host) {
		// Allow re-marking an existing tombstone without 404.
		if !ds.state.IsExcluded(host) {
			http.Error(w, "host not registered", http.StatusNotFound)
			return
		}
	}

	var body struct {
		Reason string `json:"reason,omitempty"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 4096))
		if err == nil && len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				http.Error(w, "invalid JSON body", http.StatusBadRequest)
				return
			}
		}
	}
	body.Reason = strings.TrimSpace(body.Reason)

	by := permanentRemoveActor(r)

	removedAt, err := ds.state.PermanentRemove(host, by, body.Reason)
	if err != nil {
		slog.Error("dashboard: permanent remove failed", "host", host, "user", by, "error", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	slog.Info("dashboard=permanently_removed",
		slog.Int("event_id", etwids.EvtServerRemoved),
		"host", host,
		"user", by,
		"reason", body.Reason,
	)
	ds.broadcastServerDeleted(host, by) // legacy transient signal so any cached row drops
	ds.broadcastServerPermanentlyRemoved(host, by, body.Reason, removedAt)

	resp := struct {
		OK        bool      `json:"ok"`
		Host      string    `json:"host"`
		Permanent bool      `json:"permanent"`
		RemovedAt time.Time `json:"removed_at"`
		RemovedBy string    `json:"removed_by,omitempty"`
	}{
		OK:        true,
		Host:      host,
		Permanent: true,
		RemovedAt: removedAt,
		RemovedBy: by,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// permanentRemoveBatchResult is the JSON shape returned by POST
// /api/v1/servers/permanent-remove. The top-level status is always 200
// once the handler ran; per-host failures live in `errors`.
type permanentRemoveBatchResult struct {
	Removed []string                 `json:"removed"`
	Skipped []string                 `json:"skipped"` // unknown hosts (404-equivalent)
	Errors  []permanentRemoveFailure `json:"errors"`
}

type permanentRemoveFailure struct {
	Host   string `json:"host"`
	Status int    `json:"status"`
	Reason string `json:"reason"`
}

// handlePermanentRemoveBatch processes POST /api/v1/servers/permanent-remove.
// Body: {"hosts": ["HOST01","HOST02"], "reason": "..."}
//
// Each host is its own atomic mutation; there is no transactional rollback
// across hosts. Idempotent: a tombstoned host included in a repeat call is
// listed under `removed` again, refreshed with the latest operator action.
// Unknown hosts land in `skipped`. Validation failures and storage errors
// are reported in `errors` so the caller (BatchOperations' UI) can show
// per-row status.
func (ds *DashboardServer) handlePermanentRemoveBatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Hosts  []string `json:"hosts"`
		Reason string   `json:"reason,omitempty"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	body.Reason = strings.TrimSpace(body.Reason)
	if auth := GetAuthInfo(r); auth == nil {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	} else if !isDashboardAdmin(auth, ds.cfg.Group) {
		http.Error(w, "admin group required", http.StatusForbidden)
		return
	}

	result := permanentRemoveBatchResult{
		Removed: []string{},
		Skipped: []string{},
		Errors:  []permanentRemoveFailure{},
	}
	for _, host := range body.Hosts {
		host = telemetry.CanonicalHostname(host)
		if host == "" || len(host) > 253 || !hostnameRE.MatchString(host) {
			result.Errors = append(result.Errors, permanentRemoveFailure{
				Host:   host,
				Status: http.StatusBadRequest,
				Reason: "host invalid",
			})
			continue
		}
		registered := ds.state.IsRegistered(host)
		excluded := ds.state.IsExcluded(host)
		if !registered && !excluded {
			result.Skipped = append(result.Skipped, host)
			continue
		}
		by := permanentRemoveActor(r)
		removedAt, err := ds.state.PermanentRemove(host, by, body.Reason)
		if err != nil {
			result.Errors = append(result.Errors, permanentRemoveFailure{
				Host:   host,
				Status: http.StatusInternalServerError,
				Reason: err.Error(),
			})
			continue
		}
		result.Removed = append(result.Removed, host)
		slog.Info("dashboard=permanently_removed",
			slog.Int("event_id", etwids.EvtServerRemoved),
			"host", host,
			"user", by,
			"reason", body.Reason,
		)
		ds.broadcastServerDeleted(host, by)
		ds.broadcastServerPermanentlyRemoved(host, by, body.Reason, removedAt)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(result)
}

// handleListRemovedServers processes GET /api/v1/servers/removed.
//
// Returns every permanently-removed server, newest first. Empty array when
// no hosts have been removed. The dashboard renders this list in a dedicated
// "Removed Servers" panel; operators use it to confirm a removal succeeded
// and to discover hosts eligible for restore.
//
// Errors:
//
//	500 storage failure.
func (ds *DashboardServer) handleListRemovedServers(w http.ResponseWriter, r *http.Request) {
	entries, err := ds.state.AllRemoved()
	if err != nil {
		slog.Error("dashboard: list removed failed", "error", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	out := make([]RemovedServer, len(entries))
	for i, e := range entries {
		out[i] = toRemovedServer(e)
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

// handleRestoreServer processes POST /api/v1/servers/{host}/restore.
//
// Deletes the tombstone for hostname so subsequent /register and /report
// calls are accepted. The live `servers` row is NOT auto-recreated — the
// agent's next /register creates a fresh row with the current timestamp.
//
// Errors:
//
//	400 invalid hostname
//	401 not authenticated
//	403 caller not in admin group
//	404 host is not currently tombstoned
//	500 storage failure
func (ds *DashboardServer) handleRestoreServer(w http.ResponseWriter, r *http.Request) {
	host := telemetry.CanonicalHostname(r.PathValue("host"))
	if host == "" || len(host) > 253 || !hostnameRE.MatchString(host) {
		http.Error(w, "host invalid", http.StatusBadRequest)
		return
	}
	if auth := GetAuthInfo(r); auth == nil {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	} else if !isDashboardAdmin(auth, ds.cfg.Group) {
		http.Error(w, "admin group required", http.StatusForbidden)
		return
	}

	if ds.state.IsRegistered(host) {
		http.Error(w, "host is already live — nothing to restore", http.StatusBadRequest)
		return
	}

	by := permanentRemoveActor(r)
	found, restoredAt, err := ds.state.RestoreRemoved(host)
	if err != nil {
		slog.Error("dashboard: restore failed", "host", host, "user", by, "error", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "host is not currently removed", http.StatusNotFound)
		return
	}

	slog.Info("dashboard=restored", slog.Int("event_id", etwids.EvtServerRegistered), "host", host, "user", by)
	ds.broadcastServerRestored(host, by, restoredAt)

	resp := struct {
		OK            bool      `json:"ok"`
		Host          string    `json:"host"`
		RestoredAt    time.Time `json:"restored_at"`
		RestoredBy    string    `json:"restored_by,omitempty"`
		WasTombstoned bool      `json:"was_tombstoned"`
	}{
		OK:            true,
		Host:          host,
		RestoredAt:    restoredAt,
		RestoredBy:    by,
		WasTombstoned: true,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// restoreBatchResult is the JSON shape returned by POST /api/v1/servers/restore.
type restoreBatchResult struct {
	Restored []string           `json:"restored"`
	Skipped  []string           `json:"skipped"` // already-live or un-tombstoned hosts
	Errors   []restoreBatchFail `json:"errors"`
}

type restoreBatchFail struct {
	Host   string `json:"host"`
	Status int    `json:"status"`
	Reason string `json:"reason"`
}

// handleRestoreBatch processes POST /api/v1/servers/restore.
// Body: {"hosts": ["HOST01","HOST02"]}
//
// Idempotent on live hosts (Skipped, not Errors). Non-tombstoned hosts are
// also Skipped — restores ONLY remove tombstones; they do not create new
// `servers` rows.
func (ds *DashboardServer) handleRestoreBatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Hosts []string `json:"hosts"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if auth := GetAuthInfo(r); auth == nil {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	} else if !isDashboardAdmin(auth, ds.cfg.Group) {
		http.Error(w, "admin group required", http.StatusForbidden)
		return
	}

	result := restoreBatchResult{
		Restored: []string{},
		Skipped:  []string{},
		Errors:   []restoreBatchFail{},
	}
	for _, host := range body.Hosts {
		host = telemetry.CanonicalHostname(host)
		if host == "" || len(host) > 253 || !hostnameRE.MatchString(host) {
			result.Errors = append(result.Errors, restoreBatchFail{
				Host: host, Status: http.StatusBadRequest, Reason: "host invalid",
			})
			continue
		}
		// Already live → skip cleanly. We don't error because a batch
		// restore that incidentally re-encounters an already-live host is
		// not a user mistake; it's a successful idempotent operation.
		if ds.state.IsRegistered(host) {
			result.Skipped = append(result.Skipped, host)
			continue
		}
		by := permanentRemoveActor(r)
		found, restoredAt, err := ds.state.RestoreRemoved(host)
		if err != nil {
			result.Errors = append(result.Errors, restoreBatchFail{
				Host: host, Status: http.StatusInternalServerError, Reason: err.Error(),
			})
			continue
		}
		if !found {
			result.Skipped = append(result.Skipped, host)
			continue
		}
		result.Restored = append(result.Restored, host)
		slog.Info("dashboard=restored", slog.Int("event_id", etwids.EvtServerRegistered), "host", host, "user", by)
		ds.broadcastServerRestored(host, by, restoredAt)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(result)
}
