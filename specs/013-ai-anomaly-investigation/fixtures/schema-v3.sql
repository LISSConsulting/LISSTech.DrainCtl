-- Deterministic synthetic schema-v3 fixture for v3-to-v4 migration and rollback tests.
-- It contains no credential, customer, user, session, or production host data.
PRAGMA foreign_keys = ON;
PRAGMA user_version = 3;

CREATE TABLE schema_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) WITHOUT ROWID;

CREATE TABLE audit (
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
CREATE INDEX audit_host_ts ON audit(host, ts DESC);
CREATE INDEX audit_ts ON audit(ts DESC);
CREATE INDEX audit_principal ON audit(principal, ts DESC) WHERE principal <> '';

CREATE TABLE metrics_raw (
    ts      INTEGER NOT NULL,
    host    TEXT    NOT NULL,
    counter TEXT    NOT NULL,
    value   REAL    NOT NULL,
    PRIMARY KEY (host, ts, counter)
) WITHOUT ROWID;
CREATE INDEX metrics_raw_ts ON metrics_raw(ts);

CREATE TABLE metrics_5min (
    bucket_ts    INTEGER NOT NULL,
    host         TEXT    NOT NULL,
    counter      TEXT    NOT NULL,
    avg_value    REAL    NOT NULL,
    min_value    REAL    NOT NULL,
    max_value    REAL    NOT NULL,
    sample_count INTEGER NOT NULL,
    PRIMARY KEY (host, bucket_ts, counter)
) WITHOUT ROWID;
CREATE INDEX metrics_5min_ts ON metrics_5min(bucket_ts);

CREATE TABLE metrics_hourly (
    bucket_ts    INTEGER NOT NULL,
    host         TEXT    NOT NULL,
    counter      TEXT    NOT NULL,
    avg_value    REAL    NOT NULL,
    min_value    REAL    NOT NULL,
    max_value    REAL    NOT NULL,
    sample_count INTEGER NOT NULL,
    PRIMARY KEY (host, bucket_ts, counter)
) WITHOUT ROWID;
CREATE INDEX metrics_hourly_ts ON metrics_hourly(bucket_ts);

CREATE TABLE maintenance_jobs (
    name          TEXT PRIMARY KEY,
    started_ts    INTEGER NOT NULL,
    finished_ts   INTEGER NOT NULL,
    duration_ms   INTEGER NOT NULL,
    outcome       TEXT    NOT NULL CHECK (outcome IN ('success','failure','skipped')),
    reason        TEXT    NOT NULL DEFAULT '',
    rows_affected INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;

CREATE TABLE event_spikes (
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
CREATE UNIQUE INDEX event_spikes_identity ON event_spikes(host, channel, window_start_ms);
CREATE INDEX event_spikes_host_ts ON event_spikes(host, window_start_ms DESC);
CREATE INDEX event_spikes_ts ON event_spikes(window_start_ms);

CREATE TABLE servers (
    hostname         TEXT    PRIMARY KEY,
    registered_at_ms INTEGER NOT NULL,
    last_seen_ms     INTEGER NOT NULL DEFAULT 0,
    last_result_json TEXT
) WITHOUT ROWID;

CREATE TABLE server_exclusions (
    hostname        TEXT    PRIMARY KEY,
    excluded_at_ms  INTEGER NOT NULL,
    excluded_by     TEXT    NOT NULL DEFAULT '',
    reason          TEXT    NOT NULL DEFAULT ''
) WITHOUT ROWID;
CREATE INDEX server_exclusions_at ON server_exclusions(excluded_at_ms DESC);

CREATE TABLE host_freshness (
    host                  TEXT    PRIMARY KEY COLLATE NOCASE,
    report_epoch_ms       INTEGER NOT NULL,
    offline_emitted_at_ms INTEGER
) WITHOUT ROWID;

CREATE TABLE force_update_outbox (
    command_id     TEXT    NOT NULL,
    host           TEXT    NOT NULL,
    reason         TEXT    NOT NULL DEFAULT '',
    accepted_at_ms INTEGER NOT NULL,
    agent_version  TEXT    NOT NULL,
    PRIMARY KEY (host, command_id)
) WITHOUT ROWID;
CREATE INDEX force_update_outbox_pending ON force_update_outbox(host, accepted_at_ms, command_id);

INSERT INTO schema_meta(key, value) VALUES
    ('config_version', '3'),
    ('retention_days', '30');

INSERT INTO servers(hostname, registered_at_ms, last_seen_ms, last_result_json) VALUES
    ('registered-alpha', 1790400000000, 1790430680000,
     '{"schema_version":1,"report_epoch_ms":1790430680000,"status":"ok"}'),
    ('registered-beta', 1790400000000, 1790430670000,
     '{"schema_version":1,"report_epoch_ms":1790430670000,"status":"ok"}');

INSERT INTO host_freshness(host, report_epoch_ms, offline_emitted_at_ms) VALUES
    ('registered-alpha', 1790430680000, NULL),
    ('registered-beta', 1790430670000, NULL);

INSERT INTO metrics_raw(ts, host, counter, value) VALUES
    (1790430380000, 'registered-alpha', 'sessions.active', 48.0),
    (1790430680000, 'registered-alpha', 'sessions.active', 12.0),
    (1790430380000, 'registered-beta', 'sessions.active', 50.0);

INSERT INTO metrics_5min(bucket_ts, host, counter, avg_value, min_value, max_value, sample_count) VALUES
    (1790430600000, 'registered-alpha', 'sessions.active', 30.0, 12.0, 48.0, 2);
INSERT INTO metrics_hourly(bucket_ts, host, counter, avg_value, min_value, max_value, sample_count) VALUES
    (1790429200000, 'registered-alpha', 'sessions.active', 42.0, 12.0, 48.0, 12);

INSERT INTO event_spikes(host, channel, window_start_ms, window_end_ms, observed, expected, tail_probability, confirmation_count, first_seen_at_ms, created_at_ms) VALUES
    ('registered-alpha', 'agent', 1790430620000, 1790430680000, 12, 3.0, 0.000001, 3, 1790430679000, 1790430680000);

-- Audit rows represent the legacy drain transition history; they are retained
-- through migration and are not reinterpreted as provider or session-drop data.
INSERT INTO audit(ts, host, prev_state, new_state, principal, changed_by, reason, reconciliation) VALUES
    (1790430000000, 'registered-alpha', 0, 1, 'fixture-operator', 'fixture-operator', 'scheduled-drain', 0),
    (1790430300000, 'registered-alpha', 1, 0, 'fixture-operator', 'fixture-operator', 'drain-complete', 0);

INSERT INTO maintenance_jobs(name, started_ts, finished_ts, duration_ms, outcome, reason, rows_affected) VALUES
    ('retention', 1790430000000, 1790430000120, 120, 'success', 'fixture', 3);

INSERT INTO server_exclusions(hostname, excluded_at_ms, excluded_by, reason) VALUES
    ('retired-fixture-host', 1790420000000, 'fixture-operator', 'retention-fixture');

INSERT INTO force_update_outbox(command_id, host, reason, accepted_at_ms, agent_version) VALUES
    ('fixture-command-1', 'registered-beta', 'fixture', 1790430680000, '0.0.0-fixture');
