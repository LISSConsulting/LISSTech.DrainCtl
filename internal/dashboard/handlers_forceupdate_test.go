//go:build windows

package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/etwids"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// newForceUpdateTestServer extends newTestServer with a registered
// host + the auth + last-seen state needed by handleForceUpdate. The
// AuthInfo is wired via ctx because the test environment does not run
// the full session middleware.
func newForceUpdateTestServer(t *testing.T, host string, version string, lastSeen time.Time, registered bool) *DashboardServer {
	t.Helper()
	ds := newTestServer(t)
	if registered {
		ds.state.Register(host)
	}
	// Populate the cached ServerInfo so handleForceUpdate's GetCached
	// short-circuits and handleForceUpdate reads the version we set.
	ds.state.Update(host, &dc.CheckResult{
		Version:   version,
		Timestamp: lastSeen,
		Host:      host,
		Status:    "Healthy",
	})
	// Force the offline check to pass by ensuring last_seen is within
	// the stale window. newTestServer uses the default heartbeat
	// interval; tests that need offline semantics override below.
	if registered && !lastSeen.IsZero() {
		// Re-Update with a last_seen at the desired instant. The
		// server timestamps last_seen on every Update, so the only
		// way to backdate is via the DB layer — for unit tests we
		// instead override staleAfter to a small value when we need
		// an offline scenario.
		ds.setHeartbeatInterval(1 * time.Hour)
	}
	return ds
}

// doForceUpdate issues POST /api/v1/servers/{host}/force-update with
// the supplied body. Returns the recorded response.
func doForceUpdate(t *testing.T, ds *DashboardServer, host string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/servers/"+host+"/force-update", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	// Direct handler invocation bypasses the mux, so r.PathValue
	// returns empty. Populate the wildcard manually so the handler
	// sees the host.
	r.SetPathValue("host", host)
	// Force an auth identity so isAuthorizedForHost passes. The
	// production route uses requireSession middleware; tests call
	// the handler directly so we set the auth context manually.
	ctx := context.WithValue(r.Context(), authInfoKey, &AuthInfo{Username: "operator", Groups: []string{"Domain Admins"}})
	r = r.WithContext(ctx)
	ds.handleForceUpdate(w, r)
	return w
}

// TestHandleForceUpdate_AcceptedHappyPath covers the canonical
// "operator hits the button on a healthy, current agent" case: 200,
// outcome=accepted, accepted_at populated, version echoed.
func TestHandleForceUpdate_AcceptedHappyPath(t *testing.T) {
	ds := newForceUpdateTestServer(t, "host-a", "v26.9.24", time.Now(), true)
	body, _ := json.Marshal(map[string]string{"command_id": "cmd-00001-001"})
	w := doForceUpdate(t, ds, "host-a", body)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp forceUpdateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Outcome != forceUpdateOutcomeAccepted {
		t.Errorf("outcome = %q, want accepted", resp.Outcome)
	}
	if resp.Host != "host-a" {
		t.Errorf("host = %q, want host-a", resp.Host)
	}
	if resp.CommandID != "cmd-00001-001" {
		t.Errorf("command_id = %q, want cmd-1", resp.CommandID)
	}
	if resp.Version != "v26.9.24" {
		t.Errorf("version = %q, want v26.9.24", resp.Version)
	}
	if resp.AcceptedAt == "" {
		t.Errorf("accepted_at empty; expected RFC3339")
	}
}

func TestHandleForceUpdate_DashboardOnlyLocalHostIsUnsupported(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("Hostname: %v", err)
	}
	ds := newForceUpdateTestServer(t, host, "v26.9.24", time.Now(), true)
	ds.setLocalForceUpdateSupported(false)
	body, _ := json.Marshal(map[string]string{"command_id": "cmd-00001-001"})
	w := doForceUpdate(t, ds, host, body)

	var resp forceUpdateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Outcome != forceUpdateOutcomeUnsupported {
		t.Errorf("outcome = %q, want unsupported", resp.Outcome)
	}
	if got := ds.forceUpdates.Pending(strings.ToLower(host)); len(got) != 0 {
		t.Errorf("Pending length = %d, want 0", len(got))
	}
}

