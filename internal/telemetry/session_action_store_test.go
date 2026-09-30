//go:build windows

package telemetry

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
)

func testAction(id, key string, created time.Time) sessiondata.SessionAction {
	return sessiondata.SessionAction{ActionID: id, CanonicalHost: "host.example.test", SessionID: 7, ExpectedLogonAtMS: 99, Type: sessiondata.SessionActionMessage, State: sessiondata.SessionActionQueued, CreatedAtMS: created.UnixMilli(), ExpiresAtMS: created.Add(5 * time.Minute).UnixMilli(), RequestedBy: "admin", IdempotencyKey: key, RequestFingerprint: []byte("fingerprint")}
}
func enqueueTestAction(t *testing.T, s *SessionActionStore, a sessiondata.SessionAction) {
	t.Helper()
	if _, _, err := s.Enqueue(context.Background(), SessionActionEnqueue{Action: a, IdempotencyEndpoint: "POST /sessions/host.example.test/7/actions", MessageCiphertext: []byte("ciphertext"), MessageProtection: "dpapi"}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionActionStoreConcurrentIdempotencyAndConflict(t *testing.T) {
	db := openTestDB(t)
	s := NewSessionActionStore(db)
	now := time.UnixMilli(1000000)
	a := testAction("action-one", "key", now)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	replays := make(chan bool, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, replay, err := s.Enqueue(context.Background(), SessionActionEnqueue{Action: a, IdempotencyEndpoint: "endpoint", MessageCiphertext: []byte("ciphertext"), MessageProtection: "dpapi"})
			errs <- err
			replays <- replay
		}()
	}
	wg.Wait()
	close(errs)
	close(replays)
	newCount := 0
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for replay := range replays {
		if !replay {
			newCount++
		}
	}
	if newCount != 1 {
		t.Fatalf("new inserts=%d, want 1", newCount)
	}
	changed := a
	changed.RequestFingerprint = []byte("changed")
	_, _, err := s.Enqueue(context.Background(), SessionActionEnqueue{Action: changed, IdempotencyEndpoint: "endpoint", MessageCiphertext: []byte("ciphertext"), MessageProtection: "dpapi"})
	if !errors.Is(err, ErrSessionActionIdempotencyConflict) {
		t.Fatalf("conflict err=%v", err)
	}
}

func TestSessionActionStoreDeliveryExpiryCompletionAndErasure(t *testing.T) {
	db := openTestDB(t)
	s := NewSessionActionStore(db)
	now := time.UnixMilli(2000000)
	for _, id := range []string{"b", "a"} {
		a := testAction(id, id, now)
		enqueueTestAction(t, s, a)
	}
	first, err := s.Deliver(context.Background(), "host.example.test", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].Action.ActionID != "a" || first[1].Action.ActionID != "b" {
		t.Fatalf("delivery order=%+v", first)
	}
	second, err := s.Deliver(context.Background(), "host.example.test", now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 2 || second[0].Action.ActionID != "a" {
		t.Fatalf("redelivery=%+v", second)
	}
	status, err := s.Complete(context.Background(), "host.example.test", "a", sessiondata.SessionActionOutcomeCompleted, now.Add(3*time.Second))
	if err != nil || status.State != sessiondata.SessionActionCompleted {
		t.Fatalf("completion=%+v %v", status, err)
	}
	var ciphertext any
	var protection any
	if err := db.reader.QueryRow(`SELECT message_ciphertext,message_protection FROM session_action_outbox WHERE action_id='a'`).Scan(&ciphertext, &protection); err != nil {
		t.Fatal(err)
	}
	if ciphertext != nil || protection != nil {
		t.Fatalf("terminal message retained: %v %v", ciphertext, protection)
	}
	late := testAction("late", "late", now)
	enqueueTestAction(t, s, late)
	expired, err := s.Expire(context.Background(), time.UnixMilli(late.ExpiresAtMS))
	if err != nil || len(expired) != 2 {
		t.Fatalf("expiry transitions=%+v %v", expired, err)
	}
	var lateTransition *sessiondata.SessionActionStatus
	for i := range expired {
		if expired[i].ActionID == "late" {
			lateTransition = &expired[i]
			break
		}
	}
	if lateTransition == nil || lateTransition.State != sessiondata.SessionActionExpired || lateTransition.ResultCode == nil || *lateTransition.ResultCode != "expired" {
		t.Fatalf("late expiry transition=%+v", lateTransition)
	}
	st, err := s.Status(context.Background(), "late")
	if err != nil || st.ActionID != lateTransition.ActionID || st.State != lateTransition.State || st.ResultCode == nil || *st.ResultCode != "expired" {
		t.Fatalf("expired status=%+v transition=%+v err=%v", st, lateTransition, err)
	}
}

func TestSessionActionStoreCompletionReplaysPreserveTerminalTransition(t *testing.T) {
	db := openTestDB(t)
	s := NewSessionActionStore(db)
	now := time.UnixMilli(2500000)
	outcomes := []sessiondata.SessionActionOutcome{
		sessiondata.SessionActionOutcomeCompleted,
		sessiondata.SessionActionOutcomeFailed,
		sessiondata.SessionActionOutcomeExpired,
		sessiondata.SessionActionOutcomeSessionChanged,
		sessiondata.SessionActionOutcomeUnsupported,
	}
	for _, outcome := range outcomes {
		t.Run(string(outcome), func(t *testing.T) {
			action := testAction("replay-"+string(outcome), "replay-"+string(outcome), now)
			enqueueTestAction(t, s, action)
			if _, err := s.Deliver(context.Background(), action.CanonicalHost, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			original, err := s.Complete(context.Background(), action.CanonicalHost, action.ActionID, outcome, now.Add(2*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			for _, replayOutcome := range []sessiondata.SessionActionOutcome{outcome, sessiondata.SessionActionOutcomeDuplicate} {
				replayed, err := s.Complete(context.Background(), action.CanonicalHost, action.ActionID, replayOutcome, now.Add(3*time.Second))
				if err != nil || replayed.ActionID != original.ActionID || replayed.State != original.State || replayed.ResultCode == nil || original.ResultCode == nil || *replayed.ResultCode != *original.ResultCode || replayed.CompletedAtMS == nil || original.CompletedAtMS == nil || *replayed.CompletedAtMS != *original.CompletedAtMS {
					t.Fatalf("replay outcome=%q status=%+v err=%v, want %+v", replayOutcome, replayed, err, original)
				}
			}
			var terminalAudits int
			if err := db.reader.QueryRow(`SELECT COUNT(*) FROM session_action_audit WHERE action_id = ? AND transition = ?`, action.ActionID, string(original.State)).Scan(&terminalAudits); err != nil {
				t.Fatal(err)
			}
			if terminalAudits != 1 {
				t.Fatalf("terminal audit transitions=%d, want 1", terminalAudits)
			}
		})
	}
}

func TestSessionActionStoreCompletionAcknowledgesWinningExpiryOrPrivacyState(t *testing.T) {
	db := openTestDB(t)
	s := NewSessionActionStore(db)
	now := time.UnixMilli(2750000)
	for _, scenario := range []struct {
		name       string
		transition func() ([]sessiondata.SessionActionStatus, error)
		resultCode string
		completeAt time.Time
	}{
		{
			name: "expiry",
			transition: func() ([]sessiondata.SessionActionStatus, error) {
				return s.Expire(context.Background(), now.Add(5*time.Minute))
			},
			resultCode: "expired",
			completeAt: now.Add(5 * time.Minute),
		},
		{
			name: "privacy",
			transition: func() ([]sessiondata.SessionActionStatus, error) {
				return s.CancelForPrivacy(context.Background(), now.Add(time.Second))
			},
			resultCode: "privacy_policy_changed",
			completeAt: now.Add(2 * time.Second),
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			action := testAction("winning-"+scenario.name, "winning-"+scenario.name, now)
			enqueueTestAction(t, s, action)
			if _, err := s.Deliver(context.Background(), action.CanonicalHost, now.Add(time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			if _, err := scenario.transition(); err != nil {
				t.Fatal(err)
			}
			status, err := s.Complete(context.Background(), action.CanonicalHost, action.ActionID, sessiondata.SessionActionOutcomeCompleted, scenario.completeAt)
			if err != nil || status.State != sessiondata.SessionActionExpired || status.ResultCode == nil || *status.ResultCode != scenario.resultCode {
				t.Fatalf("completion status=%+v err=%v", status, err)
			}
			var terminalAudits int
			if err := db.reader.QueryRow(`SELECT COUNT(*) FROM session_action_audit WHERE action_id = ? AND transition = 'expired'`, action.ActionID).Scan(&terminalAudits); err != nil {
				t.Fatal(err)
			}
			if terminalAudits != 1 {
				t.Fatalf("terminal audit transitions=%d, want 1", terminalAudits)
			}
		})
	}
}

func TestSessionActionStorePrivacyProtocolAuditAndRetention(t *testing.T) {
	db := openTestDB(t)
	s := NewSessionActionStore(db)
	now := time.UnixMilli(3000000)
	a := testAction("privacy", "privacy", now)
	enqueueTestAction(t, s, a)
	cancelled, err := s.CancelForPrivacy(context.Background(), now.Add(time.Second))
	if err != nil || len(cancelled) != 1 || cancelled[0].ActionID != "privacy" || cancelled[0].State != sessiondata.SessionActionExpired || cancelled[0].ResultCode == nil || *cancelled[0].ResultCode != "privacy_policy_changed" {
		t.Fatalf("privacy transition=%+v %v", cancelled, err)
	}
	if _, err := s.Complete(context.Background(), "other.example.test", "privacy", sessiondata.SessionActionOutcomeCompleted, now.Add(2*time.Second)); !errors.Is(err, ErrSessionActionProtocolInvalid) {
		t.Fatalf("protocol error=%v", err)
	}
	var plaintext int
	if err := db.reader.QueryRow(`SELECT COUNT(*) FROM session_action_audit WHERE requested_by IS NOT NULL AND requested_by = 'ciphertext'`).Scan(&plaintext); err != nil || plaintext != 0 {
		t.Fatalf("audit leaked ciphertext: %d %v", plaintext, err)
	}
	out, audit, err := s.Retain(context.Background(), now.Add(3*time.Second), now.Add(3*time.Second), 20)
	if err != nil || out != 1 || audit < 3 {
		t.Fatalf("retention out=%d audit=%d err=%v", out, audit, err)
	}
}

func TestSessionActionStorePrivacyPurgeRemovesSnapshotsAndActiveActionsTogether(t *testing.T) {
	db := openTestDB(t)
	snapshots, err := NewSessionSnapshotStore(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(4_000_000)
	snapshots.now = func() time.Time { return now }
	snapshot := sessiondata.SessionSnapshot{
		Schema: sessiondata.SnapshotSchema, Host: "host.example.test",
		AgentInstanceID: "01890f9d-5c00-7000-8000-000000000005", Sequence: 1,
		ObservedAtMS: now.UnixMilli(), CollectorVersion: "test", LogicalCPUCount: 1,
		Sessions: []sessiondata.SessionRecord{{SessionID: 7, State: sessiondata.SessionActive}},
	}
	if _, err := snapshots.Apply(context.Background(), snapshot, sessiondata.PrivacyPolicy{}); err != nil {
		t.Fatal(err)
	}
	actions := NewSessionActionStore(db)
	enqueueTestAction(t, actions, testAction("purge-together", "purge-together", now))
	statuses, err := actions.PurgeSnapshotsForPrivacy(context.Background(), now.Add(time.Second))
	if err != nil || len(statuses) != 1 || statuses[0].State != sessiondata.SessionActionExpired {
		t.Fatalf("privacy transition=%+v err=%v", statuses, err)
	}
	var snapshotCount, activeActionCount int
	if err := db.reader.QueryRow(`SELECT COUNT(*) FROM session_snapshots`).Scan(&snapshotCount); err != nil {
		t.Fatal(err)
	}
	if err := db.reader.QueryRow(`SELECT COUNT(*) FROM session_action_outbox WHERE state IN ('queued', 'delivered')`).Scan(&activeActionCount); err != nil {
		t.Fatal(err)
	}
	if snapshotCount != 0 || activeActionCount != 0 {
		t.Fatalf("privacy purge left snapshots=%d active_actions=%d", snapshotCount, activeActionCount)
	}
}

func TestSessionActionStoreDisconnectPersistsAndDeliversNoMessage(t *testing.T) {
	db := openTestDB(t)
	store := NewSessionActionStore(db)
	now := time.UnixMilli(5_000_000)
	action := testAction("disconnect-action", "disconnect-key", now)
	action.Type = sessiondata.SessionActionDisconnect
	if _, _, err := store.Enqueue(context.Background(), SessionActionEnqueue{
		Action: action, IdempotencyEndpoint: "POST /sessions/host.example.test/7/actions",
	}); err != nil {
		t.Fatalf("enqueue disconnect: %v", err)
	}
	if _, _, err := store.Enqueue(context.Background(), SessionActionEnqueue{
		Action: action, IdempotencyEndpoint: "POST /sessions/host.example.test/7/actions",
		MessageCiphertext: []byte("forbidden"), MessageProtection: "dpapi",
	}); err == nil {
		t.Fatal("disconnect accepted protected message")
	}
	deliveries, err := store.Deliver(context.Background(), action.CanonicalHost, now)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("deliveries=%+v err=%v", deliveries, err)
	}
	delivery := deliveries[0]
	if delivery.Action.Type != sessiondata.SessionActionDisconnect || delivery.MessageCiphertext != nil || delivery.MessageProtection != "" {
		t.Fatalf("disconnect delivery leaks message fields: %+v", delivery)
	}
	status, err := store.Complete(context.Background(), action.CanonicalHost, action.ActionID, sessiondata.SessionActionOutcomeCompleted, now.Add(time.Second))
	if err != nil || status.State != sessiondata.SessionActionCompleted || status.ResultCode == nil || *status.ResultCode != "completed" {
		t.Fatalf("completion=%+v err=%v", status, err)
	}
}
