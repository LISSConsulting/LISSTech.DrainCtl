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
	ms, err := NewMetricsStore(context.Background(), db)
	if err != nil {
		t.Fatalf("NewMetricsStore: %v", err)
	}
	t.Cleanup(func() { _ = ms.Close() })
	return NewAggregator(db, 60), ms, db
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

// readFiveMinBucket returns (avg, min, max, count) for the given bucket row or
// (0,0,0,0, false) if the row does not exist.
func readFiveMinBucket(t *testing.T, db *DB, host, counter string, bucketMs int64) (float64, float64, float64, int, bool) {
	t.Helper()
	var avg, min, max float64
	var count int
	err := db.reader.QueryRow(
		`SELECT avg_value, min_value, max_value, sample_count
		 FROM metrics_5min WHERE host=? AND bucket_ts=? AND counter=?`,
		host, bucketMs, counter,
	).Scan(&avg, &min, &max, &count)
	if err != nil {
		return 0, 0, 0, 0, false
	}
	return avg, min, max, count, true
}

func TestFiveMinuteAggregator_IsIdempotent(t *testing.T) {
	agg, ms, db := newAggregator(t)
	ctx := context.Background()

	bucket := pastBucketStart().Add(5 * time.Minute)
	if err := ms.Append(ctx, []Sample{
		{Ts: bucket.Add(10 * time.Second), Host: "SRV01", Counter: "cpu.util", Value: 10},
		{Ts: bucket.Add(2 * time.Minute), Host: "SRV01", Counter: "cpu.util", Value: 20},
		{Ts: bucket.Add(4 * time.Minute), Host: "SRV01", Counter: "cpu.util", Value: 30},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	now := time.Now().UTC()
	agg.roll5Min(ctx, now)
	avg1, min1, max1, count1, ok := readFiveMinBucket(t, db, "SRV01", "cpu.util", bucket.UnixMilli())
	if !ok {
		t.Fatalf("first run: bucket not materialised")
	}

	agg.roll5Min(ctx, now)
	avg2, min2, max2, count2, ok := readFiveMinBucket(t, db, "SRV01", "cpu.util", bucket.UnixMilli())
	if !ok {
		t.Fatalf("second run: bucket missing")
	}

	if avg1 != avg2 || min1 != min2 || max1 != max2 || count1 != count2 {
		t.Errorf("idempotency broken: run1=(%v,%v,%v,%d) run2=(%v,%v,%v,%d)",
			avg1, min1, max1, count1, avg2, min2, max2, count2)
	}
	if avg1 != 20 || min1 != 10 || max1 != 30 || count1 != 3 {
		t.Errorf("first run: got (avg=%v min=%v max=%v count=%d), want (20,10,30,3)",
			avg1, min1, max1, count1)
	}

	var rowCount int
	if err := db.reader.QueryRow(
		`SELECT COUNT(*) FROM metrics_5min WHERE host=? AND counter=?`,
		"SRV01", "cpu.util",
	).Scan(&rowCount); err != nil {
		t.Fatalf("count 5min: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("rerun duplicated rows: got %d, want 1", rowCount)
	}
}

func TestFiveMinuteAggregator_BucketBoundaries(t *testing.T) {
	agg, ms, db := newAggregator(t)
	ctx := context.Background()

	bucket := pastBucketStart().Add(5 * time.Minute)
	if err := ms.Append(ctx, []Sample{
		{Ts: bucket, Host: "SRV01", Counter: "cpu.util", Value: 10},
		{Ts: bucket.Add(299999 * time.Millisecond), Host: "SRV01", Counter: "cpu.util", Value: 20},
		{Ts: bucket.Add(300000 * time.Millisecond), Host: "SRV01", Counter: "cpu.util", Value: 30},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	agg.roll5Min(ctx, time.Now().UTC())

	rows, err := db.reader.Query(
		`SELECT bucket_ts, sample_count FROM metrics_5min
		 WHERE host=? AND counter=? ORDER BY bucket_ts`,
		"SRV01", "cpu.util",
	)
	if err != nil {
		t.Fatalf("query 5min buckets: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var buckets []int64
	var counts []int
	for rows.Next() {
		var bucketMs int64
		var count int
		if err := rows.Scan(&bucketMs, &count); err != nil {
			t.Fatalf("scan 5min bucket: %v", err)
		}
		buckets = append(buckets, bucketMs)
		counts = append(counts, count)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate 5min buckets: %v", err)
	}

	if len(buckets) != 2 {
		t.Fatalf("got %d buckets, want 2 (buckets=%v counts=%v)", len(buckets), buckets, counts)
	}
	if buckets[0] != bucket.UnixMilli() || buckets[1] != bucket.Add(5*time.Minute).UnixMilli() {
		t.Errorf("bucket boundaries = %v, want [%d %d]",
			buckets, bucket.UnixMilli(), bucket.Add(5*time.Minute).UnixMilli())
	}
	if counts[0] != 2 || counts[1] != 1 {
		t.Errorf("bucket counts = %v, want [2 1]", counts)
	}
}

func TestHourlyFromFiveMin_Matches_HourlyFromRaw(t *testing.T) {
	agg, ms, db := newAggregator(t)
	ctx := context.Background()

	bucket := pastBucketStart()
	samples := make([]Sample, 0, 12)
	for i := 0; i < 12; i++ {
		samples = append(samples, Sample{
			Ts:      bucket.Add(time.Duration(i) * 5 * time.Minute),
			Host:    "SRV01",
			Counter: "cpu.util",
			Value:   float64(i + 1),
		})
	}
	if err := ms.Append(ctx, samples); err != nil {
		t.Fatalf("Append: %v", err)
	}

	now := time.Now().UTC()
	agg.roll5Min(ctx, now)
	agg.rollHourly(ctx, now)

	avg, min, max, count, ok := readHourlyBucket(t, db, "SRV01", "cpu.util", bucket.UnixMilli())
	if !ok {
		t.Fatalf("hourly bucket not materialised")
	}
	if avg != 6.5 || min != 1 || max != 12 || count != 12 {
		t.Errorf("hourly bucket: got (avg=%v min=%v max=%v count=%d), want (6.5,1,12,12)",
			avg, min, max, count)
	}

	var weightedAvg float64
	var weightedCount int
	if err := db.reader.QueryRow(
		`SELECT SUM(avg_value * sample_count) / SUM(sample_count), SUM(sample_count)
		 FROM metrics_5min WHERE host=? AND counter=?`,
		"SRV01", "cpu.util",
	).Scan(&weightedAvg, &weightedCount); err != nil {
		t.Fatalf("weighted 5min aggregate: %v", err)
	}
	if weightedAvg != 6.5 || weightedCount != 12 {
		t.Errorf("weighted 5min aggregate: got avg=%v count=%d, want avg=6.5 count=12",
			weightedAvg, weightedCount)
	}
}

func TestAggregator_MissedMinuteZeroTickStillMaterializes(t *testing.T) {
	agg, ms, db := newAggregator(t)
	ctx := context.Background()

	bucket := pastBucketStart()
	samples := make([]Sample, 0, 12)
	for i := 0; i < 12; i++ {
		samples = append(samples, Sample{
			Ts:      bucket.Add(time.Duration(i) * 5 * time.Minute),
			Host:    "SRV01",
			Counter: "cpu.util",
			Value:   float64(10 + i),
		})
	}
	if err := ms.Append(ctx, samples); err != nil {
		t.Fatalf("Append: %v", err)
	}

	tick1 := bucket.Add(59 * time.Minute)
	agg.roll5Min(ctx, tick1)
	agg.rollHourly(ctx, tick1)
	if _, _, _, _, ok := readHourlyBucket(t, db, "SRV01", "cpu.util", bucket.UnixMilli()); ok {
		t.Fatalf("hourly bucket materialised before hourly watermark")
	}

	tick2 := bucket.Add(time.Hour + 59*time.Minute)
	agg.roll5Min(ctx, tick2)
	agg.rollHourly(ctx, tick2)
	avg, min, max, count, ok := readHourlyBucket(t, db, "SRV01", "cpu.util", bucket.UnixMilli())
	if !ok {
		t.Fatalf("hourly bucket not materialised on :59 tick after watermark")
	}
	if avg != 15.5 || min != 10 || max != 21 || count != 12 {
		t.Errorf("hourly bucket: got (avg=%v min=%v max=%v count=%d), want (15.5,10,21,12)",
			avg, min, max, count)
	}
}

func TestAggregator_LateSampleAfterWatermarkIsIgnored(t *testing.T) {
	agg, ms, db := newAggregator(t)
	ctx := context.Background()

	bucket := pastBucketStart().Add(5 * time.Minute)
	now := bucket.Add(20 * time.Minute)
	if err := ms.Append(ctx, []Sample{
		{Ts: bucket.Add(time.Minute), Host: "SRV01", Counter: "cpu.util", Value: 10},
	}); err != nil {
		t.Fatalf("Append initial: %v", err)
	}
	agg.roll5Min(ctx, now)

	avg, min, max, count, ok := readFiveMinBucket(t, db, "SRV01", "cpu.util", bucket.UnixMilli())
	if !ok {
		t.Fatalf("initial bucket not materialised")
	}
	if avg != 10 || min != 10 || max != 10 || count != 1 {
		t.Errorf("after first run: got (avg=%v min=%v max=%v count=%d), want (10,10,10,1)",
			avg, min, max, count)
	}

	if err := ms.Append(ctx, []Sample{
		{Ts: bucket.Add(2 * time.Minute), Host: "SRV01", Counter: "cpu.util", Value: 30},
	}); err != nil {
		t.Fatalf("Append late: %v", err)
	}
	// The current contract treats the watermark as an eligibility cutoff, not
	// a lock. Eligible buckets are unconditionally recomputed on later ticks.
	agg.roll5Min(ctx, now.Add(time.Minute))

	avg, min, max, count, ok = readFiveMinBucket(t, db, "SRV01", "cpu.util", bucket.UnixMilli())
	if !ok {
		t.Fatalf("bucket disappeared after rerun")
	}
	if avg != 20 || min != 10 || max != 30 || count != 2 {
		t.Errorf("after post-watermark rerun: got (avg=%v min=%v max=%v count=%d), want (20,10,30,2)",
			avg, min, max, count)
	}
}

func TestAggregator_DSTCrossoverBuckets(t *testing.T) {
	agg, ms, db := newAggregator(t)
	ctx := context.Background()

	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("zoneinfo unavailable: %v", err)
	}
	utcInstant := time.Date(2026, 3, 8, 7, 30, 0, 0, time.UTC)
	localInstant := utcInstant.In(loc)
	if localInstant.Hour() != 3 {
		t.Fatalf("expected spring-forward instant to land in local hour 3, got %v", localInstant)
	}

	if err := ms.Append(ctx, []Sample{
		{Ts: localInstant, Host: "SRV01", Counter: "cpu.util", Value: 10},
		{Ts: utcInstant.Add(5 * time.Minute), Host: "SRV01", Counter: "cpu.util", Value: 20},
		{Ts: utcInstant.Add(10 * time.Minute), Host: "SRV01", Counter: "cpu.util", Value: 30},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	now := utcInstant.Add(24 * time.Hour)
	agg.roll5Min(ctx, now)
	agg.rollHourly(ctx, now)

	rows, err := db.reader.Query(
		`SELECT bucket_ts FROM metrics_5min
		 WHERE host=? AND counter=? ORDER BY bucket_ts`,
		"SRV01", "cpu.util",
	)
	if err != nil {
		t.Fatalf("query 5min buckets: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var got []int64
	for rows.Next() {
		var bucketMs int64
		if err := rows.Scan(&bucketMs); err != nil {
			t.Fatalf("scan 5min bucket: %v", err)
		}
		got = append(got, bucketMs)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate 5min buckets: %v", err)
	}

	want := []int64{
		utcInstant.Truncate(5 * time.Minute).UnixMilli(),
		utcInstant.Add(5 * time.Minute).Truncate(5 * time.Minute).UnixMilli(),
		utcInstant.Add(10 * time.Minute).Truncate(5 * time.Minute).UnixMilli(),
	}
	if len(got) != len(want) {
		t.Fatalf("got %d 5min buckets %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("5min bucket[%d] = %d, want %d", i, got[i], want[i])
		}
	}
	if got[1]-got[0] != 300000 || got[2]-got[1] != 300000 {
		t.Errorf("5min bucket spacing = [%d %d], want [300000 300000]",
			got[1]-got[0], got[2]-got[1])
	}

	_, _, _, count, ok := readHourlyBucket(t, db, "SRV01", "cpu.util", utcInstant.Truncate(time.Hour).UnixMilli())
	if !ok {
		t.Fatalf("hourly bucket not materialised")
	}
	if count != 3 {
		t.Errorf("hourly bucket count = %d, want 3", count)
	}
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

	now := time.Now().UTC()
	agg.roll5Min(ctx, now)
	agg.rollHourly(ctx, now)
	avg1, min1, max1, count1, ok := readHourlyBucket(t, db, "SRV01", "cpu.util", bucket.UnixMilli())
	if !ok {
		t.Fatalf("first run: bucket not materialised")
	}

	now = time.Now().UTC()
	agg.roll5Min(ctx, now)
	agg.rollHourly(ctx, now)
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

	now := time.Now().UTC()
	agg.roll5Min(ctx, now)
	agg.rollHourly(ctx, now)

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

	// Same applies one tier down: the in-progress 5-minute bucket holding the
	// freshly written sample must not have been materialised either.
	if err := db.reader.QueryRow(`SELECT COUNT(*) FROM metrics_5min`).Scan(&count); err != nil {
		t.Fatalf("count 5min: %v", err)
	}
	if count != 0 {
		t.Errorf("metrics_5min has %d rows despite only in-progress samples", count)
	}
}

func TestHourlyAggregator_RecordsMaintenanceRow(t *testing.T) {
	agg, _, db := newAggregator(t)
	ctx := context.Background()

	before := time.Now().UTC().Add(-time.Millisecond)
	agg.rollHourly(ctx, time.Now().UTC())
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
	now := time.Now().UTC()
	agg.roll5Min(ctx, now)
	agg.rollHourly(ctx, now)

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
	now = time.Now().UTC()
	agg.roll5Min(ctx, now)
	agg.rollHourly(ctx, now)

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

	now := time.Now().UTC()
	agg.roll5Min(ctx, now)
	agg.rollHourly(ctx, now)

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