// TestHandleForceUpdate_DuplicateReplayWithinWindow covers the
// idempotency acceptance criterion: a second request with the same
// command_id within 24h returns outcome=duplicate without enqueuing a
// second execution.
func TestHandleForceUpdate_DuplicateReplayWithinWindow(t *testing.T) {
	ds := newForceUpdateTestServer(t, "host-a", "v26.9.24", time.Now(), true)
	body, _ := json.Marshal(map[string]string{"command_id": "cmd-00001-001"})

	w1 := doForceUpdate(t, ds, "host-a", body)
	var r1 forceUpdateResponse
	_ = json.Unmarshal(w1.Body.Bytes(), &r1)
	if r1.Outcome != forceUpdateOutcomeAccepted {
		t.Fatalf("first outcome = %q, want accepted", r1.Outcome)
	}

	w2 := doForceUpdate(t, ds, "host-a", body)
	var r2 forceUpdateResponse
	_ = json.Unmarshal(w2.Body.Bytes(), &r2)
	if r2.Outcome != forceUpdateOutcomeDuplicate {
		t.Errorf("second outcome = %q, want duplicate", r2.Outcome)
	}

	// Crucially, only ONE pending command should exist.
	pending := ds.forceUpdates.Pending("host-a")
	if len(pending) != 1 {
		t.Fatalf("Pending length = %d, want 1 (duplicate must not enqueue a second)", len(pending))
	}
}

// TestHandleForceUpdate_OfflineHostReturnsOffline is the negative
// acceptance criterion: offline agents must NOT report accepted.
// The handler returns 200 + outcome=offline so the dashboard UI can
// show "host offline" uniformly without status-code gymnastics.
func TestHandleForceUpdate_OfflineHostReturnsOffline(t *testing.T) {
	ds := newForceUpdateTestServer(t, "host-a", "v26.9.24", time.Now().Add(-1*time.Hour), true)
	// Override the heartbeat to a tiny window so the host is "offline".
	ds.setHeartbeatInterval(1 * time.Millisecond)
	// Sleep so the synthetic lastSeen is older than the new window.
	time.Sleep(5 * time.Millisecond)

	body, _ := json.Marshal(map[string]string{"command_id": "cmd-00001-001"})
	w := doForceUpdate(t, ds, "host-a", body)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (offline returns 200 + outcome)", w.Code)
	}
	var resp forceUpdateResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Outcome != forceUpdateOutcomeOffline {
		t.Errorf("outcome = %q, want offline", resp.Outcome)
	}
	if got := ds.forceUpdates.Pending("host-a"); len(got) != 0 {
		t.Errorf("Pending length = %d, want 0 (offline must not enqueue)", len(got))
	}
}

// TestHandleForceUpdate_UnsupportedAgentVersion covers the second
// negative acceptance criterion: agents below the floor must NOT
// receive accepted. The handler returns 200 + outcome=unsupported.
func TestHandleForceUpdate_UnsupportedAgentVersion(t *testing.T) {
	ds := newForceUpdateTestServer(t, "host-a", "v26.9.16", time.Now(), true)
	body, _ := json.Marshal(map[string]string{"command_id": "cmd-00001-001"})
	w := doForceUpdate(t, ds, "host-a", body)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp forceUpdateResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Outcome != forceUpdateOutcomeUnsupported {
		t.Errorf("outcome = %q, want unsupported", resp.Outcome)
	}
	if got := ds.forceUpdates.Pending("host-a"); len(got) != 0 {
		t.Errorf("Pending length = %d, want 0 (unsupported must not enqueue)", len(got))
	}
}

