//go:build windows

package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ServerInfo is the telemetry-layer representation of a registered server.
// LastResultJSON is an opaque JSON blob — the dashboard layer marshals a
// drainctl.CheckResult into it so the telemetry package stays free of
// upward dependencies. Empty string means the server has never reported.
type ServerInfo struct {
	Hostname       string
	RegisteredAt   time.Time
	LastSeen       time.Time
	LastResultJSON string
}

// ServerStore persists the registered-server roster to drainctl.db. Replaces
// the legacy servers.json file with its atomic-rename dance. Retention does
// not apply — rows live until the operator issues DELETE /api/v1/servers/{host}.
type ServerStore struct {
	db *DB
}

// NewServerStore wraps an open DB.
func NewServerStore(db *DB) *ServerStore {
	return &ServerStore{db: db}
}

// Register inserts a new row when the hostname is unknown. INSERT OR IGNORE
// preserves the original registered_at on re-registration — matches the legacy
// ServerState.Register behaviour.
func (s *ServerStore) Register(ctx context.Context, hostname string) error {
	nowMs := time.Now().UTC().UnixMilli()
	_, err := s.db.writer.ExecContext(ctx,
		`INSERT OR IGNORE INTO servers (hostname, registered_at_ms, last_seen_ms, last_result_json)
		 VALUES (?, ?, 0, NULL)`,
		hostname, nowMs)
	if err != nil {
		return fmt.Errorf("telemetry: servers register: %w", err)
	}
	return nil
}

// Remove deletes the row for hostname. Returns (found, error).
func (s *ServerStore) Remove(ctx context.Context, hostname string) (bool, error) {
	res, err := s.db.writer.ExecContext(ctx, `DELETE FROM servers WHERE hostname = ?`, hostname)
	if err != nil {
		return false, fmt.Errorf("telemetry: servers remove: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("telemetry: servers remove rows_affected: %w", err)
	}
	return n > 0, nil
}

// IsRegistered returns true if hostname has a row.
func (s *ServerStore) IsRegistered(ctx context.Context, hostname string) (bool, error) {
	var one int
	err := s.db.reader.QueryRowContext(ctx,
		`SELECT 1 FROM servers WHERE hostname = ?`, hostname).Scan(&one)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("telemetry: servers is_registered: %w", err)
	}
	return true, nil
}

// Update records a new last_result JSON and bumps last_seen_ms. Returns
// (updated, error): updated=false means the hostname is not registered.
func (s *ServerStore) Update(ctx context.Context, hostname, lastResultJSON string) (bool, error) {
	nowMs := time.Now().UTC().UnixMilli()
	res, err := s.db.writer.ExecContext(ctx,
		`UPDATE servers SET last_seen_ms = ?, last_result_json = ? WHERE hostname = ?`,
		nowMs, lastResultJSON, hostname)
	if err != nil {
		return false, fmt.Errorf("telemetry: servers update: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("telemetry: servers update rows_affected: %w", err)
	}
	return n > 0, nil
}

// Get returns the row for hostname, or nil when unregistered.
func (s *ServerStore) Get(ctx context.Context, hostname string) (*ServerInfo, error) {
	var (
		registeredMs, lastSeenMs int64
		lastResultJSON           sql.NullString
	)
	err := s.db.reader.QueryRowContext(ctx,
		`SELECT registered_at_ms, last_seen_ms, last_result_json FROM servers WHERE hostname = ?`,
		hostname).Scan(&registeredMs, &lastSeenMs, &lastResultJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("telemetry: servers get: %w", err)
	}
	return &ServerInfo{
		Hostname:       hostname,
		RegisteredAt:   time.UnixMilli(registeredMs).UTC(),
		LastSeen:       optionalTime(lastSeenMs),
		LastResultJSON: lastResultJSON.String,
	}, nil
}

// All returns every row, sorted by hostname ascending.
func (s *ServerStore) All(ctx context.Context) ([]ServerInfo, error) {
	rows, err := s.db.reader.QueryContext(ctx,
		`SELECT hostname, registered_at_ms, last_seen_ms, last_result_json
		 FROM servers ORDER BY hostname ASC`)
	if err != nil {
		return nil, fmt.Errorf("telemetry: servers all: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []ServerInfo{}
	for rows.Next() {
		var (
			hostname                 string
			registeredMs, lastSeenMs int64
			lastResultJSON           sql.NullString
		)
		if err := rows.Scan(&hostname, &registeredMs, &lastSeenMs, &lastResultJSON); err != nil {
			return nil, fmt.Errorf("telemetry: servers all scan: %w", err)
		}
		out = append(out, ServerInfo{
			Hostname:       hostname,
			RegisteredAt:   time.UnixMilli(registeredMs).UTC(),
			LastSeen:       optionalTime(lastSeenMs),
			LastResultJSON: lastResultJSON.String,
		})
	}
	return out, rows.Err()
}

// BackdateLastSeen forces last_seen_ms to an explicit timestamp. Intended for
// administrative backfills and tests that need to simulate a stale heartbeat
// — production code should go through Update, which stamps time.Now().
func (s *ServerStore) BackdateLastSeen(ctx context.Context, hostname string, ts time.Time) error {
	_, err := s.db.writer.ExecContext(ctx,
		`UPDATE servers SET last_seen_ms = ? WHERE hostname = ?`,
		ts.UTC().UnixMilli(), hostname)
	if err != nil {
		return fmt.Errorf("telemetry: servers backdate_last_seen: %w", err)
	}
	return nil
}

// Import inserts a batch of ServerInfo rows, used by the one-shot servers.json
// → SQLite migration. INSERT OR IGNORE preserves any rows that already exist
// (re-run safety). last_result_json may be empty; last_seen may be zero.
func (s *ServerStore) Import(ctx context.Context, infos []ServerInfo) (int, error) {
	if len(infos) == 0 {
		return 0, nil
	}
	tx, err := s.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("telemetry: servers import begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	stmt, err := tx.PrepareContext(ctx,
		`INSERT OR IGNORE INTO servers (hostname, registered_at_ms, last_seen_ms, last_result_json)
		 VALUES (?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("telemetry: servers import prepare: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	var inserted int
	for _, info := range infos {
		var lastResult interface{}
		if info.LastResultJSON != "" {
			lastResult = info.LastResultJSON
		}
		res, err := stmt.ExecContext(ctx, info.Hostname,
			info.RegisteredAt.UTC().UnixMilli(),
			info.LastSeen.UTC().UnixMilli(),
			lastResult)
		if err != nil {
			return inserted, fmt.Errorf("telemetry: servers import insert %s: %w", info.Hostname, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	if err := tx.Commit(); err != nil {
		return inserted, fmt.Errorf("telemetry: servers import commit: %w", err)
	}
	return inserted, nil
}

// optionalTime maps a zero millis value to a zero time.Time — caller can use
// .IsZero() to distinguish "never seen" from an epoch-adjacent real timestamp.
func optionalTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
