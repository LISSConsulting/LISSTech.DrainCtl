# Data Model: AI Anomaly Investigation

**Feature**: [013-ai-anomaly-investigation](./spec.md)
**Companion decisions**: [plan](./plan.md), [research](./research.md)

This model is owned by the central Windows dashboard/service. All timestamps ending in `_ms` are UTC Unix epoch milliseconds. SQLite uses the existing WAL database, single writer, bounded readers, and audit-capable writer path. New schema is additive **v4**; it does not alter `event_spikes`, report payloads, agent configuration, or existing settings projections.

## Privacy and identity boundary

`event_spikes.host` and `session_drop_anomalies.host` are the canonical registered-host identities. They are deterministic source facts and may be resolved **after dashboard-group session authorization** by source/list/detail projections only.

An investigation is not a host-owned copy. Every attempt and every provider-bound/provider-derived artifact identifies its source exclusively by this pair:

| Field | Type | Allowed values |
|---|---|---|
| `source_kind` | `TEXT` | `event_spike`, `session_drop` |
| `source_id` | `INTEGER` | positive durable ID in the corresponding source table |

The following stores and payloads MUST NOT contain a canonical host, FQDN, domain, IP address, customer identifier, username, session identifier, `changed_by`, free text, URL, path, generic/raw structure, Event Log content/XML, dump, credential, notification secret, or file content: `investigation_attempts`, evidence, result, provenance, safe failure/diagnostic fields, provider request/response handling, SSE, and browser-local durable state. An attempt projection therefore exposes the stable pair, never a copied host. Polymorphic source integrity is checked by the owning store in the source-authorization/creation transaction; SQLite cannot express a foreign key across the two source tables.

## Conceptual entities

| Entity | Identity and ownership | Purpose |
|---|---|---|
| Deterministic source anomaly | Existing `event_spikes.id`, or `session_drop_anomalies.id` | Owns source facts and canonical registered host. Detection remains independent from investigation. |
| Investigation source aggregate | `(source_kind, source_id)`; conceptual only | Immutable ordered attempt history; deliberately no mutable parent row or host copy. |
| Investigation attempt | `attempt_id` and `(source_kind, source_id, attempt_no)` | One immutable evidence snapshot and at most one wire transmission. |
| Sanitized evidence snapshot | exactly one per attempt | Versioned, field-by-field allowlist DTO persisted before a send. |
| Typed result | zero or one per attempt | Strict four-answer hypothesis, never provider prose. |
| Provider provenance | zero or one per attempt | Non-secret facts about the one attempted transmission and response validation. |
| Session-drop anomaly | `session_drop_anomalies.id` | Durable confirmed lower-tail source with canonical host, classification, and provider eligibility. |
| Session baseline/detector state | host plus scope/slot; host detector state | Restart-safe baseline, ordering, confirmation, drain horizon, and cooldown state; never provider evidence. |
| Provider configuration/status | `config.json` / derived runtime state | Write-only protected credential configuration and safe current operating status; not machine configuration. |

## SQLite v4

### 1. Investigation attempts

