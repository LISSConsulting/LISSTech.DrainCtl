//go:build windows

package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
	"github.com/google/uuid"
)

const sessionActionDeliveryLimit = 20

var (
	ErrSessionActionIdempotencyConflict = errors.New("telemetry: session action idempotency conflict")
	ErrSessionActionProtocolInvalid     = errors.New("telemetry: session action protocol invalid")
	ErrSessionActionSnapshotNotFound    = errors.New("telemetry: session action snapshot not found")
	ErrSessionActionHostNotFresh        = errors.New("telemetry: session action host not fresh")
	ErrSessionActionUnsupported         = errors.New("telemetry: session action unsupported")
	ErrSessionActionSessionNotFound     = errors.New("telemetry: session action session not found")
	ErrSessionActionIdentityChanged     = errors.New("telemetry: session action session identity changed")
	ErrSessionActionDisabled            = errors.New("telemetry: session actions disabled")
)

// SessionActionEnqueue is the complete durable command. MessageCiphertext and
// MessageProtection are produced by the caller's protected-secret service; the
// store deliberately never accepts message plaintext.
type SessionActionEnqueue struct {
	Action              sessiondata.SessionAction
	IdempotencyEndpoint string
	MessageCiphertext   []byte
	MessageProtection   string

	// ValidateTarget enables the guarded dashboard enqueue path. It is false
	// only for low-level lifecycle fixtures that deliberately create rows
	// without a corresponding snapshot.
	ValidateTarget bool
	Enabled        bool
	FreshSinceMS   int64
}

// SessionActionDelivery is an outbox command selected for an authenticated
// machine report. Its protected message fields are for the agent transport
// only and must not be exposed through dashboard APIs or audit records.
type SessionActionDelivery struct {
	Action            sessiondata.SessionAction
	MessageCiphertext []byte
	MessageProtection string
}

// SessionActionAuditEvent is the redacted immutable action lifecycle record.
type SessionActionAuditEvent struct {
	EventID           string
	EventAtMS         int64
	ActionID          string
	ActionType        sessiondata.SessionActionType
	CanonicalHost     string
	SessionID         uint32
	ExpectedLogonAtMS int64
	Transition        string
	ResultCode        *string
	RequestedBy       *string
}

// SessionActionStore owns dashboard-side action outbox, idempotency, and audit
// state. All lifecycle writes use the single telemetry writer transaction.
type SessionActionStore struct{ db *DB }

func NewSessionActionStore(db *DB) *SessionActionStore { return &SessionActionStore{db: db} }

