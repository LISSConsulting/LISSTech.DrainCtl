# Dashboard Investigation REST and SSE Contract

**Feature**: [013-ai-anomaly-investigation](../spec.md)
**Companion decisions**: [implementation plan](../plan.md), [research R1–R10](../research.md)
**Applies to**: the central Windows dashboard/service only.

This contract is additive. It does not change `GET`/`PUT /api/v1/settings`, machine `GET /api/v1/config`, any agent route, `SpikePayload`, or existing evtspike REST/SSE payloads.

No machine-account handler registers these routes. In particular, the `RemoteSettings`/machine `GET /api/v1/config` projection and the shared dashboard `GET`/`PUT /api/v1/settings` projection contain no investigation provider, credential, status, attempt, or action field; a machine request cannot use this contract to obtain or initiate provider work.

## Common rules

All routes in this document require the existing dashboard-group `drainctl_session` cookie and the existing session authorization middleware. They are not SSPI machine routes. Authorization is performed before a source lookup, evidence assembly, attempt creation, configuration read/write, or provider work.

A missing or expired session returns `401` with `{"error":{"code":"session_expired"}}`. A valid session that is no longer dashboard-group authorized returns `403` with `{"error":{"code":"access_denied"}}`. These responses disclose no investigation/configuration/source data and cause no attempt or provider call.

Requests and successful JSON responses use `application/json; charset=utf-8`. Object members not defined by this contract are rejected. JSON bodies are limited to 16 KiB. All timestamps are UTC RFC 3339 strings with millisecond precision, for example `"2026-09-26T16:31:20.000Z"`. Every durable ID is integer-backed and is encoded in JSON and paths only as a positive decimal string matching `^[1-9][0-9]{0,18}$`; numeric JSON IDs are never accepted or emitted.

A `PUT` or `POST` requires `Content-Type: application/json`; any other content type returns `415` / `invalid_content_type`. Query parameters are forbidden except `limit` and `after_attempt_number` on source-attempt history; an unsupported query parameter returns `400` / `invalid_request`. An invalid path identifier or query value returns `400` / `invalid_request` before source or attempt lookup. A known path with an unsupported method returns `405` / `method_not_allowed` with the route's `Allow` header. The existing dashboard request limiter returns `429` / `rate_limited` before handler work; this is distinct from the attempt queue's `429` / `queue_full`.

Every error body is exactly:

```json
{"error":{"code":"<safe_code>"}}
```

`safe_code` is one of the route-specific codes below. It never echoes a path parameter, credential, provider response, evidence, host, or parser/transport text. A malformed JSON body, wrong JSON type, unknown member, invalid enum, out-of-range value, or body above 16 KiB returns `400` / `invalid_request` before any state change.

All new dashboard responses are bounded: history has at most 100 attempts; a request has one source; an attempt has at most 32 omission codes; no string field except the write-only credential may exceed 128 UTF-8 bytes. The credential replacement is limited to 1–4096 UTF-8 bytes and is never copied to a response, SSE message, log, attempt, evidence, result, provenance, diagnostic, or browser-durable state.

The sole omission-code enum, used unchanged by stored evidence and every dashboard history or detail projection, is `pre_upgrade_context`, `retention_expired`, `freshness_unavailable`, `drain_unavailable`, `detector_unavailable`, `local_points`, `fleet_points`, `local_aggregate`, or `fleet_aggregate`. `omission_codes` contains distinct values from this enum only.

### Stable source link and host boundary

A source link is exactly:

```json
{"source_kind":"event_spike","source_id":"42"}
```

`source_kind` is `event_spike` or `session_drop`; `source_id` is the linked durable deterministic row ID. Investigation attempts, their REST history/detail payloads, status payloads, `investigation_update` messages, evidence, results, provenance, failures, and browser-local durable state contain only this link—not a `host`, canonical host, FQDN, domain, IP address, customer identifier, username, session identifier, `changed_by`, URL, path, prose, raw response, diagnostic text, or secret.

