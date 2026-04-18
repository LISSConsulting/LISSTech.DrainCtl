//go:build windows

package telemetry

import (
	"context"
	"log/slog"
	"time"
)

// fiveMinWatermark is the minimum age (= bucket duration + late-sample slack)
// that must elapse past a 5-minute bucket's upper boundary before it is
// eligible for aggregation.
const fiveMinWatermark = 5*time.Minute + 5*time.Minute

// hourlyWatermark is the minimum age that must elapse past an hourly bucket's
// upper boundary before it is eligible for aggregation. Numerically identical
// to "all twelve constituent 5-minute buckets have passed their own watermark",
// so the hourly tier may safely read from `metrics_5min`.
const hourlyWatermark = time.Hour + 5*time.Minute

// Aggregator rolls metrics_raw into metrics_5min and metrics_5min into
// metrics_hourly on a configurable interval. Each tick runs the 5-minute
// rollup before the hourly rollup so the hourly pass observes the freshest
// 5-minute buckets produced by the same tick.
type Aggregator struct {
	db              *DB
	intervalSeconds int
}

// NewAggregator returns an Aggregator backed by the given open DB.
func NewAggregator(db *DB, intervalSeconds int) *Aggregator {
	return &Aggregator{db: db, intervalSeconds: intervalSeconds}
}

// Run starts the aggregation loop, blocking until ctx is cancelled.
// Callers should start this in a goroutine.
//
// Both rollups receive a single `now` per tick so eligibility boundaries are
// computed against one consistent wall-clock; staggering them lets a tick
// straddle the H+1h+5m boundary, materialising hour H from a 5-min tier that
// is missing its final bucket and producing a short hourly row.
func (a *Aggregator) Run(ctx context.Context) {
	interval := time.Duration(a.intervalSeconds) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.RollOnce(ctx, time.Now().UTC())
		}
	}
}

// RollOnce runs a single 5-min-then-hourly aggregation pass at the given
// wall-clock. Exposed so callers outside this package (notably HTTP handler
// tests that need to materialise all three tiers deterministically) can drive
// one tick synchronously instead of racing the Run ticker.
func (a *Aggregator) RollOnce(ctx context.Context, now time.Time) {
	a.roll5Min(ctx, now)
	a.rollHourly(ctx, now)
}

// roll5Min aggregates all eligible 5-minute buckets from metrics_raw.
//
// Eligible means: bucket_end + watermark ≤ now, i.e.
//
//	bucket_ts ≤ now - 10min, floored to the 5-minute boundary.
//
// Every eligible bucket is unconditionally recomputed so late-arriving raw
// samples within the watermark window are reflected on the next tick.
//
// `now` is supplied by the caller so paired `roll5Min` + `rollHourly` calls
// from a single tick share one cutoff (see Run).
func (a *Aggregator) roll5Min(ctx context.Context, now time.Time) {
	started := now
	cutoff := started.Add(-fiveMinWatermark)
	maxBucketMs := cutoff.Truncate(5 * time.Minute).UnixMilli()

	var rowsAffected int64
	outcome := "success"
	reason := ""

	res, err := a.db.writer.ExecContext(ctx, `
		INSERT INTO metrics_5min (bucket_ts, host, counter, avg_value, min_value, max_value, sample_count)
		SELECT
			(ts / 300000) * 300000 AS bucket_ts,
			host,
			counter,
			AVG(value),
			MIN(value),
			MAX(value),
			COUNT(*)
		FROM metrics_raw
		WHERE (ts / 300000) * 300000 <= ?
		GROUP BY (ts / 300000) * 300000, host, counter
		ON CONFLICT(host, bucket_ts, counter) DO UPDATE SET
			avg_value    = excluded.avg_value,
			min_value    = excluded.min_value,
			max_value    = excluded.max_value,
			sample_count = excluded.sample_count`,
		maxBucketMs,
	)
	if err != nil {
		outcome = "failure"
		reason = err.Error()
		slog.Warn("telemetry: 5min aggregator failed", "error", err)
	} else {
		rowsAffected, _ = res.RowsAffected()
		slog.Info("telemetry: 5min aggregator ran",
			"rows_affected", rowsAffected, "max_bucket_ms", maxBucketMs)
	}

	a.recordMaintenance(ctx, "aggregator_5min", started, time.Now().UTC(), outcome, reason, rowsAffected)
}

