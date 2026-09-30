//go:build windows

package sessiondrop

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

func TestStorageAdaptersPreserveZeroAndNilValues(t *testing.T) {
	observation := Observation{SessionPresence: SessionPresenceNil, Freshness: FreshnessUnknown, Drain: DrainContextUnknown, ClassificationContext: ClassificationNotScored}
	if got := ObservationFromTelemetry(ObservationToTelemetry(observation)); !reflect.DeepEqual(got, observation) {
		t.Fatalf("observation round trip = %#v, want %#v", got, observation)
	}

	baseline := Baseline{Scope: BaselineScopeAllHours}
	if got := BaselineFromTelemetry(BaselineToTelemetry(baseline)); !reflect.DeepEqual(got, baseline) {
		t.Fatalf("baseline round trip = %#v, want %#v", got, baseline)
	}

	state := DetectorState{LastGapReason: GapReasonNone}
	if got := DetectorStateFromTelemetry(DetectorStateToTelemetry(state)); !reflect.DeepEqual(got, state) {
		t.Fatalf("detector state round trip = %#v, want %#v", got, state)
	}

	source := Source{}
	if got := SourceFromTelemetry(SourceToTelemetry(source)); !reflect.DeepEqual(got, source) {
		t.Fatalf("source round trip = %#v, want %#v", got, source)
	}
}

func TestDetectorStateStorageAdapterPreservesTwoConfirmationItems(t *testing.T) {
	state := DetectorState{
		CanonicalHost:           "RDSH-01",
		LastScoredReportEpochMS: 500,
		LastReferenceTotal:      new(15),
		CooldownUntilMS:         800,
		PostDrainRemaining:      2,
		Confirmation: []ConfirmationObservation{
			{ReportEpochMS: 400, Candidate: true, DrainContext: DrainContextNone, AcceptedAtMS: 410},
			{ReportEpochMS: 500, Candidate: false, DrainContext: DrainContextOverlap, AcceptedAtMS: 510},
		},
		LastGapReason:    GapReasonStale,
		StateUpdatedAtMS: 600,
	}
	if got := DetectorStateFromTelemetry(DetectorStateToTelemetry(state)); !reflect.DeepEqual(got, state) {
		t.Fatalf("detector state round trip = %#v, want %#v", got, state)
	}
}

func TestDetectorStateStorageAdapterPreservesThreeConfirmationItems(t *testing.T) {
	state := DetectorState{Confirmation: []ConfirmationObservation{
		{ReportEpochMS: 100, Candidate: true, DrainContext: DrainContextNone, AcceptedAtMS: 101},
		{ReportEpochMS: 200, Candidate: true, DrainContext: DrainContextPostHorizon, AcceptedAtMS: 202},
		{ReportEpochMS: 300, Candidate: false, DrainContext: DrainContextUnknown, AcceptedAtMS: 303},
	}}
	if got := DetectorStateFromTelemetry(DetectorStateToTelemetry(state)); !reflect.DeepEqual(got, state) {
		t.Fatalf("detector state round trip = %#v, want %#v", got, state)
	}
}

