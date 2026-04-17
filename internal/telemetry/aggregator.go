//go:build windows

package telemetry

import (
	"context"
	"log/slog"
	"time"
)

// hourlyWatermark is the minimum age a bucket's end must reach before it is
// eligible for aggregation. Prevents partial-hour buckets from being rolled
// while the raw tier is still accepting writes into them.
const hourlyWatermark = time.Hour + 5*time.Minute

// Aggregator rolls metrics_raw into metrics_hourly on a configurable interval.
// The 5-minute intermediate tier (metrics_5min) and the corresponding change
// of source for hourly aggregation are added in US3 (task T035).
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
func (a *Aggregator) Run(ctx context.Context) {
	interval := time.Duration(a.intervalSeconds) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.rollHourly(ctx)
		}
	}
}

// rollHourly aggregates all eligible hourly buckets from metrics_raw.
//
// Eligible means: bucket_end + hourlyWatermark ≤ now, i.e.
//
//	bucket_ts ≤ now - 1h - 5min, floored to the hour boundary.
//
// Every eligible bucket is unconditionally recomputed so late-arriving raw
// samples within the watermark window are reflected on the next tick.
func (a *Aggregator) rollHourly(ctx context.Context) {
	started := time.Now().UTC()
	cutoff := started.Add(-hourlyWatermark)
	maxBucketMs := cutoff.Truncate(time.Hour).UnixMilli()

	var rowsAffected int64
	outcome := "success"
	reason := ""

	res, err := a.db.writer.ExecContext(ctx, `
		INSERT INTO metrics_hourly (bucket_ts, host, counter, avg_value, min_value, max_value, sample_count)
		SELECT
			(ts / 3600000) * 3600000 AS bucket_ts,
			host,
			counter,
			AVG(value),
			MIN(value),
			MAX(value),
			COUNT(*)
		FROM metrics_raw
		WHERE (ts / 3600000) * 3600000 <= ?
		GROUP BY (ts / 3600000) * 3600000, host, counter
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
