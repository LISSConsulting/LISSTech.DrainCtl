//go:build windows

package dashboard

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/investigation"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondrop"
)

// drainBroker reads every message currently sitting in the subscriber channel
// and returns their decoded SSEEvent envelopes. Gives callers a short grace
// window so broadcasts dispatched just before the read still land.
func drainBroker(t *testing.T, ch <-chan []byte) []SSEEvent {
	t.Helper()
	var out []SSEEvent
	deadline := time.After(100 * time.Millisecond)
	for {
		select {
		case msg := <-ch:
			var ev SSEEvent
			if err := json.Unmarshal(msg, &ev); err != nil {
				t.Fatalf("decode sse event: %v (raw=%s)", err, msg)
			}
			out = append(out, ev)
		case <-deadline:
			return out
		}
	}
}

// TestBroker_PublishDetectorStatus_OnTransition drives the detector through
// the disabled → training → healthy transition sequence required by the US1
// Independent Test. Each real transition must yield exactly one SSE event;
// same-state republishes must be suppressed so the stream is not spammed on
// every evaluation tick.
func TestBroker_PublishDetectorStatus_OnTransition(t *testing.T) {
	b := NewBroker()
	id, ch, _, err := b.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Unsubscribe(id)

	mk := func(state string) evtspike.DetectorStatus {
		return evtspike.DetectorStatus{Host: "RDSH-04", State: state, EnabledChannels: 54}
	}

	if !b.PublishDetectorStatus(mk(evtspike.StateDisabled)) {
		t.Fatal("first publish (disabled) should emit")
	}
	if b.PublishDetectorStatus(mk(evtspike.StateDisabled)) {
		t.Fatal("republish of same state (disabled) should be suppressed")
	}
	if !b.PublishDetectorStatus(mk(evtspike.StateTraining)) {
		t.Fatal("transition disabled → training should emit")
	}
	if b.PublishDetectorStatus(mk(evtspike.StateTraining)) {
		t.Fatal("republish of same state (training) should be suppressed")
	}
	if !b.PublishDetectorStatus(mk(evtspike.StateHealthy)) {
		t.Fatal("transition training → healthy should emit")
	}
	if b.PublishDetectorStatus(mk(evtspike.StateHealthy)) {
		t.Fatal("republish of same state (healthy) should be suppressed")
	}

	events := drainBroker(t, ch)
	if len(events) != 3 {
		t.Fatalf("got %d detector_status events, want 3 (one per transition)", len(events))
	}
	wantStates := []string{evtspike.StateDisabled, evtspike.StateTraining, evtspike.StateHealthy}
	for i, ev := range events {
		if ev.Type != "detector_status" {
			t.Errorf("event[%d].Type = %q, want %q", i, ev.Type, "detector_status")
		}
		if ev.Host != "RDSH-04" {
			t.Errorf("event[%d].Host = %q, want RDSH-04", i, ev.Host)
		}
		var status evtspike.DetectorStatus
		if err := json.Unmarshal(ev.Data, &status); err != nil {
			t.Fatalf("decode event[%d] data: %v", i, err)
		}
		if status.State != wantStates[i] {
			t.Errorf("event[%d].data.state = %q, want %q", i, status.State, wantStates[i])
		}
	}
}

// TestBroker_PublishDetectorStatus_PerHostIsolation ensures the dedup table
// is keyed per host — two servers reporting the same state must each emit.
func TestBroker_PublishDetectorStatus_PerHostIsolation(t *testing.T) {
	b := NewBroker()
	id, ch, _, err := b.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Unsubscribe(id)

	if !b.PublishDetectorStatus(evtspike.DetectorStatus{Host: "A", State: evtspike.StateTraining}) {
		t.Fatal("host A first publish should emit")
	}
	if !b.PublishDetectorStatus(evtspike.DetectorStatus{Host: "B", State: evtspike.StateTraining}) {
		t.Fatal("host B first publish should emit (per-host dedup)")
	}
	if b.PublishDetectorStatus(evtspike.DetectorStatus{Host: "A", State: evtspike.StateTraining}) {
		t.Fatal("host A same-state republish should be suppressed")
	}

	events := drainBroker(t, ch)
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
}

