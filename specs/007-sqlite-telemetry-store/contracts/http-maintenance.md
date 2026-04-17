# Contract: GET /api/v1/maintenance/status

Exposes the last-run state of every background maintenance job. Backs the new dashboard widget (FR-030).

## Request

```http
GET /api/v1/maintenance/status
Authorization: Negotiate <gss-token>
```

No query parameters.

## Response (200 OK)

```json
{
  "jobs": [
    {
      "name": "aggregator_5min",
      "started": "2026-04-16T09:04:00Z",
      "finished": "2026-04-16T09:04:00.083Z",
      "duration_ms": 83,
      "outcome": "success",
      "reason": "",
      "rows_affected": 300,
      "overdue": false,
      "expected_interval_seconds": 60
    },
    {
      "name": "retention",
      "started": "2026-04-16T07:30:00Z",
      "finished": "2026-04-16T07:30:04.115Z",
      "duration_ms": 4115,
      "outcome": "success",
      "reason": "",
      "rows_affected": 1287,
      "overdue": false,
      "expected_interval_seconds": 900
    },
    {
      "name": "aggregator_hourly",
      "started": "2026-04-16T09:00:00Z",
      "finished": "2026-04-16T09:00:00.204Z",
      "duration_ms": 204,
      "outcome": "failure",
      "reason": "disk full: insert into metrics_hourly: database or disk is full",
      "rows_affected": 0,
      "overdue": true,
      "expected_interval_seconds": 3600
    }
  ],
  "server_time": "2026-04-16T09:15:23Z"
}
```

### Response fields

| Field | Type | Notes |
|---|---|---|
| `jobs[].name` | string | One of `aggregator_5min`, `aggregator_hourly`, `retention`, `jsonl_migration`, `drift_reconciliation`. New names may appear without client changes. |
| `jobs[].started`, `.finished` | ISO-8601 | |
| `jobs[].duration_ms` | int | |
| `jobs[].outcome` | `"success"` \| `"failure"` \| `"skipped"` | |
| `jobs[].reason` | string | Non-empty on failure. |
| `jobs[].rows_affected` | int | For aggregators: rows inserted. For retention: rows deleted. |
| `jobs[].overdue` | bool | True when `expected_interval_seconds > 0` AND `server_time - finished > 2 * expected_interval_seconds` (FR-031). One-shot startup jobs (`jsonl_migration`, `drift_reconciliation`) report `expected_interval_seconds = 0` and are never overdue. |
| `jobs[].expected_interval_seconds` | int | Server-side config echo. `0` means "one-shot startup job"; UI must not flag as overdue. Field is never null — use `0` as the sentinel. |
| `server_time` | ISO-8601 | Enables the client to compute its own "freshness" ignoring clock skew. |

### Errors

| HTTP | Body `error` | Trigger |
|---|---|---|
| 401 | `unauthorized` | Existing Kerberos failure. |
| 500 | `storage_error` | DB open/query failure. |

## Rate limit

Same middleware; no new bucket. Dashboard refreshes this endpoint at most once every 15 seconds.
