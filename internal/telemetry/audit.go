//go:build windows

package telemetry

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidCursor is returned by QueryRange when filter.Cursor cannot be
// decoded. The dashboard handler maps this to HTTP 400 `invalid_cursor`
// (contracts/http-audit.md).
var ErrInvalidCursor = errors.New("telemetry: invalid audit cursor")

// queryRangeUpperLimit caps any positive Limit the caller passes; mirrors the
// HTTP contract's 5000 ceiling as a defense-in-depth at the store layer.
// Limit <= 0 is interpreted as "unlimited" (CLI parity with the legacy
// root-package AuditStore).
const queryRangeUpperLimit = 5000

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
	conn   *sql.Conn
	reader *sql.DB
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
	return &AuditStore{conn: conn, reader: db.reader}, nil
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

// QueryFilter selects a subset of audit rows for QueryRange.
// Zero values are "no filter" — e.g. an empty Host matches every host.
type QueryFilter struct {
	From        *time.Time // inclusive lower bound on ts
	To          *time.Time // exclusive upper bound on ts
	Host        string
	Actor       string // matches audit.changed_by (contracts/http-audit.md)
	Limit       int    // <=0 means unlimited; positive values are clamped to queryRangeUpperLimit
	Cursor      string // opaque; pass the previous response's NextCursor verbatim
	ChangesOnly bool   // drops rows where prev_state == new_state
}

// auditCursor is the decoded form of QueryFilter.Cursor. The serialized form
// is base64-url(JSON) so it travels safely through HTTP query strings.
type auditCursor struct {
	Ts       int64  `json:"ts"`
	Host     string `json:"host"`
	NewState int    `json:"ns"`
}

func encodeAuditCursor(ts int64, host string, newState int) string {
	b, _ := json.Marshal(auditCursor{Ts: ts, Host: host, NewState: newState})
	return base64.URLEncoding.EncodeToString(b)
}

func decodeAuditCursor(s string) (auditCursor, error) {
	var c auditCursor
	raw, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return c, ErrInvalidCursor
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, ErrInvalidCursor
	}
	return c, nil
}

// QueryRange returns audit rows matching filter in strict DESC order by
// (ts, host, new_state). Pagination uses the PK row-value comparison
// `(ts, host, new_state) < (:cts, :chost, :cns)`; under all-DESC ordering
// SQLite's lexicographic-ASC row-value comparison advances correctly to the
// next page (see tasks.md T023 rationale). If the PK in data-model.md ever
// changes, both the cursor payload and the seek predicate must change in
// lockstep.
//
// Reads go through the shared reader pool; the pinned synchronous=FULL
// connection is reserved for Append so writes never queue behind long-running
// queries.
func (s *AuditStore) QueryRange(ctx context.Context, filter QueryFilter) ([]AuditRecord, string, error) {
	limit := filter.Limit
	if limit > queryRangeUpperLimit {
		limit = queryRangeUpperLimit
	}

	var b strings.Builder
	b.WriteString(`SELECT ts, host, prev_state, new_state, principal, changed_by, reason,
            key_modified_ts, reconciliation, before_ts
         FROM audit
         WHERE 1=1`)
	args := make([]any, 0, 8)

	if filter.From != nil {
		b.WriteString(" AND ts >= ?")
		args = append(args, filter.From.UTC().UnixMilli())
	}
	if filter.To != nil {
		b.WriteString(" AND ts < ?")
		args = append(args, filter.To.UTC().UnixMilli())
	}
	if filter.Host != "" {
		b.WriteString(" AND host = ?")
		args = append(args, filter.Host)
	}
	if filter.Actor != "" {
		b.WriteString(" AND changed_by = ?")
		args = append(args, filter.Actor)
	}
	if filter.ChangesOnly {
		b.WriteString(" AND prev_state <> new_state")
	}
	if filter.Cursor != "" {
		c, err := decodeAuditCursor(filter.Cursor)
		if err != nil {
			return nil, "", err
		}
		b.WriteString(" AND (ts, host, new_state) < (?, ?, ?)")
		args = append(args, c.Ts, c.Host, c.NewState)
	}
	b.WriteString(" ORDER BY ts DESC, host DESC, new_state DESC")
	if limit > 0 {
		// Fetch limit+1 so we can distinguish "exactly one more page" from
		// "exact last page" without emitting a spurious trailing cursor.
		b.WriteString(" LIMIT ?")
		args = append(args, limit+1)
	}

	rows, err := s.reader.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, "", fmt.Errorf("telemetry: audit QueryRange: %w", err)
	}
	defer func() { _ = rows.Close() }()

	capHint := 0
	if limit > 0 {
		capHint = limit + 1
	}
	records := make([]AuditRecord, 0, capHint)
	for rows.Next() {
		var (
			tsMs                          int64
			host, principal, changedBy    string
			reason                        string
			prevState, newState, reconInt int
			keyModifiedMs, beforeTsMs     sql.NullInt64
		)
		if err := rows.Scan(
			&tsMs, &host, &prevState, &newState,
			&principal, &changedBy, &reason,
			&keyModifiedMs, &reconInt, &beforeTsMs,
		); err != nil {
			return nil, "", fmt.Errorf("telemetry: audit scan: %w", err)
		}
		rec := AuditRecord{
			Ts:             time.UnixMilli(tsMs).UTC(),
			Host:           host,
			PrevState:      prevState,
			NewState:       newState,
			Principal:      principal,
			ChangedBy:      changedBy,
			Reason:         reason,
			Reconciliation: reconInt == 1,
		}
		if keyModifiedMs.Valid {
			t := time.UnixMilli(keyModifiedMs.Int64).UTC()
			rec.KeyModifiedTs = &t
		}
		if beforeTsMs.Valid {
			t := time.UnixMilli(beforeTsMs.Int64).UTC()
			rec.BeforeTs = &t
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("telemetry: audit rows: %w", err)
	}

	var nextCursor string
	if limit > 0 && len(records) > limit {
		records = records[:limit]
		last := records[len(records)-1]
		nextCursor = encodeAuditCursor(last.Ts.UnixMilli(), last.Host, last.NewState)
	}
	return records, nextCursor, nil
}

