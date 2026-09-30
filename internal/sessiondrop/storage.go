//go:build windows

package sessiondrop

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// ObservationStore persists accepted detector inputs without exposing the
// telemetry package to detector consumers.
type ObservationStore interface {
	InsertObservation(context.Context, Observation) error
	PendingObservation(context.Context) (*Observation, error)
}

// BaselineStore persists host-local detector baselines.
type BaselineStore interface {
	UpsertBaseline(context.Context, Baseline) error
	Baseline(context.Context, string, BaselineScope, *int) (*Baseline, error)
}

// DetectorStateStore persists host-local detector state.
type DetectorStateStore interface {
	SaveDetectorState(context.Context, DetectorState) error
	DetectorState(context.Context, string) (*DetectorState, error)
}

// SourceStore persists and reads deterministic session-drop sources.
type SourceStore interface {
	InsertSource(context.Context, Source) (Source, bool, error)
	Source(context.Context, int64) (*Source, error)
	ListSources(context.Context, int, int64) ([]Source, error)
}

// Store is the session-drop persistence dependency used by detector and
// dashboard consumers.
type Store interface {
	ObservationStore
	BaselineStore
	DetectorStateStore
	SourceStore
}

// TelemetryStore adapts telemetry's persistence records to canonical
// session-drop types. The telemetry package remains independent of this one.
type TelemetryStore struct{ store *telemetry.SessionDropStore }

var _ Store = (*TelemetryStore)(nil)

func NewTelemetryStore(store *telemetry.SessionDropStore) *TelemetryStore {
	return &TelemetryStore{store: store}
}

func (s *TelemetryStore) InsertObservation(ctx context.Context, observation Observation) error {
	return s.store.InsertObservation(ctx, ObservationToTelemetry(observation))
}

func (s *TelemetryStore) PendingObservation(ctx context.Context) (*Observation, error) {
	observation, err := s.store.PendingObservation(ctx)
	if err != nil || observation == nil {
		return nil, err
	}
	result := ObservationFromTelemetry(*observation)
	return &result, nil
}

// ConsumeNext removes the oldest accepted observation after consume has
// durably applied its detector effects. The telemetry store orders by the
// private accepted sequence; AcceptedAtMS is never used as an ordering key.
func (s *TelemetryStore) ConsumeNext(ctx context.Context, consume func(Observation) error) (bool, error) {
	return s.store.ConsumeNext(ctx, func(_ context.Context, _ *sql.Tx, observation telemetry.SessionDropObservation) error {
		return consume(ObservationFromTelemetry(observation))
	})
}

// ConsumeNextTransactional runs consume and inbox deletion in one SQLite
// transaction. Detector mutations use the transaction-bound Store, so they
// cannot survive a failed consume commit.
func (s *TelemetryStore) ConsumeNextTransactional(ctx context.Context, consume func(Store, Observation) error) (bool, error) {
	return s.store.ConsumeNext(ctx, func(ctx context.Context, tx *sql.Tx, observation telemetry.SessionDropObservation) error {
		return consume(&transactionalTelemetryStore{store: s.store, tx: tx}, ObservationFromTelemetry(observation))
	})
}

type transactionalTelemetryStore struct {
	store *telemetry.SessionDropStore
	tx    *sql.Tx
}

var _ Store = (*transactionalTelemetryStore)(nil)

func (s *transactionalTelemetryStore) InsertObservation(ctx context.Context, observation Observation) error {
	return s.store.InsertObservationTx(ctx, s.tx, ObservationToTelemetry(observation))
}

func (s *transactionalTelemetryStore) PendingObservation(context.Context) (*Observation, error) {
	return nil, nil
}

func (s *transactionalTelemetryStore) UpsertBaseline(ctx context.Context, baseline Baseline) error {
	return s.store.UpsertBaselineTx(ctx, s.tx, BaselineToTelemetry(baseline))
}

func (s *transactionalTelemetryStore) Baseline(ctx context.Context, host string, scope BaselineScope, slotIndex *int) (*Baseline, error) {
	baseline, err := s.store.BaselineTx(ctx, s.tx, host, string(scope), slotIndex)
	if err != nil || baseline == nil {
		return nil, err
	}
	result := BaselineFromTelemetry(*baseline)
	return &result, nil
}

func (s *transactionalTelemetryStore) SaveDetectorState(ctx context.Context, state DetectorState) error {
	return s.store.SaveDetectorStateTx(ctx, s.tx, DetectorStateToTelemetry(state))
}

func (s *transactionalTelemetryStore) DetectorState(ctx context.Context, host string) (*DetectorState, error) {
	state, err := s.store.DetectorStateTx(ctx, s.tx, host)
	if err != nil || state == nil {
		return nil, err
	}
	result := DetectorStateFromTelemetry(*state)
	return &result, nil
}

