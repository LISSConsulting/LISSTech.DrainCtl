//go:build windows

package dashboard

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
)

func TestSessionSnapshotSSE_MetadataOnlyAfterNewerCommit(t *testing.T) {
	cfg := dc.SessionsConfig{Enabled: true, AllowActions: true, IdentityVisibility: dc.SessionVisibilityFull, ClientVisibility: dc.SessionVisibilityFull, ProcessVisibility: dc.SessionVisibilityFull}
	ds, _ := newSessionHandlerTestServer(t, cfg)
	host := "sse.example.test"
	ds.state.Register(host)
	id, events, _, err := ds.broker.Subscribe()
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer ds.broker.Unsubscribe(id)

	if result, err := ds.ingestSessionSnapshot(context.Background(), testSessionSnapshot(host, 1)); err != nil || !result.Accepted {
		t.Fatalf("ingest result=%+v err=%v", result, err)
	}
	var event SSEEvent
	select {
	case raw := <-events:
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatalf("decode SSE event: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("accepted snapshot did not emit SSE")
	}
	if event.Type != "session_snapshot" || event.Host != host {
		t.Fatalf("event = %+v", event)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(event.Data, &data); err != nil {
		t.Fatalf("decode event data: %v", err)
	}
	for _, key := range []string{"host", "latest_attempt_instance_id", "latest_attempt_sequence", "latest_attempt_observed_at_ms", "latest_attempt_received_at_ms", "last_success_instance_id", "last_success_sequence", "last_success_observed_at_ms", "last_success_received_at_ms", "freshness", "session_count", "active_count", "capabilities", "actions_available", "collection_status", "last_error"} {
		if _, ok := data[key]; !ok {
			t.Errorf("session_snapshot data missing %q: %s", key, event.Data)
		}
	}
	for _, forbidden := range []string{"user", "domain", "station", "client_name", "client_address", "processes", "session_id", "logon_at_ms", "message", "action_id"} {
		if _, ok := data[forbidden]; ok {
			t.Errorf("session_snapshot data exposed %q: %s", forbidden, event.Data)
		}
	}
	var sequence string
	if err := json.Unmarshal(data["latest_attempt_sequence"], &sequence); err != nil || sequence != "1" {
		t.Fatalf("latest_attempt_sequence = %q err=%v, want decimal string 1", sequence, err)
	}
	if string(data["last_error"]) != "null" {
		t.Fatalf("last_error = %s, want null", data["last_error"])
	}

	stale := testSessionSnapshot(host, 1)
	if result, err := ds.ingestSessionSnapshot(context.Background(), stale); err != nil || result.Accepted {
		t.Fatalf("stale ingest result=%+v err=%v", result, err)
	}
	select {
	case raw := <-events:
		t.Fatalf("stale snapshot emitted SSE: %s", raw)
	case <-time.After(100 * time.Millisecond):
	}

	fatal := testSessionSnapshot(host, 2)
	fatal.CollectionError = &sessiondata.CollectionError{Code: sessiondata.CollectionErrorWTSEnumerationFailed}
	fatal.Sessions = []sessiondata.SessionRecord{}
	fatal.Capabilities = sessiondata.SessionCapabilities{}
	if result, err := ds.ingestSessionSnapshot(context.Background(), fatal); err != nil || !result.Accepted || !result.Fatal {
		t.Fatalf("fatal ingest result=%+v err=%v", result, err)
	}
	select {
	case raw := <-events:
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatalf("decode fatal SSE event: %v", err)
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatalf("decode fatal event data: %v", err)
		}
		if string(data["collection_status"]) != `"error"` || string(data["session_count"]) != "1" {
			t.Fatalf("fatal event did not retain last-success summary: %s", event.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("fatal snapshot did not emit SSE")
	}
}
