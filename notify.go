//go:build windows

package drainctl

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// NotifyState tracks per-target notification timing for repeat intervals.
type NotifyState struct {
	LastAlertNotify       map[string]time.Time             // key = target URL (alert trigger)
	LastSessionWarnNotify map[string]time.Time             // key = target URL (session_warning trigger)
	LastPerfNotify        map[string]map[Trigger]time.Time // key = target URL -> trigger -> last sent
}

var httpClient = &http.Client{Timeout: 5 * time.Second}

// SendNotification evaluates whether a notification should fire for the
// given trigger and dispatches to matching targets. Errors are logged but
// never returned — notifications must not crash the service.
func SendNotification(targets []NotificationTarget, state *NotifyState, result *CheckResult, trigger Trigger, changedBy string) {
	if len(targets) == 0 {
		return
	}
	if state.LastAlertNotify == nil {
		state.LastAlertNotify = make(map[string]time.Time)
	}
	if state.LastSessionWarnNotify == nil {
		state.LastSessionWarnNotify = make(map[string]time.Time)
	}
	if state.LastPerfNotify == nil {
		state.LastPerfNotify = make(map[string]map[Trigger]time.Time)
	}

	// Reset alert tracking when returning to healthy.
	// Session-warning tracking is intentionally preserved so that a session_warning
	// notification is not re-fired immediately if sessions are still at high
	// utilization when drain mode turns off.
	if trigger == TriggerHealthy || trigger == TriggerDrainOff {
		clear(state.LastAlertNotify)
	}

	// Build payload.
	stateDur := 0.0
	if result.StateDurationSeconds != nil {
		stateDur = *result.StateDurationSeconds
	}

	connAllowed := result.ConnectionsAllowed != nil && *result.ConnectionsAllowed
	payload := map[string]any{
		"event":                  string(trigger),
		"host":                   result.Host,
		"drain_mode":             result.DrainModeLabel,
		"status":                 result.Status,
		"message":                result.Message,
		"changed_by":             changedBy,
		"state_duration_seconds": int(stateDur),
		"grace_period_seconds":   result.GracePeriodSeconds,
		"connections_allowed":    connAllowed,
		"version":                result.Version,
		"timestamp":              result.Timestamp.Format(time.RFC3339),
		"subject":                NotificationSubject(result, trigger, changedBy),
	}
	if result.Transition && result.TransitionFrom != "" {
		payload["previous_mode"] = result.TransitionFrom
	}
	if result.Sessions != nil {
		payload["sessions"] = result.Sessions
	}
	if result.Performance != nil {
		payload["performance"] = result.Performance
	}

	for _, target := range targets {
		// Skip disabled targets (nil Enabled means enabled by default).
		if target.Enabled != nil && !*target.Enabled {
			continue
		}
		if target.URL == "" || !target.HasTrigger(trigger) {
			continue
		}

		// For repeating triggers (alert, session_warning, perf triggers), check
		// per-target repeat interval.
		if isRepeatTrigger(trigger) {
			now := time.Now()
			repeatInterval := time.Duration(target.RepeatMinutes) * time.Minute
			lastSent := getLastSent(state, target.URL, trigger)

			if repeatInterval > 0 {
				if !lastSent.IsZero() && now.Sub(lastSent) < repeatInterval {
					continue
				}
			} else {
				if !lastSent.IsZero() {
					continue
				}
			}
			setLastSent(state, target.URL, trigger, now)
		}

		// Dispatch to backend.
		switch target.Type {
		case "webhook":
			if err := sendWebhook(target.URL, target.Secret, payload); err != nil {
				slog.Warn("webhook notification failed", "error", err, "url", target.URL)
			} else {
				slog.Info("", "notify", "webhook", "event", string(trigger), "url", target.URL)
			}

		case "ntfy":
			title := NotificationSubject(result, trigger, changedBy)
			priority, tags := ntfyStyle(trigger)
			ntfyMsg := result.Message
			if trigger == TriggerSessionWarning && result.Sessions != nil {
				sess := result.Sessions
				ntfyMsg = fmt.Sprintf("Session utilization at %d%% (%d/%d sessions).",
					sess.UtilizationPct, sess.TotalSessions, sess.MaxSessions)
			}
			if err := sendNtfy(target.URL, title, ntfyMsg, priority, tags); err != nil {
				slog.Warn("ntfy notification failed", "error", err, "url", target.URL)
			} else {
				slog.Info("", "notify", "ntfy", "event", string(trigger), "url", target.URL)
			}

		case "email":
			if err := sendEmail(target, result, trigger, changedBy); err != nil {
				slog.Warn("email notification failed", "error", err, "url", target.URL)
			} else {
				slog.Info("", "notify", "email", "event", string(trigger), "to", target.To)
			}
		}
	}
}