func (s *transactionalTelemetryStore) InsertSource(ctx context.Context, source Source) (Source, bool, error) {
	created, inserted, err := s.store.InsertSourceTx(ctx, s.tx, SourceToTelemetry(source))
	return SourceFromTelemetry(created), inserted, err
}

func (s *transactionalTelemetryStore) Source(ctx context.Context, id int64) (*Source, error) {
	source, err := s.store.Source(ctx, id)
	if err != nil || source == nil {
		return nil, err
	}
	result := SourceFromTelemetry(*source)
	return &result, nil
}

func (s *transactionalTelemetryStore) ListSources(ctx context.Context, limit int, beforeID int64) ([]Source, error) {
	return NewTelemetryStore(s.store).ListSources(ctx, limit, beforeID)
}

func (s *TelemetryStore) UpsertBaseline(ctx context.Context, baseline Baseline) error {
	return s.store.UpsertBaseline(ctx, BaselineToTelemetry(baseline))
}

func (s *TelemetryStore) Baseline(ctx context.Context, host string, scope BaselineScope, slotIndex *int) (*Baseline, error) {
	baseline, err := s.store.Baseline(ctx, host, string(scope), slotIndex)
	if err != nil || baseline == nil {
		return nil, err
	}
	result := BaselineFromTelemetry(*baseline)
	return &result, nil
}

func (s *TelemetryStore) SaveDetectorState(ctx context.Context, state DetectorState) error {
	return s.store.SaveDetectorState(ctx, DetectorStateToTelemetry(state))
}

func (s *TelemetryStore) DetectorState(ctx context.Context, host string) (*DetectorState, error) {
	state, err := s.store.DetectorState(ctx, host)
	if err != nil || state == nil {
		return nil, err
	}
	result := DetectorStateFromTelemetry(*state)
	return &result, nil
}

func (s *TelemetryStore) InsertSource(ctx context.Context, source Source) (Source, bool, error) {
	created, inserted, err := s.store.InsertSource(ctx, SourceToTelemetry(source))
	return SourceFromTelemetry(created), inserted, err
}

func (s *TelemetryStore) Source(ctx context.Context, id int64) (*Source, error) {
	source, err := s.store.Source(ctx, id)
	if err != nil || source == nil {
		return nil, err
	}
	result := SourceFromTelemetry(*source)
	return &result, nil
}

func (s *TelemetryStore) ListSources(ctx context.Context, limit int, beforeID int64) ([]Source, error) {
	sources, err := s.store.ListSources(ctx, limit, beforeID)
	if err != nil {
		return nil, err
	}
	result := make([]Source, len(sources))
	for i := range sources {
		result[i] = SourceFromTelemetry(sources[i])
	}
	return result, nil
}

func ObservationToTelemetry(observation Observation) telemetry.SessionDropObservation {
	return telemetry.SessionDropObservation{
		AcceptedSequence:      observation.AcceptedSequence,
		CanonicalHost:         observation.CanonicalHost,
		ReportEpochMs:         observation.ReportEpochMS,
		AcceptedAtMs:          observation.AcceptedAtMS,
		LocalOffsetMinutes:    observation.LocalOffsetMinutes,
		LocalDate:             observation.LocalDate,
		SessionPresence:       string(observation.SessionPresence),
		ActiveSessions:        intToInt64(observation.ActiveSessions),
		DisconnectedSessions:  intToInt64(observation.DisconnectedSessions),
		FreshnessContext:      string(observation.Freshness),
		DrainContext:          string(observation.Drain),
		ClassificationContext: string(observation.ClassificationContext),
	}
}

func ObservationFromTelemetry(observation telemetry.SessionDropObservation) Observation {
	active := int64ToInt(observation.ActiveSessions)
	disconnected := int64ToInt(observation.DisconnectedSessions)
	result := Observation{
		AcceptedSequence:      observation.AcceptedSequence,
		CanonicalHost:         observation.CanonicalHost,
		ReportEpochMS:         observation.ReportEpochMs,
		AcceptedAtMS:          observation.AcceptedAtMs,
		LocalOffsetMinutes:    observation.LocalOffsetMinutes,
		LocalDate:             observation.LocalDate,
		SessionPresence:       SessionPresence(observation.SessionPresence),
		ActiveSessions:        active,
		DisconnectedSessions:  disconnected,
		Freshness:             FreshnessContext(observation.FreshnessContext),
		Drain:                 DrainContext(observation.DrainContext),
		ClassificationContext: Classification(observation.ClassificationContext),
	}
	if active != nil && disconnected != nil {
		total := *active + *disconnected
		result.TotalSessions = &total
	}
	return result
}