// Enqueue atomically resolves an idempotency replay or inserts a queued action
// and its redacted audit record. replay is true only for an equal fingerprint.
func (s *SessionActionStore) Enqueue(ctx context.Context, input SessionActionEnqueue) (status sessiondata.SessionActionStatus, replay bool, err error) {
	a := input.Action
	if err := validAction(a); err != nil {
		return status, false, err
	}
	if input.IdempotencyEndpoint == "" || len(a.RequestFingerprint) == 0 {
		return status, false, errors.New("telemetry: incomplete action idempotency data")
	}
	if a.Type == sessiondata.SessionActionMessage && (len(input.MessageCiphertext) == 0 || input.MessageProtection == "") {
		return status, false, errors.New("telemetry: missing protected message")
	}
	if a.Type != sessiondata.SessionActionMessage && (len(input.MessageCiphertext) != 0 || input.MessageProtection != "") {
		return status, false, fmt.Errorf("telemetry: %s has protected message", a.Type)
	}
	tx, err := s.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return status, false, fmt.Errorf("telemetry: begin action enqueue: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	var existing sessionActionRow
	err = scanAction(tx.QueryRowContext(ctx, actionSelect+` WHERE requested_by = ? AND idempotency_endpoint = ? AND idempotency_key = ?`, a.RequestedBy, input.IdempotencyEndpoint, a.IdempotencyKey), &existing)
	if err == nil {
		if !bytes.Equal(existingFingerprintOr(existing), a.RequestFingerprint) {
			return status, false, ErrSessionActionIdempotencyConflict
		}
		if err := tx.Commit(); err != nil {
			return status, false, fmt.Errorf("telemetry: commit action replay: %w", err)
		}
		return existing.status(), true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return status, false, fmt.Errorf("telemetry: query action idempotency: %w", err)
	}
	if input.ValidateTarget {
		if !input.Enabled {
			return status, false, ErrSessionActionDisabled
		}
		if err := validateActionTarget(ctx, tx, a, input.FreshSinceMS); err != nil {
			return status, false, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO session_action_outbox
(action_id, canonical_host, session_id, expected_logon_at_ms, action_type, message_ciphertext, message_protection, requested_by, idempotency_endpoint, idempotency_key, request_fingerprint, created_at_ms, expires_at_ms, state)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued')`,
		a.ActionID, a.CanonicalHost, a.SessionID, a.ExpectedLogonAtMS, a.Type, nullableBytes(input.MessageCiphertext), nullableString(input.MessageProtection), a.RequestedBy, input.IdempotencyEndpoint, a.IdempotencyKey, a.RequestFingerprint, a.CreatedAtMS, a.ExpiresAtMS)
	if err != nil {
		return status, false, fmt.Errorf("telemetry: insert action: %w", err)
	}
	if err := appendActionAudit(ctx, tx, actionAuditFrom(a, "queued", nil, &a.RequestedBy)); err != nil {
		return status, false, err
	}
	if err := tx.Commit(); err != nil {
		return status, false, fmt.Errorf("telemetry: commit action enqueue: %w", err)
	}
	return actionStatus(a, nil, nil), false, nil
}

// Deliver expires active commands first, then returns at most twenty oldest
// active commands for host. Queued rows become delivered exactly once; already
// delivered rows intentionally remain eligible for same-ID redelivery.
func (s *SessionActionStore) Deliver(ctx context.Context, host string, now time.Time) (_ []SessionActionDelivery, err error) {
	tx, err := s.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("telemetry: begin action delivery: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := expireActions(ctx, tx, now.UnixMilli(), "expired"); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, actionSelect+` WHERE canonical_host = ? AND state IN ('queued','delivered') AND expires_at_ms > ? ORDER BY created_at_ms, action_id LIMIT ?`, host, now.UnixMilli(), sessionActionDeliveryLimit)
	if err != nil {
		return nil, fmt.Errorf("telemetry: select action delivery: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("telemetry: close action delivery rows: %w", closeErr)
		}
	}()
	var result []SessionActionDelivery
	for rows.Next() {
		var row sessionActionRow
		if err := scanAction(rows, &row); err != nil {
			return nil, err
		}
		result = append(result, row.delivery())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("telemetry: scan action delivery: %w", err)
	}
	for i := range result {
		if result[i].Action.State != sessiondata.SessionActionQueued {
			continue
		}
		a := result[i].Action
		if _, err := tx.ExecContext(ctx, `UPDATE session_action_outbox SET state = 'delivered', delivered_at_ms = ? WHERE action_id = ? AND state = 'queued'`, now.UnixMilli(), a.ActionID); err != nil {
			return nil, fmt.Errorf("telemetry: mark action delivered: %w", err)
		}
		result[i].Action.State = sessiondata.SessionActionDelivered
		event := actionAuditFrom(result[i].Action, "delivered", nil, nil)
		event.EventAtMS = now.UnixMilli()
		if err := appendActionAudit(ctx, tx, event); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("telemetry: commit action delivery: %w", err)
	}
	return result, nil
}

// Complete records a valid terminal agent outcome only for a delivered command.
// Expiry is processed before validation, so an expiry always wins a late report.
// Once a command is terminal, a valid report from its owning host is a safe
// acknowledgement replay: the first terminal state and audit transition remain
// authoritative.
func (s *SessionActionStore) Complete(ctx context.Context, host, actionID string, outcome sessiondata.SessionActionOutcome, now time.Time) (sessiondata.SessionActionStatus, error) {
	tx, err := s.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return sessiondata.SessionActionStatus{}, fmt.Errorf("telemetry: begin action completion: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := expireActions(ctx, tx, now.UnixMilli(), "expired"); err != nil {
		return sessiondata.SessionActionStatus{}, err
	}
	var row sessionActionRow
	if err := scanAction(tx.QueryRowContext(ctx, actionSelect+` WHERE action_id = ?`, actionID), &row); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sessiondata.SessionActionStatus{}, ErrSessionActionProtocolInvalid
		}
		return sessiondata.SessionActionStatus{}, err
	}
	if row.Action.CanonicalHost == host && outcome.Valid() && row.Action.State != sessiondata.SessionActionQueued && row.Action.State != sessiondata.SessionActionDelivered {
		if err := tx.Commit(); err != nil {
			return sessiondata.SessionActionStatus{}, err
		}
		return row.status(), nil
	}
	if row.Action.CanonicalHost != host || !outcome.Valid() || outcome == sessiondata.SessionActionOutcomeDuplicate || row.Action.State != sessiondata.SessionActionDelivered {
		code := "protocol_invalid"
		event := actionAuditFrom(row.Action, "protocol_invalid", &code, nil)
		event.EventAtMS = now.UnixMilli()
		if err := appendActionAudit(ctx, tx, event); err != nil {
			return sessiondata.SessionActionStatus{}, err
		}
		if err := tx.Commit(); err != nil {
			return sessiondata.SessionActionStatus{}, err
		}
		return row.status(), ErrSessionActionProtocolInvalid
	}
	state := sessiondata.SessionActionState(outcome)
	code := string(outcome)
	if _, err := tx.ExecContext(ctx, `UPDATE session_action_outbox SET state = ?, completed_at_ms = ?, result_code = ?, message_ciphertext = NULL, message_protection = NULL WHERE action_id = ?`, state, now.UnixMilli(), code, actionID); err != nil {
		return sessiondata.SessionActionStatus{}, fmt.Errorf("telemetry: complete action: %w", err)
	}
	row.Action.State = state
	completed := now.UnixMilli()
	row.CompletedAtMS = &completed
	row.ResultCode = &code
	event := actionAuditFrom(row.Action, string(state), &code, nil)
	event.EventAtMS = now.UnixMilli()
	if err := appendActionAudit(ctx, tx, event); err != nil {
		return sessiondata.SessionActionStatus{}, err
	}
	if err := tx.Commit(); err != nil {
		return sessiondata.SessionActionStatus{}, fmt.Errorf("telemetry: commit action completion: %w", err)
	}
	return row.status(), nil
}

// Expire marks due active commands terminal, erases their protected messages,
// and returns the safe status projection for every committed transition.
func (s *SessionActionStore) Expire(ctx context.Context, now time.Time) ([]sessiondata.SessionActionStatus, error) {
	return s.transitionActive(ctx, now, "expired", "expired")
}

// CancelForPrivacy prevents commands surviving a session visibility transition
// and returns the safe status projection for every committed transition.
func (s *SessionActionStore) CancelForPrivacy(ctx context.Context, now time.Time) ([]sessiondata.SessionActionStatus, error) {
	tx, err := s.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	statuses, err := transitionMatchingActive(ctx, tx, now.UnixMilli(), "expired", "privacy_policy_changed", false)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return statuses, nil
}

// PurgeSnapshotsForPrivacy atomically removes every current session snapshot
// and expires all active commands. Keeping these writes in one transaction
// prevents a visibility change from leaving either a targetable row or a
// deliverable command behind if the other write fails.
func (s *SessionActionStore) PurgeSnapshotsForPrivacy(ctx context.Context, now time.Time) ([]sessiondata.SessionActionStatus, error) {
	tx, err := s.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("telemetry: begin session privacy purge: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_snapshots`); err != nil {
		return nil, fmt.Errorf("telemetry: purge session privacy data: %w", err)
	}
	statuses, err := transitionMatchingActive(ctx, tx, now.UTC().UnixMilli(), "expired", "privacy_policy_changed", false)
	if err != nil {
		return nil, fmt.Errorf("telemetry: cancel session privacy actions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("telemetry: commit session privacy purge: %w", err)
	}
	return statuses, nil
}

func (s *SessionActionStore) transitionActive(ctx context.Context, now time.Time, state, code string) ([]sessiondata.SessionActionStatus, error) {
	tx, err := s.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	statuses, err := transitionActive(ctx, tx, now.UnixMilli(), state, code)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return statuses, nil
}

// Status returns only the safe dashboard status projection.
func (s *SessionActionStore) Status(ctx context.Context, actionID string) (sessiondata.SessionActionStatus, error) {
	var row sessionActionRow
	if err := scanAction(s.db.reader.QueryRowContext(ctx, actionSelect+` WHERE action_id = ?`, actionID), &row); err != nil {
		return sessiondata.SessionActionStatus{}, err
	}
	return row.status(), nil
}

// Retain removes terminal outbox rows and audit events independently. Both
// cutoffs are action-relative and callers choose their retention policy.
func (s *SessionActionStore) Retain(ctx context.Context, terminalBefore, auditBefore time.Time, limit int) (outboxDeleted, auditDeleted int, err error) {
	if limit <= 0 {
		return 0, 0, errors.New("telemetry: retention limit must be positive")
	}
	tx, err := s.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback() //nolint:errcheck
	r, err := tx.ExecContext(ctx, `DELETE FROM session_action_outbox WHERE action_id IN (SELECT action_id FROM session_action_outbox WHERE state NOT IN ('queued','delivered') AND completed_at_ms < ? ORDER BY completed_at_ms, action_id LIMIT ?)`, terminalBefore.UnixMilli(), limit)
	if err != nil {
		return 0, 0, err
	}
	n, _ := r.RowsAffected()
	r, err = tx.ExecContext(ctx, `DELETE FROM session_action_audit WHERE event_id IN (SELECT event_id FROM session_action_audit WHERE event_at_ms < ? ORDER BY event_at_ms, event_id LIMIT ?)`, auditBefore.UnixMilli(), limit)
	if err != nil {
		return 0, 0, err
	}
	m, _ := r.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return int(n), int(m), nil
}

const actionSelect = `SELECT action_id, canonical_host, session_id, expected_logon_at_ms, action_type, message_ciphertext, message_protection, requested_by, idempotency_key, request_fingerprint, created_at_ms, expires_at_ms, state, completed_at_ms, result_code FROM session_action_outbox`

type actionScanner interface{ Scan(...any) error }
type sessionActionRow struct {
	Action        sessiondata.SessionAction
	Ciphertext    []byte
	Protection    sql.NullString
	CompletedAtMS *int64
	ResultCode    *string
}

func scanAction(s actionScanner, r *sessionActionRow) error {
	var id int64
	var typ, state string
	var ct []byte
	var completed sql.NullInt64
	var result sql.NullString
	err := s.Scan(&r.Action.ActionID, &r.Action.CanonicalHost, &id, &r.Action.ExpectedLogonAtMS, &typ, &ct, &r.Protection, &r.Action.RequestedBy, &r.Action.IdempotencyKey, &r.Action.RequestFingerprint, &r.Action.CreatedAtMS, &r.Action.ExpiresAtMS, &state, &completed, &result)
	if err != nil {
		return err
	}
	r.Action.SessionID = uint32(id)
	r.Action.Type = sessiondata.SessionActionType(typ)
	r.Action.State = sessiondata.SessionActionState(state)
	r.Ciphertext = ct
	if completed.Valid {
		v := completed.Int64
		r.CompletedAtMS = &v
	}
	if result.Valid {
		v := result.String
		r.ResultCode = &v
	}
	return nil
}
func (r sessionActionRow) status() sessiondata.SessionActionStatus {
	return actionStatus(r.Action, r.CompletedAtMS, r.ResultCode)
}
func (r sessionActionRow) delivery() SessionActionDelivery {
	return SessionActionDelivery{Action: r.Action, MessageCiphertext: append([]byte(nil), r.Ciphertext...), MessageProtection: r.Protection.String}
}
func existingFingerprintOr(r sessionActionRow) []byte { return r.Action.RequestFingerprint }
func actionStatus(a sessiondata.SessionAction, completed *int64, result *string) sessiondata.SessionActionStatus {
	return sessiondata.SessionActionStatus{ActionID: a.ActionID, Type: a.Type, CanonicalHost: a.CanonicalHost, SessionID: a.SessionID, ExpectedLogonAtMS: a.ExpectedLogonAtMS, State: a.State, CreatedAtMS: a.CreatedAtMS, ExpiresAtMS: a.ExpiresAtMS, CompletedAtMS: completed, ResultCode: result}
}
func nullableBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// validateActionTarget performs every snapshot-dependent enqueue predicate
// inside the enqueue write transaction.
func validateActionTarget(ctx context.Context, tx *sql.Tx, action sessiondata.SessionAction, freshSinceMS int64) error {
	var latestError sql.NullString
	var lastSuccess sql.NullInt64
	var capabilityActions sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT latest_attempt_error_code, last_success_received_at_ms, capability_actions FROM session_snapshots WHERE canonical_host=?`, action.CanonicalHost).Scan(&latestError, &lastSuccess, &capabilityActions)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSessionActionSnapshotNotFound
	}
	if err != nil {
		return fmt.Errorf("telemetry: read action snapshot: %w", err)
	}
	if latestError.Valid || !lastSuccess.Valid || lastSuccess.Int64 < freshSinceMS {
		return ErrSessionActionHostNotFresh
	}
	if capabilityActions.Int64 == 0 {
		return ErrSessionActionUnsupported
	}
	var logon sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT logon_at_ms FROM session_latest WHERE canonical_host=? AND session_id=?`, action.CanonicalHost, action.SessionID).Scan(&logon)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSessionActionSessionNotFound
	}
	if err != nil {
		return fmt.Errorf("telemetry: read action target: %w", err)
	}
	if !logon.Valid || logon.Int64 != action.ExpectedLogonAtMS {
		return ErrSessionActionIdentityChanged
	}
	return nil
}