A separately authorized deterministic source/list/detail handler may resolve the canonical registered host **after** session authorization by reading the linked `event_spikes` or `session_drop_anomalies` row. It may render that host only in that deterministic-source projection. It must not denormalize the result into an attempt or attach it to a response defined below. Resolving a source never relaxes the provider/evidence boundary.

## Settings

### `GET /api/v1/investigation/settings`

Returns the safe, session-only settings view. It does not return a plaintext credential, a `dpapi:` value, or any unknown persisted provider field.

```json
{
  "provider": {
    "profile": "typesafe_jev",
    "endpoint": "https://api.typesafe.ai/v1/systemone",
    "access_enabled": false,
    "acknowledged": false,
    "automatic_enabled": false,
    "has_credential": false
  },
  "session_drop": {
    "lower_tail_threshold": 0.0001,
    "minimum_drop_sessions": 3,
    "minimum_drop_percent": 30,
    "baseline_half_life_hours": 168,
    "cooldown_minutes": 60,
    "detector": {
      "slots_per_day": 96,
      "confirmation_required": 2,
      "confirmation_window": 3,
      "slot_maturity_eligible_days": 7,
      "fallback_minimum_observations": 20,
      "fallback_minimum_span_hours": 24
    }
  }
}
```

The fixed `profile` and `endpoint` are informational constants, not writable configuration. `automatic_enabled` may be true only when acknowledged provider access is enabled and a credential is present. The `session_drop.detector` members are read-only fixed detector metadata; only the five sibling session-drop fields are writable. The session-drop settings apply only to the central lower-tail detector; they neither appear in shared settings nor cause a provider request by themselves.

### `PUT /api/v1/investigation/settings`

Atomically validates and persists a full safe settings replacement through the existing scoped JSON/DPAPI update path. Existing unknown provider fields round-trip where the configuration subsystem supports them; this API neither returns nor accepts them.

```json
{
  "provider": {
    "access_enabled": true,
    "acknowledged": true,
    "automatic_enabled": false,
    "credential": {
      "operation": "preserve"
    }
  },
  "session_drop": {
    "lower_tail_threshold": 0.0001,
    "minimum_drop_sessions": 3,
    "minimum_drop_percent": 30,
    "baseline_half_life_hours": 168,
    "cooldown_minutes": 60
  }
}
```

Every settings `PUT` includes exactly one `provider.credential` command; omission is invalid. A caller that intends to retain the existing credential must send:

```json
{"operation":"preserve"}
```

The other permitted commands are:

```json
{"operation":"replace","value":"non-empty write-only credential"}
```

```json
{"operation":"clear"}
```

`value` is required only for `replace`; it is forbidden for `preserve` and `clear`. `preserve` retains the protected credential even if none exists. `replace` writes only through the existing DPAPI persistence flow. `clear` explicitly removes it. An omitted credential object is invalid, so an accidental partial update cannot clear or replace a secret.

`access_enabled:true` requires `acknowledged:true` and a credential after applying the requested credential operation. `automatic_enabled:true` requires the same conditions. Clearing the credential while either enabled boolean is true is invalid. Setting `access_enabled:false` atomically changes unsent queued attempts to terminal `failed` / `configuration_disabled` before their first send; completed and terminal history remain readable. `automatic_enabled:false` only stops future automatic roots; it does not cancel an already queued explicit attempt.

The validated ranges are:

| Field | Valid range |
|---|---|
| `lower_tail_threshold` | finite number `0.000000001..0.1` |
| `minimum_drop_sessions` | integer `1..1000000` |
| `minimum_drop_percent` | finite number `1..99` |
| `baseline_half_life_hours` | integer `24..8760` |
| `cooldown_minutes` | integer `1..1440` |

On success, return `200` and the exact safe view from `GET /api/v1/investigation/settings`. Validation or incompatible enablement returns `422` / `invalid_settings`; an atomic persistence failure returns `503` / `settings_unavailable`. Neither error modifies settings. The response never reports whether a supplied replacement credential was valid to TypeSafe; that is observable only through the safe operational status after an attempt.

## Operational status

### `GET /api/v1/investigation/status`

Returns safe central operational state; it does not query TypeSafe, make a provider request, or expose provider output.

