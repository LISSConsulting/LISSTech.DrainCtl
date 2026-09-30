# Session-Drop Events Contract

**Feature:** [013 AI Anomaly Investigation](../spec.md)
**Companion design:** [plan](../plan.md) §§ Session-drop state machine, SQLite v4, API/UI, and [research](../research.md) R7/R9
**Contract version:** `session_drop_v1`

This is a central Windows dashboard/service contract. It defines a new deterministic lower-tail source; it does not alter agent reports, `CheckResult`, `SpikePayload`, evtspike scoring, `/api/v1/spike`, `recent_spike`, or notification rules.

## 1. Terms and stable identity

| Term | Definition |
|---|---|
| **Registered host** | The canonical registered-host identity already resolved by the central service. It is retained only in the deterministic session-drop source row and host-keyed detector state. |
| **Observation identity** | `(canonical registered host, report_epoch_ms)`, where `report_epoch_ms` is the existing `CheckResult` UTC epoch-millisecond timestamp. No accepted-report ID exists or is added. |
| **Report epoch** | The report's UTC epoch-millisecond observation timestamp. It is strictly increasing per registered host for a numeric observation to be in order. An equal timestamp is a duplicate; a lower timestamp is out of order. |
| **Accepted time** | `accepted_at_ms` is the central service's UTC epoch-millisecond acceptance time, captured separately from `report_epoch_ms` and used for freshness. It does not participate in observation identity or ordering. |
| **Eligible numeric observation** | A distinct, in-order observation with a valid successful session enumeration, non-negative bounded counts, and affirmative `fresh` host freshness. It may be scored once. |
| **Unknown gap** | Nil enumeration, invalid/unavailable enumeration, missing report epoch, duplicate observation identity, stale or unknown freshness, or an out-of-order epoch. It is never numeric zero. |
| **Confirmation horizon** | The trailing up to three consecutive eligible numeric observations for one registered host. It is the only window used for fixed 2-of-3 confirmation and for the post-drain horizon. A gap clears it. A confirmed horizon has `confirmation_window_size` `2` when the second eligible report supplies the second candidate, otherwise `3`. |
| **Session-drop source** | A durable `session_drop_anomalies` row. Its investigation link is exactly `(source_kind: "session_drop", source_id: <positive decimal string encoding the durable anomaly ID>)`. |

The anomaly ID is a SQLite integer-backed durable ID, encoded in JSON and path parameters as a positive decimal string. Creation is idempotent: the transaction uniquely claims `(canonical_registered_host, confirmation_ended_report_epoch_ms)`. Re-delivery of the same observation returns the existing source and does not emit a second event. Investigation attempts retain only `source_kind` and `source_id`; they do not retain the registered host.

## 2. Observation and `TotalSessions` rules

For a successful enumeration only,

```
TotalSessions = ActiveSessions + DisconnectedSessions
```

Both operands MUST be present, finite integral counts in `[0, 2^31-1]`; the sum MUST not overflow that range. `TotalSessions == 0` is a valid numeric observation and follows normal scoring, confirmation, drain, and cooldown rules. A nil enumeration is not an empty result and is always an unknown gap.

Session-drop processing is fed only by the durable accepted-report inbox described below. The report receiver MUST first capture `accepted_at_ms` and source-local time context, then use one per-host serial SQLite writer transaction to persist the accepted `servers.last_result_json` and its detector input together:

1. persist the accepted `servers.last_result_json`;
2. for a report with `report_epoch_ms`, insert one immutable `session_drop_observation_inbox` row keyed by `(canonical_host, report_epoch_ms)`, assigning the row's private global acceptance order in that same transaction; and
3. commit before invoking a callback that can wake the detector consumer.

The inbox schema defines `accepted_sequence INTEGER PRIMARY KEY AUTOINCREMENT` and `UNIQUE(canonical_host, report_epoch_ms)`. `accepted_sequence` is the private global acceptance order of successfully inserted inbox rows; it is assigned only by SQLite in the writer transaction that persists the accepted server state. The inbox insert is idempotent. A duplicate `(canonical_host, report_epoch_ms)` key MUST retain the fields and `accepted_sequence` of the first accepted row and MUST NOT create a second detector input. The wake callback is not a handoff, acknowledgment, source of truth, or permission to discard the row: it MAY be lost, duplicated, or delayed without changing detection. A report that has no `report_epoch_ms` has no observation identity and MUST atomically record the `missing_report_epoch` detector gap with its accepted server state; it MUST NOT synthesize an inbox key or report ID.

