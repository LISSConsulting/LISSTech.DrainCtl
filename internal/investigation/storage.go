//go:build windows

package investigation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// Storage is the investigation-owned persistence boundary. Workers and handlers
// use only the canonical investigation models through this interface.
type Storage interface {
	Create(context.Context, CreateAttempt) (Attempt, error)
	ClaimNext(context.Context, int64, int64) (Attempt, bool, error)
	LoadEvidence(context.Context, int64) (EvidenceSnapshot, error)
	FinalizeResult(context.Context, int64, Report, Provenance, int64) error
	AuthorizeSend(context.Context, int64, int64) error
	CompleteSend(context.Context, int64, int64) error
	FinalizeFailure(context.Context, int64, TerminalReason, int64) error
	FinalizeUnavailable(context.Context, int64, int64) error
	RecoverRunning(context.Context, int64) (int64, error)
	Counts(context.Context) (AttemptCounts, error)
	History(context.Context, SourceRef) ([]Attempt, error)
	SourceExists(context.Context, SourceRef) (bool, error)
	LatestFailure(context.Context) (*LatestFailure, error)
	Get(context.Context, int64) (Attempt, error)
	LoadResult(context.Context, int64) (*Report, *Provenance, error)
}

// CreateAttempt contains the canonical inputs required to create an attempt.
type CreateAttempt struct {
	Source           SourceRef
	Initiation       AttemptInitiation
	RetryOfAttemptID *int64
	CreatedAtMS      int64
	Evidence         EvidenceSnapshot
}

// TelemetryStorage adapts the telemetry persistence implementation to Storage.
type TelemetryStorage struct {
	store *telemetry.InvestigationAttemptStore
}

// NewTelemetryStorage creates the investigation-owned adapter for a telemetry store.
func NewTelemetryStorage(store *telemetry.InvestigationAttemptStore) *TelemetryStorage {
	return &TelemetryStorage{store: store}
}

func (s *TelemetryStorage) Create(ctx context.Context, input CreateAttempt) (Attempt, error) {
	evidence, err := EvidenceSnapshotToTelemetry(input.Evidence)
	if err != nil {
		return Attempt{}, err
	}
	attempt, err := s.store.Create(ctx, telemetry.CreateInvestigationAttempt{
		Source:           SourceRefToTelemetry(input.Source),
		Initiation:       AttemptInitiationToTelemetry(input.Initiation),
		RetryOfAttemptID: input.RetryOfAttemptID,
		CreatedAt:        millisecondsToTime(input.CreatedAtMS),
		Evidence:         evidence,
	})
	if err != nil {
		return Attempt{}, err
	}
	return AttemptFromTelemetry(attempt), nil
}

func (s *TelemetryStorage) ClaimNext(ctx context.Context, cutoffMS, nowMS int64) (Attempt, bool, error) {
	attempt, found, err := s.store.ClaimNext(ctx, millisecondsToTime(cutoffMS), millisecondsToTime(nowMS))
	return AttemptFromTelemetry(attempt), found, err
}

func (s *TelemetryStorage) LoadEvidence(ctx context.Context, id int64) (EvidenceSnapshot, error) {
	evidence, err := s.store.Evidence(ctx, id)
	if err != nil {
		return EvidenceSnapshot{}, err
	}
	return EvidenceSnapshotFromTelemetry(id, evidence), nil
}

func (s *TelemetryStorage) FinalizeResult(ctx context.Context, id int64, report Report, provenance Provenance, completedAtMS int64) error {
	result, err := ReportToTelemetry(report)
	if err != nil {
		return err
	}
	persistedProvenance, err := ProvenanceToTelemetry(provenance)
	if err != nil {
		return err
	}
	return s.store.FinalizeResult(ctx, id, result, persistedProvenance, millisecondsToTime(completedAtMS))
}

func (s *TelemetryStorage) AuthorizeSend(ctx context.Context, id, nowMS int64) error {
	return s.store.AuthorizeSend(ctx, id, millisecondsToTime(nowMS))
}

func (s *TelemetryStorage) CompleteSend(ctx context.Context, id, nowMS int64) error {
	return s.store.CompleteSend(ctx, id, millisecondsToTime(nowMS))
}

func (s *TelemetryStorage) FinalizeFailure(ctx context.Context, id int64, reason TerminalReason, nowMS int64) error {
	return s.store.FinalizeFailure(ctx, id, TerminalReasonToTelemetry(reason), millisecondsToTime(nowMS))
}

