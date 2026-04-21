//go:build windows

package telemetry

import (
	"database/sql"
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
`

// applySchema runs the DDL block inside a transaction and stamps user_version = 1.
// Idempotent: every statement uses IF NOT EXISTS.
func applySchema(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	for _, stmt := range splitStatements(ddl) {
		if _, err = tx.Exec(stmt); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("PRAGMA user_version = 1"); err != nil {
		return err
	}
	return tx.Commit()
}

// splitStatements splits a semicolon-delimited DDL string into individual
// non-empty statements. Safe for our DDL because semicolons appear only as
// statement terminators (no string literals contain semicolons).
func splitStatements(s string) []string {
	parts := strings.Split(s, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if stmt := strings.TrimSpace(p); stmt != "" {
			out = append(out, stmt)
		}
	}
	return out
}
