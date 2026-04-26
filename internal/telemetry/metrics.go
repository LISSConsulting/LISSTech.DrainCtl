//go:build windows

package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// futureSkewThreshold is the maximum permitted clock skew into the future
// before Append logs a WARN. Samples are stored regardless — clock sync is
// the operator's responsibility (spec.md §Assumptions, spec.md:121).
const futureSkewThreshold = 5 * time.Minute

// metricsAppendSQL is the constant insert prepared once at MetricsStore
// construction. The previous inline form re-prepared on every Append; with
// per-host heartbeats that adds up. Whitespace + ON CONFLICT clause are
// preserved verbatim so the cached plan matches the prior inline plan.
const metricsAppendSQL = "INSERT INTO metrics_raw (ts, host, counter, value) VALUES (?, ?, ?, ?) ON CONFLICT(host, ts, counter) DO NOTHING"

// errReadOnlyDB is returned by NewMetricsStore when the supplied *DB has no
// writer pool (e.g. produced by OpenReadOnly). Self-defending guard — no
// current caller passes a read-only DB, but the contract should fail loudly
// rather than nil-panic on first Append.
var errReadOnlyDB = errors.New("telemetry: NewMetricsStore requires a writer-capable DB")

// errStoreClosed is returned by Append after Close has been called. The
// store rejects further work rather than re-preparing or nil-panicking.
var errStoreClosed = errors.New("telemetry: MetricsStore is closed")

// prepareWriter is a test seam for verifying that the cached statement is
// prepared exactly once across an Append loop. Production sets it to a thin
// wrapper around (*sql.DB).PrepareContext; tests swap in a counting wrapper.
// Same pattern as evtspike.subscribe / dashboard.acquireServerCredentials.
var prepareWriter = func(ctx context.Context, w *sql.DB, sqlStr string) (*sql.Stmt, error) {
	return w.PrepareContext(ctx, sqlStr)
}

// appendHook is a deterministic test seam used by
// TestMetricsStoreCloseDuringAppendIsSafe to drive the close-during-Append
// race. Production no-op. Position inside Append: AFTER the closed-check,
// BEFORE BeginTx — so a blocked hook holds the RLock with no DB tx open.
var appendHook = func() {}

// txBindStmt is the seam every Append goes through to bind the cached
// *sql.Stmt to the per-call transaction. Production wraps tx.StmtContext;
// tests count invocations to prove Append actually uses the cached stmt
// instead of regressing back to a per-call tx.PrepareContext. A regression
// that replaced this with a fresh PrepareContext would not increment the
// counter and the prepare-reuse test would fail.
var txBindStmt = func(ctx context.Context, tx *sql.Tx, stmt *sql.Stmt) *sql.Stmt {
	return tx.StmtContext(ctx, stmt)
}

// Tier identifies the resolution tier of a metrics query.
type Tier int

const (
	TierRaw     Tier = iota // metrics_raw: full-resolution, ~25 h retention
	TierOneMin              // virtual tier: 1-minute GROUP BY on metrics_raw (no table; bounded by raw retention)
	TierFiveMin             // metrics_5min: 5-minute buckets, ~6 d retention
	TierHourly              // metrics_hourly: hourly buckets, configured retention
)

