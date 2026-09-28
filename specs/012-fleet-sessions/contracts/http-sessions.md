# Contract: Sessions HTTP API

Sessions data and action routes require existing authentication plus authoritative `/api/v1/me.is_admin:true`. Non-admin callers receive `403 {"error":"admin_required"}` before any lookup. Both read endpoints set `Cache-Control: no-store`, use a bounded SQLite read timeout, and return `503 {"error":"sessions_unavailable"}` on database timeout/failure. All times are Unix milliseconds; nullable fields are explicit JSON `null`. Browser-facing `sequence` and `working_set_bytes` values are canonical unsigned decimal strings. The existing settings `GET` may expose nonsecret `SessionsConfig` to any authenticated user, but mutation of `allow_actions`, privacy, or any Sessions setting requires dashboard-admin authorization.

## GET `/api/v1/sessions`

```http
GET /api/v1/sessions?q=rdsh&state=active&sort=last_activity&dir=desc&page=1&page_size=30
```

| Parameter | Rule |
|---|---|
| `q` | optional UTF-8 0–128 bytes, no controls; case-insensitive match on canonical host and decimal session ID in every privacy mode; matches persisted user identity only when `identity_visibility=full` |
| `state` | `all` (default), `active`, `disconnected`, or `idle` |
| `sort` | `host`, `status`, `mode`, `sessions`, `active`, `idle`, `disconnected`, `users`, or `last_activity`; default `host` |
| `dir` | `asc` (default) or `desc` |
| `page` | integer 1..10000, default 1 |
| `page_size` | exactly 15, 30 (default), or 50 |

Invalid input returns `400 {"error":"invalid_sessions_query"}`. The server applies filter/sort before pagination, appends canonical host ascending as deterministic tie-breaker, and never returns PII/process data in this fleet envelope.

```json
{
  "query":{"q":"rdsh","state":"active","sort":"last_activity","dir":"desc","page":1,"page_size":30},
  "total":1,
  "page":{"number":1,"size":30,"pages":1},
  "server_now_ms":1770000124500,
  "items":[{
    "host":"rdsh-07.example.test",
    "mode":"online",
    "freshness":"fresh",
    "latest_attempt_instance_id":"f47ac10b-58cc-4372-a567-0e02b2c3d479",
    "latest_attempt_sequence":"42",
    "latest_attempt_observed_at_ms":1770000123456,
    "latest_attempt_received_at_ms":1770000123999,
    "last_success_instance_id":"f47ac10b-58cc-4372-a567-0e02b2c3d479",
    "last_success_sequence":"42",
    "last_success_observed_at_ms":1770000123456,
    "last_success_received_at_ms":1770000123999,
    "session_count":12,
    "active_count":9,
    "idle_count":1,
    "disconnected_count":2,
    "user_count":10,
    "last_activity_at_ms":1770000123000,
    "capabilities":{"session_actions":true,"processes":true,"input_delay":true,"remotefx":false},
    "collection_status":"ok",
    "collection_error_code":null,
    "detail_available":true
  }]
}
```

`latest_attempt_instance_id` and `latest_attempt_sequence` identify the newest accepted collection attempt; `last_success_instance_id` and `last_success_sequence` identify the newest accepted complete successful snapshot. Sequences are canonical unsigned decimal strings so browser JSON never loses `uint64` precision. For an instance, the server accepts only a strictly newer sequence; equal or older sequences are stale. A different UUID begins a new generation because the previous service process is terminated or cancelled before restart reporting. `observed_at_ms` is diagnostic only, never an ordering or freshness source. `latest_attempt_observed_at_ms` and `latest_attempt_received_at_ms` describe the most recently accepted collection attempt. `last_success_observed_at_ms` and `last_success_received_at_ms` are nullable and describe the most recent accepted complete successful snapshot. Freshness is `unknown` without success; otherwise `offline` if host registry status is off or receipt age exceeds ten heartbeat intervals, `stale` above three intervals, and `fresh` otherwise. A latest collection error separately sets `collection_status:"error"`; UI display precedence is offline, error, stale, fresh/unknown.

## GET `/api/v1/sessions/{host}`

`{host}` is decoded once and must be canonical lower-case RFC 1123 host bytes 1–253. It rejects slash, encoded slash, control characters, and trailing dot with `400 {"error":"invalid_host"}`. Unknown or retention-expired data returns `404 {"error":"session_snapshot_not_found"}`.

The response contains the most recent accepted attempt plus, when available, one latest complete successful snapshot and at most 500 current rows. It supports only local-presentation query parameters: `q` (0–128 UTF-8 bytes), `page` (1..100), and `page_size` (15, 30, 50). Filtering/paging is applied after bounded host snapshot read; it cannot produce an unbounded fleet query. Session ordering is state rank (`active`, `connected`, `connect_query`, `shadow`, `disconnected`, `idle`, `listen`, `reset`, `down`, `init`, `unknown`) then session ID ascending; `unknown` is always rendered last.

```json
{
  "host":"rdsh-07.example.test",
  "latest_attempt_instance_id":"f47ac10b-58cc-4372-a567-0e02b2c3d479",
  "latest_attempt_sequence":"42",
  "latest_attempt_observed_at_ms":1770000123456,
  "latest_attempt_received_at_ms":1770000123999,
  "last_success_instance_id":"f47ac10b-58cc-4372-a567-0e02b2c3d479",
  "last_success_sequence":"42",
  "last_success_observed_at_ms":1770000123456,
  "last_success_received_at_ms":1770000123999,
  "freshness":"fresh",
  "collection_status":"ok",
  "collection_error_code":null,
  "capabilities":{"session_actions":true,"processes":true,"input_delay":true,"remotefx":false},
  "actions_available":false,
  "summary":{"total":12,"active":9,"idle":1,"disconnected":2,"users":10,"last_activity_at_ms":1770000123000},
  "query":{"q":"","page":1,"page_size":30},
  "total":12,
  "sessions":[{"session_id":3,"logon_at_ms":1769990000000,"user":"a***","domain":"C***","state":"active","station":"r***","client_name":"***","client_address":"***","connect_at_ms":1769990100000,"disconnect_at_ms":null,"idle_since_ms":null,"cpu_percent":7.25,"working_set_bytes":"123731968","input_delay_ms":14,"remotefx":null,"processes":[]}]
}
```

Every returned session has every property shown. `sequence` and any non-null `working_set_bytes`, including a process value, are canonical unsigned decimal strings; `pid` remains a numeric `uint32` and CPU remains nullable numeric. `processes` has at most five entries in collector order: `cpu_percent` descending with null last, then `working_set_bytes` descending, case-insensitive `image_name` ascending, then PID ascending. A per-process `cpu_percent` is nullable for an inaccessible or first delta; session and process missing live metrics are `null`, never zero. The configured ingest-time projection controls identity/client/process values: hidden is `null`/`[]`; a masked process keeps its PID and metrics but returns `image_name:"***"`. A fatal latest collection error preserves these rows and summary from the last successful snapshot while exposing `collection_status:"error"` and its bounded error code. `actions_available` is true only for admin, `allow_actions=true`, a fresh error-free successful snapshot, and `session_actions:true`.

## Common statuses

| Status | Meaning |
|---:|---|
| 200 | authorized valid read |
| 400 | bounded request/path invalid |
| 401 | existing authentication failure |
| 403 | authenticated non-admin |
| 404 | unknown/expired snapshot |
| 429 | existing API limiter |
| 503 | bounded SQLite operation unavailable |

Action mutation is specified by [session-actions.md](./session-actions.md). List/detail never include queued command IDs or message data.