An inbox row contains no agent-wire extension or accepted-report ID. Its immutable fields are: `canonical_host`; `report_epoch_ms`; `accepted_at_ms`; private `accepted_sequence`; source-local UTC offset in minutes and source-local calendar date; typed session-enumeration presence; independently nullable typed active and disconnected session counts; the validated enumeration status; the captured freshness outcome (`fresh`, `stale`, or `unknown`); and the captured drain/classification context (`drain_overlap`, `post_drain_horizon`, and `context_unknown`). The typed presence/count fields and enumeration status MUST preserve the distinction between a nil enumeration, a valid zero count, and invalid or unavailable data. `accepted_at_ms` is evidence and freshness time only: it MUST NOT be used to assign, break ties for, or otherwise derive dequeue order.

The consumer MUST dequeue pending inbox rows strictly by `accepted_sequence ASC` and, for each row, use one SQLite transaction to apply the existing watermark, gap, baseline, confirmation, drain-horizon, cooldown, and source-creation rules, then delete the consumed inbox row (or durably mark it consumed in the same transaction). It MUST NOT use `accepted_at_ms`, report epoch, host, or any other field as an ordering key or tie breaker; `accepted_sequence` is globally unique. It MUST NOT reconstruct detector input or provenance from `servers.last_result_json`, emit an event, or reinsert a row merely because the callback ran. If the process stops before that transaction commits, the row remains pending; if it commits, the detector updates and row removal commit together, so that accepted observation is consumed exactly once. Any source SSE publication remains after the source-creation commit as specified in §9.

On startup, before the receiver accepts any newer report, the service MUST drain every pending inbox row strictly by `accepted_sequence ASC` using that same consumer transaction. Runtime wakeups use the same drain path. This preserves accepted arrival order for the existing per-host epoch-watermark rules; an older epoch accepted after a newer one is still processed as the required out-of-order gap, rather than being reordered by epoch or acceptance timestamp.

For a consumed numeric observation, the consumer MUST:

1. compare the observation identity's epoch with the persisted order watermark: an equal epoch is a `duplicate_report_epoch` gap, and a lower epoch is an `out_of_order` gap; neither moves the watermark;
2. require successful valid enumeration and `fresh` freshness evaluated with the inbox's separately captured `accepted_at_ms`; otherwise record the applicable unknown gap; and
3. atomically record the observation identity/order watermark, score the numeric observation once, update detector state, and, if confirmed outside cooldown, insert the source anomaly.

A gap clears all three confirmation positions and records its safe gap reason (`nil_enumeration`, `invalid_enumeration`, `missing_report_epoch`, `duplicate_report_epoch`, `out_of_order`, `stale`, or `freshness_unknown`). It MUST NOT train either baseline, update the reference observation, create an anomaly, or be converted to `0`. Gaps do not advance the numeric order watermark except that a valid, in-order report does so in the transaction that scores it.

## 3. Baseline, slots, maturity, and decay

The detector owns 96 quarter-hour slots per registered host. For the source-local timestamp of an eligible report, its slot is:

```
slot = local_hour * 4 + floor(local_minute / 15)     // 0..95
```

The source-local offset and local calendar date are captured with the accepted observation, so slot/date assignment is replayable across DST and service restart. Each registered host also has one all-hours fallback baseline, with no slot index. It is trained only from that host's eligible normal observations and is never pooled across hosts or carried in OpenAI-facing evidence.

Each slot and all-hours fallback uses the versioned `gamma_poisson_lower_v1` model. Its persisted sufficient statistics are `alpha`, `beta`, `last_updated_at`, `last_normal_trained_at_ms`, and the set of distinct local eligible calendar days trained in that baseline. `last_normal_trained_at_ms` is updated only when that baseline receives an eligible normal-training observation; decay, scoring, candidates, confirmed horizons, drains, post-drain observations, unknown context, gaps, and reference changes MUST NOT update it. Before a score or normal update at `t`, decay each sufficient statistic's learned mass toward its fixed prior `(alpha=1, beta=1)` by:

