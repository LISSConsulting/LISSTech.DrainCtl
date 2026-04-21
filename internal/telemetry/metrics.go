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
	TierFiveMin             // metrics_5min: 5-minute buckets, ~6 d retention
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
// counters filters by counter name; nil or empty means "all known counters".
func (s *MetricsStore) QueryRange(
	ctx context.Context,
	host string,
	from, to time.Time,
	tier Tier,
	counters []string,
) (*Series, error) {
	oldest, newest, err := s.BoundsForTier(ctx, host, tier)
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
	case TierFiveMin:
		if err := s.queryAggregated(ctx, sr, "metrics_5min", host, fromMs, toMs, counters); err != nil {
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

// BoundsForTier returns the oldest and newest timestamp the tier holds for the given host.
// Returned values are nil when the tier has no rows for the host.
func (s *MetricsStore) BoundsForTier(ctx context.Context, host string, tier Tier) (*time.Time, *time.Time, error) {
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

// Fleet query builders — aggregate across multiple hosts by time bucket.

func buildRawQueryFleet(hosts []string, fromMs, toMs int64, counters []string) (string, []any) {
	args := make([]any, 0, len(hosts)+2+len(counters))
	for _, h := range hosts {
		args = append(args, h)
	}
	args = append(args, fromMs, toMs)
	q := `SELECT ts, counter, AVG(value), MIN(value), MAX(value) FROM metrics_raw
	      WHERE host IN (` + placeholders(len(hosts)) + `) AND ts >= ? AND ts < ?`
	if len(counters) > 0 {
		q += " AND counter IN (" + placeholders(len(counters)) + ")"
		for _, c := range counters {
			args = append(args, c)
		}
	}
	q += " GROUP BY ts, counter ORDER BY counter, ts ASC"
	return q, args
}

func buildAggQueryFleet(table string, hosts []string, fromMs, toMs int64, counters []string) (string, []any) {
	args := make([]any, 0, len(hosts)+2+len(counters))
	for _, h := range hosts {
		args = append(args, h)
	}
	args = append(args, fromMs, toMs)
	// table is always an internal constant — not user input.
	// Sample-count-weighted average matches rollHourly weighting so buckets with
	// fewer raw samples (e.g. partial windows) don't skew the fleet aggregate.
	q := fmt.Sprintf(`SELECT bucket_ts, counter,
	      SUM(avg_value * sample_count) / NULLIF(SUM(sample_count), 0),
	      MIN(min_value), MAX(max_value) FROM %s
	      WHERE host IN (`+placeholders(len(hosts))+`) AND bucket_ts >= ? AND bucket_ts < ?`, table)
	if len(counters) > 0 {
		q += " AND counter IN (" + placeholders(len(counters)) + ")"
		for _, c := range counters {
			args = append(args, c)
		}
	}
	q += " GROUP BY bucket_ts, counter ORDER BY counter, bucket_ts ASC"
	return q, args
}

// BoundsForTierFleet returns the oldest and newest timestamp the tier holds
// across any of the given hosts. Returns nil/nil when no rows exist.
func (s *MetricsStore) BoundsForTierFleet(ctx context.Context, hosts []string, tier Tier) (*time.Time, *time.Time, error) {
	if len(hosts) == 0 {
		return nil, nil, nil
	}
	ph := placeholders(len(hosts))
	var q string
	switch tier {
	case TierRaw:
		q = `SELECT MIN(ts), MAX(ts) FROM metrics_raw WHERE host IN (` + ph + `)`
	case TierFiveMin:
		q = `SELECT MIN(bucket_ts), MAX(bucket_ts) FROM metrics_5min WHERE host IN (` + ph + `)`
	case TierHourly:
		q = `SELECT MIN(bucket_ts), MAX(bucket_ts) FROM metrics_hourly WHERE host IN (` + ph + `)`
	default:
		return nil, nil, fmt.Errorf("telemetry: unknown tier %v for fleet bounds", tier)
	}
	args := make([]any, len(hosts))
	for i, h := range hosts {
		args[i] = h
	}
	var minMs, maxMs sql.NullInt64
	if err := s.db.reader.QueryRowContext(ctx, q, args...).Scan(&minMs, &maxMs); err != nil {
		return nil, nil, fmt.Errorf("telemetry: fleet bounds tier %v: %w", tier, err)
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

// QueryRangeFleet reads the chosen tier for all given hosts, aggregating values
// per time bucket across the fleet. Returns an empty Series when hosts is empty
// or no data exists in the window — consistent with per-host QueryRange behaviour.
func (s *MetricsStore) QueryRangeFleet(
	ctx context.Context,
	hosts []string,
	from, to time.Time,
	tier Tier,
	counters []string,
) (*Series, error) {
	if len(hosts) == 0 {
		return &Series{Host: "_fleet", Tier: tier, From: from, To: to, Data: map[string]*CounterSeries{}}, nil
	}
	oldest, newest, err := s.BoundsForTierFleet(ctx, hosts, tier)
	if err != nil {
		return nil, err
	}
	sr := &Series{
		Host:            "_fleet",
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
		if err := s.queryRawFleet(ctx, sr, hosts, fromMs, toMs, counters); err != nil {
			return nil, err
		}
	case TierFiveMin:
		if err := s.queryAggregatedFleet(ctx, sr, "metrics_5min", hosts, fromMs, toMs, counters); err != nil {
			return nil, err
		}
	case TierHourly:
		if err := s.queryAggregatedFleet(ctx, sr, "metrics_hourly", hosts, fromMs, toMs, counters); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("telemetry: tier %q not available in this version", tier.TierName())
	}
	return sr, nil
}

func (s *MetricsStore) queryRawFleet(
	ctx context.Context,
	sr *Series,
	hosts []string,
	fromMs, toMs int64,
	counters []string,
) error {
	q, args := buildRawQueryFleet(hosts, fromMs, toMs, counters)
	rows, err := s.db.reader.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("telemetry: fleet metrics_raw query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var tsMs int64
		var counter string
		var avg, min, max float64
		if err := rows.Scan(&tsMs, &counter, &avg, &min, &max); err != nil {
			return fmt.Errorf("telemetry: fleet metrics_raw scan: %w", err)
		}
		cs := getOrMakeCounter(sr, counter)
		cs.T = append(cs.T, tsMs)
		cs.Avg = append(cs.Avg, avg)
		cs.Min = append(cs.Min, min)
		cs.Max = append(cs.Max, max)
	}
	return rows.Err()
}

func (s *MetricsStore) queryAggregatedFleet(
	ctx context.Context,
	sr *Series,
	table string,
	hosts []string,
	fromMs, toMs int64,
	counters []string,
) error {
	q, args := buildAggQueryFleet(table, hosts, fromMs, toMs, counters)
	rows, err := s.db.reader.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("telemetry: fleet %s query: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var bucketMs int64
		var counter string
		var avg, min, max float64
		if err := rows.Scan(&bucketMs, &counter, &avg, &min, &max); err != nil {
			return fmt.Errorf("telemetry: fleet %s scan: %w", table, err)
		}
		cs := getOrMakeCounter(sr, counter)
		cs.T = append(cs.T, bucketMs)
		cs.Avg = append(cs.Avg, avg)
		cs.Min = append(cs.Min, min)
		cs.Max = append(cs.Max, max)
	}
	return rows.Err()
}

// NearestCounters returns, for each named counter, the value of the
// metrics_raw sample whose ts is closest to target within ±toleranceMs.
// Counters with no sample inside the window are absent from the result.
//
// Used by the pipe handler to enrich drainctl history audit rows with the
// perf snapshot that was active at each transition — the audit table stores
// only transition metadata, so CPU/input-delay/session counters have to be
// joined from metrics_raw at read time.
func (s *MetricsStore) NearestCounters(
	ctx context.Context,
	host string,
	target time.Time,
	toleranceMs int64,
	counters []string,
) (map[string]float64, error) {
	if len(counters) == 0 {
		return map[string]float64{}, nil
	}
	q, args := buildNearestCountersQuery(host, target.UTC().UnixMilli(), toleranceMs, counters)
	rows, err := s.db.reader.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("telemetry: nearest counters query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]float64, len(counters))
	for rows.Next() {
		var counter string
		var value float64
		if err := rows.Scan(&counter, &value); err != nil {
			return nil, fmt.Errorf("telemetry: nearest counters scan: %w", err)
		}
		out[counter] = value
	}
	return out, rows.Err()
}

// buildNearestCountersQuery mirrors buildRawQuery / buildAggQuery in this
// file: it returns a ready-to-exec SQL string plus the positional arg slice.
// Only "?" placeholders are embedded — no user data is concatenated — so the
// result is safe to pass directly to QueryContext.
func buildNearestCountersQuery(host string, targetMs, toleranceMs int64, counters []string) (string, []any) {
	args := []any{targetMs, host, targetMs - toleranceMs, targetMs + toleranceMs}
	q := `SELECT counter, value FROM (
	    SELECT counter, value,
	           ROW_NUMBER() OVER (PARTITION BY counter ORDER BY ABS(ts - ?)) AS rn
	    FROM metrics_raw
	    WHERE host = ? AND ts >= ? AND ts <= ?`
	q += " AND counter IN (" + placeholders(len(counters)) + ")"
	for _, c := range counters {
		args = append(args, c)
	}
	q += ") WHERE rn = 1"
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
