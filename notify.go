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

// NotifyState tracks per-target notification timing for repeat intervals.
type NotifyState struct {
	LastAlertNotify map[string]time.Time // key = target URL
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

	// Reset alert tracking when returning to healthy, but preserve session
	// warning repeat tracking so it is not re-fired immediately if sessions
	// are still at high utilization when drain mode turns off.
	if trigger == TriggerHealthy || trigger == TriggerDrainOff {
		for k := range state.LastAlertNotify {
			if k != string(TriggerSessionWarning) {
				delete(state.LastAlertNotify, k)
			}
		}
	}

	// Build payload.
	stateDur := 0.0
	if result.StateDurationSeconds != nil {
		stateDur = *result.StateDurationSeconds
	}

	payload := map[string]any{
		"event":                  string(trigger),
		"host":                   result.Host,
		"drain_mode":             result.DrainModeLabel,
		"status":                 result.Status,
		"message":                result.Message,
		"changed_by":             changedBy,
		"state_duration_seconds": int(stateDur),
		"timestamp":              result.Timestamp.Format(time.RFC3339),
	}
	if result.Transition && result.TransitionFrom != "" {
		payload["previous_mode"] = result.TransitionFrom
	}

	for _, target := range targets {
		if target.URL == "" || !target.HasTrigger(trigger) {
			continue
		}

		// For repeating triggers (alert, session_warning), check per-target repeat interval.
		if trigger == TriggerAlert || trigger == TriggerSessionWarning {
			now := time.Now()
			repeatInterval := time.Duration(target.RepeatMinutes) * time.Minute
			lastSent := state.LastAlertNotify[target.URL]

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
			state.LastAlertNotify[target.URL] = now
		}

		// Dispatch to backend.
		switch target.Type {
		case "webhook":
			if err := sendWebhook(target.URL, payload); err != nil {
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
			if err := sendNtfy(target.URL, title, result.Message, priority, tags); err != nil {
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

	var lastErr error

	for _, target := range targets {
		if target.URL == "" {
			continue
		}

		switch target.Type {
		case "webhook":
			if err := sendWebhook(target.URL, payload); err != nil {
				LogMsg(log, LvlERR, "webhook test failed", fmt.Sprintf("error=%q url=%s", err, target.URL))
				lastErr = err
			} else {
				log(LvlOK, "notify=webhook", fmt.Sprintf("test=sent url=%s", target.URL))
			}

		case "ntfy":
			title := fmt.Sprintf("DrainCtl Test: %s", host)
			msg := "This is a test notification from DrainCtl."
			if err := sendNtfy(target.URL, title, msg, "default", "test_tube"); err != nil {
				LogMsg(log, LvlERR, "ntfy test failed", fmt.Sprintf("error=%q url=%s", err, target.URL))
				lastErr = err
			} else {
				log(LvlOK, "notify=ntfy", fmt.Sprintf("test=sent url=%s", target.URL))
			}
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
