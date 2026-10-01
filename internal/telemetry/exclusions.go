//go:build windows

package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// RemovalEntry is the telemetry-layer representation of a permanently-removed
// server. The dashboard JSON-decodes it into RemovedServer for clients.
// ExcludedBy / Reason may be empty; the dashboard surfaces them when present.
type RemovalEntry struct {
	Hostname   string
	ExcludedAt time.Time
	ExcludedBy string
	Reason     string
}

// RemovalStore persists durable tombstones for permanently-removed servers
// (a.k.a. exclusions). A row in server_exclusions causes Register and Report
// to reject subsequent agent traffic until an operator issues Restore.
//
// Storing the tombstone as a separate table — rather than flagging the
// `servers` row — keeps the live roster query distinct from the exclusion
// query: GET /api/v1/servers remains the cheap live-roster path, and the
// new GET /api/v1/servers/removed walks server_exclusions alone.
//
// Retention does not apply to server_exclusions; rows persist until the
// operator explicitly restores or until the operator purges them via a
// future "purge" endpoint (out of scope for this commit).
type RemovalStore struct {
	db *DB

	// beforeRegisterInsert is a test seam that runs after the tombstone
	// predicate and before the register insert inside its transaction.
	beforeRegisterInsert func()
}

// NewRemovalStore wraps an open DB.
func NewRemovalStore(db *DB) *RemovalStore {
	return &RemovalStore{db: db}
}

// Exclude inserts (or refreshes a tombstone. Canonical hostnames prevent a
// mixed-case retry from creating a second identity.
func (s *RemovalStore) Exclude(ctx context.Context, hostname, by, reason string) error {
	hostname = CanonicalHostname(hostname)
	nowMs := time.Now().UTC().UnixMilli()
	if _, err := s.db.writer.ExecContext(ctx,
		`DELETE FROM server_exclusions WHERE hostname = ? COLLATE NOCASE`, hostname); err != nil {
		return fmt.Errorf("telemetry: exclusions clear case variant: %w", err)
	}
	_, err := s.db.writer.ExecContext(ctx,
		`INSERT INTO server_exclusions (hostname, excluded_at_ms, excluded_by, reason)
		 VALUES (?, ?, ?, ?)`,
		hostname, nowMs, by, reason)
	if err != nil {
		return fmt.Errorf("telemetry: exclusions exclude: %w", err)
	}
	return nil
}

// Restore deletes the tombstone for hostname. Returns true when a row was
// removed. After Restore, the host is free to register again — the agent's
// next /api/v1/register creates a fresh `servers` row with a new
// registered_at_ms.
func (s *RemovalStore) Restore(ctx context.Context, hostname string) (bool, error) {
	res, err := s.db.writer.ExecContext(ctx,
		`DELETE FROM server_exclusions WHERE hostname = ? COLLATE NOCASE`, CanonicalHostname(hostname))
	if err != nil {
		return false, fmt.Errorf("telemetry: exclusions restore: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("telemetry: exclusions restore rows_affected: %w", err)
	}
	return n > 0, nil
}

// IsExcluded returns true when hostname has a tombstone. Used by the
// /api/v1/register and /api/v1/report handlers to reject automatic
// re-registration.
func (s *RemovalStore) IsExcluded(ctx context.Context, hostname string) (bool, error) {
	var one int
	err := s.db.reader.QueryRowContext(ctx,
		`SELECT 1 FROM server_exclusions WHERE hostname = ? COLLATE NOCASE`, CanonicalHostname(hostname)).Scan(&one)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("telemetry: exclusions is_excluded: %w", err)
	}
	return true, nil
}

// PermanentRemove atomically deletes a live server row and creates its durable
// tombstone. A failed tombstone write rolls back the deletion, so callers
// cannot leave a host eligible to re-register after a failed removal.
func (s *RemovalStore) PermanentRemove(ctx context.Context, hostname, by, reason string) error {
	hostname = CanonicalHostname(hostname)
	tx, err := s.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("telemetry: exclusions permanent remove begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `DELETE FROM servers WHERE hostname = ? COLLATE NOCASE`, hostname); err != nil {
		return fmt.Errorf("telemetry: exclusions permanent remove live row: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM server_exclusions WHERE hostname = ? COLLATE NOCASE`, hostname); err != nil {
		return fmt.Errorf("telemetry: exclusions permanent remove clear case variant: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO server_exclusions (hostname, excluded_at_ms, excluded_by, reason) VALUES (?, ?, ?, ?)`,
		hostname, time.Now().UTC().UnixMilli(), by, reason); err != nil {
		return fmt.Errorf("telemetry: exclusions permanent remove tombstone: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("telemetry: exclusions permanent remove commit: %w", err)
	}
	return nil
}

// RegisterIfNotExcluded atomically checks the tombstone predicate and creates
// the live row. The returned values distinguish a durable exclusion from a
// storage failure, so callers never acknowledge a registration that lost a
// concurrent permanent-remove race.
func (s *RemovalStore) RegisterIfNotExcluded(ctx context.Context, hostname string) (accepted, excluded bool, err error) {
	hostname = CanonicalHostname(hostname)
	tx, err := s.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return false, false, fmt.Errorf("telemetry: register begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var one int
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM server_exclusions WHERE hostname = ? COLLATE NOCASE`, hostname).Scan(&one)
	switch {
	case err == nil:
		return false, true, nil
	case !errors.Is(err, sql.ErrNoRows):
		return false, false, fmt.Errorf("telemetry: register exclusion check: %w", err)
	}
	if s.beforeRegisterInsert != nil {
		s.beforeRegisterInsert()
	}
	nowMs := time.Now().UTC().UnixMilli()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO servers (hostname, registered_at_ms, last_seen_ms, last_result_json)
		 SELECT ?, ?, 0, NULL
		 WHERE NOT EXISTS (SELECT 1 FROM servers WHERE hostname = ? COLLATE NOCASE)`,
		hostname, nowMs, hostname); err != nil {
		return false, false, fmt.Errorf("telemetry: register insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, false, fmt.Errorf("telemetry: register commit: %w", err)
	}
	return true, false, nil
}

// AllExcluded returns every tombstone sorted by excluded_at_ms DESC. The
// dashboard renders this list with newest removals first. The result is a
// fresh slice; safe for the caller to retain.
func (s *RemovalStore) AllExcluded(ctx context.Context) ([]RemovalEntry, error) {
	rows, err := s.db.reader.QueryContext(ctx,
		`SELECT hostname, excluded_at_ms, excluded_by, reason
		 FROM server_exclusions ORDER BY excluded_at_ms DESC, hostname ASC`)
	if err != nil {
		return nil, fmt.Errorf("telemetry: exclusions all: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []RemovalEntry{}
	for rows.Next() {
		var (
			host       string
			excludedMs int64
			by, reason string
		)
		if err := rows.Scan(&host, &excludedMs, &by, &reason); err != nil {
			return nil, fmt.Errorf("telemetry: exclusions all scan: %w", err)
		}
		out = append(out, RemovalEntry{
			Hostname:   host,
			ExcludedAt: time.UnixMilli(excludedMs).UTC(),
			ExcludedBy: by,
			Reason:     reason,
		})
	}
	return out, rows.Err()
}
