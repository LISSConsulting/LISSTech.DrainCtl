//go:build windows

package telemetry

import (
	"context"
	"fmt"
	"time"
)

// reconciliationPrincipal is the principal value written on every drift
// reconciliation row. Empty by design: the audit_principal partial index
// excludes empty principals, so reconciliation rows never appear in
// principal-filtered queries (data-model.md, FR-001a).
const reconciliationPrincipal = ""

// driftJobName is the maintenance_jobs.name written by Reconcile. The
// dashboard widget renders whatever names it finds, so this string is the
// stable identifier across the loop.
const driftJobName = "drift_reconciliation"

// DrainProbe is a snapshot of one host's current drain state passed to
// Reconcile. The caller (svc/handler.go) constructs this from registry.go's
// ReadDrainMode helper. Telemetry must not import the root drainctl package
// (cyclic), so the registry probe crosses the boundary as a plain struct.
type DrainProbe struct {
	Host        string
	ModeValue   int       // current DrainMode value (0..5)
	KeyModified time.Time // registry key LastWriteTime; zero if value absent
}

// Reconcile compares probe against the latest audit row for probe.Host and
// emits at most one reconciliation audit row when (a) the observed state
// differs from the last-known state, or (b) the registry KeyModified
// timestamp has advanced past the last audit row's ts even when the state
// matches (downtime A→B→A oscillation is invisible to a state-only check;
// FR-001a).
//
// Reconcile always upserts the drift_reconciliation maintenance_jobs row so
// the dashboard widget reflects that the job ran. rows_affected is the
// per-job count of reconciliation rows inserted (0 when no drift detected).
//
// MUST be invoked after MigrateJSONL has completed so LatestByHost sees
// imported rows (FR-020) and before live ingest starts. T044 wires that
// ordering at the call site.
func Reconcile(ctx context.Context, db *DB, audit *AuditStore, probe DrainProbe, now time.Time) error {
	started := now.UTC()

	latest, err := audit.LatestByHost(ctx)
	if err != nil {
		_ = recordDriftJob(ctx, db, started, time.Now().UTC(), "failure", err.Error(), 0)
		return fmt.Errorf("telemetry: reconcile latestByHost: %w", err)
	}

	rowsAffected := 0
	last, hasLast := latest[probe.Host]

	needsRow := false
	prevState := 0
	var beforeTs *time.Time

	switch {
	case !hasLast:
		// No prior audit history for this host. Live ingest writes the
		// first row; reconciliation has nothing to fill in.
	case last.NewState != probe.ModeValue:
		needsRow = true
		prevState = last.NewState
		ts := last.Ts
		beforeTs = &ts
	case !probe.KeyModified.IsZero() && probe.KeyModified.After(last.Ts):
		// State matches but the registry was modified after the last audit
		// row — A→B→A oscillation while the service was down.
		needsRow = true
		prevState = last.NewState
		ts := last.Ts
		beforeTs = &ts
	}

	if needsRow {
		var keyMod *time.Time
		if !probe.KeyModified.IsZero() {
			km := probe.KeyModified.UTC()
			keyMod = &km
		}
		rec := AuditRecord{
			Ts:             now.UTC(),
			Host:           probe.Host,
			PrevState:      prevState,
			NewState:       probe.ModeValue,
			Principal:      reconciliationPrincipal,
			Reason:         fmt.Sprintf("service-downtime drift: last-known %d, observed %d", prevState, probe.ModeValue),
			KeyModifiedTs:  keyMod,
			Reconciliation: true,
			BeforeTs:       beforeTs,
		}
		if err := audit.Append(ctx, rec); err != nil {
			_ = recordDriftJob(ctx, db, started, time.Now().UTC(), "failure", err.Error(), 0)
			return fmt.Errorf("telemetry: reconcile append: %w", err)
		}
		rowsAffected = 1
	}

	if err := recordDriftJob(ctx, db, started, time.Now().UTC(), "success", "", rowsAffected); err != nil {
		return fmt.Errorf("telemetry: reconcile maintenance row: %w", err)
	}
	return nil
}

// recordDriftJob upserts the drift_reconciliation maintenance_jobs row so
// the dashboard widget always reflects the latest reconciliation outcome
// (FR-029). T047 introduces MaintenanceStore.UpsertJob; until then we write
// the row inline rather than preempt that design.
func recordDriftJob(ctx context.Context, db *DB, started, finished time.Time, outcome, reason string, rowsAffected int) error {
	startMs := started.UTC().UnixMilli()
	finishMs := finished.UTC().UnixMilli()
	durMs := finishMs - startMs
	if durMs < 0 {
		durMs = 0
	}
	_, err := db.writer.ExecContext(ctx,
		`INSERT INTO maintenance_jobs (name, started_ts, finished_ts, duration_ms, outcome, reason, rows_affected)
         VALUES (?, ?, ?, ?, ?, ?, ?)
         ON CONFLICT(name) DO UPDATE SET
            started_ts=excluded.started_ts,
            finished_ts=excluded.finished_ts,
            duration_ms=excluded.duration_ms,
            outcome=excluded.outcome,
            reason=excluded.reason,
            rows_affected=excluded.rows_affected`,
		driftJobName, startMs, finishMs, durMs, outcome, reason, rowsAffected)
	return err
}