// TierName returns the canonical string identifier used in the HTTP contract.
func (t Tier) TierName() string {
	switch t {
	case TierRaw:
		return "raw"
	case TierOneMin:
		return "1min"
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
	db         *DB
	stmtMu     sync.RWMutex // serializes Append (RLock) against Close (Lock)
	appendStmt *sql.Stmt    // nil after Close; set under Lock
}

// NewMetricsStore prepares the cached metrics-insert statement and returns a
// store ready for Append/QueryRange. The metricsAppendSQL plan is parsed
// once here instead of per-call inside Append — heartbeats (one Append per
// host per minute) used to re-prepare on every call.
//
// The db.writer == nil guard is required: OpenReadOnly returns a *DB with a
// nil writer pool, and the constructor's contract is to fail loudly rather
// than nil-panic on the first Append.
func NewMetricsStore(ctx context.Context, db *DB) (*MetricsStore, error) {
	if db == nil || db.writer == nil {
		return nil, errReadOnlyDB
	}
	stmt, err := prepareWriter(ctx, db.writer, metricsAppendSQL)
	if err != nil {
		return nil, fmt.Errorf("telemetry: prepare metrics insert: %w", err)
	}
	return &MetricsStore{db: db, appendStmt: stmt}, nil
}

// Close releases the cached append statement. Idempotent — a second call is
// a no-op. Must be invoked before the underlying DB's Close on shutdown so
// the prepared statement is finalised before its connection pool tears down.
func (s *MetricsStore) Close() error {
	s.stmtMu.Lock()
	defer s.stmtMu.Unlock()
	if s.appendStmt == nil {
		return nil
	}
	err := s.appendStmt.Close()
	s.appendStmt = nil
	return err
}

// Append inserts samples into metrics_raw in a single batched transaction.
// ON CONFLICT DO NOTHING makes the call idempotent for duplicate (host, ts, counter) triples.
//
// The prepared statement is acquired under stmtMu.RLock so Close (which
// takes the write lock) waits for in-flight Appends to finish. tx.StmtContext
// returns a tx-scoped wrapper that does NOT own the cached *sql.Stmt — the
// wrapper is released when the tx commits or rolls back.
func (s *MetricsStore) Append(ctx context.Context, samples []Sample) error {
	if len(samples) == 0 {
		return nil
	}
	s.stmtMu.RLock()
	defer s.stmtMu.RUnlock()
	if s.appendStmt == nil {
		return errStoreClosed
	}
	appendHook()
	tx, err := s.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("telemetry: metrics Append begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	stmt := txBindStmt(ctx, tx, s.appendStmt)
	// no defer stmt.Close() — tx-scoped wrapper does not own underlying

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
	case TierOneMin:
		if err := s.queryOneMin(ctx, sr, host, fromMs, toMs, counters); err != nil {
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

// queryOneMin buckets metrics_raw into 1-minute groups on the fly. No
// materialised table — short windows (≤1h) easily fit within the raw tier's
// ~25-hour retention, so the on-the-fly GROUP BY avoids a whole separate
// aggregator + storage cost for a resolution only used by the 15M/1H pills.
func (s *MetricsStore) queryOneMin(
	ctx context.Context,
	sr *Series,
	host string,
	fromMs, toMs int64,
	counters []string,
) error {
	q, args := buildOneMinQuery(host, fromMs, toMs, counters)
	rows, err := s.db.reader.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("telemetry: metrics_raw 1min query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var bucketMs int64
		var counter string
		var avg, min, max float64
		if err := rows.Scan(&bucketMs, &counter, &avg, &min, &max); err != nil {
			return fmt.Errorf("telemetry: metrics_raw 1min scan: %w", err)
		}
		cs := getOrMakeCounter(sr, counter)
		cs.T = append(cs.T, bucketMs)
		cs.Avg = append(cs.Avg, avg)
		cs.Min = append(cs.Min, min)
		cs.Max = append(cs.Max, max)
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
// TierOneMin shares metrics_raw's bounds — it's a virtual tier over the same rows.
var tierBoundsSQL = map[Tier]string{
	TierRaw:     `SELECT MIN(ts), MAX(ts) FROM metrics_raw WHERE host = ?`,
	TierOneMin:  `SELECT MIN(ts), MAX(ts) FROM metrics_raw WHERE host = ?`,
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

// buildOneMinQuery groups metrics_raw into 1-minute buckets using integer
// division on ts (milliseconds). Returns (bucket_ms, counter, avg, min, max).
func buildOneMinQuery(host string, fromMs, toMs int64, counters []string) (string, []any) {
	args := []any{host, fromMs, toMs}
	q := `SELECT (ts / 60000) * 60000 AS bucket_ts, counter,
	             AVG(value), MIN(value), MAX(value)
	      FROM metrics_raw
	      WHERE host = ? AND ts >= ? AND ts < ?`
	if len(counters) > 0 {
		q += " AND counter IN (" + placeholders(len(counters)) + ")"
		for _, c := range counters {
			args = append(args, c)
		}
	}
	q += " GROUP BY bucket_ts, counter ORDER BY counter, bucket_ts ASC"
	return q, args
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
//
// Most counters (cpu_pct, input_delay_p95_ms, mem_*, rfx_*) represent rates or
// ratios per host, so fleet aggregation averages across hosts. A handful of
// counters are per-host counts — the fleet value must be the sum, not the
// average, so the chart's Sessions line matches the CounterGrid's Sessions
// tile. summableFleetCounters is the allow-list used by every fleet query
// builder to pick SUM over AVG.
var summableFleetCounters = []string{
	"sessions_total",
	"sessions_active",
	"sessions_disconnected",
	"sessions_max",
}

// summableCase returns a SQLite CASE expression that evaluates sumExpr when
// the `counter` column matches one of summableFleetCounters, otherwise avgExpr.
// Counter names are compile-time constants — safe to inline as string literals.
func summableCase(sumExpr, avgExpr string) string {
	quoted := make([]string, len(summableFleetCounters))
	for i, c := range summableFleetCounters {
		quoted[i] = "'" + c + "'"
	}
	return "CASE WHEN counter IN (" + strings.Join(quoted, ",") + ") THEN " + sumExpr + " ELSE " + avgExpr + " END"
}

// MinRawFleetBucketMs is the lower bound for raw-fleet bucketing. Set to
// 30 s — well above the PUT /api/v1/settings validator's 10 s floor for
// sample_interval — because a per-host phase difference of one full poll
// cycle can otherwise put adjacent hosts in adjacent buckets, producing
// the alternating-host zigzag that bucketing exists to prevent. 30 s is
// twice the smallest validator-allowed sample_interval and matches the
// default cadence.
const MinRawFleetBucketMs = 30_000

// DefaultRawFleetBucketMs is used when a caller doesn't have — or can't
// pass — the live sample_interval_sec from config. Matches the default
// sample_interval_sec doubled (60 s). Handlers that own config should pass
// 2 × sample_interval × 1000 directly so the bucket scales with operator
// configuration; this constant is the safe fallback for tests and
// bootstrap paths that run before config is loaded.
const DefaultRawFleetBucketMs = 60_000

// buildRawQueryFleet groups metrics_raw samples into cross-host buckets
// sized to match the agent sample interval. Host agents poll every
// sample_interval_sec (default 30, range 10–300) but clocks drift and
// pipelines stagger, so per-host samples for the same logical observation
// land at different ts values. Grouping by raw ts produces one row per host
// per instant, collapsing the fleet aggregate to a single host's value —
// Sessions tile showed "1" when two servers each had one session, memory
// zig-zagged between per-host values, etc.
//
// bucketMs should be the current sample_interval_sec × 1000 so every host's
// samples for a given poll cycle fall into the same bucket. Passing the
// configured interval (rather than a fixed default) means raising the
// interval to 60s widens the bucket in lock-step; a fixed 30s bucket would
// reintroduce per-host collapse at 60s intervals. Values below
// MinRawFleetBucketMs are clamped up.
func buildRawQueryFleet(hosts []string, fromMs, toMs int64, counters []string, bucketMs int64) (string, []any) {
	if bucketMs < MinRawFleetBucketMs {
		bucketMs = MinRawFleetBucketMs
	}
	args := make([]any, 0, len(hosts)+2+len(counters))
	for _, h := range hosts {
		args = append(args, h)
	}
	args = append(args, fromMs, toMs)
	// Two-step aggregation mirrors buildOneMinQueryFleet: inner query averages
	// each host's samples within a bucketMs window; outer combines across
	// hosts — SUM for session counts, AVG for rates. min/max fall out of the
	// per-host inner aggregates so the fleet spread reflects real inter-host
	// variance rather than single-sample noise.
	bucket := fmt.Sprintf("%d", bucketMs)
	q := `SELECT bucket_ts, counter, ` +
		summableCase("SUM(v_avg)", "AVG(v_avg)") + `, ` +
		summableCase("SUM(v_avg)", "MIN(v_min)") + `, ` +
		summableCase("SUM(v_avg)", "MAX(v_max)") + `
	      FROM (
	          SELECT (ts / ` + bucket + `) * ` + bucket + ` AS bucket_ts, counter, host,
	                 AVG(value) AS v_avg, MIN(value) AS v_min, MAX(value) AS v_max
	          FROM metrics_raw
	          WHERE host IN (` + placeholders(len(hosts)) + `) AND ts >= ? AND ts < ?`
	if len(counters) > 0 {
		q += " AND counter IN (" + placeholders(len(counters)) + ")"
		for _, c := range counters {
			args = append(args, c)
		}
	}
	q += `
	          GROUP BY bucket_ts, counter, host
	      )
	      GROUP BY bucket_ts, counter ORDER BY counter, bucket_ts ASC`
	return q, args
}

// buildOneMinQueryFleet groups metrics_raw into 1-minute buckets across all
// requested hosts. Two-step aggregation: first average each host's samples
// within the minute (inner subquery), then combine across hosts — SUM for
// session counts, AVG for rates.
func buildOneMinQueryFleet(hosts []string, fromMs, toMs int64, counters []string) (string, []any) {
	args := make([]any, 0, len(hosts)+2+len(counters))
	for _, h := range hosts {
		args = append(args, h)
	}
	args = append(args, fromMs, toMs)
	q := `SELECT bucket_ts, counter, ` +
		summableCase("SUM(v_avg)", "AVG(v_avg)") + `, ` +
		summableCase("SUM(v_avg)", "MIN(v_min)") + `, ` +
		summableCase("SUM(v_avg)", "MAX(v_max)") + `
	      FROM (
	          SELECT (ts / 60000) * 60000 AS bucket_ts, counter, host,
	                 AVG(value) AS v_avg, MIN(value) AS v_min, MAX(value) AS v_max
	          FROM metrics_raw
	          WHERE host IN (` + placeholders(len(hosts)) + `) AND ts >= ? AND ts < ?`
	if len(counters) > 0 {
		q += " AND counter IN (" + placeholders(len(counters)) + ")"
		for _, c := range counters {
			args = append(args, c)
		}
	}
	q += `
	          GROUP BY bucket_ts, counter, host
	      )
	      GROUP BY bucket_ts, counter ORDER BY counter, bucket_ts ASC`
	return q, args
}

func buildAggQueryFleet(table string, hosts []string, fromMs, toMs int64, counters []string) (string, []any) {
	args := make([]any, 0, len(hosts)+2+len(counters))
	for _, h := range hosts {
		args = append(args, h)
	}
	args = append(args, fromMs, toMs)
	// table is always an internal constant — not user input.
	// Summable counters: SUM each host's per-bucket avg_value to get fleet
	// totals. Other counters: sample-count-weighted average matches rollHourly
	// weighting so buckets with fewer raw samples don't skew the fleet mean.
	q := fmt.Sprintf(`SELECT bucket_ts, counter, `+
		summableCase("SUM(avg_value)", "SUM(avg_value * sample_count) / NULLIF(SUM(sample_count), 0)")+`, `+
		summableCase("SUM(min_value)", "MIN(min_value)")+`, `+
		summableCase("SUM(max_value)", "MAX(max_value)")+` FROM %s
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
	case TierRaw, TierOneMin:
		// TierOneMin is a virtual view of metrics_raw — shares its bounds.
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
//
// rawBucketMs sizes the cross-host grouping window at the raw tier. Pass the
// current sample_interval_sec × 1000 so per-host samples from the same poll
// cycle co-locate. Zero or sub-minimum values are clamped up to
// MinRawFleetBucketMs by the underlying query builder. Ignored for non-raw
// tiers (those use their own fixed bucketing: 1-min virtual groups on
// metrics_raw, or the precomputed bucket_ts from metrics_5min/metrics_hourly).
func (s *MetricsStore) QueryRangeFleet(
	ctx context.Context,
	hosts []string,
	from, to time.Time,
	tier Tier,
	counters []string,
	rawBucketMs int64,
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
		if err := s.queryRawFleet(ctx, sr, hosts, fromMs, toMs, counters, rawBucketMs); err != nil {
			return nil, err
		}
	case TierOneMin:
		if err := s.queryOneMinFleet(ctx, sr, hosts, fromMs, toMs, counters); err != nil {
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

// queryOneMinFleet is the fleet sibling of queryOneMin — 1-minute grouping of
// metrics_raw across all registered hosts, then one row per (bucket, counter)
// via sample-count-weighted aggregation.
func (s *MetricsStore) queryOneMinFleet(
	ctx context.Context,
	sr *Series,
	hosts []string,
	fromMs, toMs int64,
	counters []string,
) error {
	q, args := buildOneMinQueryFleet(hosts, fromMs, toMs, counters)
	rows, err := s.db.reader.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("telemetry: fleet metrics_raw 1min query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var bucketMs int64
		var counter string
		var avg, min, max float64
		if err := rows.Scan(&bucketMs, &counter, &avg, &min, &max); err != nil {
			return fmt.Errorf("telemetry: fleet metrics_raw 1min scan: %w", err)
		}
		cs := getOrMakeCounter(sr, counter)
		cs.T = append(cs.T, bucketMs)
		cs.Avg = append(cs.Avg, avg)
		cs.Min = append(cs.Min, min)
		cs.Max = append(cs.Max, max)
	}
	return rows.Err()
}

func (s *MetricsStore) queryRawFleet(
	ctx context.Context,
	sr *Series,
	hosts []string,
	fromMs, toMs int64,
	counters []string,
	bucketMs int64,
) error {
	q, args := buildRawQueryFleet(hosts, fromMs, toMs, counters, bucketMs)
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