```json
{
  "operational_state": "ready",
  "provider": {
    "profile": "typesafe_jev",
    "access_enabled": true,
    "acknowledged": true,
    "automatic_enabled": false,
    "has_credential": true
  },
  "attempt_counts": {
    "queued": 0,
    "running": 0,
    "completed": 12,
    "insufficient_evidence": 3,
    "failed": 2
  },
  "worker": {
    "workers": 1,
    "max_nonterminal_attempts": 100,
    "requests_per_minute": 10,
    "burst": 2,
    "request_timeout_seconds": 30
  },
  "latest_failure": null
}
```

`operational_state` is exactly one of:

| State | Meaning |
|---|---|
| `disabled` | Provider access is disabled. |
| `configured` | Access is acknowledged and enabled, but the credential is missing or cannot be decrypted; it is not send-ready. |
| `ready` | Access is acknowledged and enabled, the credential is available, automatic roots are off, and no active provider failure exists. |
| `automatic_enabled` | `ready` with automatic roots enabled. |
| `degraded` | A recoverable current provider, configuration, or storage condition prevents normal operation. |
| `failing` | An active terminal provider failure requires operator attention. |

`attempt_counts` are non-negative integers no greater than `9007199254740991`; `queued + running <= 100`. They count retained attempts only: an attempt removed by its own `AuditDays` expiry is no longer included, regardless of its source or retry lineage. `latest_failure` is `null` or:

```json
{
  "reason": "timeout",
  "at": "2026-09-26T16:31:20.000Z"
}
```

`reason` is exactly one of `authentication_failed`, `configuration_disabled`, `configuration_invalid`, `evidence_unavailable`, `interrupted`, `network_error`, `provider_rate_limited`, `redirect_refused`, `request_limit`, `response_invalid`, `response_limit`, `storage_unavailable`, `timeout`, or `upstream_error`. It contains no provider text. A status-store read failure returns `503` / `status_unavailable`.

## Source attempt history and explicit root request

### `GET /api/v1/investigation/sources/{source_kind}/{source_id}/attempts`

Returns the immutable ordered attempt history for one durable source. `source_kind` is `event_spike` or `session_drop`; `source_id` has the common identifier format. The source must exist, but no evidence is built and no provider work occurs.

The optional `limit` query parameter is an integer `1..100` (default `100`). The optional `after_attempt_number` is a decimal positive attempt number. The response contains the first page sorted by ascending `attempt_number` strictly greater than `after_attempt_number` (or all retained attempts from 1 when absent) and adds `next_after_attempt_number`, which is the final returned attempt number when another page exists, otherwise `null`.

```json
{
  "source": {"source_kind":"event_spike","source_id":"42"},
  "attempts": [
    {
      "attempt_id": "91",
      "attempt_number": 1,
      "initiation": "manual",
      "retry_of_attempt_id": null,
      "state": "completed",
      "created_at": "2026-09-26T16:31:20.000Z",
      "started_at": "2026-09-26T16:31:21.000Z",
      "completed_at": "2026-09-26T16:31:22.000Z",
      "failure_reason": null,
      "evidence_version": 1,
      "omission_codes": []
    }
  ],
  "next_after_attempt_number": null
}
```

History is sorted by ascending `attempt_number`, which starts at 1 and increases contiguously when attempts are created for its source; retained history can have gaps after independent expiry. `initiation` is `automatic`, `manual`, or `retry`. `state` is exactly `queued`, `running`, `completed`, `insufficient_evidence`, or `failed`. `started_at` is null only for `queued`; `completed_at` is null only for `queued` and `running`; `failure_reason` is non-null only for `failed` and uses the status reason enum. `evidence_version` is a positive integer. `omission_codes` uses the common stored-evidence omission enum without translation.

A missing source returns `404` / `source_not_found`; an expired source whose source row exists but whose attempts were removed by `AuditDays` returns `200` with an empty `attempts` array and `next_after_attempt_number:null`. A history read failure returns `503` / `history_unavailable`.

### `POST /api/v1/investigation/sources/{source_kind}/{source_id}/attempts`

