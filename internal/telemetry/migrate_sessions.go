//go:build windows

package telemetry

import "database/sql"

// sessionSchemaV4DDL is the Fleet Sessions schema. Session telemetry is
// current-state only: no history or EAV tables are created.
const sessionSchemaV4DDL = `
CREATE TABLE IF NOT EXISTS session_snapshots (
    canonical_host                   TEXT PRIMARY KEY,
    latest_attempt_instance_id       TEXT NOT NULL,
    latest_attempt_sequence          BLOB NOT NULL CHECK (length(latest_attempt_sequence) = 8),
    latest_attempt_observed_at_ms    INTEGER NOT NULL CHECK (latest_attempt_observed_at_ms >= 0),
    latest_attempt_received_at_ms    INTEGER NOT NULL CHECK (latest_attempt_received_at_ms >= 0),
    last_success_instance_id         TEXT,
    last_success_sequence            BLOB CHECK (last_success_sequence IS NULL OR length(last_success_sequence) = 8),
    last_success_observed_at_ms      INTEGER,
    last_success_received_at_ms      INTEGER,
    session_count                    INTEGER CHECK (session_count BETWEEN 0 AND 500),
    active_count                     INTEGER CHECK (active_count BETWEEN 0 AND 500),
    idle_count                       INTEGER CHECK (idle_count BETWEEN 0 AND 500),
    disconnected_count               INTEGER CHECK (disconnected_count BETWEEN 0 AND 500),
    user_count                       INTEGER CHECK (user_count BETWEEN 0 AND 500),
    last_activity_at_ms              INTEGER CHECK (last_activity_at_ms IS NULL OR last_activity_at_ms >= 0),
    collector_version                TEXT,
    logical_cpu_count                INTEGER CHECK (logical_cpu_count BETWEEN 1 AND 1024),
    capability_actions               INTEGER CHECK (capability_actions IN (0,1)),
    capability_processes             INTEGER CHECK (capability_processes IN (0,1)),
    capability_input_delay           INTEGER CHECK (capability_input_delay IN (0,1)),
    capability_remotefx              INTEGER CHECK (capability_remotefx IN (0,1)),
    latest_attempt_error_code        TEXT CHECK (latest_attempt_error_code IN
        ('wts_enumeration_failed','wts_metadata_failed','collector_timeout')),
    CHECK ((last_success_instance_id IS NULL) = (last_success_sequence IS NULL)),
    CHECK ((last_success_instance_id IS NULL) = (last_success_observed_at_ms IS NULL)),
    CHECK ((last_success_instance_id IS NULL) = (last_success_received_at_ms IS NULL)),
    CHECK (
        (last_success_instance_id IS NULL AND session_count IS NULL AND active_count IS NULL
         AND idle_count IS NULL AND disconnected_count IS NULL AND user_count IS NULL
         AND last_activity_at_ms IS NULL AND collector_version IS NULL AND logical_cpu_count IS NULL
         AND capability_actions IS NULL AND capability_processes IS NULL
         AND capability_input_delay IS NULL AND capability_remotefx IS NULL)
        OR
        (last_success_instance_id IS NOT NULL AND session_count IS NOT NULL AND active_count IS NOT NULL
         AND idle_count IS NOT NULL AND disconnected_count IS NOT NULL AND user_count IS NOT NULL
         AND collector_version IS NOT NULL AND logical_cpu_count IS NOT NULL
         AND capability_actions IS NOT NULL AND capability_processes IS NOT NULL
         AND capability_input_delay IS NOT NULL AND capability_remotefx IS NOT NULL)
    )
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS session_latest (
    canonical_host        TEXT NOT NULL,
    session_id            INTEGER NOT NULL CHECK (session_id BETWEEN 0 AND 4294967295),
    logon_at_ms           INTEGER CHECK (logon_at_ms IS NULL OR logon_at_ms >= 0),
    user_name             TEXT,
    domain_name           TEXT,
    state                 TEXT NOT NULL CHECK (state IN
        ('active','connected','connect_query','shadow','disconnected','idle','listen','reset','down','init','unknown')),
    station               TEXT,
    client_name           TEXT,
    client_address        TEXT,
    connect_at_ms         INTEGER CHECK (connect_at_ms IS NULL OR connect_at_ms >= 0),
    disconnect_at_ms      INTEGER CHECK (disconnect_at_ms IS NULL OR disconnect_at_ms >= 0),
    idle_since_ms         INTEGER CHECK (idle_since_ms IS NULL OR idle_since_ms >= 0),
    cpu_percent           REAL,
    working_set_bytes     INTEGER CHECK (working_set_bytes IS NULL OR working_set_bytes BETWEEN 0 AND 9223372036854775807),
    input_delay_ms        INTEGER CHECK (input_delay_ms IS NULL OR input_delay_ms >= 0),
    remotefx_fps          REAL,
    remotefx_quality_pct  REAL,
    remotefx_encode_ms    REAL,
    remotefx_rtt_ms       REAL,
    remotefx_loss_pct     REAL,
    remotefx_server_skip  REAL,
    remotefx_network_skip REAL,
    processes_json        TEXT NOT NULL DEFAULT '[]',
    PRIMARY KEY (canonical_host, session_id),
    FOREIGN KEY (canonical_host) REFERENCES session_snapshots(canonical_host) ON DELETE CASCADE
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS session_snapshots_last_success_received_idx
    ON session_snapshots(last_success_received_at_ms);
CREATE INDEX IF NOT EXISTS session_snapshots_fleet_aggregates_idx
    ON session_snapshots(session_count, active_count, idle_count, disconnected_count, user_count, last_activity_at_ms, canonical_host);
CREATE INDEX IF NOT EXISTS session_latest_host_state_idx
    ON session_latest(canonical_host, state, session_id);
CREATE INDEX IF NOT EXISTS session_latest_state_host_idx
    ON session_latest(state, canonical_host, session_id);

CREATE TABLE IF NOT EXISTS session_generation_fence (
    canonical_host  TEXT PRIMARY KEY,
    max_instance_id BLOB NOT NULL CHECK (length(max_instance_id) = 16)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS session_action_outbox (
    action_id              TEXT PRIMARY KEY,
    canonical_host         TEXT NOT NULL,
    session_id             INTEGER NOT NULL CHECK (session_id BETWEEN 0 AND 4294967295),
    expected_logon_at_ms   INTEGER NOT NULL CHECK (expected_logon_at_ms >= 0),
    action_type            TEXT NOT NULL CHECK (action_type IN ('logoff','message','disconnect')),
    message_ciphertext     BLOB,
    message_protection     TEXT,
    requested_by           TEXT NOT NULL,
    idempotency_endpoint   TEXT NOT NULL,
    idempotency_key        TEXT NOT NULL,
    request_fingerprint    BLOB NOT NULL,
    created_at_ms          INTEGER NOT NULL CHECK (created_at_ms >= 0),
    expires_at_ms          INTEGER NOT NULL CHECK (expires_at_ms >= 0),
    state                  TEXT NOT NULL CHECK (state IN
        ('queued','delivered','completed','failed','expired','session_changed','unsupported')),
    delivered_at_ms        INTEGER CHECK (delivered_at_ms IS NULL OR delivered_at_ms >= 0),
    completed_at_ms        INTEGER CHECK (completed_at_ms IS NULL OR completed_at_ms >= 0),
    result_code            TEXT,
    CHECK (expires_at_ms = created_at_ms + 300000),
    CHECK ((state = 'queued') = (delivered_at_ms IS NULL)),
    CHECK ((state IN ('queued','delivered')) = (completed_at_ms IS NULL)),
    CHECK ((state IN ('queued','delivered')) = (result_code IS NULL)),
    CHECK (action_type = 'message' OR (message_ciphertext IS NULL AND message_protection IS NULL)),
    CHECK (
        action_type != 'message'
        OR (state IN ('queued','delivered') AND message_ciphertext IS NOT NULL AND message_protection IS NOT NULL)
        OR (state NOT IN ('queued','delivered') AND (message_ciphertext IS NULL) = (message_protection IS NULL))
    )
) WITHOUT ROWID;
CREATE UNIQUE INDEX IF NOT EXISTS session_action_outbox_idempotency_idx
    ON session_action_outbox(requested_by, idempotency_endpoint, idempotency_key);
CREATE INDEX IF NOT EXISTS session_action_outbox_delivery_idx
    ON session_action_outbox(canonical_host, state, expires_at_ms, created_at_ms);
CREATE INDEX IF NOT EXISTS session_action_outbox_expiry_idx
    ON session_action_outbox(state, expires_at_ms);

CREATE TABLE IF NOT EXISTS session_action_audit (
    event_id               TEXT PRIMARY KEY,
    event_at_ms            INTEGER NOT NULL CHECK (event_at_ms >= 0),
    action_id              TEXT NOT NULL,
    action_type            TEXT NOT NULL CHECK (action_type IN ('logoff','message','disconnect')),
    canonical_host         TEXT NOT NULL,
    session_id             INTEGER NOT NULL CHECK (session_id BETWEEN 0 AND 4294967295),
    expected_logon_at_ms   INTEGER NOT NULL CHECK (expected_logon_at_ms >= 0),
    transition             TEXT NOT NULL CHECK (transition IN
        ('queued','delivered','completed','failed','expired','session_changed','unsupported','protocol_invalid')),
    result_code            TEXT,
    requested_by           TEXT,
    CHECK ((transition = 'queued') = (requested_by IS NOT NULL)),
    CHECK ((transition IN ('queued','delivered')) = (result_code IS NULL))
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS session_action_audit_action_idx ON session_action_audit(action_id, event_at_ms);
CREATE INDEX IF NOT EXISTS session_action_audit_retention_idx ON session_action_audit(event_at_ms);

CREATE TABLE IF NOT EXISTS session_action_ledger (
    action_id       TEXT PRIMARY KEY,
    state           TEXT NOT NULL CHECK (state IN ('claimed','terminal')),
    outcome         TEXT CHECK (outcome IN
        ('completed','failed','expired','session_changed','unsupported','duplicate')),
    claimed_at_ms   INTEGER NOT NULL CHECK (claimed_at_ms >= 0),
    completed_at_ms INTEGER CHECK (completed_at_ms IS NULL OR completed_at_ms >= 0),
    expires_at_ms   INTEGER NOT NULL CHECK (expires_at_ms >= 0),
    CHECK ((state = 'claimed' AND outcome IS NULL AND completed_at_ms IS NULL)
        OR (state = 'terminal' AND outcome IS NOT NULL AND completed_at_ms IS NOT NULL))
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS session_action_ledger_cleanup_idx
    ON session_action_ledger(state, completed_at_ms, expires_at_ms);
`

// migrateSessionsV4 creates the current-state Fleet Sessions tables and their
// indexes inside the caller's schema transaction. The DDL is idempotent so an
// interrupted prior migration and an existing v4 database both converge.
func migrateSessionsV4(tx *sql.Tx) error {
	for _, stmt := range splitStatements(sessionSchemaV4DDL) {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}
