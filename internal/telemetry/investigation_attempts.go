//go:build windows

package telemetry

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	OpenAIResponsesEndpoint = "https://api.openai.com/v1/responses"
	OpenAIResponsesProfile  = "openai_responses"
	OpenAIResponsesModel    = "gpt-6-astra"
	InvestigationFormat     = "anomaly_investigation_v1"
	maxActiveAttempts       = 100
	maxSourceAttempts       = 100
	sendLease               = 30 * time.Second
	finalizationLease       = 2 * time.Minute
)

var (
	ErrQueueFull           = errors.New("telemetry: investigation queue full")
	ErrAttemptLimitReached = errors.New("telemetry: investigation attempt limit reached")
	ErrAttemptNotFound     = errors.New("telemetry: investigation attempt not found")
	ErrSourceIneligible    = errors.New("telemetry: investigation source ineligible")
	ErrSourceCompleted     = errors.New("telemetry: investigation source completed")
	ErrRetryRequired       = errors.New("telemetry: investigation retry required")
	ErrInvalidAttempt      = errors.New("telemetry: invalid investigation attempt")
	ErrInvalidEvidence     = errors.New("telemetry: invalid investigation evidence")
	ErrInvalidResult       = errors.New("telemetry: invalid investigation result")
	ErrInvalidProvenance   = errors.New("telemetry: invalid investigation provenance")
	ErrInvalidTransition   = errors.New("telemetry: invalid investigation lifecycle transition")
)

type InvestigationSourceKind string
type InvestigationInitiation string
type InvestigationState string
type InvestigationTerminalReason string
type InvestigationSnapshotKind string

const (
	SourceEventSpike    InvestigationSourceKind   = "event_spike"
	SourceSessionDrop   InvestigationSourceKind   = "session_drop"
	InitiationAutomatic InvestigationInitiation   = "automatic"
	InitiationManual    InvestigationInitiation   = "manual"
	InitiationRetry     InvestigationInitiation   = "retry"
	StateQueued         InvestigationState        = "queued"
	StateRunning        InvestigationState        = "running"
	StateCompleted      InvestigationState        = "completed"
	StateInsufficient   InvestigationState        = "insufficient_evidence"
	StateFailed         InvestigationState        = "failed"
	SnapshotAvailable   InvestigationSnapshotKind = "available"
	SnapshotUnavailable InvestigationSnapshotKind = "unavailable"
)

const (
	ReasonAuthenticationFailed    InvestigationTerminalReason = "authentication_failed"
	ReasonConfigurationDisabled   InvestigationTerminalReason = "configuration_disabled"
	ReasonConfigurationInvalid    InvestigationTerminalReason = "configuration_invalid"
	ReasonEvidenceUnavailable     InvestigationTerminalReason = "evidence_unavailable"
	ReasonInterrupted             InvestigationTerminalReason = "interrupted"
	ReasonNetworkError            InvestigationTerminalReason = "network_error"
	ReasonProviderRateLimited     InvestigationTerminalReason = "provider_rate_limited"
	ReasonProviderRequestRejected InvestigationTerminalReason = "provider_request_rejected"
	ReasonProviderRefused         InvestigationTerminalReason = "provider_refused"
	ReasonRedirectRefused         InvestigationTerminalReason = "redirect_refused"
	ReasonRequestLimit            InvestigationTerminalReason = "request_limit"
	ReasonResponseIncomplete      InvestigationTerminalReason = "response_incomplete"
	ReasonResponseInvalid         InvestigationTerminalReason = "response_invalid"
	ReasonResponseLimit           InvestigationTerminalReason = "response_limit"
	ReasonStorageUnavailable      InvestigationTerminalReason = "storage_unavailable"
	ReasonTimeout                 InvestigationTerminalReason = "timeout"
	ReasonUpstreamError           InvestigationTerminalReason = "upstream_error"
)

type InvestigationSource struct {
	Kind InvestigationSourceKind
	ID   int64
}

type InvestigationEvidence struct {
	Version       int
	SnapshotKind  InvestigationSnapshotKind
	SourceTime    time.Time
	SnapshotAt    time.Time
	From          time.Time
	To            time.Time
	CanonicalJSON []byte
	Hash          [sha256.Size]byte
	FactIDs       []string
}

type InvestigationAttempt struct {
	ID                         int64
	Source                     InvestigationSource
	Number                     int
	Initiation                 InvestigationInitiation
	RetryOfAttemptID           *int64
	State                      InvestigationState
	CreatedAt                  time.Time
	QueuedAt                   time.Time
	StartedAt                  *time.Time
	SendAuthorizedAt           *time.Time
	SendCompletedAt            *time.Time
	SendLeaseExpiresAt         *time.Time
	FinalizationLeaseExpiresAt *time.Time
	Existing                   bool
	CompletedAt                *time.Time
	TerminalReason             InvestigationTerminalReason
	EvidenceHash               [sha256.Size]byte
}

type InvestigationSummary struct {
	Text    string
	FactIDs []string
}
type InvestigationHypothesis struct {
	Rank                                    int
	Confidence                              string
	Text                                    string
	SupportingFactIDs, ContradictingFactIDs []string
}
type InvestigationMissingEvidence struct {
	Ordinal        int
	Category, Text string
	FactIDs        []string
}
type InvestigationDiagnosticCheck struct {
	Rank            int
	CheckType, Text string
	FactIDs         []string
	HypothesisRanks []int
}
type InvestigationResult struct {
	OverallAssessment   string
	EvidenceSufficiency string
	HumanReviewRequired bool
	Summary             InvestigationSummary
	Hypotheses          []InvestigationHypothesis
	MissingEvidence     []InvestigationMissingEvidence
	RecommendedChecks   []InvestigationDiagnosticCheck
}