```sql
CREATE TABLE investigation_attempts (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    source_kind           TEXT    NOT NULL CHECK (source_kind IN ('event_spike','session_drop')),
    source_id             INTEGER NOT NULL CHECK (source_id > 0),
    attempt_no            INTEGER NOT NULL CHECK (attempt_no > 0),
    initiation            TEXT    NOT NULL CHECK (initiation IN ('automatic','manual','retry')),
    retry_of_attempt_id   INTEGER,
    state                 TEXT    NOT NULL CHECK (state IN ('queued','running','completed','insufficient_evidence','failed')),
    created_at_ms         INTEGER NOT NULL,
    queued_at_ms          INTEGER NOT NULL,
    started_at_ms         INTEGER,
    finished_at_ms        INTEGER,
    terminal_reason       TEXT    NOT NULL DEFAULT '' CHECK (terminal_reason IN (
        '', 'authentication_failed', 'configuration_disabled', 'configuration_invalid',
        'evidence_unavailable', 'interrupted', 'network_error', 'provider_rate_limited',
        'redirect_refused', 'request_limit', 'response_invalid', 'response_limit',
        'storage_unavailable', 'timeout', 'upstream_error'
    )),
    evidence_hash         BLOB    NOT NULL CHECK (length(evidence_hash) = 32),
    CHECK ((retry_of_attempt_id IS NULL AND initiation IN ('automatic','manual') AND attempt_no = 1)
        OR (retry_of_attempt_id IS NOT NULL AND initiation = 'retry' AND attempt_no > 1)),
    CHECK ((state = 'queued' AND started_at_ms IS NULL AND finished_at_ms IS NULL AND terminal_reason = '')
        OR (state = 'running' AND started_at_ms IS NOT NULL AND finished_at_ms IS NULL AND terminal_reason = '')
        OR (state IN ('completed','insufficient_evidence','failed') AND started_at_ms IS NOT NULL AND finished_at_ms IS NOT NULL)),
    CHECK ((state = 'completed' AND terminal_reason = '')
        OR (state = 'insufficient_evidence' AND terminal_reason = '')
        OR (state = 'failed' AND terminal_reason <> '')
        OR state IN ('queued','running')),
    UNIQUE (source_kind, source_id, attempt_no),
    UNIQUE (id, source_kind, source_id)
);

CREATE UNIQUE INDEX investigation_attempts_one_root
    ON investigation_attempts(source_kind, source_id)
    WHERE retry_of_attempt_id IS NULL;
CREATE UNIQUE INDEX investigation_attempts_one_child_retry
    ON investigation_attempts(retry_of_attempt_id)
    WHERE retry_of_attempt_id IS NOT NULL;
CREATE INDEX investigation_attempts_queue
    ON investigation_attempts(queued_at_ms, id) WHERE state = 'queued';
CREATE INDEX investigation_attempts_source_history
    ON investigation_attempts(source_kind, source_id, attempt_no);
CREATE INDEX investigation_attempts_created_at
    ON investigation_attempts(created_at_ms);
CREATE TRIGGER investigation_attempts_terminal_immutable
BEFORE UPDATE ON investigation_attempts
WHEN OLD.state IN ('completed','insufficient_evidence','failed')
BEGIN
    SELECT RAISE(ABORT, 'terminal investigation attempt is immutable');
END;
```

`attempt_no = 1` is the only root; retries are sequentially assigned `max(attempt_no)+1` for that source. `retry_of_attempt_id` is an opaque predecessor ID: the store validates that it names a terminal failed or insufficient-evidence attempt in the same source aggregate when the retry is created, but deliberately does not make it a foreign key. Thus retention can expire a predecessor without retaining it because a later retry exists. The child-retry uniqueness makes the lineage a single ordered chain while both rows are retained. The store rejects a retry of a completed, queued, or running attempt. Automatic creation is permitted only for the root. A manual root is permitted only when no row exists. A queued/running duplicate returns that attempt ID; a completed source creates nothing.

`terminal_reason` is a safe closed attempt-failure enum, not an HTTP message, provider prose, or diagnostic payload. `insufficient_evidence` is not a failure and has an empty reason. It may be finalized locally before a provider send, in which case `started_at_ms` is the worker's durable finalization time, result and provenance are absent, and the evidence row is still required. The terminal-immutability trigger rejects updates to terminal rows; the store never deletes or overwrites a terminal record outside retention.

### 2. Sanitized evidence, result, and provenance