func TestHandleForceUpdate_FirstSupportedReleaseAccepted(t *testing.T) {
	ds := newForceUpdateTestServer(t, "host-a", "v26.9.17", time.Now(), true)
	body, _ := json.Marshal(map[string]string{"command_id": "cmd-00001-001"})
	w := doForceUpdate(t, ds, "host-a", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp forceUpdateResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Outcome != forceUpdateOutcomeAccepted {
		t.Errorf("outcome = %q, want accepted", resp.Outcome)
	}
}

func TestHandleForceUpdate_UnknownAgentVersionIsUnsupported(t *testing.T) {
	ds := newForceUpdateTestServer(t, "host-a", "", time.Now(), true)
	body, _ := json.Marshal(map[string]string{"command_id": "cmd-00001-001"})
	w := doForceUpdate(t, ds, "host-a", body)

	var resp forceUpdateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Outcome != forceUpdateOutcomeUnsupported {
		t.Errorf("outcome = %q, want unsupported", resp.Outcome)
	}
	if got := ds.forceUpdates.Pending("host-a"); len(got) != 0 {
		t.Errorf("Pending length = %d, want 0", len(got))
	}
}

// TestHandleForceUpdate_UnregisteredHostReturns404 covers the
// "host not registered" case: 404 + standard error body.
func TestHandleForceUpdate_UnregisteredHostReturns404(t *testing.T) {
	ds := newForceUpdateTestServer(t, "host-a", "v26.9.24", time.Now(), false)
	body, _ := json.Marshal(map[string]string{"command_id": "cmd-00001-001"})
	w := doForceUpdate(t, ds, "host-a", body)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestHandleForceUpdate_InvalidCommandIDReturns422 covers malformed
// command_id (too short, contains illegal chars).
func TestHandleForceUpdate_InvalidCommandIDReturns422(t *testing.T) {
	ds := newForceUpdateTestServer(t, "host-a", "v26.9.24", time.Now(), true)
	body, _ := json.Marshal(map[string]string{"command_id": "x"}) // too short
	w := doForceUpdate(t, ds, "host-a", body)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", w.Code)
	}
}

// TestHandleForceUpdate_OversizedReasonReturns422 covers the 256-char
// reason cap. The BatchOperations contract documents this cap and
// relies on it for log-line safety.
func TestHandleForceUpdate_OversizedReasonReturns422(t *testing.T) {
	ds := newForceUpdateTestServer(t, "host-a", "v26.9.24", time.Now(), true)
	huge := make([]byte, forceUpdateMaxReasonLen+1)
	for i := range huge {
		huge[i] = 'a'
	}
	body, _ := json.Marshal(map[string]string{"command_id": "cmd-00001-001", "reason": string(huge)})
	w := doForceUpdate(t, ds, "host-a", body)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 for oversized reason", w.Code)
	}
}

func TestHandleForceUpdate_MissingCommandIDReturns422(t *testing.T) {
	ds := newForceUpdateTestServer(t, "host-a", "v26.9.24", time.Now(), true)
	w := doForceUpdate(t, ds, "host-a", []byte(`{}`))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", w.Code)
	}
}

func TestHandleReport_DeliversForceUpdateCommandOnce(t *testing.T) {
	ds := newForceUpdateTestServer(t, "host-a", "v26.9.24", time.Now(), true)
	body, _ := json.Marshal(map[string]string{"command_id": "cmd-00001-001"})
	if w := doForceUpdate(t, ds, "host-a", body); w.Code != http.StatusOK {
		t.Fatalf("enqueue status = %d, want 200", w.Code)
	}

	reportBody, _ := json.Marshal(map[string]any{
		"host":      "host-a",
		"version":   "v26.9.24",
		"status":    "Healthy",
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
	})
	deliver := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/report", bytes.NewReader(reportBody))
		ds.handleReport(w, r)
		return w
	}

	var first struct {
		PendingCommand *ForceUpdatePendingCommand `json:"pending_command"`
	}
	if err := json.Unmarshal(deliver().Body.Bytes(), &first); err != nil {
		t.Fatalf("decode first report response: %v", err)
	}
	if first.PendingCommand == nil || first.PendingCommand.CommandID != "cmd-00001-001" {
		t.Fatalf("first pending command = %+v, want cmd-00001-001", first.PendingCommand)
	}

	var second struct {
		PendingCommand *ForceUpdatePendingCommand `json:"pending_command"`
	}
	if err := json.Unmarshal(deliver().Body.Bytes(), &second); err != nil {
		t.Fatalf("decode second report response: %v", err)
	}
	if second.PendingCommand == nil || second.PendingCommand.CommandID != "cmd-00001-001" {
		t.Fatalf("second pending command = %+v, want retry", second.PendingCommand)
	}
	ds.forceUpdates.Acknowledge("host-a", "cmd-00001-001")
	var acknowledged struct {
		PendingCommand *ForceUpdatePendingCommand `json:"pending_command"`
	}
	if err := json.Unmarshal(deliver().Body.Bytes(), &acknowledged); err != nil {
		t.Fatalf("decode acknowledged report response: %v", err)
	}
	if acknowledged.PendingCommand != nil {
		t.Fatalf("acknowledged pending command = %+v, want nil", acknowledged.PendingCommand)
	}
}