// SendTestNotification sends a test message to all configured targets.
// Disabled targets (Enabled == false) are skipped.
func SendTestNotification(targets []NotificationTarget) error {
	hasTargets := false
	for _, t := range targets {
		if t.Enabled != nil && !*t.Enabled {
			continue
		}
		if t.URL != "" || t.Type == "email" {
			hasTargets = true
			break
		}
	}
	if !hasTargets {
		return fmt.Errorf("no notification targets configured")
	}

	host, _ := os.Hostname()
	if host == "" {
		host = "UNKNOWN"
	}

	payload := map[string]any{
		"event":                  "test",
		"host":                   host,
		"drain_mode":             "N/A",
		"status":                 "Test",
		"message":                "This is a test notification from DrainCtl.",
		"changed_by":             "",
		"state_duration_seconds": 0,
		"grace_period_seconds":   0,
		"connections_allowed":    true,
		"version":                Version,
		"timestamp":              time.Now().Format(time.RFC3339),
	}

	var errs []error

	for _, target := range targets {
		if target.Enabled != nil && !*target.Enabled {
			continue
		}
		if target.URL == "" && target.Type != "email" {
			continue
		}

		switch target.Type {
		case "webhook":
			if err := sendWebhook(target.URL, target.Secret, payload); err != nil {
				slog.Error("webhook test failed", "error", err, "url", target.URL)
				errs = append(errs, fmt.Errorf("webhook %s: %w", target.URL, err))
			} else {
				slog.Info("notify=webhook test=sent", "url", target.URL)
			}

		case "ntfy":
			title := fmt.Sprintf("DrainCtl Test: %s", host)
			msg := "This is a test notification from DrainCtl."
			if err := sendNtfy(target.URL, title, msg, "default", "test_tube"); err != nil {
				slog.Error("ntfy test failed", "error", err, "url", target.URL)
				errs = append(errs, fmt.Errorf("ntfy %s: %w", target.URL, err))
			} else {
				slog.Info("notify=ntfy test=sent", "url", target.URL)
			}

		case "email":
			testResult := &CheckResult{
				Host:           host,
				Status:         "Test",
				DrainModeLabel: "N/A",
				Timestamp:      time.Now(),
				Message:        "This is a test notification from DrainCtl.",
				Version:        Version,
			}
			if err := sendEmail(target, testResult, "test", ""); err != nil {
				slog.Error("email test failed", "error", err, "url", target.URL)
				errs = append(errs, fmt.Errorf("email %s: %w", target.URL, err))
			} else {
				slog.Info("notify=email test=sent", "to", target.To)
			}
		}
	}

	return errors.Join(errs...)
}

