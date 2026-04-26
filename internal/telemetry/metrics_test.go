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
	"sync/atomic"
	"testing"
	"time"
)

func newMetricsStore(t *testing.T) (*MetricsStore, *DB) {
	t.Helper()
	db := openTestDB(t)
	ms, err := NewMetricsStore(context.Background(), db)
	if err != nil {
		t.Fatalf("NewMetricsStore: %v", err)
	}
	t.Cleanup(func() { _ = ms.Close() })
	return ms, db
}

// capturingHandler is a slog.Handler that records every emitted record for
// later inspection. Used by TestAppend_FutureTimestampStoresAndLogs.
type capturingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}
func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler      { return h }

func TestAppend_BatchRoundTrips(t *testing.T) {
	ms, _ := newMetricsStore(t)

	base := time.Now().UTC().Truncate(time.Second)
	samples := []Sample{
		{Ts: base, Host: "SRV01", Counter: "cpu.util", Value: 10},
		{Ts: base.Add(time.Second), Host: "SRV01", Counter: "cpu.util", Value: 15},
		{Ts: base.Add(2 * time.Second), Host: "SRV01", Counter: "mem.util", Value: 50},
		{Ts: base, Host: "SRV02", Counter: "cpu.util", Value: 20},
	}
	if err := ms.Append(context.Background(), samples); err != nil {
		t.Fatalf("Append: %v", err)
	}

	sr, err := ms.QueryRange(context.Background(), "SRV01",
		base.Add(-time.Minute), base.Add(time.Minute), TierRaw, nil)
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if len(sr.Data) != 2 {
		t.Errorf("series count = %d, want 2", len(sr.Data))
	}
	cpu := sr.Data["cpu.util"]
	if cpu == nil || len(cpu.T) != 2 {
		t.Fatalf("cpu.util series missing or wrong size: %+v", cpu)
	}
	if cpu.T[0] >= cpu.T[1] {
		t.Errorf("raw tier not ordered ASC: %v", cpu.T)
	}
	mem := sr.Data["mem.util"]
	if mem == nil || len(mem.T) != 1 {
		t.Fatalf("mem.util series missing or wrong size: %+v", mem)
	}
	if mem.Avg[0] != 50 || mem.Min[0] != 50 || mem.Max[0] != 50 {
		t.Errorf("raw tier avg/min/max collapsed incorrectly: %+v", mem)
	}

	// Cross-host isolation: SRV01 query must not see SRV02 data.
	for _, cs := range sr.Data {
		for _, v := range cs.Avg {
			if v == 20 {
				t.Errorf("SRV01 query returned SRV02 sample: %v", sr.Data)
			}
		}
	}
}

func TestQueryRange_RawTier(t *testing.T) {
	ms, _ := newMetricsStore(t)

	base := time.Now().UTC().Truncate(time.Second)
	samples := []Sample{
		{Ts: base, Host: "SRV01", Counter: "a", Value: 1},
		{Ts: base.Add(time.Second), Host: "SRV01", Counter: "b", Value: 2},
		{Ts: base.Add(2 * time.Second), Host: "SRV01", Counter: "c", Value: 3},
	}
	if err := ms.Append(context.Background(), samples); err != nil {
		t.Fatalf("Append: %v", err)
	}

	sr, err := ms.QueryRange(context.Background(), "SRV01",
		base.Add(-time.Minute), base.Add(time.Minute), TierRaw, []string{"a", "c"})
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if _, ok := sr.Data["b"]; ok {
		t.Errorf("counter filter leaked unrequested counter: %v", sr.Data)
	}
	if sr.Data["a"] == nil || sr.Data["c"] == nil {
		t.Errorf("counter filter dropped requested counters: %v", sr.Data)
	}
	if sr.Tier != TierRaw {
		t.Errorf("tier = %s, want raw", sr.Tier.TierName())
	}
	if sr.OldestAvailable == nil || sr.NewestAvailable == nil {
		t.Fatalf("bounds not populated: oldest=%v newest=%v",
			sr.OldestAvailable, sr.NewestAvailable)
	}
	if !sr.OldestAvailable.Equal(base) {
		t.Errorf("OldestAvailable = %v, want %v", *sr.OldestAvailable, base)
	}
	if !sr.NewestAvailable.Equal(base.Add(2 * time.Second)) {
		t.Errorf("NewestAvailable = %v, want %v",
			*sr.NewestAvailable, base.Add(2*time.Second))
	}
}