func BaselineToTelemetry(baseline Baseline) telemetry.SessionDropBaseline {
	return telemetry.SessionDropBaseline{ID: baseline.ID, Host: baseline.CanonicalHost, Scope: string(baseline.Scope), SlotIndex: baseline.SlotIndex, ModelVersion: baseline.ModelVersion, Alpha: baseline.Alpha, Beta: baseline.Beta, ObservationCount: int64(baseline.ObservationCount), FirstTrainedAtMs: baseline.FirstTrainedAtMS, LastNormalTrainedAtMs: baseline.LastNormalTrainedAtMS, LastUpdatedAtMs: baseline.LastUpdatedAtMS, LocalDates: baseline.TrainedLocalDates}
}

func BaselineFromTelemetry(baseline telemetry.SessionDropBaseline) Baseline {
	return Baseline{ID: baseline.ID, CanonicalHost: baseline.Host, Scope: BaselineScope(baseline.Scope), SlotIndex: baseline.SlotIndex, ModelVersion: baseline.ModelVersion, Alpha: baseline.Alpha, Beta: baseline.Beta, ObservationCount: int(baseline.ObservationCount), FirstTrainedAtMS: baseline.FirstTrainedAtMs, LastNormalTrainedAtMS: baseline.LastNormalTrainedAtMs, LastUpdatedAtMS: baseline.LastUpdatedAtMs, TrainedLocalDates: baseline.LocalDates}
}

func DetectorStateToTelemetry(state DetectorState) telemetry.SessionDropDetectorState {
	result := telemetry.SessionDropDetectorState{Host: state.CanonicalHost, LastScoredReportEpochMs: state.LastScoredReportEpochMS, LastReferenceTotal: intToInt64(state.LastReferenceTotal), CooldownUntilMs: state.CooldownUntilMS, PostDrainRemaining: state.PostDrainRemaining, LastGapReason: string(state.LastGapReason), StateUpdatedAtMs: state.StateUpdatedAtMS}
	for i := range state.Confirmation {
		confirmation := state.Confirmation[i]
		reportEpoch := confirmation.ReportEpochMS
		candidate := confirmation.Candidate
		context := string(confirmation.DrainContext)
		acceptedAt := confirmation.AcceptedAtMS
		result.ConfirmationReportEpochs[i] = &reportEpoch
		result.ConfirmationCandidates[i] = &candidate
		result.ConfirmationContexts[i] = &context
		result.ConfirmationAcceptedAtMs[i] = &acceptedAt
	}
	return result
}

func DetectorStateFromTelemetry(state telemetry.SessionDropDetectorState) DetectorState {
	result := DetectorState{CanonicalHost: state.Host, LastScoredReportEpochMS: state.LastScoredReportEpochMs, LastReferenceTotal: int64ToInt(state.LastReferenceTotal), CooldownUntilMS: state.CooldownUntilMs, PostDrainRemaining: state.PostDrainRemaining, LastGapReason: GapReason(state.LastGapReason), StateUpdatedAtMS: state.StateUpdatedAtMs}
	for i := range state.ConfirmationReportEpochs {
		if state.ConfirmationReportEpochs[i] == nil {
			continue
		}
		result.Confirmation = append(result.Confirmation, ConfirmationObservation{ReportEpochMS: *state.ConfirmationReportEpochs[i], Candidate: *state.ConfirmationCandidates[i], DrainContext: DrainContext(*state.ConfirmationContexts[i]), AcceptedAtMS: *state.ConfirmationAcceptedAtMs[i]})
	}
	return result
}

func SourceToTelemetry(source Source) telemetry.SessionDropSource {
	result := telemetry.SessionDropSource{ID: source.ID, Host: source.RegisteredHost, ReportEpochMs: source.ReportEpochMS, AcceptedAtMs: source.AcceptedAtMS, LocalOffsetMinutes: source.LocalOffsetMinutes, LocalDate: source.LocalDate, DetectedAtMs: source.DetectedAtMS, ConfirmationStartedReportEpochMs: source.ConfirmationStartedReportEpochMS, ConfirmationEndedReportEpochMs: source.ConfirmationEndedReportEpochMS, ConfirmationStartedAtMs: source.ConfirmationStartedAtMS, ConfirmationEndedAtMs: source.ConfirmationEndedAtMS, ObservedSessions: int64(source.ObservedTotalSessions), ReferenceSessions: int64(source.ReferenceTotalSessions), ExpectedSessions: source.ExpectedTotalSessions, BaselineModelVersion: source.BaselineModelVersion, BaselineScope: string(source.BaselineScope), SlotIndex: source.SlotIndex, SlotMatureDays: source.SlotMatureDays, TailProbability: source.TailProbability, AbsoluteLoss: int64(source.AbsoluteLossSessions), RelativeLoss: source.RelativeLoss, ConfirmationWindowSize: len(source.ConfirmationFlags), ConfirmationCount: source.ConfirmationCount, FreshnessContext: string(source.FreshnessContext), DrainContext: string(source.DrainContext), Classification: string(source.Classification), ProviderEligible: source.InvestigationEligible}
	for i := range source.ConfirmationFlags {
		candidate := source.ConfirmationFlags[i]
		result.ConfirmationCandidates[i] = &candidate
	}
	return result
}

