# Contract: GET /api/v1/audit

Time-range query over the audit table. Replaces the audit portion of `/api/v1/history/{host}`.

## Request

```http
GET /api/v1/audit?from=<iso8601>&to=<iso8601>&host=<host>&actor=<principal>&limit=<n>&cursor=<opaque>&changes_only=<bool>
Authorization: Negotiate <gss-token>
```

### Query parameters

| Name | Type | Default | Notes |
|---|---|---|---|
| `from` | ISO-8601 UTC | optional | Inclusive lower bound. Omit to query from the oldest retained record. |
| `to` | ISO-8601 UTC | optional | Exclusive upper bound. Omit for now. |
| `host` | string | optional | Filter to one host. |
| `actor` | string | optional | Filter to one principal (`changed_by`). |
| `limit` | int | `500` | 1..5000. Larger values clamped. |
| `cursor` | opaque string | optional | Pagination. Returned in previous response's `next_cursor`. |
| `changes_only` | `true`/`false` | `true` | When `true` (current CLI default), drop no-op records where `prev_state == new_state`. |

### Errors

| HTTP | Body `error` | Trigger |
|---|---|---|
| 400 | `invalid_range` | Unparseable timestamps. |
| 400 | `invalid_cursor` | Cursor from a different filter set or malformed. |
| 401 | `unauthorized` | Existing Kerberos failure. |
| 500 | `storage_error` | DB open or query failure. |

## Response (200 OK)

```json
{
  "records": [
    {
      "ts": "2026-04-16T09:04:12.447Z",
      "host": "RDSH-07",
      "prev_state": 0,
      "prev_state_label": "Off",
      "new_state": 1,
      "new_state_label": "DrainAll",
      "principal": "CONTOSO\\jdoe",
      "changed_by": "CONTOSO\\jdoe",
      "reason": "",
      "key_modified_ts": "2026-04-16T09:04:12.000Z",
      "reconciliation": false
    },
    {
      "ts": "2026-04-16T08:00:00.000Z",
      "host": "RDSH-07",
      "prev_state": 1,
      "prev_state_label": "DrainAll",
      "new_state": 0,
      "new_state_label": "Off",
      "principal": "",
      "changed_by": "",
      "reason": "service-downtime drift: last-known DrainAll, observed Off",
      "key_modified_ts": null,
      "reconciliation": true,
      "before_ts": "2026-04-15T22:14:03.116Z"
    }
  ],
  "next_cursor": "eyJ0cyI6MTc0NDIz…",
  "tier": "audit",
  "total_returned": 2
}
```

### Response fields

| Field | Type | Notes |
|---|---|---|
| `records` | array | Ordered by `ts DESC`. |
| `records[].ts` | ISO-8601 | |
| `records[].prev_state`, `.new_state` | int | DrainMode enum (existing mapping). |
| `records[].prev_state_label`, `.new_state_label` | string | Human label. |
| `records[].reconciliation` | bool | True for drift rows (FR-001a). |
| `records[].before_ts` | ISO-8601 or null | Only present/non-null when `reconciliation = true`. |
| `next_cursor` | string or null | Present when more results remain. Opaque; pass verbatim to next request. |
| `tier` | `"audit"` | Constant; future-proofs alongside `/metrics` which varies tier. |
| `total_returned` | int | Convenience; equals `len(records)`. |

## Rate limit

Same middleware as existing API. No new bucket.
