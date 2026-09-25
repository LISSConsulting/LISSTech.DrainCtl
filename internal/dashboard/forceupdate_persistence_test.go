//go:build windows

package dashboard

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

func TestForceUpdateOutboxSurvivesDashboardRestartAndAcknowledgement(t *testing.T) {
	db, err := telemetry.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	outbox := telemetry.NewForceUpdateOutboxStore(db)

	first, err := newPersistentForceUpdateState(context.Background(), outbox)
	if err != nil {
		t.Fatalf("first dashboard state: %v", err)
	}
	outcome, accepted := first.Enqueue("host-a", "cmd-00001-001", "operator request", "26.9.24")
	if outcome != forceUpdateOutcomeAccepted || accepted == nil {
		t.Fatalf("Enqueue = (%q, %v), want accepted command", outcome, accepted)
	}

	// A new DashboardServer state over the same telemetry DB must redeliver the
	// oldest persisted command; no in-memory state from the first server is used.
	second, err := newPersistentForceUpdateState(context.Background(), outbox)
	if err != nil {
		t.Fatalf("restarted dashboard state: %v", err)
	}
	pending := second.Consume("host-a")
	if pending == nil || pending.CommandID != accepted.CommandID {
		t.Fatalf("restarted Consume = %#v, want %q", pending, accepted.CommandID)
	}
	if retry := second.Consume("host-a"); retry == nil || retry.CommandID != accepted.CommandID {
		t.Fatalf("retry Consume = %#v, want retained command", retry)
	}

	second.Acknowledge("host-a", accepted.CommandID)
	third, err := newPersistentForceUpdateState(context.Background(), outbox)
	if err != nil {
		t.Fatalf("acknowledged dashboard state: %v", err)
	}
	if got := third.Consume("host-a"); got != nil {
		t.Fatalf("Consume after acknowledgement = %#v, want nil", got)
	}
}

func TestLocalForceUpdateCompletionAcknowledgesOutboxAndBroadcastsCanonicalHost(t *testing.T) {
	db, err := telemetry.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	outbox := telemetry.NewForceUpdateOutboxStore(db)
	updates, err := newPersistentForceUpdateState(context.Background(), outbox)
	if err != nil {
		t.Fatalf("newPersistentForceUpdateState: %v", err)
	}
	host, commandID := "rdsh-01", "cmd-00001-001"
	if outcome, _ := updates.Enqueue(host, commandID, "operator request", "26.9.24"); outcome != forceUpdateOutcomeAccepted {
		t.Fatalf("Enqueue outcome = %q, want accepted", outcome)
	}

	ds := newTestServer(t)
	ds.forceUpdates = updates
	ds.wireServerStateCallbacks()
	_, events, _, err := ds.broker.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	ds.state.OnLocalForceUpdateCompletion(ForceUpdateCompletionPayload{
		Host: host, CommandID: commandID, Outcome: "completed",
	})

	reloaded, err := newPersistentForceUpdateState(context.Background(), outbox)
	if err != nil {
		t.Fatalf("reload force update state: %v", err)
	}
	if pending := reloaded.Consume(host); pending != nil {
		t.Fatalf("durable outbox retained local completion: %#v", pending)
	}

	select {
	case raw := <-events:
		var event SSEEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatalf("unmarshal SSE event: %v", err)
		}
		if event.Host != host {
			t.Errorf("SSE host = %q, want canonical host %q", event.Host, host)
		}
	case <-time.After(time.Second):
		t.Fatal("local completion did not broadcast SSE event")
	}
}

func TestForceUpdateOutboxExpiresCommands(t *testing.T) {
	db, err := telemetry.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	outbox := telemetry.NewForceUpdateOutboxStore(db)
	old := time.Now().Add(-2 * forceUpdateIdempotencyWindow)
	if err := outbox.Enqueue(context.Background(), telemetry.ForceUpdateOutboxEntry{
		CommandID: "cmd-00001-001", Host: "host-a", AcceptedAt: old, AgentVersion: "26.9.24",
	}, old.Add(-time.Second)); err != nil {
		t.Fatalf("Enqueue old command: %v", err)
	}
	state, err := newPersistentForceUpdateState(context.Background(), outbox)
	if err != nil {
		t.Fatalf("newPersistentForceUpdateState: %v", err)
	}
	if got := state.Consume("host-a"); got != nil {
		t.Fatalf("expired command restored as %#v", got)
	}
}
