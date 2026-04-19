//go:build windows

package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
)

// TestAPI_EvtSpikeStatus_Healthy verifies the provider-backed status path:
// given a provider that reports a fully-warm detector, the handler returns
// 200 with state="healthy" and the full DetectorStatus body per
// contracts/dashboard-sse-events.md.
func TestAPI_EvtSpikeStatus_Healthy(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	ds.evtspikeStatus = func(host string) evtspike.DetectorStatus {
		return evtspike.DetectorStatus{
			State:           evtspike.StateHealthy,
			EnabledChannels: 54,
			MatureChannels:  41,
		}
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/evtspike/status?host=SRV01", nil)
	ds.handleEvtSpikeStatus(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var got evtspike.DetectorStatus
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Host != "SRV01" {
		t.Errorf("host = %q, want SRV01", got.Host)
	}
	if got.State != evtspike.StateHealthy {
		t.Errorf("state = %q, want healthy", got.State)
	}
	if got.EnabledChannels != 54 {
		t.Errorf("enabled_channels = %d, want 54", got.EnabledChannels)
	}
	if got.MatureChannels != 41 {
		t.Errorf("mature_channels = %d, want 41", got.MatureChannels)
	}
	if got.ErrorReason != "" {
		t.Errorf("error_reason = %q, want empty (omitted unless state=error)", got.ErrorReason)
	}
}

// TestAPI_EvtSpikeStatus_Training verifies the half-mature gate in the
// state-derivation table: enabled with zero mature channels is "training".
func TestAPI_EvtSpikeStatus_Training(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	ds.evtspikeStatus = func(host string) evtspike.DetectorStatus {
		return evtspike.DetectorStatus{
			State:           evtspike.StateTraining,
			EnabledChannels: 54,
			MatureChannels:  0,
		}
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/evtspike/status?host=SRV01", nil)
	ds.handleEvtSpikeStatus(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got evtspike.DetectorStatus
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.State != evtspike.StateTraining {
		t.Errorf("state = %q, want training", got.State)
	}
	if got.MatureChannels != 0 {
		t.Errorf("mature_channels = %d, want 0", got.MatureChannels)
	}
}

// TestAPI_EvtSpikeStatus_Disabled verifies that a disabled feature returns
// 200 with state="disabled" — not 503 — because disabled is a configuration
// state, not a transient failure (contracts/dashboard-sse-events.md).
// The nil provider hook represents the "feature off" wiring.
func TestAPI_EvtSpikeStatus_Disabled(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	// evtspikeStatus left nil — feature not wired.

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/evtspike/status?host=SRV01", nil)
	ds.handleEvtSpikeStatus(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got evtspike.DetectorStatus
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Host != "SRV01" {
		t.Errorf("host = %q, want SRV01", got.Host)
	}
	if got.State != evtspike.StateDisabled {
		t.Errorf("state = %q, want disabled", got.State)
	}
	if got.EnabledChannels != 0 {
		t.Errorf("enabled_channels = %d, want 0", got.EnabledChannels)
	}
	if got.LastSpikeAt != nil {
		t.Errorf("last_spike_at = %v, want nil (never seen)", got.LastSpikeAt)
	}
}

// TestAPI_EvtSpikeStatus_UnknownHost_404 asserts that an unregistered host
// receives 404, consistent with the other host-scoped endpoints.
func TestAPI_EvtSpikeStatus_UnknownHost_404(t *testing.T) {
	ds := newTestServer(t)
	// SRV01 intentionally not registered.

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/evtspike/status?host=SRV01", nil)
	ds.handleEvtSpikeStatus(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "unknown_host" {
		t.Errorf("error = %q, want unknown_host", body["error"])
	}
}

// TestAPI_EvtSpikeStatus_MissingHost_404 covers the edge where the query param
// is absent entirely — same 404 as an unregistered host.
func TestAPI_EvtSpikeStatus_MissingHost_404(t *testing.T) {
	ds := newTestServer(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/evtspike/status", nil)
	ds.handleEvtSpikeStatus(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// TestAPI_EvtSpikeStatus_NoSession_401 exercises the rs middleware chain used
// in production wiring. A request with no drainctl_session cookie must be
// rejected with 401 before reaching the handler.
func TestAPI_EvtSpikeStatus_NoSession_401(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	storeCtx, storeCancel := context.WithCancel(context.Background())
	t.Cleanup(storeCancel)
	ds.sessionStore = NewSessionStore(storeCtx)

	handler := requireSession(ds.sessionStore)(http.HandlerFunc(ds.handleEvtSpikeStatus))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/evtspike/status?host=SRV01", nil)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}