Creates an explicit manual root attempt for an eligible durable source. The request body is exactly `{}`. This route is an explicit operator request, not a provider proxy: it never accepts provider questions, endpoint data, evidence, headers, or credentials.

The handler authorizes first, then checks source existence and deterministic provider eligibility. In one transaction it claims the root identity and persists the bounded host-free evidence snapshot before returning. A newly created row is either `queued` (eligible evidence) or terminal `insufficient_evidence` (required retained context absent); no provider wire transmission may happen before that write.

A new row returns `201` with the standard attempt summary above. For a concurrent or repeated request while the source root is `queued` or `running`, return `200` with that existing summary and no new row, evidence build, or provider call. There is no client idempotency header and no automatic HTTP retry; the source identity is the idempotency key.

The route returns:

| Status | Error code | Condition |
|---|---|---|
| `404` | `source_not_found` | No durable deterministic source exists. |
| `409` | `source_ineligible` | Source is not a confirmed evtspike or an `unexplained` confirmed session drop. |
| `409` | `provider_not_ready` | Access is not enabled, acknowledged, and credentialed at request time. |
| `409` | `source_completed` | Any attempt for the source completed; a new root is prohibited. |
| `409` | `retry_required` | The source has a failed or insufficient terminal attempt; use that attempt's retry route. |
| `429` | `queue_full` | There are already 100 queued/running attempts. |
| `503` | `attempt_store_unavailable` | The required transactional persistence could not complete; zero provider egress occurred. |

A source with a terminal `insufficient_evidence` may be returned as the new `201` attempt without sending a request. The caller must use its linked retry route for any later fresh snapshot; the root is immutable.

## Attempt detail and linked retry

### `GET /api/v1/investigation/attempts/{attempt_id}`

Returns the immutable safe detail for a retained attempt. `attempt_id` uses the common identifier format.

```json
{
  "attempt": {
    "attempt_id": "91",
    "attempt_number": 1,
    "source": {"source_kind":"event_spike","source_id":"42"},
    "initiation": "manual",
    "retry_of_attempt_id": null,
    "state": "completed",
    "created_at": "2026-09-26T16:31:20.000Z",
    "started_at": "2026-09-26T16:31:21.000Z",
    "completed_at": "2026-09-26T16:31:22.000Z",
    "failure_reason": null,
    "evidence": {
      "version": 1,
      "snapshot_at": "2026-09-26T16:31:20.000Z",
      "window_start": "2026-09-26T16:01:20.000Z",
      "window_end": "2026-09-26T16:31:20.000Z",
      "omission_codes": []
    },
    "result": {
      "kind": "hypothesis",
      "likely_cause": {
        "choice": "resource_pressure",
        "confidence": 0.83,
        "probabilities": {
          "resource_pressure": 0.83,
          "identity_or_authentication": 0.02,
          "service_or_os_failure": 0.03,
          "network_or_dependency": 0.04,
          "planned_drain_or_maintenance": 0.01,
          "fleet_correlated_event": 0.02,
          "unknown": 0.05
        }
      },
      "impact": {
        "choice": "high",
        "confidence": 0.80,
        "probabilities": {
          "low": 0.02,
          "moderate": 0.08,
          "high": 0.80,
          "critical": 0.07,
          "unknown": 0.03
        }
      },
      "evidence_sufficiency": {
        "choice": "sufficient",
        "confidence": 0.95,
        "probabilities": {
          "insufficient": 0.01,
          "partial": 0.04,
          "sufficient": 0.95
        }
      },
      "human_review": {
        "choice": "required",
        "confidence": 0.90,
        "probabilities": {
          "required": 0.90,
          "not_required": 0.10
        }
      }
    },
    "provenance": {
      "provider_profile": "typesafe_jev",
      "requested_model": "jev-latest",
      "request_started_at_ms": 1790430681000,
      "request_completed_at_ms": 1790430682000,
      "request_header_bytes": 234,
      "request_body_bytes": 1000,
      "response_header_bytes": 167,
      "response_body_bytes": 400,
      "validation_outcome": "accepted"
    }
  }
}
```