// TestHandleForceUpdate_CompletionBroadcastsSSE covers the
// completion path: a /api/v1/report body containing a force_update
// block triggers an SSE broadcast and drops the pending command.
// Asserted by counting subscribers + introspecting the broker's
// last-payload buffer.
func TestHandleForceUpdate_CompletionBroadcastsSSE(t *testing.T) {
	ds := newForceUpdateTestServer(t, "host-a", "v26.9.24", time.Now(), true)

	// Subscribe so we can verify the broadcast.
	subID, sub, _, err := ds.broker.Subscribe()
	if err != nil {
		t.Fatalf("broker.Subscribe: %v", err)
	}
	defer ds.broker.Unsubscribe(subID)

	// Enqueue a command.
	body, _ := json.Marshal(map[string]string{"command_id": "cmd-00001-001"})
	w := doForceUpdate(t, ds, "host-a", body)
	if w.Code != http.StatusOK {
		t.Fatalf("enqueue status = %d, want 200", w.Code)
	}

	// Send a /api/v1/report with a force_update completion.
	reportBody, _ := json.Marshal(map[string]any{
		"host":      "host-a",
		"version":   "v26.9.24",
		"status":    "Healthy",
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"force_update": map[string]any{
			"command_id":  "cmd-00001-001",
			"outcome":     "completed",
			"reason":      "up_to_date",
			"old_version": "v26.9.17",
			"new_version": "v26.9.17",
		},
	})
	w = httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/report", bytes.NewReader(reportBody))
	r.Header.Set("Content-Type", "application/json")
	ds.handleReport(w, r)

	// Subscriber should have received the force_update SSE event.
	select {
	case payload := <-sub:
		var evt SSEEvent
		if err := json.Unmarshal(payload, &evt); err != nil {
			t.Fatalf("decode SSE: %v", err)
		}
		if evt.Type != "force_update" {
			t.Errorf("event type = %q, want force_update", evt.Type)
		}
		if evt.Host != "HOST-A" {
			t.Errorf("event host = %q, want HOST-A", evt.Host)
		}
	case <-time.After(time.Second):
		t.Fatal("did not receive SSE force_update event within 1s")
	}

	// Pending command should be dropped after completion.
	pending := ds.forceUpdates.Pending("host-a")
	if len(pending) != 0 {
		t.Errorf("Pending length = %d, want 0 (completion drops)", len(pending))
	}
}

// TestHandleForceUpdate_AuditEventIDEmitted is a smoke test that the
// ETW event id is wired (the slog side-effect is hard to assert; we
// confirm the call path doesn't panic and the response is OK).
func TestHandleForceUpdate_AuditEventIDEmitted(t *testing.T) {
	ds := newForceUpdateTestServer(t, "host-a", "v26.9.24", time.Now(), true)
	body, _ := json.Marshal(map[string]string{"command_id": "cmd-00001-001"})
	w := doForceUpdate(t, ds, "host-a", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// The event id constant must remain valid (compile-time guard).
	_ = etwids.EvtForceUpdateAccepted
	_ = etwids.EvtForceUpdateCompleted
}

// TestNewForceUpdateState_EmptyByDefault is the trivial constructor
// test: a fresh registry has no entries for any host.
func TestNewForceUpdateState_EmptyByDefault(t *testing.T) {
	s := newForceUpdateState()
	for _, h := range []string{"a", "b", "c"} {
		if p := s.Pending(h); len(p) != 0 {
			t.Errorf("Pending(%q) on fresh state = %d, want 0", h, len(p))
		}
		if c := s.Consume(h); c != nil {
			t.Errorf("Consume(%q) on fresh state = %+v, want nil", h, c)
		}
	}
}

// ensure test imports aren't dead-code
var (
	_ = telemetry.AuditRecord{}
)
