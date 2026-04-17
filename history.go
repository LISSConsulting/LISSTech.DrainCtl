//go:build windows

package drainctl

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// HistoryOptions configures a history query.
type HistoryOptions struct {
	DBPath      string
	Limit       int
	ChangesOnly bool
	Since       *time.Time
	Until       *time.Time
}

// GetHistory returns audit records from the SQLite telemetry store.
//
// DBPath names a file inside the drainctl data directory (historically the
// JSONL trail); its parent directory is used as the telemetry data dir so
// existing callers that pass the legacy audit.jsonl path keep working without
// a signature change (T028). An empty DBPath selects DefaultDataDir().
//
// Opens the store read-only so the CLI never writes to drainctl.db while the
// service may be running concurrently (tasks.md T028a / research.md §13).
// Read-only open validates the WAL sidecars (-wal / -shm): a partially-closed
// writer or an ACL mismatch surfaces as an explicit error rather than silent
// fallback to stale data.
func GetHistory(opts HistoryOptions) ([]AuditRecord, error) {
	dataDir := DefaultDataDir()
	if opts.DBPath != "" {
		dataDir = filepath.Dir(opts.DBPath)
	}

	db, err := telemetry.OpenReadOnly(dataDir)
	if err != nil {
		return nil, fmt.Errorf("open audit store: %w", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	store := telemetry.NewReadOnlyAuditStore(db)
	defer func() { _ = store.Close() }()

	// HistoryOptions.Until is inclusive ("at or before") per the CLI contract,
	// but telemetry.QueryFilter.To is exclusive (ts < to). Nudge by +1 ms so a
	// row whose stored millisecond exactly equals Until still matches.
	var to *time.Time
	if opts.Until != nil {
		t := opts.Until.Add(time.Millisecond)
		to = &t
	}

	filter := telemetry.QueryFilter{
		From:        opts.Since,
		To:          to,
		Limit:       opts.Limit,
		ChangesOnly: opts.ChangesOnly,
	}
	recs, _, err := store.QueryRange(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("query audit: %w", err)
	}

	out := make([]AuditRecord, len(recs))
	for i, r := range recs {
		out[i] = auditFromTelemetry(r)
	}
	return out, nil
}

// auditFromTelemetry maps a telemetry audit row back to the public
// drainctl.AuditRecord shape. Fields the telemetry table does not store
// (session counts, perf snapshot, exit code) remain zero-valued — the audit
// table is the canonical record of drain-mode transitions, not every tick
// observation (data-model.md, T026).
func auditFromTelemetry(r telemetry.AuditRecord) AuditRecord {
	rec := AuditRecord{
		Timestamp:      r.Ts,
		Host:           r.Host,
		DrainMode:      DrainMode(r.NewState),
		ChangedBy:      r.ChangedBy,
		Changed:        r.PrevState != r.NewState,
		Reconciliation: r.Reconciliation,
		Reason:         r.Reason,
	}
	rec.DrainLabel = rec.DrainMode.String()
	if r.KeyModifiedTs != nil {
		rec.KeyModified = *r.KeyModifiedTs
	}
	return rec
}
