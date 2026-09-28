# Dashboard Investigation REST and SSE Contract

**Feature**: [013-ai-anomaly-investigation](../spec.md)
**Applies to**: the central Windows dashboard/service only.

This contract is additive. It does not change `GET`/`PUT /api/v1/settings`, machine `GET /api/v1/config`, any agent route, `SpikePayload`, existing evtspike REST/SSE payloads, notifications, CLI, PowerShell, or installer behavior.

No machine-account handler registers these routes. The machine `RemoteSettings`/`GET /api/v1/config` projection and shared dashboard `GET`/`PUT /api/v1/settings` projection contain no investigation provider, acknowledgement, credential, status, attempt, evidence, report, provenance, or action fields. Machine requests cannot obtain or initiate provider work.

## Common rules

All routes require the existing `drainctl_session` cookie and existing dashboard authorization middleware; they are not SSPI machine routes. Authorization atomically reads the current configured dashboard group and requires that the existing session's login-time stored group snapshot includes it; it never performs a live directory-membership lookup. Authorization occurs before source/predecessor lookup, evidence assembly, attempt creation, settings read/write, or provider work. Missing/expired or invalidated sessions return `401 {"error":{"code":"session_expired"}}`; a session whose stored snapshot lacks the current configured group returns `403 {"error":{"code":"access_denied"}}`. Neither response discloses data or creates an attempt/provider call. External membership changes take effect only when the user logs out, the session expires, or the user reauthenticates.

Requests and successful JSON responses use `application/json; charset=utf-8`. Undefined object members are rejected. JSON bodies are at most 16 KiB. A `PUT` or `POST` requires `Content-Type: application/json`; otherwise return `415` / `invalid_content_type`. Unsupported query parameters, malformed path IDs, malformed JSON, wrong types, unknown members, invalid enums, out-of-range values, and oversized bodies return `400` / `invalid_request` before state changes. Known routes with an unsupported method return `405` / `method_not_allowed` and `Allow`. The existing dashboard limiter may return `429` / `rate_limited` before handler work.

Every error body is exactly `{"error":{"code":"<safe_code>"}}`. Safe errors never echo path parameters, credentials, provider output, provider metadata, evidence, hosts, parser, transport, or diagnostic text. Timestamps are UTC RFC 3339 with milliseconds. Durable IDs are positive integer-backed decimal strings matching `^[1-9][0-9]{0,18}$`; numeric JSON IDs are never accepted or emitted.

There are two independent limits. Globally, at most 100 attempts may be `queued` or `running` across every source; the claim is transactional before any evidence or attempt insert for manual, retry, and automatic roots. Per source, at most 100 attempts are retained, including roots, retries, and local pre-send insufficient-evidence rows. History is paginated even though a complete retained source lineage is therefore bounded to 100 attempts; an attempt has at most 9 omission codes; a retained evidence snapshot has at most 134 facts; metadata strings are at most 128 UTF-8 bytes except the explicitly bounded report prose below. A replacement credential is 1–4096 UTF-8 bytes and is never returned, logged, sent over SSE, retained in browser state, evidence, result, provenance, diagnostics, or attempt data.

The sole omission enum, stored and projected without translation, is: `pre_upgrade_context`, `retention_expired`, `freshness_unavailable`, `drain_unavailable`, `detector_unavailable`, `local_points`, `fleet_points`, `local_aggregate`, `fleet_aggregate`. Values are distinct and retain this order.

### Stable source and host boundary

A source link is exactly `{"source_kind":"event_spike","source_id":"42"}`. `source_kind` is `event_spike` or `session_drop`; `source_id` links to the durable deterministic row. Investigation evidence, results, attempts, provenance, failures, and diagnostics contain only this link—not canonical host/FQDN/domain/IP, customer ID, user identity, session identity, `changed_by`, arbitrary or nonfixed URL/path, raw/source free text, raw response, diagnostics, or secret. A fully validated, bounded report `text` field is the sole free-text exception; it remains untrusted and is never a source identifier.

