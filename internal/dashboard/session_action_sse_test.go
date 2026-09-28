//go:build windows

package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
)

func TestSessionActionSSE_EmitsOnlySafeStatusMetadata(t *testing.T) {
	ds := newTestServer(t)
	id, events, _, err := ds.broker.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer ds.broker.Unsubscribe(id)

	result := "completed"
	status := sessiondata.SessionActionStatus{
		ActionID: "0195a584-5b25-7a00-91a5-7cbb4dac92d9", Type: sessiondata.SessionActionMessage,
		CanonicalHost: "sse-action.example.test", SessionID: 7, ExpectedLogonAtMS: 123,
		State: sessiondata.SessionActionCompleted, CreatedAtMS: 100, ExpiresAtMS: 300100, ResultCode: &result,
	}
	ds.broadcastSessionAction(status)
	select {
	case raw := <-events:
		var event SSEEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type != "session_action" || event.Host != status.CanonicalHost {
			t.Fatalf("event=%+v", event)
		}
		var data map[string]json.RawMessage
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatal(err)
		}
		if len(data) != 4 {
			t.Fatalf("session action SSE fields = %s, want exactly four", event.Data)
		}
		for _, key := range []string{"action_id", "state", "completed_at_ms", "result_code"} {
			if _, ok := data[key]; !ok {
				t.Errorf("missing status key %q in %s", key, event.Data)
			}
		}
		for _, forbidden := range []string{"type", "host", "session_id", "expected_logon_at_ms", "created_at_ms", "expires_at_ms", "message", "message_ciphertext", "message_protection", "requested_by", "idempotency_key", "request_fingerprint", "user", "domain"} {
			if _, ok := data[forbidden]; ok {
				t.Errorf("SSE leaked %q in %s", forbidden, event.Data)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("session action broadcast was not delivered")
	}
}

func TestCancelSessionActionsForPolicyBroadcastsOneSafeTransition(t *testing.T) {
	cfg := dc.SessionsConfig{Enabled: true, AllowActions: true, IdentityVisibility: dc.SessionVisibilityFull, ClientVisibility: dc.SessionVisibilityFull, ProcessVisibility: dc.SessionVisibilityFull}
	ds, _, token := newSessionActionTestServer(t, cfg)
	host := "policy-sse.example.test"
	logon := seedActionTarget(t, ds, host)
	id, events, _, err := ds.broker.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer ds.broker.Unsubscribe(id)

	w := httptest.NewRecorder()
	ds.handleSessionAction(w, sessionActionRequest(t, host, "7", token, "policy-sse", `{"type":"logoff","expected_logon_at_ms":`+strconv.FormatInt(logon, 10)+`}`))
	if w.Code != http.StatusAccepted {
		t.Fatalf("enqueue status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Action sessiondata.SessionActionStatus `json:"action"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	created := response.Action
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("missing queued action event")
	}
	if err := ds.CancelSessionActionsForPolicy(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertTransitionEvent(t, events, created.ActionID, "privacy_policy_changed")
	select {
	case raw := <-events:
		t.Fatalf("duplicate cancellation event=%s", raw)
	case <-time.After(50 * time.Millisecond):
	}
}