// sendWebhook performs an HTTP POST with a JSON payload to the given URL.
// If secret is non-empty the request includes an X-DrainCtl-Signature header
// containing the HMAC-SHA256 of the body: "sha256=<hex>".
func sendWebhook(url string, secret string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "DrainCtl/"+Version)
	if secret != "" {
		req.Header.Set("X-DrainCtl-Signature", webhookSignature(secret, body))
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

// webhookSignature returns the HMAC-SHA256 signature of body using secret,
// formatted as "sha256=<hex>" (compatible with GitHub-style webhook signatures).
func webhookSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// NotificationSubject returns a natural-language subject line for the given
// trigger, suitable for email subjects, ntfy titles, and webhook payloads.
func NotificationSubject(result *CheckResult, trigger Trigger, changedBy string) string {
	host := result.Host
	if host == "" {
		host = "Unknown"
	}

	dur := ""
	if result.StateDurationSeconds != nil {
		dur = formatDuration(time.Duration(*result.StateDurationSeconds * float64(time.Second)))
	}

	grace := formatDuration(time.Duration(result.GracePeriodSeconds) * time.Second)

	by := ""
	if changedBy != "" {
		by = " by " + changedBy
	}

	switch trigger {
	case TriggerDrainOn:
		return fmt.Sprintf("%s \u2014 New remote connections disabled%s", host, by)
	case TriggerDrainOff:
		return fmt.Sprintf("%s \u2014 Remote connections re-enabled%s", host, by)
	case TriggerAlert:
		return fmt.Sprintf("%s \u2014 Remote connections disabled for %s (exceeds %s grace period)", host, dur, grace)
	case TriggerGraceEntered:
		remaining := ""
		if result.StateDurationSeconds != nil {
			rem := time.Duration(result.GracePeriodSeconds)*time.Second - time.Duration(*result.StateDurationSeconds*float64(time.Second))
			if rem > 0 {
				remaining = formatDuration(rem) + " remaining in "
			}
		}
		return fmt.Sprintf("%s \u2014 Remote connections disabled, %sgrace period", host, remaining)
	case TriggerHealthy:
		return fmt.Sprintf("%s \u2014 Remote connections enabled, server healthy", host)
	case TriggerSessionWarning:
		if result.Sessions != nil {
			return fmt.Sprintf("%s \u2014 Session utilization at %d%% (%d/%d sessions)",
				host, result.Sessions.UtilizationPct, result.Sessions.TotalSessions, result.Sessions.MaxSessions)
		}
		return fmt.Sprintf("%s \u2014 Session utilization warning", host)
	case TriggerCPUWarning, TriggerCPUCritical:
		if result.Performance != nil {
			return fmt.Sprintf("%s \u2014 CPU at %.0f%%", host, result.Performance.CPUPct)
		}
		return fmt.Sprintf("%s \u2014 %s", host, trigger)
	case TriggerMemoryWarning, TriggerMemoryCritical:
		if result.Performance != nil && result.Performance.MemTotalMB > 0 {
			usedPct := (1 - result.Performance.MemAvailMB/result.Performance.MemTotalMB) * 100
			return fmt.Sprintf("%s \u2014 Memory at %.0f%%", host, usedPct)
		}
		return fmt.Sprintf("%s \u2014 %s", host, trigger)
	case TriggerInputDelayWarning, TriggerInputDelayCritical:
		if result.Performance != nil {
			return fmt.Sprintf("%s \u2014 Input delay P95 %.0fms", host, result.Performance.InputDelayP95)
		}
		return fmt.Sprintf("%s \u2014 %s", host, trigger)
	default:
		return fmt.Sprintf("%s \u2014 %s", host, trigger)
	}
}

// formatDuration returns a human-readable duration like "2h 15m" or "45m".
func formatDuration(d time.Duration) string {
	d = d.Truncate(time.Minute)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 && m > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	if h > 0 {
		return fmt.Sprintf("%dh", h)
	}
	if m > 0 {
		return fmt.Sprintf("%dm", m)
	}
	return "0m"
}

// sendNtfy posts a message to an ntfy.sh-compatible endpoint.
func sendNtfy(url string, title string, message string, priority string, tags string) error {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(message))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Title", title)
	req.Header.Set("Priority", priority)
	req.Header.Set("Tags", tags)
	req.Header.Set("User-Agent", "DrainCtl/"+Version)

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

// ntfyStyle returns the ntfy priority and tags emoji for a given trigger.
func ntfyStyle(trigger Trigger) (priority, tags string) {
	switch trigger {
	case TriggerAlert, TriggerCPUCritical, TriggerMemoryCritical, TriggerInputDelayCritical:
		return "high", "warning"
	case TriggerGraceEntered, TriggerCPUWarning, TriggerMemoryWarning,
		TriggerInputDelayWarning, TriggerSessionWarning:
		return "default", "warning"
	case TriggerDrainOn:
		return "default", "no_entry"
	case TriggerDrainOff, TriggerHealthy:
		return "default", "white_check_mark"
	default:
		return "default", "white_check_mark"
	}
}

// perfTriggers is the set of performance-related triggers that use repeat intervals.
var perfTriggers = map[Trigger]bool{
	TriggerCPUWarning:         true,
	TriggerCPUCritical:        true,
	TriggerInputDelayWarning:  true,
	TriggerInputDelayCritical: true,
	TriggerMemoryWarning:      true,
	TriggerMemoryCritical:     true,
}

// isRepeatTrigger returns true for triggers that use per-target repeat intervals.
func isRepeatTrigger(t Trigger) bool {
	return t == TriggerAlert || t == TriggerSessionWarning || perfTriggers[t]
}

// getLastSent returns the last time a notification was sent for a given target+trigger.
func getLastSent(state *NotifyState, url string, trigger Trigger) time.Time {
	switch trigger {
	case TriggerAlert:
		return state.LastAlertNotify[url]
	case TriggerSessionWarning:
		return state.LastSessionWarnNotify[url]
	default:
		if m, ok := state.LastPerfNotify[url]; ok {
			return m[trigger]
		}
		return time.Time{}
	}
}

// setLastSent records the last time a notification was sent for a given target+trigger.
func setLastSent(state *NotifyState, url string, trigger Trigger, t time.Time) {
	switch trigger {
	case TriggerAlert:
		state.LastAlertNotify[url] = t
	case TriggerSessionWarning:
		state.LastSessionWarnNotify[url] = t
	default:
		if state.LastPerfNotify[url] == nil {
			state.LastPerfNotify[url] = make(map[Trigger]time.Time)
		}
		state.LastPerfNotify[url][trigger] = t
	}
}

// ResetPerfCooldown clears per-target cooldown entries for all performance
// triggers that are no longer active.
func ResetPerfCooldown(state *NotifyState, activeTriggers []Trigger) {
	active := make(map[Trigger]bool, len(activeTriggers))
	for _, t := range activeTriggers {
		active[t] = true
	}
	for url, m := range state.LastPerfNotify {
		for t := range m {
			if !active[t] {
				delete(m, t)
			}
		}
		if len(m) == 0 {
			delete(state.LastPerfNotify, url)
		}
	}
}
