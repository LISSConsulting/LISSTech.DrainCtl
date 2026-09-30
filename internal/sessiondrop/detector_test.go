//go:build windows

package sessiondrop

import (
	"context"
	"fmt"
	"testing"
	"time"
)

type memoryStore struct {
	baselines map[string]Baseline
	states    map[string]DetectorState
	sources   []Source
}

func newMemoryStore() *memoryStore {
	return &memoryStore{baselines: map[string]Baseline{}, states: map[string]DetectorState{}}
}
func baselineKey(host string, scope BaselineScope, slot *int) string {
	if slot == nil {
		return host + "/" + string(scope)
	}
	return fmt.Sprintf("%s/%s/%d", host, scope, *slot)
}
func (m *memoryStore) InsertObservation(context.Context, Observation) error     { return nil }
func (m *memoryStore) PendingObservation(context.Context) (*Observation, error) { return nil, nil }
func (m *memoryStore) UpsertBaseline(_ context.Context, b Baseline) error {
	m.baselines[baselineKey(b.CanonicalHost, b.Scope, b.SlotIndex)] = b
	return nil
}
func (m *memoryStore) Baseline(_ context.Context, host string, scope BaselineScope, slot *int) (*Baseline, error) {
	b, ok := m.baselines[baselineKey(host, scope, slot)]
	if !ok {
		return nil, nil
	}
	return &b, nil
}
func (m *memoryStore) SaveDetectorState(_ context.Context, s DetectorState) error {
	m.states[s.CanonicalHost] = s
	return nil
}
func (m *memoryStore) DetectorState(_ context.Context, host string) (*DetectorState, error) {
	s, ok := m.states[host]
	if !ok {
		return nil, nil
	}
	return &s, nil
}
func (m *memoryStore) InsertSource(_ context.Context, s Source) (Source, bool, error) {
	for _, existing := range m.sources {
		if existing.RegisteredHost == s.RegisteredHost && existing.ConfirmationEndedReportEpochMS == s.ConfirmationEndedReportEpochMS {
			return existing, false, nil
		}
	}
	s.ID = int64(len(m.sources) + 1)
	m.sources = append(m.sources, s)
	return s, true, nil
}
func (m *memoryStore) Source(_ context.Context, id int64) (*Source, error) {
	for i := range m.sources {
		if m.sources[i].ID == id {
			return &m.sources[i], nil
		}
	}
	return nil, nil
}
func (m *memoryStore) ListSources(_ context.Context, _ int, _ int64) ([]Source, error) {
	return m.sources, nil
}

func matureFallback(host string, first, last int64) Baseline {
	return Baseline{CanonicalHost: host, Scope: BaselineScopeAllHours, ModelVersion: BaselineModelVersion, Alpha: 1001, Beta: 10, ObservationCount: FallbackMinimumObservations, FirstTrainedAtMS: &first, LastNormalTrainedAtMS: &last, LastUpdatedAtMS: last}
}
func present(host string, epoch, accepted int64, total int) Observation {
	return Observation{CanonicalHost: host, ReportEpochMS: epoch, AcceptedAtMS: accepted, LocalDate: "2026-09-27", SessionPresence: SessionPresencePresent, ActiveSessions: new(total), DisconnectedSessions: new(0), TotalSessions: new(total), Freshness: FreshnessFresh, Drain: DrainContextNone, ClassificationContext: ClassificationUnexplained}
}

func TestDetectorPersistsSecondReportConfirmationAndAcceptanceTimes(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	first := int64(1)
	last := int64(24*time.Hour/time.Millisecond) + 1
	if err := store.UpsertBaseline(ctx, matureFallback("a", first, last)); err != nil {
		t.Fatal(err)
	}
	d := NewDetector(store, DefaultSettings())
	if source, err := d.Process(ctx, present("a", 100, 101, 100)); err != nil || source != nil {
		t.Fatalf("normal = %#v, %v", source, err)
	}
	if source, err := d.Process(ctx, present("a", 200, 211, 0)); err != nil || source != nil {
		t.Fatalf("first candidate = %#v, %v", source, err)
	}
	source, err := d.Process(ctx, present("a", 300, 322, 0))
	if err != nil || source == nil {
		t.Fatalf("second candidate = %#v, %v", source, err)
	}
	if got := source.ConfirmationFlags; len(got) != 2 || !got[0] || !got[1] || source.ConfirmationCount != 2 {
		t.Fatalf("confirmation = %#v", source)
	}
	if source.ConfirmationStartedAtMS != 211 || source.ConfirmationEndedAtMS != 322 {
		t.Fatalf("source acceptance timestamps = %#v", source)
	}
	if state := store.states["a"]; len(state.Confirmation) != 0 {
		t.Fatalf("confirmation was not cleared: %#v", state.Confirmation)
	}
	detail := SourceDetailFromSource(*source)
	if detail.ConfirmationStartedAt != "1970-01-01T00:00:00.211Z" || detail.ConfirmationEndedAt != "1970-01-01T00:00:00.322Z" {
		t.Fatalf("public timestamps = %#v", detail)
	}
}

