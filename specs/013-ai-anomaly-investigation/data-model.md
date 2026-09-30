# Data Model: AI Anomaly Investigation

**Feature**: [013-ai-anomaly-investigation](./spec.md)
**Companion decisions**: [plan](./plan.md), [research](./research.md)

This model is owned solely by the central Windows dashboard/service. All `_ms` timestamps are UTC Unix epoch milliseconds. SQLite uses the existing WAL database, single writer, bounded readers, and audit-capable writer path. Schema v4 is additive: it does not alter `event_spikes`, report payloads, agent configuration, existing settings projections, `CheckResult`, `SpikePayload`, notifications, CLI, PowerShell, or installer behavior.

## Privacy, identity, and untrusted-data boundary

`event_spikes.host` and `session_drop_anomalies.host` retain canonical registered-host identity. They are resolved only after dashboard-group session authorization by source/list/detail projections. Investigation rows and all provider-bound or provider-derived records identify a source only by `(source_kind, source_id)`, where `source_kind` is `event_spike` or `session_drop` and `source_id` is its positive durable ID. SQLite cannot express a polymorphic source foreign key; source existence, eligibility, and same-source retry lineage are checked in the central creation transaction.

The following MUST NOT be stored in `investigation_*` tables (other than the deterministic source tables), request/response handling, diagnostics, SSE, or browser-local durable state: canonical host/FQDN/domain/IP/customer identity, username, session ID, `changed_by`, free text from a source, arbitrary or nonfixed URL, path, generic/raw structure, Event Log message/XML, dump, credential, notification secret, arbitrary file, raw provider body, refusal/error text, response ID, returned model, usage, reasoning, headers, annotations, or provider metadata. The sole local exceptions are the authenticated `actor` in `investigation_privacy_acknowledgements` for accountability and the fixed literal OpenAI endpoint in closed provenance; both remain ACL- and AuditDays-governed and never egress.

Provider prose is untrusted plain text, not an instruction. It is stored only in the normalized result child tables below after complete schema, lexical, byte-bound, and fact-reference validation; it is rendered only as HTML-escaped text. It is never Markdown/HTML, auto-linked, executed, sent to tools, or used for commands, PowerShell, configuration, drains, restarts, notifications, credential/file access, remediation, or other action.

## Conceptual entities

| Entity | Identity / ownership | Purpose |
|---|---|---|
| Deterministic source anomaly | Existing `event_spikes.id` or `session_drop_anomalies.id` | Owns source facts and canonical registered host. Detection remains authoritative and independent. |
| Investigation source aggregate | `(source_kind, source_id)`; conceptual only | Immutable ordered attempts; deliberately no parent lifecycle row or copied host. |
| Investigation attempt | `attempt_id`, source pair, `attempt_no` | One fresh evidence snapshot and at most one wire transmission. |
| Sanitized evidence snapshot | Exactly one per attempt | Either a versioned field-by-field allowlist EvidenceV1 DTO with its known fact-ID registry, or the explicit unavailable `{}` sentinel with no facts; persisted before any egress. |
| Investigation report | Zero or one per attempt | Fully validated `anomaly_investigation_v1`; bounded untrusted summary, assessment, sufficiency, review state, hypotheses, missing evidence, and `recommended_diagnostic_checks`. |
| Provider provenance | Zero or one provider-validated-report attempt | Closed local facts about the fixed OpenAI request and successful validation. |
| Privacy acknowledgement audit | `investigation_privacy_acknowledgements.id` | Append-only, authenticated record of acceptance of the complete fixed privacy clause set. |
| Session-drop anomaly | `session_drop_anomalies.id` | Deterministic lower-tail source with canonical host, classification, and eligibility. |
| Baseline / detector state | Host plus scope/slot; host state | Restart-safe per-host lower-tail algorithm state, never provider evidence. |

## SQLite v4

### 1. Attempts and immutable lifecycle

