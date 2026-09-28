# Contract: Dashboard SSE — Session Snapshot Event

This feature adds one metadata-only event type to the existing authenticated `GET /api/v1/events` stream. Existing `server_update` and `settings_update` payloads and delivery behavior do not change. The current broker broadcasts this safe event to every authenticated stream subscriber; it does not implement role-aware delivery. Non-admin clients MUST ignore `session_snapshot` and remain unable to call the admin-only Sessions APIs.

```http
GET /api/v1/events
Accept: text/event-stream
Cookie: drainctl_session=...
```

The existing stream authentication/rate-limit response behavior remains (`401` or `429` before stream establishment). The event has `Content-Type: text/event-stream`, `Cache-Control: no-cache`, and standard SSE framing. It is a best-effort invalidation notification, not a retained event log: reconnecting clients must refetch their currently visible list/detail state and cannot rely on `Last-Event-ID` replay.

## Event framing and payload

After a newer collection attempt transaction commits, the dashboard emits exactly one event for that host. Ordering is enforced during ingestion by `agent_instance_id` and `sequence`, not timestamps: a sequence strictly newer than the stored latest attempt is accepted only for the same instance; an attempt from a different instance is a new generation because the previous process is terminated or cancelled before restart reporting. Equal or older sequences for the same instance emit none. A successful complete attempt replaces current rows; a fatal collection-error attempt records its newer attempt/error status but preserves the last successful rows and their freshness. Emission occurs after commit; a disconnected or slow subscriber does not delay snapshot ingestion.

```text
event: session_snapshot
data: {"host":"rdsh-07.example.test","latest_attempt_instance_id":"f47ac10b-58cc-4372-a567-0e02b2c3d479","latest_attempt_sequence":"42","latest_attempt_observed_at_ms":1770000123456,"latest_attempt_received_at_ms":1770000123999,"last_success_instance_id":"f47ac10b-58cc-4372-a567-0e02b2c3d479","last_success_sequence":"42","last_success_observed_at_ms":1770000123456,"last_success_received_at_ms":1770000123999,"freshness":"fresh","session_count":12,"active_count":9,"capabilities":{"session_actions":true,"processes":true,"input_delay":true,"remotefx":false},"actions_available":false,"collection_status":"ok","last_error":null}
```


The `data` JSON object has exactly these properties:

| Property | Type | Meaning |
|---|---|---|
| `host` | canonical host string | Host whose complete successful snapshot or latest attempt status changed. |
| `latest_attempt_instance_id` | UUID string | Agent service instance that made the accepted latest collection attempt. |
| `latest_attempt_sequence` | canonical unsigned decimal string | Per-instance accepted latest attempt sequence; never a JSON number. |
| `latest_attempt_observed_at_ms` | non-negative integer | Agent observation time of the accepted latest collection attempt; diagnostic only, not ordering/freshness. |
| `latest_attempt_received_at_ms` | non-negative integer | Server receive time of the accepted latest collection attempt. |
| `last_success_instance_id` | UUID string or `null` | Agent service instance that made the most recent accepted complete successful snapshot. |
| `last_success_sequence` | canonical unsigned decimal string or `null` | Per-instance sequence of the most recent accepted complete successful snapshot. |
| `last_success_observed_at_ms` | non-negative integer or `null` | Agent observation time of the most recent accepted complete successful snapshot. |
| `last_success_received_at_ms` | non-negative integer or `null` | Server receipt time of the most recent accepted complete successful snapshot; the sole freshness source. |
| `freshness` | `fresh`, `stale`, `offline`, or `unknown` | `unknown` has no success; `offline` is host registry off or >10 heartbeats; `stale` is >3 heartbeats; otherwise fresh. |
| `session_count` | integer `0..500` | Complete array length from the last successful snapshot, or zero when none exists. |
| `active_count` | integer `0..500` | Last-success rows whose state is `active` or `connected`. |
| `capabilities` | object | Last-success agent boolean capability summary; all four keys are present. If no success exists, all values are false. |
| `actions_available` | boolean | Current config/admin/capability conjunction; always false for a latest collection error or absent successful data. |
| `collection_status` | `ok` or `error` | Latest-attempt collection status, independent of freshness and retained successful data. |
| `last_error` | `null` or `{"code":<closed collection code>,"at_ms":<integer>}` | Latest-attempt bounded fatal collection-status summary; never raw diagnostics. |

There is no per-session payload. In particular, the event MUST NOT contain username, domain, station, client name/address, PID, image name, process metric, session `logon_at_ms`, action ID, message title/text, or raw collector error. A client that currently has `/sessions/{host}` expanded treats this event as an invalidation and calls `GET /api/v1/sessions/{host}`; otherwise it refetches/bounds its existing list query when needed. A fatal-error event must not cause the client to clear retained last-good detail merely because its latest attempt is an error.

## Related state changes

No synthetic `session_snapshot` event is sent merely because time crosses the stale threshold, because server time alone can change freshness without a new collection attempt. The frontend derives current stale rendering from `last_success_received_at_ms` and its server-time offset, and refetches the list after reconnect. Settings changes continue to use the existing `settings_update`; a session privacy-policy change immediately deletes `session_snapshots` and their cascading `session_latest` rows, and safely cancels or expires active actions. On that settings update the frontend discards cached session details; a later looser policy cannot reconstruct purged data.

If an accepted newer successful empty snapshot clears a host, an event is still sent with `session_count:0`, `active_count:0`, and the same safe host/summary shape. A fatal collection error is not a clear: it emits status while retaining the last-success counts and detail. If retention deletes a stale host row, it emits no event; list/detail refetch provides the authoritative absence.

## Client handling requirements

1. Register the event listener only when `/api/v1/me.is_admin` is true. Non-admin subscribers ignore `session_snapshot`; they cannot retrieve session data because Sessions routes remain admin-only. On an admin-to-non-admin authorization change or any `403`, clear all session data from client memory and do not retry session routes.
2. Parse only the schema above; ignore unknown future properties. Treat malformed data, an unknown host, an older/equal sequence from the locally known same `latest_attempt_instance_id`, or an event from a different instance as a refetch hint, never as a row mutation.
3. Do not render PII from SSE or attempt to combine it with prior detail payload. Perform the HTTP detail refetch, which applies the current privacy projection.
4. Preserve existing EventSource reconnect behavior. On open/reopen, refetch the current list query and expanded host detail; no SSE replay is required.

This keeps the event stream backward compatible for existing consumers while preventing PII/process payload amplification across long-lived browser connections.

## `session_action` status event

After each committed action state transition, the broker emits one privacy-safe status event to authenticated streams:

```text
event: session_action
data: {"action_id":"0195a584-5b25-7a00-91a5-7cbb4dac92d9","state":"completed","completed_at_ms":1770000124999,"result_code":"completed"}
```

The object contains exactly `action_id`, `state`, `completed_at_ms` (null while queued/delivered), and `result_code` (null while queued/delivered). It contains no host, session ID, identity, client, process, message, ciphertext, audit detail, or diagnostics. The server emits it after enqueue, delivery, expiry, privacy cancellation, and accepted terminal completion. Invalid completions are not action state transitions and do not emit it.

For every locally pending action, the frontend stores its action ID in `sessionStorage`, starts `GET /api/v1/session-actions/{action_id}` polling every two seconds, and listens to this event as a prompt refetch hint. It removes the stored ID and aborts polling when the status becomes terminal or on navigation; after reload it restores status polling from `sessionStorage`. A terminal status maps to the corresponding toast. SSE is advisory: malformed events, reconnects, or missed events do not change state without the polling response.
