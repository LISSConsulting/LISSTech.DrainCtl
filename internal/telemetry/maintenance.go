//go:build windows

package telemetry

import (
	"context"
	"time"
)

// Result captures the outcome of a single maintenance job run. Callers
// populate Started and Finished as UTC wall-clock; UpsertJob derives
// duration_ms from the difference.
type Result struct {
	Started      time.Time
	Finished     time.Time
	Outcome      string // "success" | "failure" | "skipped"
	Reason       string
	RowsAffected int64
}

// Job is a persisted maintenance_jobs row as returned by ListJobs.
type Job struct {
	Name         string
	Started      time.Time
	Finished     time.Time
	DurationMs   int64
	Outcome      string
	Reason       string
	RowsAffected int64
}

// MaintenanceStore reads and writes the maintenance_jobs table. Writes go
// through the writer pool; reads use the reader pool.
type MaintenanceStore struct {
	db *DB
}

// NewMaintenanceStore returns a MaintenanceStore bound to db.
func NewMaintenanceStore(db *DB) *MaintenanceStore {
	return &MaintenanceStore{db: db}
}

// UpsertJob writes (or overwrites) the maintenance_jobs row named `name`
// with the given run. There is exactly one row per name (PK), so every
// successful call leaves `last run` as the only record for that job.
func (s *MaintenanceStore) UpsertJob(ctx context.Context, name string, run Result) error {
	startMs := run.Started.UTC().UnixMilli()
	finishMs := run.Finished.UTC().UnixMilli()
	durMs := finishMs - startMs
	if durMs < 0 {
		durMs = 0
	}
	_, err := s.db.writer.ExecContext(ctx,
		`INSERT INTO maintenance_jobs (name, started_ts, finished_ts, duration_ms, outcome, reason, rows_affected)
         VALUES (?, ?, ?, ?, ?, ?, ?)
         ON CONFLICT(name) DO UPDATE SET
            started_ts=excluded.started_ts,
            finished_ts=excluded.finished_ts,
            duration_ms=excluded.duration_ms,
            outcome=excluded.outcome,
            reason=excluded.reason,
            rows_affected=excluded.rows_affected`,
		name, startMs, finishMs, durMs, run.Outcome, run.Reason, run.RowsAffected)
	return err
}

// ListJobs returns every maintenance_jobs row ordered by name for stable
// dashboard rendering.
func (s *MaintenanceStore) ListJobs(ctx context.Context) ([]Job, error) {
	rows, err := s.db.reader.QueryContext(ctx,
		`SELECT name, started_ts, finished_ts, duration_ms, outcome, reason, rows_affected
         FROM maintenance_jobs ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var jobs []Job
	for rows.Next() {
		var (
			j                 Job
			startMs, finishMs int64
		)
		if err := rows.Scan(&j.Name, &startMs, &finishMs, &j.DurationMs, &j.Outcome, &j.Reason, &j.RowsAffected); err != nil {
			return nil, err
		}
		j.Started = time.UnixMilli(startMs).UTC()
		j.Finished = time.UnixMilli(finishMs).UTC()
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return jobs, nil
}