```sql
CREATE TABLE investigation_attempts (
    id                              INTEGER PRIMARY KEY AUTOINCREMENT,
    source_kind                     TEXT NOT NULL CHECK (source_kind IN ('event_spike','session_drop')),
    source_id                       INTEGER NOT NULL CHECK (source_id > 0),
    attempt_no                      INTEGER NOT NULL CHECK (attempt_no > 0),
    initiation                      TEXT NOT NULL CHECK (initiation IN ('automatic','manual','retry')),
    retry_of_attempt_id             INTEGER,
    state                           TEXT NOT NULL CHECK (state IN ('queued','running','completed','insufficient_evidence','failed')),
    created_at_ms                   INTEGER NOT NULL,
    queued_at_ms                    INTEGER NOT NULL,
    started_at_ms                   INTEGER,
    send_authorized_at_ms           INTEGER,
    send_completed_at_ms            INTEGER,
    send_lease_expires_at_ms        INTEGER,
    finalization_lease_expires_at_ms INTEGER,
    completed_at_ms                 INTEGER,
    terminal_reason                 TEXT NOT NULL DEFAULT '' CHECK (terminal_reason IN (
        '', 'authentication_failed', 'configuration_disabled', 'configuration_invalid',
        'evidence_unavailable', 'interrupted', 'network_error', 'provider_rate_limited',
        'provider_request_rejected', 'provider_refused', 'redirect_refused', 'request_limit',
        'response_incomplete', 'response_invalid', 'response_limit', 'storage_unavailable',
        'timeout', 'upstream_error'
    )),
    evidence_hash                   BLOB NOT NULL CHECK (length(evidence_hash) = 32),
    CHECK ((retry_of_attempt_id IS NULL AND initiation IN ('automatic','manual') AND attempt_no = 1)
        OR (retry_of_attempt_id IS NOT NULL AND initiation = 'retry' AND attempt_no > 1)),
    CHECK (
        (state = 'queued'
            AND started_at_ms IS NULL AND send_authorized_at_ms IS NULL AND send_completed_at_ms IS NULL
            AND send_lease_expires_at_ms IS NULL AND finalization_lease_expires_at_ms IS NULL
            AND completed_at_ms IS NULL AND terminal_reason = '')
        OR (state = 'running' AND started_at_ms IS NOT NULL AND completed_at_ms IS NULL
            AND terminal_reason = '' AND (
                (send_authorized_at_ms IS NULL AND send_completed_at_ms IS NULL
                    AND send_lease_expires_at_ms IS NULL AND finalization_lease_expires_at_ms IS NULL)
                OR (send_authorized_at_ms >= started_at_ms AND send_completed_at_ms IS NULL
                    AND send_lease_expires_at_ms >= send_authorized_at_ms
                    AND send_lease_expires_at_ms <= send_authorized_at_ms + 30000
                    AND finalization_lease_expires_at_ms IS NULL)
                OR (send_authorized_at_ms >= started_at_ms AND send_completed_at_ms >= send_authorized_at_ms
                    AND send_lease_expires_at_ms IS NULL
                    AND finalization_lease_expires_at_ms >= send_completed_at_ms
                    AND finalization_lease_expires_at_ms <= send_completed_at_ms + 120000)))
        OR (state = 'completed' AND started_at_ms IS NOT NULL AND send_authorized_at_ms >= started_at_ms
            AND send_completed_at_ms >= send_authorized_at_ms AND send_lease_expires_at_ms IS NULL
            AND finalization_lease_expires_at_ms IS NULL AND completed_at_ms >= started_at_ms
            AND terminal_reason = '')
        OR (state = 'insufficient_evidence' AND started_at_ms IS NOT NULL
            AND send_lease_expires_at_ms IS NULL AND finalization_lease_expires_at_ms IS NULL
            AND completed_at_ms >= started_at_ms AND terminal_reason IN ('', 'evidence_unavailable')
            AND ((send_authorized_at_ms IS NULL AND send_completed_at_ms IS NULL)
                OR (send_authorized_at_ms >= started_at_ms AND send_completed_at_ms >= send_authorized_at_ms)))
        OR (state = 'failed' AND started_at_ms IS NOT NULL
            AND send_lease_expires_at_ms IS NULL AND finalization_lease_expires_at_ms IS NULL
            AND completed_at_ms >= started_at_ms AND terminal_reason NOT IN ('', 'evidence_unavailable')
            AND ((send_authorized_at_ms IS NULL AND send_completed_at_ms IS NULL)
                OR (send_authorized_at_ms >= started_at_ms
                    AND (send_completed_at_ms IS NULL OR send_completed_at_ms >= send_authorized_at_ms))))),
    UNIQUE (source_kind, source_id, attempt_no)
);
CREATE UNIQUE INDEX investigation_attempts_one_root
    ON investigation_attempts(source_kind, source_id) WHERE retry_of_attempt_id IS NULL;
CREATE UNIQUE INDEX investigation_attempts_one_child_retry
    ON investigation_attempts(retry_of_attempt_id) WHERE retry_of_attempt_id IS NOT NULL;
CREATE INDEX investigation_attempts_queue ON investigation_attempts(queued_at_ms, id) WHERE state = 'queued';
CREATE INDEX investigation_attempts_active ON investigation_attempts(state) WHERE state IN ('queued','running');
CREATE INDEX investigation_attempts_source_history ON investigation_attempts(source_kind, source_id, attempt_no);
CREATE INDEX investigation_attempts_created_at ON investigation_attempts(created_at_ms);
CREATE TRIGGER investigation_attempts_terminal_immutable
BEFORE UPDATE ON investigation_attempts
WHEN OLD.state IN ('completed','insufficient_evidence','failed')
BEGIN
    SELECT RAISE(ABORT, 'terminal investigation attempt is immutable');
END;
```

The source-creation writer transaction checks two distinct caps before it inserts an attempt or its evidence: it counts all `queued` and `running` attempts globally and admits at most 100, then counts all retained attempts for the exact `(source_kind, source_id)` and admits at most 100. The global check applies to manual, retry, and automatic roots; an interactive manual/retry request over that cap receives HTTP `429 queue_full`, while an automatic root creates no work. The per-source cap rejects every root or retry—including a local unavailable-evidence outcome—with HTTP `409 attempt_limit_reached`. The same transaction assigns `attempt_no`, validates source existence/eligibility and retry predecessor/lineage, and inserts the attempt plus immutable snapshot, so the single writer makes both caps race-free. The store validates that a retry predecessor is retained, terminal (`failed` or `insufficient_evidence`), and in the same source aggregate. It intentionally has no foreign key, so retention may expire an earlier predecessor without retaining it because a later retry exists. A root is the sole automatic attempt; manual attempts and retries are interactive.

`started_at_ms` records when the worker begins processing or cancellation, not egress. `send_authorized_at_ms` is committed immediately before `Client.Do`; it means a transmission MAY have begun and is never proof of provider receipt. Once non-`NULL`, automatic resend is forbidden after uncertainty or restart. `send_completed_at_ms` records only that the local HTTP exchange returned, not provider receipt. A running row can therefore have no send timestamps while configuration, request-limit, local-insufficiency, or cancellation processing is in progress. Such local terminal rows retain no send timestamps. An in-flight authorized send has the 30-second `send_lease_expires_at_ms`; a post-return running row has both send timestamps and a `finalization_lease_expires_at_ms` no more than two minutes after local transport completion. Terminal rows clear both leases. The failed shape deliberately represents a crash after authorization but before transport return: it has `send_authorized_at_ms`, no `send_completed_at_ms`, and terminal `storage_unavailable`.

`terminal_reason` is a closed safe local enum, never provider material. `provider_refused`, `response_incomplete`, and `provider_request_rejected` carry no provider text. `evidence_unavailable` is exclusive to `insufficient_evidence`; it is never a failed-row reason. Its terminal transition is allowed only by the local unavailable-evidence transaction defined after provenance: it has the explicit unavailable snapshot and neither result nor provenance. A valid provider report with insufficient evidence has a result and provenance, then finalizes as `insufficient_evidence` with an empty `terminal_reason`.

### 2. Sanitized evidence and fact registry

Every central SQLite connection enables `PRAGMA foreign_keys = ON` before any transaction. The paired `(attempt_id, fact_id)` foreign keys below are therefore enforced rather than advisory.