```sql
CREATE TABLE investigation_evidence (
    attempt_id       INTEGER PRIMARY KEY,
    dto_version      INTEGER NOT NULL CHECK (dto_version = 1),
    source_time_ms   INTEGER NOT NULL,
    snapshot_at_ms   INTEGER NOT NULL,
    from_ms          INTEGER NOT NULL,
    to_ms            INTEGER NOT NULL,
    state_json       TEXT    NOT NULL CHECK (
        json_valid(state_json) AND json_type(state_json) = 'object'
        AND length(CAST(state_json AS BLOB)) <= 8000
    ),
    omissions_json   TEXT    NOT NULL CHECK (json_valid(omissions_json) AND json_type(omissions_json) = 'array' AND length(CAST(omissions_json AS BLOB)) <= 2048),
    FOREIGN KEY (attempt_id) REFERENCES investigation_attempts(id) ON DELETE CASCADE,
    CHECK (snapshot_at_ms >= source_time_ms),
    CHECK (from_ms = source_time_ms - 1800000),
    CHECK (to_ms = CASE
        WHEN source_time_ms + 1800000 <= snapshot_at_ms THEN source_time_ms + 1800000
        ELSE snapshot_at_ms
    END),
    CHECK (to_ms >= from_ms AND to_ms - from_ms <= 3600000)
);

CREATE TABLE investigation_results (
    attempt_id                              INTEGER PRIMARY KEY,
    likely_cause                            TEXT    CHECK (likely_cause IN ('resource_pressure','identity_or_authentication','service_or_os_failure','network_or_dependency','planned_drain_or_maintenance','fleet_correlated_event','unknown')),
    likely_cause_probabilities_json         TEXT    CHECK (likely_cause_probabilities_json IS NULL OR (json_valid(likely_cause_probabilities_json) AND json_type(likely_cause_probabilities_json) = 'object' AND length(CAST(likely_cause_probabilities_json AS BLOB)) <= 1024)),
    likely_cause_confidence_ppm             INTEGER CHECK (likely_cause_confidence_ppm BETWEEN 0 AND 1000000),
    impact                                  TEXT    CHECK (impact IN ('low','moderate','high','critical','unknown')),
    impact_probabilities_json                TEXT    CHECK (impact_probabilities_json IS NULL OR (json_valid(impact_probabilities_json) AND json_type(impact_probabilities_json) = 'object' AND length(CAST(impact_probabilities_json AS BLOB)) <= 1024)),
    impact_confidence_ppm                    INTEGER CHECK (impact_confidence_ppm BETWEEN 0 AND 1000000),
    evidence_sufficiency                    TEXT    NOT NULL CHECK (evidence_sufficiency IN ('insufficient','partial','sufficient')),
    evidence_sufficiency_probabilities_json TEXT    NOT NULL CHECK (json_valid(evidence_sufficiency_probabilities_json) AND json_type(evidence_sufficiency_probabilities_json) = 'object' AND length(CAST(evidence_sufficiency_probabilities_json AS BLOB)) <= 1024),
    evidence_sufficiency_confidence_ppm     INTEGER NOT NULL CHECK (evidence_sufficiency_confidence_ppm BETWEEN 0 AND 1000000),
    human_review                            TEXT    NOT NULL CHECK (human_review IN ('required','not_required')),
    human_review_probabilities_json          TEXT    NOT NULL CHECK (json_valid(human_review_probabilities_json) AND json_type(human_review_probabilities_json) = 'object' AND length(CAST(human_review_probabilities_json AS BLOB)) <= 1024),
    human_review_confidence_ppm              INTEGER NOT NULL CHECK (human_review_confidence_ppm BETWEEN 0 AND 1000000),
    FOREIGN KEY (attempt_id) REFERENCES investigation_attempts(id) ON DELETE CASCADE,
    CHECK ((evidence_sufficiency = 'insufficient'
            AND likely_cause IS NULL AND likely_cause_probabilities_json IS NULL AND likely_cause_confidence_ppm IS NULL
            AND impact IS NULL AND impact_probabilities_json IS NULL AND impact_confidence_ppm IS NULL)
        OR (evidence_sufficiency IN ('partial','sufficient')
            AND likely_cause IS NOT NULL AND likely_cause_probabilities_json IS NOT NULL AND likely_cause_confidence_ppm IS NOT NULL
            AND impact IS NOT NULL AND impact_probabilities_json IS NOT NULL AND impact_confidence_ppm IS NOT NULL))
);

CREATE TABLE investigation_provenance (
    attempt_id                INTEGER PRIMARY KEY,
    provider_profile          TEXT    NOT NULL CHECK (provider_profile = 'typesafe_jev'),
    requested_model           TEXT    NOT NULL CHECK (requested_model = 'jev-latest'),
    request_started_at_ms     INTEGER NOT NULL,
    request_completed_at_ms   INTEGER NOT NULL CHECK (request_completed_at_ms >= request_started_at_ms),
    request_header_bytes      INTEGER NOT NULL CHECK (request_header_bytes > 0 AND request_header_bytes <= 16384),
    request_body_bytes        INTEGER NOT NULL CHECK (request_body_bytes > 0 AND request_body_bytes <= 16384),
    response_header_bytes     INTEGER NOT NULL CHECK (response_header_bytes > 0 AND response_header_bytes <= 16384),
    response_body_bytes       INTEGER NOT NULL CHECK (response_body_bytes > 0 AND response_body_bytes <= 32768),
    validation_outcome        TEXT    NOT NULL CHECK (validation_outcome IN ('accepted','insufficient_evidence')),
    FOREIGN KEY (attempt_id) REFERENCES investigation_attempts(id) ON DELETE CASCADE
);
```

`investigation_provenance` is a closed local record, not a provider echo. A row exists only after a valid decoded provider response and contains exactly `provider_profile=typesafe_jev`, `requested_model=jev-latest`, `request_started_at_ms`, `request_completed_at_ms`, `request_header_bytes`, `request_body_bytes`, `response_header_bytes`, `response_body_bytes`, and `validation_outcome=accepted|insufficient_evidence`. The timestamps and all byte counts are locally measured bounded integers. Header counts include the emitted/received field syntax and final CRLF; request body counts are bounded to 16,384 UTF-8 JSON bytes, response header counts to 16,384 bytes, and response body counts to 32,768 decompressed bytes. The response `model` is bounded solely to validate response shape and is then discarded. `accepted` records a completed response and `insufficient_evidence` a valid response whose evidence-sufficiency choice is insufficient. Failed, rejected, malformed, and local pre-send insufficient outcomes have no provenance row. No provider-supplied identifier, endpoint/version string, raw response body, or prose is persisted, projected, or logged.

