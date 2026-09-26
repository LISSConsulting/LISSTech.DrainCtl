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
| **Confirmation horizon** | The trailing three consecutive eligible numeric observations for one registered host. It is the only window used for fixed 2-of-3 confirmation and for the post-drain horizon. A gap clears it. |
| **Session-drop source** | A durable `session_drop_anomalies` row. Its investigation link is exactly `(source_kind: "session_drop", source_id: <positive decimal string encoding the durable anomaly ID>)`. |

The anomaly ID is a SQLite integer-backed durable ID, encoded in JSON and path parameters as a positive decimal string. Creation is idempotent: the transaction uniquely claims `(canonical_registered_host, confirmation_ended_report_epoch_ms)`. Re-delivery of the same observation returns the existing source and does not emit a second event. Investigation attempts retain only `source_kind` and `source_id`; they do not retain the registered host.

## 2. Observation and `TotalSessions` rules

For a successful enumeration only,

```
TotalSessions = ActiveSessions + DisconnectedSessions
```

Both operands MUST be present, finite integral counts in `[0, 2^31-1]`; the sum MUST not overflow that range. `TotalSessions == 0` is a valid numeric observation and follows normal scoring, confirmation, drain, and cooldown rules. A nil enumeration is not an empty result and is always an unknown gap.

Before score/update, one per-host serial SQLite transaction MUST:

1. require `report_epoch_ms`; otherwise record a `missing_report_epoch` gap;
2. compare the observation identity's epoch with the persisted order watermark: an equal epoch is a `duplicate_report_epoch` gap, and a lower epoch is an `out_of_order` gap; neither moves the watermark;
3. require successful valid enumeration and `fresh` freshness evaluated with the separately captured central `accepted_at_ms`; otherwise record the applicable unknown gap; and
4. atomically record the observation identity/order watermark, score the numeric observation once, update detector state, and, if confirmed outside cooldown, insert the source anomaly.

A gap clears all three confirmation positions and records its safe gap reason (`nil_enumeration`, `invalid_enumeration`, `missing_report_epoch`, `duplicate_report_epoch`, `out_of_order`, `stale`, or `freshness_unknown`). It MUST NOT train either baseline, update the reference observation, create an anomaly, or be converted to `0`. Gaps do not advance the numeric order watermark except that a valid, in-order report does so in the transaction that scores it.

## 3. Baseline, slots, maturity, and decay

The detector owns 96 quarter-hour slots per registered host. For the source-local timestamp of an eligible report, its slot is:

```
slot = local_hour * 4 + floor(local_minute / 15)     // 0..95
```

The source-local offset and local calendar date are captured with the accepted observation, so slot/date assignment is replayable across DST and service restart. Each registered host also has one all-hours fallback baseline, with no slot index. It is trained only from that host's eligible normal observations and is never pooled across hosts or carried in provider-facing data.

Each slot and all-hours fallback uses the versioned `gamma_poisson_lower_v1` model. Its persisted sufficient statistics are `alpha`, `beta`, `last_updated_at`, and the set of distinct local eligible calendar days trained in that baseline. Before a score or normal update at `t`, decay each sufficient statistic's learned mass toward its fixed prior `(alpha=1, beta=1)` by:

```
d = exp(-(t - last_updated_at) / baseline_half_life)
alpha = 1 + (alpha - 1) * d
beta  = 1 + (beta  - 1) * d
```

A normal observation `x` updates both its selected host slot and that host's all-hours fallback as `alpha += x; beta += 1`, then records its local calendar day. The lower predictive-tail probability is the Gamma-Poisson/negative-binomial inclusive CDF `P(Y <= x | alpha, beta)` evaluated before that update. Implementations MUST use stable monotone numeric evaluation and persist the model version with detector state and an anomaly's baseline summary.

A host slot is mature after normal training on at least seven distinct source-local eligible calendar days. The host's all-hours fallback is usable only after at least 20 eligible normal observations spanning at least 24 hours from its first to its most recent such observation. Before local slot maturity, score with that host's usable all-hours fallback; otherwise the observation is warm-up/no-candidate. A mature local slot is always preferred over its host's all-hours fallback. Neither readiness condition is inferred from candidates, drains, post-drain observations, gaps, report count other than the fallback's 20 eligible normal observations, elapsed wall time other than the fallback's 24-hour span, or observations from another host.

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

Append one Boolean candidate flag for each eligible numeric observation to the per-host confirmation window; normal/warm-up observations append `false`. Keep only the trailing three flags. A confirmation occurs exactly when the window contains at least two `true` flags. It may occur on the second or third eligible report in the window. A gap clears the window. After any confirmation, clear the window so the same observations cannot confirm a second source.

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

| Input/condition | Numeric score | Confirmation window | Baseline/reference | Durable source | Classification / provider eligibility |
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
| Candidate confirmation, known non-drain context, outside cooldown | Yes | Confirm then clear | No train/reference update | Yes, once | `unexplained`; provider eligible |
| Candidate confirmation with non-`AllowAll` overlap or post-drain horizon | Yes | Confirm then clear | No train/reference update | Yes, once | `drain_associated`; not provider eligible |
| Candidate confirmation with unavailable drain/classification context and no known drain | Yes | Confirm then clear | No train/reference update | Yes, once | `unknown_context`; not provider eligible |
| Confirmation during cooldown | Yes | Confirm then clear | No train/reference update | No | No duplicate event/source |