// rollHourly aggregates all eligible hourly buckets from metrics_5min using
// sample-count-weighted averages so the result is identical to aggregating the
// underlying raw samples directly.
//
// Eligible means: bucket_end + watermark ≤ now, i.e.
//
//	bucket_ts ≤ now - 1h - 5min, floored to the hour boundary.
//
// Every eligible bucket is unconditionally recomputed so late updates to the
// 5-minute tier (themselves driven by late raw samples within the 5-min
// watermark) are reflected on the next tick.
//
// `now` is supplied by the caller so paired `roll5Min` + `rollHourly` calls
// from a single tick share one cutoff (see Run).
func (a *Aggregator) rollHourly(ctx context.Context, now time.Time) {
	started := now
	cutoff := started.Add(-hourlyWatermark)
	maxBucketMs := cutoff.Truncate(time.Hour).UnixMilli()

	var rowsAffected int64
	outcome := "success"
	reason := ""

	res, err := a.db.writer.ExecContext(ctx, `
		INSERT INTO metrics_hourly (bucket_ts, host, counter, avg_value, min_value, max_value, sample_count)
		SELECT
			(bucket_ts / 3600000) * 3600000 AS hour_ts,
			host,
			counter,
			SUM(avg_value * sample_count) / SUM(sample_count),
			MIN(min_value),
			MAX(max_value),
			SUM(sample_count)
		FROM metrics_5min
		WHERE (bucket_ts / 3600000) * 3600000 <= ?
		GROUP BY (bucket_ts / 3600000) * 3600000, host, counter
		ON CONFLICT(host, bucket_ts, counter) DO UPDATE SET
			avg_value    = excluded.avg_value,
			min_value    = excluded.min_value,
			max_value    = excluded.max_value,
			sample_count = excluded.sample_count`,
		maxBucketMs,
	)
	if err != nil {
		outcome = "failure"
		reason = err.Error()
		slog.Warn("telemetry: hourly aggregator failed", "error", err)
	} else {
		rowsAffected, _ = res.RowsAffected()
		slog.Info("telemetry: hourly aggregator ran",
			"rows_affected", rowsAffected, "max_bucket_ms", maxBucketMs)
	}

	a.recordMaintenance(ctx, "aggregator_hourly", started, time.Now().UTC(), outcome, reason, rowsAffected)
}

// recordMaintenance upserts a maintenance_jobs row covering one aggregator run.
// Full instrumentation (via MaintenanceStore) lands in T048; per FR-032 the
// ETW + file log record emitted above must be paired with the row written here
// so the dashboard widget and the logs stay in sync.
func (a *Aggregator) recordMaintenance(
	ctx context.Context,
	name string,
	started, finished time.Time,
	outcome, reason string,
	rowsAffected int64,
) {
	_, err := a.db.writer.ExecContext(ctx, `
		INSERT INTO maintenance_jobs
			(name, started_ts, finished_ts, duration_ms, outcome, reason, rows_affected)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			started_ts    = excluded.started_ts,
			finished_ts   = excluded.finished_ts,
			duration_ms   = excluded.duration_ms,
			outcome       = excluded.outcome,
			reason        = excluded.reason,
			rows_affected = excluded.rows_affected`,
		name, started.UnixMilli(), finished.UnixMilli(),
		finished.Sub(started).Milliseconds(), outcome, reason, rowsAffected,
	)
	if err != nil {
		slog.Warn("telemetry: record maintenance_jobs failed", "name", name, "error", err)
	}
}
