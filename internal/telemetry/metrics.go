//go:build windows

package telemetry

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// futureSkewThreshold is the maximum permitted clock skew into the future
// before Append logs a WARN. Samples are stored regardless — clock sync is
// the operator's responsibility (spec.md §Assumptions, spec.md:121).
const futureSkewThreshold = 5 * time.Minute

// Tier identifies the resolution tier of a metrics query.
type Tier int

const (
	TierRaw     Tier = iota // metrics_raw: full-resolution, ~25 h retention
	TierFiveMin             // metrics_5min: 5-minute buckets, ~6 d retention (implemented in US3)
	TierHourly              // metrics_hourly: hourly buckets, configured retention
)

// TierName returns the canonical string identifier used in the HTTP contract.
func (t Tier) TierName() string {
	switch t {
	case TierRaw:
		return "raw"
	case TierFiveMin:
		return "5min"
	case TierHourly:
		return "hourly"
	default:
		return "unknown"
	}
}

// Sample is a single metric observation to be stored in metrics_raw.
type Sample struct {
	Ts      time.Time
	Host    string
	Counter string
	Value   float64
}

// CounterSeries holds parallel time/avg/min/max arrays for one counter.
// For TierRaw, Avg == Min == Max == the original value.
type CounterSeries struct {
	T   []int64
	Avg []float64
	Min []float64
	Max []float64
}

// Series is the result of a QueryRange call.
type Series struct {
	Host            string
	Tier            Tier
	From            time.Time
	To              time.Time
	OldestAvailable *time.Time // nil when the tier has no rows for this host
	NewestAvailable *time.Time // nil when the tier has no rows for this host
	Data            map[string]*CounterSeries
}

// MetricsStore provides write and read access to all metrics tiers.
type MetricsStore struct {
	db *DB
}

// NewMetricsStore wraps an open DB as a MetricsStore.
func NewMetricsStore(db *DB) *MetricsStore {
	return &MetricsStore{db: db}
}

