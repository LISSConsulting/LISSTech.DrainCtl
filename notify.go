//go:build windows

package drainctl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// NotifyConfig holds notification settings read from the registry.
type NotifyConfig struct {
	WebhookURL      string        // empty = disabled
	NtfyURL         string        // e.g. "https://ntfy.sh/drainctl-alerts", empty = disabled
	OnTransition    bool          // notify on any state transition
	OnGraceExceeded bool          // notify when drain exceeds grace period
	RepeatInterval  time.Duration // how often to re-notify while in alert (0 = once)
}

// NotifyState tracks notification timing to support repeat intervals.
type NotifyState struct {
	LastAlertNotify time.Time // tracks repeat interval
}

// Enabled returns true if at least one backend is configured.
func (c NotifyConfig) Enabled() bool {
	return c.WebhookURL != "" || c.NtfyURL != ""
}

var httpClient = &http.Client{Timeout: 5 * time.Second}

// SendNotification evaluates whether a notification should fire based on
// config and current state, then dispatches to configured backends.
// Errors are logged but never returned — notifications must not crash the service.
func SendNotification(cfg NotifyConfig, state *NotifyState, result *CheckResult, transition bool, changedBy string, log LogFunc) {
	if log == nil {
		log = DiscardLogger()
	}
	if !cfg.Enabled() {
		return
	}

	// Determine event type and whether we should send.
	var event string
	var shouldSend bool

	switch {
	case transition && cfg.OnTransition:
		event = "transition"
		shouldSend = true
	case result.Status == "Alert" && cfg.OnGraceExceeded:
		event = "alert"
		now := time.Now()
		if cfg.RepeatInterval > 0 {
			if state.LastAlertNotify.IsZero() || now.Sub(state.LastAlertNotify) >= cfg.RepeatInterval {
				shouldSend = true
				state.LastAlertNotify = now
			}
		} else {
			// RepeatInterval == 0 means once only.
			if state.LastAlertNotify.IsZero() {
				shouldSend = true
				state.LastAlertNotify = now
			}
		}
	case result.Status == "Grace" && transition && cfg.OnTransition:
		event = "grace"
		shouldSend = true
	case result.Status == "Healthy" && transition && cfg.OnTransition:
		event = "healthy"
		shouldSend = true
		// Reset alert tracking when returning to healthy.
		state.LastAlertNotify = time.Time{}
	}

	// If returning to healthy (even without transition flag), reset alert state.
	if result.Status == "Healthy" {
		state.LastAlertNotify = time.Time{}
	}

	if !shouldSend {
		return
	}

	// Build webhook payload.
	stateDur := 0.0
	if result.StateDurationSeconds != nil {
		stateDur = *result.StateDurationSeconds
	}

	payload := map[string]any{
		"event":                  event,
		"host":                   result.Host,
		"drain_mode":             result.DrainModeLabel,
		"status":                 result.Status,
		"message":                result.Message,
		"changed_by":             changedBy,
		"state_duration_seconds": int(stateDur),
		"timestamp":              result.Timestamp.Format(time.RFC3339),
	}
	if transition && result.TransitionFrom != "" {
		payload["previous_mode"] = result.TransitionFrom
	}

	// Send to webhook.
	if cfg.WebhookURL != "" {
		if err := sendWebhook(cfg.WebhookURL, payload); err != nil {
			LogMsg(log, LvlWRN, "webhook notification failed", fmt.Sprintf("error=%q url=%s", err, cfg.WebhookURL))
		} else {
			log(LvlINF, "notify=webhook", fmt.Sprintf("event=%s", event))
		}
	}

	// Send to ntfy.
	if cfg.NtfyURL != "" {
		title := fmt.Sprintf("DrainCtl: %s on %s", event, result.Host)
		priority := "default"
		tags := "white_check_mark"
		switch result.Status {
		case "Alert":
			priority = "high"
			tags = "warning"
		case "Grace":
			tags = "warning"
		}
		if err := sendNtfy(cfg.NtfyURL, title, result.Message, priority, tags); err != nil {
			LogMsg(log, LvlWRN, "ntfy notification failed", fmt.Sprintf("error=%q url=%s", err, cfg.NtfyURL))
		} else {
			log(LvlINF, "notify=ntfy", fmt.Sprintf("event=%s", event))
		}
	}
}

// SendTestNotification sends a test message to all configured backends.
func SendTestNotification(cfg NotifyConfig, log LogFunc) error {
	if log == nil {
		log = DiscardLogger()
	}
	if !cfg.Enabled() {
		return fmt.Errorf("no notification backends configured")
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

	var lastErr error

	if cfg.WebhookURL != "" {
		if err := sendWebhook(cfg.WebhookURL, payload); err != nil {
			LogMsg(log, LvlERR, "webhook test failed", fmt.Sprintf("error=%q", err))
			lastErr = err
		} else {
			log(LvlOK, "notify=webhook", "test=sent")
		}
	}

	if cfg.NtfyURL != "" {
		title := fmt.Sprintf("DrainCtl Test: %s", host)
		msg := "This is a test notification from DrainCtl."
		if err := sendNtfy(cfg.NtfyURL, title, msg, "default", "test_tube"); err != nil {
			LogMsg(log, LvlERR, "ntfy test failed", fmt.Sprintf("error=%q", err))
			lastErr = err
		} else {
			log(LvlOK, "notify=ntfy", "test=sent")
		}
	}

	return lastErr
}

// sendWebhook performs an HTTP POST with a JSON payload to the given URL.
func sendWebhook(url string, payload map[string]any) error {
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

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
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

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}
