//go:build windows

package dashboard

import (
	"context"
	"testing"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/investigation"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondrop"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

func TestFeatureRuntimeDefaultDisabledAutomaticDoesNotAdmitAndPublishesAfterCommit(t *testing.T) {
	ctx := context.Background()
	db, err := telemetry.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open telemetry database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	runtime, ok := NewFeatureRuntime(db, sessiondrop.DefaultSettings()).(*productionFeatureRuntime)
	if !ok {
		t.Fatal("feature runtime does not expose production implementation")
	}
	// The provider is otherwise ready, but automatic admission remains disabled
	// by default. A confirmed eligible source must not create an attempt.
	runtime.configuration = investigation.NewConfigAccessor(func(context.Context) (investigation.ConfigSnapshot, error) {
		return investigation.ConfigSnapshot{
			AccessEnabled: true, AutomaticEnabled: false,
			PrivacyAcknowledgementVersion: "openai_responses_privacy_v1", AcknowledgementReferenceCurrent: true,
			CredentialCiphertext: "protected-credential",
		}, nil
	}, func(string) ([]byte, error) { return []byte("unused"), nil })

	first := int64(1)
	last := int64(24*60*60*1000 + 1)
	if err := runtime.inbox.UpsertBaseline(ctx, sessiondrop.Baseline{
		CanonicalHost: "RDSH-01", Scope: sessiondrop.BaselineScopeAllHours, ModelVersion: sessiondrop.BaselineModelVersion,
		Alpha: 1001, Beta: 10, ObservationCount: sessiondrop.FallbackMinimumObservations,
		FirstTrainedAtMS: &first, LastNormalTrainedAtMS: &last, LastUpdatedAtMS: last,
	}); err != nil {
		t.Fatalf("seed mature baseline: %v", err)
	}
	for _, observation := range []sessiondrop.Observation{
		featureRuntimePresentObservation(100, 101, 100),
		featureRuntimePresentObservation(200, 202, 0),
		featureRuntimePresentObservation(300, 303, 0),
	} {
		if err := runtime.inbox.InsertObservation(ctx, observation); err != nil {
			t.Fatalf("insert observation: %v", err)
		}
	}

	published := 0
	runtime.publishSessionDrop = func(event sessiondrop.SSEEvent) {
		published++
		if event.SourceID == "" {
			t.Fatal("published source event has no durable source ID")
		}
		sources, err := runtime.inbox.ListSources(ctx, 10, 0)
		if err != nil || len(sources) != 1 {
			t.Fatalf("source was not durable when published: %#v, %v", sources, err)
		}
	}
	runtime.publishInvestigation = func(investigation.Update) {
		t.Fatal("automatic-disabled runtime published an investigation attempt")
	}

	if err := runtime.DrainInbox(ctx); err != nil {
		t.Fatalf("drain inbox: %v", err)
	}
	if published != 1 {
		t.Fatalf("session-drop publications = %d, want exactly one", published)
	}
	counts, err := runtime.attempts.Counts(ctx)
	if err != nil {
		t.Fatalf("count investigation attempts: %v", err)
	}
	if counts.Queued != 0 || counts.Running != 0 || counts.Completed != 0 || counts.InsufficientEvidence != 0 || counts.Failed != 0 {
		t.Fatalf("automatic-disabled runtime admitted attempts: %#v", counts)
	}
}

func TestFeatureRuntimeWorkerPassRecoversExpiredLeasesBeforeAdmission(t *testing.T) {
	ctx := context.Background()
	db, err := telemetry.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open telemetry database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	runtime := NewFeatureRuntime(db, sessiondrop.DefaultSettings()).(*productionFeatureRuntime)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	clock := now
	runtime.investigation.Now = func() time.Time { return clock }
	runtime.controller.Now = func() time.Time { return clock }
	ready := investigation.NewConfigAccessor(func(context.Context) (investigation.ConfigSnapshot, error) {
		return investigation.ConfigSnapshot{
			AccessEnabled: true, AutomaticEnabled: true,
			PrivacyAcknowledgementVersion: "openai_responses_privacy_v1", AcknowledgementReferenceCurrent: true,
			CredentialCiphertext: "protected-credential",
		}, nil
	}, func(string) ([]byte, error) { return []byte("credential"), nil })
	runtime.configuration = ready
	runtime.investigation.Configuration = ready
	runtime.controller.Readiness = ready

	for i := range investigation.MaxNonterminalAttempts {
		source := featureRuntimeLeaseSpike(t, ctx, runtime.spikes, now, i)
		attempt, err := runtime.controller.CreateManual(ctx, source)
		if err != nil {
			t.Fatalf("create attempt %d: %v", i, err)
		}
		claimed, found, err := runtime.attempts.ClaimNext(ctx, 0, clock.UnixMilli())
		if err != nil || !found || claimed.ID != attempt.ID {
			t.Fatalf("claim attempt %d = (%+v, %v, %v)", i, claimed, found, err)
		}
		if err := runtime.attempts.AuthorizeSend(ctx, attempt.ID, clock.UnixMilli()); err != nil {
			t.Fatalf("authorize attempt %d: %v", i, err)
		}
	}
	extra := featureRuntimeLeaseSpike(t, ctx, runtime.spikes, now, investigation.MaxNonterminalAttempts)
	if _, admitted, err := runtime.controller.CreateAutomatic(ctx, extra); err != nil || admitted {
		t.Fatalf("admission at capacity = (%v, %v), want false, nil", admitted, err)
	}

	clock = now.Add(time.Duration(investigation.SendLeaseMilliseconds-1) * time.Millisecond)
	if err := runtime.runWorkerPass(ctx); err != nil {
		t.Fatalf("worker pass before lease deadline: %v", err)
	}
	counts, err := runtime.attempts.Counts(ctx)
	if err != nil {
		t.Fatalf("count active leases before deadline: %v", err)
	}
	if counts.Running != investigation.MaxNonterminalAttempts {
		t.Fatalf("running attempts before deadline = %d, want %d", counts.Running, investigation.MaxNonterminalAttempts)
	}

	clock = now.Add(time.Duration(investigation.SendLeaseMilliseconds) * time.Millisecond)
	if err := runtime.runWorkerPass(ctx); err != nil {
		t.Fatalf("worker pass at lease deadline: %v", err)
	}
	counts, err = runtime.attempts.Counts(ctx)
	if err != nil {
		t.Fatalf("count recovered leases: %v", err)
	}
	if counts.Running != 0 || counts.Failed != investigation.MaxNonterminalAttempts {
		t.Fatalf("counts after lease recovery = %#v, want no running and %d failed", counts, investigation.MaxNonterminalAttempts)
	}
	if _, admitted, err := runtime.controller.CreateAutomatic(ctx, extra); err != nil || !admitted {
		t.Fatalf("admission after lease recovery = (%v, %v), want true, nil", admitted, err)
	}
}

func TestFeatureRuntimeWorkerPassRecoversExpiredFinalizationLease(t *testing.T) {
	ctx := context.Background()
	db, err := telemetry.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open telemetry database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	runtime := NewFeatureRuntime(db, sessiondrop.DefaultSettings()).(*productionFeatureRuntime)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	clock := now
	runtime.investigation.Now = func() time.Time { return clock }
	runtime.controller.Now = func() time.Time { return clock }
	ready := investigation.NewConfigAccessor(func(context.Context) (investigation.ConfigSnapshot, error) {
		return investigation.ConfigSnapshot{
			AccessEnabled: true, AutomaticEnabled: true,
			PrivacyAcknowledgementVersion: "openai_responses_privacy_v1", AcknowledgementReferenceCurrent: true,
			CredentialCiphertext: "protected-credential",
		}, nil
	}, func(string) ([]byte, error) { return []byte("credential"), nil })
	runtime.investigation.Configuration = ready
	runtime.controller.Readiness = ready

	attempt, err := runtime.controller.CreateManual(ctx, featureRuntimeLeaseSpike(t, ctx, runtime.spikes, now, 0))
	if err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	claimed, found, err := runtime.attempts.ClaimNext(ctx, 0, clock.UnixMilli())
	if err != nil || !found || claimed.ID != attempt.ID {
		t.Fatalf("claim attempt = (%+v, %v, %v)", claimed, found, err)
	}
	if err := runtime.attempts.AuthorizeSend(ctx, attempt.ID, clock.UnixMilli()); err != nil {
		t.Fatalf("authorize attempt: %v", err)
	}
	if err := runtime.attempts.CompleteSend(ctx, attempt.ID, clock.UnixMilli()); err != nil {
		t.Fatalf("complete attempt: %v", err)
	}

	clock = now.Add(time.Duration(investigation.FinalizationLeaseMilliseconds-1) * time.Millisecond)
	if err := runtime.runWorkerPass(ctx); err != nil {
		t.Fatalf("worker pass before finalization deadline: %v", err)
	}
	counts, err := runtime.attempts.Counts(ctx)
	if err != nil {
		t.Fatalf("count active finalization lease: %v", err)
	}
	if counts.Running != 1 {
		t.Fatalf("running attempts before finalization deadline = %d, want 1", counts.Running)
	}

	clock = now.Add(time.Duration(investigation.FinalizationLeaseMilliseconds) * time.Millisecond)
	if err := runtime.runWorkerPass(ctx); err != nil {
		t.Fatalf("worker pass at finalization deadline: %v", err)
	}
	counts, err = runtime.attempts.Counts(ctx)
	if err != nil {
		t.Fatalf("count recovered finalization lease: %v", err)
	}
	if counts.Running != 0 || counts.Failed != 1 {
		t.Fatalf("counts after finalization lease recovery = %#v, want no running and one failed", counts)
	}
}

func featureRuntimeLeaseSpike(t *testing.T, ctx context.Context, store *telemetry.EventSpikeStore, now time.Time, n int) investigation.SourceRef {
	t.Helper()
	windowStart := now.Add(-time.Hour).Add(time.Duration(n) * time.Millisecond)
	spike, inserted, err := store.Insert(ctx, telemetry.EventSpike{
		Host: "lease-test", Channel: "agent",
		WindowStart: windowStart, WindowEnd: windowStart.Add(time.Minute),
		Observed: 1, Expected: 0, TailProbability: 0.01, ConfirmationCount: 1, FirstSeenAt: now,
	})
	if err != nil || !inserted {
		t.Fatalf("insert spike %d = (%+v, %v, %v)", n, spike, inserted, err)
	}
	return investigation.SourceRef{Kind: investigation.SourceKindEventSpike, ID: spike.ID}
}

func featureRuntimePresentObservation(epoch, accepted int64, total int) sessiondrop.Observation {
	return sessiondrop.Observation{
		CanonicalHost: "RDSH-01", ReportEpochMS: epoch, AcceptedAtMS: accepted, LocalDate: "2026-09-27",
		SessionPresence: sessiondrop.SessionPresencePresent, ActiveSessions: new(total), DisconnectedSessions: new(0), TotalSessions: new(total),
		Freshness: sessiondrop.FreshnessFresh, Drain: sessiondrop.DrainContextNone, ClassificationContext: sessiondrop.ClassificationUnexplained,
	}
}
