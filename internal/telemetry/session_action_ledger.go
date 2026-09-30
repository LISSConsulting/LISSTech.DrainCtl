//go:build windows

package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
)

// SessionActionLedgerStore is the agent-local execution ledger. Claim is
// durable before WTS is invoked, preventing an action from being re-executed
// after a process restart or a delivered-command retry.
type SessionActionLedgerStore struct{ db *DB }

func NewSessionActionLedgerStore(db *DB) *SessionActionLedgerStore {
	return &SessionActionLedgerStore{db: db}
}

// Claim inserts the action's durable pre-execution marker. If the ID already
// exists, claimed is false and entry is its retained state; callers must not
// invoke WTS in that case.
func (s *SessionActionLedgerStore) Claim(ctx context.Context, actionID string, expiresAt, claimedAt time.Time) (claimed bool, entry sessiondata.SessionActionLedgerEntry, err error) {
	if actionID == "" || expiresAt.UnixMilli() < 0 || claimedAt.UnixMilli() < 0 {
		return false, entry, errors.New("telemetry: invalid action ledger claim")
	}
	result, err := s.db.writer.ExecContext(ctx, `INSERT INTO session_action_ledger(action_id,state,claimed_at_ms,expires_at_ms) VALUES (?, 'claimed', ?, ?) ON CONFLICT(action_id) DO NOTHING`, actionID, claimedAt.UnixMilli(), expiresAt.UnixMilli())
	if err != nil {
		return false, entry, fmt.Errorf("telemetry: claim action ledger: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, entry, err
	}
	if n == 1 {
		return true, sessiondata.SessionActionLedgerEntry{ActionID: actionID, State: sessiondata.SessionActionLedgerClaimed, ClaimedAtMS: claimedAt.UnixMilli(), ExpiresAtMS: expiresAt.UnixMilli()}, nil
	}
	entry, err = s.Entry(ctx, actionID)
	return false, entry, err
}

// Complete transitions a claimed action to terminal before its report is sent.
// Retried completions are idempotent only if the original terminal record is
// retained; they never replace its first safe outcome.
func (s *SessionActionLedgerStore) Complete(ctx context.Context, actionID string, outcome sessiondata.SessionActionOutcome, completedAt time.Time) error {
	if actionID == "" || !outcome.Valid() || completedAt.UnixMilli() < 0 {
		return errors.New("telemetry: invalid action ledger completion")
	}
	result, err := s.db.writer.ExecContext(ctx, `UPDATE session_action_ledger SET state = 'terminal', outcome = ?, completed_at_ms = ? WHERE action_id = ? AND state = 'claimed'`, outcome, completedAt.UnixMilli(), actionID)
	if err != nil {
		return fmt.Errorf("telemetry: complete action ledger: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	entry, err := s.Entry(ctx, actionID)
	if err != nil {
		return err
	}
	if entry.State == sessiondata.SessionActionLedgerTerminal {
		return nil
	}
	return errors.New("telemetry: action ledger claim missing")
}

func (s *SessionActionLedgerStore) Entry(ctx context.Context, actionID string) (entry sessiondata.SessionActionLedgerEntry, err error) {
	var state string
	var outcome sql.NullString
	var completed sql.NullInt64
	err = s.db.reader.QueryRowContext(ctx, `SELECT action_id,state,outcome,claimed_at_ms,completed_at_ms,expires_at_ms FROM session_action_ledger WHERE action_id = ?`, actionID).Scan(&entry.ActionID, &state, &outcome, &entry.ClaimedAtMS, &completed, &entry.ExpiresAtMS)
	if err != nil {
		return entry, err
	}
	entry.State = sessiondata.SessionActionLedgerState(state)
	if outcome.Valid {
		v := sessiondata.SessionActionOutcome(outcome.String)
		entry.Outcome = &v
	}
	if completed.Valid {
		v := completed.Int64
		entry.CompletedAtMS = &v
	}
	return entry, nil
}

// Acknowledge removes only a terminal action after the dashboard has accepted
// its completion. A claimed action is intentionally retained for restart-safe
// duplicate suppression.
func (s *SessionActionLedgerStore) Acknowledge(ctx context.Context, actionID string) error {
	_, err := s.db.writer.ExecContext(ctx, `DELETE FROM session_action_ledger WHERE action_id = ? AND state = 'terminal'`, actionID)
	if err != nil {
		return fmt.Errorf("telemetry: acknowledge action ledger: %w", err)
	}
	return nil
}

// Cleanup removes at most limit unacknowledged records after expiry plus the
// configured action retention. It includes stale claimed rows: by then their
// dashboard commands are necessarily expired, so retaining them cannot add
// execution safety and would leak an unbounded crash ledger.
func (s *SessionActionLedgerStore) Cleanup(ctx context.Context, now time.Time, retention time.Duration, limit int) (int, error) {
	if limit <= 0 || retention < 0 {
		return 0, errors.New("telemetry: invalid action ledger cleanup")
	}
	cutoff := now.Add(-retention).UnixMilli()
	result, err := s.db.writer.ExecContext(ctx, `DELETE FROM session_action_ledger WHERE action_id IN (SELECT action_id FROM session_action_ledger WHERE expires_at_ms <= ? ORDER BY expires_at_ms, action_id LIMIT ?)`, cutoff, limit)
	if err != nil {
		return 0, fmt.Errorf("telemetry: cleanup action ledger: %w", err)
	}
	n, err := result.RowsAffected()
	return int(n), err
}