```
d = exp(-(t - last_updated_at) / baseline_half_life)
alpha = 1 + (alpha - 1) * d
beta  = 1 + (beta  - 1) * d
```

A normal observation `x` updates both its selected host slot and that host's all-hours fallback as `alpha += x; beta += 1`, then records its local calendar day and updates that baseline's `last_normal_trained_at_ms`. The lower predictive-tail probability is the Gamma-Poisson/negative-binomial inclusive CDF `P(Y <= x | alpha, beta)` evaluated before that update. Implementations MUST use stable monotone numeric evaluation and persist the model version with detector state and an anomaly's baseline summary.

A host slot is mature after normal training on at least seven distinct source-local eligible calendar days. The host's all-hours fallback is usable only after at least 20 eligible normal observations spanning at least 24 hours from its `first_trained_at_ms` to its `last_normal_trained_at_ms`. Before local slot maturity, score with that host's usable all-hours fallback; otherwise the observation is warm-up/no-candidate. A mature local slot is always preferred over its host's all-hours fallback. Neither readiness condition is inferred from candidates, drains, post-drain observations, gaps, report count other than the fallback's 20 eligible normal observations, elapsed wall time other than the fallback's first-normal-training to last-normal-training 24-hour span, decay or score timestamps, or observations from another host.

`last_reference_total` is the most recent normal, baseline-training eligible value for the host; it is deliberately not updated by a candidate, confirmed value, drain/horizon value, or unknown-context value. This is the “last eligible observation” used for relative loss, so a first low value cannot make later lows look normal.

## 4. Fixed detector settings and validation

Only the following session-drop detector settings are configurable. Missing values receive the defaults below; rejected values are not clamped.

| Setting | Default | Inclusive validation range | Meaning |
|---|---:|---:|---|
| `lower_tail_threshold` | `0.0001` | `0.000000001`–`0.1` | Candidate requires `P(Y <= observed) < threshold`. |
| `minimum_drop_sessions` | `3` | `1`–`1000000` sessions | Candidate requires `last_reference_total - observed >= value`. |
| `minimum_drop_percent` | `30` | `1`–`99` percent | Candidate requires `100 * (last_reference_total - observed) / last_reference_total >= value`. |
| `baseline_half_life_hours` | `168` | `24`–`8760` hours | Time-based decay half-life used by both the host slot and its all-hours fallback. |
| `cooldown_minutes` | `60` | `1`–`1440` minutes | Per-registered-host interval after a confirmed source is inserted. |

The slot count (`96`), slot maturity (`7` distinct eligible days), all-hours-fallback readiness (`20` eligible normal observations spanning at least `24` hours), confirmation rule (`2` of the trailing `3` consecutive eligible numeric observations), and source-kind literal (`session_drop`) are fixed contract constants, not configuration. `last_reference_total` MUST be positive for a relative-loss comparison. A zero reference can train a normal baseline but cannot satisfy a relative loss; it therefore cannot create a candidate until a later positive normal reference exists.

## 5. Candidate, confirmation, drain, and anti-poisoning rules

For every eligible numeric observation after selecting a mature scoring baseline, calculate the lower tail and loss from `last_reference_total` before any update. It is a **candidate** only when all are true:

1. `P(Y <= observed) < lower_tail_threshold`;
2. `last_reference_total - observed >= minimum_drop_sessions`; and
3. `last_reference_total > 0` and `100 * (last_reference_total - observed) / last_reference_total >= minimum_drop_percent`.

Append one Boolean candidate flag for each eligible numeric observation to the per-host confirmation window; normal/warm-up observations append `false`. Keep only the trailing three flags. A confirmation occurs exactly when the window contains at least two `true` flags. It may occur on the second or third eligible report in the window. At confirmation, persist exactly the eligible reports examined: `confirmation_window_size` is `2` for a second-report confirmation and `3` for a third-report confirmation; `confirmation_flags` contains exactly that many Boolean flags in chronological order; and `confirmation_count` is their count of `true` values (therefore `2..confirmation_window_size`). A gap clears the window. After any confirmation, clear the window so the same observations cannot confirm a second source.

