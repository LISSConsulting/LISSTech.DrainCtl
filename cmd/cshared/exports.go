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
	if result, err := dc.CheckViaPipe(); err == nil {
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
	if records, err := dc.HistoryViaPipe(int(limit), changesOnly != 0); err == nil {
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

//export DrainCtl_Free
func DrainCtl_Free(p *C.char) {
	C.free(unsafe.Pointer(p))
}

func main() {}