```sql
CREATE TABLE investigation_evidence (
    attempt_id       INTEGER PRIMARY KEY REFERENCES investigation_attempts(id) ON DELETE CASCADE,
    dto_version      INTEGER NOT NULL CHECK (dto_version = 1),
    snapshot_kind    TEXT NOT NULL CHECK (snapshot_kind IN ('available','unavailable')),
    source_time_ms   INTEGER NOT NULL CHECK (source_time_ms > 0),
    snapshot_at_ms   INTEGER NOT NULL CHECK (snapshot_at_ms >= source_time_ms),
    from_ms          INTEGER NOT NULL,
    to_ms            INTEGER NOT NULL,
    canonical_json   TEXT NOT NULL CHECK (
        (snapshot_kind = 'available'
            AND json_valid(canonical_json) AND json_type(canonical_json) = 'object'
            AND length(CAST(canonical_json AS BLOB)) <= 8000)
        OR (snapshot_kind = 'unavailable' AND canonical_json = '{}')
    ),
    CHECK (from_ms = source_time_ms - 1800000),
    CHECK (to_ms = CASE WHEN source_time_ms + 1800000 <= snapshot_at_ms
                        THEN source_time_ms + 1800000 ELSE snapshot_at_ms END),
    CHECK (to_ms >= from_ms AND to_ms - from_ms <= 3600000)
);
CREATE TABLE investigation_evidence_facts (
    attempt_id INTEGER NOT NULL REFERENCES investigation_evidence(attempt_id) ON DELETE CASCADE,
    fact_id    TEXT NOT NULL CHECK (fact_id GLOB 'F[0-1][0-9][0-9]'
                                    AND CAST(substr(fact_id, 2) AS INTEGER) BETWEEN 1 AND 134),
    ordinal    INTEGER NOT NULL CHECK (ordinal BETWEEN 1 AND 134),
    PRIMARY KEY (attempt_id, fact_id),
    UNIQUE (attempt_id, ordinal),
    CHECK (fact_id = printf('F%03d', ordinal))
) WITHOUT ROWID;
CREATE TRIGGER investigation_evidence_facts_unavailable_rejected
BEFORE INSERT ON investigation_evidence_facts
WHEN (SELECT snapshot_kind FROM investigation_evidence
      WHERE attempt_id = NEW.attempt_id) = 'unavailable'
BEGIN
    SELECT RAISE(ABORT, 'unavailable evidence has no facts');
END;
CREATE TRIGGER investigation_evidence_immutable
BEFORE UPDATE ON investigation_evidence
BEGIN
    SELECT RAISE(ABORT, 'investigation evidence is immutable');
END;
```

`snapshot_kind` closes the two snapshot shapes. An `available` snapshot is a schema-validated, compact deterministic `EvidenceV1` serialization, not a generic map or request capture. The application builds EvidenceV1 as an object, validates the complete fixed schema, canonicalizes it, and verifies that its SHA-256 over the UTF-8 `canonical_json` bytes equals the attempt's `evidence_hash` before insert and before egress. It then inserts one registry row for every final visible fact and verifies the registry is nonempty and exactly contiguous `F001..Fnn`. An `unavailable` snapshot is the explicit host-free sentinel: `canonical_json` is exactly `{}`, `evidence_hash` is the SHA-256 of the UTF-8 bytes `{}`, and it has no fact rows. The fact-insert trigger enforces the latter shape; the writer enforces both complete shapes atomically because SQLite cannot validate EvidenceV1 or a child-row cardinality in a table `CHECK`.

For `available` snapshots, EvidenceV1 outer order is `v`, `snapshot_at_ms`, `window`, `source`, `context`, optional `local`, optional `fleet`, `omitted`; all nested key orders, enum sets, integer-only values, point/window bounds, labels, metrics, source-kind/anomaly pairing, and omission order are closed. It rejects unknown keys, `null`, floats, exponents, signed zero, stringified numbers, duplicate JSON keys, and all excluded values.

Available evidence has at most 61 local points, 61 fleet points, and 9 distinct ordered omissions. It uses only facts `F001`–`F134`; retries rebuild a fresh snapshot and IDs. Canonical available evidence is at most 8,000 UTF-8 bytes and the complete compact request is at most 16,384 UTF-8 bytes. The user `input_text` is exactly `EVIDENCE_JSON_V1\n` followed once by those canonical object bytes; outer request JSON serialization escapes that text once and never JSON-encodes the evidence a second time. Deterministic trimming removes the last fleet point, then local point, then empty aggregate while recording one ordered omission and renumbering. If mandatory evidence plus request overhead cannot fit, the attempt persists the unavailable snapshot and finishes locally as `insufficient_evidence/evidence_unavailable`, without egress or fabricated placeholder facts. Source missing or expired instead creates no attempt and returns its normal not-found result.

### 3. Strict versioned report, normalized to enforce references

The normalized tables are intentional: report prose is bounded by table-level byte/character checks plus the decoder's strict lexical validator, while each cited fact has a foreign key to the attempt-local fact registry. Thus a result cannot contain an unknown fact reference, a dynamic field, a probability map, or raw provider material. The decoder accepts exactly one duplicate-free JSON value with no trailing data, validates the same `anomaly_investigation_v1` strict schema, then inserts all report rows in one transaction.

```sql
CREATE TABLE investigation_results (
    attempt_id              INTEGER PRIMARY KEY REFERENCES investigation_attempts(id) ON DELETE CASCADE,
    result_version          INTEGER NOT NULL CHECK (result_version = 1),
    overall_assessment      TEXT NOT NULL CHECK (overall_assessment IN (
        'insufficient_evidence', 'indeterminate',
        'likely_localized_operational_issue', 'likely_fleet_wide_operational_issue',
        'likely_expected_or_maintenance_related'
    )),
    evidence_sufficiency    TEXT NOT NULL CHECK (evidence_sufficiency IN ('insufficient','partial','sufficient')),
    human_review_required   INTEGER NOT NULL CHECK (human_review_required IN (0,1)),
    summary_text_kind       TEXT NOT NULL CHECK (summary_text_kind = 'untrusted_summary'),
    summary_text            TEXT NOT NULL CHECK (length(CAST(summary_text AS BLOB)) BETWEEN 1 AND 1280
        AND summary_text = trim(summary_text) AND summary_text NOT GLOB '*[^ -~]*'
        AND instr(summary_text, '/') = 0 AND instr(summary_text, char(92)) = 0
        AND instr(summary_text, '<') = 0 AND instr(summary_text, '>') = 0
        AND instr(summary_text, '@') = 0 AND instr(summary_text, '`') = 0),
    CHECK ((evidence_sufficiency = 'insufficient' AND overall_assessment = 'insufficient_evidence' AND human_review_required = 1)
        OR (evidence_sufficiency IN ('partial','sufficient') AND overall_assessment <> 'insufficient_evidence'))
);
CREATE TABLE investigation_result_summary_facts (
    attempt_id INTEGER NOT NULL,
    fact_id    TEXT NOT NULL,
    PRIMARY KEY (attempt_id, fact_id),
    FOREIGN KEY (attempt_id) REFERENCES investigation_results(attempt_id) ON DELETE CASCADE,
    FOREIGN KEY (attempt_id, fact_id) REFERENCES investigation_evidence_facts(attempt_id, fact_id)
) WITHOUT ROWID;

