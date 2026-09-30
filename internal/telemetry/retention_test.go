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

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
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

func seedSessionWorkload(t *testing.T, db *DB, table string, bucketMs int64) {
	t.Helper()
	tx, err := db.writer.Begin()
	if err != nil {
		t.Fatalf("begin %s seed: %v", table, err)
	}
	defer tx.Rollback() //nolint:errcheck
	aggregate := sessiondata.SessionWorkloadAggregate{
		BaseSampleCount: 1,
		CPUSum:          1,
		CPUCount:        1,
	}
	aggregate.CPUHistogram[2] = 1
	if err := upsertSessionWorkloadAggregate(
		context.Background(), tx, table, bucketMs, "SRV01", aggregate,
		[]uint64{1}, []uint64{0}, []uint64{0},
	); err != nil {
		t.Fatalf("seed %s bucket=%d: %v", table, bucketMs, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit %s seed: %v", table, err)
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

func seedEventSpike(t *testing.T, db *DB, windowStartMs int64, host, channel string) {
	t.Helper()
	if _, err := db.writer.Exec(
		`INSERT INTO event_spikes
		    (host, channel, window_start_ms, window_end_ms, observed, expected,
		     tail_probability, confirmation_count, first_seen_at_ms, created_at_ms)
		 VALUES (?, ?, ?, ?, 10, 5.0, 0.001, 3, ?, ?)`,
		host, channel, windowStartMs, windowStartMs+5*60*1000, windowStartMs, windowStartMs,
	); err != nil {
		t.Fatalf("seed event_spikes ts=%d host=%s channel=%s: %v", windowStartMs, host, channel, err)
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
	oldSpike := now.Add(-400 * 24 * time.Hour).UnixMilli()

	seedRaw(t, db, oldRaw, "SRV01", "cpu.util", 1)
	seed5Min(t, db, old5Min, "SRV01", "cpu.util")
	seedHourly(t, db, oldHourly, "SRV01", "cpu.util")
	seedAudit(t, db, oldAudit, "SRV01", 1)
	seedEventSpike(t, db, oldSpike, "SRV01", "Application")
	seedSessionWorkload(t, db, "session_workload_raw", oldRaw)
	seedSessionWorkload(t, db, "session_workload_5min", old5Min)
	seedSessionWorkload(t, db, "session_workload_hourly", oldHourly)

	res := r.RunOnce(context.Background(), now)
	if res.Outcome != "success" {
		t.Fatalf("outcome=%s reason=%s", res.Outcome, res.Reason)
	}
	if res.RowsAffected != 8 {
		t.Errorf("rows_affected=%d, want 8", res.RowsAffected)
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
	if n := countRows(t, db, `SELECT COUNT(*) FROM event_spikes`); n != 0 {
		t.Errorf("event_spikes remaining=%d, want 0", n)
	}
	for _, table := range []string{"session_workload_raw", "session_workload_5min", "session_workload_hourly"} {
		if n := countRows(t, db, `SELECT COUNT(*) FROM `+table); n != 0 {
			t.Errorf("%s remaining=%d, want 0", table, n)
		}
	}
}

func TestRetention_LeavesNewerRowsAlone(t *testing.T) {
	r, db := newRetention(t, 30, 365)
	now := time.Now().UTC()

	seedRaw(t, db, now.Add(-10*time.Hour).UnixMilli(), "SRV01", "cpu.util", 1)
	seed5Min(t, db, now.Add(-3*24*time.Hour).Truncate(time.Hour).UnixMilli(), "SRV01", "cpu.util")
	seedHourly(t, db, now.Add(-15*24*time.Hour).Truncate(time.Hour).UnixMilli(), "SRV01", "cpu.util")
	seedAudit(t, db, now.Add(-30*24*time.Hour).UnixMilli(), "SRV01", 1)

	seedSessionWorkload(t, db, "session_workload_raw", now.Add(-10*time.Hour).UnixMilli())
	seedSessionWorkload(t, db, "session_workload_5min", now.Add(-3*24*time.Hour).Truncate(time.Hour).UnixMilli())
	seedSessionWorkload(t, db, "session_workload_hourly", now.Add(-15*24*time.Hour).Truncate(time.Hour).UnixMilli())
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
	for _, table := range []string{"session_workload_raw", "session_workload_5min", "session_workload_hourly"} {
		if n := countRows(t, db, `SELECT COUNT(*) FROM `+table); n != 1 {
			t.Errorf("%s remaining=%d, want 1", table, n)
		}
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

	// event_spikes shares the audit cutoff; seed one past and one at boundary.
	seedEventSpike(t, db, auditCutoff-1, "SRV01", "Application")
	seedEventSpike(t, db, auditCutoff, "SRV01", "System")

	res := r.RunOnce(context.Background(), now)
	if res.Outcome != "success" {
		t.Fatalf("outcome=%s reason=%s", res.Outcome, res.Reason)
	}
	if res.RowsAffected != 5 {
		t.Errorf("rows_affected=%d, want 5 (one per tier)", res.RowsAffected)
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
	if n := countRows(t, db, `SELECT COUNT(*) FROM event_spikes`); n != 1 {
		t.Errorf("event_spikes remaining=%d, want 1 (boundary kept)", n)
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

func seedInvestigationAttempt(t *testing.T, db *DB, state string, createdAtMs, startedAtMs int64, sendAuthorizedAtMs, sendCompletedAtMs, sendLeaseExpiresAtMs, finalizationLeaseExpiresAtMs any) int64 {
	t.Helper()
	completedAtMs := any(nil)
	terminalReason := ""
	if state == "failed" {
		completedAtMs = startedAtMs
		terminalReason = "network_error"
	}
	sourceID := createdAtMs
	if sendLeaseExpiresAtMs != nil {
		sourceID++
	}
	if finalizationLeaseExpiresAtMs != nil {
		sourceID += 2
	}
	result, err := db.writer.Exec(
		`INSERT INTO investigation_attempts (
		    source_kind, source_id, attempt_no, initiation, state, created_at_ms,
		    queued_at_ms, started_at_ms, send_authorized_at_ms, send_completed_at_ms,
		    send_lease_expires_at_ms, finalization_lease_expires_at_ms, completed_at_ms,
		    terminal_reason, evidence_hash
		) VALUES ('event_spike', ?, 1, 'manual', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sourceID, state, createdAtMs, createdAtMs, startedAtMs,
		sendAuthorizedAtMs, sendCompletedAtMs, sendLeaseExpiresAtMs,
		finalizationLeaseExpiresAtMs, completedAtMs, terminalReason, make([]byte, 32),
	)
	if err != nil {
		t.Fatalf("seed investigation attempt state=%s: %v", state, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("investigation attempt ID: %v", err)
	}
	if _, err := db.writer.Exec(
		`INSERT INTO investigation_evidence (
		    attempt_id, dto_version, snapshot_kind, source_time_ms, snapshot_at_ms,
		    from_ms, to_ms, canonical_json
		) VALUES (?, 1, 'available', ?, ?, ?, ?, '{}')`,
		id, createdAtMs, createdAtMs, createdAtMs-30*60*1000, createdAtMs,
	); err != nil {
		t.Fatalf("seed investigation evidence: %v", err)
	}
	return id
}

func TestRetention_InvestigationAttemptLeaseRecoveryPrecedesExpiry(t *testing.T) {
	r, db := newRetention(t, 30, 1)
	now := time.Now().UTC().Truncate(time.Millisecond)
	old := now.Add(-2 * 24 * time.Hour).UnixMilli()
	started := old + 1

	sendAuthorizedAt := now.Add(-time.Second).UnixMilli()
	sendLeaseExpiresAt := now.Add(29 * time.Second).UnixMilli()
	sendCompletedAt := now.Add(-2 * time.Millisecond).UnixMilli()
	finalizationLeaseExpiresAt := now.Add(119 * time.Second).UnixMilli()
	sendPhaseID := seedInvestigationAttempt(t, db, "running", old, started,
		sendAuthorizedAt, nil, sendLeaseExpiresAt, nil)
	finalizationPhaseID := seedInvestigationAttempt(t, db, "running", old, started,
		sendAuthorizedAt, sendCompletedAt, nil, finalizationLeaseExpiresAt)
	unsentID := seedInvestigationAttempt(t, db, "running", old, started, nil, nil, nil, nil)

	r.RunOnce(context.Background(), now)
	for _, id := range []int64{sendPhaseID, finalizationPhaseID} {
		var state string
		if err := db.reader.QueryRow(`SELECT state FROM investigation_attempts WHERE id = ?`, id).Scan(&state); err != nil {
			t.Fatalf("active lease attempt %d was not retained: %v", id, err)
		}
		if state != "running" {
			t.Errorf("active lease attempt %d state=%q, want running", id, state)
		}
	}
	var unsentState, unsentReason string
	if err := db.reader.QueryRow(
		`SELECT state, terminal_reason FROM investigation_attempts WHERE id = ?`, unsentID,
	).Scan(&unsentState, &unsentReason); err != nil {
		t.Fatalf("unsent attempt missing: %v", err)
	}
	if unsentState != "failed" || unsentReason != "interrupted" {
		t.Errorf("unsent attempt = (%q, %q), want (failed, interrupted)", unsentState, unsentReason)
	}

	if _, err := db.writer.Exec(
		`UPDATE investigation_attempts
		 SET send_lease_expires_at_ms = ?
		 WHERE id = ?`,
		now.UnixMilli(), sendPhaseID,
	); err != nil {
		t.Fatalf("expire send lease: %v", err)
	}
	if _, err := db.writer.Exec(
		`UPDATE investigation_attempts
		 SET finalization_lease_expires_at_ms = ?
		 WHERE id = ?`,
		now.UnixMilli(), finalizationPhaseID,
	); err != nil {
		t.Fatalf("expire finalization lease: %v", err)
	}
	res := r.RunOnce(context.Background(), now)
	if res.RowsAffected != 2 {
		t.Errorf("rows_affected=%d, want 2 recovery transitions", res.RowsAffected)
	}
	for _, id := range []int64{sendPhaseID, finalizationPhaseID} {
		var state, reason string
		if err := db.reader.QueryRow(
			`SELECT state, terminal_reason FROM investigation_attempts WHERE id = ?`, id,
		).Scan(&state, &reason); err != nil {
			t.Fatalf("recovered attempt %d missing: %v", id, err)
		}
		if state != "failed" || reason != "storage_unavailable" {
			t.Errorf("recovered attempt %d = (%q, %q), want (failed, storage_unavailable)", id, state, reason)
		}
	}

	r.RunOnce(context.Background(), now.Add(time.Millisecond))
	if n := countRows(t, db, `SELECT COUNT(*) FROM investigation_attempts`); n != 0 {
		t.Errorf("attempts after later retention pass=%d, want 0", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM investigation_evidence`); n != 0 {
		t.Errorf("evidence after parent deletion=%d, want 0", n)
	}
}

func TestRetention_ExpiresSessionSourcesAndOnlyUnreferencedAcknowledgements(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	auditCutoff := now.Add(-24 * time.Hour).UnixMilli()
	db := openTestDB(t)
	r := NewRetention(db, 1, func() RetentionSettings {
		return RetentionSettings{MetricsDays: 30, AuditDays: 1, CurrentAcknowledgementAuditID: 2}
	})

	if _, err := db.writer.Exec(
		`INSERT INTO session_drop_anomalies (
		    host, report_epoch_ms, accepted_at_ms, local_offset_minutes, local_date,
		    detected_at_ms, confirmation_started_report_epoch_ms,
		    confirmation_ended_report_epoch_ms, confirmation_started_at_ms,
		    confirmation_ended_at_ms, observed_sessions, reference_sessions,
		    expected_sessions, baseline_model_version, baseline_scope, slot_index,
		    slot_mature_days, tail_probability, absolute_loss, relative_loss,
		    confirmation_window_size, confirmation_1_candidate, confirmation_2_candidate,
		    confirmation_3_candidate, confirmation_count, freshness_context, drain_context,
		    classification, provider_eligible
		) VALUES (
		    'SRV01', 1, 1, 0, '2026-01-01', ?, 1, 1, 1, 1, 1, 2, 2.0,
		    'gamma_poisson_lower_v1', 'all_hours', NULL, NULL, 0.1, 1, 0.5,
		    2, 1, 1, NULL, 2, 'fresh', 'none', 'unexplained', 1
		)`,
		auditCutoff-1,
	); err != nil {
		t.Fatalf("seed session-drop source: %v", err)
	}
	for _, id := range []int64{1, 2} {
		if _, err := db.writer.Exec(
			`INSERT INTO investigation_privacy_acknowledgements
			    (id, acknowledgement_version, actor, accepted_at_ms, clause_set_hash, clause_set_marker)
			 VALUES (?, 'openai_responses_privacy_v1', 'operator', ?, ?, 'openai_responses_privacy_v1_complete_clauses')`,
			id, auditCutoff-1, PrivacyAcknowledgementClauseSetHash(),
		); err != nil {
			t.Fatalf("seed acknowledgement %d: %v", id, err)
		}
	}

	res := r.RunOnce(context.Background(), now)
	if res.RowsAffected != 2 {
		t.Errorf("rows_affected=%d, want 2 (source plus unreferenced acknowledgement)", res.RowsAffected)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM session_drop_anomalies`); n != 0 {
		t.Errorf("session-drop sources=%d, want 0", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM investigation_privacy_acknowledgements WHERE id = 1`); n != 0 {
		t.Errorf("unreferenced acknowledgement=%d, want 0", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM investigation_privacy_acknowledgements WHERE id = 2`); n != 1 {
		t.Errorf("current acknowledgement=%d, want 1", n)
	}
}