### Drain/context classification and precedence

For every eligible observation, retain only the safe classification context needed for its confirmation window:

- `drain_overlap`: a known active drain whose mode is not `AllowAll` overlaps the observation;
- `post_drain_horizon`: one of the next three eligible numeric observations after a known non-`AllowAll` drain ends; and
- `context_unknown`: drain state, drain end, or required detector classification state is unavailable for that observation.

The post-drain count advances only for eligible numeric observations; gaps do not consume it. A known non-`AllowAll` drain and its three-observation horizon are never baseline-training eligible. `AllowAll` is not a drain exclusion.

At confirmation, classify the durable anomaly using all observations in its confirming horizon, in this order:

1. `drain_associated` if any has `drain_overlap` or `post_drain_horizon`;
2. `unknown_context` if none is drain-associated and any has `context_unknown`; otherwise
3. `unexplained`.

This precedence is intentional: known drain evidence wins over incomplete ancillary context. Freshness that is stale or unknown is an unknown **gap** under §2 and cannot reach classification; `unknown_context` applies only when the numeric observation was fresh and in order but drain/classification context was unavailable.

An observation MUST NOT train or lower either baseline, nor replace `last_reference_total`, when it is a candidate, part of a confirmed horizon, drain-overlapping, post-drain-horizon, or context-unknown. Only a mature/warm-up normal observation with known non-drain context trains and becomes the next reference. This applies even while cooldown suppresses creation of a further source.

On confirmation, if `confirmed_at < cooldown_until`, persist confirmation/cooldown state and preserve the no-training rule, but insert no second anomaly and emit no SSE event. Otherwise insert exactly one session-drop source, set `cooldown_until = confirmed_at + cooldown_minutes`, and publish after the durable commit. Cooldown never changes classification of a source that is inserted.

## 6. Detection matrix

| Input/condition | Numeric score | Confirmation window | Baseline/reference | Durable source | Classification / OpenAI investigation eligibility |
|---|---|---|---|---|---|
| Successful enumeration; `TotalSessions = 0`; fresh, distinct, in order | Yes | Appends normal/candidate flag | Applies normal anti-poisoning rules | Only if it confirms | Same as any other numeric observation; zero is never an unknown gap |
| Nil enumeration | No | Clear | No update | No | Gap `nil_enumeration` |
| Invalid/unavailable count or missing report epoch | No | Clear | No update | No | Corresponding unknown gap |
| Equal-epoch observation identity | No | Clear | No update | No | Duplicate gap `duplicate_report_epoch`; never re-score or create a duplicate |
| Lower-epoch observation identity | No | Clear | No update | No | Out-of-order gap `out_of_order`; never re-score or create a duplicate |
| Stale or freshness unknown | No | Clear | No update | No | Gap `stale` or `freshness_unknown` |
| Warm-up (neither selected slot mature) | No lower-tail candidate | Appends `false` | Normal train/reference only if known non-drain context | No | No anomaly |
| Mature normal, known non-drain context | Yes | Appends `false` | Train and set reference | No | No anomaly |
| Candidate, fewer than 2 flags in horizon | Yes | Appends `true` | Neither train nor set reference | No | No anomaly yet |
| Candidate confirmation, known non-drain context, outside cooldown | Yes | Confirm then clear | No train/reference update | Yes, once | `unexplained`; eligible for the configured `openai_responses` investigation lifecycle |
| Candidate confirmation with non-`AllowAll` overlap or post-drain horizon | Yes | Confirm then clear | No train/reference update | Yes, once | `drain_associated`; not eligible for OpenAI investigation |
| Candidate confirmation with unavailable drain/classification context and no known drain | Yes | Confirm then clear | No train/reference update | Yes, once | `unknown_context`; not eligible for OpenAI investigation |
| Confirmation during cooldown | Yes | Confirm then clear | No train/reference update | No | No duplicate event/source |

## 7. Durable data and retention boundary