CREATE TABLE investigation_hypotheses (
    attempt_id INTEGER NOT NULL REFERENCES investigation_results(attempt_id) ON DELETE CASCADE,
    rank       INTEGER NOT NULL CHECK (rank BETWEEN 1 AND 5),
    confidence TEXT NOT NULL CHECK (confidence IN ('low','medium','high')),
    text_kind  TEXT NOT NULL CHECK (text_kind = 'untrusted_hypothesis'),
    text       TEXT NOT NULL CHECK (length(CAST(text AS BLOB)) BETWEEN 1 AND 960
        AND text = trim(text) AND text NOT GLOB '*[^ -~]*'
        AND instr(text, '/') = 0 AND instr(text, char(92)) = 0 AND instr(text, '<') = 0
        AND instr(text, '>') = 0 AND instr(text, '@') = 0 AND instr(text, '`') = 0),
    PRIMARY KEY (attempt_id, rank)
) WITHOUT ROWID;
CREATE TABLE investigation_hypothesis_facts (
    attempt_id INTEGER NOT NULL,
    hypothesis_rank INTEGER NOT NULL CHECK (hypothesis_rank BETWEEN 1 AND 5),
    polarity TEXT NOT NULL CHECK (polarity IN ('supporting','contradicting')),
    fact_id TEXT NOT NULL,
    PRIMARY KEY (attempt_id, hypothesis_rank, polarity, fact_id),
    FOREIGN KEY (attempt_id, hypothesis_rank) REFERENCES investigation_hypotheses(attempt_id, rank) ON DELETE CASCADE,
    FOREIGN KEY (attempt_id, fact_id) REFERENCES investigation_evidence_facts(attempt_id, fact_id)
) WITHOUT ROWID;

CREATE TABLE investigation_missing_evidence (
    attempt_id INTEGER NOT NULL REFERENCES investigation_results(attempt_id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal BETWEEN 1 AND 6),
    category TEXT NOT NULL CHECK (category IN ('additional_time_series','host_health_detail','service_state','authentication_detail','network_dependency_detail','change_or_maintenance_context','fleet_comparison','other')),
    text_kind TEXT NOT NULL CHECK (text_kind = 'untrusted_missing_evidence'),
    text TEXT NOT NULL CHECK (length(CAST(text AS BLOB)) BETWEEN 1 AND 640 AND text = trim(text)
        AND text NOT GLOB '*[^ -~]*' AND instr(text, '/') = 0 AND instr(text, char(92)) = 0
        AND instr(text, '<') = 0 AND instr(text, '>') = 0 AND instr(text, '@') = 0 AND instr(text, '`') = 0),
    PRIMARY KEY (attempt_id, ordinal)
) WITHOUT ROWID;
CREATE TABLE investigation_missing_evidence_facts (
    attempt_id INTEGER NOT NULL,
    missing_ordinal INTEGER NOT NULL CHECK (missing_ordinal BETWEEN 1 AND 6),
    fact_id TEXT NOT NULL,
    PRIMARY KEY (attempt_id, missing_ordinal, fact_id),
    FOREIGN KEY (attempt_id, missing_ordinal) REFERENCES investigation_missing_evidence(attempt_id, ordinal) ON DELETE CASCADE,
    FOREIGN KEY (attempt_id, fact_id) REFERENCES investigation_evidence_facts(attempt_id, fact_id)
) WITHOUT ROWID;

CREATE TABLE investigation_recommended_diagnostic_checks (
    attempt_id INTEGER NOT NULL REFERENCES investigation_results(attempt_id) ON DELETE CASCADE,
    rank INTEGER NOT NULL CHECK (rank BETWEEN 1 AND 6),
    check_type TEXT NOT NULL CHECK (check_type IN ('inspect_retained_metrics','verify_service_state','verify_authentication_state','verify_network_or_dependency','verify_change_or_maintenance_context','compare_fleet','collect_additional_observation')),
    text_kind TEXT NOT NULL CHECK (text_kind = 'untrusted_diagnostic_check'),
    text TEXT NOT NULL CHECK (length(CAST(text AS BLOB)) BETWEEN 1 AND 960 AND text = trim(text)
        AND text NOT GLOB '*[^ -~]*' AND instr(text, '/') = 0 AND instr(text, char(92)) = 0
        AND instr(text, '<') = 0 AND instr(text, '>') = 0 AND instr(text, '@') = 0 AND instr(text, '`') = 0),
    PRIMARY KEY (attempt_id, rank)
) WITHOUT ROWID;
CREATE TABLE investigation_recommended_diagnostic_check_facts (
    attempt_id INTEGER NOT NULL,
    check_rank INTEGER NOT NULL CHECK (check_rank BETWEEN 1 AND 6),
    fact_id TEXT NOT NULL,
    PRIMARY KEY (attempt_id, check_rank, fact_id),
    FOREIGN KEY (attempt_id, check_rank) REFERENCES investigation_recommended_diagnostic_checks(attempt_id, rank) ON DELETE CASCADE,
    FOREIGN KEY (attempt_id, fact_id) REFERENCES investigation_evidence_facts(attempt_id, fact_id)
) WITHOUT ROWID;
CREATE TABLE investigation_recommended_diagnostic_check_hypotheses (
    attempt_id INTEGER NOT NULL,
    check_rank INTEGER NOT NULL CHECK (check_rank BETWEEN 1 AND 6),
    hypothesis_rank INTEGER NOT NULL CHECK (hypothesis_rank BETWEEN 1 AND 5),
    PRIMARY KEY (attempt_id, check_rank, hypothesis_rank),
    FOREIGN KEY (attempt_id, check_rank) REFERENCES investigation_recommended_diagnostic_checks(attempt_id, rank) ON DELETE CASCADE,
    FOREIGN KEY (attempt_id, hypothesis_rank) REFERENCES investigation_hypotheses(attempt_id, rank) ON DELETE CASCADE
) WITHOUT ROWID;
```

Before finalizing, the transaction enforces: summary has 1–12 distinct facts; hypotheses are consecutive `1..N`, `N=0` only for insufficient evidence otherwise `1..5`; each hypothesis has 1–12 supporting facts, at most 12 contradicting facts, and disjoint sets; missing items are consecutive `1..M`, `M<=6`, and each has at most 8 related facts; `recommended_diagnostic_checks` are consecutive `1..C`, `1<=C<=6`, each has at most 8 facts and at most 5 existing hypothesis ranks. Insufficient reports require no hypotheses and at least one missing item. Partial/sufficient reports require hypotheses. The strict response schema itself requires all properties and `additionalProperties:false` at every object level.

SQLite enforces printable ASCII only, trim/no leading or trailing space, and prohibition of `/`, `\\`, `<`, `>`, `@`, and backtick. The decoder applies the same strict schema patterns and local lexical validator before persistence: valid UTF-8; printable ASCII `U+0020..U+007E`; ordinary U+0020 is the only permitted whitespace; and the existing byte limits. It independently rejects CR/LF/tab, IPv4/IPv6 literals, and case-insensitive ASCII-normalized substring containment of any in-memory credential or known excluded source/customer/user/session canary. It does not claim to detect invented hostnames or domains absent from those canaries. These checks apply to every report prose column before it can be persisted. The fixed `text_kind` values preserve its untrusted role. Prose is never executed, routed, or treated as authorization; the product makes no claim that semantic text classification can prove text free of commands or remediation.

### 4. Closed local provenance

```sql
CREATE TABLE investigation_provenance (
    attempt_id              INTEGER PRIMARY KEY REFERENCES investigation_attempts(id) ON DELETE CASCADE,
    provider_profile        TEXT NOT NULL CHECK (provider_profile = 'openai_responses'),
    provider_endpoint       TEXT NOT NULL CHECK (provider_endpoint = 'https://api.openai.com/v1/responses'),
    requested_model         TEXT NOT NULL CHECK (requested_model = 'gpt-6-astra'),
    response_format         TEXT NOT NULL CHECK (response_format = 'anomaly_investigation_v1'),
    store                   INTEGER NOT NULL CHECK (store = 0),
    send_authorized_at_ms   INTEGER NOT NULL,
    send_completed_at_ms    INTEGER NOT NULL CHECK (send_completed_at_ms >= send_authorized_at_ms),
    request_header_bytes    INTEGER NOT NULL CHECK (request_header_bytes > 0 AND request_header_bytes <= 16384),
    request_body_bytes      INTEGER NOT NULL CHECK (request_body_bytes > 0 AND request_body_bytes <= 16384),
    response_header_bytes   INTEGER NOT NULL CHECK (response_header_bytes > 0 AND response_header_bytes <= 16384),
    response_body_bytes     INTEGER NOT NULL CHECK (response_body_bytes > 0 AND response_body_bytes <= 32768),
    validation_outcome      TEXT NOT NULL CHECK (validation_outcome IN ('accepted','insufficient_evidence'))
);
```

```sql
CREATE TRIGGER investigation_attempts_evidence_unavailable_insert_rejected
BEFORE INSERT ON investigation_attempts
WHEN NEW.state = 'insufficient_evidence' AND NEW.terminal_reason = 'evidence_unavailable'
BEGIN
    SELECT RAISE(ABORT, 'evidence_unavailable must follow unavailable evidence');