type CreateInvestigationAttempt struct {
	Source           InvestigationSource
	Initiation       InvestigationInitiation
	RetryOfAttemptID *int64
	CreatedAt        time.Time
	Evidence         InvestigationEvidence
}

type InvestigationAttemptCounts struct{ Queued, Running, Completed, InsufficientEvidence, Failed int64 }

type InvestigationProvenance struct {
	ProviderProfile     string
	ProviderEndpoint    string
	RequestedModel      string
	ResponseFormat      string
	Store               bool
	SendAuthorizedAt    time.Time
	SendCompletedAt     time.Time
	RequestHeaderBytes  int
	RequestBodyBytes    int
	ResponseHeaderBytes int
	ResponseBodyBytes   int
	ValidationOutcome   string
}

// InvestigationAttemptStore persists host-free, source-ID-only artifacts.
type InvestigationAttemptStore struct{ db *DB }

func NewInvestigationAttemptStore(db *DB) *InvestigationAttemptStore {
	return &InvestigationAttemptStore{db: db}
}

func validSource(source InvestigationSource) bool {
	return (source.Kind == SourceEventSpike || source.Kind == SourceSessionDrop) && source.ID > 0
}

// sourceEligibility resolves source absence separately from a known but
// ineligible source so callers can preserve the public admission contract.
func sourceEligibility(ctx context.Context, tx *sql.Tx, source InvestigationSource) (found, eligible bool, err error) {
	switch source.Kind {
	case SourceEventSpike:
		var exists int
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM event_spikes WHERE id=?)`, source.ID).Scan(&exists)
		return exists == 1, exists == 1, err
	case SourceSessionDrop:
		var providerEligible int
		err = tx.QueryRowContext(ctx, `SELECT provider_eligible FROM session_drop_anomalies WHERE id=?`, source.ID).Scan(&providerEligible)
		if errors.Is(err, sql.ErrNoRows) {
			return false, false, nil
		}
		return err == nil, providerEligible == 1, err
	default:
		return false, false, nil
	}
}
func validReason(reason InvestigationTerminalReason) bool {
	switch reason {
	case ReasonAuthenticationFailed, ReasonConfigurationDisabled, ReasonConfigurationInvalid, ReasonEvidenceUnavailable, ReasonInterrupted, ReasonNetworkError, ReasonProviderRateLimited, ReasonProviderRequestRejected, ReasonProviderRefused, ReasonRedirectRefused, ReasonRequestLimit, ReasonResponseIncomplete, ReasonResponseInvalid, ReasonResponseLimit, ReasonStorageUnavailable, ReasonTimeout, ReasonUpstreamError:
		return true
	}
	return false
}

// Create admits an attempt before any evidence-related write. The two caps are
// deliberately independent and checked in the same writer transaction.
func (s *InvestigationAttemptStore) Create(ctx context.Context, input CreateInvestigationAttempt) (InvestigationAttempt, error) {
	if s == nil || s.db == nil || s.db.writer == nil || !validSource(input.Source) || !validEvidence(input.Evidence) {
		return InvestigationAttempt{}, ErrInvalidAttempt
	}
	if input.Initiation == InitiationRetry != (input.RetryOfAttemptID != nil) || (input.Initiation != InitiationRetry && input.Initiation != InitiationManual && input.Initiation != InitiationAutomatic) {
		return InvestigationAttempt{}, ErrInvalidAttempt
	}
	now := input.CreatedAt.UTC()
	if now.UnixMilli() <= 0 {
		return InvestigationAttempt{}, ErrInvalidAttempt
	}
	tx, err := s.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return InvestigationAttempt{}, fmt.Errorf("telemetry: attempt create begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if found, eligible, eligibilityErr := sourceEligibility(ctx, tx, input.Source); eligibilityErr != nil {
		return InvestigationAttempt{}, fmt.Errorf("telemetry: investigation source eligibility: %w", eligibilityErr)
	} else if !found {
		return InvestigationAttempt{}, ErrAttemptNotFound
	} else if !eligible {
		return InvestigationAttempt{}, ErrSourceIneligible
	}
	var existingRoot *InvestigationAttempt
	if input.Initiation != InitiationRetry {
		var existingID int64
		err = tx.QueryRowContext(ctx, `SELECT id FROM investigation_attempts WHERE source_kind=? AND source_id=? AND retry_of_attempt_id IS NULL`, input.Source.Kind, input.Source.ID).Scan(&existingID)
		if err == nil {
			existing, getErr := scanAttempt(tx.QueryRowContext(ctx, `SELECT id,source_kind,source_id,attempt_no,initiation,retry_of_attempt_id,state,created_at_ms,queued_at_ms,started_at_ms,send_authorized_at_ms,send_completed_at_ms,send_lease_expires_at_ms,finalization_lease_expires_at_ms,completed_at_ms,terminal_reason,evidence_hash FROM investigation_attempts WHERE id=?`, existingID))
			if getErr != nil {
				return InvestigationAttempt{}, getErr
			}
			switch existing.State {
			case StateQueued, StateRunning:
				existing.Existing = true
				return existing, nil
			case StateCompleted, StateFailed, StateInsufficient:
				existingRoot = &existing
			default:
				return InvestigationAttempt{}, ErrInvalidAttempt
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return InvestigationAttempt{}, fmt.Errorf("telemetry: attempt root lookup: %w", err)
		}
	}
	if input.Initiation == InitiationRetry {
		var state InvestigationState
		var sourceKind InvestigationSourceKind
		var sourceID int64
		err = tx.QueryRowContext(ctx, `SELECT state, source_kind, source_id FROM investigation_attempts WHERE id=?`, *input.RetryOfAttemptID).Scan(&state, &sourceKind, &sourceID)
		if errors.Is(err, sql.ErrNoRows) {
			return InvestigationAttempt{}, ErrAttemptNotFound
		}
		if err != nil {
			return InvestigationAttempt{}, fmt.Errorf("telemetry: retry predecessor: %w", err)
		}
		if sourceKind != input.Source.Kind || sourceID != input.Source.ID || (state != StateFailed && state != StateInsufficient) {
			return InvestigationAttempt{}, ErrInvalidAttempt
		}
	}
	var retained, lastNumber int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(attempt_no), 0) FROM investigation_attempts WHERE source_kind=? AND source_id=?`, input.Source.Kind, input.Source.ID).Scan(&retained, &lastNumber); err != nil {
		return InvestigationAttempt{}, fmt.Errorf("telemetry: attempt source count: %w", err)
	}
	if retained >= maxSourceAttempts {
		return InvestigationAttempt{}, ErrAttemptLimitReached
	}
	if existingRoot != nil {
		switch existingRoot.State {
		case StateCompleted:
			return InvestigationAttempt{}, ErrSourceCompleted
		case StateFailed, StateInsufficient:
			return InvestigationAttempt{}, ErrRetryRequired
		}
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM investigation_attempts WHERE state IN ('queued','running')`).Scan(&active); err != nil {
		return InvestigationAttempt{}, fmt.Errorf("telemetry: attempt active count: %w", err)
	}
	if active >= maxActiveAttempts {
		return InvestigationAttempt{}, ErrQueueFull
	}
	attemptNo := lastNumber + 1
	res, err := tx.ExecContext(ctx, `INSERT INTO investigation_attempts (source_kind,source_id,attempt_no,initiation,retry_of_attempt_id,state,created_at_ms,queued_at_ms,evidence_hash) VALUES (?,?,?,?,?,'queued',?,?,?)`, input.Source.Kind, input.Source.ID, attemptNo, input.Initiation, input.RetryOfAttemptID, now.UnixMilli(), now.UnixMilli(), input.Evidence.Hash[:])
	if err != nil {
		return InvestigationAttempt{}, fmt.Errorf("telemetry: attempt insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return InvestigationAttempt{}, fmt.Errorf("telemetry: attempt id: %w", err)
	}
	if err := insertEvidence(ctx, tx, id, input.Evidence); err != nil {
		return InvestigationAttempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return InvestigationAttempt{}, fmt.Errorf("telemetry: attempt create commit: %w", err)
	}
	return InvestigationAttempt{ID: id, Source: input.Source, Number: attemptNo, Initiation: input.Initiation, RetryOfAttemptID: input.RetryOfAttemptID, State: StateQueued, CreatedAt: now, QueuedAt: now, EvidenceHash: input.Evidence.Hash}, nil
}

func validEvidence(e InvestigationEvidence) bool {
	if e.Version != 1 || e.SourceTime.UTC().UnixMilli() <= 0 || e.SnapshotAt.Before(e.SourceTime) || !e.From.Equal(e.SourceTime.Add(-30*time.Minute)) || !e.To.Equal(minTime(e.SourceTime.Add(30*time.Minute), e.SnapshotAt)) || e.To.Before(e.From) {
		return false
	}
	if e.SnapshotKind == SnapshotUnavailable {
		return string(e.CanonicalJSON) == "{}" && e.Hash == sha256.Sum256([]byte("{}")) && len(e.FactIDs) == 0
	}
	if e.SnapshotKind != SnapshotAvailable || len(e.CanonicalJSON) == 0 || len(e.CanonicalJSON) > 8000 || e.Hash != sha256.Sum256(e.CanonicalJSON) || len(e.FactIDs) == 0 || len(e.FactIDs) > 134 {
		return false
	}
	for i, id := range e.FactIDs {
		if id != fmt.Sprintf("F%03d", i+1) {
			return false
		}
	}
	return true
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func insertEvidence(ctx context.Context, tx *sql.Tx, id int64, e InvestigationEvidence) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO investigation_evidence (attempt_id,dto_version,snapshot_kind,source_time_ms,snapshot_at_ms,from_ms,to_ms,canonical_json) VALUES (?,?,?,?,?,?,?,?)`, id, e.Version, e.SnapshotKind, e.SourceTime.UTC().UnixMilli(), e.SnapshotAt.UTC().UnixMilli(), e.From.UTC().UnixMilli(), e.To.UTC().UnixMilli(), string(e.CanonicalJSON))
	if err != nil {
		return fmt.Errorf("telemetry: evidence insert: %w", err)
	}
	for n, factID := range e.FactIDs {
		if _, err = tx.ExecContext(ctx, `INSERT INTO investigation_evidence_facts (attempt_id,fact_id,ordinal) VALUES (?,?,?)`, id, factID, n+1); err != nil {
			return fmt.Errorf("telemetry: evidence fact insert: %w", err)
		}
	}
	return nil
}