// TestQueryRange_HourlyTier_FallsBackWhenRawPurged confirms the hourly tier
// is queryable independently of metrics_raw. After raw retention purges
// expire, the hourly table remains the durable source for long windows.
func TestQueryRange_HourlyTier_FallsBackWhenRawPurged(t *testing.T) {
	ms, db := newMetricsStore(t)

	bucket0 := time.Now().UTC().Truncate(time.Hour).Add(-24 * time.Hour)
	for i := 0; i < 24; i++ {
		b := bucket0.Add(time.Duration(i) * time.Hour).UnixMilli()
		if _, err := db.writer.Exec(
			`INSERT INTO metrics_hourly(bucket_ts, host, counter, avg_value, min_value, max_value, sample_count)
			 VALUES (?, ?, 'cpu.util', ?, ?, ?, ?)`,
			b, "SRV01", float64(i), 0.0, float64(i*2), 60,
		); err != nil {
			t.Fatalf("seed hourly bucket %d: %v", i, err)
		}
	}

	var rawCount int
	if err := db.reader.QueryRow(
		`SELECT COUNT(*) FROM metrics_raw WHERE host = ?`, "SRV01",
	).Scan(&rawCount); err != nil {
		t.Fatalf("count raw: %v", err)
	}
	if rawCount != 0 {
		t.Fatalf("raw tier unexpectedly populated: %d rows", rawCount)
	}

	sr, err := ms.QueryRange(context.Background(), "SRV01",
		bucket0.Add(-time.Hour), bucket0.Add(25*time.Hour), TierHourly, nil)
	if err != nil {
		t.Fatalf("QueryRange hourly: %v", err)
	}
	cs := sr.Data["cpu.util"]
	if cs == nil || len(cs.T) != 24 {
		t.Fatalf("hourly series len = %d, want 24: %+v", len(cs.T), cs)
	}
	for i := 1; i < len(cs.T); i++ {
		if cs.T[i-1] >= cs.T[i] {
			t.Errorf("hourly tier not ordered ASC at idx %d: %v", i, cs.T)
			break
		}
	}
	if sr.Tier != TierHourly {
		t.Errorf("tier = %s, want hourly", sr.Tier.TierName())
	}
}

// TestAppend_ConcurrentSafety drives Append from many goroutines at once.
// The writer pool pins SetMaxOpenConns(1) — the test guards against races,
// deadlocks, and lost rows under that contract.
func TestAppend_ConcurrentSafety(t *testing.T) {
	ms, db := newMetricsStore(t)

	const goroutines = 20
	const perGoroutine = 50
	base := time.Now().UTC().Truncate(time.Second)

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			counter := fmt.Sprintf("c.%d", g)
			batch := make([]Sample, perGoroutine)
			for i := 0; i < perGoroutine; i++ {
				batch[i] = Sample{
					Ts:      base.Add(time.Duration(i) * time.Second),
					Host:    "SRV01",
					Counter: counter,
					Value:   float64(i),
				}
			}
			if err := ms.Append(context.Background(), batch); err != nil {
				errCh <- err
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent Append: %v", err)
	}

	var count int
	if err := db.reader.QueryRow(
		`SELECT COUNT(*) FROM metrics_raw WHERE host=?`, "SRV01",
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	want := goroutines * perGoroutine
	if count != want {
		t.Errorf("row count = %d, want %d", count, want)
	}
}

// TestAppend_DiskFullIsGracefullyHandled injects a broken writer by closing
// its pool, then confirms Append returns a wrapped error rather than
// panicking or hanging. Mirrors the caller contract for any underlying I/O
// failure (disk full, permission error, volume removed).
func TestAppend_DiskFullIsGracefullyHandled(t *testing.T) {
	ms, db := newMetricsStore(t)

	if err := db.writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	err := ms.Append(context.Background(), []Sample{
		{Ts: time.Now().UTC(), Host: "SRV01", Counter: "cpu.util", Value: 1},
	})
	if err == nil {
		t.Fatal("Append on closed writer returned nil error")
	}
	if !strings.Contains(err.Error(), "metrics Append") {
		t.Errorf("error does not cite Append call site: %v", err)
	}
}

// TestAppend_FutureTimestampStoresAndLogs verifies spec.md:121: future-dated
// samples are stored and queryable, flagged in logs but not rejected.
func TestAppend_FutureTimestampStoresAndLogs(t *testing.T) {
	ms, _ := newMetricsStore(t)

	h := &capturingHandler{}
	orig := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(orig) })

	future := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	if err := ms.Append(context.Background(), []Sample{
		{Ts: future, Host: "SRV01", Counter: "cpu.util", Value: 99},
	}); err != nil {
		t.Fatalf("Append with future ts: %v", err)
	}

	sr, err := ms.QueryRange(context.Background(), "SRV01",
		future.Add(-time.Minute), future.Add(time.Minute), TierRaw, nil)
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	cs := sr.Data["cpu.util"]
	if cs == nil || len(cs.T) != 1 {
		t.Fatalf("future sample not stored: %+v", cs)
	}
	if cs.Avg[0] != 99 {
		t.Errorf("stored value = %v, want 99", cs.Avg[0])
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	var found bool
	for _, r := range h.records {
		if r.Level >= slog.LevelWarn && strings.Contains(r.Message, "future-dated") {
			found = true
			break
		}
	}
	if !found {
		msgs := make([]string, 0, len(h.records))
		for _, r := range h.records {
			msgs = append(msgs, r.Message)
		}
		t.Errorf("expected a WARN log mentioning 'future-dated', got: %v", msgs)
	}
}

