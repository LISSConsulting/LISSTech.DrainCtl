//go:build windows

package telemetry

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// AuditRecord is the telemetry-package representation of a single audit row.
// Mirrors the columns of the audit table in data-model.md. The root-package
// drainctl.AuditRecord is the public API surface; callers convert between the
// two at the package boundary (T026).
type AuditRecord struct {
	Ts             time.Time
	Host           string
	PrevState      int
	NewState       int
	Principal      string
	ChangedBy      string
	Reason         string
	KeyModifiedTs  *time.Time
	Reconciliation bool
	BeforeTs       *time.Time
}

// AuditStore owns the audit-table write path. It holds a dedicated *sql.Conn
// pulled from the auditDB pool; that pool's connector applies
// PRAGMA synchronous = FULL on every new connection, so audit commits are
// fsync-durable against OS crash / power loss (research.md §2). Metrics
// writes stay at synchronous=NORMAL on a separate pool. This pinning also
// keeps the audit connection decoupled from the writer pool (SetMaxOpenConns=1),
// so audit writes never queue behind long-running metrics transactions.
//
// Immutable by design (FR-001b): Append is the only write method exposed;
// retention is handled by the bulk retention worker on its own connection.
type AuditStore struct {
	conn *sql.Conn
}

// NewAuditStore pins a single *sql.Conn from db.auditDB and reinforces
// synchronous=FULL at acquisition time so the guarantee holds even if a
// future refactor drops the pragma from the connector.
func NewAuditStore(ctx context.Context, db *DB) (*AuditStore, error) {
	conn, err := db.auditDB.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("telemetry: audit acquire conn: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA synchronous = FULL"); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("telemetry: audit set synchronous=FULL: %w", err)
	}
	return &AuditStore{conn: conn}, nil
}

// Close releases the pinned connection back to the pool. Idempotent.
func (s *AuditStore) Close() error {
	if s == nil || s.conn == nil {
		return nil
	}
	c := s.conn
	s.conn = nil
	return c.Close()
}

// Append inserts a single audit record through the pinned synchronous=FULL
// connection. ON CONFLICT DO NOTHING on the (ts, host, new_state) primary key
// makes retry and JSONL re-import idempotent (FR-021, data-model.md).
func (s *AuditStore) Append(ctx context.Context, rec AuditRecord) error {
	tsMs := rec.Ts.UTC().UnixMilli()

	var keyModMs sql.NullInt64
	if rec.KeyModifiedTs != nil {
		keyModMs = sql.NullInt64{Int64: rec.KeyModifiedTs.UTC().UnixMilli(), Valid: true}
	}
	var beforeMs sql.NullInt64
	if rec.BeforeTs != nil {
		beforeMs = sql.NullInt64{Int64: rec.BeforeTs.UTC().UnixMilli(), Valid: true}
	}
	reconc := 0
	if rec.Reconciliation {
		reconc = 1
	}

	_, err := s.conn.ExecContext(ctx,
		`INSERT INTO audit
            (ts, host, prev_state, new_state, principal, changed_by, reason, key_modified_ts, reconciliation, before_ts)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
         ON CONFLICT(ts, host, new_state) DO NOTHING`,
		tsMs, rec.Host, rec.PrevState, rec.NewState,
		rec.Principal, rec.ChangedBy, rec.Reason,
		keyModMs, reconc, beforeMs,
	)
	if err != nil {
		return fmt.Errorf("telemetry: audit Append [%s %d->%d]: %w",
			rec.Host, rec.PrevState, rec.NewState, err)
	}
	return nil
}