The append-only `investigation_privacy_acknowledgements` table alone may retain the authenticated dashboard actor for local accountability. Closed provenance alone may retain the fixed literal `https://api.openai.com/v1/responses` endpoint as a locally constructed constant. Neither exception is included in provider evidence/request data or diagnostic material; the actor is never projected, and both local records remain authorization-controlled and subject to `AuditDays` retention. No arbitrary or nonfixed URL is retained or egressed.

A separately authorized deterministic source/list/detail handler may resolve canonical registered-host identity after authorization from its `event_spikes` or `session_drop_anomalies` row. Only those authorized source/list/detail REST projections may resolve a host; they must not denormalize it into this contract, SSE, or provider artifacts.

## Settings

### `GET /api/v1/investigation/settings`

Returns the session-only safe view; it never returns plaintext or `dpapi:` credentials or unknown persisted provider fields.

```json
{
  "provider": {
    "profile": "openai_responses",
    "endpoint": "https://api.openai.com/v1/responses",
    "model": "gpt-6-astra",
    "access_enabled": false,
    "acknowledged": false,
    "privacy_acknowledgement_version": "",
    "automatic_enabled": false,
    "has_credential": false
  },
  "session_drop": {
    "lower_tail_threshold": 0.0001,
    "minimum_drop_sessions": 3,
    "minimum_drop_percent": 30,
    "baseline_half_life_hours": 168,
    "cooldown_minutes": 60,
    "detector": {"slots_per_day":96,"confirmation_required":2,"confirmation_window":3,"slot_maturity_eligible_days":7,"fallback_minimum_observations":20,"fallback_minimum_span_hours":24}
  }
}
```

`profile`, `endpoint`, and `model` are fixed informational constants, not writable configuration. `acknowledged` is derived only when the persisted `privacy_acknowledgement_version` equals `openai_responses_privacy_v1` **and** the persisted `privacy_acknowledgement_audit_id` references a matching immutable acknowledgement audit row; it is false otherwise. The audit ID itself is not projected. Migration from the prior provider clears both fields. `automatic_enabled` may be true only when access is enabled, the current acknowledgement is present, and a credential exists. Detector metadata is read-only; only the five sibling session-drop fields are writable and apply only to central lower-tail detection.

### `PUT /api/v1/investigation/settings`

Atomically validates and persists this closed full safe replacement through the existing scoped JSON/DPAPI path. This API accepts no unknown provider, acknowledgement, credential-command, or session-drop member and never returns plaintext or `dpapi:` credentials.

```json
{
  "provider": {
    "access_enabled": true,
    "privacy_acknowledgement": {
      "version": "openai_responses_privacy_v1",
      "third_party_subprocessors": true,
      "no_training_without_opt_in": true,
      "default_abuse_monitoring_up_to_30_days": true,
      "store_false_application_state_only": true,
      "temporary_prompt_cache_possible": true,
      "zdr_mam_separate_approval": true,
      "audit_days_local_only": true,
      "global_endpoint_no_regional_guarantee": true
    },
    "automatic_enabled": false,
    "credential": {"operation":"preserve"}
  },
  "session_drop": {"lower_tail_threshold":0.0001,"minimum_drop_sessions":3,"minimum_drop_percent":30,"baseline_half_life_hours":168,"cooldown_minutes":60}
}
```

`privacy_acknowledgement` is required when enabling access or automatic roots and is otherwise either omitted or the exact closed object above: it has `version:"openai_responses_privacy_v1"` and exactly the eight shown Boolean members, each `true`. Missing, unknown, non-Boolean, or `false` acknowledgement members—and every other version—reject enablement with `422` / `invalid_settings`. Before changing configuration, the server appends an immutable row to append-only SQLite `investigation_privacy_acknowledgements`, containing a positive `id`, the fixed current version, authenticated dashboard actor, `accepted_at_ms`, and the fixed `clause_set_hash` for that exact complete clause set. In the same configuration write, it persists both the current version and `privacy_acknowledgement_audit_id` referencing that row. If the configuration write fails, an unreferenced audit row may remain harmlessly; access fails closed because acknowledgement is derived only from an exact current-version row referenced by the configuration. The server never persists or returns the eight Boolean clauses, the audit ID, or the actor. `GET` exposes only the safe `privacy_acknowledgement_version` and derived `acknowledged` fields. Migration from the prior provider clears both the accepted version and audit ID.

