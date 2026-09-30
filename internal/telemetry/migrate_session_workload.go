//go:build windows

package telemetry

import "database/sql"

const sessionWorkloadSchemaV5DDL = `
CREATE TABLE IF NOT EXISTS session_workload_raw (
    bucket_ts INTEGER NOT NULL,
    canonical_host TEXT NOT NULL,
    base_sample_count INTEGER NOT NULL,
    successful_empty_count INTEGER NOT NULL,
    error_sample_count INTEGER NOT NULL,
    cpu_sum REAL NOT NULL,
    cpu_count INTEGER NOT NULL,
    cpu_histogram BLOB NOT NULL,
    cpu_ge_5_sum REAL NOT NULL,
    cpu_ge_20_sum REAL NOT NULL,
    cpu_observed_series BLOB NOT NULL,
    cpu_ge_5_series BLOB NOT NULL,
    cpu_ge_20_series BLOB NOT NULL,
    memory_sum_bytes REAL NOT NULL,
    memory_count INTEGER NOT NULL,
    memory_zero_count INTEGER NOT NULL,
    memory_histogram BLOB NOT NULL,
    PRIMARY KEY (canonical_host, bucket_ts)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS session_workload_raw_ts ON session_workload_raw(bucket_ts);

CREATE TABLE IF NOT EXISTS session_workload_5min (
    bucket_ts INTEGER NOT NULL,
    canonical_host TEXT NOT NULL,
    base_sample_count INTEGER NOT NULL,
    successful_empty_count INTEGER NOT NULL,
    error_sample_count INTEGER NOT NULL,
    cpu_sum REAL NOT NULL,
    cpu_count INTEGER NOT NULL,
    cpu_histogram BLOB NOT NULL,
    cpu_ge_5_sum REAL NOT NULL,
    cpu_ge_20_sum REAL NOT NULL,
    cpu_observed_series BLOB NOT NULL,
    cpu_ge_5_series BLOB NOT NULL,
    cpu_ge_20_series BLOB NOT NULL,
    memory_sum_bytes REAL NOT NULL,
    memory_count INTEGER NOT NULL,
    memory_zero_count INTEGER NOT NULL,
    memory_histogram BLOB NOT NULL,
    PRIMARY KEY (canonical_host, bucket_ts)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS session_workload_5min_ts ON session_workload_5min(bucket_ts);

CREATE TABLE IF NOT EXISTS session_workload_hourly (
    bucket_ts INTEGER NOT NULL,
    canonical_host TEXT NOT NULL,
    base_sample_count INTEGER NOT NULL,
    successful_empty_count INTEGER NOT NULL,
    error_sample_count INTEGER NOT NULL,
    cpu_sum REAL NOT NULL,
    cpu_count INTEGER NOT NULL,
    cpu_histogram BLOB NOT NULL,
    cpu_ge_5_sum REAL NOT NULL,
    cpu_ge_20_sum REAL NOT NULL,
    cpu_observed_series BLOB NOT NULL,
    cpu_ge_5_series BLOB NOT NULL,
    cpu_ge_20_series BLOB NOT NULL,
    memory_sum_bytes REAL NOT NULL,
    memory_count INTEGER NOT NULL,
    memory_zero_count INTEGER NOT NULL,
    memory_histogram BLOB NOT NULL,
    PRIMARY KEY (canonical_host, bucket_ts)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS session_workload_hourly_ts ON session_workload_hourly(bucket_ts);
`

func migrateSessionWorkloadV5(tx *sql.Tx) error {
	for _, stmt := range splitStatements(sessionWorkloadSchemaV5DDL) {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}