// ClaimNext takes the oldest still-valid queued attempt. It records only
// worker processing time; provider authorization is a distinct later action.
func (s *InvestigationAttemptStore) ClaimNext(ctx context.Context, cutoff, now time.Time) (InvestigationAttempt, bool, error) {
	tx, err := s.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return InvestigationAttempt{}, false, fmt.Errorf("telemetry: claim begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM investigation_attempts WHERE state='queued' AND created_at_ms>=? ORDER BY queued_at_ms,id LIMIT 1`, cutoff.UTC().UnixMilli()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return InvestigationAttempt{}, false, nil
	}
	if err != nil {
		return InvestigationAttempt{}, false, fmt.Errorf("telemetry: claim select: %w", err)
	}
	var evidenceHash []byte
	var canonicalJSON, snapshot string
	var facts int
	err = tx.QueryRowContext(ctx, `SELECT a.evidence_hash,e.canonical_json,e.snapshot_kind,(SELECT COUNT(*) FROM investigation_evidence_facts f WHERE f.attempt_id=a.id) FROM investigation_attempts a JOIN investigation_evidence e ON e.attempt_id=a.id WHERE a.id=?`, id).Scan(&evidenceHash, &canonicalJSON, &snapshot, &facts)
	hashValid := len(evidenceHash) == sha256.Size && sha256.Sum256([]byte(canonicalJSON)) == bytesToHash(evidenceHash)
	available := snapshot == string(SnapshotAvailable) && facts > 0
	unavailable := snapshot == string(SnapshotUnavailable) && canonicalJSON == "{}" && facts == 0
	if err != nil || !hashValid || (!available && !unavailable) {
		if _, uerr := tx.ExecContext(ctx, `UPDATE investigation_attempts SET state='failed',started_at_ms=?,completed_at_ms=?,terminal_reason='storage_unavailable' WHERE id=? AND state='queued'`, now.UTC().UnixMilli(), now.UTC().UnixMilli(), id); uerr != nil {
			return InvestigationAttempt{}, false, fmt.Errorf("telemetry: claim invalid terminalize: %w", uerr)
		}
		if err = tx.Commit(); err != nil {
			return InvestigationAttempt{}, false, err
		}
		return InvestigationAttempt{}, false, nil
	}
	res, err := tx.ExecContext(ctx, `UPDATE investigation_attempts SET state='running',started_at_ms=? WHERE id=? AND state='queued'`, now.UTC().UnixMilli(), id)
	if err != nil {
		return InvestigationAttempt{}, false, fmt.Errorf("telemetry: claim update: %w", err)
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return InvestigationAttempt{}, false, ErrInvalidTransition
	}
	if err = tx.Commit(); err != nil {
		return InvestigationAttempt{}, false, fmt.Errorf("telemetry: claim commit: %w", err)
	}
	attempt, err := s.Get(ctx, id)
	return attempt, err == nil, err
}

