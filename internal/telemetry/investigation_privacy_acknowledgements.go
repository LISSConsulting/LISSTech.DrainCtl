//go:build windows

package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const PrivacyAcknowledgementVersion = "openai_responses_privacy_v1"

const PrivacyAcknowledgementClauseSetMarker = "openai_responses_privacy_v1_complete_clauses"

const privacyAcknowledgementClauseSet = `{"version":"openai_responses_privacy_v1","third_party_subprocessors":true,"no_training_without_opt_in":true,"default_abuse_monitoring_up_to_30_days":true,"store_false_application_state_only":true,"temporary_prompt_cache_possible":true,"zdr_mam_separate_approval":true,"audit_days_local_only":true,"global_endpoint_no_regional_guarantee":true}`

// PrivacyAcknowledgementClauseSetHash returns the fixed digest of the complete
// closed acknowledgement object. Callers must not derive a digest from an
// arbitrary request shape.
func PrivacyAcknowledgementClauseSetHash() []byte {
	sum := sha256.Sum256([]byte(privacyAcknowledgementClauseSet))
	return append([]byte(nil), sum[:]...)
}

var (
	ErrInvalidPrivacyAcknowledgement  = errors.New("telemetry: invalid privacy acknowledgement")
	ErrPrivacyAcknowledgementNotFound = errors.New("telemetry: privacy acknowledgement not found")
)

// PrivacyAcknowledgement is the sole telemetry record permitted to retain an
// authenticated dashboard actor for investigation consent accountability.
type PrivacyAcknowledgement struct {
	ID            int64
	Version       string
	Actor         string
	AcceptedAt    time.Time
	ClauseSetHash []byte
}

// PrivacyAcknowledgementStore owns the append-only local consent audit.
type PrivacyAcknowledgementStore struct{ db *DB }

func NewPrivacyAcknowledgementStore(db *DB) *PrivacyAcknowledgementStore {
	return &PrivacyAcknowledgementStore{db: db}
}

// Append records acceptance of the complete, fixed privacy clause set. It
// deliberately has no update or delete operation; retention handles only
// unreferenced historical rows.
func (s *PrivacyAcknowledgementStore) Append(ctx context.Context, actor string, acceptedAt time.Time, clauseSetHash []byte) (PrivacyAcknowledgement, error) {
	if s == nil || s.db == nil || s.db.writer == nil ||
		len(actor) > 320 || strings.TrimSpace(actor) == "" ||
		!bytes.Equal(clauseSetHash, PrivacyAcknowledgementClauseSetHash()) {
		return PrivacyAcknowledgement{}, ErrInvalidPrivacyAcknowledgement
	}
	at := acceptedAt.UTC()
	if at.UnixMilli() <= 0 {
		return PrivacyAcknowledgement{}, ErrInvalidPrivacyAcknowledgement
	}
	result, err := s.db.writer.ExecContext(ctx, `INSERT INTO investigation_privacy_acknowledgements
		(acknowledgement_version, actor, accepted_at_ms, clause_set_hash, clause_set_marker)
		VALUES (?, ?, ?, ?, ?)`,
		PrivacyAcknowledgementVersion, actor, at.UnixMilli(), clauseSetHash,
		PrivacyAcknowledgementClauseSetMarker)
	if err != nil {
		return PrivacyAcknowledgement{}, fmt.Errorf("telemetry: privacy acknowledgement append: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return PrivacyAcknowledgement{}, fmt.Errorf("telemetry: privacy acknowledgement id: %w", err)
	}
	return PrivacyAcknowledgement{ID: id, Version: PrivacyAcknowledgementVersion, Actor: actor, AcceptedAt: at, ClauseSetHash: append([]byte(nil), clauseSetHash...)}, nil
}

// Lookup returns the immutable acknowledgement identified by id from a WAL
// snapshot. Callers must compare Version and ClauseSetHash with configuration;
// merely finding a row never constitutes acknowledgement.
func (s *PrivacyAcknowledgementStore) Lookup(ctx context.Context, id int64) (PrivacyAcknowledgement, error) {
	if s == nil || s.db == nil || s.db.reader == nil || id <= 0 {
		return PrivacyAcknowledgement{}, ErrPrivacyAcknowledgementNotFound
	}
	var row PrivacyAcknowledgement
	var acceptedAtMs int64
	if err := s.db.reader.QueryRowContext(ctx, `SELECT id, acknowledgement_version, actor, accepted_at_ms, clause_set_hash
		FROM investigation_privacy_acknowledgements WHERE id = ?`, id).Scan(
		&row.ID, &row.Version, &row.Actor, &acceptedAtMs, &row.ClauseSetHash,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PrivacyAcknowledgement{}, ErrPrivacyAcknowledgementNotFound
		}
		return PrivacyAcknowledgement{}, fmt.Errorf("telemetry: privacy acknowledgement lookup: %w", err)
	}
	row.AcceptedAt = time.UnixMilli(acceptedAtMs).UTC()
	return row, nil
}

// IsCurrentReference reports whether an immutable audit row is exactly the
// current fixed-version acknowledgement. It intentionally does not reveal its
// actor or other audit contents to safe settings projections.
func (s *PrivacyAcknowledgementStore) IsCurrentReference(ctx context.Context, id int64, version string, clauseSetHash []byte) (bool, error) {
	if s == nil || s.db == nil || s.db.reader == nil || id <= 0 ||
		version != PrivacyAcknowledgementVersion ||
		!bytes.Equal(clauseSetHash, PrivacyAcknowledgementClauseSetHash()) {
		return false, nil
	}
	var one int
	err := s.db.reader.QueryRowContext(ctx, `SELECT 1 FROM investigation_privacy_acknowledgements
		WHERE id = ? AND acknowledgement_version = ? AND clause_set_hash = ? AND clause_set_marker = ?`,
		id, version, clauseSetHash, PrivacyAcknowledgementClauseSetMarker).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("telemetry: privacy acknowledgement reference: %w", err)
	}
	return true, nil
}
