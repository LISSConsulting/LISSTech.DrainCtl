# HTTP Contract: Recent Metrics Seed

## Route

- **Method**: `GET`
- **Path**: `/api/v1/metrics`

This production route replaces the current dev-only sparkline seed behavior and returns a
bounded recent retained-history slice per host for dashboard consumers that still need a
small per-host history map on cold start.

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

- This route is for recent per-host historical seed data, not for Overview fleet history.
- Returned sample objects intentionally match the existing frontend `MetricsSample`-style
  expectations for `serverMetrics` cold-start seeding.
- The endpoint is bounded; it is not a bulk export API.
