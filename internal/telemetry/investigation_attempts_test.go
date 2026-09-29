//go:build windows

package telemetry

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

func testEvidence(at time.Time) InvestigationEvidence {
	body := []byte(`{"v":1}`)
	return InvestigationEvidence{Version: 1, SnapshotKind: SnapshotAvailable, SourceTime: at,
		SnapshotAt: at, From: at.Add(-30 * time.Minute), To: at, CanonicalJSON: body,
		Hash: sha256.Sum256(body), FactIDs: []string{"F001"}}
}

func seedInvestigationEventSpike(t *testing.T, db *DB, id int64, now time.Time) {
	t.Helper()
	_, err := db.writer.Exec(`INSERT INTO event_spikes(id,host,channel,window_start_ms,window_end_ms,observed,expected,tail_probability,confirmation_count,first_seen_at_ms,created_at_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		id, "host", "agent", now.Add(time.Duration(id)*time.Millisecond).UnixMilli(), now.Add(time.Minute).UnixMilli(), 1, 1, 0.1, 1, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		t.Fatalf("seed event spike %d: %v", id, err)
	}
}

func TestInvestigationAttempts_GlobalAdmissionBeforeEvidence(t *testing.T) {
	db := openTestDB(t)
	store := NewInvestigationAttemptStore(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	for id := int64(1); id <= maxActiveAttempts; id++ {
		seedInvestigationEventSpike(t, db, id, now.Add(time.Duration(id)*time.Minute))
		if _, err := store.Create(ctx, CreateInvestigationAttempt{Source: InvestigationSource{Kind: SourceEventSpike, ID: id}, Initiation: InitiationManual, CreatedAt: now, Evidence: testEvidence(now)}); err != nil {
			t.Fatalf("create %d: %v", id, err)
		}
	}
	seedInvestigationEventSpike(t, db, maxActiveAttempts+1, now)
	_, err := store.Create(ctx, CreateInvestigationAttempt{Source: InvestigationSource{Kind: SourceEventSpike, ID: maxActiveAttempts + 1}, Initiation: InitiationManual, CreatedAt: now, Evidence: testEvidence(now)})
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("Create error = %v, want ErrQueueFull", err)
	}
	var evidenceCount int
	if err := db.reader.QueryRow(`SELECT COUNT(*) FROM investigation_evidence`).Scan(&evidenceCount); err != nil {
		t.Fatal(err)
	}
	if evidenceCount != maxActiveAttempts {
		t.Fatalf("evidence rows = %d, want %d; rejected admission wrote evidence", evidenceCount, maxActiveAttempts)
	}
}

func TestInvestigationAttemptsSourceExistsAndLatestFailure(t *testing.T) {
	db := openTestDB(t)
	store := NewInvestigationAttemptStore(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	source := InvestigationSource{Kind: SourceEventSpike, ID: 1}
	seedInvestigationEventSpike(t, db, source.ID, now)
	exists, err := store.SourceExists(ctx, source)
	if err != nil || !exists {
		t.Fatalf("source existence = (%v, %v), want (true, nil)", exists, err)
	}
	exists, err = store.SourceExists(ctx, InvestigationSource{Kind: SourceEventSpike, ID: 2})
	if err != nil || exists {
		t.Fatalf("unknown source existence = (%v, %v), want (false, nil)", exists, err)
	}
	attempt, err := store.Create(ctx, CreateInvestigationAttempt{Source: source, Initiation: InitiationManual, CreatedAt: now, Evidence: testEvidence(now)})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimNext(ctx, now.Add(-time.Minute), now); err != nil || !ok {
		t.Fatalf("claim = (%v, %v)", ok, err)
	}
	if err := store.FinalizeFailure(ctx, attempt.ID, ReasonTimeout, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	failure, err := store.LatestFailure(ctx)
	if err != nil || failure == nil || failure.ID != attempt.ID || failure.TerminalReason != ReasonTimeout || failure.CompletedAt == nil || failure.CompletedAt.UnixMilli() != now.Add(time.Second).UnixMilli() {
		t.Fatalf("latest failure = %#v, %v", failure, err)
	}
}

func TestInvestigationAttempts_QueuedRootDeduplicatesBeforeGlobalCap(t *testing.T) {
	db := openTestDB(t)
	store := NewInvestigationAttemptStore(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	for id := int64(1); id <= maxActiveAttempts; id++ {
		seedInvestigationEventSpike(t, db, id, now)
		if _, err := store.Create(ctx, CreateInvestigationAttempt{Source: InvestigationSource{Kind: SourceEventSpike, ID: id}, Initiation: InitiationManual, CreatedAt: now, Evidence: testEvidence(now)}); err != nil {
			t.Fatalf("create %d: %v", id, err)
		}
	}
	got, err := store.Create(ctx, CreateInvestigationAttempt{Source: InvestigationSource{Kind: SourceEventSpike, ID: 1}, Initiation: InitiationManual, CreatedAt: now, Evidence: testEvidence(now)})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 1 || got.State != StateQueued {
		t.Fatalf("duplicate root = %#v", got)
	}
}

func TestInvestigationAttempts_LifecycleLeasesAndRecovery(t *testing.T) {
	db := openTestDB(t)
	store := NewInvestigationAttemptStore(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	newRunning := func(sourceID int64) InvestigationAttempt {
		t.Helper()
		seedInvestigationEventSpike(t, db, sourceID, now)
		attempt, err := store.Create(ctx, CreateInvestigationAttempt{
			Source: InvestigationSource{Kind: SourceEventSpike, ID: sourceID}, Initiation: InitiationManual,
			CreatedAt: now, Evidence: testEvidence(now),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, found, err := store.ClaimNext(ctx, now.Add(-time.Hour), now); err != nil || !found {
			t.Fatalf("ClaimNext = found:%v err:%v", found, err)
		}
		return attempt
	}
	assertRunning := func(id int64) {
		t.Helper()
		got, err := store.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != StateRunning {
			t.Fatalf("attempt %d state=%s, want running", id, got.State)
		}
	}
	assertTerminal := func(id int64, reason InvestigationTerminalReason) {
		t.Helper()
		got, err := store.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != StateFailed || got.TerminalReason != reason ||
			got.SendLeaseExpiresAt != nil || got.FinalizationLeaseExpiresAt != nil {
			t.Fatalf("recovered attempt %d = %+v, want failed/%s without leases", id, got, reason)
		}
		result, provenance, err := store.Result(ctx, id)
		if err != nil || result != nil || provenance != nil {
			t.Fatalf("recovered attempt %d reconstructed result/provenance = (%#v, %#v, %v)", id, result, provenance, err)
		}
	}

	unauthorized := newRunning(7)
	if recovered, err := store.RecoverRunning(ctx, now); err != nil || recovered != 1 {
		t.Fatalf("RecoverRunning unauthorized = (%d, %v), want one recovery", recovered, err)
	}
	assertTerminal(unauthorized.ID, ReasonInterrupted)

	sendPhase := newRunning(8)
	if err := store.AuthorizeSend(ctx, sendPhase.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := store.AuthorizeSend(ctx, sendPhase.ID, now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("overlapping send authorization error = %v, want ErrInvalidTransition", err)
	}
	if recovered, err := store.RecoverRunning(ctx, now.Add(sendLease-time.Millisecond)); err != nil || recovered != 0 {
		t.Fatalf("RecoverRunning before send lease deadline = (%d, %v), want no recovery", recovered, err)
	}
	assertRunning(sendPhase.ID)
	if recovered, err := store.RecoverRunning(ctx, now.Add(sendLease)); err != nil || recovered != 1 {
		t.Fatalf("RecoverRunning at send lease deadline = (%d, %v), want one recovery", recovered, err)
	}
	assertTerminal(sendPhase.ID, ReasonStorageUnavailable)
	if recovered, err := store.RecoverRunning(ctx, now.Add(sendLease+time.Millisecond)); err != nil || recovered != 0 {
		t.Fatalf("RecoverRunning after send lease deadline = (%d, %v), want no repeat recovery", recovered, err)
	}

	finalizationPhase := newRunning(9)
	if err := store.AuthorizeSend(ctx, finalizationPhase.ID, now); err != nil {
		t.Fatal(err)
	}
	returnedAt := now.Add(time.Second)
	if err := store.CompleteSend(ctx, finalizationPhase.ID, returnedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.AuthorizeSend(ctx, finalizationPhase.ID, returnedAt); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("send lease during finalization error = %v, want ErrInvalidTransition", err)
	}
	if recovered, err := store.RecoverRunning(ctx, returnedAt.Add(finalizationLease-time.Millisecond)); err != nil || recovered != 0 {
		t.Fatalf("RecoverRunning before finalization lease deadline = (%d, %v), want no recovery", recovered, err)
	}
	assertRunning(finalizationPhase.ID)
	if recovered, err := store.RecoverRunning(ctx, returnedAt.Add(finalizationLease)); err != nil || recovered != 1 {
		t.Fatalf("RecoverRunning at finalization lease deadline = (%d, %v), want one recovery", recovered, err)
	}
	assertTerminal(finalizationPhase.ID, ReasonStorageUnavailable)
	if recovered, err := store.RecoverRunning(ctx, returnedAt.Add(finalizationLease+time.Millisecond)); err != nil || recovered != 0 {
		t.Fatalf("RecoverRunning after finalization lease deadline = (%d, %v), want no repeat recovery", recovered, err)
	}
	if _, found, err := store.ClaimNext(ctx, now.Add(-time.Hour), returnedAt.Add(finalizationLease+time.Millisecond)); err != nil || found {
		t.Fatalf("ClaimNext after recovery = found:%v err:%v, want no resend", found, err)
	}
}

func TestInvestigationAttempts_PerSourceRetainedCap(t *testing.T) {
	db := openTestDB(t)
	store := NewInvestigationAttemptStore(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	seedInvestigationEventSpike(t, db, 44, now)
	hash := sha256.Sum256([]byte("x"))
	result, err := db.writer.ExecContext(ctx, `INSERT INTO investigation_attempts(source_kind,source_id,attempt_no,initiation,state,created_at_ms,queued_at_ms,started_at_ms,completed_at_ms,terminal_reason,evidence_hash) VALUES('event_spike',44,1,'manual','failed',?,?,?,?, 'network_error',?)`, now.UnixMilli(), now.UnixMilli(), now.UnixMilli(), now.UnixMilli(), hash[:])
	if err != nil {
		t.Fatalf("seed root: %v", err)
	}
	retryOf, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("seed root ID: %v", err)
	}
	for n := 2; n <= maxSourceAttempts; n++ {
		result, err := db.writer.ExecContext(ctx, `INSERT INTO investigation_attempts(source_kind,source_id,attempt_no,initiation,retry_of_attempt_id,state,created_at_ms,queued_at_ms,started_at_ms,completed_at_ms,terminal_reason,evidence_hash) VALUES('event_spike',44,?,'retry',?,'failed',?,?,?,?, 'network_error',?)`, n, retryOf, now.UnixMilli(), now.UnixMilli(), now.UnixMilli(), now.UnixMilli(), hash[:])
		if err != nil {
			t.Fatalf("seed retry %d: %v", n, err)
		}
		retryOf, err = result.LastInsertId()
		if err != nil {
			t.Fatalf("seed retry %d ID: %v", n, err)
		}
	}
	_, err = store.Create(ctx, CreateInvestigationAttempt{Source: InvestigationSource{Kind: SourceEventSpike, ID: 44}, Initiation: InitiationManual, CreatedAt: now, Evidence: testEvidence(now)})
	if !errors.Is(err, ErrAttemptLimitReached) {
		t.Fatalf("Create error = %v, want ErrAttemptLimitReached", err)
	}
}

func TestInvestigationAttempts_ResultReconstructsNormalizedChildrenAndProvenance(t *testing.T) {
	db := openTestDB(t)
	store := NewInvestigationAttemptStore(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	seedInvestigationEventSpike(t, db, 45, now)
	attempt, err := store.Create(ctx, CreateInvestigationAttempt{Source: InvestigationSource{Kind: SourceEventSpike, ID: 45}, Initiation: InitiationManual, CreatedAt: now, Evidence: testEvidence(now)})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.ClaimNext(ctx, now.Add(-time.Hour), now); err != nil || !found {
		t.Fatalf("ClaimNext = found:%v err:%v", found, err)
	}
	authorized := now.Add(time.Second)
	completed := authorized.Add(time.Second)
	if err := store.AuthorizeSend(ctx, attempt.ID, authorized); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteSend(ctx, attempt.ID, completed); err != nil {
		t.Fatal(err)
	}
	report := InvestigationResult{
		OverallAssessment: "indeterminate", EvidenceSufficiency: "partial", HumanReviewRequired: true,
		Summary:           InvestigationSummary{Text: "Review retained metrics.", FactIDs: []string{"F001"}},
		Hypotheses:        []InvestigationHypothesis{{Rank: 1, Confidence: "medium", Text: "A local condition may be relevant.", SupportingFactIDs: []string{"F001"}, ContradictingFactIDs: []string{}}},
		MissingEvidence:   []InvestigationMissingEvidence{{Ordinal: 1, Category: "host_health_detail", Text: "Host health detail is unavailable.", FactIDs: []string{"F001"}}},
		RecommendedChecks: []InvestigationDiagnosticCheck{{Rank: 1, CheckType: "inspect_retained_metrics", Text: "Inspect retained metrics.", FactIDs: []string{"F001"}, HypothesisRanks: []int{1}}},
	}
	provenance := InvestigationProvenance{ProviderProfile: OpenAIResponsesProfile, ProviderEndpoint: OpenAIResponsesEndpoint, RequestedModel: OpenAIResponsesModel, ResponseFormat: InvestigationFormat, SendAuthorizedAt: authorized, SendCompletedAt: completed, RequestHeaderBytes: 1, RequestBodyBytes: 1, ResponseHeaderBytes: 1, ResponseBodyBytes: 1, ValidationOutcome: "accepted"}
	if err := store.FinalizeResult(ctx, attempt.ID, report, provenance, completed); err != nil {
		t.Fatal(err)
	}
	gotReport, gotProvenance, err := store.Result(ctx, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotReport == nil || gotProvenance == nil {
		t.Fatal("result/provenance missing")
	}
	if gotReport.Summary.FactIDs[0] != "F001" || gotReport.Hypotheses[0].SupportingFactIDs[0] != "F001" || len(gotReport.Hypotheses[0].ContradictingFactIDs) != 0 || gotReport.MissingEvidence[0].FactIDs[0] != "F001" || gotReport.RecommendedChecks[0].HypothesisRanks[0] != 1 {
		t.Fatalf("reconstructed report = %#v", gotReport)
	}
	if gotProvenance.SendAuthorizedAt.UnixMilli() != authorized.UnixMilli() || gotProvenance.SendCompletedAt.UnixMilli() != completed.UnixMilli() || gotProvenance.ProviderEndpoint != OpenAIResponsesEndpoint {
		t.Fatalf("reconstructed provenance = %#v", gotProvenance)
	}
}