END;
CREATE TRIGGER investigation_attempts_evidence_unavailable_consistent
BEFORE UPDATE OF state, terminal_reason ON investigation_attempts
WHEN NEW.state = 'insufficient_evidence' AND NEW.terminal_reason = 'evidence_unavailable'
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM investigation_evidence
        WHERE attempt_id = NEW.id AND snapshot_kind = 'unavailable'
    ) THEN RAISE(ABORT, 'evidence_unavailable requires unavailable evidence') END;
    SELECT CASE WHEN EXISTS (
        SELECT 1 FROM investigation_results WHERE attempt_id = NEW.id
    ) OR EXISTS (
        SELECT 1 FROM investigation_provenance WHERE attempt_id = NEW.id
    ) THEN RAISE(ABORT, 'evidence_unavailable cannot have result or provenance') END;
END;
CREATE TRIGGER investigation_results_evidence_unavailable_rejected
BEFORE INSERT ON investigation_results
WHEN EXISTS (
    SELECT 1
    FROM investigation_attempts
    WHERE id = NEW.attempt_id
      AND state = 'insufficient_evidence'
      AND terminal_reason = 'evidence_unavailable'
)
BEGIN
    SELECT RAISE(ABORT, 'evidence_unavailable cannot have result');
END;
CREATE TRIGGER investigation_provenance_evidence_unavailable_rejected
BEFORE INSERT ON investigation_provenance
WHEN EXISTS (
    SELECT 1
    FROM investigation_attempts
    WHERE id = NEW.attempt_id
      AND state = 'insufficient_evidence'
      AND terminal_reason = 'evidence_unavailable'
)
BEGIN
    SELECT RAISE(ABORT, 'evidence_unavailable cannot have provenance');
END;
CREATE TRIGGER investigation_provenance_send_timestamps_required
BEFORE INSERT ON investigation_provenance
WHEN NOT EXISTS (
    SELECT 1
    FROM investigation_attempts
    WHERE id = NEW.attempt_id
      AND send_authorized_at_ms = NEW.send_authorized_at_ms
      AND send_completed_at_ms = NEW.send_completed_at_ms
)
BEGIN
    SELECT RAISE(ABORT, 'provenance requires completed recorded send');
END;
```

The local no-egress outcome writes the unavailable snapshot and its terminal `insufficient_evidence/evidence_unavailable` attempt transition in one SQLite writer transaction, without an `investigation_results` or `investigation_provenance` row. The terminal trigger verifies that complete shape, and the child-table triggers preserve it after the attempt becomes immutable.

A provenance row exists only for a fully validated report. It contains only local constants and local measurements: no provider response ID, returned model, usage, reasoning, response/refusal/error body, headers, annotations, system fingerprint, or provider metadata. Its fixed literal `provider_endpoint` is the sole provenance URL exception and never egresses. `response_body_bytes` is the bounded decompressed JSON response-body byte count. Valid insufficient reports receive `validation_outcome='insufficient_evidence'`; all failed and local pre-send insufficient outcomes have no provenance.

### 5. Privacy acknowledgement audit and config reference

```sql
CREATE TABLE investigation_privacy_acknowledgements (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    acknowledgement_version TEXT NOT NULL
        CHECK (acknowledgement_version = 'openai_responses_privacy_v1'),
    actor                   TEXT NOT NULL CHECK (length(CAST(actor AS BLOB)) BETWEEN 1 AND 320),
    accepted_at_ms          INTEGER NOT NULL CHECK (accepted_at_ms > 0),
    clause_set_hash         BLOB NOT NULL CHECK (length(clause_set_hash) = 32),
    clause_set_marker       TEXT NOT NULL
        CHECK (clause_set_marker = 'openai_responses_privacy_v1_complete_clauses')
);
CREATE INDEX investigation_privacy_acknowledgements_accepted_at
    ON investigation_privacy_acknowledgements(accepted_at_ms);
