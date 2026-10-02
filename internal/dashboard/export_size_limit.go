//go:build windows

package dashboard

import "net/http"

// exportSizeLimitRowCap is the documented hard cap on the number of
// observation rows a single export can contain. Above this cap the
// handler returns 413 payload_too_large so the operator is told the
// export was refused rather than silently producing a truncated file
// (per FR-017 / SC-004).
//
// 50,000 rows gives a 5x margin over the 10,000-row SC-004 budget and
// keeps typical 1h/6h/24h windows comfortably under it. Raw 90-day
// windows can exceed the cap and intentionally fail with a clear error
// instead of silently truncating.
const exportSizeLimitRowCap = 50000

// enforceExportSizeLimit returns true when the snapshot's row count is
// within the documented cap. When the cap is exceeded it writes the
// 413 payload_too_large response body and returns false; the caller
// MUST stop the export and return without writing file bytes.
//
// Per FR-017 the response body identifies the limit and the actual row
// count so the operator can narrow the time range or the host filter
// without guessing.
func enforceExportSizeLimit(snap Snapshot, w http.ResponseWriter) bool {
	rows := len(snap.Rows)
	if rows <= exportSizeLimitRowCap {
		return true
	}
	writeExportError(w, "payload_too_large", http.StatusRequestEntityTooLarge, exportErrorBody{
		LimitRows: exportSizeLimitRowCap,
		Rows:      rows,
		Message:   "Narrow the time range or restrict the cohort.",
	})
	return false
}
