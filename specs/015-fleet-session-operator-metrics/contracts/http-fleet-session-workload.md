# Contract: Fleet Session Workload Metrics

## Endpoint

`GET /api/v1/metrics/_fleet`

Authentication, `from`, `to`, `resolution`, repeated `host`, and `counters` validation retain the existing fleet metrics contract. The response adds an optional `session_workload` object. It is present for fleet queries even when `points` is empty; pre-feature history is not synthesized.

## Response addition

```json
{
  "session_workload": {
    "points": [
      {
        "t": 1790640000000,
        "bucket_ms": 300000,
        "cpu": {
          "p95_pct": 18.5,
          "avg_pct": 6.47,
          "observed_sessions": 102,
          "ge_5_avg": 11.4,
          "ge_5_max": 15,
          "ge_5_rate_pct": 11.18,
          "ge_20_avg": 2.0,
          "ge_20_max": 3,
          "ge_20_rate_pct": 1.96
        },
        "memory": {
          "p95_bytes": 912680550,
          "avg_bytes": 387241115,
          "observed_sessions": 99
        },
        "coverage": {
          "expected_hosts": 17,
          "contributing_hosts": 14,
          "successful_empty_hosts": 1,
          "error_hosts": 1,
          "stale_hosts": 1,
          "offline_hosts": 0,
          "unsupported_hosts": 1,
          "partial": true
        }
      }
    ]
  }
}
```

`cpu` and `memory` are nullable independently when that metric has zero valid observations. A point is omitted when zero hosts contributed a successful snapshot. Numeric zero is retained when it is a valid statistic.

## Semantics

- CPU is normalized to total host CPU capacity before aggregation and thresholding.
- AVG is pooled observation sum/count.
- P95 is nearest-rank over a merged anonymous histogram, never an average or percentile of host summaries.
- CPU threshold counts are average concurrent counts for retained buckets; `*_max` is the maximum concurrent count. For raw one-minute points, average and max are equal.
- Rates use CPU-observed sessions as denominator. If no CPU observations exist, CPU metric/rates are null.
- `expected_hosts` is the selected registered host count; omitted `host` means all registered hosts.
- A successful empty snapshot contributes host coverage and no observations.
- Error, stale, offline, and unsupported hosts do not contribute carried-forward observations.
- Histogram error bounds are ≤0.5 CPU percentage point and ≤2% memory relative error.
- No workload object contains session/user/client/process/action identity.

## Compatibility

The existing `series` object remains unchanged for other charts, including Sessions Trend and Utilization. Legacy `session_cpu_p95_pct` and `session_mem_p95_bytes` values are not exposed as aliases for new workload metrics. Old agents simply produce missing/unsupported workload coverage while heartbeat and other telemetry continue normally.
