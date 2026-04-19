//go:build windows

package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
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

// makeSpike returns a SpikePayload whose WindowEnd is t, for seeding the store
// with recognisable entries. The other fields are irrelevant to these contract
// tests — only ordering, host-scoping, and limit-clamping are verified here.
func makeSpike(host, channel string, end time.Time) dc.SpikePayload {
	return dc.SpikePayload{
		Host:              host,
		Channel:           channel,
		WindowStart:       end.Add(-10 * time.Second),
		WindowEnd:         end,
		Observed:          42,
		Expected:          3.5,
		TailProbability:   1e-6,
		ConfirmationCount: 3,
		FirstSeenAt:       end.Add(-30 * time.Second),
	}
}

// TestAPI_EvtSpikeSpikes_EmptyRegisteredHost verifies that a registered host
// with no spikes returns 200 [] — NOT 404. Per
// contracts/dashboard-sse-events.md: "Empty array if no spikes recorded for
// this host. Not a 404."
func TestAPI_EvtSpikeSpikes_EmptyRegisteredHost(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	ds.spikestore = NewSpikeStore()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/evtspike/spikes?host=SRV01", nil)
	ds.handleEvtSpikeSpikes(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got []evtspike.RecentSpikeEntry
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

// TestAPI_EvtSpikeSpikes_RingBufferOrdering verifies newest-first ordering
// and per-host ring-buffer drop-oldest behaviour. Inserts more spikes than the
// store's per-host capacity and asserts the returned list is (a) bounded by
// capacity, (b) ordered newest-first by assigned ID.
func TestAPI_EvtSpikeSpikes_RingBufferOrdering(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	ds.spikestore = NewSpikeStore()

	// Insert 25 > capacity(20) so the oldest 5 are evicted.
	base := time.Date(2026, 4, 16, 14, 0, 0, 0, time.UTC)
	for i := 0; i < 25; i++ {
		ds.spikestore.Append("SRV01", makeSpike("SRV01", "Application", base.Add(time.Duration(i)*time.Second)))
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/evtspike/spikes?host=SRV01&limit=20", nil)
	ds.handleEvtSpikeSpikes(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got []evtspike.RecentSpikeEntry
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != spikeStoreHostCapacity {
		t.Fatalf("len = %d, want %d (capacity)", len(got), spikeStoreHostCapacity)
	}
	// Newest-first: IDs strictly decreasing.
	for i := 1; i < len(got); i++ {
		if got[i-1].ID <= got[i].ID {
			t.Fatalf("not newest-first at index %d: id[%d]=%d id[%d]=%d", i, i-1, got[i-1].ID, i, got[i].ID)
		}
	}
	// Newest-first: WindowEnd strictly decreasing (matches insertion order).
	for i := 1; i < len(got); i++ {
		if !got[i-1].WindowEnd.After(got[i].WindowEnd) {
			t.Fatalf("window_end not strictly decreasing at index %d: %s vs %s",
				i, got[i-1].WindowEnd, got[i].WindowEnd)
		}
	}
	// First five (IDs 1..5) were evicted; smallest surviving ID is 6.
	if smallest := got[len(got)-1].ID; smallest != 6 {
		t.Errorf("oldest surviving ID = %d, want 6 (first 5 evicted)", smallest)
	}
}

// TestAPI_EvtSpikeSpikes_LimitClamp verifies the handler clamps ?limit=500 to
// the contract maximum of 50 per contracts/dashboard-sse-events.md (limit ∈
// [1..50]). With a capacity-20 ring buffer the clamp is exercised by the
// clamp-to-size logic but the handler MUST still accept limit=500 without
// error and return up to the clamped maximum.
func TestAPI_EvtSpikeSpikes_LimitClamp(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	ds.spikestore = NewSpikeStore()

	base := time.Date(2026, 4, 16, 14, 0, 0, 0, time.UTC)
	for i := 0; i < 25; i++ {
		ds.spikestore.Append("SRV01", makeSpike("SRV01", "Application", base.Add(time.Duration(i)*time.Second)))
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/evtspike/spikes?host=SRV01&limit=500", nil)
	ds.handleEvtSpikeSpikes(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got []evtspike.RecentSpikeEntry
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Clamped to 50, but capped again by actual ring size (20).
	if len(got) > spikeStoreMaxLimit {
		t.Errorf("len = %d, exceeds clamp %d", len(got), spikeStoreMaxLimit)
	}
	if len(got) != spikeStoreHostCapacity {
		t.Errorf("len = %d, want %d (ring capacity)", len(got), spikeStoreHostCapacity)
	}
}

// TestAPI_EvtSpikeSpikes_DefaultLimit verifies that omitting ?limit= uses the
// default of 20 entries.
func TestAPI_EvtSpikeSpikes_DefaultLimit(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	ds.spikestore = NewSpikeStore()

	base := time.Date(2026, 4, 16, 14, 0, 0, 0, time.UTC)
	for i := 0; i < 25; i++ {
		ds.spikestore.Append("SRV01", makeSpike("SRV01", "Application", base.Add(time.Duration(i)*time.Second)))
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/evtspike/spikes?host=SRV01", nil)
	ds.handleEvtSpikeSpikes(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got []evtspike.RecentSpikeEntry
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != spikeStoreDefaultLimit {
		t.Errorf("len = %d, want %d (default limit)", len(got), spikeStoreDefaultLimit)
	}
}

// TestAPI_EvtSpikeSpikes_SmallLimit verifies that a small ?limit=5 returns
// exactly 5 entries, newest first.
func TestAPI_EvtSpikeSpikes_SmallLimit(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	ds.spikestore = NewSpikeStore()

	base := time.Date(2026, 4, 16, 14, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		ds.spikestore.Append("SRV01", makeSpike("SRV01", "Application", base.Add(time.Duration(i)*time.Second)))
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/evtspike/spikes?host=SRV01&limit=5", nil)
	ds.handleEvtSpikeSpikes(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got []evtspike.RecentSpikeEntry
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("len = %d, want 5", len(got))
	}
	// IDs 6..10 in decreasing order.
	for i, want := range []int64{10, 9, 8, 7, 6} {
		if got[i].ID != want {
			t.Errorf("got[%d].ID = %d, want %d", i, got[i].ID, want)
		}
	}
}

// TestAPI_EvtSpikeSpikes_PerHostIsolation verifies that spikes recorded for
// one host are NOT returned when querying another host.
func TestAPI_EvtSpikeSpikes_PerHostIsolation(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	ds.state.Register("SRV02")
	ds.spikestore = NewSpikeStore()

	base := time.Date(2026, 4, 16, 14, 0, 0, 0, time.UTC)
	ds.spikestore.Append("SRV01", makeSpike("SRV01", "Application", base))
	ds.spikestore.Append("SRV01", makeSpike("SRV01", "Application", base.Add(time.Second)))
	ds.spikestore.Append("SRV02", makeSpike("SRV02", "System", base.Add(2*time.Second)))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/evtspike/spikes?host=SRV02", nil)
	ds.handleEvtSpikeSpikes(w, r)

	var got []evtspike.RecentSpikeEntry
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (only SRV02 spike)", len(got))
	}
	if got[0].Host != "SRV02" {
		t.Errorf("host = %q, want SRV02", got[0].Host)
	}
}

// TestAPI_EvtSpikeSpikes_UnknownHost_404 asserts 404 for unregistered hosts,
// consistent with the status endpoint and other host-scoped routes.
func TestAPI_EvtSpikeSpikes_UnknownHost_404(t *testing.T) {
	ds := newTestServer(t)
	ds.spikestore = NewSpikeStore()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/evtspike/spikes?host=SRV01", nil)
	ds.handleEvtSpikeSpikes(w, r)

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

// TestAPI_EvtSpikeSpikes_MissingHost_404 covers absent host param.
func TestAPI_EvtSpikeSpikes_MissingHost_404(t *testing.T) {
	ds := newTestServer(t)
	ds.spikestore = NewSpikeStore()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/evtspike/spikes", nil)
	ds.handleEvtSpikeSpikes(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// TestAPI_EvtSpikeSpikes_NoSession_401 exercises the rs middleware chain in
// production wiring. Requests without a session cookie are rejected before
// reaching the handler.
func TestAPI_EvtSpikeSpikes_NoSession_401(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	ds.spikestore = NewSpikeStore()
	storeCtx, storeCancel := context.WithCancel(context.Background())
	t.Cleanup(storeCancel)
	ds.sessionStore = NewSessionStore(storeCtx)

	handler := requireSession(ds.sessionStore)(http.HandlerFunc(ds.handleEvtSpikeSpikes))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/evtspike/spikes?host=SRV01", nil)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// TestAPI_EvtSpikeSpikes_NilStore_EmptyArray verifies the feature-off wiring:
// when the subsystem has not installed a SpikeStore, the handler returns 200
// [] for a registered host rather than erroring.
func TestAPI_EvtSpikeSpikes_NilStore_EmptyArray(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	// ds.spikestore left nil.

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/evtspike/spikes?host=SRV01", nil)
	ds.handleEvtSpikeSpikes(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got []evtspike.RecentSpikeEntry
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}