```

`InvestigationProviderConfig` stores both `privacy_acknowledgement_version` and nullable `privacy_acknowledgement_audit_id`. Acknowledgement is derived fail-closed: it is true only when the configured version is `openai_responses_privacy_v1`, the configured ID resolves to an audit row, and that row has the same fixed version, complete-clause marker, and fixed clause-set hash. Settings validation requires every privacy boolean explicitly; it first appends the immutable audit row with the authenticated actor—the sole identity exception, retained only locally under ACL and AuditDays—then atomically writes configuration that references its ID. A configuration-write failure may leave an unreferenced audit row, but can never create acknowledged access. Any migration, rollback, or legacy-config rewrite clears both fields; a later upgrade…

### 6. Session-drop sources, baselines, and detector state

```sql
CREATE TABLE session_drop_anomalies (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    host TEXT NOT NULL,
    report_epoch_ms INTEGER NOT NULL,
    accepted_at_ms INTEGER NOT NULL,
    local_offset_minutes INTEGER NOT NULL CHECK (local_offset_minutes BETWEEN -840 AND 840),
    local_date TEXT NOT NULL CHECK (length(local_date) = 10),
    detected_at_ms INTEGER NOT NULL,
    confirmation_started_report_epoch_ms INTEGER NOT NULL,
    confirmation_ended_report_epoch_ms INTEGER NOT NULL,
    confirmation_started_at_ms INTEGER NOT NULL,
    confirmation_ended_at_ms INTEGER NOT NULL,
    observed_sessions INTEGER NOT NULL CHECK (observed_sessions >= 0),
    reference_sessions INTEGER NOT NULL CHECK (reference_sessions >= 0),
    expected_sessions REAL NOT NULL CHECK (expected_sessions >= 0),
    baseline_model_version TEXT NOT NULL CHECK (baseline_model_version = 'gamma_poisson_lower_v1'),
    baseline_scope TEXT NOT NULL CHECK (baseline_scope IN ('slot','all_hours')),
    slot_index INTEGER CHECK (slot_index BETWEEN 0 AND 95),
    slot_mature_days INTEGER CHECK (slot_mature_days >= 7),
    tail_probability REAL NOT NULL CHECK (tail_probability >= 0 AND tail_probability <= 1),
    absolute_loss INTEGER NOT NULL CHECK (absolute_loss >= 0),
    relative_loss REAL NOT NULL CHECK (relative_loss >= 0 AND relative_loss <= 1),
    confirmation_window_size INTEGER NOT NULL CHECK (confirmation_window_size IN (2,3)),
    confirmation_1_candidate INTEGER NOT NULL CHECK (confirmation_1_candidate IN (0,1)),
    confirmation_2_candidate INTEGER NOT NULL CHECK (confirmation_2_candidate IN (0,1)),
    confirmation_3_candidate INTEGER CHECK (confirmation_3_candidate IN (0,1)),
    confirmation_count INTEGER NOT NULL CHECK (confirmation_count BETWEEN 2 AND confirmation_window_size),
    freshness_context TEXT NOT NULL CHECK (freshness_context = 'fresh'),
    drain_context TEXT NOT NULL CHECK (drain_context IN ('none','overlap','post_horizon','unknown')),
    classification TEXT NOT NULL CHECK (classification IN ('unexplained','drain_associated','unknown_context')),
    provider_eligible INTEGER NOT NULL CHECK (provider_eligible IN (0,1)),
    CHECK ((baseline_scope = 'slot' AND slot_index IS NOT NULL AND slot_mature_days IS NOT NULL)
        OR (baseline_scope = 'all_hours' AND slot_index IS NULL AND slot_mature_days IS NULL)),
    CHECK (confirmation_started_report_epoch_ms <= confirmation_ended_report_epoch_ms AND report_epoch_ms = confirmation_ended_report_epoch_ms),
    CHECK (confirmation_started_at_ms <= confirmation_ended_at_ms AND accepted_at_ms = confirmation_ended_at_ms),
    CHECK ((confirmation_window_size = 2) = (confirmation_3_candidate IS NULL)),
    CHECK (confirmation_count = confirmation_1_candidate + confirmation_2_candidate
        + COALESCE(confirmation_3_candidate, 0)),
    CHECK ((classification = 'unexplained' AND provider_eligible = 1 AND drain_context = 'none')
        OR (classification = 'drain_associated' AND provider_eligible = 0 AND drain_context IN ('overlap','post_horizon'))
        OR (classification = 'unknown_context' AND provider_eligible = 0 AND drain_context = 'unknown')),
    UNIQUE (host, confirmation_ended_report_epoch_ms)
);
CREATE INDEX session_drop_anomalies_host_time ON session_drop_anomalies(host, detected_at_ms DESC, id DESC);
CREATE INDEX session_drop_anomalies_eligible_time ON session_drop_anomalies(provider_eligible, detected_at_ms DESC, id DESC);

