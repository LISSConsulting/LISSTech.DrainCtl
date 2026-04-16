//go:build windows

package main

/*
#include <stdlib.h>
*/
import "C"
import (
	"encoding/json"
	"log/slog"
	"time"
	"unsafe"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/pipe"
)

func marshalJSON(v any) *C.char {
	data, err := json.Marshal(v)
	if err != nil {
		return marshalError(err)
	}
	return C.CString(string(data))
}

func marshalError(err error) *C.char {
	out, _ := json.Marshal(map[string]string{"error": err.Error()})
	return C.CString(string(out))
}

//export DrainCtl_Version
func DrainCtl_Version() *C.char {
	return C.CString(dc.Version)
}

//export DrainCtl_ReadDrainMode
func DrainCtl_ReadDrainMode() *C.char {
	state, err := dc.ReadDrainMode()
	if err != nil {
		return marshalError(err)
	}
	return marshalJSON(state)
}

//export DrainCtl_Check
func DrainCtl_Check(dbPath *C.char, graceMinutes C.int, retentionDays C.int) *C.char {
	// Try service pipe first.
	if result, err := pipe.CheckViaPipe(); err == nil {
		return marshalJSON(result)
	}

	// Fallback: direct check.
	db := C.GoString(dbPath)
	if db == "" {
		db = dc.DefaultAuditPath()
	}

	out, err := dc.Check(dc.CheckOptions{
		DBPath:        db,
		GracePeriod:   time.Duration(graceMinutes) * time.Minute,
		RetentionDays: dc.ClampRetention(int(retentionDays)),
	})
	if err != nil {
		return marshalError(err)
	}
	return marshalJSON(out.Result)
}

//export DrainCtl_History
func DrainCtl_History(dbPath *C.char, limit C.int, changesOnly C.int) *C.char {
	// Try service pipe first.
	if records, err := pipe.HistoryViaPipe(int(limit), changesOnly != 0); err == nil {
		if records == nil {
			return C.CString("[]")
		}
		return marshalJSON(records)
	}

	// Fallback: direct file read.
	db := C.GoString(dbPath)
	if db == "" {
		db = dc.DefaultAuditPath()
	}

	records, err := dc.GetHistory(dc.HistoryOptions{
		DBPath:      db,
		Limit:       int(limit),
		ChangesOnly: changesOnly != 0,
	})
	if err != nil {
		return marshalError(err)
	}
	if records == nil {
		return C.CString("[]")
	}
	durations := dc.ComputeStateDurations(records)
	out := make([]dc.HistoryRecord, len(records))
	for i, r := range records {
		out[i] = dc.AuditToHistory(r, &durations[i])
	}
	return marshalJSON(out)
}

//export DrainCtl_AuditSetup
func DrainCtl_AuditSetup() *C.char {
	err := dc.RunAuditSetup()
	if err != nil {
		return marshalError(err)
	}
	return C.CString(`{"ok":true}`)
}

//export DrainCtl_GetSettings
func DrainCtl_GetSettings() *C.char {
	cfg, err := dc.LoadConfig()
	if err != nil {
		return marshalError(err)
	}

	// Extract legacy flat fields for PS module backwards compat.
	webhookURL := ""
	ntfyURL := ""
	onTransition := false
	onGraceExceeded := false
	repeatMinutes := 0

	for _, t := range cfg.Notifications {
		if t.Type == "webhook" && webhookURL == "" {
			webhookURL = t.URL
		}
		if t.Type == "ntfy" && ntfyURL == "" {
			ntfyURL = t.URL
		}
		if t.HasTrigger(dc.TriggerDrainOn) || t.HasTrigger(dc.TriggerDrainOff) ||
			t.HasTrigger(dc.TriggerGraceEntered) || t.HasTrigger(dc.TriggerHealthy) {
			onTransition = true
		}
		if t.HasTrigger(dc.TriggerAlert) {
			onGraceExceeded = true
		}
		if repeatMinutes == 0 && t.RepeatMinutes > 0 {
			repeatMinutes = t.RepeatMinutes
		}
	}

	out := map[string]any{
		"notifications":     cfg.Notifications,
		"webhook_url":       webhookURL,
		"ntfy_url":          ntfyURL,
		"on_transition":     onTransition,
		"on_grace_exceeded": onGraceExceeded,
		"repeat_minutes":    repeatMinutes,
		"enabled":           cfg.HasTargets(),
	}
	return marshalJSON(out)
}