func TestSourceStorageAdaptersAndProjectionsPreserveTwoAndThreeConfirmations(t *testing.T) {
	for _, confirmationFlags := range [][]bool{{true, false}, {true, false, true}} {
		source := Source{
			ID:                               42,
			RegisteredHost:                   "RDSH-01",
			ReportEpochMS:                    300,
			AcceptedAtMS:                     303,
			LocalOffsetMinutes:               -240,
			LocalDate:                        "2026-09-27",
			DetectedAtMS:                     304,
			ConfirmationStartedReportEpochMS: 100,
			ConfirmationEndedReportEpochMS:   300,
			ConfirmationStartedAtMS:          101,
			ConfirmationEndedAtMS:            303,
			ObservedTotalSessions:            4,
			ReferenceTotalSessions:           12,
			ExpectedTotalSessions:            10.5,
			AbsoluteLossSessions:             8,
			RelativeLoss:                     2.0 / 3.0,
			TailProbability:                  0.0001,
			BaselineModelVersion:             BaselineModelVersion,
			BaselineScope:                    BaselineScopeSlot,
			SlotIndex:                        new(18),
			SlotMatureDays:                   new(7),
			ConfirmationFlags:                confirmationFlags,
			ConfirmationCount:                2,
			FreshnessContext:                 FreshnessFresh,
			DrainContext:                     DrainContextNone,
			Classification:                   ClassificationUnexplained,
			InvestigationEligible:            true,
		}
		if got := SourceFromTelemetry(SourceToTelemetry(source)); !reflect.DeepEqual(got, source) {
			t.Fatalf("source %d-confirmation round trip = %#v, want %#v", len(confirmationFlags), got, source)
		}
		summary := SourceSummaryFromSource(source)
		if summary.ID != "42" || summary.ConfirmationWindowSize != len(confirmationFlags) || summary.ConfirmedAt != "1970-01-01T00:00:00.303Z" {
			t.Fatalf("source summary = %#v", summary)
		}
		detail := SourceDetailFromSource(source)
		if !reflect.DeepEqual(detail.ConfirmationFlags, confirmationFlags) || detail.ConfirmationStartedAt != "1970-01-01T00:00:00.101Z" || detail.ConfirmationEndedReportEpochMS != "300" {
			t.Fatalf("source detail = %#v", detail)
		}
		list := ListResponseFromSources([]Source{source}, new(int64(41)))
		if len(list.Items) != 1 || list.Items[0] != summary || list.NextBefore == nil || *list.NextBefore != "41" {
			t.Fatalf("list response = %#v", list)
		}
	}
}

func TestSourceProjectionsFormatExactSecondWithMilliseconds(t *testing.T) {
	source := Source{
		ConfirmationStartedAtMS: 1_000,
		ConfirmationEndedAtMS:   2_000,
	}

	summary := SourceSummaryFromSource(source)
	detail := SourceDetailFromSource(source)
	if summary.ConfirmedAt != "1970-01-01T00:00:02.000Z" ||
		detail.ConfirmationStartedAt != "1970-01-01T00:00:01.000Z" ||
		detail.ConfirmationEndedAt != "1970-01-01T00:00:02.000Z" {
		t.Fatalf("public exact-second timestamps = summary %#v, detail %#v", summary, detail)
	}
}