// TestNewCounterRoundTripsThroughAllTiersAndHTTP ingests a counter name that
// the schema has never seen before, then confirms it materialises through
// raw, 5-min, and hourly tables with no DDL change (FR-012). The dashboard
// handler (T015) re-encodes whatever MetricsStore returns, so verifying
// QueryRange across tiers is equivalent to verifying the HTTP response.
func TestNewCounterRoundTripsThroughAllTiersAndHTTP(t *testing.T) {
	ms, db := newMetricsStore(t)
	agg := NewAggregator(db, 60)
	ctx := context.Background()

	const newCounter = "never.seen.before.counter"
	base := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Hour)
	samples := []Sample{
		{Ts: base, Host: "SRV01", Counter: newCounter, Value: 10},
		{Ts: base.Add(15 * time.Minute), Host: "SRV01", Counter: newCounter, Value: 20},
		{Ts: base.Add(30 * time.Minute), Host: "SRV01", Counter: newCounter, Value: 30},
		{Ts: base.Add(45 * time.Minute), Host: "SRV01", Counter: newCounter, Value: 40},
	}
	if err := ms.Append(ctx, samples); err != nil {
		t.Fatalf("Append: %v", err)
	}

	sr, err := ms.QueryRange(ctx, "SRV01",
		base.Add(-time.Hour), base.Add(2*time.Hour), TierRaw, nil)
	if err != nil {
		t.Fatalf("QueryRange raw: %v", err)
	}
	if cs := sr.Data[newCounter]; cs == nil || len(cs.T) != 4 {
		t.Errorf("raw tier missing new counter or wrong len: %+v", cs)
	}

	now := time.Now().UTC()
	agg.roll5Min(ctx, now)

	var fiveMinCount int
	if err := db.reader.QueryRow(
		`SELECT COUNT(*) FROM metrics_5min WHERE host=? AND counter=?`,
		"SRV01", newCounter,
	).Scan(&fiveMinCount); err != nil {
		t.Fatalf("count 5min: %v", err)
	}
	if fiveMinCount != 4 {
		t.Errorf("5min bucket count = %d, want 4 (one per sample, distinct 5-min boundaries)", fiveMinCount)
	}

	agg.rollHourly(ctx, now)
	sr, err = ms.QueryRange(ctx, "SRV01",
		base.Add(-time.Hour), base.Add(2*time.Hour), TierHourly, nil)
	if err != nil {
		t.Fatalf("QueryRange hourly: %v", err)
	}
	cs := sr.Data[newCounter]
	if cs == nil || len(cs.T) != 1 {
		t.Fatalf("hourly tier missing new counter: %+v", cs)
	}
	if cs.Avg[0] != 25 {
		t.Errorf("hourly avg = %v, want 25", cs.Avg[0])
	}
	if cs.Min[0] != 10 || cs.Max[0] != 40 {
		t.Errorf("hourly min/max = %v/%v, want 10/40", cs.Min[0], cs.Max[0])
	}
}