CREATE TABLE session_drop_baselines (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    host TEXT NOT NULL CHECK (length(host) > 0),
    scope TEXT NOT NULL CHECK (scope IN ('slot','all_hours')),
    slot_index INTEGER CHECK (slot_index BETWEEN 0 AND 95),
    model_version TEXT NOT NULL CHECK (model_version = 'gamma_poisson_lower_v1'),
    alpha REAL NOT NULL CHECK (alpha >= 1.0), beta REAL NOT NULL CHECK (beta >= 1.0),
    observation_count INTEGER NOT NULL DEFAULT 0 CHECK (observation_count >= 0),
    first_trained_at_ms INTEGER,
    last_normal_trained_at_ms INTEGER,
    last_updated_at_ms INTEGER NOT NULL,
    CHECK ((scope = 'slot' AND slot_index IS NOT NULL) OR (scope = 'all_hours' AND slot_index IS NULL)),
    CHECK ((observation_count = 0 AND first_trained_at_ms IS NULL AND last_normal_trained_at_ms IS NULL)
        OR (observation_count > 0 AND first_trained_at_ms IS NOT NULL AND last_normal_trained_at_ms IS NOT NULL
            AND last_normal_trained_at_ms >= first_trained_at_ms))
);
CREATE UNIQUE INDEX session_drop_baselines_host_slot ON session_drop_baselines(host, slot_index) WHERE scope = 'slot';
CREATE UNIQUE INDEX session_drop_baselines_host_all_hours ON session_drop_baselines(host) WHERE scope = 'all_hours';
CREATE TABLE session_drop_baseline_days (
    baseline_id INTEGER NOT NULL REFERENCES session_drop_baselines(id) ON DELETE CASCADE,
    local_date TEXT NOT NULL CHECK (length(local_date) = 10),
    PRIMARY KEY (baseline_id, local_date)
) WITHOUT ROWID;
CREATE TABLE session_drop_detector_state (
    host TEXT PRIMARY KEY,
    last_scored_report_epoch_ms INTEGER NOT NULL DEFAULT 0,
    last_reference_total INTEGER CHECK (last_reference_total IS NULL OR last_reference_total >= 0),
    cooldown_until_ms INTEGER NOT NULL DEFAULT 0,
    post_drain_remaining INTEGER NOT NULL DEFAULT 0 CHECK (post_drain_remaining BETWEEN 0 AND 3),
    confirmation_1_report_epoch_ms INTEGER, confirmation_1_candidate INTEGER CHECK (confirmation_1_candidate IN (0,1)), confirmation_1_context TEXT CHECK (confirmation_1_context IN ('none','overlap','post_horizon','unknown')),
    confirmation_2_report_epoch_ms INTEGER, confirmation_2_candidate INTEGER CHECK (confirmation_2_candidate IN (0,1)), confirmation_2_context TEXT CHECK (confirmation_2_context IN ('none','overlap','post_horizon','unknown')),
    confirmation_3_report_epoch_ms INTEGER, confirmation_3_candidate INTEGER CHECK (confirmation_3_candidate IN (0,1)), confirmation_3_context TEXT CHECK (confirmation_3_context IN ('none','overlap','post_horizon','unknown')),
    last_gap_reason TEXT NOT NULL DEFAULT '' CHECK (last_gap_reason IN ('','nil_enumeration','invalid_enumeration','missing_report_epoch','duplicate_report_epoch','out_of_order','stale','freshness_unknown')),
    state_updated_at_ms INTEGER NOT NULL,
    CHECK ((confirmation_1_report_epoch_ms IS NULL) = (confirmation_1_candidate IS NULL) AND (confirmation_1_report_epoch_ms IS NULL) = (confirmation_1_context IS NULL)),
    CHECK ((confirmation_2_report_epoch_ms IS NULL) = (confirmation_2_candidate IS NULL) AND (confirmation_2_report_epoch_ms IS NULL) = (confirmation_2_context IS NULL)),
    CHECK ((confirmation_3_report_epoch_ms IS NULL) = (confirmation_3_candidate IS NULL) AND (confirmation_3_report_epoch_ms IS NULL) = (confirmation_3_context IS NULL))
) WITHOUT ROWID;
CREATE INDEX session_drop_detector_cooldown ON session_drop_detector_state(cooldown_until_ms);