func (s *TelemetryStorage) FinalizeUnavailable(ctx context.Context, id, nowMS int64) error {
	return s.store.FinalizeUnavailable(ctx, id, millisecondsToTime(nowMS))
}

func (s *TelemetryStorage) RecoverRunning(ctx context.Context, nowMS int64) (int64, error) {
	return s.store.RecoverRunning(ctx, millisecondsToTime(nowMS))
}

func (s *TelemetryStorage) Counts(ctx context.Context) (AttemptCounts, error) {
	counts, err := s.store.Counts(ctx)
	if err != nil {
		return AttemptCounts{}, err
	}
	return AttemptCountsFromTelemetry(counts)
}

func (s *TelemetryStorage) History(ctx context.Context, source SourceRef) ([]Attempt, error) {
	attempts, err := s.store.History(ctx, SourceRefToTelemetry(source))
	if err != nil {
		return nil, err
	}
	out := make([]Attempt, len(attempts))
	for i := range attempts {
		out[i] = AttemptFromTelemetry(attempts[i])
	}
	return out, nil
}

func (s *TelemetryStorage) SourceExists(ctx context.Context, source SourceRef) (bool, error) {
	return s.store.SourceExists(ctx, SourceRefToTelemetry(source))
}

func (s *TelemetryStorage) LatestFailure(ctx context.Context) (*LatestFailure, error) {
	attempt, err := s.store.LatestFailure(ctx)
	if err != nil || attempt == nil || attempt.CompletedAt == nil {
		return nil, err
	}
	return &LatestFailure{
		Reason: TerminalReason(attempt.TerminalReason),
		At:     attempt.CompletedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
	}, nil
}

func (s *TelemetryStorage) Get(ctx context.Context, id int64) (Attempt, error) {
	attempt, err := s.store.Get(ctx, id)
	if err != nil {
		return Attempt{}, err
	}
	return AttemptFromTelemetry(attempt), nil
}

func (s *TelemetryStorage) LoadResult(ctx context.Context, id int64) (*Report, *Provenance, error) {
	result, provenance, err := s.store.Result(ctx, id)
	if err != nil || result == nil {
		return nil, nil, err
	}
	report := ReportFromTelemetry(*result)
	canonicalProvenance := ProvenanceFromTelemetry(id, *provenance)
	return &report, &canonicalProvenance, nil
}

func SourceRefFromTelemetry(source telemetry.InvestigationSource) SourceRef {
	return SourceRef{Kind: SourceKind(source.Kind), ID: source.ID}
}

func SourceRefToTelemetry(source SourceRef) telemetry.InvestigationSource {
	return telemetry.InvestigationSource{Kind: telemetry.InvestigationSourceKind(source.Kind), ID: source.ID}
}

func AttemptInitiationToTelemetry(initiation AttemptInitiation) telemetry.InvestigationInitiation {
	return telemetry.InvestigationInitiation(initiation)
}

func TerminalReasonToTelemetry(reason TerminalReason) telemetry.InvestigationTerminalReason {
	return telemetry.InvestigationTerminalReason(reason)
}

// AttemptFromTelemetry converts all durable attempt fields to the canonical
// Unix-millisecond representation.
func AttemptFromTelemetry(attempt telemetry.InvestigationAttempt) Attempt {
	return Attempt{
		ID:                           attempt.ID,
		Source:                       SourceRefFromTelemetry(attempt.Source),
		Number:                       attempt.Number,
		Initiation:                   AttemptInitiation(attempt.Initiation),
		RetryOfAttemptID:             attempt.RetryOfAttemptID,
		State:                        AttemptState(attempt.State),
		CreatedAtMS:                  timeToMilliseconds(attempt.CreatedAt),
		QueuedAtMS:                   timeToMilliseconds(attempt.QueuedAt),
		StartedAtMS:                  optionalTimeToMilliseconds(attempt.StartedAt),
		SendAuthorizedAtMS:           optionalTimeToMilliseconds(attempt.SendAuthorizedAt),
		SendCompletedAtMS:            optionalTimeToMilliseconds(attempt.SendCompletedAt),
		SendLeaseExpiresAtMS:         optionalTimeToMilliseconds(attempt.SendLeaseExpiresAt),
		FinalizationLeaseExpiresAtMS: optionalTimeToMilliseconds(attempt.FinalizationLeaseExpiresAt),
		CompletedAtMS:                optionalTimeToMilliseconds(attempt.CompletedAt),
		TerminalReason:               TerminalReason(attempt.TerminalReason),
		EvidenceHash:                 attempt.EvidenceHash,
		Existing:                     attempt.Existing,
	}
}