func validAction(a sessiondata.SessionAction) error {
	if a.ActionID == "" || a.CanonicalHost == "" || a.RequestedBy == "" || a.IdempotencyKey == "" || (a.Type != sessiondata.SessionActionDisconnect && a.Type != sessiondata.SessionActionLogoff && a.Type != sessiondata.SessionActionMessage) || a.State != sessiondata.SessionActionQueued || a.ExpectedLogonAtMS < 0 || a.CreatedAtMS < 0 || a.ExpiresAtMS != a.CreatedAtMS+300000 {
		return errors.New("telemetry: invalid session action")
	}
	return nil
}
func actionAuditFrom(a sessiondata.SessionAction, transition string, code, requestedBy *string) SessionActionAuditEvent {
	return SessionActionAuditEvent{EventID: uuid.NewString(), EventAtMS: a.CreatedAtMS, ActionID: a.ActionID, ActionType: a.Type, CanonicalHost: a.CanonicalHost, SessionID: a.SessionID, ExpectedLogonAtMS: a.ExpectedLogonAtMS, Transition: transition, ResultCode: code, RequestedBy: requestedBy}
}
func appendActionAudit(ctx context.Context, tx *sql.Tx, e SessionActionAuditEvent) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO session_action_audit(event_id,event_at_ms,action_id,action_type,canonical_host,session_id,expected_logon_at_ms,transition,result_code,requested_by) VALUES(?,?,?,?,?,?,?,?,?,?)`, e.EventID, e.EventAtMS, e.ActionID, e.ActionType, e.CanonicalHost, e.SessionID, e.ExpectedLogonAtMS, e.Transition, e.ResultCode, e.RequestedBy)
	if err != nil {
		return fmt.Errorf("telemetry: append action audit: %w", err)
	}
	return nil
}
func expireActions(ctx context.Context, tx *sql.Tx, now int64, code string) error {
	_, err := transitionActive(ctx, tx, now, "expired", code)
	return err
}
func transitionActive(ctx context.Context, tx *sql.Tx, now int64, state, code string) ([]sessiondata.SessionActionStatus, error) {
	return transitionMatchingActive(ctx, tx, now, state, code, true)
}
func transitionMatchingActive(ctx context.Context, tx *sql.Tx, now int64, state, code string, expiredOnly bool) (_ []sessiondata.SessionActionStatus, err error) {
	query := actionSelect + ` WHERE state IN ('queued','delivered')`
	var rows *sql.Rows
	if expiredOnly {
		rows, err = tx.QueryContext(ctx, query+` AND expires_at_ms <= ?`, now)
	} else {
		rows, err = tx.QueryContext(ctx, query)
	}
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("telemetry: close selected active actions: %w", closeErr)
		}
	}()
	var actions []sessionActionRow
	for rows.Next() {
		var row sessionActionRow
		if err := scanAction(rows, &row); err != nil {
			return nil, err
		}
		actions = append(actions, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	statuses := make([]sessiondata.SessionActionStatus, 0, len(actions))
	for _, row := range actions {
		if _, err := tx.ExecContext(ctx, `UPDATE session_action_outbox SET state=?, delivered_at_ms=COALESCE(delivered_at_ms, ?), completed_at_ms=?,result_code=?,message_ciphertext=NULL,message_protection=NULL WHERE action_id=?`, state, now, now, code, row.Action.ActionID); err != nil {
			return nil, err
		}
		result := code
		row.Action.State = sessiondata.SessionActionState(state)
		completed := now
		row.CompletedAtMS = &completed
		row.ResultCode = &result
		event := actionAuditFrom(row.Action, state, &result, nil)
		event.EventAtMS = now
		if err := appendActionAudit(ctx, tx, event); err != nil {
			return nil, err
		}
		statuses = append(statuses, row.status())
	}
	return statuses, nil
}