`session_drop_anomalies` is a deterministic source table and MAY contain the canonical registered host. Each row persists: integer-backed `id`, encoded in JSON and path parameters as a positive decimal string; canonical registered host; `confirmation_ended_report_epoch_ms`; `detected_at_ms`; `confirmation_started_at_ms` and `confirmation_ended_at_ms`; observed, reference, and expected totals; `absolute_loss` and `relative_loss`; `baseline_model_version`; `baseline_scope` (`slot` or `all_hours`); `slot_index` when `baseline_scope` is `slot`; `slot_mature_days`; model version and tail probability; `confirmation_window_size` (`2` or `3`), first and second confirmation flags (required Booleans), third confirmation flag (Boolean when the size is `3`, otherwise null), and confirmation count; `freshness_context`; `drain_context` (`none`, `overlap`, `post_horizon`, or `unknown`); `classification`; and `provider_eligibility`. `confirmation_started_at_ms` and `confirmation_ended_at_ms` are respectively the central acceptance times of the first and last eligible observations in the confirming horizon. They are distinct from every report-epoch field and are not inferred from `detected_at_ms`.

`session_drop_observation_inbox` is durable detector input, not an API, SSE, attempt, or browser-state table. It holds only unconsumed immutable accepted-report rows defined in §2. Its exact ordering/identity schema is `accepted_sequence INTEGER PRIMARY KEY AUTOINCREMENT` and `UNIQUE(canonical_host, report_epoch_ms)`: the private, globally unique sequence is the sole drain order, while the host/epoch key provides idempotency. `accepted_at_ms` is retained only as acceptance/freshness evidence and is never an ordering key. Detector state tables MAY contain the canonical host only where necessary for that host's 96 slots and all-hours fallback, report-epoch order watermark, confirmation flags, reference total, drain horizon, cooldown, and freshness acceptance time. Both baseline types are host-keyed and MUST be trained and scored only from that host's observations. Source expiry, attempt expiry, host offline/transient state, freshness gaps, and unregistration that does not permanently remove the host MUST NOT clear baseline/detector state; permanent host removal deletes its pending inbox rows and host-keyed detector state in the same transaction.

Session-drop sources are retained independently for `AuditDays` measured from each row's `detected_at`; expiring a source MUST NOT retain it because a linked attempt exists or alter an attempt's independently `created_at`-anchored retention. An investigation artifact (attempt, evidence snapshot/fact registry, provenance, diagnostic, attempt SSE payload, or browser-local durable state) MAY identify its source only as `source_kind: "session_drop"` and the positive-decimal `source_id`. It MUST NOT contain the registered host, FQDN, domain, IP, customer identity, report epoch, acceptance time, per-session data, raw source text, or other source free text. The sole free-text exception is a bounded, validated `anomaly_investigation_v1` report prose field, which remains provider-controlled untrusted plain text and is not a source identifier. This rule does not authorize raw provider text or source free text in any artifact.

## 8. Authenticated REST API

These additive endpoints use the existing dashboard-group `drainctl_session` cookie and existing session authorization middleware before route lookup. Machine-account routes, including `/api/v1/config`, have no equivalent endpoint. Missing or expired sessions return `401 {"error":{"code":"session_expired"}}`; dashboard-group denial returns `403 {"error":{"code":"access_denied"}}`. Neither response discloses host or source existence. All successful responses are UTF-8 JSON objects with exactly the members specified below; objects are closed and clients MUST NOT infer meaning from absent or additional members. JSON integers are numbers only where specified. Every ID and cursor is a base-10 positive decimal string with no sign, whitespace, leading zero, decimal point, or exponent. Every timestamp is an RFC 3339 UTC string with millisecond precision.

### `GET /api/v1/session-drops?limit={1..200}&before={positive_decimal_id}`

Returns newest-first deterministic source summaries, ordered by durable `id` descending. `limit` is optional and defaults to `50`; it is an unsigned decimal integer in the inclusive range `1..200`, with no leading zero. `before` is optional and, when present, is an exclusive positive-decimal ID cursor: returned IDs are strictly less than it. The service fetches enough rows to determine exhaustion. `next_before` is the final returned item's ID only when another matching row exists; otherwise it is JSON `null` (including for an empty page).