`state_json` is the exact persisted provider state object, serialized compactly and deterministically—not a JSON string containing JSON, a generic map, or an unbounded request capture. Its outer object-key order is exactly `v`, `snapshot_at_ms`, `window`, `source`, `context`, optional `local`, optional `fleet`, `omitted`; nested object and point keys use the corresponding fixed provider-contract order. `source_time_ms` is durable evidence-table metadata only: it is not a `state_json` key. The builder requires `snapshot_at_ms >= source_time_ms` and derives the exact source-relative window `window.from_ms = source_time_ms - 1,800,000` and `window.to_ms = min(source_time_ms + 1,800,000, snapshot_at_ms)`; its span is at most 3,600,000 milliseconds. `omissions_json` and the state object's required `omitted` array contain only the single closed enum sequence `pre_upgrade_context`, `retention_expired`, `freshness_unavailable`, `drain_unavailable`, `detector_unavailable`, `local_points`, `fleet_points`, `local_aggregate`, `fleet_aggregate`: every applicable value appears at most once and in that displayed order. The builder removes only permitted trailing optional units in the fixed provider order until state is at most 8,000 bytes and the complete request is at most 16 KiB. The DTO substitutes `source` and `peer_n`; channel is a recognized code or `custom_channel`.

A result is inserted only after the decoder verifies exactly one `choice` answer, its closed bounded full probability map, and separate confidence for each fixed question: `likely_cause`, `impact`, `evidence_sufficiency`, and `human_review`. The decoder validates every probability-map key against that question's complete enum, finite exact-decimal probabilities and confidence in `[0,1]`, and a probability sum within `0.000001` of one; it converts each independently to parts per million (`0..1000000`) by decimal half-up rounding. It rejects `score`, `noul`, dynamic/fifth/duplicate/missing questions, prose, unsupported enums, and unbounded maps. When provider evidence sufficiency is `insufficient`, all four answers are validated, but cause and impact choices, maps, and confidences are not persisted or displayed; only sufficiency and human-review data remain. A locally pre-send insufficient attempt has no result or provenance. Provenance is written only for a wire attempt and contains no request header values, response body, response headers, or error content.

### 3. Session-drop source anomalies

```sql
CREATE TABLE session_drop_anomalies (
    id                                     INTEGER PRIMARY KEY AUTOINCREMENT,
    host                                   TEXT    NOT NULL,
    report_epoch_ms                        INTEGER NOT NULL,
    accepted_at_ms                         INTEGER NOT NULL,
    local_offset_minutes                   INTEGER NOT NULL CHECK (local_offset_minutes BETWEEN -840 AND 840),
    local_date                             TEXT    NOT NULL CHECK (length(local_date) = 10),
    detected_at_ms                         INTEGER NOT NULL,
    confirmation_started_report_epoch_ms   INTEGER NOT NULL,
    confirmation_ended_report_epoch_ms     INTEGER NOT NULL,
    observed_sessions                      INTEGER NOT NULL CHECK (observed_sessions >= 0),
    reference_sessions                     INTEGER NOT NULL CHECK (reference_sessions >= 0),
    expected_sessions                      REAL    NOT NULL CHECK (expected_sessions >= 0),
    baseline_model_version                 TEXT    NOT NULL CHECK (baseline_model_version = 'gamma_poisson_lower_v1'),
    baseline_scope                         TEXT    NOT NULL CHECK (baseline_scope IN ('slot','all_hours')),
    slot_index                             INTEGER CHECK (slot_index BETWEEN 0 AND 95),
    slot_mature_days                       INTEGER CHECK (slot_mature_days >= 7),
    tail_probability                       REAL    NOT NULL CHECK (tail_probability >= 0.0 AND tail_probability <= 1.0),
    absolute_loss                          INTEGER NOT NULL CHECK (absolute_loss >= 0),
    relative_loss                          REAL    NOT NULL CHECK (relative_loss >= 0.0 AND relative_loss <= 1.0),
    confirmation_1_candidate               INTEGER NOT NULL CHECK (confirmation_1_candidate IN (0,1)),
    confirmation_2_candidate               INTEGER NOT NULL CHECK (confirmation_2_candidate IN (0,1)),
    confirmation_3_candidate               INTEGER NOT NULL CHECK (confirmation_3_candidate IN (0,1)),
    confirmation_count                     INTEGER NOT NULL CHECK (confirmation_count BETWEEN 2 AND 3),
    freshness_context                      TEXT    NOT NULL CHECK (freshness_context = 'fresh'),
    drain_context                          TEXT    NOT NULL CHECK (drain_context IN ('none','overlap','post_horizon','unknown')),
    classification                         TEXT    NOT NULL CHECK (classification IN ('unexplained','drain_associated','unknown_context')),
    provider_eligible                      INTEGER NOT NULL CHECK (provider_eligible IN (0,1)),
    CHECK ((baseline_scope = 'slot' AND slot_index IS NOT NULL AND slot_mature_days IS NOT NULL)
        OR (baseline_scope = 'all_hours' AND slot_index IS NULL AND slot_mature_days IS NULL)),
    CHECK (confirmation_started_report_epoch_ms <= confirmation_ended_report_epoch_ms),
    CHECK (report_epoch_ms = confirmation_ended_report_epoch_ms),
    CHECK ((classification = 'unexplained' AND provider_eligible = 1 AND drain_context = 'none')
        OR (classification = 'drain_associated' AND provider_eligible = 0 AND drain_context IN ('overlap','post_horizon'))
        OR (classification = 'unknown_context' AND provider_eligible = 0 AND drain_context = 'unknown')),
    UNIQUE (host, confirmation_ended_report_epoch_ms)
);

CREATE INDEX session_drop_anomalies_host_time
    ON session_drop_anomalies(host, detected_at_ms DESC, id DESC);
CREATE INDEX session_drop_anomalies_eligible_time
    ON session_drop_anomalies(provider_eligible, detected_at_ms DESC, id DESC);
```