// AttemptToTelemetry converts all canonical durable attempt fields to the
// time.Time representation used by telemetry.
func AttemptToTelemetry(attempt Attempt) telemetry.InvestigationAttempt {
	return telemetry.InvestigationAttempt{
		ID:                         attempt.ID,
		Source:                     SourceRefToTelemetry(attempt.Source),
		Number:                     attempt.Number,
		Initiation:                 telemetry.InvestigationInitiation(attempt.Initiation),
		RetryOfAttemptID:           attempt.RetryOfAttemptID,
		State:                      telemetry.InvestigationState(attempt.State),
		CreatedAt:                  millisecondsToTime(attempt.CreatedAtMS),
		QueuedAt:                   millisecondsToTime(attempt.QueuedAtMS),
		StartedAt:                  optionalMillisecondsToTime(attempt.StartedAtMS),
		SendAuthorizedAt:           optionalMillisecondsToTime(attempt.SendAuthorizedAtMS),
		SendCompletedAt:            optionalMillisecondsToTime(attempt.SendCompletedAtMS),
		SendLeaseExpiresAt:         optionalMillisecondsToTime(attempt.SendLeaseExpiresAtMS),
		FinalizationLeaseExpiresAt: optionalMillisecondsToTime(attempt.FinalizationLeaseExpiresAtMS),
		CompletedAt:                optionalMillisecondsToTime(attempt.CompletedAtMS),
		TerminalReason:             telemetry.InvestigationTerminalReason(attempt.TerminalReason),
		EvidenceHash:               attempt.EvidenceHash,
		Existing:                   attempt.Existing,
	}
}

// EvidenceSnapshotFromTelemetry creates the canonical evidence form.
func EvidenceSnapshotFromTelemetry(attemptID int64, evidence telemetry.InvestigationEvidence) EvidenceSnapshot {
	return EvidenceSnapshot{
		AttemptID:     attemptID,
		Version:       evidence.Version,
		Kind:          SnapshotKind(evidence.SnapshotKind),
		SourceTimeMS:  timeToMilliseconds(evidence.SourceTime),
		SnapshotAtMS:  timeToMilliseconds(evidence.SnapshotAt),
		FromMS:        timeToMilliseconds(evidence.From),
		ToMS:          timeToMilliseconds(evidence.To),
		CanonicalJSON: evidence.CanonicalJSON,
		FactIDs:       evidence.FactIDs,
		OmissionCodes: omissionCodes(evidence.CanonicalJSON),
	}
}

// omissionCodes reads the closed omission metadata from canonical EvidenceV1.
// Persisting the canonical object is therefore lossless without a second column.
func omissionCodes(canonical []byte) []OmissionCode {
	var payload struct {
		Omitted json.RawMessage `json:"omitted"`
	}
	if json.Unmarshal(canonical, &payload) != nil || len(payload.Omitted) == 0 {
		return nil
	}

	var omissions []struct {
		Code OmissionCode `json:"code"`
	}
	if json.Unmarshal(payload.Omitted, &omissions) != nil || omissions == nil {
		return nil
	}
	out := make([]OmissionCode, len(omissions))
	for i, omission := range omissions {
		out[i] = omission.Code
	}
	return out
}

// EvidenceSnapshotToTelemetry accepts omission metadata already represented by
// canonical EvidenceV1, rejecting only a caller-supplied inconsistent copy.
func EvidenceSnapshotToTelemetry(evidence EvidenceSnapshot) (telemetry.InvestigationEvidence, error) {
	if evidence.Kind == SnapshotKindAvailable && len(evidence.OmissionCodes) != 0 && !reflect.DeepEqual(evidence.OmissionCodes, omissionCodes(evidence.CanonicalJSON)) {
		return telemetry.InvestigationEvidence{}, fmt.Errorf("investigation: omission metadata differs from canonical evidence")
	}
	return telemetry.InvestigationEvidence{
		Version:       evidence.Version,
		SnapshotKind:  telemetry.InvestigationSnapshotKind(evidence.Kind),
		SourceTime:    millisecondsToTime(evidence.SourceTimeMS),
		SnapshotAt:    millisecondsToTime(evidence.SnapshotAtMS),
		From:          millisecondsToTime(evidence.FromMS),
		To:            millisecondsToTime(evidence.ToMS),
		CanonicalJSON: evidence.CanonicalJSON,
		Hash:          sha256.Sum256(evidence.CanonicalJSON),
		FactIDs:       evidence.FactIDs,
	}, nil
}

