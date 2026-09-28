# Contract: Agent Session Snapshot Ingest

`POST /api/v1/session-snapshot` is a machine-authenticated endpoint, independent of and additive to the existing 64 KiB heartbeat/report payload. Older agents continue the heartbeat unchanged; dashboards without this endpoint receive no snapshot posts.

## Request

```http
POST /api/v1/session-snapshot HTTP/1.1
Content-Type: application/json
Authorization: <existing machine authentication>
Content-Length: <= 524288
```

The authenticated machine identity MUST resolve to the same `canonical_host` as body `host`. A body over 512 KiB (524,288 bytes before JSON parsing) is rejected without buffering beyond that limit. `Content-Encoding` is not accepted. JSON is UTF-8, exactly one object, has no duplicate keys, unknown top-level/session/process/RemoteFX keys, or trailing bytes.

### JSON shape

```json
{
  "schema": "drainctl.session-snapshot.v1",
  "host": "rdsh-07.example.test",
  "agent_instance_id": "0195a584-5b25-7a00-91a5-7cbb4dac92d9",
  "sequence": "42",
  "observed_at_ms": 1770000123456,
  "collector_version": "1.8.0",
  "logical_cpu_count": 8,
  "capabilities": {"session_actions": true, "processes": true, "input_delay": true, "remotefx": false},
  "collection_error": null,
  "sessions": [{
    "session_id": 3, "logon_at_ms": 1769990000000, "user": "alex", "domain": "CONTOSO",
    "state": "active", "station": "rdp-tcp#4", "client_name": "WS-17", "client_address": "192.0.2.17",
    "connect_at_ms": 1769990100000, "disconnect_at_ms": null, "idle_since_ms": null,
    "cpu_percent": 7.25, "working_set_bytes": "123731968", "input_delay_ms": 14, "remotefx": null,
    "processes": [{"pid": 812, "image_name": "app.exe", "cpu_percent": null, "working_set_bytes": "41943040"}]
  }]
}
```

Every displayed top-level property and every nullable session and process property is required. `sequence` and all non-null `working_set_bytes` values are canonical unsigned decimal strings (ASCII digits only, no sign, leading zero, whitespace, decimal point, or exponent; `0` is the only zero spelling). `logical_cpu_count` is required. The four properties inside `capabilities` default to `false` if absent for forward compatibility. `collection_error` is `null` or exactly `{"code":"<machine-code>"}`. `sessions` and each session's `processes` are required and may be empty. `remotefx` is required and is null or an object containing all seven nullable metrics: `fps`, `quality_percent`, `encode_time_ms`, `rtt_ms`, `loss_percent`, `server_skipped_fps`, and `network_skipped_fps`.

### Bounds and validation