Every PUT includes exactly one credential command: `{"operation":"preserve"}`, `{"operation":"replace","value":"non-empty write-only credential"}`, or `{"operation":"clear"}`. `value` is required only for `replace`; it is forbidden otherwise. `preserve` retains the protected value even when absent; `replace` uses only the existing DPAPI flow; `clear` removes it. Omitting the command is invalid.

`access_enabled:true` and `automatic_enabled:true` each require the current acknowledgement and a credential after applying the command. Clearing while either is enabled is invalid. Disabling access changes unsent queued attempts and running attempts before their first send to terminal `failed` / `configuration_disabled`; terminal history remains readable. Disabling automatic roots does not cancel an already queued explicit attempt.

| Field | Valid range |
|---|---|
| `lower_tail_threshold` | finite `0.000000001..0.1` |
| `minimum_drop_sessions` | integer `1..1000000` |
| `minimum_drop_percent` | finite `1..99` |
| `baseline_half_life_hours` | integer `24..8760` |
| `cooldown_minutes` | integer `1..1440` |

Success is `200` with the GET view. Invalid/incompatible settings, including an invalid acknowledgement, return `422` / `invalid_settings`; append or atomic configuration persistence failure returns `503` / `settings_unavailable`. Credential validity is observable only through safe operational state after an attempt.

## Operational status

### `GET /api/v1/investigation/status`

Returns central safe state and never queries OpenAI or exposes provider output.

```json
{"operational_state":"ready","provider":{"profile":"openai_responses","endpoint":"https://api.openai.com/v1/responses","model":"gpt-6-astra","access_enabled":true,"acknowledged":true,"privacy_acknowledgement_version":"openai_responses_privacy_v1","automatic_enabled":false,"has_credential":true},"attempt_counts":{"queued":0,"running":0,"completed":12,"insufficient_evidence":3,"failed":2},"worker":{"workers":1,"max_nonterminal_attempts":100,"requests_per_minute":10,"burst":2,"request_timeout_seconds":30},"latest_failure":null}
```

`operational_state` is `disabled`, `configured`, `ready`, `automatic_enabled`, `degraded`, or `failing`. Attempt counts are retained-only non-negative safe integers, with `queued + running <= 100`. `latest_failure` is null or `{"reason":"timeout","at":"2026-09-26T16:31:20.000Z"}`. Its reason is exactly one of `authentication_failed`, `configuration_disabled`, `configuration_invalid`, `evidence_unavailable`, `interrupted`, `network_error`, `provider_rate_limited`, `provider_request_rejected`, `provider_refused`, `redirect_refused`, `request_limit`, `response_incomplete`, `response_invalid`, `response_limit`, `storage_unavailable`, `timeout`, or `upstream_error`. Status read failure returns `503` / `status_unavailable`.

## Source history and root request

### `GET /api/v1/investigation/sources/{source_kind}/{source_id}/attempts`

Returns immutable retained attempts for an existing durable source without building evidence/provider work. `limit` is optional `1..100` (default 100); `after_attempt_number` is an optional positive decimal. The page is ascending by `attempt_number`, strictly after that value, with `next_after_attempt_number` equal to the final returned number only when another page exists. Retention can make a page empty or leave gaps in attempt numbers; while retained, a source has no more than 100 attempts, so paging remains required by the stable API but its complete retained lineage is bounded.

```json
{"source":{"source_kind":"event_spike","source_id":"42"},"attempts":[{"attempt_id":"91","attempt_number":1,"initiation":"manual","retry_of_attempt_id":null,"state":"completed","created_at":"2026-09-26T16:31:20.000Z","started_at":"2026-09-26T16:31:21.000Z","send_authorized_at":"2026-09-26T16:31:21.100Z","send_completed_at":"2026-09-26T16:31:22.000Z","completed_at":"2026-09-26T16:31:22.000Z","terminal_reason":"","evidence_version":1,"omission_codes":[]}],"next_after_attempt_number":null}
```