func ReportTextFromTelemetry(summary telemetry.InvestigationSummary) ReportText {
	return ReportText{TextKind: TextKindSummary, Text: summary.Text, FactIDs: summary.FactIDs}
}

func ReportTextToTelemetry(text ReportText) (telemetry.InvestigationSummary, error) {
	if text.TextKind != TextKindSummary {
		return telemetry.InvestigationSummary{}, fmt.Errorf("investigation: telemetry summary requires text kind %q", TextKindSummary)
	}
	return telemetry.InvestigationSummary{Text: text.Text, FactIDs: text.FactIDs}, nil
}

func ReportFromTelemetry(result telemetry.InvestigationResult) Report {
	report := Report{
		ResultVersion:       1,
		Summary:             ReportTextFromTelemetry(result.Summary),
		OverallAssessment:   OverallAssessment(result.OverallAssessment),
		EvidenceSufficiency: EvidenceSufficiency(result.EvidenceSufficiency),
		HumanReviewRequired: result.HumanReviewRequired,
	}
	if result.Hypotheses != nil {
		report.Hypotheses = make([]Hypothesis, len(result.Hypotheses))
	}
	if result.MissingEvidence != nil {
		report.MissingEvidence = make([]MissingEvidence, len(result.MissingEvidence))
	}
	if result.RecommendedChecks != nil {
		report.RecommendedDiagnosticChecks = make([]RecommendedDiagnosticCheck, len(result.RecommendedChecks))
	}
	for i, hypothesis := range result.Hypotheses {
		report.Hypotheses[i] = Hypothesis{Rank: hypothesis.Rank, Confidence: Confidence(hypothesis.Confidence), TextKind: TextKindHypothesis, Text: hypothesis.Text, SupportingFactIDs: hypothesis.SupportingFactIDs, ContradictingFactIDs: hypothesis.ContradictingFactIDs}
	}
	for i, missing := range result.MissingEvidence {
		report.MissingEvidence[i] = MissingEvidence{Category: MissingEvidenceCategory(missing.Category), TextKind: TextKindMissingEvidence, Text: missing.Text, RelatedFactIDs: missing.FactIDs}
	}
	for i, check := range result.RecommendedChecks {
		report.RecommendedDiagnosticChecks[i] = RecommendedDiagnosticCheck{Rank: check.Rank, CheckType: DiagnosticCheckType(check.CheckType), TextKind: TextKindDiagnosticCheck, Text: check.Text, FactIDs: check.FactIDs, RelatedHypothesisRanks: check.HypothesisRanks}
	}
	return report
}

func ReportToTelemetry(report Report) (telemetry.InvestigationResult, error) {
	if report.ResultVersion != 1 {
		return telemetry.InvestigationResult{}, fmt.Errorf("investigation: telemetry only persists result version 1")
	}
	summary, err := ReportTextToTelemetry(report.Summary)
	if err != nil {
		return telemetry.InvestigationResult{}, err
	}
	result := telemetry.InvestigationResult{
		OverallAssessment:   string(report.OverallAssessment),
		EvidenceSufficiency: string(report.EvidenceSufficiency),
		HumanReviewRequired: report.HumanReviewRequired,
		Summary:             summary,
	}
	if report.Hypotheses != nil {
		result.Hypotheses = make([]telemetry.InvestigationHypothesis, len(report.Hypotheses))
	}
	if report.MissingEvidence != nil {
		result.MissingEvidence = make([]telemetry.InvestigationMissingEvidence, len(report.MissingEvidence))
	}
	if report.RecommendedDiagnosticChecks != nil {
		result.RecommendedChecks = make([]telemetry.InvestigationDiagnosticCheck, len(report.RecommendedDiagnosticChecks))
	}
	for i, hypothesis := range report.Hypotheses {
		if hypothesis.TextKind != TextKindHypothesis {
			return telemetry.InvestigationResult{}, fmt.Errorf("investigation: telemetry hypothesis requires text kind %q", TextKindHypothesis)
		}
		result.Hypotheses[i] = telemetry.InvestigationHypothesis{Rank: hypothesis.Rank, Confidence: string(hypothesis.Confidence), Text: hypothesis.Text, SupportingFactIDs: hypothesis.SupportingFactIDs, ContradictingFactIDs: hypothesis.ContradictingFactIDs}
	}
	for i, missing := range report.MissingEvidence {
		if missing.TextKind != TextKindMissingEvidence {
			return telemetry.InvestigationResult{}, fmt.Errorf("investigation: telemetry missing evidence requires text kind %q", TextKindMissingEvidence)
		}
		result.MissingEvidence[i] = telemetry.InvestigationMissingEvidence{Ordinal: i + 1, Category: string(missing.Category), Text: missing.Text, FactIDs: missing.RelatedFactIDs}
	}
	for i, check := range report.RecommendedDiagnosticChecks {
		if check.TextKind != TextKindDiagnosticCheck {
			return telemetry.InvestigationResult{}, fmt.Errorf("investigation: telemetry diagnostic check requires text kind %q", TextKindDiagnosticCheck)
		}
		result.RecommendedChecks[i] = telemetry.InvestigationDiagnosticCheck{Rank: check.Rank, CheckType: string(check.CheckType), Text: check.Text, FactIDs: check.FactIDs, HypothesisRanks: check.RelatedHypothesisRanks}
	}
	return result, nil
}

