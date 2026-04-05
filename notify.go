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
	"net/http"
	"os"
	"time"
)

// NotifyState tracks per-target notification timing for repeat intervals.
type NotifyState struct {
	LastAlertNotify       map[string]time.Time // key = target URL (alert trigger)
	LastSessionWarnNotify map[string]time.Time // key = target URL (session_warning trigger)
}

var httpClient = &http.Client{Timeout: 5 * time.Second}

// SendNotification evaluates whether a notification should fire for the
// given trigger and dispatches to matching targets. Errors are logged but
// never returned — notifications must not crash the service.
func SendNotification(targets []NotificationTarget, state *NotifyState, result *CheckResult, trigger Trigger, changedBy string, log LogFunc) {
	if log == nil {
		log = DiscardLogger()
	}
	if len(targets) == 0 {
		return
	}
	if state.LastAlertNotify == nil {
		state.LastAlertNotify = make(map[string]time.Time)
	}
	if state.LastSessionWarnNotify == nil {
		state.LastSessionWarnNotify = make(map[string]time.Time)
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
	}
	if result.Transition && result.TransitionFrom != "" {
		payload["previous_mode"] = result.TransitionFrom
	}
	if result.Sessions != nil {
		payload["sessions"] = result.Sessions
	}

	for _, target := range targets {
		if target.URL == "" || !target.HasTrigger(trigger) {
			continue
		}

		// For repeating triggers (alert, session_warning), check per-target repeat interval.
		// alert uses LastAlertNotify; session_warning uses LastSessionWarnNotify so its
		// suppression window survives healthy/drain_off transitions independently.
		if trigger == TriggerAlert || trigger == TriggerSessionWarning {
			trackMap := state.LastAlertNotify
			if trigger == TriggerSessionWarning {
				trackMap = state.LastSessionWarnNotify
			}

			now := time.Now()
			repeatInterval := time.Duration(target.RepeatMinutes) * time.Minute
			lastSent := trackMap[target.URL]

			if repeatInterval > 0 {
				if !lastSent.IsZero() && now.Sub(lastSent) < repeatInterval {
					continue
				}
			} else {
				// RepeatMinutes == 0 means fire once only.
				if !lastSent.IsZero() {
					continue
				}
			}
			trackMap[target.URL] = now
		}

		// Dispatch to backend.
		switch target.Type {
		case "webhook":
			if err := sendWebhook(target.URL, target.Secret, payload); err != nil {
				LogMsg(log, LvlWRN, "webhook notification failed", fmt.Sprintf("error=%q url=%s", err, target.URL))
			} else {
				log(LvlINF, "notify=webhook", fmt.Sprintf("event=%s url=%s", trigger, target.URL))
			}

		case "ntfy":
			title := fmt.Sprintf("DrainCtl: %s on %s", trigger, result.Host)
			priority := "default"
			tags := "white_check_mark"
			switch result.Status {
			case "Alert":
				priority = "high"
				tags = "warning"
			case "Grace":
				tags = "warning"
			}
			ntfyMsg := result.Message
			if trigger == TriggerSessionWarning && result.Sessions != nil {
				sess := result.Sessions
				ntfyMsg = fmt.Sprintf("Session utilization at %d%% (%d/%d sessions).",
					sess.UtilizationPct, sess.TotalSessions, sess.MaxSessions)
				priority = "default"
				tags = "busts_in_silhouette"
			}
			if err := sendNtfy(target.URL, title, ntfyMsg, priority, tags); err != nil {
				LogMsg(log, LvlWRN, "ntfy notification failed", fmt.Sprintf("error=%q url=%s", err, target.URL))
			} else {
				log(LvlINF, "notify=ntfy", fmt.Sprintf("event=%s url=%s", trigger, target.URL))
			}
		}
	}
}

// SendTestNotification sends a test message to all configured targets.
func SendTestNotification(targets []NotificationTarget, log LogFunc) error {
	if log == nil {
		log = DiscardLogger()
	}

	hasTargets := false
	for _, t := range targets {
		if t.URL != "" {
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
		"timestamp":              time.Now().Format(time.RFC3339),
	}

	var errs []error

	for _, target := range targets {
		if target.URL == "" {
			continue
		}

		switch target.Type {
		case "webhook":
			if err := sendWebhook(target.URL, target.Secret, payload); err != nil {
				LogMsg(log, LvlERR, "webhook test failed", fmt.Sprintf("error=%q url=%s", err, target.URL))
				errs = append(errs, fmt.Errorf("webhook %s: %w", target.URL, err))
			} else {
				log(LvlOK, "notify=webhook", fmt.Sprintf("test=sent url=%s", target.URL))
			}

		case "ntfy":
			title := fmt.Sprintf("DrainCtl Test: %s", host)
			msg := "This is a test notification from DrainCtl."
			if err := sendNtfy(target.URL, title, msg, "default", "test_tube"); err != nil {
				LogMsg(log, LvlERR, "ntfy test failed", fmt.Sprintf("error=%q url=%s", err, target.URL))
				errs = append(errs, fmt.Errorf("ntfy %s: %w", target.URL, err))
			} else {
				log(LvlOK, "notify=ntfy", fmt.Sprintf("test=sent url=%s", target.URL))
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
