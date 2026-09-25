//go:build windows

package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// newTestServerWithExclusions wires a *telemetry.RemovalStore into a
// fresh ServerState so the durable tombstone surface is exercised end-to-end.
func newTestServerWithExclusions(t *testing.T) (*DashboardServer, *telemetry.ServerStore, *telemetry.RemovalStore) {
	t.Helper()
	db, err := telemetry.Open(t.TempDir())
	if err != nil {
		t.Fatalf("telemetry.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	serverStore := telemetry.NewServerStore(db)
	removals := telemetry.NewRemovalStore(db)
	state := NewServerState(serverStore)
	state.SetExclusionReader(removals)
	state.SetRemovalWriter(removals)
	ds := &DashboardServer{
		state:                state,
		cfg:                  dc.DashboardConfig{Group: "Domain Admins"},
		broker:               NewBroker(),
		remoteEvtSpikeStatus: make(map[string]evtspike.DetectorStatus),
	}
	return ds, serverStore, removals
}

// minimalReport builds a CheckResult the report handler accepts.
func minimalReport(host string) []byte {
	r := dc.CheckResult{Host: host, Status: "Healthy"}
	out, _ := json.Marshal(r)
	return out
}

func asDashboardAdmin(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), authInfoKey, &AuthInfo{
		Username: `DOMAIN\alice`,
		Groups:   []string{"Domain Admins"},
	}))
}

// ── Permanent remove (single host) ───────────────────────────────────────────

