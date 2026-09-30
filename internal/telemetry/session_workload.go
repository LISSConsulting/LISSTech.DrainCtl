//go:build windows

package telemetry

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
)

const sessionWorkloadBaseBucket = time.Minute

type SessionWorkloadCPU struct {
	P95Pct           float64 `json:"p95_pct"`
	AvgPct           float64 `json:"avg_pct"`
	ObservedSessions float64 `json:"observed_sessions"`
	GE5Avg           float64 `json:"ge_5_avg"`
	GE5Max           uint64  `json:"ge_5_max"`
	GE5RatePct       float64 `json:"ge_5_rate_pct"`
	GE20Avg          float64 `json:"ge_20_avg"`
	GE20Max          uint64  `json:"ge_20_max"`
	GE20RatePct      float64 `json:"ge_20_rate_pct"`
}

type SessionWorkloadMemory struct {
	P95Bytes         float64 `json:"p95_bytes"`
	AvgBytes         float64 `json:"avg_bytes"`
	ObservedSessions float64 `json:"observed_sessions"`
}

type SessionWorkloadCoverage struct {
	ExpectedHosts        int  `json:"expected_hosts"`
	ContributingHosts    int  `json:"contributing_hosts"`
	SuccessfulEmptyHosts int  `json:"successful_empty_hosts"`
	ErrorHosts           int  `json:"error_hosts"`
	StaleHosts           int  `json:"stale_hosts"`
	OfflineHosts         int  `json:"offline_hosts"`
	UnsupportedHosts     int  `json:"unsupported_hosts"`
	Partial              bool `json:"partial"`
}

type SessionWorkloadPoint struct {
	T             int64                   `json:"t"`
	BucketMS      int64                   `json:"bucket_ms"`
	CPU           *SessionWorkloadCPU     `json:"cpu"`
	Memory        *SessionWorkloadMemory  `json:"memory"`
	ReportedHosts map[string]struct{}     `json:"-"`
	Coverage      SessionWorkloadCoverage `json:"coverage"`
}

type SessionWorkloadSeries struct {
	Points []SessionWorkloadPoint `json:"points"`
}