// Append inserts samples into metrics_raw in a single batched transaction.
// ON CONFLICT DO NOTHING makes the call idempotent for duplicate (host, ts, counter) triples.
func (s *MetricsStore) Append(ctx context.Context, samples []Sample) error {
	if len(samples) == 0 {
		return nil
	}
	tx, err := s.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("telemetry: metrics Append begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO metrics_raw (ts, host, counter, value)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(host, ts, counter) DO NOTHING`)
	if err != nil {
		return fmt.Errorf("telemetry: metrics Append prepare: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	now := time.Now().UTC()
	skewCutoff := now.Add(futureSkewThreshold)
	skewCount := 0
	firstSkewIdx := -1

	for i := range samples {
		sm := &samples[i]
		ts := sm.Ts.UTC()
		if ts.After(skewCutoff) {
			if firstSkewIdx < 0 {
				firstSkewIdx = i
			}
			skewCount++
		}
		if _, err = stmt.ExecContext(ctx, ts.UnixMilli(), sm.Host, sm.Counter, sm.Value); err != nil {
			return fmt.Errorf("telemetry: metrics Append insert [%s %s]: %w",
				sm.Host, sm.Counter, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("telemetry: metrics Append commit: %w", err)
	}
	if skewCount > 0 {
		sm := &samples[firstSkewIdx]
		slog.Warn("telemetry: metrics Append received future-dated samples",
			"count", skewCount,
			"host", sm.Host,
			"counter", sm.Counter,
			"ts", sm.Ts.UTC().Format(time.RFC3339),
			"skew_seconds", int(sm.Ts.UTC().Sub(now).Seconds()))
	}
	return nil
}

// QueryRange reads the chosen tier for one host and time window.
// TierFiveMin is not supported until US3 (task T036); callers should use TierHourly as the coarser fallback.
// counters filters by counter name; nil or empty means "all known counters".
func (s *MetricsStore) QueryRange(
	ctx context.Context,
	host string,
	from, to time.Time,
	tier Tier,
	counters []string,
) (*Series, error) {
	oldest, newest, err := s.boundsForTier(ctx, host, tier)
	if err != nil {
		return nil, err
	}

	sr := &Series{
		Host:            host,
		Tier:            tier,
		From:            from,
		To:              to,
		OldestAvailable: oldest,
		NewestAvailable: newest,
		Data:            map[string]*CounterSeries{},
	}

	fromMs := from.UTC().UnixMilli()
	toMs := to.UTC().UnixMilli()

	switch tier {
	case TierRaw:
		if err := s.queryRaw(ctx, sr, host, fromMs, toMs, counters); err != nil {
			return nil, err
		}
	case TierHourly:
		if err := s.queryAggregated(ctx, sr, "metrics_hourly", host, fromMs, toMs, counters); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("telemetry: tier %q not available in this version", tier.TierName())
	}

	return sr, nil
}

func (s *MetricsStore) queryRaw(
	ctx context.Context,
	sr *Series,
	host string,
	fromMs, toMs int64,
	counters []string,
) error {
	q, args := buildRawQuery(host, fromMs, toMs, counters)
	rows, err := s.db.reader.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("telemetry: metrics_raw query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var tsMs int64
		var counter string
		var value float64
		if err := rows.Scan(&tsMs, &counter, &value); err != nil {
			return fmt.Errorf("telemetry: metrics_raw scan: %w", err)
		}
		cs := getOrMakeCounter(sr, counter)
		cs.T = append(cs.T, tsMs)
		cs.Avg = append(cs.Avg, value)
		cs.Min = append(cs.Min, value)
		cs.Max = append(cs.Max, value)
	}
	return rows.Err()
}

func (s *MetricsStore) queryAggregated(
	ctx context.Context,
	sr *Series,
	table string,
	host string,
	fromMs, toMs int64,
	counters []string,
) error {
	q, args := buildAggQuery(table, host, fromMs, toMs, counters)
	rows, err := s.db.reader.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("telemetry: %s query: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var bucketMs int64
		var counter string
		var avg, min, max float64
		if err := rows.Scan(&bucketMs, &counter, &avg, &min, &max); err != nil {
			return fmt.Errorf("telemetry: %s scan: %w", table, err)
		}
		cs := getOrMakeCounter(sr, counter)
		cs.T = append(cs.T, bucketMs)
		cs.Avg = append(cs.Avg, avg)
		cs.Min = append(cs.Min, min)
		cs.Max = append(cs.Max, max)
	}
	return rows.Err()
}

// tierBoundsSQL maps each Tier to a prebuilt bounds query (avoids runtime SQL formatting).
var tierBoundsSQL = map[Tier]string{
	TierRaw:     `SELECT MIN(ts), MAX(ts) FROM metrics_raw WHERE host = ?`,
	TierFiveMin: `SELECT MIN(bucket_ts), MAX(bucket_ts) FROM metrics_5min WHERE host = ?`,
	TierHourly:  `SELECT MIN(bucket_ts), MAX(bucket_ts) FROM metrics_hourly WHERE host = ?`,
}

// boundsForTier returns the oldest and newest timestamp the tier holds for the given host.
func (s *MetricsStore) boundsForTier(ctx context.Context, host string, tier Tier) (*time.Time, *time.Time, error) {
	q, ok := tierBoundsSQL[tier]
	if !ok {
		return nil, nil, fmt.Errorf("telemetry: unknown tier %d", int(tier))
	}

	var minMs, maxMs sql.NullInt64
	if err := s.db.reader.QueryRowContext(ctx, q, host).Scan(&minMs, &maxMs); err != nil {
		return nil, nil, fmt.Errorf("telemetry: bounds query (tier %s): %w", tier.TierName(), err)
	}

	var oldest, newest *time.Time
	if minMs.Valid {
		t := time.UnixMilli(minMs.Int64).UTC()
		oldest = &t
	}
	if maxMs.Valid {
		t := time.UnixMilli(maxMs.Int64).UTC()
		newest = &t
	}
	return oldest, newest, nil
}

func buildRawQuery(host string, fromMs, toMs int64, counters []string) (string, []any) {
	args := []any{host, fromMs, toMs}
	q := `SELECT ts, counter, value FROM metrics_raw
	      WHERE host = ? AND ts >= ? AND ts < ?`
	if len(counters) > 0 {
		q += " AND counter IN (" + placeholders(len(counters)) + ")"
		for _, c := range counters {
			args = append(args, c)
		}
	}
	q += " ORDER BY counter, ts ASC"
	return q, args
}

func buildAggQuery(table, host string, fromMs, toMs int64, counters []string) (string, []any) {
	args := []any{host, fromMs, toMs}
	// table is always an internal constant — not user input.
	q := fmt.Sprintf(`SELECT bucket_ts, counter, avg_value, min_value, max_value FROM %s
	      WHERE host = ? AND bucket_ts >= ? AND bucket_ts < ?`, table)
	if len(counters) > 0 {
		q += " AND counter IN (" + placeholders(len(counters)) + ")"
		for _, c := range counters {
			args = append(args, c)
		}
	}
	q += " ORDER BY counter, bucket_ts ASC"
	return q, args
}

func placeholders(n int) string {
	return strings.Repeat("?,", n-1) + "?"
}

func getOrMakeCounter(sr *Series, counter string) *CounterSeries {
	cs, ok := sr.Data[counter]
	if !ok {
		cs = &CounterSeries{}
		sr.Data[counter] = cs
	}
	return cs
}