```json
{
  "items": [
    {
      "id": "42",
      "source_kind": "session_drop",
      "registered_host": "authorized-source-only",
      "confirmed_at": "2026-09-26T16:31:20.000Z",
      "classification": "unexplained",
      "investigation_eligible": true,
      "observed_total_sessions": 12,
      "reference_total_sessions": 48,
      "expected_total_sessions": 46.5,
      "absolute_loss_sessions": 36,
      "relative_loss": 0.75,
      "tail_probability": 0.00002,
      "baseline_model_version": "gamma_poisson_lower_v1",
      "baseline_scope": "slot",
      "slot_index": 65,
      "slot_mature_days": 7,
      "confirmation_window_size": 3,
      "confirmation_count": 2
    }
  ],
  "next_before": null
}
```

`items` is always an array of zero to `limit` closed summary objects. List summary objects MUST NOT contain an attempt, `attempts`, attempt summary, attempt state, or other attempt-lineage member. `source_kind` is always `session_drop`; `registered_host` is the authorized source-row identity; `classification` is `unexplained`, `drain_associated`, or `unknown_context`; `investigation_eligible` is a Boolean equal to the persisted provider-eligibility value; counts and `absolute_loss_sessions` are non-negative JSON integers; `expected_total_sessions`, `relative_loss`, and `tail_probability` are finite JSON numbers (`relative_loss` and `tail_probability` are in `[0,1]`); `baseline_model_version` is `gamma_poisson_lower_v1`; `baseline_scope` is `slot` or `all_hours`; `confirmation_count` is `2` or `3`. `slot_index` and `slot_mature_days` are non-null integers (`0..95` and at least `7`, respectively) only when `baseline_scope` is `slot`; otherwise both are JSON `null`.
`confirmation_window_size` is `2` or `3`; `confirmation_count` is a JSON integer in `2..confirmation_window_size`.

### `GET /api/v1/session-drops/{id}`

`id` is a required positive decimal ID path segment. A successful response is one closed object containing a `source` detail and the complete retained immutable attempt lineage in its `attempts` array. The array contains every retained linked attempt, in ascending `attempt_number` order, with no pagination; retention may make it empty and its fixed per-source cap means it contains at most 100 attempts. Root and retry creation both enforce this cap transactionally and return `409` / `attempt_limit_reached` when 100 retained linked attempts exist, as specified by the investigation attempt-creation endpoints in [dashboard-investigations.md](dashboard-investigations.md). The detail source is the list summary's closed object with these additional required members:

```json
{
  "source": {
    "id": "42",
    "source_kind": "session_drop",
    "registered_host": "authorized-source-only",
    "confirmed_at": "2026-09-26T16:31:20.000Z",
    "classification": "unexplained",
    "investigation_eligible": true,
    "observed_total_sessions": 12,
    "reference_total_sessions": 48,
    "expected_total_sessions": 46.5,
    "absolute_loss_sessions": 36,
    "relative_loss": 0.75,
    "tail_probability": 0.00002,
    "baseline_model_version": "gamma_poisson_lower_v1",
    "baseline_scope": "slot",
    "slot_index": 65,
    "slot_mature_days": 7,
    "confirmation_count": 2,
    "confirmation_window_size": 3,
    "confirmation_started_at": "2026-09-26T16:01:20.123Z",
    "confirmation_ended_at": "2026-09-26T16:31:20.123Z",
    "confirmation_flags": [true, false, true],
    "freshness_context": "fresh",
    "drain_context": "none",
    "confirmation_ended_report_epoch_ms": "1790430680000"
  },
  "attempts": [
    {
      "attempt_id": "91",
      "attempt_number": 1,
      "initiation": "automatic",
      "retry_of_attempt_id": null,
      "state": "completed",
      "created_at": "2026-09-26T16:31:20.000Z",
      "started_at": "2026-09-26T16:31:21.000Z",
      "send_authorized_at": "2026-09-26T16:31:21.100Z",
      "send_completed_at": "2026-09-26T16:31:21.900Z",
      "completed_at": "2026-09-26T16:31:22.000Z",
      "terminal_reason": "",
      "evidence_version": 1,
      "omission_codes": []
    }
  ]
}
```