// LatestByHost returns the most recent audit record for every host that has at
// least one row in the audit table, keyed by host. Tie-breaks on new_state DESC
// to match the PK ordering used by QueryRange.
//
// Uses the SQLite MIN/MAX-per-group optimization over the audit_host_ts index
// (host, ts DESC): the CTE picks max ts per host without a full scan, the outer
// JOIN is a PK seek per host, and the new_state correlated MAX is a PK seek at
// a specific (host, ts). Cost is O(hosts), not O(rows) (tasks.md T024, FR-020).
// A naive ROW_NUMBER window over the whole table would force SQLite to rank
// every row before filtering, defeating the index.
//
// Used by startup drift reconciliation (reconcile.go, T025) as the baseline it
// compares against current registry state. T044 constrains that LatestByHost
// MUST NOT be called until JSONL migration has completed, otherwise the
// baseline is missing pre-SQLite history and every pre-migration host appears
// as drift.
func (s *AuditStore) LatestByHost(ctx context.Context) (map[string]AuditRecord, error) {
	const q = `
        WITH latest AS (
            SELECT host, MAX(ts) AS ts FROM audit GROUP BY host
        )
        SELECT a.ts, a.host, a.prev_state, a.new_state, a.principal, a.changed_by, a.reason,
               a.key_modified_ts, a.reconciliation, a.before_ts
        FROM audit a
        JOIN latest l ON a.host = l.host AND a.ts = l.ts
        WHERE a.new_state = (
            SELECT MAX(new_state) FROM audit
            WHERE host = a.host AND ts = a.ts
        )`

	rows, err := s.reader.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("telemetry: audit LatestByHost: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make(map[string]AuditRecord)
	for rows.Next() {
		var (
			tsMs                          int64
			host, principal, changedBy    string
			reason                        string
			prevState, newState, reconInt int
			keyModifiedMs, beforeTsMs     sql.NullInt64
		)
		if err := rows.Scan(
			&tsMs, &host, &prevState, &newState,
			&principal, &changedBy, &reason,
			&keyModifiedMs, &reconInt, &beforeTsMs,
		); err != nil {
			return nil, fmt.Errorf("telemetry: audit LatestByHost scan: %w", err)
		}
		rec := AuditRecord{
			Ts:             time.UnixMilli(tsMs).UTC(),
			Host:           host,
			PrevState:      prevState,
			NewState:       newState,
			Principal:      principal,
			ChangedBy:      changedBy,
			Reason:         reason,
			Reconciliation: reconInt == 1,
		}
		if keyModifiedMs.Valid {
			t := time.UnixMilli(keyModifiedMs.Int64).UTC()
			rec.KeyModifiedTs = &t
		}
		if beforeTsMs.Valid {
			t := time.UnixMilli(beforeTsMs.Int64).UTC()
			rec.BeforeTs = &t
		}
		result[host] = rec
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("telemetry: audit LatestByHost rows: %w", err)
	}
	return result, nil
}
