# HTTP Contract: Fleet Metrics Query

## Route

- **Method**: `GET`
- **Path**: `/api/v1/metrics/_fleet`

This is the fleet-wide sentinel form of the existing retained metrics query. The server
implements it through the `/api/v1/metrics/{host}` router using `_fleet` as the host value;
callers treat the contract above as the canonical route.

## Query Parameters

- `from` (required): ISO-8601 UTC timestamp, inclusive lower bound
- `to` (required): ISO-8601 UTC timestamp, exclusive upper bound
- `resolution` (optional): `auto` | `raw` | `5min` | `hourly`; default `auto`
- `counters` (optional): comma-separated counter names; omit to return all available counters

## Success Response

- **Status**: `200 OK`

```json
{
  "host": "_fleet",
  "tier": "5min",
  "from": "2026-04-20T16:00:00Z",
  "to": "2026-04-20T17:00:00Z",
  "oldest_available": "2026-04-15T17:00:00Z",
  "newest_available": "2026-04-20T16:59:45Z",
  "series": {
    "cpu_pct": {
      "t": [1776700800000, 1776701100000],
      "avg": [41.2, 43.8],
      "min": [18.5, 20.1],
      "max": [79.0, 81.4]
    },
    "mem_avail_mb": {
      "t": [1776700800000, 1776701100000],
      "avg": [8192.0, 7680.0],
      "min": [7000.0, 6500.0],
      "max": [10000.0, 9800.0]
    }
  }
}
```

All timestamps (`t`) are Unix milliseconds. `avg`, `min`, `max` are fleet-aggregated values
across all registered hosts for each interval. `oldest_available` and `newest_available` are
non-null when retained data exists; null when the retained store has no rows for any host.

## Counter Families by Overview Chart

| Chart family | Primary counters used by the frontend |
|---|---|
| LOAD | `cpu_pct`, `mem_avail_mb`, `mem_total_mb`, `sessions_total`, `input_delay_p95_ms` |
| HIC | `cpu_pct`, `input_delay_p95_ms`, `pages_sec`, `tcp_retrans_sec`, `disk_queue`, `mem_avail_mb`, `mem_total_mb` |
| SESSIONS | `sessions_total`, `sessions_active`, `sessions_disconnected`, `sessions_max`, `session_cpu_p95_pct`, `session_mem_p95_bytes` |
| REMOTEFX | `rfx_fps_out`, `rfx_fps_out_p50`, `rfx_encode_ms`, `rfx_quality_pct`, `rfx_skip_server_sec`, `rfx_skip_net_sec`, `rfx_rtt_ms`, `rfx_loss_pct` |

RFX counters are only present in the response when RemoteFX data has been collected.
The frontend detects RFX availability by checking whether `rfx_fps_out` has a non-empty
`t` array; the REMOTEFX sub-tab is hidden when absent.

Memory percentage is derived client-side: `(1 - mem_avail_mb / mem_total_mb) * 100`.

## Empty Window Response

- **Status**: `200 OK`

```json
{
  "host": "_fleet",
  "tier": "hourly",
  "from": "2026-04-20T16:00:00Z",
  "to": "2026-04-20T17:00:00Z",
  "oldest_available": null,
  "newest_available": null,
  "series": {}
}
```

The frontend interprets `oldest_available: null` as an explicit empty retained-data state.
Each chart family renders a "No retained history for this window" placeholder. The client
must not substitute browser-local history.

## Error Responses

- `400 invalid_range` — `from`/`to` absent, unparseable, equal, or span > 90 days
- `400 invalid_resolution` — unrecognised resolution value
- `500 storage_error` — metric store unavailable or query failure

When the request fails, each chart family shows an explicit "Unable to reach the metrics
endpoint" error state in red. The client must not substitute browser-local history.

## Notes

- `tier` reports the tier actually served after automatic resolution or degradation.
- The contract preserves current Overview aggregation semantics (LOAD/HIC/SESSIONS/REMOTE FX).
- RFX P50 fields other than `fpsOut` have no stored P50 counter; the frontend defaults them
  to 0 for MVP and shows only the fleet-average value.