`result` and `provenance` are non-null for a completed provider response and for a provider response whose `evidence_sufficiency` is `insufficient`. A local pre-send `insufficient_evidence` attempt has `result:null` and `provenance:null`. `result.kind` is always `hypothesis`; it is not a diagnosis, recommendation, or action. Every non-null question result is a ChoiceAnswer with exactly `choice`, `confidence`, and `probabilities`. Its `choice` uses the corresponding closed enum below; `confidence` is a finite number in `[0,1]`; and `probabilities` has exactly every value of that enum as a finite `[0,1]` value whose sum is within `1e-6` of one. No `reason_codes` field exists.

`evidence.omission_codes` uses the common stored-evidence omission enum without translation.

| Question | Choice values |
|---|---|
| `likely_cause` | `resource_pressure`, `identity_or_authentication`, `service_or_os_failure`, `network_or_dependency`, `planned_drain_or_maintenance`, `fleet_correlated_event`, `unknown` |
| `impact` | `low`, `moderate`, `high`, `critical`, `unknown` |
| `evidence_sufficiency` | `insufficient`, `partial`, `sufficient` |
| `human_review` | `required`, `not_required` |

The provider response is validated for all four ChoiceAnswers. When `evidence_sufficiency.choice` is `insufficient`, the terminal state is `insufficient_evidence`; `evidence_sufficiency` and `human_review` are persisted and displayed, while `likely_cause` and `impact` are `null`. Their choice, confidence, and probabilities are neither persisted nor displayed. A retained provenance has exactly `provider_profile:"typesafe_jev"`, `requested_model:"jev-latest"`, non-negative integer `request_started_at_ms` and `request_completed_at_ms`, locally measured bounded integer `request_header_bytes`, `request_body_bytes`, `response_header_bytes`, and `response_body_bytes` for emitted request and received response bytes, and `validation_outcome`, which is exactly `accepted` or `insufficient_evidence`. Provenance retains no provider-controlled value, including `model`, usage, answers, headers, or raw body. The provider response's `model` member is an untrusted bounded shape-validation input only: it is discarded after validation and is never persisted, logged, projected, or included in provenance.

A retained failed attempt, including one caused by a rejected or invalid provider response, has `result:null`, `provenance:null`, and a safe `failure_reason`; rejected and failed responses create no provenance row. A missing or AuditDays-expired attempt returns `404` / `attempt_not_found`; a read failure returns `503` / `attempt_detail_unavailable`.

### `POST /api/v1/investigation/attempts/{attempt_id}/retry`

Creates one explicit, linked retry from a `failed` or `insufficient_evidence` attempt. The request body is exactly `{}`. Authorization happens before predecessor lookup or new evidence assembly. After authorization and predecessor lookup establish that the predecessor is retryable, the handler looks up its deterministic source before configuration readiness, evidence assembly, attempt creation, or provider work. The retry uses a fresh bounded snapshot and does not mutate its predecessor.

A successful transaction returns `201` with the standard attempt summary. The new attempt has `initiation:"retry"`, `attempt_number` one greater than the latest source attempt number, and `retry_of_attempt_id` equal to the path attempt ID. It follows the same storage-before-egress and one-wire-transmission rules as a manual root.

`retry_of_attempt_id` is an integer-backed durable link encoded as a positive decimal string, not a foreign key. If a predecessor expires, a retained child keeps this opaque value; predecessor expiry neither retains the predecessor nor deletes, changes, or prevents access to its child.

| Status | Error code | Condition |
|---|---|---|
| `404` | `attempt_not_found` | The predecessor is absent or expired. |
| `404` | `source_not_found` | The retained retryable predecessor's deterministic source row is absent or expired. This is returned after authorization and predecessor lookup, before a fresh snapshot, new attempt row, configuration/provider work, or egress. |
| `409` | `retry_not_allowed` | The predecessor is `queued`, `running`, or `completed`. |
| `409` | `provider_not_ready` | Access is not enabled, acknowledged, and credentialed at request time. |
| `429` | `queue_full` | There are already 100 queued/running attempts. |
| `503` | `attempt_store_unavailable` | The retry/evidence transaction could not complete; zero provider egress occurred. |