`initiation` is `automatic`, `manual`, or `retry`; state is `queued`, `running`, `completed`, `insufficient_evidence`, or `failed`. `started_at` is null only for queued and means worker or cancellation processing began, not provider egress. `send_authorized_at` and `send_completed_at` are nullable independently of state: a `running` attempt may have neither timestamp. `send_authorized_at` is committed immediately before `Client.Do`; once non-null it means a transmission MAY have begun, never proves provider receipt, and forbids automatic resend after uncertainty or restart. `send_completed_at` is set only after the local HTTP exchange returns; it does not prove provider receipt. Local insufficiency, configuration cancellation, and request-limit terminalization have both timestamps null. `completed_at` is null only for queued/running and non-null for every terminal state. `terminal_reason` is…

### `POST /api/v1/investigation/sources/{source_kind}/{source_id}/attempts`

Creates an explicit eligible manual root. Body is exactly `{}` and accepts no provider question, endpoint, evidence, header, or credential. After authorization, deterministic source/eligibility checks and one transaction claim the root, enforce both the global 100 queued+running cap and fixed 100-attempt retained cap for that source before evidence or attempt insertion, and persist bounded host-free evidence before return. The row is `queued` or terminal `insufficient_evidence`; no provider transmission precedes that write. A queued/running duplicate returns `200` with that summary and no new work.

| Status | Error | Condition |
|---|---|---|
| 404 | `source_not_found` | No durable deterministic source. |
| 409 | `source_ineligible` | Not confirmed evtspike or unexplained confirmed session drop. |
| 409 | `provider_not_ready` | Not enabled, currently acknowledged, and credentialed. |
| 409 | `source_completed` | A completed attempt exists. |
| 409 | `retry_required` | Failed/insufficient terminal attempt exists. |
| 409 | `attempt_limit_reached` | The source already retains 100 attempts; no root, retry, or local insufficient-evidence row is created. |
| 429 | `queue_full` | 100 queued/running attempts. |
| 503 | `attempt_store_unavailable` | Required transaction failed; zero egress. |

A new insufficient-evidence root returns `201` and later requires its linked retry route.

## Attempt detail and retry

### `GET /api/v1/investigation/attempts/{attempt_id}`

Returns immutable retained safe detail. A retained source’s complete detail lineage is bounded to 100 attempts; this endpoint returns one retained member of that lineage. Once retention deletes an attempt, its detail is unavailable even if a later retry’s opaque predecessor link remains.

```json
{
  "attempt": {
    "attempt_id":"91","attempt_number":1,"source":{"source_kind":"event_spike","source_id":"42"},"initiation":"manual","retry_of_attempt_id":null,"state":"completed","created_at":"2026-09-26T16:31:20.000Z","started_at":"2026-09-26T16:31:21.000Z","send_authorized_at":"2026-09-26T16:31:21.100Z","send_completed_at":"2026-09-26T16:31:22.000Z","completed_at":"2026-09-26T16:31:22.000Z","terminal_reason":"",
    "evidence":{"version":1,"snapshot_kind":"available","snapshot_at":"2026-09-26T16:31:20.000Z","window_start":"2026-09-26T16:01:20.000Z","window_end":"2026-09-26T16:31:20.000Z","fact_ids":["F001","F002","F003"],"omission_codes":[]},
    "result":{"result_version":1,"summary":{"text_kind":"untrusted_summary","text":"The observed anomaly needs operator review.","fact_ids":["F001","F002"]},"overall_assessment":"indeterminate","evidence_sufficiency":"partial","human_review_required":true,"hypotheses":[{"rank":1,"confidence":"medium","text_kind":"untrusted_hypothesis","text":"A localized operational condition may be consistent with the retained metrics.","supporting_fact_ids":["F002"],"contradicting_fact_ids":[]}],"missing_evidence":[{"category":"host_health_detail","text_kind":"untrusted_missing_evidence","text":"Retained host health detail is unavailable.","related_fact_ids":["F003"]}],"recommended_diagnostic_checks":[{"rank":1,"check_type":"inspect_retained_metrics","text_kind":"untrusted…
    "provenance":{"provider_profile":"openai_responses","provider_endpoint":"https://api.openai.com/v1/responses","requested_model":"gpt-6-astra","response_format":"anomaly_investigation_v1","store":false,"send_authorized_at":"2026-09-26T16:31:21.100Z","send_completed_at":"2026-09-26T16:31:22.000Z","request_header_bytes":234,"request_body_bytes":12000,"response_header_bytes":167,"response_body_bytes":5000,"validation_outcome":"accepted"}
  }
}
```