-- Durable handoff from accepted server reports to the session-drop detector.
CREATE TABLE session_drop_observation_inbox (
    accepted_sequence       INTEGER PRIMARY KEY AUTOINCREMENT,
    canonical_host          TEXT NOT NULL CHECK (length(canonical_host) > 0),
    report_epoch_ms         INTEGER NOT NULL CHECK (report_epoch_ms > 0),
    accepted_at_ms          INTEGER NOT NULL CHECK (accepted_at_ms > 0),
    local_offset_minutes    INTEGER NOT NULL CHECK (local_offset_minutes BETWEEN -840 AND 840),
    local_date              TEXT NOT NULL CHECK (length(local_date) = 10),
    session_presence        TEXT NOT NULL CHECK (session_presence IN ('present','nil','invalid','unavailable')),
    active_sessions         INTEGER CHECK (active_sessions >= 0),
    disconnected_sessions   INTEGER CHECK (disconnected_sessions >= 0),
    total_sessions          INTEGER CHECK (total_sessions >= 0),
    freshness_context       TEXT NOT NULL CHECK (freshness_context IN ('fresh','stale','unknown')),
    drain_context           TEXT NOT NULL CHECK (drain_context IN ('none','overlap','post_horizon','unknown')),
    classification_context  TEXT NOT NULL CHECK (classification_context IN (
        'unexplained','drain_associated','unknown_context','not_scored'
    )),
    CHECK (
        (session_presence = 'present'
            AND active_sessions IS NOT NULL AND disconnected_sessions IS NOT NULL
            AND total_sessions = active_sessions + disconnected_sessions)
        OR (session_presence <> 'present'
            AND active_sessions IS NULL AND disconnected_sessions IS NULL AND total_sessions IS NULL)
    ),
    UNIQUE (canonical_host, report_epoch_ms)
);
```

The report-acceptance writer transaction first persists the accepted `servers.last_result_json`, then inserts one inbox row with its private global `accepted_sequence`, canonical host, report epoch, central `accepted_at_ms`, source-local offset/date, typed session presence/counts, and the already-derived freshness, drain, and classification context. The SQLite writer assigns `accepted_sequence` in that same transaction; `accepted_at_ms` is evidence/time only and never an ordering key. It uses `INSERT ... ON CONFLICT(canonical_host, report_epoch_ms) DO NOTHING`; a duplicate report therefore cannot create a second detector observation. The post-commit callback only wakes the inbox consumer and carries no observation state. A crash after this commit and before the wake leaves the row durable.

The consumer selects the oldest pending inbox row strictly by ascending `accepted_sequence` and, in one writer transaction, applies its gap/scoring decision, detector watermark, baseline changes, confirmation window, source anomaly insertion, and cooldown updates, then deletes that exact inbox row. The anomaly insert copies `accepted_at_ms` from the consumed rows: `confirmation_started_at_ms` is the first included row's acceptance time and `confirmation_ended_at_ms` is the terminal confirmation row's acceptance time. Thus accepted-report time is never reconstructed from a callback, wall clock, or report epoch, and is never used to order the drain. Startup drains every pending inbox row with that transaction before it accepts any newer report; a crash after acceptance before consumption resumes at the same lowest `accepted_sequence`.

Each retained anomaly persists the chronological confirmation window actually used. `confirmation_started_at_ms` and `confirmation_ended_at_ms` are the central `accepted_at_ms` values from the first and last included durable inbox rows; they are distinct from the corresponding report-epoch fields and are projected exactly as persisted. `confirmation_window_size` is exactly 2 or 3, the first two candidate flags are always present, and the third is `NULL` exactly for a two-observation window. The count is the sum of the present true flags and is therefore 2 through the persisted window size. This permits a 2-of-3 confirmation to emit after the second eligible report when both observed flags are candidates, without inventing a third observation. The REST projection serializes the persisted values; it does not recompute them.

This preserves the existing lower-tail contract: `TotalSessions = Active + Disconnected` is scored exactly once for each distinct in-order fresh report; zero is valid and nil/invalid/unavailable/stale/out-of-order is an unknown gap. Each host has 96 local quarter-hour slots and a host-only all-hours fallback. Slot readiness requires seven distinct eligible local days; all-hours requires 20 normal observations across 24 hours, measured only from `first_trained_at_ms` through `last_normal_trained_at_ms`. The baseline-update writer transaction sets `last_normal_trained_at_ms` only when it admits an eligible normal observation; it never advances it for decay, scoring, candidates, drain observations, or gaps. Decay, `gamma_poisson_lower_v1`, lower-tail default `1e-4`, minimum loss defaults (3 sessions/30%), trailing 2-of-3 confirmation, post-drain gap, per-host cooldown, candidate non-training, drain classification, and no pooled host baselines remain unchanged. Only `unexplained` is provider eligible; no session-drop notification is created…

## Lifecycle, transport, configuration, and retention

1. Authorization happens before source lookup, evidence construction, attempt creation, or disclosure. Machine routes have neither configuration nor investigation access.
2. Before egress, the one writer transaction persists only the source link, retry lineage, immutable attempt, and an available evidence snapshot with its fact registry. An unavailable snapshot is instead persisted for a local no-egress insufficient-evidence outcome. Before either insert it transactionally enforces both the global maximum of 100 `queued`+`running` attempts and the per-source maximum of 100 retained attempts. The global excess response is `429 queue_full` for manual/retry calls and no automatic work; per-source excess is `409 attempt_limit_reached`. A pre-send storage failure produces zero egress and only a safe local diagnostic when possible. No terminal result, failure, or provenance is persisted as part of the pre-send commit.
3. A FIFO claim is one writer transaction that requires `state='queued'`, `created_at_ms >= cutoff`, and a persisted available snapshot whose canonical SHA-256 equals `evidence_hash` with a valid nonempty contiguous fact registry; it otherwise does not claim or egress the row. The exact unavailable `{}` snapshot/hash is terminal local insufficiency and is never queued or claimed. The successful claim sets only `running` and `started_at_ms`. Local configuration, request-limit, cancellation, and evidence outcomes can then finalize without send timestamps. Immediately before the sole `Client.Do`, a second writer transaction sets `send_authorized_at_ms` and the 30-second `send_lease_expires_at_ms`; this is the only transition that authorizes egress. It is intentionally durable before the call because a crash at that point leaves provider receipt unknown and MUST NOT result in resend.
4. Recovery runs before worker processing and in retention passes. A live `running` row is protected while either bounded lease is active: the 30-second `send_lease_expires_at_ms` or the two-minute `finalization_lease_expires_at_ms`. Recovery does not terminalize or delete its evidence while either expiry is greater than `now_ms`. When neither lease is active—because no lease was recorded or the applicable lease expired—recovery atomically applies the restart/lease table: `send_authorized_at_ms IS NULL` becomes `failed/interrupted`; non-`NULL` `send_authorized_at_ms` becomes `failed/storage_unavailable`. Each terminal transition clears both leases. The latter outcome applies whether the process crashed immediately after authorization or after local transport return; it never reconstructs result/provenance and never resends. Retention applies normal AuditDays deletion only after that recovery transaction has terminalized an expired or unleased row.
5. One worker claims FIFO, validates current configuration immediately before its sole send, then records `send_authorized_at_ms`. It makes one foreground, non-streaming, stateless request: `POST https://api.openai.com/v1/responses`, model `gpt-6-astra`, `store:false`, `background:false`, `stream:false`, `truncation:"disabled"`, `reasoning.effort:"high"`, `max_output_tokens:8192`, `tools:[]`, `tool_choice:"none"`, `parallel_tool_calls:false`, and strict `text.format` schema name `anomaly_investigation_v1`. There is no tool, stateful API, polling, redirect, fallback, or automatic retry. Limits remain one worker, 10/minute burst 2, and 30 seconds.
6. `InvestigationProviderConfig` remains encrypted config.json/DPAPI material only. `internal/investigation` owns the narrow decrypt-for-send accessor; plaintext key bytes never enter `Config`, `ServiceConfig`, `DashboardConfig`, `RemoteSettings`, closures, or worker state, and temporary decrypted/request-construction bytes are zeroed and released after request construction. `profile` is fixed `openai_responses`, no endpoint field; `access_enabled` and `automatic_enabled` default false. The acknowledgement version and audit-ID reference follow the fail-closed protocol in section 5; migration, rollback, and legacy config rewrites clear both fields. Every settings `PUT` requires exactly one credential operation: `preserve`, `replace`, or `clear`.
7. Attempts expire independently in bounded chunks when `created_at_ms < now_ms - AuditDays`. Retention deletes expired `queued`, `completed`, `insufficient_evidence`, and `failed` rows. For an expired `running` row, it first checks both lease expiries: while either the 30-second send lease or two-minute finalization lease is active it retains the row and all evidence; only after neither is active does it atomically apply the restart/lease table and then apply normal AuditDays deletion to the resulting terminal row. Cascades remove only that attempt's evidence, facts, report, children, and provenance. Session-drop source rows expire independently in chunks when `detected_at_ms < now_ms - AuditDays`; pending observation inbox rows are removed only by atomic consumption or permanent host removal. Baselines/detector state persist until permanent host removal. Migration adds these v4 objects transactionally with no historical backfill.

After the one send, the worker retains at most one bounded, validated terminal persistence candidate per active worker in process memory: either normalized result/provenance plus its terminal state, or the closed terminal failure. It immediately discards the raw provider body after validation. When the initial terminal-write transaction cannot complete, it records `send_completed_at_ms`, clears the send lease, and starts the fixed two-minute finalization lease. While that lease remains active, it may retry only the same validated in-memory candidate to SQLite and never makes another provider call. At its deadline it discards the candidate. A process restart never receives that in-memory candidate: recovery instead writes `failed/storage_unavailable`, even if the local HTTP exchange had returned.

All source creation/deduplication, retry eligibility, queue claim, terminal transition, evidence/result/provenance insertion, accepted-report/inbox insertion, inbox consumption with detector watermark/baseline/confirmation updates, anomaly insertion, cooldown update, and permanent host removal are central SQLite writer transactions. Reads use WAL snapshots. This preserves central-only ownership, evidence-before-egress, one worker/one transmission/explicit retry, durable accepted-report detection, independent retention/rollback, and deterministic detector authority.
