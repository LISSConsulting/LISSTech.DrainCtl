//go:build windows

package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newRetention(t *testing.T, metricsDays, auditDays int) (*Retention, *DB) {
	t.Helper()
	db := openTestDB(t)
	r := NewRetention(db, 1, func() RetentionSettings {
		return RetentionSettings{MetricsDays: metricsDays, AuditDays: auditDays}
	})
	return r, db
}

func seedRaw(t *testing.T, db *DB, tsMs int64, host, counter string, value float64) {
	t.Helper()
	if _, err := db.writer.Exec(
		`INSERT INTO metrics_raw(ts, host, counter, value) VALUES(?, ?, ?, ?)`,
		tsMs, host, counter, value,
	); err != nil {
		t.Fatalf("seed metrics_raw ts=%d: %v", tsMs, err)
	}
}

func seed5Min(t *testing.T, db *DB, bucketMs int64, host, counter string) {
	t.Helper()
	if _, err := db.writer.Exec(
		`INSERT INTO metrics_5min(bucket_ts, host, counter, avg_value, min_value, max_value, sample_count)
         VALUES(?, ?, ?, ?, ?, ?, ?)`,
		bucketMs, host, counter, 1.0, 1.0, 1.0, 1,
	); err != nil {
		t.Fatalf("seed metrics_5min bucket=%d: %v", bucketMs, err)
	}
}

func seedHourly(t *testing.T, db *DB, bucketMs int64, host, counter string) {
	t.Helper()
	if _, err := db.writer.Exec(
		`INSERT INTO metrics_hourly(bucket_ts, host, counter, avg_value, min_value, max_value, sample_count)
         VALUES(?, ?, ?, ?, ?, ?, ?)`,
		bucketMs, host, counter, 1.0, 1.0, 1.0, 60,
	); err != nil {
		t.Fatalf("seed metrics_hourly bucket=%d: %v", bucketMs, err)
	}
}

func seedAudit(t *testing.T, db *DB, tsMs int64, host string, newState int) {
	t.Helper()
	if _, err := db.writer.Exec(
		`INSERT INTO audit(ts, host, prev_state, new_state) VALUES(?, ?, 0, ?)`,
		tsMs, host, newState,
	); err != nil {
		t.Fatalf("seed audit ts=%d host=%s: %v", tsMs, host, err)
	}
}