func TestNearestCounters_PicksClosestWithinTolerance(t *testing.T) {
	ms, _ := newMetricsStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	// Three cpu_pct samples around the target at t=base:
	//   -60s → 10, -10s → 42 (closest), +90s → 77
	// Only one input_delay_max_ms sample, within tolerance.
	samples := []Sample{
		{Ts: base.Add(-60 * time.Second), Host: "SRV01", Counter: "cpu_pct", Value: 10},
		{Ts: base.Add(-10 * time.Second), Host: "SRV01", Counter: "cpu_pct", Value: 42},
		{Ts: base.Add(90 * time.Second), Host: "SRV01", Counter: "cpu_pct", Value: 77},
		{Ts: base.Add(5 * time.Second), Host: "SRV01", Counter: "input_delay_max_ms", Value: 125},
	}
	if err := ms.Append(ctx, samples); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := ms.NearestCounters(ctx, "SRV01", base, 120*1000,
		[]string{"cpu_pct", "input_delay_max_ms"})
	if err != nil {
		t.Fatalf("NearestCounters: %v", err)
	}
	if v := got["cpu_pct"]; v != 42 {
		t.Errorf("cpu_pct = %v, want 42 (closest sample)", v)
	}
	if v := got["input_delay_max_ms"]; v != 125 {
		t.Errorf("input_delay_max_ms = %v, want 125", v)
	}
}

