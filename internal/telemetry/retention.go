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
	MetricsDays                   int
	AuditDays                     int
	CurrentAcknowledgementAuditID int64
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

// RunOnce performs a single retention pass at now: it first recovers running
// investigation attempts whose applicable lease has expired, then deletes each
// expired tier in chunks, runs incremental_vacuum, and checkpoints the WAL.
// The maintenance_jobs "retention" row reflects every recovery and deletion
// mutation in the pass, including partial-tier failures.
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

	nowMs := now.UTC().UnixMilli()
	recoveryRows, err := r.recoverInvestigationAttempts(ctx, nowMs)
	if err != nil {
		result.Outcome = "failure"
		result.Reason = fmt.Sprintf("investigation_recovery: %s", err.Error())
		slog.Warn("telemetry: retention investigation recovery failed", "error", err)
	} else {
		slog.Info("telemetry: retention investigation recovery", "rows", recoveryRows)
	}

	tiers := []struct {
		label string
		query string
		args  []int64
	}{
		{
			label: "metrics_raw",
			query: `DELETE FROM metrics_raw WHERE (host, ts, counter) IN (
                        SELECT host, ts, counter FROM metrics_raw WHERE ts < ? LIMIT 10000)`,
			args: []int64{rawCutoff},
		},
		{
			label: "metrics_5min",
			query: `DELETE FROM metrics_5min WHERE (host, bucket_ts, counter) IN (
                        SELECT host, bucket_ts, counter FROM metrics_5min WHERE bucket_ts < ? LIMIT 10000)`,
			args: []int64{fiveMinCutoff},
		},
		{
			label: "metrics_hourly",
			query: `DELETE FROM metrics_hourly WHERE (host, bucket_ts, counter) IN (
                        SELECT host, bucket_ts, counter FROM metrics_hourly WHERE bucket_ts < ? LIMIT 10000)`,
			args: []int64{hourlyCutoff},
		},
		{
			label: "audit",
			query: `DELETE FROM audit WHERE (ts, host, new_state) IN (
                        SELECT ts, host, new_state FROM audit WHERE ts < ? LIMIT 10000)`,
			args: []int64{auditCutoff},
		},
		{
			label: "event_spikes",
			query: `DELETE FROM event_spikes WHERE id IN (
                        SELECT id FROM event_spikes WHERE window_start_ms < ? LIMIT 10000)`,
			args: []int64{auditCutoff},
		},
		{
			label: "investigation_attempts",
			query: `DELETE FROM investigation_attempts WHERE id IN (
                        SELECT id FROM investigation_attempts
                        WHERE created_at_ms < ?
                          AND (state = 'queued' OR (state IN ('completed', 'insufficient_evidence', 'failed')
                               AND completed_at_ms < ?))
                        LIMIT 10000)`,
			args: []int64{auditCutoff, nowMs},
		},
		{
			label: "session_drop_anomalies",
			query: `DELETE FROM session_drop_anomalies WHERE id IN (
                        SELECT id FROM session_drop_anomalies WHERE detected_at_ms < ? LIMIT 10000)`,
			args: []int64{auditCutoff},
		},
		{
			label: "investigation_privacy_acknowledgements",
			query: `DELETE FROM investigation_privacy_acknowledgements WHERE id IN (
                        SELECT id FROM investigation_privacy_acknowledgements
                        WHERE accepted_at_ms < ?
                          AND NOT (
                              id = ?
                              AND acknowledgement_version = 'openai_responses_privacy_v1'
                              AND clause_set_hash = X'dc6150c7a127e143eb2ff247e804564d2085f879300e73b76556a290666ecba0'
                              AND clause_set_marker = 'openai_responses_privacy_v1_complete_clauses'
                          )
                        LIMIT 10000)`,
			args: []int64{auditCutoff, settings.CurrentAcknowledgementAuditID},
		},
	}

	var total = recoveryRows
	for _, tier := range tiers {
		deleted, err := r.deleteChunked(ctx, tier.query, tier.args...)
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

// deleteChunked issues query with its arguments repeatedly until a call deletes
// fewer than deleteChunkSize rows, bounding each transaction so readers see
// steady progress and the writer lock is released between chunks.
func (r *Retention) deleteChunked(ctx context.Context, query string, args ...int64) (int64, error) {
	var total int64
	values := make([]any, len(args))
	for i := range args {
		values[i] = args[i]
	}
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		res, err := r.db.writer.ExecContext(ctx, query, values...)
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

// recoverInvestigationAttempts makes expired running work terminal before
// ordinary retention. An active send or finalization lease protects evidence.
// Once neither lease is live, an unauthorised attempt was never sent; an
// authorised one has an uncertain provider outcome and cannot be resent.
func (r *Retention) recoverInvestigationAttempts(ctx context.Context, nowMs int64) (int64, error) {
	res, err := r.db.writer.ExecContext(ctx,
		`UPDATE investigation_attempts
		 SET state = 'failed',
		     terminal_reason = CASE
		         WHEN send_authorized_at_ms IS NULL THEN 'interrupted'
		         ELSE 'storage_unavailable'
		     END,
		     send_lease_expires_at_ms = NULL,
		     finalization_lease_expires_at_ms = NULL,
		     completed_at_ms = ?
		 WHERE state = 'running'
		   AND (send_lease_expires_at_ms IS NULL OR send_lease_expires_at_ms <= ?)
		   AND (finalization_lease_expires_at_ms IS NULL OR finalization_lease_expires_at_ms <= ?)`,
		nowMs, nowMs, nowMs,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// jitter returns a duration in [-retentionJitterMax, +retentionJitterMax).
func jitter() time.Duration {
	span := int64(2 * retentionJitterMax)
	return time.Duration(rand.Int64N(span)) - retentionJitterMax //nolint:gosec // non-security scheduling jitter
}