func countRows(t *testing.T, db *DB, query string) int {
	t.Helper()
	var n int
	if err := db.reader.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func TestRetention_DeletesOlderThanThreshold(t *testing.T) {
	r, db := newRetention(t, 30, 365)
	now := time.Now().UTC()

	oldRaw := now.Add(-50 * time.Hour).UnixMilli()
	old5Min := now.Add(-8 * 24 * time.Hour).Truncate(time.Hour).UnixMilli()
	oldHourly := now.Add(-60 * 24 * time.Hour).Truncate(time.Hour).UnixMilli()
	oldAudit := now.Add(-400 * 24 * time.Hour).UnixMilli()

	seedRaw(t, db, oldRaw, "SRV01", "cpu.util", 1)
	seed5Min(t, db, old5Min, "SRV01", "cpu.util")
	seedHourly(t, db, oldHourly, "SRV01", "cpu.util")
	seedAudit(t, db, oldAudit, "SRV01", 1)

	res := r.RunOnce(context.Background(), now)
	if res.Outcome != "success" {
		t.Fatalf("outcome=%s reason=%s", res.Outcome, res.Reason)
	}
	if res.RowsAffected != 4 {
		t.Errorf("rows_affected=%d, want 4", res.RowsAffected)
	}

	if n := countRows(t, db, `SELECT COUNT(*) FROM metrics_raw`); n != 0 {
		t.Errorf("metrics_raw remaining=%d, want 0", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM metrics_5min`); n != 0 {
		t.Errorf("metrics_5min remaining=%d, want 0", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM metrics_hourly`); n != 0 {
		t.Errorf("metrics_hourly remaining=%d, want 0", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM audit`); n != 0 {
		t.Errorf("audit remaining=%d, want 0", n)
	}
}

func TestRetention_LeavesNewerRowsAlone(t *testing.T) {
	r, db := newRetention(t, 30, 365)
	now := time.Now().UTC()

	seedRaw(t, db, now.Add(-10*time.Hour).UnixMilli(), "SRV01", "cpu.util", 1)
	seed5Min(t, db, now.Add(-3*24*time.Hour).Truncate(time.Hour).UnixMilli(), "SRV01", "cpu.util")
	seedHourly(t, db, now.Add(-15*24*time.Hour).Truncate(time.Hour).UnixMilli(), "SRV01", "cpu.util")
	seedAudit(t, db, now.Add(-30*24*time.Hour).UnixMilli(), "SRV01", 1)

	res := r.RunOnce(context.Background(), now)
	if res.Outcome != "success" {
		t.Fatalf("outcome=%s reason=%s", res.Outcome, res.Reason)
	}
	if res.RowsAffected != 0 {
		t.Errorf("rows_affected=%d, want 0 (nothing eligible)", res.RowsAffected)
	}

	if n := countRows(t, db, `SELECT COUNT(*) FROM metrics_raw`); n != 1 {
		t.Errorf("metrics_raw remaining=%d, want 1", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM metrics_5min`); n != 1 {
		t.Errorf("metrics_5min remaining=%d, want 1", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM metrics_hourly`); n != 1 {
		t.Errorf("metrics_hourly remaining=%d, want 1", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM audit`); n != 1 {
		t.Errorf("audit remaining=%d, want 1", n)
	}
}

// TestRetention_PerTierThresholds pins each tier's cutoff to its own knob —
// shrinking one window must not collapse the others. Seeds one row just past
// each cutoff (eligible) plus one at the boundary (kept, < not <=).
func TestRetention_PerTierThresholds(t *testing.T) {
	r, db := newRetention(t, 30, 365)
	now := time.Now().UTC()

	rawCutoff := now.Add(-25 * time.Hour).Truncate(5 * time.Minute).UnixMilli()
	seedRaw(t, db, rawCutoff-1, "SRV01", "cpu.util", 1)
	seedRaw(t, db, rawCutoff, "SRV01", "cpu.util", 2)

	fiveMinCutoff := now.Add(-6 * 24 * time.Hour).Truncate(time.Hour).UnixMilli()
	seed5Min(t, db, fiveMinCutoff-1, "SRV01", "a")
	seed5Min(t, db, fiveMinCutoff, "SRV01", "b")

	hourlyCutoff := now.Add(-30 * 24 * time.Hour).UnixMilli()
	seedHourly(t, db, hourlyCutoff-1, "SRV01", "a")
	seedHourly(t, db, hourlyCutoff, "SRV01", "b")

	auditCutoff := now.Add(-365 * 24 * time.Hour).UnixMilli()
	seedAudit(t, db, auditCutoff-1, "SRV01", 1)
	seedAudit(t, db, auditCutoff, "SRV02", 1)

	res := r.RunOnce(context.Background(), now)
	if res.Outcome != "success" {
		t.Fatalf("outcome=%s reason=%s", res.Outcome, res.Reason)
	}
	if res.RowsAffected != 4 {
		t.Errorf("rows_affected=%d, want 4 (one per tier)", res.RowsAffected)
	}

	if n := countRows(t, db, `SELECT COUNT(*) FROM metrics_raw`); n != 1 {
		t.Errorf("metrics_raw remaining=%d, want 1 (boundary kept)", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM metrics_5min`); n != 1 {
		t.Errorf("metrics_5min remaining=%d, want 1", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM metrics_hourly`); n != 1 {
		t.Errorf("metrics_hourly remaining=%d, want 1", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM audit`); n != 1 {
		t.Errorf("audit remaining=%d, want 1", n)
	}
}

// TestRetention_ChunkedDeleteBoundsTransaction seeds more rows than deleteChunkSize
// so deleteChunked must loop; final row count must be zero regardless of chunking.
func TestRetention_ChunkedDeleteBoundsTransaction(t *testing.T) {
	r, db := newRetention(t, 30, 365)
	now := time.Now().UTC()

	const N = deleteChunkSize*2 + 500
	tx, err := db.writer.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO metrics_raw(ts, host, counter, value) VALUES(?, ?, ?, ?)`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	oldBase := now.Add(-100 * time.Hour).UnixMilli()
	for i := 0; i < N; i++ {
		if _, err := stmt.Exec(oldBase+int64(i), "SRV01", "cpu.util", float64(i)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	res := r.RunOnce(context.Background(), now)
	if res.Outcome != "success" {
		t.Fatalf("outcome=%s reason=%s", res.Outcome, res.Reason)
	}
	if res.RowsAffected != int64(N) {
		t.Errorf("rows_affected=%d, want %d", res.RowsAffected, N)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM metrics_raw`); n != 0 {
		t.Errorf("metrics_raw remaining=%d, want 0 after chunked purge", n)
	}
}

// TestRetention_IncrementalVacuumRuns proves the retention worker issues
// PRAGMA incremental_vacuum on every pass. We flip the test DB into
// auto_vacuum=INCREMENTAL (requires VACUUM to rewrite byte 36 of the header
// because Open()'s pragma block lands after journal_mode=WAL has already
// initialized the file) so the pragma's effect is observable — a successful
// call empties freelist_count. Without the retention pragma, deleted pages
// would remain on the freelist.
func TestRetention_IncrementalVacuumRuns(t *testing.T) {
	r, db := newRetention(t, 30, 365)
	now := time.Now().UTC()

	if _, err := db.writer.Exec(`PRAGMA auto_vacuum = INCREMENTAL`); err != nil {
		t.Fatalf("set auto_vacuum=INCREMENTAL: %v", err)
	}
	if _, err := db.writer.Exec(`VACUUM`); err != nil {
		t.Fatalf("VACUUM to apply auto_vacuum change: %v", err)
	}

	const N = 3000
	tx, err := db.writer.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO metrics_raw(ts, host, counter, value) VALUES(?, ?, ?, ?)`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	oldBase := now.Add(-100 * time.Hour).UnixMilli()
	for i := 0; i < N; i++ {
		if _, err := stmt.Exec(oldBase+int64(i), "SRV01", "cpu.util", float64(i)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	r.RunOnce(context.Background(), now)

	var freelist int
	if err := db.writer.QueryRow(`PRAGMA freelist_count`).Scan(&freelist); err != nil {
		t.Fatalf("freelist_count: %v", err)
	}
	if freelist != 0 {
		t.Errorf("freelist_count=%d, want 0 (incremental_vacuum should have returned freed pages)", freelist)
	}
}

// TestRetention_FailureIsNonFatalAndRetryRuns covers FR-013: a retention pass
// that errs mid-delete must not crash, and the next scheduled run must recover.
// A cancelled context drives the error on the first pass; a fresh context on
// the retry completes the delete and records success in maintenance_jobs.
func TestRetention_FailureIsNonFatalAndRetryRuns(t *testing.T) {
	r, db := newRetention(t, 30, 365)
	now := time.Now().UTC()

	seedRaw(t, db, now.Add(-100*time.Hour).UnixMilli(), "SRV01", "cpu.util", 1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := r.RunOnce(ctx, now)
	if res.Outcome != "failure" {
		t.Errorf("first pass outcome=%s, want failure", res.Outcome)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM metrics_raw`); n != 1 {
		t.Errorf("metrics_raw after failed pass=%d, want 1 (delete should not have committed)", n)
	}

	res = r.RunOnce(context.Background(), now)
	if res.Outcome != "success" {
		t.Fatalf("retry outcome=%s reason=%s", res.Outcome, res.Reason)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM metrics_raw`); n != 0 {
		t.Errorf("metrics_raw after retry=%d, want 0", n)
	}

	var outcome string
	if err := db.reader.QueryRow(
		`SELECT outcome FROM maintenance_jobs WHERE name='retention'`,
	).Scan(&outcome); err != nil {
		t.Fatalf("read maintenance_jobs: %v", err)
	}
	if outcome != "success" {
		t.Errorf("maintenance outcome=%s, want success after retry", outcome)
	}
}

// TestRetention_ShrinkFrom30dTo1dIsChunkedAndDashboardStaysResponsive seeds a
// 30-day hourly dataset, shrinks the retention window to 1 day, runs the
// worker, and asserts a reader goroutine never observed > 100 ms latency during
// the purge. Validates the chunked-delete + WAL-mode isolation guarantees.
func TestRetention_ShrinkFrom30dTo1dIsChunkedAndDashboardStaysResponsive(t *testing.T) {
	db := openTestDB(t)

	var mu sync.RWMutex
	settings := RetentionSettings{MetricsDays: 30, AuditDays: 365}
	r := NewRetention(db, 1, func() RetentionSettings {
		mu.RLock()
		defer mu.RUnlock()
		return settings
	})

	now := time.Now().UTC()
	const hours = 30 * 24
	const counters = 10

	tx, err := db.writer.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO metrics_hourly(bucket_ts, host, counter, avg_value, min_value, max_value, sample_count)
         VALUES(?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	for h := 1; h <= hours; h++ {
		bucket := now.Add(-time.Duration(h) * time.Hour).Truncate(time.Hour).UnixMilli()
		for c := 0; c < counters; c++ {
			counter := fmt.Sprintf("c.%d", c)
			if _, err := stmt.Exec(bucket, "SRV01", counter, 1.0, 1.0, 1.0, 60); err != nil {
				t.Fatalf("seed h=%d c=%d: %v", h, c, err)
			}
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	mu.Lock()
	settings.MetricsDays = 1
	mu.Unlock()

	var maxLatencyMs atomic.Int64
	var readerErr atomic.Value // holds error
	var readCount atomic.Int64
	var wg sync.WaitGroup
	stopCh := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopCh:
				return
			default:
			}
			start := time.Now()
			var cnt int
			if err := db.reader.QueryRow(
				`SELECT COUNT(*) FROM metrics_hourly WHERE host='SRV01'`,
			).Scan(&cnt); err != nil {
				readerErr.Store(err)
				return
			}
			readCount.Add(1)
			elapsed := time.Since(start).Milliseconds()
			for {
				cur := maxLatencyMs.Load()
				if elapsed <= cur || maxLatencyMs.CompareAndSwap(cur, elapsed) {
					break
				}
			}
		}
	}()

	res := r.RunOnce(context.Background(), now)
	close(stopCh)
	wg.Wait()

	if res.Outcome != "success" {
		t.Fatalf("outcome=%s reason=%s", res.Outcome, res.Reason)
	}
	if v := readerErr.Load(); v != nil {
		t.Fatalf("reader goroutine errored during retention: %v", v)
	}
	if readCount.Load() == 0 {
		t.Fatal("reader goroutine never completed a query; responsiveness claim vacuous")
	}

	remaining := countRows(t, db, `SELECT COUNT(*) FROM metrics_hourly WHERE host='SRV01'`)
	if remaining > 24*counters {
		t.Errorf("remaining=%d, want <= %d (last 24h × %d counters)",
			remaining, 24*counters, counters)
	}
	if remaining == 0 {
		t.Error("retention removed every row including those inside the 1-day window")
	}

	if m := maxLatencyMs.Load(); m >= 100 {
		t.Errorf("reader max latency=%dms during retention, want < 100ms", m)
	}
}

// TestRetention_EmitsLogsAndMaintenanceRowPerRun covers FR-032: the widget
// (maintenance_jobs row) and the log sink must report the same facts. Asserts
// that per-tier log records sum to the maintenance row's rows_affected.
func TestRetention_EmitsLogsAndMaintenanceRowPerRun(t *testing.T) {
	r, db := newRetention(t, 30, 365)
	now := time.Now().UTC()

	h := &capturingHandler{}
	orig := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(orig) })

	seedRaw(t, db, now.Add(-100*time.Hour).UnixMilli(), "SRV01", "cpu.util", 1)
	seedAudit(t, db, now.Add(-400*24*time.Hour).UnixMilli(), "SRV01", 1)

	res := r.RunOnce(context.Background(), now)
	if res.Outcome != "success" {
		t.Fatalf("outcome=%s reason=%s", res.Outcome, res.Reason)
	}
	if res.RowsAffected != 2 {
		t.Errorf("rows_affected=%d, want 2 (1 raw + 1 audit)", res.RowsAffected)
	}

	var foundLog bool
	var loggedRows int64
	h.mu.Lock()
	for _, rec := range h.records {
		if !strings.Contains(rec.Message, "retention") {
			continue
		}
		foundLog = true
		rec.Attrs(func(a slog.Attr) bool {
			if a.Key == "rows" {
				loggedRows += a.Value.Int64()
			}
			return true
		})
	}
	h.mu.Unlock()
	if !foundLog {
		t.Fatal("no slog record mentioning 'retention'")
	}

	var outcome string
	var storedRows int64
	if err := db.reader.QueryRow(
		`SELECT outcome, rows_affected FROM maintenance_jobs WHERE name='retention'`,
	).Scan(&outcome, &storedRows); err != nil {
		t.Fatalf("read maintenance_jobs: %v", err)
	}
	if outcome != "success" {
		t.Errorf("maintenance outcome=%s, want success", outcome)
	}
	if storedRows != res.RowsAffected {
		t.Errorf("maintenance rows_affected=%d, Result.RowsAffected=%d", storedRows, res.RowsAffected)
	}
	if loggedRows != storedRows {
		t.Errorf("sum of per-tier log rows=%d, maintenance rows_affected=%d", loggedRows, storedRows)
	}
}