func encodeCountSeries(values []uint64) []byte {
	result := make([]byte, 1, 2+len(values))
	result[0] = 1
	var scratch [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(scratch[:], uint64(len(values)))
	result = append(result, scratch[:n]...)
	for _, value := range values {
		n = binary.PutUvarint(scratch[:], value)
		result = append(result, scratch[:n]...)
	}
	return result
}

func decodeCountSeries(data []byte) ([]uint64, error) {
	if len(data) == 0 || data[0] != 1 {
		return nil, sessiondata.ErrInvalidWorkloadHistogram
	}
	data = data[1:]
	length, n := binary.Uvarint(data)
	if n <= 0 || length > 60 {
		return nil, sessiondata.ErrInvalidWorkloadHistogram
	}
	data = data[n:]
	result := make([]uint64, length)
	for i := range result {
		value, n := binary.Uvarint(data)
		if n <= 0 {
			return nil, sessiondata.ErrInvalidWorkloadHistogram
		}
		result[i] = value
		data = data[n:]
	}
	if len(data) != 0 {
		return nil, sessiondata.ErrInvalidWorkloadHistogram
	}
	return result, nil
}

func upsertSessionWorkload(ctx context.Context, tx *sql.Tx, snapshot sessiondata.SessionSnapshot, received int64) error {
	aggregate := sessiondata.CalculateSessionWorkload(snapshot)
	bucket := time.UnixMilli(received).UTC().Truncate(sessionWorkloadBaseBucket).UnixMilli()
	return upsertSessionWorkloadAggregate(ctx, tx, "session_workload_raw", bucket, CanonicalHostname(snapshot.Host), aggregate,
		[]uint64{aggregate.CPUCount}, []uint64{aggregate.CPUGE5}, []uint64{aggregate.CPUGE20})
}

func upsertSessionWorkloadAggregate(ctx context.Context, tx *sql.Tx, table string, bucket int64, host string, aggregate sessiondata.SessionWorkloadAggregate, observedSeries, ge5Series, ge20Series []uint64) error {
	//nolint:gosec // table is selected exclusively from internal tier constants.
	query := fmt.Sprintf(`INSERT INTO %s (
		bucket_ts, canonical_host, base_sample_count, successful_empty_count, error_sample_count,
		cpu_sum, cpu_count, cpu_histogram, cpu_ge_5_sum, cpu_ge_20_sum,
		cpu_observed_series, cpu_ge_5_series, cpu_ge_20_series,
		memory_sum_bytes, memory_count, memory_zero_count, memory_histogram
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	ON CONFLICT(canonical_host,bucket_ts) DO UPDATE SET
		base_sample_count=excluded.base_sample_count, successful_empty_count=excluded.successful_empty_count,
		error_sample_count=excluded.error_sample_count, cpu_sum=excluded.cpu_sum, cpu_count=excluded.cpu_count,
		cpu_histogram=excluded.cpu_histogram, cpu_ge_5_sum=excluded.cpu_ge_5_sum, cpu_ge_20_sum=excluded.cpu_ge_20_sum,
		cpu_observed_series=excluded.cpu_observed_series, cpu_ge_5_series=excluded.cpu_ge_5_series,
		cpu_ge_20_series=excluded.cpu_ge_20_series, memory_sum_bytes=excluded.memory_sum_bytes,
		memory_count=excluded.memory_count, memory_zero_count=excluded.memory_zero_count,
		memory_histogram=excluded.memory_histogram`, table)
	_, err := tx.ExecContext(ctx, query, bucket, host, aggregate.BaseSampleCount, aggregate.SuccessfulEmptyCount,
		aggregate.ErrorSampleCount, aggregate.CPUSum, aggregate.CPUCount, aggregate.EncodeCPUHistogram(),
		aggregate.CPUGE5, aggregate.CPUGE20, encodeCountSeries(observedSeries), encodeCountSeries(ge5Series),
		encodeCountSeries(ge20Series), aggregate.MemorySumBytes, aggregate.MemoryCount,
		aggregate.MemoryZeroCount, aggregate.EncodeMemoryHistogram())
	if err != nil {
		return fmt.Errorf("telemetry: upsert session workload: %w", err)
	}
	return nil
}

type sessionWorkloadRow struct {
	bucket                                int64
	host                                  string
	agg                                   sessiondata.SessionWorkloadAggregate
	observedSeries, ge5Series, ge20Series []uint64
}

func scanSessionWorkloadRow(scanner interface{ Scan(...any) error }) (sessionWorkloadRow, error) {
	var row sessionWorkloadRow
	var cpuBlob, memoryBlob, observedBlob, ge5Blob, ge20Blob []byte
	if err := scanner.Scan(&row.bucket, &row.host, &row.agg.BaseSampleCount, &row.agg.SuccessfulEmptyCount,
		&row.agg.ErrorSampleCount, &row.agg.CPUSum, &row.agg.CPUCount, &cpuBlob,
		&row.agg.CPUGE5, &row.agg.CPUGE20, &observedBlob, &ge5Blob, &ge20Blob,
		&row.agg.MemorySumBytes, &row.agg.MemoryCount, &row.agg.MemoryZeroCount, &memoryBlob); err != nil {
		return row, err
	}
	cpu, err := sessiondata.DecodeCPUHistogram(cpuBlob)
	if err != nil {
		return row, err
	}
	zero, memory, err := sessiondata.DecodeMemoryHistogram(memoryBlob)
	if err != nil || zero != row.agg.MemoryZeroCount {
		if err == nil {
			err = sessiondata.ErrInvalidWorkloadHistogram
		}
		return row, err
	}
	if row.observedSeries, err = decodeCountSeries(observedBlob); err != nil {
		return row, err
	}
	if row.ge5Series, err = decodeCountSeries(ge5Blob); err != nil {
		return row, err
	}
	if row.ge20Series, err = decodeCountSeries(ge20Blob); err != nil {
		return row, err
	}
	if len(row.observedSeries) != len(row.ge5Series) || len(row.ge5Series) != len(row.ge20Series) {
		return row, sessiondata.ErrInvalidWorkloadHistogram
	}
	row.agg.CPUHistogram = cpu
	row.agg.MemoryHistogram = memory
	return row, nil
}

const sessionWorkloadColumns = `bucket_ts, canonical_host, base_sample_count, successful_empty_count, error_sample_count,
	cpu_sum, cpu_count, cpu_histogram, cpu_ge_5_sum, cpu_ge_20_sum,
	cpu_observed_series, cpu_ge_5_series, cpu_ge_20_series,
	memory_sum_bytes, memory_count, memory_zero_count, memory_histogram`

func sessionWorkloadTier(tier Tier) (table string, bucket time.Duration) {
	switch tier {
	case TierFiveMin:
		return "session_workload_5min", 5 * time.Minute
	case TierHourly:
		return "session_workload_hourly", time.Hour
	default:
		return "session_workload_raw", time.Minute
	}
}

// QuerySessionWorkload merges anonymous per-host rows for only the requested hosts.
func (s *MetricsStore) QuerySessionWorkload(ctx context.Context, hosts []string, from, to time.Time, tier Tier) (*SessionWorkloadSeries, error) {
	result := &SessionWorkloadSeries{Points: []SessionWorkloadPoint{}}
	if len(hosts) == 0 {
		return result, nil
	}
	table, bucket := sessionWorkloadTier(tier)
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(hosts)), ",")
	//nolint:gosec // table is selected from internal tier constants; placeholders contain no values.
	query := fmt.Sprintf("SELECT %s FROM %s WHERE canonical_host IN (%s) AND bucket_ts>=? AND bucket_ts<? ORDER BY bucket_ts, canonical_host", sessionWorkloadColumns, table, placeholders)
	args := make([]any, 0, len(hosts)+2)
	for _, host := range hosts {
		args = append(args, host)
	}
	args = append(args, from.UTC().UnixMilli(), to.UTC().UnixMilli())
	rows, err := s.db.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("telemetry: query session workload: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			slog.Warn("telemetry: close session workload rows", "error", err)
		}
	}()
	type bucketAggregate struct {
		agg                                   sessiondata.SessionWorkloadAggregate
		contributing, empty, errors           int
		maxBase                               uint64
		observedSeries, ge5Series, ge20Series []uint64
		reported                              map[string]struct{}
	}
	buckets := make(map[int64]*bucketAggregate)
	for rows.Next() {
		row, err := scanSessionWorkloadRow(rows)
		if err != nil {
			return nil, fmt.Errorf("telemetry: decode session workload: %w", err)
		}
		entry := buckets[row.bucket]
		if entry == nil {
			entry = &bucketAggregate{reported: make(map[string]struct{})}
			buckets[row.bucket] = entry
		}
		entry.agg.Merge(row.agg)
		if row.agg.BaseSampleCount > 0 {
			entry.contributing++
		}
		if row.agg.SuccessfulEmptyCount > 0 {
			entry.empty++
		}
		if row.agg.BaseSampleCount == 0 && row.agg.ErrorSampleCount > 0 {
			entry.errors++
		}
		entry.maxBase = max(entry.maxBase, row.agg.BaseSampleCount)
		if len(entry.observedSeries) == 0 {
			entry.observedSeries = make([]uint64, len(row.observedSeries))
			entry.ge5Series = make([]uint64, len(row.ge5Series))
			entry.ge20Series = make([]uint64, len(row.ge20Series))
		}
		if len(entry.observedSeries) != len(row.observedSeries) {
			return nil, sessiondata.ErrInvalidWorkloadHistogram
		}
		for i := range row.observedSeries {
			entry.observedSeries[i] += row.observedSeries[i]
			entry.ge5Series[i] += row.ge5Series[i]
			entry.ge20Series[i] += row.ge20Series[i]
		}
		entry.reported[row.host] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	keys := make([]int64, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		entry := buckets[key]
		point := SessionWorkloadPoint{T: key, BucketMS: bucket.Milliseconds(), Coverage: SessionWorkloadCoverage{
			ExpectedHosts: len(hosts), ContributingHosts: entry.contributing, SuccessfulEmptyHosts: entry.empty,
			ErrorHosts: entry.errors,
		}, ReportedHosts: entry.reported}
		point.Coverage.UnsupportedHosts = max(0, point.Coverage.ExpectedHosts-point.Coverage.ContributingHosts-point.Coverage.ErrorHosts)
		point.Coverage.Partial = point.Coverage.ContributingHosts < point.Coverage.ExpectedHosts
		if entry.agg.CPUCount > 0 {
			p95, ok := entry.agg.CPUPercentile(.95)
			if !ok {
				return nil, sessiondata.ErrInvalidWorkloadHistogram
			}
			divisor := float64(max(uint64(1), entry.maxBase))
			point.CPU = &SessionWorkloadCPU{P95Pct: p95, AvgPct: entry.agg.CPUSum / float64(entry.agg.CPUCount),
				ObservedSessions: float64(entry.agg.CPUCount) / divisor, GE5Avg: float64(entry.agg.CPUGE5) / divisor, GE5Max: maxSeries(entry.ge5Series),
				GE5RatePct: float64(entry.agg.CPUGE5) / float64(entry.agg.CPUCount) * 100,
				GE20Avg:    float64(entry.agg.CPUGE20) / divisor, GE20Max: maxSeries(entry.ge20Series),
				GE20RatePct: float64(entry.agg.CPUGE20) / float64(entry.agg.CPUCount) * 100}
		}
		if entry.agg.MemoryCount > 0 {
			p95, ok := entry.agg.MemoryPercentile(.95)
			if !ok || math.IsNaN(p95) {
				return nil, sessiondata.ErrInvalidWorkloadHistogram
			}
			point.Memory = &SessionWorkloadMemory{P95Bytes: p95, AvgBytes: entry.agg.MemorySumBytes / float64(entry.agg.MemoryCount), ObservedSessions: float64(entry.agg.MemoryCount) / float64(max(uint64(1), entry.maxBase))}
		}
		result.Points = append(result.Points, point)
	}
	return result, nil
}