//export DrainCtl_SetSettings
func DrainCtl_SetSettings(jsonStr *C.char) *C.char {
	raw := []byte(C.GoString(jsonStr))

	// Peek at keys to auto-detect format.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return marshalError(err)
	}

	cfg, err := dc.LoadConfig()
	if err != nil {
		return marshalError(err)
	}

	if _, isNew := probe["notifications"]; isNew {
		// New format: replace notifications array directly.
		var input struct {
			Notifications []dc.NotificationTarget `json:"notifications"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return marshalError(err)
		}
		cfg.Notifications = input.Notifications
	} else {
		// Legacy flat format.
		var input struct {
			WebhookURL      *string `json:"webhook_url"`
			NtfyURL         *string `json:"ntfy_url"`
			OnTransition    *bool   `json:"on_transition"`
			OnGraceExceeded *bool   `json:"on_grace_exceeded"`
			RepeatMinutes   *int    `json:"repeat_minutes"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return marshalError(err)
		}

		// Build triggers from legacy booleans.
		triggers := buildLegacyTriggers(input.OnTransition, input.OnGraceExceeded)

		// Find or create webhook target.
		if input.WebhookURL != nil {
			idx := findTarget(cfg.Notifications, "webhook")
			if idx >= 0 {
				cfg.Notifications[idx].URL = *input.WebhookURL
				if triggers != nil {
					cfg.Notifications[idx].Triggers = triggers
				}
				if input.RepeatMinutes != nil {
					cfg.Notifications[idx].RepeatMinutes = *input.RepeatMinutes
				}
				// Remove target if URL cleared.
				if *input.WebhookURL == "" {
					cfg.Notifications = append(cfg.Notifications[:idx], cfg.Notifications[idx+1:]...)
				}
			} else if *input.WebhookURL != "" {
				t := dc.NotificationTarget{
					Type:     "webhook",
					URL:      *input.WebhookURL,
					Triggers: dc.DefaultTriggers,
				}
				if triggers != nil {
					t.Triggers = triggers
				}
				if input.RepeatMinutes != nil {
					t.RepeatMinutes = *input.RepeatMinutes
				}
				cfg.Notifications = append(cfg.Notifications, t)
			}
		}

		// Find or create ntfy target.
		if input.NtfyURL != nil {
			idx := findTarget(cfg.Notifications, "ntfy")
			if idx >= 0 {
				cfg.Notifications[idx].URL = *input.NtfyURL
				if triggers != nil {
					cfg.Notifications[idx].Triggers = triggers
				}
				if input.RepeatMinutes != nil {
					cfg.Notifications[idx].RepeatMinutes = *input.RepeatMinutes
				}
				// Remove target if URL cleared.
				if *input.NtfyURL == "" {
					cfg.Notifications = append(cfg.Notifications[:idx], cfg.Notifications[idx+1:]...)
				}
			} else if *input.NtfyURL != "" {
				t := dc.NotificationTarget{
					Type:     "ntfy",
					URL:      *input.NtfyURL,
					Triggers: dc.DefaultTriggers,
				}
				if triggers != nil {
					t.Triggers = triggers
				}
				if input.RepeatMinutes != nil {
					t.RepeatMinutes = *input.RepeatMinutes
				}
				cfg.Notifications = append(cfg.Notifications, t)
			}
		}

		// Apply repeat_minutes to all existing targets if only that field was set.
		if input.RepeatMinutes != nil && input.WebhookURL == nil && input.NtfyURL == nil {
			for i := range cfg.Notifications {
				cfg.Notifications[i].RepeatMinutes = *input.RepeatMinutes
			}
		}
	}

	if err := dc.SaveConfig(cfg); err != nil {
		return marshalError(err)
	}
	return C.CString(`{"ok":true}`)
}

// findTarget returns the index of the first NotificationTarget with the given type, or -1.
func findTarget(targets []dc.NotificationTarget, typ string) int {
	for i, t := range targets {
		if t.Type == typ {
			return i
		}
	}
	return -1
}

// buildLegacyTriggers converts legacy boolean flags into a trigger slice.
// Returns nil if neither flag was provided (so callers can skip overwriting).
func buildLegacyTriggers(onTransition, onGraceExceeded *bool) []dc.Trigger {
	if onTransition == nil && onGraceExceeded == nil {
		return nil
	}
	var triggers []dc.Trigger
	trans := onTransition != nil && *onTransition
	grace := onGraceExceeded != nil && *onGraceExceeded
	if trans {
		triggers = append(triggers, dc.TriggerDrainOn, dc.TriggerDrainOff, dc.TriggerGraceEntered, dc.TriggerHealthy)
	}
	if grace {
		triggers = append(triggers, dc.TriggerAlert)
	}
	return triggers
}