func TestDetectorPersistsThirdReportTwoOfThreeAndKeepsNilDistinctFromZero(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	first := int64(1)
	last := int64(24*time.Hour/time.Millisecond) + 1
	if err := store.UpsertBaseline(ctx, matureFallback("a", first, last)); err != nil {
		t.Fatal(err)
	}
	d := NewDetector(store, DefaultSettings())
	_, _ = d.Process(ctx, present("a", 100, 101, 100))
	_, _ = d.Process(ctx, present("a", 200, 202, 0))
	_, _ = d.Process(ctx, present("a", 300, 303, 100))
	source, err := d.Process(ctx, present("a", 400, 404, 0))
	if err != nil || source == nil {
		t.Fatalf("third report confirmation = %#v, %v", source, err)
	}
	if got := source.ConfirmationFlags; len(got) != 3 || !got[0] || got[1] || !got[2] || source.ConfirmationCount != 2 {
		t.Fatalf("confirmation = %#v", source)
	}
	nilObservation := present("a", 500, 505, 0)
	nilObservation.SessionPresence = SessionPresenceNil
	nilObservation.ActiveSessions, nilObservation.DisconnectedSessions, nilObservation.TotalSessions = nil, nil, nil
	if source, err := d.Process(ctx, nilObservation); err != nil || source != nil {
		t.Fatalf("nil observation = %#v, %v", source, err)
	}
	if got := store.states["a"].LastGapReason; got != GapReasonNilEnumeration {
		t.Fatalf("nil gap = %q", got)
	}
	if got := store.states["a"].LastScoredReportEpochMS; got != 400 {
		t.Fatalf("nil changed numeric watermark to %d", got)
	}
}

func TestDetectorMaintainsHostIsolationAndGapWatermark(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	first := int64(1)
	last := int64(24*time.Hour/time.Millisecond) + 1
	for _, host := range []string{"a", "b"} {
		if err := store.UpsertBaseline(ctx, matureFallback(host, first, last)); err != nil {
			t.Fatal(err)
		}
	}
	d := NewDetector(store, DefaultSettings())
	_, _ = d.Process(ctx, present("a", 100, 101, 100))
	_, _ = d.Process(ctx, present("b", 100, 101, 50))
	_, _ = d.Process(ctx, present("a", 200, 201, 0))
	_, _ = d.Process(ctx, present("a", 200, 202, 0))
	if state := store.states["a"]; state.LastGapReason != GapReasonDuplicateReportEpoch || state.LastScoredReportEpochMS != 200 {
		t.Fatalf("a state = %#v", state)
	}
	if state := store.states["b"]; state.LastScoredReportEpochMS != 100 || state.LastReferenceTotal == nil || *state.LastReferenceTotal != 50 {
		t.Fatalf("b borrowed a state: %#v", state)
	}
}

func TestDetectorDrainPrecedenceCooldownAndCandidateAntiPoisoning(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	first := int64(1)
	last := int64(24*time.Hour/time.Millisecond) + 1
	if err := store.UpsertBaseline(ctx, matureFallback("a", first, last)); err != nil {
		t.Fatal(err)
	}
	d := NewDetector(store, DefaultSettings())
	_, _ = d.Process(ctx, present("a", 100, 101, 100))
	before := store.baselines[baselineKey("a", BaselineScopeAllHours, nil)]
	for _, observation := range []Observation{present("a", 200, 202, 0), present("a", 300, 303, 0)} {
		if _, err := d.Process(ctx, observation); err != nil {
			t.Fatal(err)
		}
	}
	if after := store.baselines[baselineKey("a", BaselineScopeAllHours, nil)]; after.ObservationCount != before.ObservationCount {
		t.Fatalf("candidate trained baseline: %d, want %d", after.ObservationCount, before.ObservationCount)
	}
	if len(store.sources) != 1 {
		t.Fatalf("sources = %d, want 1", len(store.sources))
	}
	_, _ = d.Process(ctx, present("a", 400, 404, 0))
	_, _ = d.Process(ctx, present("a", 500, 505, 0))
	if len(store.sources) != 1 {
		t.Fatalf("cooldown created duplicate source: %d", len(store.sources))
	}

	store = newMemoryStore()
	if err := store.UpsertBaseline(ctx, matureFallback("b", first, last)); err != nil {
		t.Fatal(err)
	}
	d = NewDetector(store, DefaultSettings())
	_, _ = d.Process(ctx, present("b", 100, 101, 100))
	drain := present("b", 200, 202, 0)
	drain.Drain = DrainContextOverlap
	if source, err := d.Process(ctx, drain); err != nil || source != nil {
		t.Fatalf("drain candidate = %#v, %v", source, err)
	}
	postDrain := present("b", 300, 303, 0)
	if source, err := d.Process(ctx, postDrain); err != nil || source == nil {
		t.Fatalf("post-drain confirmation = %#v, %v", source, err)
	} else if source.Classification != ClassificationDrainAssociated || source.InvestigationEligible || source.DrainContext != DrainContextPostHorizon {
		t.Fatalf("drain source = %#v", source)
	}
}