`evidence.snapshot_kind` is `available` or `unavailable`. An available snapshot has strict `EvidenceV1` and its contiguous retained `F001`–`F134` IDs; its projection exposes only version, kind, timestamps/window, IDs, and omission codes, never canonical evidence bytes or contents. An unavailable local pre-send snapshot is explicitly host-free: its persisted `canonical_json` is exactly `{}`, its persisted `evidence_hash` is the SHA-256 of those two UTF-8 bytes, and it has no fact rows, so its detail projection has `snapshot_kind:"unavailable"` and `fact_ids:[]`. The unavailable snapshot is not `EvidenceV1`, is never sent, and does not fabricate placeholder facts.

`result` is either null or exactly `anomaly_investigation_v1`: all fields shown are required; no unknown members exist. `overall_assessment` is `insufficient_evidence`, `indeterminate`, `likely_localized_operational_issue`, `likely_fleet_wide_operational_issue`, or `likely_expected_or_maintenance_related`; `evidence_sufficiency` is `insufficient`, `partial`, or `sufficient`. Hypotheses are 0–5 with sequential ranks, `low|medium|high` confidence, 1–12 distinct supporting IDs, and distinct/disjoint supporting/contradicting ID arrays of at most 12. Missing-evidence items are 0–6 with category `additional_time_series`, `host_health_detail`, `service_sta…

All `text` is provider-controlled **untrusted plain text**. The developer prompt requests read-only diagnostic checks and `check_type` constrains a check's intent, but neither semantic classification nor validation can prove prose free of commands or remediation. The safety guarantee is that the product never executes, routes, or treats it as an instruction. Text is trimmed printable ASCII only (`U+0020`–`U+007E`), uses ordinary spaces, and rejects CR, LF, tab, `/`, `\`, `<`, `>`, `@`, backtick, IPv4/IPv6 literals, and case-insensitive ASCII-normalized substring containment of every sensitive evidence/canary value; it does not claim to detect invented hostnames or domains absent from that evidence/canary set. Summary is 1–1280 bytes; hypothesis/check text 1–…

If sufficiency is `insufficient`, assessment is `insufficient_evidence`, human review is true, hypotheses are empty, and missing evidence is nonempty; state is `insufficient_evidence`. Partial/sufficient reports have a non-insufficient assessment and 1–5 hypotheses; state is `completed`. A local pre-send insufficient row has the explicit unavailable snapshot, `terminal_reason:"evidence_unavailable"`, `result:null`, and `provenance:null`. A provider-valid insufficient report has an available snapshot, `state:"insufficient_evidence"`, `terminal_reason:""`, and its validated result and provenance. Failed rows, including rejection/refusal/incomplete/invalid response, have null result/provenance and a non-empty safe terminal reason.

Provenance exists only for a fully validated report and is a REST projection of the closed local provider provenance: it has exactly the shown fields, with durable `send_authorized_at_ms` and `send_completed_at_ms` rendered as RFC 3339 `send_authorized_at` and `send_completed_at`; it has no `request_started*` or `request_completed*` aliases. Byte counts are local measurements; `response_body_bytes` is the bounded decompressed JSON response-body byte count; outcome is `accepted` or `insufficient_evidence`. It never contains a response ID, returned model, usage, reasoning, headers, system fingerprint, raw body, refusal, error text, arbitrary URL, or identity. The fixed endpoint is the sole provenance URL and is the locally constructed constant described above. Missing/expired attempt is `404` / `attempt_not_found`; detail read failure is `503` / `attempt_detail_unavailable`.

### `POST /api/v1/investigation/attempts/{attempt_id}/retry`

Creates one explicit linked retry from a failed or insufficient attempt. Body is exactly `{}`. Authorization precedes predecessor lookup; after predecessor lookup, deterministic source lookup precedes configuration, fresh evidence, attempt creation, and provider work. In the same transaction that creates its fresh snapshot, the retry enforces the global 100 queued+running cap before evidence or attempt insertion and the fixed 100-attempt retained cap for its source. The transaction returns `201` standard summary; child initiation is `retry`, number is one greater than the latest source attempt, and `retry_of_attempt_id` is the path ID. It creates a fresh snapshot and never mutates/reuses predecessor evidence. The opaque link remains if its predecessor expires.

| Status | Error | Condition |
|---|---|---|
| 404 | `attempt_not_found` | Predecessor absent/expired. |
| 404 | `source_not_found` | Retryable predecessor source absent/expired, before snapshot/work. |
| 409 | `retry_not_allowed` | Predecessor queued, running, or completed. |
| 409 | `provider_not_ready` | Provider not enabled, acknowledged, credentialed. |
| 409 | `attempt_limit_reached` | The retry source already retains 100 attempts; no retry or local insufficient-evidence row is created. |
| 429 | `queue_full` | 100 queued/running attempts. |
| 503 | `attempt_store_unavailable` | Retry/evidence transaction failed; zero egress. |

Required retained context absence returns an immutable `201` insufficient-evidence retry without egress. No automatic retry, HTTP retry, retry header, retry of running work, or restart resend exists; recovery never reconstructs or retransmits a provider request.

## SSE: `investigation_update`

The authenticated `GET /api/v1/events` transport retains its existing keepalive/reconnect behavior. Before opening a shared subscription, authorization atomically reads the current configured dashboard group and requires that the session exists and its login-time stored group snapshot includes that group; it does not perform a live directory-membership lookup. The broker periodically repeats that session-existence and snapshot-versus-current-group check for every open stream and closes a stream on failure before emitting any further investigation event. Changing `Dashboard.Group` invalidates all sessions and closes all streams, so every user must reauthenticate. A missing, expired, invalidated, or stale session receives `401 {"error":{"code":"session_expired"}}` before subscription; a session whose stored snapshot lacks the current configured group receives `403 {"error":{"code":"access_denied"}}`; an open stream is closed on either condition and receives zero further investigation or session-drop events. External membership changes take effect only on logout, session expiry, or reauthentication. Existing currently authorized event consumers and their payloads remain compatible. Existing browser-event fanout filters investigation updates by the authenticated session rather than broadcaster-global state.

```text
data: {"type":"investigation_update","data":{"kind":"attempt","attempt":{"attempt_id":"91","source":{"source_kind":"event_spike","source_id":"42"},"attempt_number":1,"initiation":"manual","retry_of_attempt_id":null,"state":"queued","terminal_reason":"","updated_at":"2026-09-26T16:31:20.000Z"}},"timestamp":"2026-09-26T16:31:20.000Z"}
```

`data.kind` is `attempt` or `status`. Attempt payloads have exactly the shown members: no evidence, fact IDs/content, report, untrusted prose, provenance, diagnostic data, host, credential, or provider output. `terminal_reason` distinguishes local pre-send insufficiency (`evidence_unavailable`, no report/provenance) from provider-valid insufficiency (empty string, report/provenance available through REST). A status payload is exactly `{"kind":"status","status":{"operational_state":"degraded","attempt_counts":{"queued":0,"running":0,"completed":12,"insufficient_evidence":3,"failed":3},"latest_failure":{"reason":"provider_refused","at":"2026-09-26T16:31:22.000Z"}}}` and uses status enums without provider configuration/credential fields. Events are persisted-state invalidation signals only: clients must refresh REST detail/status, events are not audit records, may be coalesced, and older clients ignore the type.

## Lifecycle, egress, retention, compatibility

1. State only progresses `queued → running → completed|insufficient_evidence|failed`; terminal rows and evidence/result/provenance/failure are immutable. SQLite uses `created_at_ms`, `started_at_ms`, `send_authorized_at_ms`, `send_completed_at_ms`, and `completed_at_ms`; every REST projection uses the corresponding RFC 3339 `created_at`, `started_at`, `send_authorized_at`, `send_completed_at`, and `completed_at` names. `started_at_ms` is set only when worker or cancellation processing starts; it is not an egress timestamp. The worker commits `send_authorized_at_ms` immediately before `Client.Do`; it means a transmission MAY have begun, never proves provider receipt, and permits no automatic resend after uncertainty or restart. `send_completed_at_ms` means only that the local HTTP exchange returned, not provider receipt.
2. One dashboard worker permits a global maximum of 100 nonterminal attempts, 10 sends/minute burst 2, and a 30-second complete request timeout. Manual, retry, and automatic roots transactionally check the global queued+running cap before evidence/attempt insertion; interactive manual/retry roots receive `429` / `queue_full`, while automatic roots create zero work. Independently, a source retaining 100 attempts rejects manual/retry work with `409` / `attempt_limit_reached`.
3. A worker claims only in one transaction that changes a durable queued row to running when `created_at_ms >= AuditDays cutoff` and its persisted evidence snapshot and evidence hash are both present and valid; otherwise it terminalizes or leaves it for retention without egress. `running` may have neither send timestamp. Local insufficiency, configuration cancellation, and request-limit terminalization never set a send timestamp. Immediately before the sole provider request, the worker durably sets `send_authorized_at_ms` and `send_lease_expires_at_ms = now + 30 seconds`; after `Client.Do` returns, it durably sets `send_completed_at_ms`. It never reconstructs or resends a request.
4. Retention never deletes a live running attempt or its linked evidence while either bounded lease is active: the 30-second send lease or the two-minute finalization lease. If a provider response has been received but final SQLite terminalization cannot be written, the attempt receives `finalization_lease_expires_at_ms = now + 2 minutes`. Until that fixed deadline, the worker remains assigned and one bounded in-memory already-validated terminal candidate may be retried to SQLite without resend. At the deadline, the candidate is discarded. As soon as SQLite is writable, the worker atomically finalizes the row as `failed` / `storage_unavailable`, releases the worker, and emits the durable update. On startup and periodic retention recovery, an active applicable send or finalization lease preserves the running row. Only after no applicable lease remains active does recovery atomically terminalize a row with `send_authorized_at_ms IS NULL` as `failed` / `interrupted`, or a row with non-null `send_authorized_at_ms` and no durable terminal state as `failed` / `storage_unavailable`. Recovery reconstructs neither result/provenance nor a provider request, never resends, and never retries SQLite finalization outside the original process. Once terminalized, normal `AuditDays` deletion may proceed. No running row is retained indefinitely.
5. The sole transport is direct `POST https://api.openai.com/v1/responses`, `Authorization: Bearer <credential>`, fixed `gpt-6-astra`, `store:false`, `background:false`, `stream:false`, `truncation:"disabled"`, `reasoning.effort:"high"`, `max_output_tokens:8192`, `tools:[]`, `tool_choice:"none"`, `parallel_tool_calls:false`, and strict `text.format` schema `anomaly_investigation_v1`. No conversation, previous response, polling, streaming, tool/stateful API, fallback, redirect, or automatic resend exists.
6. Evidence is persisted before egress, host-free, field-allowlisted, source-window bounded, <=8,000 UTF-8 bytes; the full compact request is <=16,384 bytes. It assigns visible units contiguous F001–F134 in order window/source/context, local points, fleet points, omissions. Request/response headers are <=16,384 bytes; decompressed 2xx response <=32,768 bytes; non-2xx content is discarded at <=4,096 bytes. Raw OpenAI material is never retained.
7. Safe terminal reasons include `provider_request_rejected` for 400/404/409/422 documented request/policy/data-residency rejection, `provider_refused` for a completed refusal, and `response_incomplete` for any incomplete response. Invalid strict output, tools/output irregularity, semantic/fact/prose validation failure, or unexpected foreground status is `response_invalid`. All three retain no partial report or provenance and cause no action.
8. `AuditDays` expires each attempt independently by `created_at_ms`; linked evidence/result/provenance/failure expire with it. Retention deletes expired queued and terminal attempts with their linked data only after applying running-row recovery and respecting both active leases above. It may delete an old unreferenced `investigation_privacy_acknowledgements` row after `AuditDays`, but never the row referenced by the current configuration. Session-drop source rows are independently removed in chunks by `detected_at`; baseline state remains until permanent host removal. Rollback may ignore additive v4 records; if an older binary rewrites configuration and drops investigation fields, re-upgrade clears or invalidates the acknowledgement audit ID/version and defaults access and acknowledgement disabled.