`confirmation_window_size` is `2` or `3`. `confirmation_flags` contains exactly `confirmation_window_size` Boolean values in chronological confirmation-window order; its first two elements are always present, and its third element is present only when `confirmation_window_size` is `3`. Its count of `true` values equals `confirmation_count`, which is in `2..confirmation_window_size`. `freshness_context` is always `fresh`; `drain_context` is `none`, `overlap`, `post_horizon`, or `unknown`. `confirmation_ended_report_epoch_ms` is a decimal string for a non-negative UTC epoch millisecond. `confirmation_started_at` and `confirmation_ended_at` are RFC 3339 timestamps that project the durable central acceptance times exactly. Every retained linked attempt appears exactly once in `attempts`; each closed immutable attempt summary has the same field meanings and nullability as the source-history summary. `send_authorized_at` and `send_completed_at` are nullable independently of state: a `running` attempt may have neither timestamp. `send_authorized_at` means a transmission MAY have begun; `send_completed_at` means only that the local bounded HTTP exchange returned. `terminal_reason` uses only this canonical enum: ``, `authentication_failed`, `configuration_disabled`, `configuration_invalid`, `evidence_unavailable`, `interrupted`, `network_error`, `provider_rate_limited`, `provider_request_rejected`, `provider_refused`, `redirect_refused`, `request_limit`, `response_incomplete`, `response_invalid`, `response_limit`, `storage_unavailable`, `timeout`, or `upstream_error`. It is empty for queued, running, completed, and provider-validated insufficient reports; is `evidence_unavailable` only for local pre-send insufficient evidence; and otherwise is a non-empty failed-row reason.

Malformed `limit`, `before`, or `{id}` returns `400` / `invalid_request`. An authorized well-formed unknown or expired source ID returns `404` / `source_not_found`. A source-list read failure returns `503` / `session_drop_list_unavailable`; a source/detail or linked-attempt-summary read failure returns `503` / `session_drop_detail_unavailable`. Every error body is exactly `{"error":{"code":"<safe_code>"}}`. `registered_host`, `confirmation_ended_report_epoch_ms`, `confirmation_started_at`, and `confirmation_ended_at` are permitted only in these deterministic source/list/detail projections after authorization from the source row. They MUST NOT be added to generic attempt/history responses, OpenAI artifacts, SSE, or browser-persisted state.

## 9. SSE event

After a newly inserted durable source commits, the session-authenticated SSE broker emits exactly one additive event using the existing transport framing: one literal `data:` line containing exactly the existing JSON envelope `{type,host?,data,timestamp}`, followed by the SSE record terminator. It emits no named `event:` line. For this event the envelope has no `host` member:

```text
data: {"type":"session_drop","data":{"schema_version":1,"source_kind":"session_drop","source_id":"42","confirmed_at":"2026-09-26T16:31:20.000Z","classification":"unexplained","investigation_eligible":true,"confirmation_count":2},"timestamp":"2026-09-26T16:31:20.000Z"}

```

The event is intentionally host-free: it MUST NOT contain `registered_host`, any hostname/FQDN/domain/IP/customer identifier, accepted-report ID (none exists), report epoch, acceptance time, counts, evidence, OpenAI reasoning report/provenance, diagnostics, or free text. Old browsers ignore the unknown `session_drop` type. SSE is notification of durable change, not the source-of-record; clients use the authenticated list/detail API to resolve authorized host identity and source timing.

## 10. Compatibility and non-actions

- A confirmed session drop does not send an existing or new notification, webhook, email, ntfy message, command, drain, restart, remediation, or OpenAI Responses request by itself.
- Only an `unexplained` source can enter the separately configured opt-in `openai_responses` investigation lifecycle. Disabled, unavailable, or unacknowledged OpenAI configuration never changes scoring, persistence, classification, or SSE behavior.
- No field is added to `SpikePayload`, `CheckResult`, existing evtspike REST/SSE payloads, or existing notification payloads. Mixed-version agents continue their current report/spike contracts; old browsers ignore this additive SSE type; an older service may ignore v4 records without deleting them.
