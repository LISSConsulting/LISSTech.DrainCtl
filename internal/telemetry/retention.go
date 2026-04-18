//go:build windows

package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"
)

const (
	rawRetention           = 25 * time.Hour
	fiveMinRetention       = 6 * 24 * time.Hour
	deleteChunkSize        = 10000
	incrementalVacuumPages = 5000
	retentionJitterMax     = 30 * time.Second
)

// RetentionSettings holds the per-tier retention windows. A provider closure
// is supplied to NewRetention so each pass reads the latest config without
// rebuilding the worker on hot-reload.
type RetentionSettings struct {
	MetricsDays int
	AuditDays   int
}

// Retention purges expired rows from each metrics tier and the audit table,
// then runs incremental_vacuum and drives the WAL checkpoint policy
// (tasks.md T004a) on the dedicated checkpoint connection.
type Retention struct {
	db              *DB
	intervalMinutes int
	provider        func() RetentionSettings
	maintenance     *MaintenanceStore
}

// NewRetention returns a retention worker bound to db. intervalMinutes drives
// the wake cadence; provider is called at the start of every pass to fetch
// the current per-tier retention windows.
func NewRetention(db *DB, intervalMinutes int, provider func() RetentionSettings) *Retention {
	return &Retention{
		db:              db,
		intervalMinutes: intervalMinutes,
		provider:        provider,
		maintenance:     NewMaintenanceStore(db),
	}
}

// Run drives the retention loop on a jittered interval until ctx is cancelled.
// Each wake is base ± 30s so concurrent DrainCtl instances on the same host
// don't synchronise their WAL-holding work onto the same second.
func (r *Retention) Run(ctx context.Context) {
	base := time.Duration(r.intervalMinutes) * time.Minute
	for {
		t := time.NewTimer(base + jitter())
		select {
		case <-ctx.Done():
			if !t.Stop() {
				<-t.C
			}
			return
		case <-t.C:
			r.RunOnce(ctx, time.Now().UTC())
		}
	}
}

// RunOnce performs a single retention pass at now: chunked DELETE on each
// tier against its cutoff, then PRAGMA incremental_vacuum, then the WAL
// checkpoint. The maintenance_jobs "retention" row reflects the full pass,
// including partial-tier failures (outcome=failure, reason=<first error>,
// rows_affected=<sum across tiers that succeeded>).
func (r *Retention) RunOnce(ctx context.Context, now time.Time) Result {
	started := now
	result := Result{Started: started, Outcome: "success"}

	settings := r.provider()

	// Align raw/5-min cutoffs DOWN to the next-larger aggregation bucket so
	// retention only ever deletes whole source buckets. If the cutoff were
	// mid-bucket, the aggregator (which recomputes any eligible bucket whose
	// source rows still exist) could overwrite a materialised aggregate using
	// a partial slice of raw data and corrupt avg/min/max.
	rawCutoff := now.Add(-rawRetention).Truncate(5 * time.Minute).UnixMilli()
	fiveMinCutoff := now.Add(-fiveMinRetention).Truncate(time.Hour).UnixMilli()
	hourlyCutoff := now.Add(-time.Duration(settings.MetricsDays) * 24 * time.Hour).UnixMilli()
	auditCutoff := now.Add(-time.Duration(settings.AuditDays) * 24 * time.Hour).UnixMilli()

	tiers := []struct {
		label  string
		query  string
		cutoff int64
	}{
		{
			label: "metrics_raw",
			query: `DELETE FROM metrics_raw WHERE (host, ts, counter) IN (
                        SELECT host, ts, counter FROM metrics_raw WHERE ts < ? LIMIT 10000)`,
			cutoff: rawCutoff,
		},
		{
			label: "metrics_5min",
			query: `DELETE FROM metrics_5min WHERE (host, bucket_ts, counter) IN (
                        SELECT host, bucket_ts, counter FROM metrics_5min WHERE bucket_ts < ? LIMIT 10000)`,
			cutoff: fiveMinCutoff,
		},
		{
			label: "metrics_hourly",
			query: `DELETE FROM metrics_hourly WHERE (host, bucket_ts, counter) IN (
                        SELECT host, bucket_ts, counter FROM metrics_hourly WHERE bucket_ts < ? LIMIT 10000)`,
			cutoff: hourlyCutoff,
		},
		{
			label: "audit",
			query: `DELETE FROM audit WHERE (ts, host, new_state) IN (
                        SELECT ts, host, new_state FROM audit WHERE ts < ? LIMIT 10000)`,
			cutoff: auditCutoff,
		},
	}

	var total int64
	for _, tier := range tiers {
		deleted, err := r.deleteChunked(ctx, tier.query, tier.cutoff)
		total += deleted
		if err != nil {
			slog.Warn("telemetry: retention tier failed",
				"tier", tier.label, "deleted", deleted, "error", err)
			if result.Outcome != "failure" {
				result.Outcome = "failure"
				result.Reason = fmt.Sprintf("%s: %s", tier.label, err.Error())
			}
			continue
		}
		slog.Info("telemetry: retention tier pruned",
			"tier", tier.label, "rows", deleted)
	}

	if _, err := r.db.writer.ExecContext(ctx,
		fmt.Sprintf("PRAGMA incremental_vacuum(%d)", incrementalVacuumPages)); err != nil {
		slog.Warn("telemetry: incremental_vacuum failed", "error", err)
	}

	r.db.WALCheckpoint(ctx)

	result.RowsAffected = total
	result.Finished = time.Now().UTC()

	if err := r.maintenance.UpsertJob(ctx, "retention", result); err != nil {
		slog.Warn("telemetry: record maintenance_jobs failed",
			"name", "retention", "error", err)
	}

	return result
}

// deleteChunked issues query with cutoff repeatedly until a call deletes
// fewer than deleteChunkSize rows, bounding each transaction so readers see
// steady progress and the writer lock is released between chunks.
func (r *Retention) deleteChunked(ctx context.Context, query string, cutoff int64) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		res, err := r.db.writer.ExecContext(ctx, query, cutoff)
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n < deleteChunkSize {
			return total, nil
		}
	}
}

// jitter returns a duration in [-retentionJitterMax, +retentionJitterMax).
func jitter() time.Duration {
	span := int64(2 * retentionJitterMax)
	return time.Duration(rand.Int64N(span)) - retentionJitterMax //nolint:gosec // non-security scheduling jitter
}
