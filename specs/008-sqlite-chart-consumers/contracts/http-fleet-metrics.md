# HTTP Contract: Fleet Metrics Query

## Route

- **Method**: `GET`
- **Path**: `/api/v1/metrics/_fleet`

This is the fleet-wide sentinel form of the existing retained metrics query. The server
may implement it through the existing `/api/v1/metrics/{host}` router using `_fleet` as
the accepted `host` value, but callers treat the contract above as the canonical route.

## Query Parameters

- `from` (required): ISO-8601 UTC timestamp, inclusive lower bound
- `to` (required): ISO-8601 UTC timestamp, exclusive upper bound
- `resolution` (optional): `auto` | `raw` | `5min` | `hourly`; default `auto`
- `counters` (optional): comma-separated counter names

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
    "cpu_p95": {
      "t": [1776700800000, 1776701100000],
      "avg": [66.0, 69.5],
      "min": [66.0, 69.5],
      "max": [66.0, 69.5]
    }
  }
}
```

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

The frontend interprets this as an explicit empty or unavailable retained-data state for
the selected shared window.

## Error Responses

- `400 invalid_range`
- `400 invalid_resolution`
- `500 storage_error`

When the request fails, the chart remains visible and shows an explicit query error
state. The client must not substitute browser-local history.

## Notes

- The contract preserves current Overview aggregation semantics rather than redefining
  the meaning of LOAD/HIC/SESSIONS/REMOTE FX.
- `tier` reports the tier actually served after automatic resolution or degradation.
