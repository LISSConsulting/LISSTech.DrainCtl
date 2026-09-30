//go:build windows

package telemetry

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
)

func int64Pointer(value int64) *int64 { return &value }
func intPointer(value int) *int       { return &value }
func boolPointer(value bool) *bool    { return &value }

func TestSessionDropStore_ConsumesInboxBySequenceNotAcceptanceTime(t *testing.T) {
	store := NewSessionDropStore(openTestDB(t))
	ctx := context.Background()
	first := SessionDropObservation{CanonicalHost: "srv-a", ReportEpochMs: 100, AcceptedAtMs: 900, LocalDate: "2026-09-27", SessionPresence: "present", ActiveSessions: int64Pointer(2), DisconnectedSessions: int64Pointer(1), FreshnessContext: "fresh", DrainContext: "none", ClassificationContext: "unexplained"}
	second := SessionDropObservation{CanonicalHost: "srv-b", ReportEpochMs: 101, AcceptedAtMs: 100, LocalDate: "2026-09-27", SessionPresence: "present", ActiveSessions: int64Pointer(4), DisconnectedSessions: int64Pointer(0), FreshnessContext: "fresh", DrainContext: "none", ClassificationContext: "unexplained"}
	if err := store.InsertObservation(ctx, first); err != nil {
		t.Fatalf("insert first: %v", err)
	}
	if err := store.InsertObservation(ctx, second); err != nil {
		t.Fatalf("insert second: %v", err)
	}
	// The duplicate must retain the initial accepted sequence and immutable fields.
	first.AcceptedAtMs = 1
	if err := store.InsertObservation(ctx, first); err != nil {
		t.Fatalf("insert duplicate: %v", err)
	}
	var consumed []string
	for {
		ok, err := store.ConsumeNext(ctx, func(_ context.Context, tx *sql.Tx, observation SessionDropObservation) error {
			consumed = append(consumed, observation.CanonicalHost)
			return store.SaveDetectorStateTx(ctx, tx, SessionDropDetectorState{Host: observation.CanonicalHost, LastScoredReportEpochMs: observation.ReportEpochMs, LastGapReason: "", StateUpdatedAtMs: observation.AcceptedAtMs})
		})
		if err != nil {
			t.Fatalf("consume next: %v", err)
		}
		if !ok {
			break
		}
	}
	if want := []string{"SRV-A", "SRV-B"}; !reflect.DeepEqual(consumed, want) {
		t.Fatalf("consume order = %v, want %v", consumed, want)
	}
	var pending int
	if err := store.db.reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_drop_observation_inbox`).Scan(&pending); err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if pending != 0 {
		t.Fatalf("pending inbox rows = %d, want 0", pending)
	}
	state, err := store.DetectorState(ctx, "srv-a")
	if err != nil || state == nil || state.LastScoredReportEpochMs != 100 {
		t.Fatalf("state after consumption = %#v, %v", state, err)
	}
}

func TestSessionDropStore_HostBaselineStateAndSourcesAreIsolated(t *testing.T) {
	store := NewSessionDropStore(openTestDB(t))
	ctx := context.Background()
	for _, host := range []string{"srv-a", "srv-b"} {
		baseline := SessionDropBaseline{Host: host, Scope: "all_hours", ModelVersion: sessionDropModelVersion, Alpha: 9, Beta: 3, ObservationCount: 2, FirstTrainedAtMs: int64Pointer(100), LastNormalTrainedAtMs: int64Pointer(200), LastUpdatedAtMs: 300, LocalDates: []string{"2026-09-26", "2026-09-27"}}
		if err := store.UpsertBaseline(ctx, baseline); err != nil {
			t.Fatalf("upsert %s baseline: %v", host, err)
		}
		if err := store.SaveDetectorState(ctx, SessionDropDetectorState{Host: host, LastScoredReportEpochMs: 100, LastReferenceTotal: int64Pointer(10), StateUpdatedAtMs: 300}); err != nil {
			t.Fatalf("save %s state: %v", host, err)
		}
	}
	if err := store.UpsertBaseline(ctx, SessionDropBaseline{
		Host: "srv-a", Scope: "slot", SlotIndex: intPointer(95), ModelVersion: sessionDropModelVersion,
		Alpha: 3, Beta: 2, ObservationCount: 1, FirstTrainedAtMs: int64Pointer(400),
		LastNormalTrainedAtMs: int64Pointer(400), LastUpdatedAtMs: 400, LocalDates: []string{"2026-10-25"},
	}); err != nil {
		t.Fatalf("upsert DST-adjacent slot baseline: %v", err)
	}
	slot := intPointer(95)
	slotBaseline, err := store.Baseline(ctx, "srv-a", "slot", slot)
	if err != nil || slotBaseline == nil || slotBaseline.SlotIndex == nil || *slotBaseline.SlotIndex != 95 || !reflect.DeepEqual(slotBaseline.LocalDates, []string{"2026-10-25"}) {
		t.Fatalf("slot baseline = %#v, %v", slotBaseline, err)
	}
	baseline, err := store.Baseline(ctx, "srv-a", "all_hours", nil)
	if err != nil {
		t.Fatalf("get baseline: %v", err)
	}
	if baseline == nil || baseline.LastNormalTrainedAtMs == nil || *baseline.LastNormalTrainedAtMs != 200 || !reflect.DeepEqual(baseline.LocalDates, []string{"2026-09-26", "2026-09-27"}) {
		t.Fatalf("baseline = %#v", baseline)
	}
	if err := store.SaveDetectorState(ctx, SessionDropDetectorState{Host: "srv-a", LastScoredReportEpochMs: 101, LastReferenceTotal: int64Pointer(10), StateUpdatedAtMs: 999}); err != nil {
		t.Fatalf("update state: %v", err)
	}
	baseline, err = store.Baseline(ctx, "srv-a", "all_hours", nil)
	if err != nil {
		t.Fatalf("get baseline after state update: %v", err)
	}
	if got := *baseline.LastNormalTrainedAtMs; got != 200 {
		t.Fatalf("last normal training = %d, want unchanged 200", got)
	}
	other, err := store.Baseline(ctx, "srv-b", "all_hours", nil)
	if err != nil || other == nil || other.ObservationCount != 2 {
		t.Fatalf("other baseline = %#v, %v", other, err)
	}
}

func TestSessionDropStore_PersistsConfirmationAcceptanceTimesAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	store := NewSessionDropStore(db)
	ctx := context.Background()
	overlap := "overlap"
	none := "none"
	unknown := "unknown"
	state := SessionDropDetectorState{
		Host:                     "srv-a",
		LastScoredReportEpochMs:  103,
		ConfirmationReportEpochs: [3]*int64{int64Pointer(101), int64Pointer(102), int64Pointer(103)},
		ConfirmationCandidates:   [3]*bool{boolPointer(true), boolPointer(false), boolPointer(true)},
		ConfirmationContexts:     [3]*string{&none, &overlap, &unknown},
		ConfirmationAcceptedAtMs: [3]*int64{int64Pointer(801), int64Pointer(905), int64Pointer(999)},
		StateUpdatedAtMs:         1000,
	}
	if err := store.SaveDetectorState(ctx, state); err != nil {
		t.Fatalf("save detector state: %v", err)
	}
	source := testSessionDropSource("srv-a", 102)
	source.ConfirmationStartedAtMs = *state.ConfirmationAcceptedAtMs[0]
	source.ConfirmationEndedAtMs = *state.ConfirmationAcceptedAtMs[1]
	source.AcceptedAtMs = *state.ConfirmationAcceptedAtMs[1]
	created, inserted, err := store.InsertSource(ctx, source)
	if err != nil || !inserted {
		t.Fatalf("insert source = %#v, %t, %v", created, inserted, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	db, err = Open(dir)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store = NewSessionDropStore(db)
	loadedState, err := store.DetectorState(ctx, "srv-a")
	if err != nil || loadedState == nil {
		t.Fatalf("load detector state = %#v, %v", loadedState, err)
	}
	if !reflect.DeepEqual(loadedState.ConfirmationReportEpochs, state.ConfirmationReportEpochs) ||
		!reflect.DeepEqual(loadedState.ConfirmationCandidates, state.ConfirmationCandidates) ||
		!reflect.DeepEqual(loadedState.ConfirmationContexts, state.ConfirmationContexts) ||
		!reflect.DeepEqual(loadedState.ConfirmationAcceptedAtMs, state.ConfirmationAcceptedAtMs) {
		t.Fatalf("loaded confirmation state = %#v", loadedState)
	}
	loadedSource, err := store.Source(ctx, created.ID)
	if err != nil || loadedSource == nil || loadedSource.ConfirmationStartedAtMs != 801 || loadedSource.ConfirmationEndedAtMs != 905 {
		t.Fatalf("loaded source = %#v, %v", loadedSource, err)
	}
}

func TestSessionDropStore_RejectsPartialConfirmation(t *testing.T) {
	store := NewSessionDropStore(openTestDB(t))
	err := store.SaveDetectorState(context.Background(), SessionDropDetectorState{
		Host:                     "srv-a",
		ConfirmationReportEpochs: [3]*int64{int64Pointer(100)},
		ConfirmationCandidates:   [3]*bool{boolPointer(true)},
		ConfirmationContexts:     [3]*string{new("none")},
		StateUpdatedAtMs:         100,
	})
	if err == nil {
		t.Fatal("save partial confirmation succeeded")
	}
}

func TestServerStore_UpdateAcceptedCommitsInboxWithServerUpdate(t *testing.T) {
	db := openTestDB(t)
	servers := NewServerStore(db)
	inbox := NewSessionDropStore(db)
	ctx := context.Background()
	if err := servers.Register(ctx, "srv-a"); err != nil {
		t.Fatalf("register: %v", err)
	}
	observation := SessionDropObservation{ReportEpochMs: 123, AcceptedAtMs: 456, LocalDate: "2026-09-27", SessionPresence: "present", ActiveSessions: int64Pointer(3), DisconnectedSessions: int64Pointer(2), FreshnessContext: "fresh", DrainContext: "none", ClassificationContext: "unexplained"}
	updated, err := servers.UpdateAccepted(ctx, "srv-a", `{"healthy":true}`, observation)
	if err != nil || !updated {
		t.Fatalf("update accepted = %t, %v", updated, err)
	}
	pending, err := inbox.PendingObservation(ctx)
	if err != nil || pending == nil {
		t.Fatalf("pending observation = %#v, %v", pending, err)
	}
	if pending.CanonicalHost != "SRV-A" || pending.AcceptedSequence == 0 || pending.ReportEpochMs != 123 || pending.AcceptedAtMs != 456 {
		t.Fatalf("pending observation = %#v", pending)
	}
	server, err := servers.Get(ctx, "srv-a")
	if err != nil || server == nil || server.LastResultJSON != `{"healthy":true}` {
		t.Fatalf("server = %#v, %v", server, err)
	}
	// Duplicate acceptance keeps the initial inbox observation rather than
	// allocating another sequence.
	originalSequence := pending.AcceptedSequence
	observation.AcceptedAtMs = 789
	if updated, err := servers.UpdateAccepted(ctx, "srv-a", `{"healthy":false}`, observation); err != nil || !updated {
		t.Fatalf("duplicate update accepted = %t, %v", updated, err)
	}
	pending, err = inbox.PendingObservation(ctx)
	if err != nil || pending == nil || pending.AcceptedSequence != originalSequence || pending.AcceptedAtMs != 456 {
		t.Fatalf("duplicate pending observation = %#v, %v", pending, err)
	}
	if updated, err := servers.UpdateAccepted(ctx, "missing", `{}`, observation); err != nil || updated {
		t.Fatalf("missing update accepted = %t, %v", updated, err)
	}
}
func testSessionDropSource(host string, endingEpoch int64) SessionDropSource {
	return SessionDropSource{Host: host, ReportEpochMs: endingEpoch, AcceptedAtMs: 900, LocalDate: "2026-09-27", DetectedAtMs: 1000, ConfirmationStartedReportEpochMs: endingEpoch - 1, ConfirmationEndedReportEpochMs: endingEpoch, ConfirmationStartedAtMs: 800, ConfirmationEndedAtMs: 900, ObservedSessions: 4, ReferenceSessions: 12, ExpectedSessions: 10, BaselineModelVersion: sessionDropModelVersion, BaselineScope: "slot", SlotIndex: intPointer(3), SlotMatureDays: intPointer(7), TailProbability: 0.001, AbsoluteLoss: 8, RelativeLoss: 2.0 / 3.0, ConfirmationWindowSize: 2, ConfirmationCandidates: [3]*bool{boolPointer(true), boolPointer(true), nil}, ConfirmationCount: 2, FreshnessContext: "fresh", DrainContext: "none", Classification: "unexplained", ProviderEligible: true}
}
