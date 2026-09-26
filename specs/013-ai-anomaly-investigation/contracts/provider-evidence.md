# Provider Evidence Contract — `typesafe_jev` v1

**Status:** implementation-binding v1
**Related:** [specification](../spec.md), [plan](../plan.md), [research](../research.md#r1-fixed-typesafe-http-integration-and-consent)

This contract defines the entire provider boundary. The central Windows dashboard/service is the only caller. Agents, machine-account routes, browser-local durable state, existing shared settings, and `/api/v1/config` are outside this contract and MUST NOT receive provider authority, credentials, evidence, results, or provenance.

The provider is advisory only: it does not determine anomalies, modify detector state, or authorize remediation. Every accepted classification is displayed and stored as a **hypothesis**.

## 1. Fixed transport and consent

| Item | Required value |
|---|---|
| Profile | `typesafe_jev` |
| Method and endpoint | `POST https://api.typesafe.ai/v1/systemone` exactly |
| URL | No configurable host, path, port, userinfo, query, or fragment. Reject any other URL before dialing. Production code MUST construct this URL as a constant. Tests inject an `http.RoundTripper` or `http.Client` fake which records a request whose URL remains this exact official endpoint. |
| TLS | Normal platform certificate and hostname validation; TLS 1.2 or newer. No insecure skip-verify, custom trust, proxy endpoint, or fallback provider. |
| Redirects | Disabled. Any 3xx response is terminal `failed` with `redirect_refused`; never follow it. |
| Timeout | 30 seconds for the complete request. |
| Request headers | Application-selected headers are exactly `Authorization: Bearer <credential>`, `Content-Type: application/json`, `Accept: application/json`, and `Accept-Encoding: gzip`. HTTP `Host` is the fixed endpoint host. No credential is permitted in URL, body, log, diagnostic, telemetry, audit, evidence, or provenance. |
| Header limit | Reject request construction or response processing when aggregate headers exceed 16 KiB (16,384 bytes). Count each field name, colon, optional single space, value, and CRLF, plus final CRLF, as emitted/received. |
| Request limit | UTF-8 JSON request body, including JSON syntax, at most 16 KiB (16,384 bytes). |
| State limit | The canonical compact UTF-8 JSON encoding of the `state` object is at most 8,000 bytes. |
| Success body limit | Read at most 32 KiB (32,768 decompressed bytes). Reject content exceeding the limit; do not retain it. |
| Error body limit | Read and discard at most 4 KiB (4,096 decompressed bytes); never parse, log, return, or persist it. |
| Compression | Accept only `identity` and `gzip`; for `gzip`, enforce the decompressed limits above while streaming. Reject every other content encoding. |
| Rate and concurrency | One worker; no more than 100 queued/running attempts; 10 sends/minute, burst 2. A full queue is an API rejection and creates no attempt. A worker rate limit delays an existing queued attempt; it is not an attempt failure. |

The credential is obtained only from the existing runtime DPAPI configuration after a current configuration check. Provider access and automatic investigation default to false and are independently enabled. A send additionally requires persisted acknowledgement that TypeSafe AI is a third party; processes API Input in the United States and through service providers; does not train or fine-tune on API Input; publishes no fixed external retention period; and is outside DrainCtl's deletion control. `AuditDays` applies only to DrainCtl's local copies.

## 2. Sanitized state schema

`state` is a JSON object, never a JSON string containing JSON. The builder MUST construct this DTO field-by-field; it MUST NOT marshal telemetry rows, `CheckResult`, `SpikePayload`, request objects, maps, interfaces, raw JSON, or arbitrary structs.

All integer timestamps are UTC Unix milliseconds. All counts and scaled values are non-negative base-10 integers. An absent optional item is represented only by an omission entry; it is never represented by `null`, an empty string, a fabricated zero, or a replacement lookup. `null` is not valid anywhere in the state object.

```json
{
  "v": 1,
  "snapshot_at_ms": 0,
  "window": {"from_ms": 0, "to_ms": 0},
  "source": {
    "kind": "event_spike",
    "id": 0,
    "label": "source",
    "anomaly": "evtspike",
    "details": {
      "channel": "agent",
      "window_from_ms": 0,
      "window_to_ms": 0,
      "observed_count": 0,
      "expected_count_milli": 0,
      "tail_probability_ppb": 0
    }
  },
  "context": {
    "freshness": "fresh",
    "drain": "allow_all",
    "detector": "confirmed"
  },
  "local": {
    "points": [
      {"at_ms": 0, "label": "source", "metric": "sessions_active", "value_milli": 0}
    ]
  },
  "fleet": {
    "points": [
      {"at_ms": 0, "label": "peer_1", "metric": "sessions_active", "value_milli": 0}
    ]
  },
  "omitted": ["local_points"]
}
```

### 2.1 Required fields and closed values

| Path | Rule |
|---|---|
| `v` | Required literal integer `1`. |
| `snapshot_at_ms` | Required positive UTC epoch milliseconds at evidence assembly. |
| `window.from_ms`, `window.to_ms` | Required. Let `source_time_ms` be the source event's durable timestamp. Require `snapshot_at_ms >= source_time_ms`; set `from_ms = source_time_ms - 1,800,000` and `to_ms = min(source_time_ms + 1,800,000, snapshot_at_ms)`. This source-relative window has a maximum span of 3,600,000 milliseconds (60 minutes). |
| `source.kind` | Required: `event_spike` or `session_drop`. |
| `source.id` | Required positive durable source ID. It is an identifier, not a host identity. |
| `source.label` | Required literal `source`. |
| `source.anomaly` | Required: `evtspike` for `event_spike`; `session_drop` for `session_drop`. |
| `source.details` | Required closed union selected by `source.kind`, defined below. |
| `context.freshness` | Required: `fresh`, `stale`, or `unknown`. |
| `context.drain` | Required: `allow_all`, `draining`, or `unknown`. |
| `context.detector` | Required: `confirmed`, `unexplained`, `drain_associated`, `unknown_context`, or `unavailable`. `event_spike` uses `confirmed`; a `session_drop` uses its durable classification, with unavailable retained context as `unavailable`. A session-drop source is eligible to be sent only when this value and `source.details.classification` are both `unexplained`; `drain_associated` and `unknown_context` are retained locally but never sent. |
| `local.points` | Optional, at most 61 points, chronological by `(at_ms, metric)`, each inside `window`. Contains only retained source-local aggregates. |
| `fleet.points` | Optional, at most 61 points, chronological by `(at_ms, label, metric)`; a point is inside `window` and is anonymous fleet correlation only. |
| point `at_ms` | Integer inside `window`. |
| point `label` | Literal `source` in `local`; `peer_1` through `peer_60` in `fleet`, assigned in ascending `(at_ms, stable durable peer source ID)` order. |
| point `metric` | Required closed retained-counter enum: `cpu_pct`, `cpu_p95_pct`, `mem_avail_mb`, `mem_total_mb`, `pages_sec`, `disk_queue`, `tcp_retrans_sec`, `input_delay_p50_ms`, `input_delay_p95_ms`, `input_delay_max_ms`, `session_cpu_p95_pct`, `session_cpu_p50_pct`, `session_mem_p95_bytes`, `session_mem_p50_bytes`, `rfx_fps_out`, `rfx_fps_out_p50`, `rfx_skip_server_sec`, `rfx_skip_net_sec`, `rfx_encode_ms`, `rfx_encode_ms_p50`, `rfx_quality_pct`, `rfx_quality_pct_p50`, `rfx_rtt_ms`, `rfx_rtt_ms_p50`, `rfx_loss_pct`, `rfx_loss_pct_p50`, `rfx_skip_server_sec_p50`, `rfx_skip_net_sec_p50`, `sessions_total`, `sessions_active`, `sessions_disconnected`, or `sessions_max`. No other metric code or arbitrary metric string is permitted. |
| point `value_milli` | Required non-negative integer equal to the metric's retained counter value multiplied by 1,000. A successful zero is `0`; an unavailable enumeration is omitted, never zero. |
| `omitted` | Required array of distinct omission enums in the order in §3. |

`event_spike` `source.details` contains exactly `channel`, `window_from_ms`, `window_to_ms`, `observed_count`, `expected_count_milli`, and `tail_probability_ppb`. `channel` is exactly one fixed channel code: `agent`, `dashboard`, `eventlog`, `wmi`, `perf`, `service`, `unknown`, or `custom_channel`; unrecognized source channel text maps only to `custom_channel`. The window endpoints are integers within `window` and ordered. `observed_count` is an integer. `expected_count_milli` is the expected count multiplied by 1,000. `tail_probability_ppb` is the lower/upper applicable event-spike tail probability multiplied by 1,000,000,000, in `[0, 1,000,000,000]`.

`session_drop` `source.details` contains exactly `observed_count`, `reference_count`, `expected_count_milli`, `absolute_loss`, `relative_loss_bps`, `tail_probability_ppb`, and `classification`. `observed_count`, `reference_count`, and `absolute_loss` are integers; `expected_count_milli` is the expected count multiplied by 1,000; `relative_loss_bps` is the absolute loss divided by `reference_count`, multiplied by 10,000, with `0` only when both values are zero; `tail_probability_ppb` is the lower-tail probability multiplied by 1,000,000,000, in `[0, 1,000,000,000]`; and `classification` is exactly `unexplained`, `drain_associated`, or `unknown_context`. Only `unexplained` is eligible to be sent; `drain_associated` and `unknown_context` are never provider evidence. The builder MUST verify `absolute_loss = reference_count - observed_count` and MUST reject negative loss rather than serialize it.

Only the displayed keys are permitted. `local` and `fleet`, when present, each contain only `points`; a point contains only the displayed keys. No additional JSON property is allowed at any depth. Numeric JSON values MUST be finite unsigned base-10 integers—no float, exponent, signed zero, or stringified number.

### 2.2 Host identity and forbidden data

The canonical registered host remains only in the linked deterministic `event_spikes` or `session_drop_anomalies` row (and host-keyed detector state). After dashboard-group authorization, source/list/detail projections may resolve it from that row. It MUST NOT be copied into this state, an attempt, result, provenance, provider error, diagnostic, SSE payload, or browser-local durable state.

The builder replaces the source host with `source` and all peer identities with `peer_n`. It MUST reject rather than redact any field whose value could contain: canonical host, FQDN, domain, IP address, customer name/identifier, user name, session ID, `changed_by`, free text, URL, path, Event Log message/XML, dump bytes, credential, notification secret, arbitrary file content, generic map, raw struct, or raw provider request/response. The only strings allowed in state are object keys and the closed literals listed in this contract.

## 3. Deterministic omission and serialization

1. Capture `snapshot_at_ms` once. Using the §2 source-relative window formula, derive and clip the source window once; do not query beyond it.
2. Add mandatory source and context fields in the order shown in §2.
3. Add local points in chronological order, then fleet points in chronological order.
4. Serialize compact UTF-8 JSON with the exact object-key order shown in §2 and each point-key order shown there. Serialize the outer System One request once after state construction.
5. If the canonical state exceeds 8,000 bytes or the full request exceeds 16 KiB, remove optional units from the end in this exact order: last fleet point, last local point, then the entire empty `fleet` object, then the entire empty `local` object. Record each corresponding omission enum exactly once; serialize `omitted` in the single enum order in this section, not removal order. Re-serialize the state and then the full request after each removal. Do not remove mandatory source/context fields and do not truncate a string or number.
6. If mandatory content plus required `omitted` cannot meet either bound, create/persist an `insufficient_evidence` attempt when storage is available and make no request.

`omitted` is the single closed enum array: `pre_upgrade_context`, `retention_expired`, `freshness_unavailable`, `drain_unavailable`, `detector_unavailable`, `local_points`, `fleet_points`, `local_aggregate`, `fleet_aggregate`. It contains each applicable value at most once and only in that displayed order. Source data unavailable because the source row does not exist is not an omission: it is ineligible and creates no attempt.

## 4. Fixed System One request

The request has exactly these top-level fields in this order: `model`, `state`, `questions`. `model` is the fixed literal `jev-latest`. `state` is the §2 object in its canonical compact encoding, not a quoted or nested JSON string. No model discovery call is made.

```json
{
  "model": "jev-latest",
  "state": {
    "v": 1,
    "snapshot_at_ms": 0,
    "window": {"from_ms": 0, "to_ms": 0},
    "source": {"kind": "event_spike", "id": 0, "label": "source", "anomaly": "evtspike", "details": {"channel": "agent", "window_from_ms": 0, "window_to_ms": 0, "observed_count": 0, "expected_count_milli": 0, "tail_probability_ppb": 0}},
    "context": {"freshness": "fresh", "drain": "allow_all", "detector": "confirmed"},
    "omitted": []
  },
  "questions": {
    "likely_cause": {
      "type": "choice",
      "criteria": {
        "resource_pressure": null,
        "identity_or_authentication": null,
        "service_or_os_failure": null,
        "network_or_dependency": null,
        "planned_drain_or_maintenance": null,
        "fleet_correlated_event": null,
        "unknown": null
      }
    },
    "impact": {
      "type": "choice",
      "criteria": {"low": null, "moderate": null, "high": null, "critical": null, "unknown": null}
    },
    "evidence_sufficiency": {
      "type": "choice",
      "criteria": {"insufficient": null, "partial": null, "sufficient": null}
    },
    "human_review": {
      "type": "choice",
      "criteria": {"required": null, "not_required": null}
    }
  }
}
```

`questions` contains exactly these four names in this order. Every question contains exactly `type: "choice"` and its displayed `criteria`; no `instructions`, dynamic question, prose, `noul`, or `score` question is permitted. Criteria keys are the enum values, in the displayed order; their values are JSON `null` solely to express the provider's allowed choice names and are not evidence values.

## 5. Strict response validation and retained result

Accept only a 2xx response with `Content-Type: application/json` (media-type comparison ignores parameters). Decode one JSON value with duplicate-key detection; reject trailing data, unknown top-level fields, and all malformed or oversized values. The decoder MUST parse number tokens as exact decimal values, not Go `float32` or `float64`.

The response must contain exactly `model`, `answers`, and `usage`. `model` is required solely for response-shape validation and is a non-empty string of at most 128 UTF-8 bytes. After that bounded validation, the implementation MUST discard it immediately: it MUST NOT be logged, persisted, projected, returned, or included in provenance, diagnostics, results, SSE payloads, or browser-local state. `usage` contains only non-negative integer `input_tokens` and `output_tokens`; retain neither response body nor prose. `answers` contains exactly the four question names in §4, with no duplicates or fifth name.

Each answer contains exactly `type`, `choice`, `confidence`, and `probabilities`:

- `type` is literal `choice`; reject `noul`, `score`, and every other discriminator.
- `choice` is one of that question's closed enum values.
- `confidence` is an exact finite JSON decimal in `[0, 1]`.
- `probabilities` has exactly the closed enum keys for that question, each an exact finite JSON decimal in `[0, 1]`; their exact-decimal sum is within `0.000001` of `1`.
- No answer may contain a prose field, explanation, rationale, extra field, raw JSON payload, or an unknown enum.

For retention, convert each validated `confidence` and each validated probability independently to an integer in parts per million (`0` through `1,000,000`) by decimal half-up rounding. Persist, separately for each of the four questions, its choice, `confidence_ppm`, and a closed full map from every criterion enum to `probability_ppm`. The maps are bounded to their question's displayed criterion keys; no arbitrary map, `reason_codes`, response prose, or raw response is retained.

### 5.1 Closed local provenance

Only a valid decoded provider response creates provenance. Its persisted provenance contains exactly this closed, locally controlled schema:

```json
{
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
```

`provider_profile` and `requested_model` are the fixed local request constants. The timestamps and all four byte counts are locally measured bounded integers: the request fields measure the emitted request, and the response fields measure the received response. `request_completed_at_ms` MUST be greater than or equal to `request_started_at_ms`; each byte count MUST be positive and within its applicable request, response-header, or response-body bound. `validation_outcome` is exactly `accepted` or `insufficient_evidence`; a valid decoded response whose `evidence_sufficiency.choice` is `insufficient` uses `insufficient_evidence`, and every other valid decoded response uses `accepted`. No provider-controlled value—including `model`, `usage`, answers, headers, or raw body—may be included, copied, logged, projected, or derived into provenance. Failed, rejected, and local pre-send insufficient outcomes create no provenance.

If `evidence_sufficiency.choice` is `insufficient`, terminal state is `insufficient_evidence`: validate all four answers, but persist and display only the evidence-sufficiency and human-review choice/confidence/probability sets, and create provenance with `validation_outcome: "insufficient_evidence"` as defined in §5.1. Do not persist or display the likely-cause or impact choice, confidence, or probabilities. A local pre-send insufficient attempt has no result or provenance. Otherwise a valid response is terminal `completed` and creates provenance with `validation_outcome: "accepted"`.

## 6. One-send lifecycle and safe failure mapping

The sanitized snapshot and immutable `queued` attempt (stable `source.kind`/`source.id`, initiation, and optional retry predecessor only) MUST commit in SQLite before a send. Immediately before the first transmission, validate current credential, acknowledgement, and access state; then atomically claim and mark the attempt `running`.

A running attempt performs one—and only one—HTTP `POST`. A connection failure after bytes may have left the process is still a consumed send. There is no HTTP retry, redirect retry, credential retry, alternate endpoint, fallback provider, model retry, or replay. On restart, every persisted `running` row becomes terminal `failed` with `interrupted` and is never sent again. Only an authorized explicit retry of a `failed` or `insufficient_evidence` predecessor may create a new immutable linked attempt with a fresh snapshot. For such a retry, after authorization and predecessor lookup but before evidence assembly, new-attempt creation, or provider work, the implementation MUST re-read the deterministic source row. If retention has expired that row, return `404 source_not_found`; do not reuse stale evidence and do not create, queue, or send a new attempt.

The only safe attempt failure enums are `authentication_failed`, `configuration_disabled`, `configuration_invalid`, `evidence_unavailable`, `interrupted`, `network_error`, `provider_rate_limited`, `redirect_refused`, `request_limit`, `response_invalid`, `response_limit`, `storage_unavailable`, `timeout`, and `upstream_error`. Queue-full is an API rejection, not an attempt failure; worker rate limiting leaves the attempt queued.

| Condition | Terminal result / safe failure |
|---|---|
| Required retained evidence missing or bounded mandatory state cannot be built | `insufficient_evidence` / `evidence_unavailable` (no send) |
| Access disabled or acknowledgement absent before send | Cancel unsent queued work / `configuration_disabled` (no send) |
| Missing, undecryptable, or malformed credential; invalid fixed transport before send | Cancel unsent queued work / `configuration_invalid` (no send) |
| Queue cap before attempt creation | API rejection; no attempt |
| Worker rate limit before send | Leave the attempt `queued`; delay send |
| Request/header/state bound failure before send | `failed` / `request_limit` (no send) |
| TLS, DNS, connection, or other network failure | `failed` / `network_error` (one send if dial began) |
| Timeout | `failed` / `timeout` (one send if dial began) |
| HTTP 3xx | `failed` / `redirect_refused` (one send) |
| HTTP 401 or 403 | `failed` / `authentication_failed` (one send) |
| HTTP 429 | `failed` / `provider_rate_limited` (one send) |
| Other non-2xx | `failed` / `upstream_error` (one send) |
| Response header/body/encoding limit | `failed` / `response_limit` (one send) |
| Invalid JSON, wrong media type, prose, unknown field/type/enum, missing/duplicate/fifth answer, or invalid probability/confidence | `failed` / `response_invalid` (one send) |
| SQLite failure before state/attempt commit | No durable attempt if impossible; zero send; emit only structured local `storage_unavailable` diagnostic |
| SQLite failure after send but before terminal transition | Preserve no body; emit structured local `storage_unavailable` diagnostic; make terminal state durable only when SQLite is writable |

## 7. Required boundary canary tests

Tests MUST capture the serialized body, request headers, persisted sanitized snapshot/result/provenance/diagnostic, and fake-client receive count. The fake is an injected `http.RoundTripper` or `http.Client`; it MUST observe the exact fixed official endpoint and no real provider call is required for routine Windows smoke.

1. A request contains exactly one Bearer credential header and no credential bytes anywhere else.
2. Every body and persisted provider artifact has zero instances of a sentinel canonical host, FQDN/domain, IP, customer identifier, username, session ID, `changed_by`, URL, path, free text, Event Log/XML, dump marker, notification secret, or arbitrary-file marker.
3. Canonical host substitution is exactly `source`; peer labels are only deterministic `peer_n`; event channel codes and retained sample metrics are only their closed enums. Every retained counter code is accepted and every other metric string is rejected. Session-drop evidence is sent only for `unexplained` classifications; `drain_associated`, `unknown_context`, and `confirmed` are rejected.
4. Source-detail union fields; the exact source-relative window formula (`from_ms = source_time_ms - 1,800,000`, `to_ms = min(source_time_ms + 1,800,000, snapshot_at_ms)`, with `snapshot_at_ms >= source_time_ms` and a 60-minute maximum); valid `value_milli: 0`; unavailable-counter omission; field order; the single omission-enum order; object-shaped `state`; 8,000-byte state; 16-KiB request/header; 32-KiB decompressed success; and 4-KiB discarded-error bounds are enforced.
5. The only request questions are the four v1 `choice` questions and their exact criteria enums; dynamic names/types and instructions are absent.
6. The validator rejects nested-string state, prose, `score`, `noul`, non-enum values, unknown fields, missing/duplicate/fifth answers, invalid exact-decimal probability sums, and out-of-range/non-finite confidence or probability. It validates a required bounded response `model` then discards it. Model-echo canaries containing a credential, canonical host/FQDN, IP address, URL/path, and prose independently prove that each echo is absent from every persisted, logged, projected, returned, diagnostic, SSE, and browser-local artifact. Provenance is exactly the §5.1 closed local schema with only `accepted` or `insufficient_evidence` validation outcomes. It retains independently bounded probability maps and confidences for all four answers, except that an `insufficient` evidence-sufficiency response retains neither cause nor impact set.
7. Redirects, non-fixed URLs, insecure TLS, disabled/unacknowledged configuration, storage-before-egress failure, timeout, authentication failure, rate limiting, and malformed/oversized responses cause the stated safe outcome.
8. A server observes at most one request for one attempt despite transport failure or restart; a later operator retry is a new linked attempt and may make one new request.
9. Retrying an authorized failed or insufficient predecessor whose deterministic source row has expired returns `404 source_not_found` after predecessor lookup and before evidence assembly, new-attempt creation, provider work, or send; no stale evidence is reused.