A retry that lacks required retained context is recorded as terminal `insufficient_evidence` and returned `201`; it still has its immutable link and makes no provider call. If its source has expired, no retry is created and no predecessor evidence may be reused. No automatic retry, resend after restart, retry header, or retry of a running attempt exists. Restart recovery changes a durable `running` row to `failed` with `failure_reason:"interrupted"`; that row becomes retryable only through this route.

## SSE: `investigation_update`

The existing authenticated `GET /api/v1/events` stream remains the transport and its existing keepalive, reconnect, slow-subscriber, and session-revalidation behavior is unchanged. After a successful durable write, the broker publishes one additive event:

```text
event: investigation_update
data: {"kind":"attempt","attempt":{"attempt_id":"91","source":{"source_kind":"event_spike","source_id":"42"},"attempt_number":1,"initiation":"manual","retry_of_attempt_id":null,"state":"queued","failure_reason":null,"updated_at":"2026-09-26T16:31:20.000Z"}}

```

`kind` is `attempt` or `status`.

For `kind:"attempt"`, `attempt` is required and has exactly the members shown above. `updated_at` is the durable creation, state-transition, or terminal-update timestamp. The event does not include evidence, result, provenance, diagnostic data, canonical host, or credentials; clients refresh attempt detail through REST when needed.

For `kind:"status"`, the payload is exactly:

```json
{
  "kind": "status",
  "status": {
    "operational_state": "degraded",
    "attempt_counts": {
      "queued": 0,
      "running": 0,
      "completed": 12,
      "insufficient_evidence": 3,
      "failed": 3
    },
    "latest_failure": {
      "reason": "authentication_failed",
      "at": "2026-09-26T16:31:22.000Z"
    }
  }
}
```

`status` has no provider credential/configuration fields and uses the `GET /api/v1/investigation/status` enums. Status events are published for persisted settings/status/degradation changes; attempt events are published for persisted attempt creation and lifecycle transitions. Events are not a durable audit API, may be coalesced by a slow subscriber, and must be treated as an invalidation signal. Older clients ignore this unknown event type.

## Lifecycle, egress, retention, and compatibility invariants

1. The only state progression is `queued → running → completed|insufficient_evidence|failed`. Terminal rows and their evidence/result/provenance/failure are immutable.
2. One dashboard-owned worker permits at most 100 nonterminal attempts, 10 sends per minute with burst 2, and a 30-second request timeout. Worker rate limiting delays a queued attempt; it is not an attempt failure. `queue_full` is only an API rejection before a new attempt is created. Immediately before its first and only send, the worker revalidates current acknowledged/enabled/credentialed configuration. Disablement or invalid configuration changes unsent queued work to terminal `failed` with `configuration_disabled` or `configuration_invalid`, respectively, and never sends.
3. Each attempt persists its sanitized evidence before its sole fixed-endpoint transmission. `authentication_failed`, `network_error`, `provider_rate_limited`, `redirect_refused`, `request_limit`, `response_invalid`, `response_limit`, `storage_unavailable`, `timeout`, and `upstream_error` never cause automatic HTTP retry or any detector/drain/notification/remediation action.
4. `AuditDays` expires every attempt independently from that attempt's own `created_at`, without considering a source, predecessor, or retry child. Evidence, result, provenance, and failure cascade only when their own attempt expires. `retry_of_attempt_id` has no self-FK cascade and remains an opaque integer on retained retries whose predecessor has expired. Deterministic source retention is independent: retention maintenance explicitly deletes session-drop source rows in bounded chunks when their own `detected_at` is older than `AuditDays`, regardless of linked attempts. For example, an unexpired retry can retain its opaque predecessor link after that predecessor expires, yet a retry request fails `404` / `source_not_found` once its session-drop source row has expired; neither retained attempt prolongs the source. Host baselines and detector state persist until permanent host removal. A pre-upgrade/missing context is reported only through bounded unavailable/omission state; it is never backfilled from unbounded history.
5. A rollback may leave additive v4 records ignored by an older binary. If an older binary rewrites configuration and drops investigation fields, re-upgrade defaults provider access and acknowledgement to disabled until explicitly restored.
