//go:build windows

package telemetry

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ForceUpdateOutboxEntry is a durable dashboard-to-agent command awaiting an
// authenticated completion report. Times are stored as Unix milliseconds to
// match the rest of the telemetry schema.
type ForceUpdateOutboxEntry struct {
	CommandID    string
	Host         string
	Reason       string
	AcceptedAt   time.Time
	AgentVersion string
}

// ForceUpdateOutboxStore persists the dashboard's retryable force-update
// commands. Rows are removed only by acknowledgement or expiry; delivery does
// not mutate a row, so every report retries the oldest pending command.
type ForceUpdateOutboxStore struct {
	db *DB
}

const forceUpdateOutboxCapacity = 1024

// NewForceUpdateOutboxStore wraps an open telemetry database.
func NewForceUpdateOutboxStore(db *DB) *ForceUpdateOutboxStore {
	return &ForceUpdateOutboxStore{db: db}
}

// Load removes expired entries and returns all retained commands ordered by
// host and acceptance time. It is called when a dashboard server starts.
func (s *ForceUpdateOutboxStore) Load(ctx context.Context, cutoff time.Time) ([]ForceUpdateOutboxEntry, error) {
	if _, err := s.db.writer.ExecContext(ctx, `DELETE FROM force_update_outbox WHERE accepted_at_ms < ?`, cutoff.UnixMilli()); err != nil {
		return nil, fmt.Errorf("force-update outbox expire: %w", err)
	}
	rows, err := s.db.reader.QueryContext(ctx, `SELECT command_id, host, reason, accepted_at_ms, agent_version FROM force_update_outbox ORDER BY host, accepted_at_ms, command_id`)
	if err != nil {
		return nil, fmt.Errorf("force-update outbox load: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var entries []ForceUpdateOutboxEntry
	for rows.Next() {
		var entry ForceUpdateOutboxEntry
		var acceptedAtMS int64
		if err := rows.Scan(&entry.CommandID, &entry.Host, &entry.Reason, &acceptedAtMS, &entry.AgentVersion); err != nil {
			return nil, fmt.Errorf("force-update outbox scan: %w", err)
		}
		entry.AcceptedAt = time.UnixMilli(acceptedAtMS).UTC()
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("force-update outbox rows: %w", err)
	}
	return entries, nil
}

// Enqueue durably records entry before it is made available to a reporting
// agent. A full outbox is an error: callers must not claim an accepted command
// they cannot retain across a dashboard restart.
func (s *ForceUpdateOutboxStore) Enqueue(ctx context.Context, entry ForceUpdateOutboxEntry, cutoff time.Time) error {
	tx, err := s.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("force-update outbox begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `DELETE FROM force_update_outbox WHERE accepted_at_ms < ?`, cutoff.UnixMilli()); err != nil {
		return fmt.Errorf("force-update outbox expire: %w", err)
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM force_update_outbox`).Scan(&count); err != nil {
		return fmt.Errorf("force-update outbox count: %w", err)
	}
	if count >= forceUpdateOutboxCapacity {
		return fmt.Errorf("force-update outbox capacity %d reached", forceUpdateOutboxCapacity)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO force_update_outbox(command_id, host, reason, accepted_at_ms, agent_version) VALUES (?, ?, ?, ?, ?)`, entry.CommandID, entry.Host, entry.Reason, entry.AcceptedAt.UnixMilli(), entry.AgentVersion); err != nil {
		return fmt.Errorf("force-update outbox insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("force-update outbox commit: %w", err)
	}
	return nil
}

// Acknowledge permanently removes the command after a completion report from
// the addressed machine account. It is deliberately idempotent for duplicate
// report retries.
func (s *ForceUpdateOutboxStore) Acknowledge(ctx context.Context, host, commandID string) error {
	if _, err := s.db.writer.ExecContext(ctx, `DELETE FROM force_update_outbox WHERE host = ? AND command_id = ?`, host, commandID); err != nil {
		return fmt.Errorf("force-update outbox acknowledge: %w", err)
	}
	return nil
}

// IsDuplicate reports whether an unexpired row already has host/commandID.
func (s *ForceUpdateOutboxStore) IsDuplicate(ctx context.Context, host, commandID string, cutoff time.Time) (bool, error) {
	if _, err := s.db.writer.ExecContext(ctx, `DELETE FROM force_update_outbox WHERE accepted_at_ms < ?`, cutoff.UnixMilli()); err != nil {
		return false, fmt.Errorf("force-update outbox expire: %w", err)
	}
	var one int
	err := s.db.reader.QueryRowContext(ctx, `SELECT 1 FROM force_update_outbox WHERE host = ? AND command_id = ?`, host, commandID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("force-update outbox duplicate: %w", err)
	}
	return true, nil
}
