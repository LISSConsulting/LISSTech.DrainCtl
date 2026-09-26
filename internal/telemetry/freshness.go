//go:build windows

package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// FreshnessStore persists the current accepted-report epoch and the one-shot
// offline transition for each host. Its writer transactions make transition
// publication safe across concurrent requests and dashboard restarts.
type FreshnessStore struct {
	db *DB
}

// NewFreshnessStore wraps an open telemetry database.
func NewFreshnessStore(db *DB) *FreshnessStore {
	return &FreshnessStore{db: db}
}

// MarkOffline records an offline transition only when reportEpoch remains the
// current persisted heartbeat for a registered host. It returns true exactly
// once for that epoch. A deleted host or superseded report returns false, so a
// delayed status check cannot recreate stale state or overwrite a newer report.
func (s *FreshnessStore) MarkOffline(ctx context.Context, host string, reportEpoch, now time.Time) (bool, error) {
	return s.transition(ctx, host, reportEpoch.UTC().UnixMilli(), now.UTC().UnixMilli(), true)
}

// MarkFresh records reportEpoch as the latest accepted report and clears its
// offline transition marker only for a registered host. It returns true when
// that report recovered a previously offline epoch. Older reports are ignored.
func (s *FreshnessStore) MarkFresh(ctx context.Context, host string, reportEpoch time.Time) (bool, error) {
	return s.transition(ctx, host, reportEpoch.UTC().UnixMilli(), 0, false)
}

// Remove deletes freshness state when its server is deleted. It is idempotent:
// false means no matching state existed.
func (s *FreshnessStore) Remove(ctx context.Context, host string) (bool, error) {
	result, err := s.db.writer.ExecContext(ctx,
		`DELETE FROM host_freshness WHERE host = ? COLLATE NOCASE`, CanonicalHostname(host))
	if err != nil {
		return false, fmt.Errorf("telemetry: freshness remove: %w", err)
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("telemetry: freshness remove rows_affected: %w", err)
	}
	return removed > 0, nil
}

func (s *FreshnessStore) transition(ctx context.Context, host string, reportEpochMs, offlineAtMs int64, offline bool) (bool, error) {
	host = CanonicalHostname(host)
	tx, err := s.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("telemetry: freshness begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if offline {
		var currentEpochMs int64
		err = tx.QueryRowContext(ctx,
			`SELECT last_seen_ms FROM servers WHERE hostname = ? COLLATE NOCASE`, host).
			Scan(&currentEpochMs)
		switch {
		case errors.Is(err, sql.ErrNoRows), currentEpochMs != reportEpochMs:
			return commitFreshnessTransition(tx, false)
		case err != nil:
			return false, fmt.Errorf("telemetry: freshness verify server epoch: %w", err)
		}
	}
	if !offline {
		var registered int
		err = tx.QueryRowContext(ctx,
			`SELECT 1 FROM servers WHERE hostname = ? COLLATE NOCASE`, host).
			Scan(&registered)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return commitFreshnessTransition(tx, false)
		case err != nil:
			return false, fmt.Errorf("telemetry: freshness verify server: %w", err)
		}
	}

	var (
		storedEpochMs int64
		offlineAt     sql.NullInt64
	)
	err = tx.QueryRowContext(ctx,
		`SELECT report_epoch_ms, offline_emitted_at_ms FROM host_freshness WHERE host = ? COLLATE NOCASE`, host).
		Scan(&storedEpochMs, &offlineAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if offline {
			if _, err = tx.ExecContext(ctx,
				`INSERT INTO host_freshness (host, report_epoch_ms, offline_emitted_at_ms) VALUES (?, ?, ?)`,
				host, reportEpochMs, offlineAtMs); err != nil {
				return false, fmt.Errorf("telemetry: freshness insert offline: %w", err)
			}
			return commitFreshnessTransition(tx, true)
		}
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO host_freshness (host, report_epoch_ms, offline_emitted_at_ms) VALUES (?, ?, NULL)`,
			host, reportEpochMs); err != nil {
			return false, fmt.Errorf("telemetry: freshness insert fresh: %w", err)
		}
		return commitFreshnessTransition(tx, false)
	case err != nil:
		return false, fmt.Errorf("telemetry: freshness load: %w", err)
	case reportEpochMs < storedEpochMs:
		return commitFreshnessTransition(tx, false)
	case offline:
		if reportEpochMs == storedEpochMs && offlineAt.Valid {
			return commitFreshnessTransition(tx, false)
		}
		if _, err = tx.ExecContext(ctx,
			`UPDATE host_freshness SET host = ?, report_epoch_ms = ?, offline_emitted_at_ms = ? WHERE host = ? COLLATE NOCASE`,
			host, reportEpochMs, offlineAtMs, host); err != nil {
			return false, fmt.Errorf("telemetry: freshness mark offline: %w", err)
		}
		return commitFreshnessTransition(tx, true)
	default:
		recovered := offlineAt.Valid
		if reportEpochMs == storedEpochMs && !recovered {
			return commitFreshnessTransition(tx, false)
		}
		if _, err = tx.ExecContext(ctx,
			`UPDATE host_freshness SET host = ?, report_epoch_ms = ?, offline_emitted_at_ms = NULL WHERE host = ? COLLATE NOCASE`,
			host, reportEpochMs, host); err != nil {
			return false, fmt.Errorf("telemetry: freshness mark fresh: %w", err)
		}
		return commitFreshnessTransition(tx, recovered)
	}
}

func commitFreshnessTransition(tx *sql.Tx, transitioned bool) (bool, error) {
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("telemetry: freshness commit: %w", err)
	}
	return transitioned, nil
}