func ProvenanceFromTelemetry(attemptID int64, provenance telemetry.InvestigationProvenance) Provenance {
	return Provenance{AttemptID: attemptID, ProviderProfile: ProviderProfileName(provenance.ProviderProfile), ProviderEndpoint: provenance.ProviderEndpoint, RequestedModel: provenance.RequestedModel, ResponseFormat: ResponseFormatName(provenance.ResponseFormat), Store: provenance.Store, SendAuthorizedAtMS: timeToMilliseconds(provenance.SendAuthorizedAt), SendCompletedAtMS: timeToMilliseconds(provenance.SendCompletedAt), RequestHeaderBytes: provenance.RequestHeaderBytes, RequestBodyBytes: provenance.RequestBodyBytes, ResponseHeaderBytes: provenance.ResponseHeaderBytes, ResponseBodyBytes: provenance.ResponseBodyBytes, ValidationOutcome: ValidationOutcome(provenance.ValidationOutcome)}
}

func ProvenanceToTelemetry(provenance Provenance) (telemetry.InvestigationProvenance, error) {
	if provenance.Store {
		return telemetry.InvestigationProvenance{}, fmt.Errorf("investigation: telemetry provenance always disables provider storage")
	}
	return telemetry.InvestigationProvenance{ProviderProfile: string(provenance.ProviderProfile), ProviderEndpoint: provenance.ProviderEndpoint, RequestedModel: provenance.RequestedModel, ResponseFormat: string(provenance.ResponseFormat), Store: false, SendAuthorizedAt: millisecondsToTime(provenance.SendAuthorizedAtMS), SendCompletedAt: millisecondsToTime(provenance.SendCompletedAtMS), RequestHeaderBytes: provenance.RequestHeaderBytes, RequestBodyBytes: provenance.RequestBodyBytes, ResponseHeaderBytes: provenance.ResponseHeaderBytes, ResponseBodyBytes: provenance.ResponseBodyBytes, ValidationOutcome: string(provenance.ValidationOutcome)}, nil
}

func AttemptCountsFromTelemetry(counts telemetry.InvestigationAttemptCounts) (AttemptCounts, error) {
	if int64(int(counts.Queued)) != counts.Queued || int64(int(counts.Running)) != counts.Running || int64(int(counts.Completed)) != counts.Completed || int64(int(counts.InsufficientEvidence)) != counts.InsufficientEvidence || int64(int(counts.Failed)) != counts.Failed {
		return AttemptCounts{}, fmt.Errorf("investigation: telemetry attempt count overflows int")
	}
	return AttemptCounts{Queued: int(counts.Queued), Running: int(counts.Running), Completed: int(counts.Completed), InsufficientEvidence: int(counts.InsufficientEvidence), Failed: int(counts.Failed)}, nil
}

func AttemptCountsToTelemetry(counts AttemptCounts) telemetry.InvestigationAttemptCounts {
	return telemetry.InvestigationAttemptCounts{Queued: int64(counts.Queued), Running: int64(counts.Running), Completed: int64(counts.Completed), InsufficientEvidence: int64(counts.InsufficientEvidence), Failed: int64(counts.Failed)}
}

func millisecondsToTime(milliseconds int64) time.Time { return time.UnixMilli(milliseconds).UTC() }
func timeToMilliseconds(value time.Time) int64        { return value.UTC().UnixMilli() }

func optionalTimeToMilliseconds(value *time.Time) *int64 {
	if value == nil {
		return nil
	}
	milliseconds := timeToMilliseconds(*value)
	return &milliseconds
}

func optionalMillisecondsToTime(milliseconds *int64) *time.Time {
	if milliseconds == nil {
		return nil
	}
	value := millisecondsToTime(*milliseconds)
	return &value
}
