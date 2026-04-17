//go:build windows

package telemetry

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func newAggregator(t *testing.T) (*Aggregator, *MetricsStore, *DB) {
	t.Helper()
	db := openTestDB(t)
	return NewAggregator(db, 60), NewMetricsStore(db), db
}

// pastBucketStart returns the start of an hour bucket comfortably past the
// hourlyWatermark — any sample in this bucket is eligible for aggregation.
func pastBucketStart() time.Time {
	return time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Hour)
}

// readHourlyBucket returns (avg, min, max, count) for the given bucket row or
// (0,0,0,0, false) if the row does not exist.
func readHourlyBucket(t *testing.T, db *DB, host, counter string, bucketMs int64) (float64, float64, float64, int, bool) {
	t.Helper()
	var avg, min, max float64
	var count int
	err := db.reader.QueryRow(
		`SELECT avg_value, min_value, max_value, sample_count
		 FROM metrics_hourly WHERE host=? AND bucket_ts=? AND counter=?`,
		host, bucketMs, counter,
	).Scan(&avg, &min, &max, &count)
	if err != nil {
		return 0, 0, 0, 0, false
	}
	return avg, min, max, count, true
}

func TestHourlyAggregator_IsIdempotent(t *testing.T) {
	agg, ms, db := newAggregator(t)
	ctx := context.Background()

	bucket := pastBucketStart()
	if err := ms.Append(ctx, []Sample{
		{Ts: bucket.Add(5 * time.Minute), Host: "SRV01", Counter: "cpu.util", Value: 10},
		{Ts: bucket.Add(20 * time.Minute), Host: "SRV01", Counter: "cpu.util", Value: 20},
		{Ts: bucket.Add(40 * time.Minute), Host: "SRV01", Counter: "cpu.util", Value: 30},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	agg.rollHourly(ctx)
	avg1, min1, max1, count1, ok := readHourlyBucket(t, db, "SRV01", "cpu.util", bucket.UnixMilli())
	if !ok {
		t.Fatalf("first run: bucket not materialised")
	}

	agg.rollHourly(ctx)
	avg2, min2, max2, count2, ok := readHourlyBucket(t, db, "SRV01", "cpu.util", bucket.UnixMilli())
	if !ok {
		t.Fatalf("second run: bucket missing")
	}

	if avg1 != avg2 || min1 != min2 || max1 != max2 || count1 != count2 {
		t.Errorf("idempotency broken: run1=(%v,%v,%v,%d) run2=(%v,%v,%v,%d)",
			avg1, min1, max1, count1, avg2, min2, max2, count2)
	}

	var rowCount int
	if err := db.reader.QueryRow(
		`SELECT COUNT(*) FROM metrics_hourly WHERE host=? AND counter=?`,
		"SRV01", "cpu.util",
	).Scan(&rowCount); err != nil {
		t.Fatalf("count hourly: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("rerun duplicated rows: got %d, want 1", rowCount)
	}
}

func TestHourlyAggregator_SkipsIncompleteHours(t *testing.T) {
	agg, ms, db := newAggregator(t)
	ctx := context.Background()

	// Current in-progress hour — bucket_ts > (now-watermark).Truncate(hour),
	// so the aggregator must skip it.
	currentBucket := time.Now().UTC().Truncate(time.Hour)
	if err := ms.Append(ctx, []Sample{
		{Ts: time.Now().UTC(), Host: "SRV01", Counter: "cpu.util", Value: 42},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	agg.rollHourly(ctx)

	var count int
	if err := db.reader.QueryRow(
		`SELECT COUNT(*) FROM metrics_hourly WHERE host=? AND bucket_ts=?`,
		"SRV01", currentBucket.UnixMilli(),
	).Scan(&count); err != nil {
		t.Fatalf("count current: %v", err)
	}
	if count != 0 {
		t.Errorf("current-hour bucket materialised (count=%d); watermark violated", count)
	}

	// No eligible samples → no hourly rows overall.
	if err := db.reader.QueryRow(`SELECT COUNT(*) FROM metrics_hourly`).Scan(&count); err != nil {
		t.Fatalf("count hourly: %v", err)
	}
	if count != 0 {
		t.Errorf("metrics_hourly has %d rows despite only in-progress samples", count)
	}
}

func TestHourlyAggregator_RecordsMaintenanceRow(t *testing.T) {
	agg, _, db := newAggregator(t)
	ctx := context.Background()

	before := time.Now().UTC().Add(-time.Millisecond)
	agg.rollHourly(ctx)
	after := time.Now().UTC().Add(time.Millisecond)

	var startedMs, finishedMs, durationMs, rowsAffected int64
	var outcome, reason string
	err := db.reader.QueryRow(
		`SELECT started_ts, finished_ts, duration_ms, outcome, reason, rows_affected
		 FROM maintenance_jobs WHERE name = 'aggregator_hourly'`,
	).Scan(&startedMs, &finishedMs, &durationMs, &outcome, &reason, &rowsAffected)
	if err != nil {
		t.Fatalf("read maintenance_jobs: %v", err)
	}
	if outcome != "success" {
		t.Errorf("outcome = %q, want success (reason=%q)", outcome, reason)
	}
	if startedMs < before.UnixMilli() || finishedMs > after.UnixMilli() {
		t.Errorf("started/finished outside test window: started=%d finished=%d want in [%d,%d]",
			startedMs, finishedMs, before.UnixMilli(), after.UnixMilli())
	}
	if finishedMs < startedMs {
		t.Errorf("finished_ts (%d) < started_ts (%d)", finishedMs, startedMs)
	}
	// duration_ms is stored as finished.Sub(started).Milliseconds() which
	// truncates to whole ms; the UnixMilli difference rounds separately and
	// can be 1 ms larger across a boundary. Accept either answer.
	wallDelta := finishedMs - startedMs
	if durationMs != wallDelta && durationMs != wallDelta-1 {
		t.Errorf("duration_ms (%d) not consistent with wall delta (%d)", durationMs, wallDelta)
	}
	if rowsAffected != 0 {
		t.Errorf("rows_affected = %d on empty DB, want 0", rowsAffected)
	}
}

func TestAggregator_LateSampleUpdatesBucket(t *testing.T) {
	agg, ms, db := newAggregator(t)
	ctx := context.Background()

	bucket := pastBucketStart()
	if err := ms.Append(ctx, []Sample{
		{Ts: bucket.Add(5 * time.Minute), Host: "SRV01", Counter: "cpu.util", Value: 10},
	}); err != nil {
		t.Fatalf("Append initial: %v", err)
	}
	agg.rollHourly(ctx)

	avg, min, max, count, ok := readHourlyBucket(t, db, "SRV01", "cpu.util", bucket.UnixMilli())
	if !ok {
		t.Fatalf("initial bucket not materialised")
	}
	if avg != 10 || min != 10 || max != 10 || count != 1 {
		t.Errorf("after first run: got (avg=%v min=%v max=%v count=%d), want (10,10,10,1)",
			avg, min, max, count)
	}

	if err := ms.Append(ctx, []Sample{
		{Ts: bucket.Add(35 * time.Minute), Host: "SRV01", Counter: "cpu.util", Value: 30},
	}); err != nil {
		t.Fatalf("Append late: %v", err)
	}
	agg.rollHourly(ctx)

	avg, min, max, count, ok = readHourlyBucket(t, db, "SRV01", "cpu.util", bucket.UnixMilli())
	if !ok {
		t.Fatalf("bucket disappeared after rerun")
	}
	if avg != 20 || min != 10 || max != 30 || count != 2 {
		t.Errorf("after late-sample rerun: got (avg=%v min=%v max=%v count=%d), want (20,10,30,2)",
			avg, min, max, count)
	}
}

func TestAggregator_EmitsLogsAndMaintenanceRowPerRun(t *testing.T) {
	agg, ms, db := newAggregator(t)
	ctx := context.Background()

	h := &capturingHandler{}
	orig := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(orig) })

	bucket := pastBucketStart()
	if err := ms.Append(ctx, []Sample{
		{Ts: bucket.Add(10 * time.Minute), Host: "SRV01", Counter: "cpu.util", Value: 42},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	agg.rollHourly(ctx)

	var loggedRows int64 = -1
	var foundLog bool
	h.mu.Lock()
	for _, r := range h.records {
		if !strings.Contains(r.Message, "hourly aggregator") {
			continue
		}
		foundLog = true
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "rows_affected" {
				loggedRows = a.Value.Int64()
			}
			return true
		})
	}
	h.mu.Unlock()
	if !foundLog {
		t.Fatalf("expected a slog record mentioning 'hourly aggregator'")
	}
	if loggedRows < 0 {
		t.Fatalf("log record had no rows_affected attribute")
	}

	var storedRows int64
	var outcome string
	if err := db.reader.QueryRow(
		`SELECT rows_affected, outcome FROM maintenance_jobs WHERE name='aggregator_hourly'`,
	).Scan(&storedRows, &outcome); err != nil {
		t.Fatalf("read maintenance_jobs: %v", err)
	}
	if outcome != "success" {
		t.Errorf("outcome = %q, want success", outcome)
	}
	if loggedRows != storedRows {
		t.Errorf("log.rows_affected (%d) != maintenance_jobs.rows_affected (%d)",
			loggedRows, storedRows)
	}
	if storedRows != 1 {
		t.Errorf("storedRows = %d, want 1 (the single seeded sample)", storedRows)
	}
}