This is the only new source table that retains a canonical registered host. A detector observation is identified and ordered by `(host, report_epoch_ms)`, where `report_epoch_ms` is the existing `CheckResult` timestamp; no accepted-report ID is added to the wire contract or persisted. `accepted_at_ms` is the central service acceptance time used for freshness context, not a source identity or ordering value. A confirmed drop is deterministically identified by `(host, confirmation_ended_report_epoch_ms)` and is stored whether it is unexplained, drain-associated, or unknown-context; only the first is investigation-eligible. Nil/invalid/unavailable/stale/out-of-order reports are gaps and create **no** anomaly row. A valid numeric zero is retained as `observed_sessions = 0`.

### 4. Baselines and detector state

```sql
CREATE TABLE session_drop_baselines (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    host                  TEXT    NOT NULL CHECK (length(host) > 0),
    scope                 TEXT    NOT NULL CHECK (scope IN ('slot','all_hours')),
    slot_index            INTEGER CHECK (slot_index BETWEEN 0 AND 95),
    model_version         TEXT    NOT NULL CHECK (model_version = 'gamma_poisson_lower_v1'),
    alpha                 REAL    NOT NULL CHECK (alpha >= 1.0),
    beta                  REAL    NOT NULL CHECK (beta >= 1.0),
    observation_count     INTEGER NOT NULL DEFAULT 0 CHECK (observation_count >= 0),
    first_trained_at_ms   INTEGER,
    last_updated_at_ms    INTEGER NOT NULL,
    CHECK ((scope = 'slot' AND slot_index IS NOT NULL)
        OR (scope = 'all_hours' AND slot_index IS NULL)),
    CHECK ((observation_count = 0 AND first_trained_at_ms IS NULL)
        OR (observation_count > 0 AND first_trained_at_ms IS NOT NULL))
);

CREATE UNIQUE INDEX session_drop_baselines_host_slot
    ON session_drop_baselines(host, slot_index) WHERE scope = 'slot';
CREATE UNIQUE INDEX session_drop_baselines_host_all_hours
    ON session_drop_baselines(host) WHERE scope = 'all_hours';

CREATE TABLE session_drop_baseline_days (
    baseline_id INTEGER NOT NULL,
    local_date  TEXT    NOT NULL CHECK (length(local_date) = 10),
    PRIMARY KEY (baseline_id, local_date),
    FOREIGN KEY (baseline_id) REFERENCES session_drop_baselines(id) ON DELETE CASCADE
) WITHOUT ROWID;

CREATE TABLE session_drop_detector_state (
    host                          TEXT PRIMARY KEY,
    last_scored_report_epoch_ms   INTEGER NOT NULL DEFAULT 0,
    last_reference_total          INTEGER CHECK (last_reference_total IS NULL OR last_reference_total >= 0),
    cooldown_until_ms             INTEGER NOT NULL DEFAULT 0,
    post_drain_remaining          INTEGER NOT NULL DEFAULT 0 CHECK (post_drain_remaining BETWEEN 0 AND 3),
    confirmation_1_report_epoch_ms INTEGER,
    confirmation_1_candidate      INTEGER CHECK (confirmation_1_candidate IN (0,1)),
    confirmation_1_context        TEXT    CHECK (confirmation_1_context IN ('none','overlap','post_horizon','unknown')),
    confirmation_2_report_epoch_ms INTEGER,
    confirmation_2_candidate      INTEGER CHECK (confirmation_2_candidate IN (0,1)),
    confirmation_2_context        TEXT    CHECK (confirmation_2_context IN ('none','overlap','post_horizon','unknown')),
    confirmation_3_report_epoch_ms INTEGER,
    confirmation_3_candidate      INTEGER CHECK (confirmation_3_candidate IN (0,1)),
    confirmation_3_context        TEXT    CHECK (confirmation_3_context IN ('none','overlap','post_horizon','unknown')),
    last_gap_reason               TEXT    NOT NULL DEFAULT '' CHECK (last_gap_reason IN ('','nil_enumeration','invalid_enumeration','missing_report_epoch','duplicate_report_epoch','out_of_order','stale','freshness_unknown')),
    state_updated_at_ms           INTEGER NOT NULL,
    CHECK ((confirmation_1_report_epoch_ms IS NULL) = (confirmation_1_candidate IS NULL)
        AND (confirmation_1_report_epoch_ms IS NULL) = (confirmation_1_context IS NULL)),
    CHECK ((confirmation_2_report_epoch_ms IS NULL) = (confirmation_2_candidate IS NULL)
        AND (confirmation_2_report_epoch_ms IS NULL) = (confirmation_2_context IS NULL)),
    CHECK ((confirmation_3_report_epoch_ms IS NULL) = (confirmation_3_candidate IS NULL)
        AND (confirmation_3_report_epoch_ms IS NULL) = (confirmation_3_context IS NULL))
) WITHOUT ROWID;

CREATE INDEX session_drop_detector_cooldown
    ON session_drop_detector_state(cooldown_until_ms);
```