func SourceFromTelemetry(source telemetry.SessionDropSource) Source {
	result := Source{ID: source.ID, RegisteredHost: source.Host, ReportEpochMS: source.ReportEpochMs, AcceptedAtMS: source.AcceptedAtMs, LocalOffsetMinutes: source.LocalOffsetMinutes, LocalDate: source.LocalDate, DetectedAtMS: source.DetectedAtMs, ConfirmationStartedReportEpochMS: source.ConfirmationStartedReportEpochMs, ConfirmationEndedReportEpochMS: source.ConfirmationEndedReportEpochMs, ConfirmationStartedAtMS: source.ConfirmationStartedAtMs, ConfirmationEndedAtMS: source.ConfirmationEndedAtMs, ObservedTotalSessions: int(source.ObservedSessions), ReferenceTotalSessions: int(source.ReferenceSessions), ExpectedTotalSessions: source.ExpectedSessions, AbsoluteLossSessions: int(source.AbsoluteLoss), RelativeLoss: source.RelativeLoss, TailProbability: source.TailProbability, BaselineModelVersion: source.BaselineModelVersion, BaselineScope: BaselineScope(source.BaselineScope), SlotIndex: source.SlotIndex, SlotMatureDays: source.SlotMatureDays, ConfirmationCount: source.ConfirmationCount, FreshnessContext: FreshnessContext(source.FreshnessContext), DrainContext: DrainContext(source.DrainContext), Classification: Classification(source.Classification), InvestigationEligible: source.ProviderEligible}
	if source.ConfirmationWindowSize > 0 {
		result.ConfirmationFlags = make([]bool, source.ConfirmationWindowSize)
		for i := range result.ConfirmationFlags {
			result.ConfirmationFlags[i] = *source.ConfirmationCandidates[i]
		}
	}
	return result
}

func SourceSummaryFromSource(source Source) SourceSummary {
	return SourceSummary{ID: strconv.FormatInt(source.ID, 10), SourceKind: SourceKind, RegisteredHost: source.RegisteredHost, ConfirmedAt: FormatRFC3339Milliseconds(source.ConfirmationEndedAtMS), Classification: source.Classification, InvestigationEligible: source.InvestigationEligible, ObservedTotalSessions: source.ObservedTotalSessions, ReferenceTotalSessions: source.ReferenceTotalSessions, ExpectedTotalSessions: source.ExpectedTotalSessions, AbsoluteLossSessions: source.AbsoluteLossSessions, RelativeLoss: source.RelativeLoss, TailProbability: source.TailProbability, BaselineModelVersion: source.BaselineModelVersion, BaselineScope: source.BaselineScope, SlotIndex: source.SlotIndex, SlotMatureDays: source.SlotMatureDays, ConfirmationWindowSize: len(source.ConfirmationFlags), ConfirmationCount: source.ConfirmationCount}
}

func SourceDetailFromSource(source Source) SourceDetail {
	return SourceDetail{SourceSummary: SourceSummaryFromSource(source), ConfirmationStartedAt: FormatRFC3339Milliseconds(source.ConfirmationStartedAtMS), ConfirmationEndedAt: FormatRFC3339Milliseconds(source.ConfirmationEndedAtMS), ConfirmationFlags: source.ConfirmationFlags, FreshnessContext: source.FreshnessContext, DrainContext: source.DrainContext, ConfirmationEndedReportEpochMS: strconv.FormatInt(source.ConfirmationEndedReportEpochMS, 10)}
}

func ListResponseFromSources(sources []Source, nextBefore *int64) ListResponse {
	result := ListResponse{}
	if len(sources) > 0 {
		result.Items = make([]SourceSummary, len(sources))
		for i := range sources {
			result.Items[i] = SourceSummaryFromSource(sources[i])
		}
	}
	if nextBefore != nil {
		cursor := strconv.FormatInt(*nextBefore, 10)
		result.NextBefore = &cursor
	}
	return result
}

func intToInt64(value *int) *int64 {
	if value == nil {
		return nil
	}
	converted := int64(*value)
	return &converted
}

func int64ToInt(value *int64) *int {
	if value == nil {
		return nil
	}
	converted := int(*value)
	return &converted
}

// FormatRFC3339Milliseconds projects a Unix millisecond timestamp to the
// canonical public UTC RFC3339 representation with exactly three fractional
// digits.
func FormatRFC3339Milliseconds(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}