func TestBroker_PublishDetectorStatus_EmitsReadinessChangesOnly(t *testing.T) {
	b := NewBroker()
	id, ch, _, err := b.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Unsubscribe(id)

	warmupStart := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	status := evtspike.DetectorStatus{
		Host:            "RDSH-04",
		State:           evtspike.StateTraining,
		EnabledChannels: 54,
		MatureChannels:  1,
		WarmupStartedAt: &warmupStart,
	}
	if !b.PublishDetectorStatus(status) {
		t.Fatal("first training snapshot should emit")
	}
	if b.PublishDetectorStatus(status) {
		t.Fatal("identical training snapshot should be suppressed")
	}

	status.MatureChannels = 2
	if !b.PublishDetectorStatus(status) {
		t.Fatal("maturity change during training should emit")
	}
	status.EnabledChannels = 53
	if !b.PublishDetectorStatus(status) {
		t.Fatal("subscribed-channel capacity change during training should emit")
	}
	warmupStart = warmupStart.Add(time.Hour)
	status.WarmupStartedAt = &warmupStart
	if !b.PublishDetectorStatus(status) {
		t.Fatal("warm-up restart should emit")
	}

	lastSpike := warmupStart.Add(time.Minute)
	status.LastSpikeAt = &lastSpike
	if b.PublishDetectorStatus(status) {
		t.Fatal("last spike alone should use recent_spike, not duplicate detector-status")
	}

	if events := drainBroker(t, ch); len(events) != 4 {
		t.Fatalf("got %d detector_status events, want 4 status-surface changes", len(events))
	}
}

// TestBroker_PublishRecentSpike_EmitsEveryCall verifies the recent_spike
// dispatcher has no dedup — each confirmed spike must reach subscribers even
// if two fire back-to-back on the same host and channel.
func TestBroker_PublishRecentSpike_EmitsEveryCall(t *testing.T) {
	b := NewBroker()
	id, ch, _, err := b.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Unsubscribe(id)

	for i := 0; i < 3; i++ {
		entry := evtspike.RecentSpikeEntry{
			ID: int64(i + 1),
			SpikePayload: evtspike.SpikePayload{
				Host:    "RDSH-04",
				Channel: "Application",
			},
		}
		b.PublishRecentSpike(entry)
	}

	events := drainBroker(t, ch)
	if len(events) != 3 {
		t.Fatalf("got %d recent_spike events, want 3", len(events))
	}
	for i, ev := range events {
		if ev.Type != "recent_spike" {
			t.Errorf("event[%d].Type = %q, want recent_spike", i, ev.Type)
		}
		var entry evtspike.RecentSpikeEntry
		if err := json.Unmarshal(ev.Data, &entry); err != nil {
			t.Fatalf("decode event[%d] data: %v", i, err)
		}
		if entry.ID != int64(i+1) {
			t.Errorf("event[%d].data.id = %d, want %d", i, entry.ID, i+1)
		}
	}
}

func TestBroker_PublishesHostFreeFeatureEventsInEstablishedEnvelope(t *testing.T) {
	b := NewBroker()
	id, ch, _, err := b.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Unsubscribe(id)

	if !b.PublishInvestigationUpdate(investigation.Update{
		Kind: "attempt",
		Attempt: &investigation.AttemptUpdate{
			AttemptID: "7",
			Source:    investigation.SourceLink{SourceKind: investigation.SourceKindSessionDrop, SourceID: "9"},
			State:     investigation.AttemptStateQueued,
		},
	}) {
		t.Fatal("investigation update was not published")
	}
	if !b.PublishSessionDrop(sessiondrop.SSEEvent{
		SchemaVersion: 1,
		SourceKind:    sessiondrop.SourceKind,
		SourceID:      "9",
	}) {
		t.Fatal("session-drop event was not published")
	}

	events := drainBroker(t, ch)
	if len(events) != 2 {
		t.Fatalf("got %d feature events, want 2", len(events))
	}
	for _, event := range events {
		if event.Host != "" {
			t.Fatalf("%s event exposed host %q", event.Type, event.Host)
		}
		if event.Type != "investigation_update" && event.Type != "session_drop" {
			t.Fatalf("unexpected feature event type %q", event.Type)
		}
	}
}