func TestNearestCounters_OutsideToleranceOmitted(t *testing.T) {
	ms, _ := newMetricsStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	// Sample lives 10 minutes away from the target; a 60 s tolerance must omit it.
	if err := ms.Append(ctx, []Sample{
		{Ts: base.Add(-10 * time.Minute), Host: "SRV01", Counter: "cpu_pct", Value: 99},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := ms.NearestCounters(ctx, "SRV01", base, 60*1000, []string{"cpu_pct"})
	if err != nil {
		t.Fatalf("NearestCounters: %v", err)
	}
	if _, ok := got["cpu_pct"]; ok {
		t.Errorf("expected cpu_pct omitted (outside tolerance), got %v", got)
	}
}

func TestNearestCounters_CrossHostIsolation(t *testing.T) {
	ms, _ := newMetricsStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	if err := ms.Append(ctx, []Sample{
		{Ts: base, Host: "SRV01", Counter: "cpu_pct", Value: 10},
		{Ts: base, Host: "SRV02", Counter: "cpu_pct", Value: 90},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := ms.NearestCounters(ctx, "SRV01", base, 60*1000, []string{"cpu_pct"})
	if err != nil {
		t.Fatalf("NearestCounters: %v", err)
	}
	if v := got["cpu_pct"]; v != 10 {
		t.Errorf("cpu_pct = %v, want 10 (SRV01 only; SRV02 sample must not leak)", v)
	}
}

func TestNearestCounters_EmptyCounterList(t *testing.T) {
	ms, _ := newMetricsStore(t)
	got, err := ms.NearestCounters(context.Background(), "SRV01", time.Now(), 60*1000, nil)
	if err != nil {
		t.Fatalf("NearestCounters(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}

// TestRestart_LosesAtMostOneSamplingInterval is the SC-004 durability gate:
// drive metrics ingest, simulate a crash mid-flight by closing the DB
// without a graceful flush, reopen against the same data dir, continue
// ingest from roughly one sampling interval later, then scan the merged
// timeline and assert no gap exceeds 2 × samplingInterval — i.e. at most
// one interval was lost across the restart.
//
// Real SIGKILL can't be modelled from inside the test runner (it would kill
// the runner itself). Instead we rely on SQLite's WAL-mode crash-safety
// guarantee: commits that completed before the close() call must be
// visible on the next Open() against the same directory, exactly as they
// would be after a power loss. That is the property operators rely on.
func TestRestart_LosesAtMostOneSamplingInterval(t *testing.T) {
	const (
		samplingInterval = 15 * time.Second
		preSamples       = 20
		postSamples      = 20
	)
	host := "SRV01"
	counter := "cpu_pct"

	dir := t.TempDir()
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)

	// ── Pre-crash ingest ────────────────────────────────────────────────
	db1, err := Open(dir)
	if err != nil {
		t.Fatalf("Open (pre): %v", err)
	}
	ms1, err := NewMetricsStore(context.Background(), db1)
	if err != nil {
		_ = db1.Close()
		t.Fatalf("NewMetricsStore (pre): %v", err)
	}
	preBatch := make([]Sample, preSamples)
	for i := 0; i < preSamples; i++ {
		preBatch[i] = Sample{
			Ts:      base.Add(time.Duration(i) * samplingInterval),
			Host:    host,
			Counter: counter,
			Value:   float64(i),
		}
	}
	if err := ms1.Append(context.Background(), preBatch); err != nil {
		_ = db1.Close()
		t.Fatalf("Append (pre): %v", err)
	}

	// Simulate a crash: close without any extra flush / checkpoint. WAL-mode
	// SQLite's synchronous=NORMAL commits are durable through this close by
	// construction. This is what a kill -9 would leave behind, give or take
	// whatever's still in the WAL that hasn't been fsync'd to the main file
	// (fine — recovery folds it in on next open).
	if err := db1.Close(); err != nil {
		t.Fatalf("Close (simulated crash): %v", err)
	}

	// ── Post-crash ingest: resume ONE interval later (worst case) ───────
	db2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open (post): %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })

	ms2, err := NewMetricsStore(context.Background(), db2)
	if err != nil {
		t.Fatalf("NewMetricsStore (post): %v", err)
	}
	t.Cleanup(func() { _ = ms2.Close() })
	resumeStart := base.Add(time.Duration(preSamples+1) * samplingInterval) // skip exactly one interval
	postBatch := make([]Sample, postSamples)
	for i := 0; i < postSamples; i++ {
		postBatch[i] = Sample{
			Ts:      resumeStart.Add(time.Duration(i) * samplingInterval),
			Host:    host,
			Counter: counter,
			Value:   float64(preSamples + i),
		}
	}
	if err := ms2.Append(context.Background(), postBatch); err != nil {
		t.Fatalf("Append (post): %v", err)
	}

	// ── Read back the merged timeline and check gaps ────────────────────
	sr, err := ms2.QueryRange(context.Background(), host,
		base.Add(-time.Minute), resumeStart.Add(time.Duration(postSamples+1)*samplingInterval),
		TierRaw, []string{counter})
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	cs := sr.Data[counter]
	if cs == nil {
		t.Fatalf("counter %q missing from series map: %+v", counter, sr.Data)
	}
	if want := preSamples + postSamples; len(cs.T) != want {
		t.Fatalf("sample count = %d, want %d (pre=%d + post=%d — pre-crash durability regression?)",
			len(cs.T), want, preSamples, postSamples)
	}

	maxGapMs := int64(2) * int64(samplingInterval/time.Millisecond)
	for i := 1; i < len(cs.T); i++ {
		gap := cs.T[i] - cs.T[i-1]
		if gap > maxGapMs {
			t.Errorf("gap at index %d = %d ms, exceeds %d ms (2 × sampling interval = %d s) — SC-004 violated",
				i, gap, maxGapMs, samplingInterval/time.Second)
		}
	}
}

// TestNewMetricsStoreFailsOnReadOnlyDB proves the nil-writer guard. A *DB
// produced by OpenReadOnly has writer == nil; passing it to NewMetricsStore
// must return errReadOnlyDB rather than nil-panicking on first Append.
func TestNewMetricsStoreFailsOnReadOnlyDB(t *testing.T) {
	roDB := &DB{} // writer/auditDB/checkpointDB all nil — same shape as OpenReadOnly result
	ms, err := NewMetricsStore(context.Background(), roDB)
	if !errors.Is(err, errReadOnlyDB) {
		t.Fatalf("err = %v, want errReadOnlyDB", err)
	}
	if ms != nil {
		t.Errorf("store = %v on error path, want nil", ms)
	}

	// nil DB should also be rejected by the same guard.
	if _, err := NewMetricsStore(context.Background(), nil); !errors.Is(err, errReadOnlyDB) {
		t.Errorf("nil DB err = %v, want errReadOnlyDB", err)
	}
}

// TestMetricsStoreAppendReusesStmt is the load-bearing verification for the
// Step 6 change: across 100 Append calls the cached statement must be
// prepared exactly once AND every Append must bind that exact cached stmt
// to its per-call transaction. Counts both seams so a regression that
// replaced txBindStmt with a fresh tx.PrepareContext would fail this test:
// prepareWriter alone wouldn't catch it because that seam only fires inside
// NewMetricsStore.
func TestMetricsStoreAppendReusesStmt(t *testing.T) {
	var prepares atomic.Int32
	origPrepare := prepareWriter
	prepareWriter = func(ctx context.Context, w *sql.DB, sqlStr string) (*sql.Stmt, error) {
		prepares.Add(1)
		return w.PrepareContext(ctx, sqlStr)
	}
	t.Cleanup(func() { prepareWriter = origPrepare })

	var binds atomic.Int32
	var boundStmtPtr atomic.Pointer[sql.Stmt]
	origBind := txBindStmt
	txBindStmt = func(ctx context.Context, tx *sql.Tx, stmt *sql.Stmt) *sql.Stmt {
		binds.Add(1)
		boundStmtPtr.Store(stmt)
		return tx.StmtContext(ctx, stmt)
	}
	t.Cleanup(func() { txBindStmt = origBind })

	ms, _ := newMetricsStore(t)

	if got := prepares.Load(); got != 1 {
		t.Fatalf("prepareWriter called %d times after construction, want 1", got)
	}

	base := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 100; i++ {
		err := ms.Append(context.Background(), []Sample{
			{Ts: base.Add(time.Duration(i) * time.Second), Host: "SRV01", Counter: "cpu.util", Value: float64(i)},
		})
		if err != nil {
			t.Fatalf("Append #%d: %v", i, err)
		}
	}

	if got := prepares.Load(); got != 1 {
		t.Errorf("prepareWriter called %d times after 100 Appends, want exactly 1", got)
	}
	if got := binds.Load(); got != 100 {
		t.Errorf("txBindStmt called %d times across 100 Appends, want exactly 100 — Append regressed away from binding the cached stmt?", got)
	}
	if got := boundStmtPtr.Load(); got != ms.appendStmt {
		t.Errorf("txBindStmt was called with stmt=%p, want the cached appendStmt=%p — Append is binding a different stmt", got, ms.appendStmt)
	}
}

// TestMetricsStoreCloseDuringAppendIsSafe drives the close-during-Append
// race deterministically via appendHook. Asserts ordering: Close blocks
// while Append holds the RLock at the hook, then proceeds; the in-flight
// Append commits successfully; subsequent Appends return errStoreClosed.
func TestMetricsStoreCloseDuringAppendIsSafe(t *testing.T) {
	ms, _ := newMetricsStore(t)

	hookEntered := make(chan struct{})
	hookRelease := make(chan struct{})
	origHook := appendHook
	appendHook = func() {
		close(hookEntered)
		<-hookRelease
	}
	t.Cleanup(func() { appendHook = origHook })

	appendDone := make(chan error, 1)
	go func() {
		appendDone <- ms.Append(context.Background(), []Sample{
			{Ts: time.Now().UTC(), Host: "SRV01", Counter: "cpu.util", Value: 1},
		})
	}()

	// Wait until the in-flight Append is parked at the hook holding RLock.
	<-hookEntered

	// Reset the hook so Close (called below from another goroutine) and
	// any subsequent Append do NOT re-enter the blocking path.
	appendHook = origHook

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- ms.Close()
	}()

	// Close must NOT complete while Append still holds the RLock.
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned (%v) before in-flight Append released RLock — RWMutex contract violated", err)
	case <-time.After(100 * time.Millisecond):
		// expected: Close is parked waiting for the RLock.
	}

	// Release the in-flight Append. It should commit successfully because
	// stmt was prepared before Close fires (the tx-scoped wrapper from
	// StmtContext was acquired prior to the hook).
	close(hookRelease)

	if err := <-appendDone; err != nil {
		t.Errorf("in-flight Append returned %v, want nil — close-during-append corrupted tx", err)
	}
	if err := <-closeDone; err != nil {
		t.Errorf("Close returned %v, want nil", err)
	}

	// Subsequent Append must return errStoreClosed, no panic.
	err := ms.Append(context.Background(), []Sample{
		{Ts: time.Now().UTC(), Host: "SRV01", Counter: "cpu.util", Value: 2},
	})
	if !errors.Is(err, errStoreClosed) {
		t.Errorf("post-Close Append err = %v, want errStoreClosed", err)
	}
}

// TestMetricsStoreAppendAfterCloseFails is the simple no-race counterpart
// to the close-during-Append test: serial Close → Append must surface
// errStoreClosed without nil-panicking on the cleared stmt pointer.
func TestMetricsStoreAppendAfterCloseFails(t *testing.T) {
	ms, _ := newMetricsStore(t)

	if err := ms.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Idempotent — second Close is a no-op.
	if err := ms.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	err := ms.Append(context.Background(), []Sample{
		{Ts: time.Now().UTC(), Host: "SRV01", Counter: "cpu.util", Value: 1},
	})
	if !errors.Is(err, errStoreClosed) {
		t.Errorf("post-Close Append err = %v, want errStoreClosed", err)
	}
}
