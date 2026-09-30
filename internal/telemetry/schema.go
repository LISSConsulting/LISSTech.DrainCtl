//go:build windows

package telemetry

import (
	"database/sql"
	"strconv"
	"strings"
)

// ddl is the canonical schema applied to every new or existing drainctl.db.
// All tables use IF NOT EXISTS so the statement block is idempotent.
const ddl = `
CREATE TABLE IF NOT EXISTS schema_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS audit (
    ts              INTEGER NOT NULL,
    host            TEXT    NOT NULL,
    prev_state      INTEGER NOT NULL,
    new_state       INTEGER NOT NULL,
    principal       TEXT    NOT NULL DEFAULT '',
    changed_by      TEXT    NOT NULL DEFAULT '',
    reason          TEXT    NOT NULL DEFAULT '',
    key_modified_ts INTEGER,
    reconciliation  INTEGER NOT NULL DEFAULT 0 CHECK (reconciliation IN (0,1)),
    before_ts       INTEGER,
    PRIMARY KEY (ts, host, new_state)
);

CREATE INDEX IF NOT EXISTS audit_host_ts   ON audit(host, ts DESC);
CREATE INDEX IF NOT EXISTS audit_ts        ON audit(ts DESC);
CREATE INDEX IF NOT EXISTS audit_principal ON audit(principal, ts DESC) WHERE principal <> '';

CREATE TABLE IF NOT EXISTS metrics_raw (
    ts      INTEGER NOT NULL,
    host    TEXT    NOT NULL,
    counter TEXT    NOT NULL,
    value   REAL    NOT NULL,
    PRIMARY KEY (host, ts, counter)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS metrics_raw_ts ON metrics_raw(ts);

CREATE TABLE IF NOT EXISTS metrics_5min (
    bucket_ts    INTEGER NOT NULL,
    host         TEXT    NOT NULL,
    counter      TEXT    NOT NULL,
    avg_value    REAL    NOT NULL,
    min_value    REAL    NOT NULL,
    max_value    REAL    NOT NULL,
    sample_count INTEGER NOT NULL,
    PRIMARY KEY (host, bucket_ts, counter)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS metrics_5min_ts ON metrics_5min(bucket_ts);

CREATE TABLE IF NOT EXISTS metrics_hourly (
    bucket_ts    INTEGER NOT NULL,
    host         TEXT    NOT NULL,
    counter      TEXT    NOT NULL,
    avg_value    REAL    NOT NULL,
    min_value    REAL    NOT NULL,
    max_value    REAL    NOT NULL,
    sample_count INTEGER NOT NULL,
    PRIMARY KEY (host, bucket_ts, counter)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS metrics_hourly_ts ON metrics_hourly(bucket_ts);

CREATE TABLE IF NOT EXISTS maintenance_jobs (
    name          TEXT PRIMARY KEY,
    started_ts    INTEGER NOT NULL,
    finished_ts   INTEGER NOT NULL,
    duration_ms   INTEGER NOT NULL,
    outcome       TEXT    NOT NULL CHECK (outcome IN ('success','failure','skipped')),
    reason        TEXT    NOT NULL DEFAULT '',
    rows_affected INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS event_spikes (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    host               TEXT    NOT NULL,
    channel            TEXT    NOT NULL,
    window_start_ms    INTEGER NOT NULL,
    window_end_ms      INTEGER NOT NULL,
    observed           INTEGER NOT NULL,
    expected           REAL    NOT NULL,
    tail_probability   REAL    NOT NULL,
    confirmation_count INTEGER NOT NULL,
    first_seen_at_ms   INTEGER NOT NULL,
    created_at_ms      INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS event_spikes_identity
    ON event_spikes(host, channel, window_start_ms);
CREATE INDEX IF NOT EXISTS event_spikes_host_ts
    ON event_spikes(host, window_start_ms DESC);
CREATE INDEX IF NOT EXISTS event_spikes_ts
    ON event_spikes(window_start_ms);

CREATE TABLE IF NOT EXISTS servers (
    hostname         TEXT    PRIMARY KEY,
    registered_at_ms INTEGER NOT NULL,
    last_seen_ms     INTEGER NOT NULL DEFAULT 0,
    last_result_json TEXT
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS server_exclusions (
    hostname        TEXT    PRIMARY KEY,
    excluded_at_ms  INTEGER NOT NULL,
    excluded_by     TEXT    NOT NULL DEFAULT '',
    reason          TEXT    NOT NULL DEFAULT ''
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS server_exclusions_at
    ON server_exclusions(excluded_at_ms DESC);
CREATE TABLE IF NOT EXISTS host_freshness (
    host                  TEXT    PRIMARY KEY COLLATE NOCASE,
    report_epoch_ms       INTEGER NOT NULL,
    offline_emitted_at_ms INTEGER
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS force_update_outbox (
    command_id     TEXT    NOT NULL,
    host           TEXT    NOT NULL,
    reason         TEXT    NOT NULL DEFAULT '',
    accepted_at_ms INTEGER NOT NULL,
    agent_version  TEXT    NOT NULL,
    PRIMARY KEY (host, command_id)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS force_update_outbox_pending
    ON force_update_outbox(host, accepted_at_ms, command_id);
`

