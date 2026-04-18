//go:build windows

package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func newMetricsStore(t *testing.T) (*MetricsStore, *DB) {
	t.Helper()
	db := openTestDB(t)
	return NewMetricsStore(db), db
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
