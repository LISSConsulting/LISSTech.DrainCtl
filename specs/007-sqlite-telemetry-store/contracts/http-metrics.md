# Contract: GET /api/v1/metrics/{host}

Returns per-counter time-series for a single host at a chosen resolution tier.

## Request

```http
GET /api/v1/metrics/{host}?from=<iso8601>&to=<iso8601>&resolution=<tier>&counters=<csv>
Authorization: Negotiate <gss-token>      (existing Kerberos SSO; no change)
```

### Path parameters

| Name | Type | Notes |
|---|---|---|
| `host` | string | URL-encoded hostname. Must exist in `ServerState` or a 404 is returned. |

### Query parameters

| Name | Type | Default | Notes |
|---|---|---|---|
| `from` | ISO-8601 UTC | required | Inclusive lower bound. |
| `to` | ISO-8601 UTC | required | Exclusive upper bound. Must be > `from`. |
| `resolution` | `raw` \| `5min` \| `hourly` \| `auto` | `auto` | `auto` picks the coarsest tier yielding ≥ 240 points in the window. |
| `counters` | CSV of counter names | all known counters | e.g. `cpu_pct,mem_avail_mb`. Unknown names are silently skipped. |

### Errors

| HTTP | Body `error` | Trigger |
|---|---|---|
| 400 | `invalid_range` | `to <= from`, unparseable timestamps, or range > 90 days. |
| 400 | `invalid_resolution` | `resolution` not in the allowed set. |
| 401 | `unauthorized` | missing/invalid Kerberos ticket (existing behaviour). |
| 404 | `unknown_host` | host not registered. |
| 500 | `storage_error` | DB open or query failure. |

## Response (200 OK)

```json
{
  "host": "RDSH-07",
  "tier": "5min",
  "from": "2026-04-15T00:00:00Z",
  "to":   "2026-04-16T00:00:00Z",
  "oldest_available": "2026-04-10T12:00:00Z",
  "newest_available": "2026-04-16T09:15:00Z",
  "series": {
    "cpu_pct": {
      "t": [1744243200000, 1744243500000, 1744243800000],
      "avg": [12.4, 15.1, 13.7],
      "min": [10.0, 11.2, 12.1],
      "max": [18.9, 22.0, 15.5]
    },
    "mem_avail_mb": {
      "t": [...],
      "avg": [...],
      "min": [...],
      "max": [...]
    }
  }
}
```

### Response fields

| Field | Type | Notes |
|---|---|---|
| `host` | string | Echo of the path. |
| `tier` | `"raw"` \| `"5min"` \| `"hourly"` | The tier the server actually served (may differ from request if `auto` or if finer tier has been purged — FR-019). |
| `from`, `to` | ISO-8601 | Echo of the request. |
| `oldest_available`, `newest_available` | ISO-8601 | Timestamps of the oldest and newest rows the chosen tier holds for this host (not just within the request window). Lets the UI show a "data beyond this range is not retained" indicator. |
| `series.<counter>.t` | array of int | Parallel array of Unix-ms timestamps. |
| `series.<counter>.avg` / `.min` / `.max` | array of number | For `tier=raw`, `avg == min == max == value`. |

### Empty-state response

When the tier holds zero rows for the requested host+window, return 200 with an empty series map and the `oldest_available` / `newest_available` fields set to `null`. The UI renders FR-019a ("Collecting data…") based on this shape.

```json
{
  "host": "RDSH-07",
  "tier": "raw",
  "from": "2026-04-16T09:00:00Z",
  "to":   "2026-04-16T09:05:00Z",
  "oldest_available": null,
  "newest_available": null,
  "series": {}
}
```

## Rate limit

Subject to the existing `/api/v1/*` rate-limit middleware (`internal/dashboard/ratelimit.go`). No new bucket.

## Deprecation

`GET /api/v1/history/{host}` (the current endpoint backing the dashboard chart) is retained for one release and then removed. For the cut-over release, it returns 410 Gone with an `error: "use /api/v1/metrics/{host} or /api/v1/audit"` body. The CLI's `drainctl history` command is repointed at the new endpoints before that removal.
