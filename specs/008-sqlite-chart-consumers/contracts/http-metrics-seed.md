# HTTP Contract: Recent Metrics Seed

## Route

- **Method**: `GET`
- **Path**: `/api/v1/metrics`

Returns a bounded recent retained-history slice per host for dashboard consumers that need
per-host history on cold start (sparkline seeding). Called once per browser session on the
first dashboard poll; `seedServerMetrics()` in `frontend/src/lib/state.svelte.js` applies
the result to the per-host ring buffers.

## Query Parameters

- `from` (optional): ISO-8601 UTC timestamp, inclusive lower bound
- `to` (optional): ISO-8601 UTC timestamp, exclusive upper bound
- `resolution` (optional): `raw` only for the initial version; omitted defaults to the
  recent raw window used for sparklines
- `counters` (optional): comma-separated counter names
- `limit` (optional): maximum points per host, bounded server-side

If `from`/`to` are omitted, the server uses the bounded default recent seed window.

## Success Response

- **Status**: `200 OK`

```json
{
  "RDSH-01": [
    {
      "time": 1776700800000,
      "cpu": 37.5,
      "mem": 58.2,
      "inputDelay": 14,
      "sessions": 21,
      "diskQueue": 0.8,
      "tcpRetrans": 0.2
    }
  ],
  "RDSH-02": [
    {
      "time": 1776700800000,
      "cpu": 24.1,
      "mem": 53.6,
      "inputDelay": 8,
      "sessions": 15,
      "diskQueue": 0.4,
      "tcpRetrans": 0.1
    }
  ]
}
```

## Error Responses

- `400 invalid_range`
- `400 invalid_resolution`
- `500 storage_error`

## Notes

- This route is for recent per-host historical seed data only; fleet Overview history uses
  `GET /api/v1/metrics/_fleet` (see `http-fleet-metrics.md`).
- Returned sample objects match the frontend `MetricsSeedSample` typedef in `api.js` and
  are stored directly into the `serverMetrics` ring buffer.
- The endpoint is bounded (default and max limits enforced server-side); it is not a bulk
  export API.
- `seedServerMetrics()` always overwrites any stale browser-local data so retained SQLite
  history is authoritative at cold start.

## Migrated Consumers (US3 complete)

| Surface | File | Seed Behavior |
|---------|------|---------------|
| Per-host CPU sparklines | `ServerTable.svelte` | `s.cpu` from seed slice |
| Per-host Memory sparklines | `ServerTable.svelte` | `s.mem` from seed slice |
| Per-host Input Delay sparklines | `ServerTable.svelte` | `s.inputDelay` from seed slice |
| Per-host Sessions sparklines | `ServerTable.svelte` | `s.sessions` from seed slice |
| ServerDetail sparkline row | `ServerDetail.svelte` | Same `serverMetrics` ring buffer |

## Deferred (no seed needed)

| Surface | Why |
|---------|-----|
| Per-host anomaly spikes | Already server-authoritative via `fetchRecentSpikes()` REST |
| Per-host detector status | Already transient + server-authoritative via SSE |
| Drain history | Production REST `GET /api/v1/history/{host}` — not localStorage-based |
| ServerDetail durable CPU chart | Uses `fetchMetrics(host, …)` directly — already migrated |