// PruneExpiredQueued removes work that must never be claimed or sent after the
// independent AuditDays retention boundary.
func (s *InvestigationAttemptStore) PruneExpiredQueued(ctx context.Context, cutoff time.Time) (int64, error) {
	result, err := s.db.writer.ExecContext(ctx, `DELETE FROM investigation_attempts WHERE state='queued' AND created_at_ms < ?`, cutoff.UTC().UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("telemetry: prune expired queued investigation attempts: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("telemetry: prune expired queued investigation attempt rows: %w", err)
	}
	return rows, nil
}

// FinalizeResult atomically persists a fully normalized, already validated
// report and its closed local provenance before exposing a terminal state.
func (s *InvestigationAttemptStore) FinalizeResult(ctx context.Context, id int64, result InvestigationResult, provenance InvestigationProvenance, now time.Time) error {
	if !validResult(result) || !validProvenance(provenance) {
		return ErrInvalidResult
	}
	state := StateCompleted
	if result.EvidenceSufficiency == "insufficient" {
		state = StateInsufficient
	}
	tx, err := s.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("telemetry: result begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = insertResult(ctx, tx, id, result); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO investigation_provenance
		(attempt_id,provider_profile,provider_endpoint,requested_model,response_format,store,send_authorized_at_ms,send_completed_at_ms,request_header_bytes,request_body_bytes,response_header_bytes,response_body_bytes,validation_outcome)
		VALUES (?,?,?,?,?,0,?,?,?,?,?,?,?)`, id, provenance.ProviderProfile, provenance.ProviderEndpoint,
		provenance.RequestedModel, provenance.ResponseFormat, provenance.SendAuthorizedAt.UTC().UnixMilli(),
		provenance.SendCompletedAt.UTC().UnixMilli(), provenance.RequestHeaderBytes, provenance.RequestBodyBytes,
		provenance.ResponseHeaderBytes, provenance.ResponseBodyBytes, provenance.ValidationOutcome); err != nil {
		return fmt.Errorf("telemetry: provenance insert: %w", err)
	}
	res, err := tx.ExecContext(ctx, `UPDATE investigation_attempts SET state=?,completed_at_ms=?,terminal_reason='',
		send_lease_expires_at_ms=NULL,finalization_lease_expires_at_ms=NULL WHERE id=? AND state='running'
		AND send_authorized_at_ms=? AND send_completed_at_ms=?`, state, now.UTC().UnixMilli(), id,
		provenance.SendAuthorizedAt.UTC().UnixMilli(), provenance.SendCompletedAt.UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("telemetry: result terminalize: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return ErrInvalidTransition
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("telemetry: result commit: %w", err)
	}
	return nil
}

func validProvenance(p InvestigationProvenance) bool {
	return p.ProviderProfile == OpenAIResponsesProfile && p.ProviderEndpoint == OpenAIResponsesEndpoint &&
		p.RequestedModel == OpenAIResponsesModel && p.ResponseFormat == InvestigationFormat && !p.Store &&
		!p.SendAuthorizedAt.IsZero() && !p.SendCompletedAt.Before(p.SendAuthorizedAt) &&
		p.RequestHeaderBytes > 0 && p.RequestHeaderBytes <= 16384 && p.RequestBodyBytes > 0 && p.RequestBodyBytes <= 16384 &&
		p.ResponseHeaderBytes > 0 && p.ResponseHeaderBytes <= 16384 && p.ResponseBodyBytes > 0 && p.ResponseBodyBytes <= 32768 &&
		(p.ValidationOutcome == "accepted" || p.ValidationOutcome == "insufficient_evidence")
}

func validResult(r InvestigationResult) bool {
	if !validText(r.Summary.Text, 1280) || len(r.Summary.FactIDs) == 0 || len(r.Summary.FactIDs) > 12 ||
		len(r.Hypotheses) > 5 || len(r.MissingEvidence) > 6 || len(r.RecommendedChecks) == 0 || len(r.RecommendedChecks) > 6 {
		return false
	}
	insufficient := r.EvidenceSufficiency == "insufficient" && r.OverallAssessment == "insufficient_evidence" && r.HumanReviewRequired
	if insufficient {
		return len(r.Hypotheses) == 0 && len(r.MissingEvidence) > 0
	}
	return (r.EvidenceSufficiency == "partial" || r.EvidenceSufficiency == "sufficient") && r.OverallAssessment != "insufficient_evidence" && len(r.Hypotheses) > 0
}

func validText(text string, max int) bool {
	if len(text) == 0 || len(text) > max || text[0] == ' ' || text[len(text)-1] == ' ' {
		return false
	}
	for i := range text {
		if text[i] < 0x20 || text[i] > 0x7e || text[i] == '/' || text[i] == '\\' || text[i] == '<' || text[i] == '>' || text[i] == '@' || text[i] == '`' {
			return false
		}
	}
	return true
}