func TestDetectorClassifiesSecondReportConfirmationAcrossPersistedHorizon(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	first := int64(1)
	last := int64(24*time.Hour/time.Millisecond) + 1
	if err := store.UpsertBaseline(ctx, matureFallback("a", first, last)); err != nil {
		t.Fatal(err)
	}
	d := NewDetector(store, DefaultSettings())
	if source, err := d.Process(ctx, present("a", 100, 101, 100)); err != nil || source != nil {
		t.Fatalf("normal = %#v, %v", source, err)
	}
	postDrain := present("a", 200, 202, 0)
	postDrain.Drain = DrainContextPostHorizon
	if source, err := d.Process(ctx, postDrain); err != nil || source != nil {
		t.Fatalf("post-drain candidate = %#v, %v", source, err)
	}
	source, err := d.Process(ctx, present("a", 300, 303, 0))
	if err != nil || source == nil {
		t.Fatalf("second candidate = %#v, %v", source, err)
	}
	if source.Classification != ClassificationDrainAssociated || source.DrainContext != DrainContextPostHorizon || source.InvestigationEligible {
		t.Fatalf("source = %#v, want persisted post-drain context to prevent eligibility", source)
	}
}

func TestDetectorClassifiesThirdReportConfirmationAcrossPersistedHorizon(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	first := int64(1)
	last := int64(24*time.Hour/time.Millisecond) + 1
	if err := store.UpsertBaseline(ctx, matureFallback("a", first, last)); err != nil {
		t.Fatal(err)
	}
	d := NewDetector(store, DefaultSettings())
	_, _ = d.Process(ctx, present("a", 100, 101, 100))
	unknown := present("a", 200, 202, 0)
	unknown.ClassificationContext = ClassificationNotScored
	if source, err := d.Process(ctx, unknown); err != nil || source != nil {
		t.Fatalf("unknown-context candidate = %#v, %v", source, err)
	}
	if source, err := d.Process(ctx, present("a", 300, 303, 100)); err != nil || source != nil {
		t.Fatalf("intervening normal = %#v, %v", source, err)
	}
	source, err := d.Process(ctx, present("a", 400, 404, 0))
	if err != nil || source == nil {
		t.Fatalf("third candidate = %#v, %v", source, err)
	}
	if source.Classification != ClassificationUnknownContext || source.DrainContext != DrainContextUnknown || source.InvestigationEligible {
		t.Fatalf("source = %#v, want persisted unknown context to prevent eligibility", source)
	}
}

func TestDetectorRequiresProbabilityStrictlyBelowThreshold(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	first := int64(1)
	last := int64(24*time.Hour/time.Millisecond) + 1
	if err := store.UpsertBaseline(ctx, matureFallback("a", first, last)); err != nil {
		t.Fatal(err)
	}
	d := NewDetector(store, DefaultSettings())
	_, _ = d.Process(ctx, present("a", 100, 101, 100))
	baseline := store.baselines[baselineKey("a", BaselineScopeAllHours, nil)]
	decayed := decayedBaseline(baseline, 202, d.settings.BaselineHalfLifeHours)
	d.settings.LowerTailThreshold = LowerTailProbability(decayed.Alpha, decayed.Beta, 0)

	if source, err := d.Process(ctx, present("a", 200, 202, 0)); err != nil || source != nil {
		t.Fatalf("threshold-equal observation = %#v, %v", source, err)
	}
	if len(store.states["a"].Confirmation) != 0 {
		t.Fatalf("threshold-equal observation became a candidate: %#v", store.states["a"].Confirmation)
	}
}