Each registered host has exactly 96 host-local quarter-hour slots (`local_hour * 4 + floor(local_minute / 15)`) plus one host-keyed all-hours fallback; there are no fleet-pooled or global baselines. A slot is mature only after seven distinct eligible local calendar days in `session_drop_baseline_days`. Until then, its own host's all-hours fallback is usable only after at least 20 eligible normal observations spanning at least 24 hours, determined from `observation_count` and `first_trained_at_ms`. If neither baseline is mature, the observation is warm-up only and is not scored.

Every slot and all-hours fallback is `gamma_poisson_lower_v1`, with sufficient statistics `alpha`, `beta`, `observation_count`, `first_trained_at_ms`, `last_updated_at_ms`, and the distinct-day rows for slots. Before score or normal training at time `t`, decay learned mass to prior `(1,1)` using `d = exp(-(t-last_updated_at)/baseline_half_life)`; `alpha = 1 + (alpha-1)*d`, `beta = 1 + (beta-1)*d`. Evaluate the inclusive Gamma-Poisson/negative-binomial lower CDF before updating. Every eligible normal observation trains both its host slot and its host all-hours fallback (`alpha += sessions`, `beta += 1`, increment `observation_count`, and set `first_trained_at_ms` on the first observation); only slot training records its local date. Candidate, confirmed, drain/horizon, unknown-context, and gap observations update neither baseline nor `last_reference_total`. The same anti-poisoning rule therefore applies to both baseline types.

`last_scored_report_epoch_ms` is the exactly-once/order watermark for the canonical host. The three fixed confirmation positions retain the report epoch, candidate flag, and drain/classification context for trailing eligible numeric observations; a gap clears them. In one scoring transaction, an epoch equal to the watermark is a duplicate, an epoch lower than the watermark is out-of-order, and neither changes the watermark. A strictly greater valid epoch atomically records the watermark, scoring result, baseline/state update, source insertion, and cooldown. Concurrent report intake therefore cannot score, train, or confirm the same `(host, report_epoch_ms)` observation twice.

## State transitions and validation

### Attempt lifecycle

```text
create evidence + queued row → queued → running → completed
                                        ├→ insufficient_evidence
                                        └→ failed
```

