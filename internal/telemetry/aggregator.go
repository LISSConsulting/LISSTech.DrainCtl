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
	cutoff := time.Now().UTC().Add(-hourlyWatermark)
	maxBucketMs := cutoff.Truncate(time.Hour).UnixMilli()

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
		slog.Warn("telemetry: hourly aggregator failed", "error", err)
		return
	}
	rows, _ := res.RowsAffected()
	slog.Info("telemetry: hourly aggregator ran", "rows_affected", rows, "max_bucket_ms", maxBucketMs)
}