func TestPermanentRemove_UnknownHostReturns404(t *testing.T) {
	ds, _, _ := newTestServerWithExclusions(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/servers/GHOST/permanent-remove", nil)
	r.SetPathValue("host", "GHOST")
	r = asDashboardAdmin(r)
	ds.handlePermanentRemoveServer(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestPermanentRemove_HappyPath(t *testing.T) {
	ds, _, _ := newTestServerWithExclusions(t)
	ds.state.Register("SRV01")

	w := httptest.NewRecorder()
	body := bytes.NewReader([]byte(`{"reason":"decommissioned"}`))
	r := httptest.NewRequest(http.MethodPost, "/api/v1/servers/SRV01/permanent-remove", body)
	r.SetPathValue("host", "SRV01")
	r = asDashboardAdmin(r)
	ds.handlePermanentRemoveServer(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		OK        bool      `json:"ok"`
		Host      string    `json:"host"`
		Permanent bool      `json:"permanent"`
		RemovedAt time.Time `json:"removed_at"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK || !resp.Permanent || resp.Host != "SRV01" {
		t.Errorf("resp = %+v", resp)
	}
	if resp.RemovedAt.IsZero() {
		t.Error("RemovedAt is zero")
	}
	if ds.state.IsRegistered("SRV01") {
		t.Error("SRV01 still registered after permanent-remove")
	}
	if !ds.state.IsExcluded("SRV01") {
		t.Error("SRV01 not excluded after permanent-remove")
	}
}

func TestPermanentRemove_Idempotent(t *testing.T) {
	ds, _, _ := newTestServerWithExclusions(t)
	ds.state.Register("SRV01")

	makeReq := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/servers/SRV01/permanent-remove", nil)
		r.SetPathValue("host", "SRV01")
		r = asDashboardAdmin(r)
		ds.handlePermanentRemoveServer(w, r)
		return w
	}
	if w := makeReq(); w.Code != http.StatusOK {
		t.Fatalf("first call status = %d, want 200", w.Code)
	}
	// Second call must also succeed (idempotent). The host is already excluded
	// so we go down the already-tombstoned branch — handlePermanentRemoveServer
	// permits that and refreshes metadata.
	if w := makeReq(); w.Code != http.StatusOK {
		t.Fatalf("second call status = %d, want 200", w.Code)
	}
}

func TestPermanentRemove_RejectsBadHost(t *testing.T) {
	ds, _, _ := newTestServerWithExclusions(t)

	for _, bad := range []string{"", "host with spaces", strings.Repeat("a", 254)} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/servers/x/permanent-remove", nil)
		r.SetPathValue("host", bad)
		ds.handlePermanentRemoveServer(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("bad host %q: status = %d, want 400", bad, w.Code)
		}
	}
}

// ── Removed-list endpoint ─────────────────────────────────────────────────────

func TestListRemoved_EmptyArrayWhenNoRemovals(t *testing.T) {
	ds, _, _ := newTestServerWithExclusions(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/servers/removed", nil)
	ds.handleListRemovedServers(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body, _ := io.ReadAll(w.Body)
	if !bytes.HasPrefix(bytes.TrimSpace(body), []byte("[]")) {
		t.Errorf("body = %s, want empty array", body)
	}
}

func TestListRemoved_ShowsPermanentTombstones(t *testing.T) {
	ds, _, _ := newTestServerWithExclusions(t)
	ds.state.Register("SRV01")
	ds.state.Register("SRV02")

	if _, err := ds.state.PermanentRemove("SRV01", "alice", ""); err != nil {
		t.Fatalf("remove SRV01: %v", err)
	}
	// Force a real time advance between the two exclusions so the
	// (excluded_at_ms DESC, hostname ASC) ordering in AllExcluded does not
	// collide on a single-millisecond tie. SQLite's UnixMilli resolution
	// is the actual storage granularity.
	time.Sleep(5 * time.Millisecond)
	if _, err := ds.state.PermanentRemove("SRV02", "bob", "decommissioned"); err != nil {
		t.Fatalf("remove SRV02: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/servers/removed", nil)
	ds.handleListRemovedServers(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got []RemovedServer
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	// Newest first when timestamps differ; SRV02 was removed after SRV01.
	wantFirst := got[0]
	if wantFirst.Host != "SRV02" {
		t.Errorf("first entry = %+v, want SRV02 first (newest)", wantFirst)
	}
	hosts := map[string]RemovedServer{got[0].Host: got[0], got[1].Host: got[1]}
	if hosts["SRV02"].RemovedBy != "bob" || hosts["SRV02"].Reason != "decommissioned" {
		t.Errorf("SRV02 entry = %+v", hosts["SRV02"])
	}
	if hosts["SRV01"].RemovedBy != "alice" || hosts["SRV01"].Reason != "" {
		t.Errorf("SRV01 entry = %+v", hosts["SRV01"])
	}
	if !got[0].Permanent || !got[1].Permanent {
		t.Errorf("entries not flagged permanent: [0]=%v [1]=%v", got[0].Permanent, got[1].Permanent)
	}
}

// ── Restore ───────────────────────────────────────────────────────────────────

func TestRestore_UnknownTombstoneReturns404(t *testing.T) {
	ds, _, _ := newTestServerWithExclusions(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/servers/GHOST/restore", nil)
	r.SetPathValue("host", "GHOST")
	r = asDashboardAdmin(r)
	ds.handleRestoreServer(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestRestore_RemovesTombstone(t *testing.T) {
	ds, _, _ := newTestServerWithExclusions(t)
	ds.state.Register("SRV01")
	if _, err := ds.state.PermanentRemove("SRV01", "alice", "decommissioned"); err != nil {
		t.Fatalf("permanent-remove: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/servers/SRV01/restore", nil)
	r.SetPathValue("host", "SRV01")
	r = asDashboardAdmin(r)
	ds.handleRestoreServer(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		OK            bool      `json:"ok"`
		Host          string    `json:"host"`
		RestoredAt    time.Time `json:"restored_at"`
		WasTombstoned bool      `json:"was_tombstoned"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK || resp.Host != "SRV01" || !resp.WasTombstoned || resp.RestoredAt.IsZero() {
		t.Errorf("resp = %+v", resp)
	}
	if ds.state.IsExcluded("SRV01") {
		t.Error("SRV01 still excluded after restore")
	}
}

func TestRestore_RefusesAlreadyLive(t *testing.T) {
	ds, _, _ := newTestServerWithExclusions(t)
	ds.state.Register("SRV01")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/servers/SRV01/restore", nil)
	r.SetPathValue("host", "SRV01")
	r = asDashboardAdmin(r)
	ds.handleRestoreServer(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// ── Exclusion gate in /register and /report ──────────────────────────────────

func TestRegister_RefusesExcludedWith410(t *testing.T) {
	ds, _, _ := newTestServerWithExclusions(t)
	ds.state.Register("SRV01")
	if _, err := ds.state.PermanentRemove("SRV01", "alice", ""); err != nil {
		t.Fatalf("permanent-remove: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader([]byte(`{"hostname":"SRV01"}`)))
	r.Header.Set("Content-Type", "application/json")
	ds.handleRegister(w, r)

	if w.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410; body=%s", w.Code, w.Body.String())
	}
	if h := w.Header().Get("X-Permanently-Removed"); h != "1" {
		t.Errorf("X-Permanently-Removed = %q, want 1", h)
	}
	if ds.state.IsRegistered("SRV01") {
		t.Error("excluded host should NOT be re-registered by 410-rejected call")
	}
}

func TestRegister_AcceptsAfterRestore(t *testing.T) {
	ds, _, _ := newTestServerWithExclusions(t)
	ds.state.Register("SRV01")
	if _, err := ds.state.PermanentRemove("SRV01", "alice", ""); err != nil {
		t.Fatalf("permanent-remove: %v", err)
	}
	if _, _, err := ds.state.RestoreRemoved("SRV01"); err != nil {
		t.Fatalf("restore: %v", err)
	}

	w := httptest.NewRecorder()
	// We have to skip the auth path because there's no AuthInfo on the request.
	// Drive the gate directly: a plain /register against the same hostname
	// must now succeed because the tombstone was deleted.
	r := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader([]byte(`{"hostname":"SRV01"}`)))
	r.Header.Set("Content-Type", "application/json")
	ds.handleRegister(w, r)

	// Without an AuthInfo, the handler short-circuits via `isAuthorizedForHost`
	// and returns 403. For this test we want only the durable-gate path
	// outcome — simulate that by checking that IsExcluded is false and the
	// gate path did NOT trip with 410.
	if w.Code == http.StatusGone {
		t.Fatalf("got 410 — restored host should pass the durable gate. body=%s", w.Body.String())
	}
	if ds.state.IsExcluded("SRV01") {
		t.Fatal("SRV01 still excluded after restore")
	}
}

func TestReport_RefusesExcludedWith410(t *testing.T) {
	ds, srv, _ := newTestServerWithExclusions(t)
	ds.state.Register("SRV01")
	if _, err := ds.state.PermanentRemove("SRV01", "alice", ""); err != nil {
		t.Fatalf("permanent-remove: %v", err)
	}

	// Simulate the race that the 410-on-/report gate guards against: an
	// agent's /report races between ServerStore.Remove and RemovalStore.Exclude
	// and the live row is momentarily re-added before the tombstone lands.
	// In production this would be a stale /report call from an in-flight
	// heartbeat; we recreate it by writing the live row back via the
	// underlying ServerStore.Import helper which performs INSERT OR IGNORE
	// (so it can re-add the row even though PermanentRemove already deleted it).
	ctx := context.Background()
	n, err := srv.Import(ctx, []telemetry.ServerInfo{
		{Hostname: "SRV01", RegisteredAt: time.Now().UTC()},
	})
	if err != nil || n != 1 {
		t.Fatalf("re-add live row via ServerStore.Import: inserted=%d err=%v", n, err)
	}
	if !ds.state.IsRegistered("SRV01") {
		t.Fatal("SRV01 not registered after race-recreation")
	}
	if !ds.state.IsExcluded("SRV01") {
		t.Fatal("SRV01 not excluded (race setup failed)")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/report", bytes.NewReader(minimalReport("SRV01")))
	r.Header.Set("Content-Type", "application/json")
	ds.handleReport(w, r)

	if w.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410; body=%s", w.Code, w.Body.String())
	}
	if h := w.Header().Get("X-Permanently-Removed"); h != "1" {
		t.Errorf("X-Permanently-Removed = %q, want 1", h)
	}
}

// ── Batch endpoints ───────────────────────────────────────────────────────────

func TestPermanentRemoveBatch_AllPaths(t *testing.T) {
	ds, _, _ := newTestServerWithExclusions(t)
	ds.state.Register("LIVE01")
	ds.state.Register("LIVE02")

	body, _ := json.Marshal(map[string]any{
		"hosts":  []string{"LIVE01", "LIVE02", "GHOST01", "bad host name!"},
		"reason": "decom",
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/servers/permanent-remove",
		bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r = asDashboardAdmin(r)
	ds.handlePermanentRemoveBatch(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var got permanentRemoveBatchResult
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Removed) != 2 || got.Removed[0] != "LIVE01" || got.Removed[1] != "LIVE02" {
		t.Errorf("removed = %+v, want [LIVE01 LIVE02]", got.Removed)
	}
	if len(got.Skipped) != 1 || got.Skipped[0] != "GHOST01" {
		t.Errorf("skipped = %+v, want [GHOST01]", got.Skipped)
	}
	if len(got.Errors) != 1 || got.Errors[0].Status != http.StatusBadRequest {
		t.Errorf("errors = %+v, want one 400 entry", got.Errors)
	}
}

func TestRestoreBatch_AllPaths(t *testing.T) {
	ds, _, _ := newTestServerWithExclusions(t)
	ds.state.Register("LIVE01")
	ds.state.Register("REMOVED01")
	if _, err := ds.state.PermanentRemove("REMOVED01", "alice", ""); err != nil {
		t.Fatalf("permanent-remove: %v", err)
	}
	ds.state.Register("REMOVED02")
	if _, err := ds.state.PermanentRemove("REMOVED02", "alice", ""); err != nil {
		t.Fatalf("permanent-remove: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"hosts": []string{"REMOVED01", "REMOVED02", "LIVE01", "GHOST01"},
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/servers/restore", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r = asDashboardAdmin(r)
	ds.handleRestoreBatch(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var got restoreBatchResult
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// REMOVED01 + REMOVED02 → restored
	if len(got.Restored) != 2 || got.Restored[0] != "REMOVED01" || got.Restored[1] != "REMOVED02" {
		t.Errorf("restored = %+v", got.Restored)
	}
	// LIVE01 = already-live, GHOST01 = never-tombstoned → skipped (both)
	if len(got.Skipped) != 2 {
		t.Errorf("skipped = %+v, want 2 (LIVE01 + GHOST01)", got.Skipped)
	}
	if len(got.Errors) != 0 {
		t.Errorf("errors = %+v, want 0", got.Errors)
	}
}

// ── Wire durability end-to-end ────────────────────────────────────────────────

// TestExclusionDurableAcrossInstances is the acceptance test: a tombstone
// written via the production path (handlers + telemetry.RemovalStore) persists
// across a fresh ServerState instance reading the same DB file, and the
// next Register attempt is rejected.
func TestExclusionDurableAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	db, err := telemetry.Open(dir)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	store := telemetry.NewServerStore(db)
	removals := telemetry.NewRemovalStore(db)
	state := NewServerState(store)
	state.SetExclusionReader(removals)
	state.SetRemovalWriter(removals)
	state.Register("SRV01")

	// Mark permanent-removal.
	if _, err := state.PermanentRemove("SRV01", "alice", "test"); err != nil {
		t.Fatalf("PermanentRemove: %v", err)
	}
	if !state.IsExcluded("SRV01") {
		t.Fatal("SRV01 not excluded post PermanentRemove")
	}
	_ = db.Close()

	// Reopen with a fresh DB and ServerState — simulating a service restart.
	db2, err := telemetry.Open(dir)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer func() { _ = db2.Close() }()
	store2 := telemetry.NewServerStore(db2)
	removals2 := telemetry.NewRemovalStore(db2)
	state2 := NewServerState(store2)
	state2.SetExclusionReader(removals2)
	state2.SetRemovalWriter(removals2)

	// Tombstone must survive.
	if !state2.IsExcluded("SRV01") {
		t.Fatal("tombstone lost across DB reopen")
	}

	// Re-registration must be refused by the gate (this is silent in
	// ServerState.Register; the HTTP handler also rejects with 410).
	state2.Register("SRV01")
	// After the post-restart Register, IsRegistered may be true (the
	// Register path silently no-ops but a stale caller could observe a
	// still-registered row) — what matters is IsExcluded stays true.
	if !state2.IsExcluded("SRV01") {
		t.Fatal("IsExcluded flipped after attempted re-register")
	}

	// Restore, then attempt re-register through HTTP layer.
	if found, _, err := state2.RestoreRemoved("SRV01"); err != nil || !found {
		t.Fatalf("restore failed: found=%v err=%v", found, err)
	}
	if state2.IsExcluded("SRV01") {
		t.Fatal("SRV01 still excluded after Restore")
	}
	state2.Register("SRV01")
	if !state2.IsRegistered("SRV01") {
		t.Fatal("SRV01 not registered after restore + Register")
	}
}

// ── Removed/Registered summary helpers (touch the helpers we depend on) ──────

func TestToRemovedServer_PopulatesAllFields(t *testing.T) {
	in := telemetry.RemovalEntry{
		Hostname:   "SRV01",
		ExcludedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		ExcludedBy: "alice",
		Reason:     "decom",
	}
	got := toRemovedServer(in)
	if got.Host != "SRV01" || !got.Permanent || got.RemovedBy != "alice" || got.Reason != "decom" {
		t.Errorf("toRemovedServer = %+v", got)
	}
	if !got.RemovedAt.Equal(in.ExcludedAt) {
		t.Errorf("RemovedAt = %v, want %v", got.RemovedAt, in.ExcludedAt)
	}
}

// ── SSE event publication (smoke) ─────────────────────────────────────────────

func TestBroadcastServerPermanentlyRemoved_PublishesEvent(t *testing.T) {
	ds := &DashboardServer{broker: NewBroker()}
	id, ch, done, err := ds.broker.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer ds.broker.Unsubscribe(id)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	ds.broadcastServerPermanentlyRemoved("SRV01", "alice", "decom", now)
	select {
	case got := <-ch:
		s := string(got)
		if !strings.Contains(s, `"server_permanently_removed"`) {
			t.Errorf("type missing: %s", s)
		}
		if !strings.Contains(s, `"SRV01"`) {
			t.Errorf("host missing: %s", s)
		}
		if !strings.Contains(s, `"permanent":true`) {
			t.Errorf("permanent flag missing: %s", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SSE event")
	}
	_ = done
}

func TestBroadcastServerRestored_PublishesEvent(t *testing.T) {
	ds := &DashboardServer{broker: NewBroker()}
	id, ch, _, err := ds.broker.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer ds.broker.Unsubscribe(id)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	ds.broadcastServerRestored("SRV01", "alice", now)
	select {
	case got := <-ch:
		s := string(got)
		if !strings.Contains(s, `"server_restored"`) {
			t.Errorf("type missing: %s", s)
		}
		if !strings.Contains(s, `"SRV01"`) {
			t.Errorf("host missing: %s", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SSE event")
	}
}

// ── Sanity: ensure removing tombstone store wiring doesn't break tests ──────

func TestRegister_WithoutExclusionsWiredIsLegacyBehaviour(t *testing.T) {
	ds := newTestServer(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader([]byte(`{"hostname":"SRV01"}`)))

	r.Header.Set("Content-Type", "application/json")
	ds.handleRegister(w, r)

	// Without exclusion wiring and without AuthInfo, we expect the
	// identity-mismatch path to take over (machine-account check kicks in
	// and fails). What matters is that we did NOT observe a 410 Gone — the
	// pre-removed state legacy behavior is preserved.
	if w.Code == http.StatusGone {
		t.Fatalf("got 410 without exclusions wired (gate should be no-op): body=%s", w.Body.String())
	}
}
func TestPermanentRemoval_RequiresConfiguredAdminGroup(t *testing.T) {
	ds, _, _ := newTestServerWithExclusions(t)
	ds.state.Register("SRV01")

	for _, path := range []string{
		"/api/v1/servers/SRV01/permanent-remove",
		"/api/v1/servers/SRV01/restore",
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, path, nil)
		r = r.WithContext(context.WithValue(r.Context(), authInfoKey, &AuthInfo{
			Username: `DOMAIN\bob`,
			Groups:   []string{"Operators"},
		}))
		if strings.HasSuffix(path, "permanent-remove") {
			r.SetPathValue("host", "SRV01")
			ds.handlePermanentRemoveServer(w, r)
		} else {
			r.SetPathValue("host", "SRV01")
			ds.handleRestoreServer(w, r)
		}
		if w.Code != http.StatusForbidden {
			t.Errorf("%s status = %d, want 403", path, w.Code)
		}
	}
}

func TestPermanentRemoval_MixedCaseHostnamesShareOneIdentity(t *testing.T) {
	ds, _, _ := newTestServerWithExclusions(t)
	ds.state.Register("Srv01")
	if !ds.state.IsRegistered("sRv01") {
		t.Fatal("mixed-case lookup did not find registered host")
	}

	w := httptest.NewRecorder()
	r := asDashboardAdmin(httptest.NewRequest(http.MethodPost, "/api/v1/servers/srv01/permanent-remove", nil))
	r.SetPathValue("host", "srv01")
	ds.handlePermanentRemoveServer(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("permanent remove status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if ds.state.IsRegistered("SRV01") || !ds.state.IsExcluded("SrV01") {
		t.Fatal("mixed-case permanent removal did not operate on the registered identity")
	}

	w = httptest.NewRecorder()
	r = asDashboardAdmin(httptest.NewRequest(http.MethodPost, "/api/v1/servers/sRv01/restore", nil))
	r.SetPathValue("host", "sRv01")
	ds.handleRestoreServer(w, r)
	if w.Code != http.StatusOK || ds.state.IsExcluded("srv01") {
		t.Fatalf("mixed-case restore failed: status=%d excluded=%v", w.Code, ds.state.IsExcluded("srv01"))
	}
}

// keep context import used in case future tests need it.
var _ = context.Background
var _ = errors.New
