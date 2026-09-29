//go:build windows

package dashboard

import (
	"context"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

func enqueueSubsystemAction(t *testing.T, store *telemetry.SessionActionStore, id string, created time.Time) {
	t.Helper()
	action := sessiondata.SessionAction{
		ActionID: id, CanonicalHost: "sessions.example.test", SessionID: 7, ExpectedLogonAtMS: 99,
		Type: sessiondata.SessionActionMessage, State: sessiondata.SessionActionQueued,
		CreatedAtMS: created.UnixMilli(), ExpiresAtMS: created.Add(5 * time.Minute).UnixMilli(),
		RequestedBy: "CONTOSO\\admin", IdempotencyKey: id, RequestFingerprint: []byte(id),
	}
	if _, _, err := store.Enqueue(context.Background(), telemetry.SessionActionEnqueue{
		Action: action, IdempotencyEndpoint: "POST /api/v1/sessions/sessions.example.test/7/actions",
		MessageCiphertext: []byte("protected-" + id), MessageProtection: "dpapi",
	}); err != nil {
		t.Fatalf("enqueue %q: %v", id, err)
	}
}

func TestSubsystemDependencies_PreserveTypedSessionStores(t *testing.T) {
	db, err := telemetry.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	snapshots, err := telemetry.NewSessionSnapshotStore(db)
	if err != nil {
		t.Fatal(err)
	}
	queries, err := telemetry.NewSessionQueryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	actions := telemetry.NewSessionActionStore(db)
	freshness := telemetry.NewFreshnessStore(db)
	servers := telemetry.NewServerStore(db)
	events := telemetry.NewEventSpikeStore(db)

	s := NewSubsystem(dc.DashboardConfig{}, t.TempDir(), SubsystemDependencies{
		Servers: servers, EventSpikes: events, SessionSnapshots: snapshots, SessionQueries: queries,
		SessionActions: actions, Freshness: freshness, LocalForceUpdateSupported: true,
	})
	if s.srv != servers || s.sps != events || s.sessionSnapshots != snapshots || s.sessionQueries != queries || s.sessionActions != actions || s.freshness != freshness || !s.localForceUpdateSupported {
		t.Fatalf("typed dependencies were not preserved: %#v", s)
	}
}

func TestSessionRetention_ExpiresActionsAndPurgesPrivacy(t *testing.T) {
	db, err := telemetry.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	actions := telemetry.NewSessionActionStore(db)
	now := time.UnixMilli(7_000_000)
	enqueueSubsystemAction(t, actions, "expired", now.Add(-6*time.Minute))
	enqueueSubsystemAction(t, actions, "privacy", now)

	ds := newTestServer(t)
	ds.sessionActions = actions
	ds.now = func() time.Time { return now }
	ds.testLoadConfigFunc = func() (*dc.Config, error) {
		return &dc.Config{Sessions: dc.SessionsConfig{Enabled: true, RetentionHours: 24}}, nil
	}
	if err := ds.runSessionRetentionOnce(context.Background()); err != nil {
		t.Fatalf("run retention once: %v", err)
	}

	expired, err := actions.Status(context.Background(), "expired")
	if err != nil || expired.State != sessiondata.SessionActionExpired || expired.ResultCode == nil || *expired.ResultCode != "expired" {
		t.Fatalf("expired action = %+v, %v", expired, err)
	}
	if err := ds.PurgeSessionSnapshotsForPrivacy(context.Background()); err != nil {
		t.Fatalf("privacy purge: %v", err)
	}
	privacy, err := actions.Status(context.Background(), "privacy")
	if err != nil || privacy.State != sessiondata.SessionActionExpired || privacy.ResultCode == nil || *privacy.ResultCode != "privacy_policy_changed" {
		t.Fatalf("privacy-cancelled action = %+v, %v", privacy, err)
	}
}

func TestSessionRetention_StopsWhenContextCancelled(t *testing.T) {
	db, err := telemetry.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ds := newTestServer(t)
	ds.sessionActions = telemetry.NewSessionActionStore(db)
	ds.testLoadConfigFunc = func() (*dc.Config, error) {
		return &dc.Config{Sessions: dc.SessionsConfig{Enabled: true, RetentionHours: 24}}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		ds.runSessionRetention(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("session retention did not stop after context cancellation")
	}
}