func maxSeries(values []uint64) uint64 {
	var maximum uint64
	for _, value := range values {
		maximum = max(maximum, value)
	}
	return maximum
}

func (a *Aggregator) rollSessionWorkload(ctx context.Context, source, target string, bucket time.Duration, maxBucketMS int64) error {
	//nolint:gosec // source is selected exclusively from internal rollup tier constants.
	query := fmt.Sprintf("SELECT %s FROM %s WHERE (bucket_ts / ?) * ? <= ? ORDER BY canonical_host, bucket_ts", sessionWorkloadColumns, source)
	rows, err := a.db.reader.QueryContext(ctx, query, bucket.Milliseconds(), bucket.Milliseconds(), maxBucketMS)
	if err != nil {
		return fmt.Errorf("telemetry: query workload rollup: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			slog.Warn("telemetry: close session workload rollup rows", "error", err)
		}
	}()
	type key struct {
		host   string
		bucket int64
	}
	type rolled struct {
		agg                                   sessiondata.SessionWorkloadAggregate
		observedSeries, ge5Series, ge20Series []uint64
	}
	groups := make(map[key]*rolled)
	for rows.Next() {
		row, err := scanSessionWorkloadRow(rows)
		if err != nil {
			return fmt.Errorf("telemetry: decode workload rollup: %w", err)
		}
		k := key{host: row.host, bucket: (row.bucket / bucket.Milliseconds()) * bucket.Milliseconds()}
		group := groups[k]
		if group == nil {
			length := int(bucket / sessionWorkloadBaseBucket)
			group = &rolled{
				observedSeries: make([]uint64, length),
				ge5Series:      make([]uint64, length),
				ge20Series:     make([]uint64, length),
			}
			groups[k] = group
		}
		group.agg.Merge(row.agg)
		offset := int(time.Duration(row.bucket-k.bucket) * time.Millisecond / sessionWorkloadBaseBucket)
		if offset < 0 || offset+len(row.observedSeries) > len(group.observedSeries) {
			return sessiondata.ErrInvalidWorkloadHistogram
		}
		for i := range row.observedSeries {
			group.observedSeries[offset+i] += row.observedSeries[i]
			group.ge5Series[offset+i] += row.ge5Series[i]
			group.ge20Series[offset+i] += row.ge20Series[i]
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	tx, err := a.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("telemetry: begin workload rollup: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	for k, group := range groups {
		if err := upsertSessionWorkloadAggregate(ctx, tx, target, k.bucket, k.host, group.agg, group.observedSeries, group.ge5Series, group.ge20Series); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("telemetry: commit workload rollup: %w", err)
	}
	return nil
}