| Field | Rule |
|---|---|
| `schema` | exact `drainctl.session-snapshot.v1` |
| `host` | canonical RFC 1123 lower-case ASCII host, 1–253 bytes; must match machine identity |
| `agent_instance_id` | UUIDv7 generated and durably reserved once at agent service start; UUIDv4 and every non-UUIDv7 form reject |
| `sequence` | canonical unsigned decimal string representing `0..18446744073709551615`; incremented for every snapshot-post attempt from this instance |
| `observed_at_ms` and session timestamps | integer `0..253402300799999`, or null where nullable; agent wall time for display/diagnostics only |
| `collector_version` | UTF-8 1–64 bytes, no control characters |
| `logical_cpu_count` | integer `1..1024` |
| `collection_error.code` | `wts_enumeration_failed`, `wts_metadata_failed`, or `collector_timeout` |
| `sessions` | length `0..500`; unique `session_id` |
| `session_id`, `pid` | integer `0..4294967295`; PID zero rejected |
| `state` | `active`, `connected`, `connect_query`, `shadow`, `disconnected`, `idle`, `listen`, `reset`, `down`, `init`, or `unknown` |
| `user`, `domain`, `station`, `client_name` | nullable UTF-8, no NUL/control, maximum 256 bytes |
| `client_address` | nullable UTF-8, no NUL/control, maximum 128 bytes |
| `image_name` | UTF-8 base filename only (no `/`, `\`, `:`), 1–260 bytes, no controls |
| `processes` | length `0..5`; unique PID within its session |
| `cpu_percent` | session/process: nullable finite JSON number in `[0, 100 * logical_cpu_count]`; process null means a first or inaccessible delta |
| `working_set_bytes` | canonical unsigned decimal string for `0..9223372036854775807`; server rejects values above SQLite `MaxInt64` |
| `input_delay_ms` | integer `0..600000` or null |
| RemoteFX fps | finite `(0, 240]` or null |
| RemoteFX quality/loss (%) | finite `(0, 100]` / `[0, 100]`, respectively, or null |
| RemoteFX encode/rtt (ms) | finite `[0, 60000]` or null |
| RemoteFX skipped FPS | finite `[0, 1000000]` or null |

`unknown` is a valid mapped state, not a validation failure; consumers render it after every known state.

Numbers must be finite and integral where an integer is required. Go decoders use `json.Decoder.UseNumber` and range-check before conversion; decimal-string `uint64` fields are parsed without a float intermediary. Any invalid field rejects the entire request; valid sessions from an invalid complete snapshot are never retained. A non-null `logon_at_ms` must be no later than `observed_at_ms`; the other independently nullable WTS times have no assumed mutual ordering.

A non-null `collection_error` is a fatal complete-enumeration failure: `sessions` MUST be `[]` and all capabilities MUST be false. It does not represent a missing optional metric. A normal `collection_error:null` snapshot represents a complete successful enumeration; PDH and process collection failures remain successful snapshots with affected metrics null and corresponding capability false.

### Agent collection contract

The agent enumerates WTS metadata once per snapshot and emits the complete current set it can enumerate. It never derives session metadata from RD Broker. Each named PDH session counter is matched to `session_id` exactly; prefix, substring, display-name, and enumeration-order matching are forbidden. Missing counter families null only their metrics and set the relevant capability false. CPU is normalized to percent of all reported logical CPUs; working set is current bytes; input delay is current milliseconds.

When enabled, process collection uses Toolhelp enumeration, `ProcessIdToSessionId`, limited-information handles, and creation-time CPU deltas. It closes each handle in the same collection pass and computes CPU over the collector interval. A first or inaccessible delta emits `cpu_percent:null`; failure to inspect a process otherwise omits only that process and can set `processes:false`. It emits deterministic top-N (`top_processes`) ordered by CPU descending (null last), working set descending, image name case-insensitive ascending, then PID ascending, with at most five entries.

## Acceptance, replacement, and error attempts

The agent durably reserves its UUIDv7 `agent_instance_id` at service start in its local telemetry DB before posting any snapshot. For that `canonical_host`, reservation uses parsed UUIDv7 16-byte order and advances the local `session_generation_fence.max_instance_id`; it MUST be strictly greater than the locally fenced value. The dashboard performs the same parsed-byte comparison against its durable per-host fence inside the apply transaction; UUID text locale/collation order is never used.

The dashboard then compares the validated request with the host's stored latest attempt and fence. Its first accepted UUIDv7 for a host creates the fence.

* For the same `agent_instance_id`, only a strictly greater `sequence` is accepted (`202`). Its `received_at_ms` is sampled inside the transaction.
* For a different instance ID greater than the host fence, the request starts a new generation: the transaction advances the fence and accepts its sequence, including a reset/lower sequence.
* A different instance ID less than or equal to the host fence is stale. It commits no storage change, returns `202 {"accepted":false,"reason":"stale_snapshot","received_at_ms":...}`, and publishes no SSE event. This includes a delayed old-generation snapshot after a newer generation has committed.
* `observed_at_ms` is retained for display and diagnostics but never participates in ordering or freshness.
* An accepted successful (`collection_error:null`) snapshot atomically replaces the host's `session_latest` rows, updates latest-attempt and last-success instance ID, sequence, observation/receipt values, persisted logical CPU count and aggregates, collector version, and capabilities, and clears the latest attempt error.
* Snapshot privacy and retention purges delete `session_snapshots` and cascading current rows but retain `session_generation_fence`; after such a purge, a lower/equal **different** generation remains stale and cannot reclaim the host.
* An accepted fatal (`collection_error` non-null) snapshot updates only latest-attempt instance ID, sequence, observation/receipt, and error code. It preserves all last-success fields, current rows, aggregates, capabilities, and freshness receipt. It emits a privacy-safe error-status SSE event.
* An accepted successful empty `sessions` array is the only accepted snapshot condition that clears rows; it sets last-success aggregates to zero and `last_activity_at_ms:null`.

```json
{"accepted":true,"received_at_ms":1770000123999,"session_count":1}
```

For an accepted fatal attempt, `session_count` is omitted because it has no new successful data:

```json
{"accepted":true,"received_at_ms":1770000123999,"collection_error":"wts_enumeration_failed"}
```

```json
{"accepted":false,"reason":"stale_snapshot","received_at_ms":1770000123999}
```

`received_at_ms` in a stale response is sampled after validation and is informational only. Valid accepted and stale requests always use `202`; no accepted request uses an error status.

| Status | `error` | Meaning |
|---:|---|---|
| 202 | — | valid accepted (`accepted:true`) or stale/idempotent (`accepted:false`) request |
| 400 | `invalid_snapshot` | JSON/schema/value/identity validation failure |
| 401 | `unauthorized_machine` | absent or invalid machine credentials |
| 403 | `host_identity_mismatch` | authenticated host differs from body host |
| 413 | `snapshot_too_large` | body exceeds 512 KiB |
| 415 | `unsupported_media_type` | content type is not `application/json` |
| 429 | `rate_limited` | existing machine limiter denies request |
| 503 | `storage_error` | no transaction committed |

Error bodies are `{"error":"<code>"}` and contain no submitted PII. Publishing a privacy-safe snapshot or error-status SSE event follows commit and cannot roll back storage.

## Report response command delivery

A normal authenticated agent report response may include `pending_session_actions`; it is absent or empty when no unexpired active command exists:

```json
{"pending_session_actions":[{"action_id":"0195a584-5b25-7a00-91a5-7cbb4dac92d9","type":"message","session_id":3,"expected_logon_at_ms":1769990000000,"expires_at_ms":1770000423999,"message":"Please save your work."}]}
```

The array contains at most 20 unexpired actions for the authenticated canonical host, ordered by `created_at_ms` ascending. Both `queued` and already `delivered` actions remain eligible for same-ID redelivery until a terminal transition or expiry. Before response, selected queued rows become delivered; selected delivered rows remain delivered. The agent first durably inserts an action ID as `claimed` in `session_action_ledger`, then immediately checks expiry, re-enumerates the target, exactly compares `logon_at_ms`, and only then calls WTS. On a restart or redelivery of a claimed ID it never re-executes WTS and reports a safe `failed` or `duplicate` outcome. It records terminal outcome durably before reporting it.

The next normal report adds `completed_session_actions`, at most 20 objects with exactly `action_id` and `outcome`. `outcome` is `completed`, `failed`, `expired`, `session_changed`, `unsupported`, or `duplicate`. It contains no username, client, process, message, or raw error. `duplicate` acknowledges a locally completed known ID and does not alter an already terminal server state. A dashboard acknowledgement permits ledger cleanup; otherwise cleanup waits until expiry plus retention.
