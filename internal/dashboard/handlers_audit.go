//go:build windows

package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// handleAudit serves GET /api/v1/audit per contracts/http-audit.md.
// Time-range query over the audit table with cursor pagination.
func (ds *DashboardServer) handleAudit(w http.ResponseWriter, r *http.Request) {
	if ds.as == nil {
		writeJSONError(w, "storage_error", http.StatusInternalServerError)
		return
	}

	q := r.URL.Query()

	filter := telemetry.QueryFilter{
		Host:        q.Get("host"),
		Actor:       q.Get("actor"),
		ChangesOnly: true,
	}

	if s := q.Get("from"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeJSONError(w, "invalid_range", http.StatusBadRequest)
			return
		}
		tu := t.UTC()
		filter.From = &tu
	}
	if s := q.Get("to"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeJSONError(w, "invalid_range", http.StatusBadRequest)
			return
		}
		tu := t.UTC()
		filter.To = &tu
	}
	if filter.From != nil && filter.To != nil && !filter.To.After(*filter.From) {
		writeJSONError(w, "invalid_range", http.StatusBadRequest)
		return
	}

	// changes_only defaults to true (CLI parity — matches http-audit.md).
	if s := q.Get("changes_only"); s != "" {
		switch strings.ToLower(s) {
		case "true", "1":
			filter.ChangesOnly = true
		case "false", "0":
			filter.ChangesOnly = false
		default:
			writeJSONError(w, "invalid_range", http.StatusBadRequest)
			return
		}
	}

	limit := 500
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			writeJSONError(w, "invalid_range", http.StatusBadRequest)
			return
		}
		if n > 5000 {
			n = 5000
		}
		limit = n
	}
	filter.Limit = limit

	// Cursors are bound to the filter set (host, actor, from, to, changes_only).
	// Reusing a cursor from a different filter set is rejected per the contract
	// to prevent silently-skipped pages when a client varies filters mid-walk.
	filterSig := auditFilterSignature(filter)
	if rawCursor := q.Get("cursor"); rawCursor != "" {
		inner, err := unwrapAuditCursor(rawCursor, filterSig)
		if err != nil {
			writeJSONError(w, "invalid_cursor", http.StatusBadRequest)
			return
		}
		filter.Cursor = inner
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	records, nextCursor, err := ds.as.QueryRange(ctx, filter)
	if err != nil {
		if errors.Is(err, telemetry.ErrInvalidCursor) {
			writeJSONError(w, "invalid_cursor", http.StatusBadRequest)
			return
		}
		slog.Error("audit: query failed", "error", err)
		writeJSONError(w, "storage_error", http.StatusInternalServerError)
		return
	}

	type auditJSON struct {
		Ts             time.Time  `json:"ts"`
		Host           string     `json:"host"`
		PrevState      int        `json:"prev_state"`
		PrevStateLabel string     `json:"prev_state_label"`
		NewState       int        `json:"new_state"`
		NewStateLabel  string     `json:"new_state_label"`
		Principal      string     `json:"principal"`
		ChangedBy      string     `json:"changed_by"`
		Reason         string     `json:"reason"`
		KeyModifiedTs  *time.Time `json:"key_modified_ts"`
		Reconciliation bool       `json:"reconciliation"`
		BeforeTs       *time.Time `json:"before_ts,omitempty"`
	}
	out := make([]auditJSON, len(records))
	for i, rec := range records {
		row := auditJSON{
			Ts:             rec.Ts.UTC(),
			Host:           rec.Host,
			PrevState:      rec.PrevState,
			PrevStateLabel: dc.DrainMode(rec.PrevState).String(), //nolint:gosec // PrevState is 0..3 from a trusted source
			NewState:       rec.NewState,
			NewStateLabel:  dc.DrainMode(rec.NewState).String(), //nolint:gosec // NewState is 0..3 from a trusted source
			Principal:      rec.Principal,
			ChangedBy:      rec.ChangedBy,
			Reason:         rec.Reason,
			KeyModifiedTs:  rec.KeyModifiedTs,
			Reconciliation: rec.Reconciliation,
		}
		if rec.Reconciliation {
			row.BeforeTs = rec.BeforeTs
		}
		out[i] = row
	}

	resp := struct {
		Records       []auditJSON `json:"records"`
		NextCursor    *string     `json:"next_cursor"`
		Tier          string      `json:"tier"`
		TotalReturned int         `json:"total_returned"`
	}{
		Records:       out,
		Tier:          "audit",
		TotalReturned: len(out),
	}
	if nextCursor != "" {
		wrapped := wrapAuditCursor(nextCursor, filterSig)
		resp.NextCursor = &wrapped
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// auditCursorEnvelope carries the inner telemetry-store cursor alongside a
// short SHA-256 signature of the filter fields the cursor was produced under.
// Clients that change filters (host, actor, from, to, changes_only) mid-walk
// get `invalid_cursor` instead of silently-skipped pages (contracts/http-audit.md).
type auditCursorEnvelope struct {
	Sig   string `json:"s"` // hex-encoded sha256(filter canonical form)
	Inner string `json:"c"` // opaque cursor from telemetry.AuditStore
}

// auditFilterSignature produces a stable hex digest over the filter dimensions
// that define "the same walk". Limit is intentionally excluded so a client can
// shrink/grow page size without invalidating its cursor.
func auditFilterSignature(f telemetry.QueryFilter) string {
	var from, to int64 = -1, -1
	if f.From != nil {
		from = f.From.UTC().UnixMilli()
	}
	if f.To != nil {
		to = f.To.UTC().UnixMilli()
	}
	canonical := fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%t",
		f.Host, f.Actor, from, to, f.ChangesOnly)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:8]) // 8 bytes is plenty vs collisions by a hostile client
}

// wrapAuditCursor binds a telemetry-store cursor to the current filter signature.
func wrapAuditCursor(inner, sig string) string {
	b, _ := json.Marshal(auditCursorEnvelope{Sig: sig, Inner: inner})
	return base64.URLEncoding.EncodeToString(b)
}

// unwrapAuditCursor decodes a client-supplied cursor and rejects it if the
// signature does not match the current request's filter set.
func unwrapAuditCursor(raw, wantSig string) (string, error) {
	decoded, err := base64.URLEncoding.DecodeString(raw)
	if err != nil {
		return "", fmt.Errorf("audit cursor: decode: %w", err)
	}
	var env auditCursorEnvelope
	if err := json.Unmarshal(decoded, &env); err != nil {
		return "", fmt.Errorf("audit cursor: unmarshal: %w", err)
	}
	if env.Sig != wantSig {
		return "", fmt.Errorf("audit cursor: filter mismatch")
	}
	return env.Inner, nil
}
