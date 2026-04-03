//go:build windows

package main

/*
#include <stdlib.h>
*/
import "C"
import (
	"encoding/json"
	"time"
	"unsafe"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/pipe"
)

func marshalJSON(v any) *C.char {
	data, err := json.Marshal(v)
	if err != nil {
		return C.CString(`{"error":"` + err.Error() + `"}`)
	}
	return C.CString(string(data))
}

func marshalError(err error) *C.char {
	return C.CString(`{"error":"` + err.Error() + `"}`)
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
		RetentionDays: int(retentionDays),
		Log:           dc.DiscardLogger(),
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
	err := dc.RunAuditSetup(dc.DiscardLogger())
	if err != nil {
		return marshalError(err)
	}
	return C.CString(`{"ok":true}`)
}

//export DrainCtl_GetNotifyConfig
func DrainCtl_GetNotifyConfig() *C.char {
	cfg := dc.ReadNotifyConfig(dc.DiscardLogger())
	out := map[string]any{
		"webhook_url":       cfg.WebhookURL,
		"ntfy_url":          cfg.NtfyURL,
		"on_transition":     cfg.OnTransition,
		"on_grace_exceeded": cfg.OnGraceExceeded,
		"repeat_minutes":    int(cfg.RepeatInterval.Minutes()),
		"enabled":           cfg.Enabled(),
	}
	return marshalJSON(out)
}

//export DrainCtl_SetNotifyConfig
func DrainCtl_SetNotifyConfig(jsonStr *C.char) *C.char {
	var input struct {
		WebhookURL      *string `json:"webhook_url"`
		NtfyURL         *string `json:"ntfy_url"`
		OnTransition    *bool   `json:"on_transition"`
		OnGraceExceeded *bool   `json:"on_grace_exceeded"`
		RepeatMinutes   *int    `json:"repeat_minutes"`
	}

	if err := json.Unmarshal([]byte(C.GoString(jsonStr)), &input); err != nil {
		return marshalError(err)
	}

	// Read existing config, then overlay provided fields.
	cfg := dc.ReadNotifyConfig(dc.DiscardLogger())
	if input.WebhookURL != nil {
		cfg.WebhookURL = *input.WebhookURL
	}
	if input.NtfyURL != nil {
		cfg.NtfyURL = *input.NtfyURL
	}
	if input.OnTransition != nil {
		cfg.OnTransition = *input.OnTransition
	}
	if input.OnGraceExceeded != nil {
		cfg.OnGraceExceeded = *input.OnGraceExceeded
	}
	if input.RepeatMinutes != nil {
		cfg.RepeatInterval = time.Duration(*input.RepeatMinutes) * time.Minute
	}

	if err := dc.WriteNotifyConfig(cfg, dc.DiscardLogger()); err != nil {
		return marshalError(err)
	}
	return C.CString(`{"ok":true}`)
}

//export DrainCtl_TestNotify
func DrainCtl_TestNotify() *C.char {
	cfg := dc.ReadNotifyConfig(dc.DiscardLogger())
	if err := dc.SendTestNotification(cfg, dc.DiscardLogger()); err != nil {
		return marshalError(err)
	}
	return C.CString(`{"ok":true}`)
}

//export DrainCtl_Free
func DrainCtl_Free(p *C.char) {
	C.free(unsafe.Pointer(p))
}

func main() {}