func insertResult(ctx context.Context, tx *sql.Tx, id int64, r InvestigationResult) error {
	review := 0
	if r.HumanReviewRequired {
		review = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO investigation_results
		(attempt_id,result_version,overall_assessment,evidence_sufficiency,human_review_required,summary_text_kind,summary_text)
		VALUES (?,1,?,?,?,'untrusted_summary',?)`, id, r.OverallAssessment, r.EvidenceSufficiency, review, r.Summary.Text); err != nil {
		return fmt.Errorf("telemetry: result insert: %w", err)
	}
	for _, fact := range r.Summary.FactIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO investigation_result_summary_facts(attempt_id,fact_id) VALUES (?,?)`, id, fact); err != nil {
			return fmt.Errorf("telemetry: result summary fact: %w", err)
		}
	}
	for _, h := range r.Hypotheses {
		if h.Rank < 1 || h.Rank > 5 || !validText(h.Text, 960) {
			return ErrInvalidResult
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO investigation_hypotheses(attempt_id,rank,confidence,text_kind,text) VALUES (?,? ,?,'untrusted_hypothesis',?)`, id, h.Rank, h.Confidence, h.Text); err != nil {
			return fmt.Errorf("telemetry: hypothesis insert: %w", err)
		}
		for _, f := range h.SupportingFactIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO investigation_hypothesis_facts(attempt_id,hypothesis_rank,polarity,fact_id) VALUES (?,?,'supporting',?)`, id, h.Rank, f); err != nil {
				return err
			}
		}
		for _, f := range h.ContradictingFactIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO investigation_hypothesis_facts(attempt_id,hypothesis_rank,polarity,fact_id) VALUES (?,?,'contradicting',?)`, id, h.Rank, f); err != nil {
				return err
			}
		}
	}
	for _, m := range r.MissingEvidence {
		if m.Ordinal < 1 || m.Ordinal > 6 || !validText(m.Text, 640) {
			return ErrInvalidResult
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO investigation_missing_evidence(attempt_id,ordinal,category,text_kind,text) VALUES (?, ?, ?,'untrusted_missing_evidence',?)`, id, m.Ordinal, m.Category, m.Text); err != nil {
			return err
		}
		for _, f := range m.FactIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO investigation_missing_evidence_facts(attempt_id,missing_ordinal,fact_id) VALUES (?,?,?)`, id, m.Ordinal, f); err != nil {
				return err
			}
		}
	}
	for _, c := range r.RecommendedChecks {
		if c.Rank < 1 || c.Rank > 6 || !validText(c.Text, 960) {
			return ErrInvalidResult
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO investigation_recommended_diagnostic_checks(attempt_id,rank,check_type,text_kind,text) VALUES (?, ?, ?,'untrusted_diagnostic_check',?)`, id, c.Rank, c.CheckType, c.Text); err != nil {
			return err
		}
		for _, f := range c.FactIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO investigation_recommended_diagnostic_check_facts(attempt_id,check_rank,fact_id) VALUES (?,?,?)`, id, c.Rank, f); err != nil {
				return err
			}
		}
		for _, rank := range c.HypothesisRanks {
			if _, err := tx.ExecContext(ctx, `INSERT INTO investigation_recommended_diagnostic_check_hypotheses(attempt_id,check_rank,hypothesis_rank) VALUES (?,?,?)`, id, c.Rank, rank); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *InvestigationAttemptStore) AuthorizeSend(ctx context.Context, id int64, now time.Time) error {
	return s.transition(ctx, id, `UPDATE investigation_attempts SET send_authorized_at_ms=?,send_lease_expires_at_ms=? WHERE id=? AND state='running' AND send_authorized_at_ms IS NULL AND send_completed_at_ms IS NULL AND send_lease_expires_at_ms IS NULL AND finalization_lease_expires_at_ms IS NULL`, now.UTC().UnixMilli(), now.Add(sendLease).UTC().UnixMilli())
}
func (s *InvestigationAttemptStore) CompleteSend(ctx context.Context, id int64, now time.Time) error {
	return s.transition(ctx, id, `UPDATE investigation_attempts SET send_completed_at_ms=?,send_lease_expires_at_ms=NULL,finalization_lease_expires_at_ms=? WHERE id=? AND state='running' AND send_authorized_at_ms IS NOT NULL AND send_completed_at_ms IS NULL AND send_lease_expires_at_ms IS NOT NULL AND finalization_lease_expires_at_ms IS NULL`, now.UTC().UnixMilli(), now.Add(finalizationLease).UTC().UnixMilli())
}
func (s *InvestigationAttemptStore) transition(ctx context.Context, id int64, query string, args ...any) error {
	args = append(args, id)
	res, err := s.db.writer.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("telemetry: attempt transition: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("telemetry: attempt transition affected: %w", err)
	}
	if n != 1 {
		return ErrInvalidTransition
	}
	return nil
}

// FinalizeFailure commits a safe terminal outcome. Local no-send failures and
// sent failures are both supported, but evidence_unavailable has its own API.
func (s *InvestigationAttemptStore) FinalizeFailure(ctx context.Context, id int64, reason InvestigationTerminalReason, now time.Time) error {
	if !validReason(reason) || reason == ReasonEvidenceUnavailable {
		return ErrInvalidAttempt
	}
	return s.transition(ctx, id, `UPDATE investigation_attempts SET state='failed',completed_at_ms=?,terminal_reason=?,send_lease_expires_at_ms=NULL,finalization_lease_expires_at_ms=NULL WHERE id=? AND state='running'`, now.UTC().UnixMilli(), reason)
}
func (s *InvestigationAttemptStore) FinalizeUnavailable(ctx context.Context, id int64, now time.Time) error {
	return s.transition(ctx, id, `UPDATE investigation_attempts SET state='insufficient_evidence',completed_at_ms=?,terminal_reason='evidence_unavailable' WHERE id=? AND state='running' AND send_authorized_at_ms IS NULL AND send_completed_at_ms IS NULL`, now.UTC().UnixMilli())
}

// RecoverRunning applies the no-resend restart table. An active send lease
// protects only an authorized pre-return row; an active finalization lease
// protects only a post-return row. Every other running row terminalizes.
func (s *InvestigationAttemptStore) RecoverRunning(ctx context.Context, now time.Time) (int64, error) {
	nowMS := now.UTC().UnixMilli()
	res, err := s.db.writer.ExecContext(ctx, `UPDATE investigation_attempts
		SET state='failed',completed_at_ms=?,
		    terminal_reason=CASE WHEN send_authorized_at_ms IS NULL THEN 'interrupted' ELSE 'storage_unavailable' END,
		    send_lease_expires_at_ms=NULL,finalization_lease_expires_at_ms=NULL
		WHERE state='running'
		  AND (send_authorized_at_ms IS NULL
		       OR (send_completed_at_ms IS NULL
		           AND (send_lease_expires_at_ms IS NULL OR send_lease_expires_at_ms <= ?))
		       OR (send_completed_at_ms IS NOT NULL
		           AND (finalization_lease_expires_at_ms IS NULL OR finalization_lease_expires_at_ms <= ?)))`, nowMS, nowMS, nowMS)
	if err != nil {
		return 0, fmt.Errorf("telemetry: attempt recovery: %w", err)
	}
	n, err := res.RowsAffected()
	return n, err
}
func (s *InvestigationAttemptStore) Counts(ctx context.Context) (InvestigationAttemptCounts, error) {
	var c InvestigationAttemptCounts
	err := s.db.reader.QueryRowContext(ctx, `SELECT COALESCE(SUM(state='queued'),0),COALESCE(SUM(state='running'),0),COALESCE(SUM(state='completed'),0),COALESCE(SUM(state='insufficient_evidence'),0),COALESCE(SUM(state='failed'),0) FROM investigation_attempts`).Scan(&c.Queued, &c.Running, &c.Completed, &c.InsufficientEvidence, &c.Failed)
	if err != nil {
		return c, fmt.Errorf("telemetry: attempt counts: %w", err)
	}
	return c, nil
}
func (s *InvestigationAttemptStore) History(ctx context.Context, source InvestigationSource) ([]InvestigationAttempt, error) {
	if !validSource(source) {
		return nil, ErrInvalidAttempt
	}
	rows, err := s.db.reader.QueryContext(ctx, `SELECT id,source_kind,source_id,attempt_no,initiation,retry_of_attempt_id,state,created_at_ms,queued_at_ms,started_at_ms,send_authorized_at_ms,send_completed_at_ms,send_lease_expires_at_ms,finalization_lease_expires_at_ms,completed_at_ms,terminal_reason,evidence_hash FROM investigation_attempts WHERE source_kind=? AND source_id=? ORDER BY attempt_no`, source.Kind, source.ID)
	if err != nil {
		return nil, fmt.Errorf("telemetry: attempt history: %w", err)
	}
	out := make([]InvestigationAttempt, 0)
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, a)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return out, nil
}

// SourceExists distinguishes an empty retained history from an expired or
// unknown deterministic source without exposing source identity.
func (s *InvestigationAttemptStore) SourceExists(ctx context.Context, source InvestigationSource) (bool, error) {
	if !validSource(source) {
		return false, ErrInvalidAttempt
	}
	var exists int
	var err error
	switch source.Kind {
	case SourceEventSpike:
		err = s.db.reader.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM event_spikes WHERE id=?)`, source.ID).Scan(&exists)
	case SourceSessionDrop:
		err = s.db.reader.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM session_drop_anomalies WHERE id=?)`, source.ID).Scan(&exists)
	default:
		return false, ErrInvalidAttempt
	}
	if err != nil {
		return false, fmt.Errorf("telemetry: investigation source existence: %w", err)
	}
	return exists == 1, nil
}

// LatestFailure returns the latest retained terminal failed attempt.
func (s *InvestigationAttemptStore) LatestFailure(ctx context.Context) (*InvestigationAttempt, error) {
	row := s.db.reader.QueryRowContext(ctx, `SELECT id,source_kind,source_id,attempt_no,initiation,retry_of_attempt_id,state,created_at_ms,queued_at_ms,started_at_ms,send_authorized_at_ms,send_completed_at_ms,send_lease_expires_at_ms,finalization_lease_expires_at_ms,completed_at_ms,terminal_reason,evidence_hash FROM investigation_attempts WHERE state='failed' ORDER BY completed_at_ms DESC,id DESC LIMIT 1`)
	attempt, err := scanAttempt(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("telemetry: latest investigation failure: %w", err)
	}
	return &attempt, nil
}
func (s *InvestigationAttemptStore) Get(ctx context.Context, id int64) (InvestigationAttempt, error) {
	if id <= 0 {
		return InvestigationAttempt{}, ErrAttemptNotFound
	}
	row := s.db.reader.QueryRowContext(ctx, `SELECT id,source_kind,source_id,attempt_no,initiation,retry_of_attempt_id,state,created_at_ms,queued_at_ms,started_at_ms,send_authorized_at_ms,send_completed_at_ms,send_lease_expires_at_ms,finalization_lease_expires_at_ms,completed_at_ms,terminal_reason,evidence_hash FROM investigation_attempts WHERE id=?`, id)
	a, err := scanAttempt(row)
	if errors.Is(err, sql.ErrNoRows) {
		return InvestigationAttempt{}, ErrAttemptNotFound
	}
	return a, err
}

// Evidence returns the immutable snapshot and contiguous fact registry for one
// attempt. It deliberately exposes no source identity beyond the opaque ID.
func (s *InvestigationAttemptStore) Evidence(ctx context.Context, id int64) (InvestigationEvidence, error) {
	if id <= 0 {
		return InvestigationEvidence{}, ErrAttemptNotFound
	}
	var evidence InvestigationEvidence
	var sourceTime, snapshotAt, from, to int64
	var canonical string
	err := s.db.reader.QueryRowContext(ctx, `SELECT dto_version,snapshot_kind,source_time_ms,snapshot_at_ms,from_ms,to_ms,canonical_json
		FROM investigation_evidence WHERE attempt_id=?`, id).Scan(&evidence.Version, &evidence.SnapshotKind, &sourceTime, &snapshotAt, &from, &to, &canonical)
	if errors.Is(err, sql.ErrNoRows) {
		return InvestigationEvidence{}, ErrAttemptNotFound
	}
	if err != nil {
		return InvestigationEvidence{}, err
	}
	evidence.SourceTime = time.UnixMilli(sourceTime).UTC()
	evidence.SnapshotAt = time.UnixMilli(snapshotAt).UTC()
	evidence.From = time.UnixMilli(from).UTC()
	evidence.To = time.UnixMilli(to).UTC()
	evidence.CanonicalJSON = []byte(canonical)
	evidence.Hash = sha256.Sum256(evidence.CanonicalJSON)
	rows, err := s.db.reader.QueryContext(ctx, `SELECT fact_id FROM investigation_evidence_facts WHERE attempt_id=? ORDER BY ordinal`, id)
	if err != nil {
		return InvestigationEvidence{}, err
	}
	for rows.Next() {
		var factID string
		if err := rows.Scan(&factID); err != nil {
			_ = rows.Close()
			return InvestigationEvidence{}, err
		}
		evidence.FactIDs = append(evidence.FactIDs, factID)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return InvestigationEvidence{}, err
	}
	return evidence, nil
}

// Result reconstructs the complete normalized report and its closed provenance.
// Both are absent for local insufficiency and failed attempts.
func (s *InvestigationAttemptStore) Result(ctx context.Context, id int64) (*InvestigationResult, *InvestigationProvenance, error) {
	if id <= 0 {
		return nil, nil, ErrAttemptNotFound
	}
	result := &InvestigationResult{}
	var review int
	err := s.db.reader.QueryRowContext(ctx, `SELECT overall_assessment,evidence_sufficiency,human_review_required,summary_text FROM investigation_results WHERE attempt_id=?`, id).Scan(&result.OverallAssessment, &result.EvidenceSufficiency, &review, &result.Summary.Text)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("telemetry: investigation result: %w", err)
	}
	result.HumanReviewRequired = review == 1
	if result.Summary.FactIDs, err = resultFactIDs(ctx, s.db.reader, `SELECT fact_id FROM investigation_result_summary_facts WHERE attempt_id=? ORDER BY fact_id`, id); err != nil {
		return nil, nil, err
	}
	rows, err := s.db.reader.QueryContext(ctx, `SELECT rank,confidence,text FROM investigation_hypotheses WHERE attempt_id=? ORDER BY rank`, id)
	if err != nil {
		return nil, nil, fmt.Errorf("telemetry: investigation hypotheses: %w", err)
	}
	for rows.Next() {
		var item InvestigationHypothesis
		if err = rows.Scan(&item.Rank, &item.Confidence, &item.Text); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		if item.SupportingFactIDs, err = resultFactIDs(ctx, s.db.reader, `SELECT fact_id FROM investigation_hypothesis_facts WHERE attempt_id=? AND hypothesis_rank=? AND polarity='supporting' ORDER BY fact_id`, id, item.Rank); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		if item.ContradictingFactIDs, err = resultFactIDs(ctx, s.db.reader, `SELECT fact_id FROM investigation_hypothesis_facts WHERE attempt_id=? AND hypothesis_rank=? AND polarity='contradicting' ORDER BY fact_id`, id, item.Rank); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		result.Hypotheses = append(result.Hypotheses, item)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, nil, err
	}
	rows, err = s.db.reader.QueryContext(ctx, `SELECT ordinal,category,text FROM investigation_missing_evidence WHERE attempt_id=? ORDER BY ordinal`, id)
	if err != nil {
		return nil, nil, fmt.Errorf("telemetry: investigation missing evidence: %w", err)
	}
	for rows.Next() {
		var item InvestigationMissingEvidence
		if err = rows.Scan(&item.Ordinal, &item.Category, &item.Text); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		if item.FactIDs, err = resultFactIDs(ctx, s.db.reader, `SELECT fact_id FROM investigation_missing_evidence_facts WHERE attempt_id=? AND missing_ordinal=? ORDER BY fact_id`, id, item.Ordinal); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		result.MissingEvidence = append(result.MissingEvidence, item)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, nil, err
	}
	rows, err = s.db.reader.QueryContext(ctx, `SELECT rank,check_type,text FROM investigation_recommended_diagnostic_checks WHERE attempt_id=? ORDER BY rank`, id)
	if err != nil {
		return nil, nil, fmt.Errorf("telemetry: investigation diagnostic checks: %w", err)
	}
	for rows.Next() {
		var item InvestigationDiagnosticCheck
		if err = rows.Scan(&item.Rank, &item.CheckType, &item.Text); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		if item.FactIDs, err = resultFactIDs(ctx, s.db.reader, `SELECT fact_id FROM investigation_recommended_diagnostic_check_facts WHERE attempt_id=? AND check_rank=? ORDER BY fact_id`, id, item.Rank); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		if item.HypothesisRanks, err = resultRanks(ctx, s.db.reader, `SELECT hypothesis_rank FROM investigation_recommended_diagnostic_check_hypotheses WHERE attempt_id=? AND check_rank=? ORDER BY hypothesis_rank`, id, item.Rank); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		result.RecommendedChecks = append(result.RecommendedChecks, item)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, nil, err
	}
	if result.Hypotheses == nil {
		result.Hypotheses = []InvestigationHypothesis{}
	}
	if result.MissingEvidence == nil {
		result.MissingEvidence = []InvestigationMissingEvidence{}
	}
	if result.RecommendedChecks == nil {
		result.RecommendedChecks = []InvestigationDiagnosticCheck{}
	}

	provenance := &InvestigationProvenance{}
	var authorized, completed int64
	err = s.db.reader.QueryRowContext(ctx, `SELECT provider_profile,provider_endpoint,requested_model,response_format,store,send_authorized_at_ms,send_completed_at_ms,request_header_bytes,request_body_bytes,response_header_bytes,response_body_bytes,validation_outcome FROM investigation_provenance WHERE attempt_id=?`, id).Scan(&provenance.ProviderProfile, &provenance.ProviderEndpoint, &provenance.RequestedModel, &provenance.ResponseFormat, &provenance.Store, &authorized, &completed, &provenance.RequestHeaderBytes, &provenance.RequestBodyBytes, &provenance.ResponseHeaderBytes, &provenance.ResponseBodyBytes, &provenance.ValidationOutcome)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrInvalidResult
	}
	if err != nil {
		return nil, nil, fmt.Errorf("telemetry: investigation provenance: %w", err)
	}
	provenance.SendAuthorizedAt = time.UnixMilli(authorized).UTC()
	provenance.SendCompletedAt = time.UnixMilli(completed).UTC()
	return result, provenance, nil
}

func resultFactIDs(ctx context.Context, db *sql.DB, query string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			_ = rows.Close()
			return nil, err
		}
		values = append(values, value)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return values, nil
}

func resultRanks(ctx context.Context, db *sql.DB, query string, args ...any) ([]int, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	values := make([]int, 0)
	for rows.Next() {
		var value int
		if err := rows.Scan(&value); err != nil {
			_ = rows.Close()
			return nil, err
		}
		values = append(values, value)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return values, nil
}

type attemptScanner interface{ Scan(...any) error }

func scanAttempt(row attemptScanner) (InvestigationAttempt, error) {
	var a InvestigationAttempt
	var retry, started, authorized, completed, sendLease, finalLease, finished sql.NullInt64
	var hash []byte
	var created, queued int64
	err := row.Scan(&a.ID, &a.Source.Kind, &a.Source.ID, &a.Number, &a.Initiation, &retry, &a.State, &created, &queued, &started, &authorized, &completed, &sendLease, &finalLease, &finished, &a.TerminalReason, &hash)
	if err != nil {
		return a, err
	}
	if len(hash) != sha256.Size {
		return a, ErrInvalidAttempt
	}
	copy(a.EvidenceHash[:], hash)
	a.CreatedAt = time.UnixMilli(created).UTC()
	a.QueuedAt = time.UnixMilli(queued).UTC()
	a.RetryOfAttemptID = nullableInt(retry)
	a.StartedAt = nullableMillis(started)
	a.SendAuthorizedAt = nullableMillis(authorized)
	a.SendCompletedAt = nullableMillis(completed)
	a.SendLeaseExpiresAt = nullableMillis(sendLease)
	a.FinalizationLeaseExpiresAt = nullableMillis(finalLease)
	a.CompletedAt = nullableMillis(finished)
	return a, nil
}
func nullableInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	x := v.Int64
	return &x
}
func nullableMillis(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	x := time.UnixMilli(v.Int64).UTC()
	return &x
}

func bytesToHash(value []byte) [sha256.Size]byte {
	var hash [sha256.Size]byte
	copy(hash[:], value)
	return hash
}