func TestTelemetryStoreConsumeTransactionalRollsBackAllDetectorEffects(t *testing.T) {
	ctx := context.Background()
	db, err := telemetry.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open telemetry database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store := NewTelemetryStore(telemetry.NewSessionDropStore(db))
	observation := Observation{
		CanonicalHost: "RDSH-01", ReportEpochMS: 300, AcceptedAtMS: 303,
		LocalDate: "2026-09-27", SessionPresence: SessionPresencePresent,
		ActiveSessions: new(4), DisconnectedSessions: new(0), TotalSessions: new(4),
		Freshness: FreshnessFresh, Drain: DrainContextNone, ClassificationContext: ClassificationUnexplained,
	}
	if err := store.InsertObservation(ctx, observation); err != nil {
		t.Fatalf("insert observation: %v", err)
	}

	rollback := errors.New("abort detector consume")
	consumed, err := store.ConsumeNextTransactional(ctx, func(txStore Store, got Observation) error {
		if got.CanonicalHost != "RDSH-01" {
			t.Fatalf("consumed observation = %#v", got)
		}
		if err := txStore.UpsertBaseline(ctx, Baseline{CanonicalHost: got.CanonicalHost, Scope: BaselineScopeAllHours, ModelVersion: BaselineModelVersion, Alpha: 3, Beta: 2, ObservationCount: 1, FirstTrainedAtMS: new(got.AcceptedAtMS), LastNormalTrainedAtMS: new(got.AcceptedAtMS), LastUpdatedAtMS: got.AcceptedAtMS}); err != nil {
			return err
		}
		if err := txStore.SaveDetectorState(ctx, DetectorState{CanonicalHost: got.CanonicalHost, LastScoredReportEpochMS: got.ReportEpochMS, StateUpdatedAtMS: got.AcceptedAtMS}); err != nil {
			return err
		}
		if _, _, err := txStore.InsertSource(ctx, Source{
			RegisteredHost: got.CanonicalHost, ReportEpochMS: got.ReportEpochMS, AcceptedAtMS: got.AcceptedAtMS, LocalDate: got.LocalDate,
			DetectedAtMS: got.AcceptedAtMS, ConfirmationStartedReportEpochMS: got.ReportEpochMS, ConfirmationEndedReportEpochMS: got.ReportEpochMS,
			ConfirmationStartedAtMS: got.AcceptedAtMS, ConfirmationEndedAtMS: got.AcceptedAtMS, BaselineScope: BaselineScopeAllHours,
			ConfirmationFlags: []bool{true, true}, ConfirmationCount: 2, FreshnessContext: FreshnessFresh, DrainContext: DrainContextNone, Classification: ClassificationUnexplained, InvestigationEligible: true,
		}); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) || consumed {
		t.Fatalf("failed consume = (%t, %v), want rollback", consumed, err)
	}

	if pending, err := store.PendingObservation(ctx); err != nil || pending == nil || pending.AcceptedSequence == 0 {
		t.Fatalf("pending after rollback = %#v, %v", pending, err)
	}
	if baseline, err := store.Baseline(ctx, "RDSH-01", BaselineScopeAllHours, nil); err != nil || baseline != nil {
		t.Fatalf("baseline after rollback = %#v, %v", baseline, err)
	}
	if state, err := store.DetectorState(ctx, "RDSH-01"); err != nil || state != nil {
		t.Fatalf("state after rollback = %#v, %v", state, err)
	}
	if sources, err := store.ListSources(ctx, 10, 0); err != nil || len(sources) != 0 {
		t.Fatalf("sources after rollback = %#v, %v", sources, err)
	}
}

func TestTelemetryStoreConsumeTransactionalCommitsEffectsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	db, err := telemetry.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open telemetry database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store := NewTelemetryStore(telemetry.NewSessionDropStore(db))
	if err := store.InsertObservation(ctx, Observation{CanonicalHost: "RDSH-01", ReportEpochMS: 300, AcceptedAtMS: 303, LocalDate: "2026-09-27", SessionPresence: SessionPresencePresent, ActiveSessions: new(4), DisconnectedSessions: new(0), TotalSessions: new(4), Freshness: FreshnessFresh, Drain: DrainContextNone, ClassificationContext: ClassificationUnexplained}); err != nil {
		t.Fatalf("insert observation: %v", err)
	}
	callbacks := 0
	consumed, err := store.ConsumeNextTransactional(ctx, func(txStore Store, observation Observation) error {
		callbacks++
		return txStore.SaveDetectorState(ctx, DetectorState{CanonicalHost: observation.CanonicalHost, LastScoredReportEpochMS: observation.ReportEpochMS, StateUpdatedAtMS: observation.AcceptedAtMS})
	})
	if err != nil || !consumed {
		t.Fatalf("consume = (%t, %v)", consumed, err)
	}
	consumed, err = store.ConsumeNextTransactional(ctx, func(Store, Observation) error {
		callbacks++
		return nil
	})
	if err != nil || consumed || callbacks != 1 {
		t.Fatalf("second consume = (%t, %v), callbacks = %d; want empty inbox and one callback", consumed, err, callbacks)
	}
	if pending, err := store.PendingObservation(ctx); err != nil || pending != nil {
		t.Fatalf("pending after commit = %#v, %v", pending, err)
	}
	if state, err := store.DetectorState(ctx, "RDSH-01"); err != nil || state == nil || state.LastScoredReportEpochMS != 300 {
		t.Fatalf("committed detector state = %#v, %v", state, err)
	}
}
