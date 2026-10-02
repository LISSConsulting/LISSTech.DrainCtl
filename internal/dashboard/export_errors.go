//go:build windows

package dashboard

import (
	"encoding/json"
	"net/http"
)

// exportErrorBody is the canonical error envelope shape used by every
// /api/v1/metrics/.../export endpoint (per contracts/http-metrics-export.md).
// The bare-code shape mirrors the rest of the dashboard (`{"error":"<code>"}`)
// while the 413 payload_too_large case carries structured guidance.
type exportErrorBody struct {
	Error     string  `json:"error"`
	LimitRows int     `json:"limit_rows,omitempty"`
	Rows      int     `json:"rows,omitempty"`
	Message   string  `json:"message,omitempty"`
	Generator *string `json:"generator,omitempty"`
}

// writeExportError emits a JSON error body with the documented envelope and
// HTTP status. Pass zero limitRows/rows/message for the simple code-only
// shape. The HTTP body MUST NOT include exported observation values,
// per-Credentials, or full file contents (per FR-015 and the observability
// guidance in spec.md).
func writeExportError(w http.ResponseWriter, code string, status int, extra exportErrorBody) {
	extra.Error = code
	if extra.LimitRows == 0 {
		extra.LimitRows = 0
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(extra)
}