## 7. Durable data and retention boundary

`session_drop_anomalies` is a deterministic source table and MAY contain the canonical registered host. Each row persists: integer-backed `id`, encoded in JSON and path parameters as a positive decimal string; canonical registered host; `confirmation_ended_report_epoch_ms`; `accepted_at_ms`; `detected_at_ms`; `confirmation_started_at_ms` and `confirmation_ended_at_ms`; observed, reference, and expected totals; `absolute_loss` and `relative_loss`; `baseline_scope` (`slot` or `all_hours`); `slot_index` when `baseline_scope` is `slot`; `slot_mature_days`; model version and tail probability; confirmation flags/count; `freshness_context`; `drain_context` (`none`, `overlap`, `post_horizon`, or `unknown`); `classification`; and `provider_eligible`. The transaction retains `confirmation_ended_report_epoch_ms`…

Detector state tables MAY contain the canonical host only where necessary for that host's 96 slots and all-hours fallback, report-epoch order watermark, confirmation flags, reference total, drain horizon, cooldown, and freshness acceptance time. Both baseline types are host-keyed and MUST be trained and scored only from that host's observations. These deterministic records are not provider evidence.

Session-drop sources are retained independently for `AuditDays` measured from each row's `detected_at`; expiring a source MUST NOT retain it because a linked attempt exists or alter an attempt's independently `created_at`-anchored retention. Investigation attempt rows, sanitized evidence snapshots, results, provenance, provider diagnostics, attempt SSE, and browser-local durable state MUST contain only `source_kind: "session_drop"` and the integer-backed `source_id` encoded as a positive decimal string; they MUST NOT copy the registered host, FQDN, domain, IP, customer identity, report epoch, acceptance time, or per-session data. Evidence, result, and provenance retention cascade only from their own attempt. Baselines persist only while their canonical host remains registered and MUST be deleted when t…

## 8. Authenticated REST API

These additive endpoints require an authenticated dashboard-group session before route lookup. Machine-account routes, including `/api/v1/config`, have no equivalent endpoint. Unauthorized requests return `401`/`403` before source lookup and disclose neither host nor source existence.

### `GET /api/v1/session-drops?limit={1..200}&before={positive_decimal_id}`

Returns newest-first deterministic source summaries. `before` is an exclusive positive-decimal-string ID cursor; absent `limit` defaults to `50`.

```json
{
  "items": [
    {
      "id": "42",
      "source_kind": "session_drop",
      "registered_host": "authorized-source-only",
      "confirmed_at": "2026-09-26T16:31:20Z",
      "classification": "unexplained",
      "investigation_eligible": true,
      "observed_total_sessions": 12,
      "reference_total_sessions": 48,
      "expected_total_sessions": 46.5,
      "tail_probability": 0.00002,
      "baseline_scope": "slot",
      "slot_index": 65,
      "confirmation_count": 2
    }
  ],
  "next_before": "42"
}
```

### `GET /api/v1/session-drops/{id}`

Returns the same authorized source fields plus `baseline_scope` (`slot` or `all_hours`), model, `slot_index` when the scope is `slot`, safe freshness and drain context, confirmation timestamps/flags, and the source's `confirmation_ended_report_epoch_ms` and `accepted_at_ms`. It MAY return ordered investigation attempt summaries linked by `source_kind`/`source_id`, but those summaries remain host-free and contain no evidence, provider result, provenance, diagnostics, report epoch, or acceptance time. Unknown IDs return `404` only after authorization.

`registered_host`, `confirmation_ended_report_epoch_ms`, and `accepted_at_ms` are permitted only in these deterministic source/list/detail projections after authorization from the source row. They MUST NOT be added to generic attempt/history responses, provider artifacts, or browser-persisted state.

## 9. SSE event

After a newly inserted durable source commits, the session-authenticated SSE broker emits exactly one additive event:

```json
{
  "type": "session_drop",
  "timestamp": "2026-09-26T16:31:20Z",
  "data": {
    "schema_version": 1,
    "source_kind": "session_drop",
    "source_id": "42",
    "confirmed_at": "2026-09-26T16:31:20Z",
    "classification": "unexplained",
    "investigation_eligible": true,
    "confirmation_count": 2
  }
}
```

The event is intentionally host-free: it MUST NOT contain `registered_host`, any hostname/FQDN/domain/IP/customer identifier, accepted-report ID (none exists), report epoch, acceptance time, counts, evidence, provider result/provenance, diagnostics, or free text. Old browsers ignore the unknown `session_drop` type. SSE is notification of durable change, not the source-of-record; clients use the authenticated list/detail API to resolve authorized host identity and source timing.

## 10. Compatibility and non-actions

- A confirmed session drop does not send an existing or new notification, webhook, email, ntfy message, command, drain, restart, remediation, or provider request by itself.
- Only an `unexplained` source can enter the separately configured opt-in investigation lifecycle. Disabled, unavailable, or unacknowledged provider configuration never changes scoring, persistence, classification, or SSE behavior.
- No field is added to `SpikePayload`, `CheckResult`, existing evtspike REST/SSE payloads, or existing notification payloads. Mixed-version agents continue their current report/spike contracts; old browsers ignore this additive SSE type; an older service may ignore v4 records without deleting them.
