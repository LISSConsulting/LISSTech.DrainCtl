//go:build windows

package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// newTestServerWithSpikes builds a DashboardServer whose ServerState and
// EventSpikeStore share the same freshly-opened SQLite telemetry DB. Used by
// /api/evtspike/* and /api/v1/spike contract tests.
func newTestServerWithSpikes(t *testing.T) *DashboardServer {
	t.Helper()
	db, err := telemetry.Open(t.TempDir())
	if err != nil {
		t.Fatalf("telemetry.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &DashboardServer{
		state:                NewServerState(telemetry.NewServerStore(db)),
		cfg:                  dc.DashboardConfig{Group: "Domain Admins"},
		broker:               NewBroker(),
		spikes:               telemetry.NewEventSpikeStore(db),
		remoteEvtSpikeStatus: make(map[string]evtspike.DetectorStatus),
	}
}

// seedSpike persists a spike via the EventSpikeStore exactly the way the live
// OnEvtSpikeIngest wiring does. Tests call this instead of the retired
// in-memory SpikeStore.Append.
func seedSpike(t *testing.T, ds *DashboardServer, spike dc.SpikePayload) {
	t.Helper()
	_, _, err := ds.spikes.Insert(context.Background(), telemetry.EventSpike{
		Host:              spike.Host,
		Channel:           spike.Channel,
		WindowStart:       spike.WindowStart,
		WindowEnd:         spike.WindowEnd,
		Observed:          spike.Observed,
		Expected:          spike.Expected,
		TailProbability:   spike.TailProbability,
		ConfirmationCount: spike.ConfirmationCount,
		FirstSeenAt:       spike.FirstSeenAt,
	})
	if err != nil {
		t.Fatalf("seedSpike: %v", err)
	}
}

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
// with recognisable entries.
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
	ds := newTestServerWithSpikes(t)
	ds.state.Register("SRV01")

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

// TestAPI_EvtSpikeSpikes_NewestFirstOrdering verifies that the recent list is
// ordered newest-first by window_start.
func TestAPI_EvtSpikeSpikes_NewestFirstOrdering(t *testing.T) {
	ds := newTestServerWithSpikes(t)
	ds.state.Register("SRV01")

	base := time.Date(2026, 4, 16, 14, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		seedSpike(t, ds, makeSpike("SRV01", "Application", base.Add(time.Duration(i)*time.Second)))
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/evtspike/spikes?host=SRV01&limit=10", nil)
	ds.handleEvtSpikeSpikes(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got []evtspike.RecentSpikeEntry
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("len = %d, want 10", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].ID <= got[i].ID {
			t.Fatalf("not newest-first at %d: id[%d]=%d id[%d]=%d", i, i-1, got[i-1].ID, i, got[i].ID)
		}
		if !got[i-1].WindowEnd.After(got[i].WindowEnd) {
			t.Fatalf("window_end not strictly decreasing at %d", i)
		}
	}
}

// TestAPI_EvtSpikeSpikes_LimitClamp verifies the handler clamps ?limit=500 to
// spikeListMaxLimit (50) in recent-list mode.
func TestAPI_EvtSpikeSpikes_LimitClamp(t *testing.T) {
	ds := newTestServerWithSpikes(t)
	ds.state.Register("SRV01")

	base := time.Date(2026, 4, 16, 14, 0, 0, 0, time.UTC)
	for i := 0; i < 75; i++ {
		seedSpike(t, ds, makeSpike("SRV01", "Application", base.Add(time.Duration(i)*time.Second)))
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
	if len(got) != spikeListMaxLimit {
		t.Errorf("len = %d, want %d (clamp)", len(got), spikeListMaxLimit)
	}
}

// TestAPI_EvtSpikeSpikes_DefaultLimit verifies that omitting ?limit= uses the
// default of 20 entries.
func TestAPI_EvtSpikeSpikes_DefaultLimit(t *testing.T) {
	ds := newTestServerWithSpikes(t)
	ds.state.Register("SRV01")

	base := time.Date(2026, 4, 16, 14, 0, 0, 0, time.UTC)
	for i := 0; i < 25; i++ {
		seedSpike(t, ds, makeSpike("SRV01", "Application", base.Add(time.Duration(i)*time.Second)))
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
	if len(got) != spikeDefaultLimit {
		t.Errorf("len = %d, want %d (default)", len(got), spikeDefaultLimit)
	}
}

// TestAPI_EvtSpikeSpikes_SmallLimit verifies that a small ?limit=5 returns
// exactly 5 entries, newest first.
func TestAPI_EvtSpikeSpikes_SmallLimit(t *testing.T) {
	ds := newTestServerWithSpikes(t)
	ds.state.Register("SRV01")

	base := time.Date(2026, 4, 16, 14, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		seedSpike(t, ds, makeSpike("SRV01", "Application", base.Add(time.Duration(i)*time.Second)))
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
	// IDs 6..10 in decreasing order (auto-increment starts at 1).
	for i, want := range []int64{10, 9, 8, 7, 6} {
		if got[i].ID != want {
			t.Errorf("got[%d].ID = %d, want %d", i, got[i].ID, want)
		}
	}
}

// TestAPI_EvtSpikeSpikes_PerHostIsolation verifies that spikes recorded for
// one host are NOT returned when querying another host.
func TestAPI_EvtSpikeSpikes_PerHostIsolation(t *testing.T) {
	ds := newTestServerWithSpikes(t)
	ds.state.Register("SRV01")
	ds.state.Register("SRV02")

	base := time.Date(2026, 4, 16, 14, 0, 0, 0, time.UTC)
	seedSpike(t, ds, makeSpike("SRV01", "Application", base))
	seedSpike(t, ds, makeSpike("SRV01", "System", base.Add(time.Second)))
	seedSpike(t, ds, makeSpike("SRV02", "System", base.Add(2*time.Second)))

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

// TestAPI_EvtSpikeSpikes_UnknownHost_404 asserts 404 for unregistered hosts.
func TestAPI_EvtSpikeSpikes_UnknownHost_404(t *testing.T) {
	ds := newTestServerWithSpikes(t)

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
	ds := newTestServerWithSpikes(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/evtspike/spikes", nil)
	ds.handleEvtSpikeSpikes(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// TestAPI_EvtSpikeSpikes_NoSession_401 exercises the rs middleware chain.
func TestAPI_EvtSpikeSpikes_NoSession_401(t *testing.T) {
	ds := newTestServerWithSpikes(t)
	ds.state.Register("SRV01")
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
// when no EventSpikeStore is installed, the handler returns 200 [] for a
// registered host rather than erroring.
func TestAPI_EvtSpikeSpikes_NilStore_EmptyArray(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")

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

// TestAPI_EvtSpikeSpikes_Range returns only rows whose window_start falls in
// the supplied [from, to) window.
func TestAPI_EvtSpikeSpikes_Range(t *testing.T) {
	ds := newTestServerWithSpikes(t)
	ds.state.Register("SRV01")

	base := time.Date(2026, 4, 16, 14, 0, 0, 0, time.UTC)
	// Seed 10 spikes at 1-minute intervals — WindowStart = end - 10s.
	for i := 0; i < 10; i++ {
		seedSpike(t, ds, makeSpike("SRV01", "Application", base.Add(time.Duration(i)*time.Minute)))
	}

	from := base.Add(3*time.Minute - 10*time.Second)
	to := base.Add(7*time.Minute - 10*time.Second)
	url := "/api/evtspike/spikes?host=SRV01&from=" + from.UTC().Format(time.RFC3339Nano) +
		"&to=" + to.UTC().Format(time.RFC3339Nano)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, url, nil)
	ds.handleEvtSpikeSpikes(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var got []evtspike.RecentSpikeEntry
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// window_start values 3min-10s, 4min-10s, 5min-10s, 6min-10s fall in [3min-10s, 7min-10s).
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
}

// TestAPI_EvtSpikeSpikes_RangeMissingToOrFrom_400 verifies that supplying only
// one side of the window is a client error.
func TestAPI_EvtSpikeSpikes_RangeMissingToOrFrom_400(t *testing.T) {
	ds := newTestServerWithSpikes(t)
	ds.state.Register("SRV01")

	for _, qs := range []string{"&from=2026-01-01T00:00:00Z", "&to=2026-01-01T00:00:00Z"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/evtspike/spikes?host=SRV01"+qs, nil)
		ds.handleEvtSpikeSpikes(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("qs=%q: status = %d, want 400", qs, w.Code)
		}
	}
}

// TestAPI_EvtSpikeSpikes_RangeInvalidFormat_400 rejects malformed timestamps.
func TestAPI_EvtSpikeSpikes_RangeInvalidFormat_400(t *testing.T) {
	ds := newTestServerWithSpikes(t)
	ds.state.Register("SRV01")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet,
		"/api/evtspike/spikes?host=SRV01&from=not-a-time&to=2026-01-01T00:00:00Z", nil)
	ds.handleEvtSpikeSpikes(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestAPI_EvtSpikeSpikes_RangeInverted_400 rejects to<=from windows.
func TestAPI_EvtSpikeSpikes_RangeInverted_400(t *testing.T) {
	ds := newTestServerWithSpikes(t)
	ds.state.Register("SRV01")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet,
		"/api/evtspike/spikes?host=SRV01&from=2026-01-02T00:00:00Z&to=2026-01-01T00:00:00Z", nil)
	ds.handleEvtSpikeSpikes(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// ── handleReportSpike ─────────────────────────────────────────────────────────

// TestHandleReportSpike_ValidSpike_Inserted verifies the happy path: a
// registered host POSTs a valid SpikePayload → 200 {"ok":true}, store gains
// one row retrievable via Recent.
func TestHandleReportSpike_ValidSpike_Inserted(t *testing.T) {
	ds := newTestServerWithSpikes(t)
	ds.state.Register("SRV01")

	body, _ := json.Marshal(makeSpike("SRV01", "Application", time.Now()))
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/spike", bytes.NewReader(body))
	ds.handleReportSpike(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	entries, err := ds.spikes.Recent(context.Background(), "SRV01", 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("store rows = %d, want 1", len(entries))
	}
	if entries[0].Channel != "Application" {
		t.Errorf("channel = %q, want Application", entries[0].Channel)
	}
}

// TestHandleReportSpike_InvalidJSON_400 verifies that a malformed body
// returns 400 without panicking.
func TestHandleReportSpike_InvalidJSON_400(t *testing.T) {
	ds := newTestServerWithSpikes(t)
	ds.state.Register("SRV01")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/spike", bytes.NewBufferString("not json"))
	ds.handleReportSpike(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// TestHandleReportSpike_EmptyHost_400 verifies that a valid JSON body with a
// missing host field is rejected with 400.
func TestHandleReportSpike_EmptyHost_400(t *testing.T) {
	ds := newTestServerWithSpikes(t)
	ds.state.Register("SRV01")

	spike := makeSpike("SRV01", "Application", time.Now())
	spike.Host = ""
	body, _ := json.Marshal(spike)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/spike", bytes.NewReader(body))
	ds.handleReportSpike(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// TestHandleReportSpike_UnregisteredHost_403 verifies that a spike for an
// unregistered host is rejected with 403.
func TestHandleReportSpike_UnregisteredHost_403(t *testing.T) {
	ds := newTestServerWithSpikes(t)

	body, _ := json.Marshal(makeSpike("SRV01", "Application", time.Now()))
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/spike", bytes.NewReader(body))
	ds.handleReportSpike(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

// TestHandleReportSpike_NilStore_200 verifies the feature-off path: when no
// store is wired (subsystem disabled), the endpoint still returns 200 so the
// remote agent doesn't retry indefinitely.
func TestHandleReportSpike_NilStore_200(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")

	body, _ := json.Marshal(makeSpike("SRV01", "Application", time.Now()))
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/spike", bytes.NewReader(body))
	ds.handleReportSpike(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

// TestHandleReportSpike_DedupIdenticalPosts verifies that posting the same
// spike twice stores only one row — guards against remote-agent retry loops.
func TestHandleReportSpike_DedupIdenticalPosts(t *testing.T) {
	ds := newTestServerWithSpikes(t)
	ds.state.Register("SRV01")

	spike := makeSpike("SRV01", "Application", time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC))
	body, _ := json.Marshal(spike)

	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/spike", bytes.NewReader(body))
		ds.handleReportSpike(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("POST %d: status = %d, want 200", i+1, w.Code)
		}
	}
	entries, err := ds.spikes.Recent(context.Background(), "SRV01", 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("store rows = %d after 3 identical posts, want 1", len(entries))
	}
}