// Appends a brand-new notification target. The body is a JSON-encoded
// dc.NotifyTargetUpdate; required fields must be supplied (no preserved-
// existing fallback). For email targets, From + To + smtp(s):// URL are
// required. Returns {"ok":true,"target_index":N}.
//
//export DrainCtl_NotifyAppendTarget
func DrainCtl_NotifyAppendTarget(jsonStr *C.char) *C.char {
	raw := C.GoString(jsonStr)
	var update dc.NotifyTargetUpdate
	if err := json.Unmarshal([]byte(raw), &update); err != nil {
		return marshalError(err)
	}
	cfg, err := dc.LoadConfig()
	if err != nil {
		return marshalError(err)
	}
	if err := dc.AppendNotifyTarget(cfg, update); err != nil {
		return marshalError(err)
	}
	if err := dc.SaveConfig(cfg); err != nil {
		return marshalError(err)
	}
	resultIdx := -1
	count := 0
	for _, t := range cfg.Notifications {
		if t.Type == update.Type {
			if t.URL == update.URL {
				resultIdx = count
			}
			count++
		}
	}
	return marshalJSON(map[string]any{"ok": true, "target_index": resultIdx})
}

// Updates an existing notification target at TargetIndex. Errors if no
// target of that type exists at that index — use DrainCtl_NotifyAppendTarget
// to create new targets. Pointer fields left out preserve existing values.
//
//export DrainCtl_NotifySetTarget
func DrainCtl_NotifySetTarget(jsonStr *C.char) *C.char {
	raw := C.GoString(jsonStr)
	var update dc.NotifyTargetUpdate
	if err := json.Unmarshal([]byte(raw), &update); err != nil {
		return marshalError(err)
	}

	cfg, err := dc.LoadConfig()
	if err != nil {
		return marshalError(err)
	}
	if err := dc.SetNotifyTarget(cfg, update); err != nil {
		return marshalError(err)
	}
	if err := dc.SaveConfig(cfg); err != nil {
		return marshalError(err)
	}

	// Compute the resulting per-type index so the caller can confirm where
	// the target landed (especially when TargetIndex was past-end).
	resultIdx := -1
	count := 0
	for _, t := range cfg.Notifications {
		if t.Type == update.Type {
			if t.URL == update.URL {
				resultIdx = count
			}
			count++
		}
	}
	return marshalJSON(map[string]any{"ok": true, "target_index": resultIdx})
}

// Removes the index-th target of typ. Returns {"ok":true} on success.
//
//export DrainCtl_NotifyRemoveTarget
func DrainCtl_NotifyRemoveTarget(typ *C.char, index C.int) *C.char {
	cfg, err := dc.LoadConfig()
	if err != nil {
		return marshalError(err)
	}
	if err := dc.RemoveNotifyTarget(cfg, C.GoString(typ), int(index)); err != nil {
		return marshalError(err)
	}
	if err := dc.SaveConfig(cfg); err != nil {
		return marshalError(err)
	}
	return C.CString(`{"ok":true}`)
}

// Tests all currently-configured notification targets and returns a per-target
// result array so the caller can show exactly which one failed and why.
//
// Returns: {"ok": bool, "results": [{type, url, type_index, ok, error?}, ...], "error": optional}
//
//export DrainCtl_TestNotify
func DrainCtl_TestNotify() *C.char {
	cfg, err := dc.LoadConfig()
	if err != nil {
		return marshalError(err)
	}
	results, err := dc.SendTestNotification(cfg.Notifications)
	body := map[string]any{
		"ok":      err == nil,
		"results": results,
	}
	if err != nil {
		body["error"] = err.Error()
	}
	return marshalJSON(body)
}

//export DrainCtl_EnableDashboard
func DrainCtl_EnableDashboard(port C.int, group *C.char) *C.char {
	g := C.GoString(group)
	if g == "" {
		g = dc.DefaultDashboardGroup
	}
	p := int(port)
	if p == 0 {
		p = dc.DefaultDashboardPort
	}

	cfg, err := dc.LoadConfig()
	if err != nil {
		return marshalError(err)
	}

	cfg.Dashboard.Enabled = true
	cfg.Dashboard.Port = p
	cfg.Dashboard.Group = g

	if err := dc.SaveConfig(cfg); err != nil {
		return marshalError(err)
	}
	return marshalJSON(map[string]any{"ok": true, "port": p, "group": g})
}

//export DrainCtl_DisableDashboard
func DrainCtl_DisableDashboard() *C.char {
	cfg, err := dc.LoadConfig()
	if err != nil {
		return marshalError(err)
	}

	cfg.Dashboard.Enabled = false

	if err := dc.SaveConfig(cfg); err != nil {
		return marshalError(err)
	}
	return C.CString(`{"ok":true}`)
}

//export DrainCtl_InstallCertificate
func DrainCtl_InstallCertificate(certPath *C.char, keyPath *C.char) *C.char {
	cert := C.GoString(certPath)
	key := C.GoString(keyPath)
	if err := dc.InstallCertificate(cert, key); err != nil {
		return marshalError(err)
	}
	return C.CString(`{"ok":true}`)
}

//export DrainCtl_Free
func DrainCtl_Free(p *C.char) {
	C.free(unsafe.Pointer(p))
}

func init() {
	slog.SetDefault(slog.New(slog.DiscardHandler))
}

func main() {}