const schemaVersion = 5

// applySchema runs additive idempotent DDL and advances user_version to the
// current schema version.

// It never downgrades a schema version advanced by a future feature.
func applySchema(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err = tx.Exec(ddl); err != nil {
		return err
	}
	if _, err = tx.Exec(schemaV4DDL); err != nil {
		return err
	}
	var version int
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if err = migrateSessionsV4(tx); err != nil {
		return err
	}
	if err = migrateSessionWorkloadV5(tx); err != nil {
		return err
	}
	if version < schemaVersion {
		if _, err = tx.Exec("PRAGMA user_version = " + strconv.Itoa(schemaVersion)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// schemaV4DDL is executed as one SQLite script. In particular, trigger bodies
// contain semicolons; splitting this script would turn valid trigger bodies
// into invalid standalone statements and destroy migration atomicity.
const schemaV4DDL = `
CREATE TABLE IF NOT EXISTS investigation_attempts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    source_kind TEXT NOT NULL CHECK (source_kind IN ('event_spike','session_drop')),
    source_id INTEGER NOT NULL CHECK (source_id > 0),
    attempt_no INTEGER NOT NULL CHECK (attempt_no > 0),
    initiation TEXT NOT NULL CHECK (initiation IN ('automatic','manual','retry')),
    retry_of_attempt_id INTEGER,
    state TEXT NOT NULL CHECK (state IN ('queued','running','completed','insufficient_evidence','failed')),
    created_at_ms INTEGER NOT NULL, queued_at_ms INTEGER NOT NULL, started_at_ms INTEGER,
    send_authorized_at_ms INTEGER, send_completed_at_ms INTEGER,
    send_lease_expires_at_ms INTEGER, finalization_lease_expires_at_ms INTEGER, completed_at_ms INTEGER,
    terminal_reason TEXT NOT NULL DEFAULT '' CHECK (terminal_reason IN (
        '', 'authentication_failed', 'configuration_disabled', 'configuration_invalid',
        'evidence_unavailable', 'interrupted', 'network_error', 'provider_rate_limited',
        'provider_request_rejected', 'provider_refused', 'redirect_refused', 'request_limit',
        'response_incomplete', 'response_invalid', 'response_limit', 'storage_unavailable',
        'timeout', 'upstream_error'
    )),
    evidence_hash BLOB NOT NULL CHECK (length(evidence_hash) = 32),
    CHECK ((retry_of_attempt_id IS NULL AND initiation IN ('automatic','manual') AND attempt_no = 1)
        OR (retry_of_attempt_id IS NOT NULL AND initiation = 'retry' AND attempt_no > 1)),
    CHECK (
        (state = 'queued' AND started_at_ms IS NULL AND send_authorized_at_ms IS NULL
            AND send_completed_at_ms IS NULL AND send_lease_expires_at_ms IS NULL
            AND finalization_lease_expires_at_ms IS NULL AND completed_at_ms IS NULL AND terminal_reason = '')
        OR (state = 'running' AND started_at_ms IS NOT NULL AND completed_at_ms IS NULL AND terminal_reason = ''
            AND ((send_authorized_at_ms IS NULL AND send_completed_at_ms IS NULL
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
            AND finalization_lease_expires_at_ms IS NULL AND completed_at_ms >= started_at_ms AND terminal_reason = '')
        OR (state = 'insufficient_evidence' AND started_at_ms IS NOT NULL AND send_lease_expires_at_ms IS NULL
            AND finalization_lease_expires_at_ms IS NULL AND completed_at_ms >= started_at_ms
            AND terminal_reason IN ('', 'evidence_unavailable')
            AND ((send_authorized_at_ms IS NULL AND send_completed_at_ms IS NULL)
                OR (send_authorized_at_ms >= started_at_ms AND send_completed_at_ms >= send_authorized_at_ms)))
        OR (state = 'failed' AND started_at_ms IS NOT NULL AND send_lease_expires_at_ms IS NULL
            AND finalization_lease_expires_at_ms IS NULL AND completed_at_ms >= started_at_ms
            AND terminal_reason NOT IN ('', 'evidence_unavailable')
            AND ((send_authorized_at_ms IS NULL AND send_completed_at_ms IS NULL)
                OR (send_authorized_at_ms >= started_at_ms
                    AND (send_completed_at_ms IS NULL OR send_completed_at_ms >= send_authorized_at_ms))))),
    UNIQUE (source_kind, source_id, attempt_no)
);
CREATE UNIQUE INDEX IF NOT EXISTS investigation_attempts_one_root ON investigation_attempts(source_kind, source_id) WHERE retry_of_attempt_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS investigation_attempts_one_child_retry ON investigation_attempts(retry_of_attempt_id) WHERE retry_of_attempt_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS investigation_attempts_queue ON investigation_attempts(queued_at_ms, id) WHERE state = 'queued';
CREATE INDEX IF NOT EXISTS investigation_attempts_active ON investigation_attempts(state) WHERE state IN ('queued','running');
CREATE INDEX IF NOT EXISTS investigation_attempts_source_history ON investigation_attempts(source_kind, source_id, attempt_no);
CREATE INDEX IF NOT EXISTS investigation_attempts_created_at ON investigation_attempts(created_at_ms);
CREATE TRIGGER IF NOT EXISTS investigation_attempts_terminal_immutable BEFORE UPDATE ON investigation_attempts
WHEN OLD.state IN ('completed','insufficient_evidence','failed')
BEGIN SELECT RAISE(ABORT, 'terminal investigation attempt is immutable'); END;

CREATE TABLE IF NOT EXISTS investigation_evidence (
    attempt_id INTEGER PRIMARY KEY REFERENCES investigation_attempts(id) ON DELETE CASCADE,
    dto_version INTEGER NOT NULL CHECK (dto_version = 1),
    snapshot_kind TEXT NOT NULL CHECK (snapshot_kind IN ('available','unavailable')),
    source_time_ms INTEGER NOT NULL CHECK (source_time_ms > 0),
    snapshot_at_ms INTEGER NOT NULL CHECK (snapshot_at_ms >= source_time_ms),
    from_ms INTEGER NOT NULL, to_ms INTEGER NOT NULL,
    canonical_json TEXT NOT NULL CHECK ((snapshot_kind = 'available' AND json_valid(canonical_json)
        AND json_type(canonical_json) = 'object' AND length(CAST(canonical_json AS BLOB)) <= 8000)
        OR (snapshot_kind = 'unavailable' AND canonical_json = '{}')),
    CHECK (from_ms = source_time_ms - 1800000),
    CHECK (to_ms = CASE WHEN source_time_ms + 1800000 <= snapshot_at_ms THEN source_time_ms + 1800000 ELSE snapshot_at_ms END),
    CHECK (to_ms >= from_ms AND to_ms - from_ms <= 3600000)
);
CREATE TABLE IF NOT EXISTS investigation_evidence_facts (
    attempt_id INTEGER NOT NULL REFERENCES investigation_evidence(attempt_id) ON DELETE CASCADE,
    fact_id TEXT NOT NULL CHECK (fact_id GLOB 'F[0-1][0-9][0-9]' AND CAST(substr(fact_id, 2) AS INTEGER) BETWEEN 1 AND 134),
    ordinal INTEGER NOT NULL CHECK (ordinal BETWEEN 1 AND 134),
    PRIMARY KEY (attempt_id, fact_id), UNIQUE (attempt_id, ordinal),
    CHECK (fact_id = printf('F%03d', ordinal))
) WITHOUT ROWID;
CREATE TRIGGER IF NOT EXISTS investigation_evidence_facts_unavailable_rejected
BEFORE INSERT ON investigation_evidence_facts
WHEN (SELECT snapshot_kind FROM investigation_evidence WHERE attempt_id = NEW.attempt_id) = 'unavailable'
BEGIN SELECT RAISE(ABORT, 'unavailable evidence has no facts'); END;
CREATE TRIGGER IF NOT EXISTS investigation_evidence_immutable BEFORE UPDATE ON investigation_evidence
BEGIN SELECT RAISE(ABORT, 'investigation evidence is immutable'); END;

CREATE TABLE IF NOT EXISTS investigation_results (
    attempt_id INTEGER PRIMARY KEY REFERENCES investigation_attempts(id) ON DELETE CASCADE,
    result_version INTEGER NOT NULL CHECK (result_version = 1),
    overall_assessment TEXT NOT NULL CHECK (overall_assessment IN ('insufficient_evidence','indeterminate','likely_localized_operational_issue','likely_fleet_wide_operational_issue','likely_expected_or_maintenance_related')),
    evidence_sufficiency TEXT NOT NULL CHECK (evidence_sufficiency IN ('insufficient','partial','sufficient')),
    human_review_required INTEGER NOT NULL CHECK (human_review_required IN (0,1)),
    summary_text_kind TEXT NOT NULL CHECK (summary_text_kind = 'untrusted_summary'),
    summary_text TEXT NOT NULL CHECK (length(CAST(summary_text AS BLOB)) BETWEEN 1 AND 1280 AND summary_text = trim(summary_text) AND summary_text NOT GLOB '*[^ -~]*' AND instr(summary_text, '/') = 0 AND instr(summary_text, char(92)) = 0 AND instr(summary_text, '<') = 0 AND instr(summary_text, '>') = 0 AND instr(summary_text, '@') = 0 AND instr(summary_text, char(96)) = 0),
    CHECK ((evidence_sufficiency = 'insufficient' AND overall_assessment = 'insufficient_evidence' AND human_review_required = 1) OR (evidence_sufficiency IN ('partial','sufficient') AND overall_assessment <> 'insufficient_evidence'))
);
CREATE TABLE IF NOT EXISTS investigation_result_summary_facts (
    attempt_id INTEGER NOT NULL, fact_id TEXT NOT NULL, PRIMARY KEY (attempt_id, fact_id),
    FOREIGN KEY (attempt_id) REFERENCES investigation_results(attempt_id) ON DELETE CASCADE,
    FOREIGN KEY (attempt_id, fact_id) REFERENCES investigation_evidence_facts(attempt_id, fact_id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS investigation_hypotheses (
    attempt_id INTEGER NOT NULL REFERENCES investigation_results(attempt_id) ON DELETE CASCADE,
    rank INTEGER NOT NULL CHECK (rank BETWEEN 1 AND 5), confidence TEXT NOT NULL CHECK (confidence IN ('low','medium','high')),
    text_kind TEXT NOT NULL CHECK (text_kind = 'untrusted_hypothesis'),
    text TEXT NOT NULL CHECK (length(CAST(text AS BLOB)) BETWEEN 1 AND 960 AND text = trim(text) AND text NOT GLOB '*[^ -~]*' AND instr(text, '/') = 0 AND instr(text, char(92)) = 0 AND instr(text, '<') = 0 AND instr(text, '>') = 0 AND instr(text, '@') = 0 AND instr(text, char(96)) = 0),
    PRIMARY KEY (attempt_id, rank)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS investigation_hypothesis_facts (
    attempt_id INTEGER NOT NULL, hypothesis_rank INTEGER NOT NULL CHECK (hypothesis_rank BETWEEN 1 AND 5),
    polarity TEXT NOT NULL CHECK (polarity IN ('supporting','contradicting')), fact_id TEXT NOT NULL,
    PRIMARY KEY (attempt_id, hypothesis_rank, polarity, fact_id),
    FOREIGN KEY (attempt_id, hypothesis_rank) REFERENCES investigation_hypotheses(attempt_id, rank) ON DELETE CASCADE,
    FOREIGN KEY (attempt_id, fact_id) REFERENCES investigation_evidence_facts(attempt_id, fact_id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS investigation_missing_evidence (
    attempt_id INTEGER NOT NULL REFERENCES investigation_results(attempt_id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal BETWEEN 1 AND 6),
    category TEXT NOT NULL CHECK (category IN ('additional_time_series','host_health_detail','service_state','authentication_detail','network_dependency_detail','change_or_maintenance_context','fleet_comparison','other')),
    text_kind TEXT NOT NULL CHECK (text_kind = 'untrusted_missing_evidence'),
    text TEXT NOT NULL CHECK (length(CAST(text AS BLOB)) BETWEEN 1 AND 640 AND text = trim(text) AND text NOT GLOB '*[^ -~]*' AND instr(text, '/') = 0 AND instr(text, char(92)) = 0 AND instr(text, '<') = 0 AND instr(text, '>') = 0 AND instr(text, '@') = 0 AND instr(text, char(96)) = 0),
    PRIMARY KEY (attempt_id, ordinal)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS investigation_missing_evidence_facts (
    attempt_id INTEGER NOT NULL, missing_ordinal INTEGER NOT NULL CHECK (missing_ordinal BETWEEN 1 AND 6), fact_id TEXT NOT NULL,
    PRIMARY KEY (attempt_id, missing_ordinal, fact_id),
    FOREIGN KEY (attempt_id, missing_ordinal) REFERENCES investigation_missing_evidence(attempt_id, ordinal) ON DELETE CASCADE,
    FOREIGN KEY (attempt_id, fact_id) REFERENCES investigation_evidence_facts(attempt_id, fact_id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS investigation_recommended_diagnostic_checks (
    attempt_id INTEGER NOT NULL REFERENCES investigation_results(attempt_id) ON DELETE CASCADE,
    rank INTEGER NOT NULL CHECK (rank BETWEEN 1 AND 6),
    check_type TEXT NOT NULL CHECK (check_type IN ('inspect_retained_metrics','verify_service_state','verify_authentication_state','verify_network_or_dependency','verify_change_or_maintenance_context','compare_fleet','collect_additional_observation')),
    text_kind TEXT NOT NULL CHECK (text_kind = 'untrusted_diagnostic_check'),
    text TEXT NOT NULL CHECK (length(CAST(text AS BLOB)) BETWEEN 1 AND 960 AND text = trim(text) AND text NOT GLOB '*[^ -~]*' AND instr(text, '/') = 0 AND instr(text, char(92)) = 0 AND instr(text, '<') = 0 AND instr(text, '>') = 0 AND instr(text, '@') = 0 AND instr(text, char(96)) = 0),
    PRIMARY KEY (attempt_id, rank)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS investigation_recommended_diagnostic_check_facts (
    attempt_id INTEGER NOT NULL, check_rank INTEGER NOT NULL CHECK (check_rank BETWEEN 1 AND 6), fact_id TEXT NOT NULL,
    PRIMARY KEY (attempt_id, check_rank, fact_id),
    FOREIGN KEY (attempt_id, check_rank) REFERENCES investigation_recommended_diagnostic_checks(attempt_id, rank) ON DELETE CASCADE,
    FOREIGN KEY (attempt_id, fact_id) REFERENCES investigation_evidence_facts(attempt_id, fact_id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS investigation_recommended_diagnostic_check_hypotheses (
    attempt_id INTEGER NOT NULL, check_rank INTEGER NOT NULL CHECK (check_rank BETWEEN 1 AND 6), hypothesis_rank INTEGER NOT NULL CHECK (hypothesis_rank BETWEEN 1 AND 5),
    PRIMARY KEY (attempt_id, check_rank, hypothesis_rank),
    FOREIGN KEY (attempt_id, check_rank) REFERENCES investigation_recommended_diagnostic_checks(attempt_id, rank) ON DELETE CASCADE,
    FOREIGN KEY (attempt_id, hypothesis_rank) REFERENCES investigation_hypotheses(attempt_id, rank) ON DELETE CASCADE
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS investigation_provenance (
    attempt_id INTEGER PRIMARY KEY REFERENCES investigation_attempts(id) ON DELETE CASCADE,
    provider_profile TEXT NOT NULL CHECK (provider_profile = 'openai_responses'),
    provider_endpoint TEXT NOT NULL CHECK (provider_endpoint = 'https://api.openai.com/v1/responses'),
    requested_model TEXT NOT NULL CHECK (requested_model = 'gpt-6-astra'),
    response_format TEXT NOT NULL CHECK (response_format = 'anomaly_investigation_v1'), store INTEGER NOT NULL CHECK (store = 0),
    send_authorized_at_ms INTEGER NOT NULL, send_completed_at_ms INTEGER NOT NULL CHECK (send_completed_at_ms >= send_authorized_at_ms),
    request_header_bytes INTEGER NOT NULL CHECK (request_header_bytes > 0 AND request_header_bytes <= 16384),
    request_body_bytes INTEGER NOT NULL CHECK (request_body_bytes > 0 AND request_body_bytes <= 16384),
    response_header_bytes INTEGER NOT NULL CHECK (response_header_bytes > 0 AND response_header_bytes <= 16384),
    response_body_bytes INTEGER NOT NULL CHECK (response_body_bytes > 0 AND response_body_bytes <= 32768),
    validation_outcome TEXT NOT NULL CHECK (validation_outcome IN ('accepted','insufficient_evidence'))
);
CREATE TRIGGER IF NOT EXISTS investigation_attempts_evidence_unavailable_insert_rejected BEFORE INSERT ON investigation_attempts
WHEN NEW.state = 'insufficient_evidence' AND NEW.terminal_reason = 'evidence_unavailable'
BEGIN SELECT RAISE(ABORT, 'evidence_unavailable must follow unavailable evidence'); END;
CREATE TRIGGER IF NOT EXISTS investigation_attempts_evidence_unavailable_consistent BEFORE UPDATE OF state, terminal_reason ON investigation_attempts
WHEN NEW.state = 'insufficient_evidence' AND NEW.terminal_reason = 'evidence_unavailable'
BEGIN
    SELECT CASE WHEN NOT EXISTS (SELECT 1 FROM investigation_evidence WHERE attempt_id = NEW.id AND snapshot_kind = 'unavailable') THEN RAISE(ABORT, 'evidence_unavailable requires unavailable evidence') END;
    SELECT CASE WHEN EXISTS (SELECT 1 FROM investigation_results WHERE attempt_id = NEW.id) OR EXISTS (SELECT 1 FROM investigation_provenance WHERE attempt_id = NEW.id) THEN RAISE(ABORT, 'evidence_unavailable cannot have result or provenance') END;
END;
CREATE TRIGGER IF NOT EXISTS investigation_results_evidence_unavailable_rejected BEFORE INSERT ON investigation_results
WHEN EXISTS (SELECT 1 FROM investigation_attempts WHERE id = NEW.attempt_id AND state = 'insufficient_evidence' AND terminal_reason = 'evidence_unavailable')
BEGIN SELECT RAISE(ABORT, 'evidence_unavailable cannot have result'); END;
CREATE TRIGGER IF NOT EXISTS investigation_provenance_evidence_unavailable_rejected BEFORE INSERT ON investigation_provenance
WHEN EXISTS (SELECT 1 FROM investigation_attempts WHERE id = NEW.attempt_id AND state = 'insufficient_evidence' AND terminal_reason = 'evidence_unavailable')
BEGIN SELECT RAISE(ABORT, 'evidence_unavailable cannot have provenance'); END;
CREATE TRIGGER IF NOT EXISTS investigation_provenance_send_timestamps_required BEFORE INSERT ON investigation_provenance
WHEN NOT EXISTS (SELECT 1 FROM investigation_attempts WHERE id = NEW.attempt_id AND send_authorized_at_ms = NEW.send_authorized_at_ms AND send_completed_at_ms = NEW.send_completed_at_ms)
BEGIN SELECT RAISE(ABORT, 'provenance requires completed recorded send'); END;

CREATE TABLE IF NOT EXISTS investigation_privacy_acknowledgements (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    acknowledgement_version TEXT NOT NULL CHECK (acknowledgement_version = 'openai_responses_privacy_v1'),
    actor TEXT NOT NULL CHECK (length(CAST(actor AS BLOB)) BETWEEN 1 AND 320),
    accepted_at_ms INTEGER NOT NULL CHECK (accepted_at_ms > 0),
    clause_set_hash BLOB NOT NULL CHECK (clause_set_hash = X'dc6150c7a127e143eb2ff247e804564d2085f879300e73b76556a290666ecba0'),
    clause_set_marker TEXT NOT NULL CHECK (clause_set_marker = 'openai_responses_privacy_v1_complete_clauses')
);
CREATE INDEX IF NOT EXISTS investigation_privacy_acknowledgements_accepted_at ON investigation_privacy_acknowledgements(accepted_at_ms);
CREATE TRIGGER IF NOT EXISTS investigation_privacy_acknowledgements_immutable BEFORE UPDATE ON investigation_privacy_acknowledgements
BEGIN SELECT RAISE(ABORT, 'investigation privacy acknowledgement is immutable'); END;

CREATE TABLE IF NOT EXISTS session_drop_anomalies (
    id INTEGER PRIMARY KEY AUTOINCREMENT, host TEXT NOT NULL, report_epoch_ms INTEGER NOT NULL, accepted_at_ms INTEGER NOT NULL,
    local_offset_minutes INTEGER NOT NULL CHECK (local_offset_minutes BETWEEN -840 AND 840), local_date TEXT NOT NULL CHECK (length(local_date) = 10),
    detected_at_ms INTEGER NOT NULL, confirmation_started_report_epoch_ms INTEGER NOT NULL, confirmation_ended_report_epoch_ms INTEGER NOT NULL,
    confirmation_started_at_ms INTEGER NOT NULL, confirmation_ended_at_ms INTEGER NOT NULL,
    observed_sessions INTEGER NOT NULL CHECK (observed_sessions >= 0), reference_sessions INTEGER NOT NULL CHECK (reference_sessions >= 0),
    expected_sessions REAL NOT NULL CHECK (expected_sessions >= 0), baseline_model_version TEXT NOT NULL CHECK (baseline_model_version = 'gamma_poisson_lower_v1'),
    baseline_scope TEXT NOT NULL CHECK (baseline_scope IN ('slot','all_hours')), slot_index INTEGER CHECK (slot_index BETWEEN 0 AND 95),
    slot_mature_days INTEGER CHECK (slot_mature_days >= 7), tail_probability REAL NOT NULL CHECK (tail_probability >= 0 AND tail_probability <= 1),
    absolute_loss INTEGER NOT NULL CHECK (absolute_loss >= 0), relative_loss REAL NOT NULL CHECK (relative_loss >= 0 AND relative_loss <= 1),
    confirmation_window_size INTEGER NOT NULL CHECK (confirmation_window_size IN (2,3)),
    confirmation_1_candidate INTEGER NOT NULL CHECK (confirmation_1_candidate IN (0,1)), confirmation_2_candidate INTEGER NOT NULL CHECK (confirmation_2_candidate IN (0,1)),
    confirmation_3_candidate INTEGER CHECK (confirmation_3_candidate IN (0,1)), confirmation_count INTEGER NOT NULL CHECK (confirmation_count BETWEEN 2 AND confirmation_window_size),
    freshness_context TEXT NOT NULL CHECK (freshness_context = 'fresh'), drain_context TEXT NOT NULL CHECK (drain_context IN ('none','overlap','post_horizon','unknown')),
    classification TEXT NOT NULL CHECK (classification IN ('unexplained','drain_associated','unknown_context')), provider_eligible INTEGER NOT NULL CHECK (provider_eligible IN (0,1)),
    CHECK ((baseline_scope = 'slot' AND slot_index IS NOT NULL AND slot_mature_days IS NOT NULL) OR (baseline_scope = 'all_hours' AND slot_index IS NULL AND slot_mature_days IS NULL)),
    CHECK (confirmation_started_report_epoch_ms <= confirmation_ended_report_epoch_ms AND report_epoch_ms = confirmation_ended_report_epoch_ms),
    CHECK (confirmation_started_at_ms <= confirmation_ended_at_ms AND accepted_at_ms = confirmation_ended_at_ms),
    CHECK ((confirmation_window_size = 2) = (confirmation_3_candidate IS NULL)),
    CHECK (confirmation_count = confirmation_1_candidate + confirmation_2_candidate + COALESCE(confirmation_3_candidate, 0)),
    CHECK ((classification = 'unexplained' AND provider_eligible = 1 AND drain_context = 'none') OR (classification = 'drain_associated' AND provider_eligible = 0 AND drain_context IN ('overlap','post_horizon')) OR (classification = 'unknown_context' AND provider_eligible = 0 AND drain_context = 'unknown')),
    UNIQUE (host, confirmation_ended_report_epoch_ms)
);
CREATE INDEX IF NOT EXISTS session_drop_anomalies_host_time ON session_drop_anomalies(host, detected_at_ms DESC, id DESC);
CREATE INDEX IF NOT EXISTS session_drop_anomalies_eligible_time ON session_drop_anomalies(provider_eligible, detected_at_ms DESC, id DESC);
CREATE TABLE IF NOT EXISTS session_drop_baselines (
    id INTEGER PRIMARY KEY AUTOINCREMENT, host TEXT NOT NULL CHECK (length(host) > 0), scope TEXT NOT NULL CHECK (scope IN ('slot','all_hours')),
    slot_index INTEGER CHECK (slot_index BETWEEN 0 AND 95), model_version TEXT NOT NULL CHECK (model_version = 'gamma_poisson_lower_v1'),
    alpha REAL NOT NULL CHECK (alpha >= 1.0), beta REAL NOT NULL CHECK (beta >= 1.0), observation_count INTEGER NOT NULL DEFAULT 0 CHECK (observation_count >= 0),
    first_trained_at_ms INTEGER, last_normal_trained_at_ms INTEGER, last_updated_at_ms INTEGER NOT NULL,
    CHECK ((scope = 'slot' AND slot_index IS NOT NULL) OR (scope = 'all_hours' AND slot_index IS NULL)),
    CHECK ((observation_count = 0 AND first_trained_at_ms IS NULL AND last_normal_trained_at_ms IS NULL) OR (observation_count > 0 AND first_trained_at_ms IS NOT NULL AND last_normal_trained_at_ms IS NOT NULL AND last_normal_trained_at_ms >= first_trained_at_ms))
);
CREATE UNIQUE INDEX IF NOT EXISTS session_drop_baselines_host_slot ON session_drop_baselines(host, slot_index) WHERE scope = 'slot';
CREATE UNIQUE INDEX IF NOT EXISTS session_drop_baselines_host_all_hours ON session_drop_baselines(host) WHERE scope = 'all_hours';
CREATE TABLE IF NOT EXISTS session_drop_baseline_days (
    baseline_id INTEGER NOT NULL REFERENCES session_drop_baselines(id) ON DELETE CASCADE,
    local_date TEXT NOT NULL CHECK (length(local_date) = 10), PRIMARY KEY (baseline_id, local_date)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS session_drop_detector_state (
    host TEXT PRIMARY KEY, last_scored_report_epoch_ms INTEGER NOT NULL DEFAULT 0, last_reference_total INTEGER CHECK (last_reference_total IS NULL OR last_reference_total >= 0),
    cooldown_until_ms INTEGER NOT NULL DEFAULT 0, post_drain_remaining INTEGER NOT NULL DEFAULT 0 CHECK (post_drain_remaining BETWEEN 0 AND 3),
    confirmation_1_report_epoch_ms INTEGER, confirmation_1_candidate INTEGER CHECK (confirmation_1_candidate IN (0,1)), confirmation_1_context TEXT CHECK (confirmation_1_context IN ('none','overlap','post_horizon','unknown')), confirmation_1_accepted_at_ms INTEGER,
    confirmation_2_report_epoch_ms INTEGER, confirmation_2_candidate INTEGER CHECK (confirmation_2_candidate IN (0,1)), confirmation_2_context TEXT CHECK (confirmation_2_context IN ('none','overlap','post_horizon','unknown')), confirmation_2_accepted_at_ms INTEGER,
    confirmation_3_report_epoch_ms INTEGER, confirmation_3_candidate INTEGER CHECK (confirmation_3_candidate IN (0,1)), confirmation_3_context TEXT CHECK (confirmation_3_context IN ('none','overlap','post_horizon','unknown')), confirmation_3_accepted_at_ms INTEGER,
    last_gap_reason TEXT NOT NULL DEFAULT '' CHECK (last_gap_reason IN ('','nil_enumeration','invalid_enumeration','missing_report_epoch','duplicate_report_epoch','out_of_order','stale','freshness_unknown')),
    state_updated_at_ms INTEGER NOT NULL,
    CHECK ((confirmation_1_report_epoch_ms IS NULL) = (confirmation_1_candidate IS NULL) AND (confirmation_1_report_epoch_ms IS NULL) = (confirmation_1_context IS NULL) AND (confirmation_1_report_epoch_ms IS NULL) = (confirmation_1_accepted_at_ms IS NULL)),
    CHECK ((confirmation_2_report_epoch_ms IS NULL) = (confirmation_2_candidate IS NULL) AND (confirmation_2_report_epoch_ms IS NULL) = (confirmation_2_context IS NULL) AND (confirmation_2_report_epoch_ms IS NULL) = (confirmation_2_accepted_at_ms IS NULL)),
    CHECK ((confirmation_3_report_epoch_ms IS NULL) = (confirmation_3_candidate IS NULL) AND (confirmation_3_report_epoch_ms IS NULL) = (confirmation_3_context IS NULL) AND (confirmation_3_report_epoch_ms IS NULL) = (confirmation_3_accepted_at_ms IS NULL))
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS session_drop_detector_cooldown ON session_drop_detector_state(cooldown_until_ms);
CREATE TABLE IF NOT EXISTS session_drop_observation_inbox (
    accepted_sequence INTEGER PRIMARY KEY AUTOINCREMENT, canonical_host TEXT NOT NULL CHECK (length(canonical_host) > 0),
    report_epoch_ms INTEGER NOT NULL CHECK (report_epoch_ms > 0), accepted_at_ms INTEGER NOT NULL CHECK (accepted_at_ms > 0),
    local_offset_minutes INTEGER NOT NULL CHECK (local_offset_minutes BETWEEN -840 AND 840), local_date TEXT NOT NULL CHECK (length(local_date) = 10),
    session_presence TEXT NOT NULL CHECK (session_presence IN ('present','nil','invalid','unavailable')),
    active_sessions INTEGER CHECK (active_sessions >= 0), disconnected_sessions INTEGER CHECK (disconnected_sessions >= 0), total_sessions INTEGER CHECK (total_sessions >= 0),
    freshness_context TEXT NOT NULL CHECK (freshness_context IN ('fresh','stale','unknown')),
    drain_context TEXT NOT NULL CHECK (drain_context IN ('none','overlap','post_horizon','unknown')),
    classification_context TEXT NOT NULL CHECK (classification_context IN ('unexplained','drain_associated','unknown_context','not_scored')),
    CHECK ((session_presence = 'present' AND active_sessions IS NOT NULL AND disconnected_sessions IS NOT NULL AND total_sessions = active_sessions + disconnected_sessions) OR (session_presence <> 'present' AND active_sessions IS NULL AND disconnected_sessions IS NULL AND total_sessions IS NULL)),
    UNIQUE (canonical_host, report_epoch_ms)
);
`

// splitStatements splits semicolon-delimited DDL into non-empty statements.
// It is used only by migration scripts whose semicolons are terminators.
func splitStatements(s string) []string {
	parts := strings.Split(s, ";")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if stmt := strings.TrimSpace(part); stmt != "" {
			out = append(out, stmt)
		}
	}
	return out
}