1. Authorization is checked before source lookup, evidence construction, or transaction entry.
2. For a root, source existence and eligibility are checked before evidence construction. For a retry, the store first loads and validates the retained terminal failed or insufficient-evidence predecessor after authorization, then re-resolves its deterministic source row before evidence construction or a new-attempt transaction. If that source row has expired or is otherwise absent, the retry returns `404 source_not_found`; it creates no row, reuses no stale evidence, and performs no provider work.
3. For an existing source, root/retry eligibility, source existence, attempt number, evidence snapshot, and queue capacity are checked in one write transaction. At most 100 rows in `queued` or `running` state may exist; a full queue is an API rejection and creates no attempt.
4. Evidence inserts with the queued attempt atomically. If this write fails, the service performs zero egress and emits only a structured local safe diagnostic; it writes a failed row only if a later database write succeeds.
5. The single worker claims FIFO by `(queued_at_ms, id)` transactionally. Immediately before first send, it validates enabled access, acknowledgement, and an available credential. Invalid/disabled configuration changes unsent rows to terminal `failed` with `configuration_disabled` or `configuration_invalid`; no bytes are sent.
6. The worker durably changes the claimed row to `running` before its one fixed-endpoint POST. One worker, 10 requests/minute with burst 2, and a 30-second request timeout are enforced in memory around the durable claim; worker rate limiting delays a queued attempt rather than failing it, and no HTTP retry policy is configured.
7. A valid strict response atomically inserts result, closed local provenance, and finalizes `completed` or `insufficient_evidence`. Its provider `model` is bounded for shape validation and discarded. A local/provider limit, transport, authentication, malformed output, or upstream outcome finalizes `failed` with one exact safe attempt-failure enum only and no provenance row. A durable terminal row is immutable.
8. Startup recovery changes only `running` rows to `failed/interrupted`; it never sends them again. Queued rows may be considered only after current-config validation. Retrying is an explicit operator action that creates a new linked attempt and a fresh snapshot.

### Session-drop detector lifecycle

A source report is eligible only if its existing `report_epoch_ms` is present, strictly greater than the canonical host's `last_scored_report_epoch_ms`, its enumeration is numeric and valid, and its host status is fresh at central `accepted_at_ms`. `TotalSessions = Active + Disconnected`; zero is numeric. Nil/invalid enumeration, missing epoch, an equal epoch (duplicate), a lower epoch (out-of-order), stale freshness, or unknown freshness is a closed-enum gap: it clears all three confirmation positions, does not train or replace `last_reference_total`, and creates no anomaly. A gap does not advance the numeric order watermark or add a report wire field.

For an eligible observation, select the mature host slot or that host's mature all-hours fallback and calculate the lower tail before an update. It is a candidate only if `P(Y <= observed) < lower_tail_threshold`, `last_reference_total - observed >= minimum_drop_sessions`, and `last_reference_total > 0` with relative loss meeting `minimum_drop_percent`. Warm-up and normal observations append `false`; candidate observations append `true`; retain only three flags. Two `true` flags confirm, then clear the window. Candidates, all values in a confirming horizon, non-`AllowAll` drain overlap, the next three eligible numeric observations after such a drain ends, and unknown drain/classification context never train either baseline or replace the reference. On confirmation, drain overlap/horizon produces a retained ineligible drain-associated source; unknown context produces a retained ineligible unknown-context source; otherwise an unexplained eligible source is inserted if outside cooldown. Cooldown suppresses only a duplicate source, not scoring, confirmation, or training.

The only configurable detector settings are `lower_tail_threshold` (default `0.0001`, range `0.000000001..0.1`), `minimum_drop_sessions` (default `3`, integer `1..1000000`), `minimum_drop_percent` (default `30`, integer percent `1..99`), `baseline_half_life_hours` (default `168`, integer `24..8760`), and `cooldown_minutes` (default `60`, integer `1..1440`). Invalid values are rejected, never clamped. Slot count 96, slot maturity seven distinct eligible days, all-hours maturity 20 normal observations spanning 24 hours, and confirmation two of trailing three are fixed constants, not configuration. These settings are session-only safe configuration and never enter machine `/api/v1/config`.

## Sanitized DTO and typed result

`EvidenceV1` is the persisted provider state object. Its exact outer serialization order is `v`, `snapshot_at_ms`, `window`, `source`, `context`, optional `local`, optional `fleet`, `omitted`; `source` and every nested object use the fixed provider-contract key order. `source_time_ms` is not a state-object key: it remains evidence-table metadata used only to derive the canonical source-relative `window.from_ms` and `window.to_ms`. Optional missing evidence is represented only by the deterministic `omitted` list. No field accepts arbitrary keys or prose. Its omission list is exactly `pre_upgrade_context`, `retention_expired`, `freshness_unavailable`, `drain_unavailable`, `detector_unavailable`, `local_points`, `fleet_points`, `local_aggregate`, and `fleet_aggregate`, with each applicable member at most once in that order.

`ResultV1` is the `investigation_results` row. Its four required answers and closed enums are:

| Question | Enum |
|---|---|
| likely cause | `resource_pressure`, `identity_or_authentication`, `service_or_os_failure`, `network_or_dependency`, `planned_drain_or_maintenance`, `fleet_correlated_event`, `unknown` |
| impact | `low`, `moderate`, `high`, `critical`, `unknown` |
| evidence sufficiency | `insufficient`, `partial`, `sufficient` |
| human review | `required`, `not_required` |

Each probability map is a bounded closed map of integer parts per million, and each confidence is stored separately in parts per million; there are no `reason_codes`. Dashboard language labels every retained answer a hypothesis. It suppresses cause and impact entirely for `insufficient` evidence and never turns any result into a command, notification, drain, restart, configuration change, or remediation.

## Provider configuration and safe status

`InvestigationProviderConfig` is stored through the existing atomic `config.json`/DPAPI path. Its credential is plaintext only in protected runtime memory and DPAPI-protected at rest; it is never a SQLite field.

| Field | Type / validation |
|---|---|
| `profile` | fixed literal `typesafe_jev`; no endpoint field |
| `access_enabled` | boolean; default false |
| `automatic_enabled` | boolean; default false; has effect only with valid enabled access |
| `acknowledged` | boolean; enabling access requires true acknowledgement of third-party, US/service-provider processing, no training/fine-tuning, no fixed external retention, and no DrainCtl deletion control |
| `credential` | required command object on every settings `PUT`: `{"operation":"preserve"}`, `{"operation":"replace","value":"non-empty write-only credential"}`, or `{"operation":"clear"}`; omission is invalid. `preserve` explicitly retains the DPAPI secret (including its absence), `replace` writes it through DPAPI, and `clear` removes it. `value` is required only for `replace` and forbidden otherwise. |
| detector settings | validated fields listed above |

The sole derived `InvestigationProviderStatus` exposes only safe fields: fixed profile/endpoint metadata, `access_enabled`, `automatic_enabled`, `acknowledged`, `has_credential`, validated detector settings, state (`disabled`, `configured`, `ready`, `automatic_enabled`, `degraded`, `failing`), retained-attempt counts only, and latest safe reason enum. `disabled` means access is disabled or unacknowledged. `configured` means acknowledged access is enabled but the credential is missing or undecryptable and is not send-ready. `ready` means acknowledged, enabled, credentialed, automatic off, and no active provider failure. `automatic_enabled` means the ready conditions with automatic investigation on. `degraded` and `failing` respectively mean a current recoverable or non-recoverable provider failure over an otherwise send-ready configuration. Dedicated dashboard-session settings/status routes may return it. Existing shared settings and machine `GET /api/v1/config` projections remain unchanged and receive neither this configuration nor investigation authority.

## Retention, migration, rollback, and ownership

Attempts expire independently when their own `created_at_ms` is earlier than `now_ms - AuditDays`, in bounded chunks. Deleting one attempt cascades only to that attempt's evidence, result, and provenance; `retry_of_attempt_id` neither prevents expiration nor cascades, so an expired predecessor is not retained because a retry remains. Session-drop source rows are separately deleted in bounded chunks when their own `detected_at_ms` is earlier than `now_ms - AuditDays`; attempt expiry never extends or shortens source retention. Baselines and detector state persist only while their host remains registered and are deleted when that server is permanently removed, never because an attempt or source expires. The existing retention pass retains its WAL checkpoint/incremental-vacuum behavior and records aggregate counts only.

Migration v4 adds only these tables, indexes, checks, and triggers inside existing `applySchema` transaction, then advances `PRAGMA user_version` to 4. It does not backfill attempts, evidence, baselines, freshness, drain context, or historical provider work. Missing pre-upgrade context is represented only by `pre_upgrade_context`. Downgrade leaves v4 tables untouched; old binaries may ignore them. If an old binary rewrites `config.json` and removes unknown provider fields, a later v4 service treats provider access and acknowledgement as disabled until explicitly restored. Rollback never queues or sends provider work and never drops v4 data.

All attempt creation/deduplication, retry eligibility, queue claim, terminal transition, detector watermark update, baseline update, confirmation update, anomaly insertion, and cooldown update are central SQLite writer transactions. Reads use WAL snapshots. Authorization and source resolution occur before detailed projection; authorization never depends on provider output. This preserves a single durable authority while keeping host identity on deterministic source rows and outside every investigation/provider artifact.
