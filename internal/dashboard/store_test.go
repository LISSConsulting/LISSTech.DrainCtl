//go:build windows

package dashboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// newTestServerState builds a ServerState backed by a fresh on-disk SQLite
// telemetry DB rooted at t.TempDir(). The DB is closed on test cleanup.
func newTestServerState(t *testing.T) *ServerState {
	t.Helper()
	state, _ := newTestServerStateWithDB(t)
	return state
}

func newTestServerStateWithDB(t *testing.T) (*ServerState, *telemetry.DB) {
	t.Helper()
	db, err := telemetry.Open(t.TempDir())
	if err != nil {
		t.Fatalf("telemetry.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewServerState(telemetry.NewServerStore(db)), db
}

// mustUpdate keeps fixture writes explicit about persistence errors while
// allowing tests to inspect both accepted and unregistered-host behavior.
func mustUpdate(t *testing.T, state *ServerState, host string, result *dc.CheckResult) {
	t.Helper()
	if _, err := state.Update(host, result); err != nil {
		t.Errorf("Update(%q): %v", host, err)
	}
}

func TestGetSettings_ProjectsCollectorSafeSessionsLikeRemoteConfig(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	cfg := dc.DefaultConfig()
	cfg.Sessions = dc.SessionsConfig{
		Enabled:            true,
		CollectProcesses:   true,
		TopProcesses:       5,
		RetentionHours:     72,
		AllowActions:       true,
		IdentityVisibility: dc.SessionVisibilityMasked,
		ClientVisibility:   dc.SessionVisibilityHidden,
		ProcessVisibility:  dc.SessionVisibilityFull,
	}
	if err := dc.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	local, err := GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if local.Sessions == nil {
		t.Fatal("in-process settings omitted sessions")
	}
	want := remoteSessionsConfig(cfg.Sessions)
	if *local.Sessions != want {
		t.Errorf("in-process sessions = %#v, want %#v", *local.Sessions, want)
	}

	ds := newTestServer(t)
	response := httptest.NewRecorder()
	ds.handleGetSettings(response, httptest.NewRequest(http.MethodGet, "/api/v1/config", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/config status = %d: %s", response.Code, response.Body.String())
	}
	var remote RemoteSettings
	if err := json.NewDecoder(response.Body).Decode(&remote); err != nil {
		t.Fatalf("decode remote settings: %v", err)
	}
	if remote.Sessions == nil {
		t.Fatal("remote settings omitted sessions")
	}
	if *local.Sessions != *remote.Sessions {
		t.Errorf("in-process sessions = %#v, remote sessions = %#v", *local.Sessions, *remote.Sessions)
	}

	raw, err := json.Marshal(local)
	if err != nil {
		t.Fatalf("marshal in-process settings: %v", err)
	}
	var fields struct {
		Sessions map[string]json.RawMessage `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode in-process settings JSON: %v", err)
	}
	if _, ok := fields.Sessions["allow_actions"]; ok {
		t.Error("in-process sessions disclosed allow_actions")
	}
	if _, ok := fields.Sessions["retention_hours"]; ok {
		t.Error("in-process sessions disclosed retention_hours")
	}
}

// ── Construction ──────────────────────────────────────────────────────────────

func TestServerState_FreshIsEmpty(t *testing.T) {
	s := newTestServerState(t)
	if got := s.All(); len(got) != 0 {
		t.Errorf("All() = %d servers, want 0 for fresh store", len(got))
	}
}

// ── Local session snapshots ───────────────────────────────────────────────────

func TestReportSessionSnapshot_DeliversCallbackWithoutMutatingHostState(t *testing.T) {
	s := newTestServerState(t)
	s.Register("SRV01")
	before := s.All()
	snapshot := sessiondata.SessionSnapshot{
		Schema:           sessiondata.SnapshotSchema,
		Host:             "srv01.example.test",
		AgentInstanceID:  "00000000-0000-0000-0000-000000000001",
		Sequence:         7,
		ObservedAtMS:     123,
		CollectorVersion: "test",
		LogicalCPUCount:  1,
		Sessions:         []sessiondata.SessionRecord{},
	}
	var received sessiondata.SessionSnapshot
	s.OnSessionSnapshot = func(got sessiondata.SessionSnapshot) error {
		received = got
		return nil
	}

	handled, err := s.ReportSessionSnapshot(snapshot)
	if err != nil {
		t.Fatalf("ReportSessionSnapshot() error = %v", err)
	}
	if !handled {
		t.Fatal("ReportSessionSnapshot() handled = false, want true")
	}
	if !reflect.DeepEqual(received, snapshot) {
		t.Errorf("callback snapshot = %#v, want %#v", received, snapshot)
	}
	if after := s.All(); !reflect.DeepEqual(after, before) {
		t.Errorf("host state changed: before %#v, after %#v", before, after)
	}
}

func TestReportSessionSnapshot_NilCallbackIsUnsupported(t *testing.T) {
	handled, err := newTestServerState(t).ReportSessionSnapshot(sessiondata.SessionSnapshot{})
	if err != nil {
		t.Fatalf("ReportSessionSnapshot() error = %v, want nil", err)
	}
	if handled {
		t.Fatal("ReportSessionSnapshot() handled = true, want false without callback")
	}
}

func TestReportSessionSnapshot_PropagatesCallbackError(t *testing.T) {
	want := errors.New("session store unavailable")
	s := newTestServerState(t)
	s.OnSessionSnapshot = func(sessiondata.SessionSnapshot) error { return want }

	handled, err := s.ReportSessionSnapshot(sessiondata.SessionSnapshot{})
	if !handled {
		t.Fatal("ReportSessionSnapshot() handled = false, want true")
	}
	if !errors.Is(err, want) {
		t.Errorf("ReportSessionSnapshot() error = %v, want %v", err, want)
	}
}

// ── Register ──────────────────────────────────────────────────────────────────

func TestRegister_AddsServer(t *testing.T) {
	s := newTestServerState(t)
	s.Register("SRV01")
	if !s.IsRegistered("SRV01") {
		t.Error("SRV01 should be registered after Register()")
	}
}

func TestRegister_SetsRegisteredAt(t *testing.T) {
	before := time.Now()
	s := newTestServerState(t)
	s.Register("SRV01")

	all := s.All()
	if len(all) != 1 {
		t.Fatalf("All() = %d, want 1", len(all))
	}
	if all[0].RegisteredAt.Before(before.Add(-time.Second)) {
		t.Error("RegisteredAt should be set to approximately now")
	}
}

func TestRegister_Idempotent(t *testing.T) {
	s := newTestServerState(t)
	s.Register("SRV01")
	original := s.All()[0].RegisteredAt

	time.Sleep(10 * time.Millisecond)
	s.Register("SRV01")

	all := s.All()
	if len(all) != 1 {
		t.Fatalf("All() = %d after re-register, want 1", len(all))
	}
	if !all[0].RegisteredAt.Equal(original) {
		t.Errorf("RegisteredAt changed on re-register: original=%v after=%v",
			original, all[0].RegisteredAt)
	}
}

// ── Remove ────────────────────────────────────────────────────────────────────

func TestRemove_KnownHostReturnsTrue(t *testing.T) {
	s := newTestServerState(t)
	s.Register("SRV01")

	if !s.Remove("SRV01") {
		t.Error("Remove() should return true for a registered host")
	}
	if s.IsRegistered("SRV01") {
		t.Error("SRV01 should not be registered after Remove()")
	}
}

func TestRemove_UnknownHostReturnsFalse(t *testing.T) {
	s := newTestServerState(t)
	if s.Remove("GHOST") {
		t.Error("Remove() should return false for an unknown host")
	}
}

func TestRemove_OnlyRemovesTargetServer(t *testing.T) {
	s := newTestServerState(t)
	s.Register("SRV01")
	s.Register("SRV02")
	s.Remove("SRV01")

	if s.IsRegistered("SRV01") {
		t.Error("SRV01 should be removed")
	}
	if !s.IsRegistered("SRV02") {
		t.Error("SRV02 should remain after SRV01 is removed")
	}
}

// ── IsRegistered ──────────────────────────────────────────────────────────────

func TestIsRegistered_TrueForRegistered(t *testing.T) {
	s := newTestServerState(t)
	s.Register("SRV01")
	if !s.IsRegistered("SRV01") {
		t.Error("IsRegistered() should return true for a registered host")
	}
}

func TestIsRegistered_FalseForUnknown(t *testing.T) {
	s := newTestServerState(t)
	if s.IsRegistered("NOBODY") {
		t.Error("IsRegistered() should return false for an unknown host")
	}
}

// ── Update ────────────────────────────────────────────────────────────────────

func TestUpdate_SetsLastResult(t *testing.T) {
	s := newTestServerState(t)
	s.Register("SRV01")

	result := &dc.CheckResult{Host: "SRV01", Status: "Alert"}
	mustUpdate(t, s, "SRV01", result)

	all := s.All()
	if all[0].LastResult == nil {
		t.Fatal("LastResult should be set after Update()")
	}
	if all[0].LastResult.Status != "Alert" {
		t.Errorf("LastResult.Status = %q, want Alert", all[0].LastResult.Status)
	}
}

func TestReportLocal_PersistenceFailureReturnsFalse(t *testing.T) {
	s := newTestServerState(t)
	s.Register("SRV01")
	s.store = failingReportStore{s.store}
	if s.ReportLocal("SRV01", &dc.CheckResult{Host: "SRV01"}, nil) {
		t.Fatal("ReportLocal acknowledged an unpersisted result")
	}
}

func TestUpdate_SetsLastSeen(t *testing.T) {
	s := newTestServerState(t)
	s.Register("SRV01")
	before := time.Now()

	mustUpdate(t, s, "SRV01", &dc.CheckResult{Host: "SRV01", Status: "Healthy"})

	all := s.All()
	if all[0].LastSeen.Before(before.Add(-time.Second)) {
		t.Error("LastSeen should be updated to approximately now after Update()")
	}
}

func TestUpdate_UnregisteredHostNoSideEffect(t *testing.T) {
	s := newTestServerState(t)
	mustUpdate(t, s, "GHOST", &dc.CheckResult{Host: "GHOST", Status: "Healthy"})

	if len(s.All()) != 0 {
		t.Error("Update() on unregistered host should not create a ServerInfo entry")
	}
}

func TestUpdate_FiresOnUpdateOnlyForRegisteredHost(t *testing.T) {
	s := newTestServerState(t)
	var fired []string
	s.OnUpdate = func(h string) { fired = append(fired, h) }

	mustUpdate(t, s, "GHOST", &dc.CheckResult{Host: "GHOST"})
	if len(fired) != 0 {
		t.Errorf("OnUpdate fired for unregistered host: %v", fired)
	}

	s.Register("SRV01")
	mustUpdate(t, s, "SRV01", &dc.CheckResult{Host: "SRV01"})
	if len(fired) != 1 || fired[0] != "SRV01" {
		t.Errorf("OnUpdate fired = %v, want [SRV01]", fired)
	}
}

func TestUpdate_FiresOnMetricsOnlyForRegisteredHost(t *testing.T) {
	s := newTestServerState(t)
	var count int
	s.OnMetrics = func(r dc.CheckResult) { count++ }

	mustUpdate(t, s, "GHOST", &dc.CheckResult{Host: "GHOST"})
	if count != 0 {
		t.Errorf("OnMetrics fired for unregistered host: count=%d", count)
	}

	s.Register("SRV01")
	mustUpdate(t, s, "SRV01", &dc.CheckResult{Host: "SRV01"})
	if count != 1 {
		t.Errorf("OnMetrics count=%d, want 1", count)
	}
}

type countingSessionDropInboxWaker struct {
	count int
	state *ServerState
	db    *telemetry.DB
}

func (w *countingSessionDropInboxWaker) WakeSessionDropInbox() {
	w.count++
	if got := w.state.Get("SRV01"); got == nil || got.LastResult == nil {
		panic("session-drop wake occurred before accepted result persisted")
	}
	pending, err := telemetry.NewSessionDropStore(w.db).PendingObservation(context.Background())
	if err != nil || pending == nil {
		panic("session-drop wake occurred before durable inbox insert")
	}
}

func TestUpdate_WakesSessionDropInboxOnlyAfterAcceptedResultCommit(t *testing.T) {
	s, db := newTestServerStateWithDB(t)
	s.Register("SRV01")
	waker := &countingSessionDropInboxWaker{state: s, db: db}
	s.SetSessionDropInboxWaker(waker)

	mustUpdate(t, s, "SRV01", &dc.CheckResult{
		Host:      "SRV01",
		Timestamp: time.Now().UTC(),
		Status:    "Healthy",
		Sessions:  &dc.SessionSummary{ActiveSessions: 0, DisconnectedSessions: 0},
	})
	pending, err := telemetry.NewSessionDropStore(db).PendingObservation(context.Background())
	if err != nil {
		t.Fatalf("PendingObservation: %v", err)
	}
	if pending == nil {
		t.Fatal("accepted report did not persist a durable session-drop observation")
	}
	if pending.CanonicalHost != "SRV01" || pending.ReportEpochMs <= 0 {
		t.Fatalf("pending observation = %#v, want SRV01 with report epoch", pending)
	}
	acceptedSequence := pending.AcceptedSequence

	mustUpdate(t, s, "SRV01", &dc.CheckResult{
		Host:      "SRV01",
		Timestamp: time.UnixMilli(pending.ReportEpochMs).UTC(),
		Status:    "Healthy",
		Sessions:  &dc.SessionSummary{ActiveSessions: 0, DisconnectedSessions: 0},
	})
	pending, err = telemetry.NewSessionDropStore(db).PendingObservation(context.Background())
	if err != nil {
		t.Fatalf("PendingObservation after duplicate: %v", err)
	}
	if pending == nil || pending.AcceptedSequence != acceptedSequence {
		t.Fatalf("duplicate report replaced durable observation: %#v, want sequence %d", pending, acceptedSequence)
	}

	if waker.count != 2 {
		t.Fatalf("inbox wake count = %d, want 2 for two accepted reports; durable inbox remains idempotent", waker.count)
	}
}

func TestUpdate_DurableInboxSurvivesMissedWakeAndConsumesOnce(t *testing.T) {
	s, db := newTestServerStateWithDB(t)
	s.Register("SRV01")
	mustUpdate(t, s, "SRV01", &dc.CheckResult{
		Host:      "SRV01",
		Timestamp: time.Now().UTC(),
		Status:    "Healthy",
		Sessions:  &dc.SessionSummary{ActiveSessions: 4, DisconnectedSessions: 2},
	})

	if got := s.Get("SRV01"); got == nil || got.LastResult == nil {
		t.Fatal("accepted result was not durable before missed wake simulation")
	}
	inbox := telemetry.NewSessionDropStore(db)
	if pending, err := inbox.PendingObservation(context.Background()); err != nil || pending == nil {
		t.Fatalf("pending observation after missed wake = (%#v, %v), want durable row", pending, err)
	}

	consumed := 0
	drained, err := inbox.ConsumeNext(context.Background(), func(_ context.Context, _ *sql.Tx, observation telemetry.SessionDropObservation) error {
		consumed++
		if observation.CanonicalHost != "SRV01" {
			t.Fatalf("consumed host = %q, want SRV01", observation.CanonicalHost)
		}
		return nil
	})
	if err != nil || !drained {
		t.Fatalf("startup drain = (%v, %v), want (true, nil)", drained, err)
	}
	drained, err = inbox.ConsumeNext(context.Background(), func(context.Context, *sql.Tx, telemetry.SessionDropObservation) error {
		t.Fatal("consumed the same durable observation twice")
		return nil
	})
	if err != nil || drained {
		t.Fatalf("second startup drain = (%v, %v), want (false, nil)", drained, err)
	}
	if consumed != 1 {
		t.Fatalf("consumed observations = %d, want exactly once", consumed)
	}
}

func TestUpdate_InboxDrainUsesAcceptanceSequenceNotReportEpoch(t *testing.T) {
	s, db := newTestServerStateWithDB(t)
	s.Register("SRV01")
	s.Register("SRV02")
	reportEpoch := time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC)

	// Deliberately accept the newer report epoch first. Detector drain order
	// must preserve acceptance order, not reorder it by the report timestamp.
	mustUpdate(t, s, "SRV02", &dc.CheckResult{Host: "SRV02", Timestamp: reportEpoch.Add(time.Minute)})
	mustUpdate(t, s, "SRV01", &dc.CheckResult{Host: "SRV01", Timestamp: reportEpoch})

	inbox := telemetry.NewSessionDropStore(db)
	var drainedHosts []string
	for range 2 {
		drained, err := inbox.ConsumeNext(context.Background(), func(_ context.Context, _ *sql.Tx, observation telemetry.SessionDropObservation) error {
			drainedHosts = append(drainedHosts, observation.CanonicalHost)
			return nil
		})
		if err != nil || !drained {
			t.Fatalf("ConsumeNext = (%v, %v), want (true, nil)", drained, err)
		}
	}
	if len(drainedHosts) != 2 || drainedHosts[0] != "SRV02" || drainedHosts[1] != "SRV01" {
		t.Fatalf("drain order = %v, want acceptance order [SRV02 SRV01]", drainedHosts)
	}
}

func TestUpdate_WithoutReportEpochPersistsResultWithoutInboxObservation(t *testing.T) {
	s, db := newTestServerStateWithDB(t)
	s.Register("SRV01")
	mustUpdate(t, s, "SRV01", &dc.CheckResult{Host: "SRV01", Status: "Healthy"})

	if got := s.Get("SRV01"); got == nil || got.LastResult == nil {
		t.Fatal("report without an observation identity did not persist last result")
	}
	pending, err := telemetry.NewSessionDropStore(db).PendingObservation(context.Background())
	if err != nil {
		t.Fatalf("PendingObservation: %v", err)
	}
	if pending != nil {
		t.Fatalf("report without epoch created inbox observation %#v", pending)
	}
}

func TestSessionDropObservationPreservesNilAndSuccessfulZeroSessions(t *testing.T) {
	acceptedAt := time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC)
	reportedAt := acceptedAt.Add(-time.Minute)

	nilSessions := sessionDropObservation("SRV01", &dc.CheckResult{Timestamp: reportedAt}, acceptedAt)
	if nilSessions.SessionPresence != "nil" || nilSessions.ActiveSessions != nil || nilSessions.DisconnectedSessions != nil {
		t.Fatalf("nil session observation = %#v, want typed nil enumeration", nilSessions)
	}

	zeroSessions := sessionDropObservation("SRV01", &dc.CheckResult{
		Timestamp: reportedAt,
		Sessions:  &dc.SessionSummary{ActiveSessions: 0, DisconnectedSessions: 0},
	}, acceptedAt)
	if zeroSessions.SessionPresence != "present" || zeroSessions.ActiveSessions == nil || zeroSessions.DisconnectedSessions == nil {
		t.Fatalf("zero session observation = %#v, want present typed zero counts", zeroSessions)
	}
	if *zeroSessions.ActiveSessions != 0 || *zeroSessions.DisconnectedSessions != 0 {
		t.Fatalf("zero session counts = (%d, %d), want (0, 0)", *zeroSessions.ActiveSessions, *zeroSessions.DisconnectedSessions)
	}
}

func TestSessionDropObservationUsesSourceLocalReportTime(t *testing.T) {
	sourceLocal := time.Date(2026, time.September, 27, 0, 15, 0, 0, time.FixedZone("UTC-7", -7*60*60))
	observation := sessionDropObservation("SRV01", &dc.CheckResult{
		Timestamp:      sourceLocal,
		DrainModeValue: uint32(dc.DrainPersistent),
	}, sourceLocal.UTC().Add(time.Second))

	if observation.ReportEpochMs != sourceLocal.UTC().UnixMilli() {
		t.Fatalf("report epoch = %d, want %d", observation.ReportEpochMs, sourceLocal.UTC().UnixMilli())
	}
	if observation.LocalOffsetMinutes != -420 || observation.LocalDate != "2026-09-27" {
		t.Fatalf("source-local context = (%d, %q), want (-420, 2026-09-27)", observation.LocalOffsetMinutes, observation.LocalDate)
	}
	if observation.DrainContext != "overlap" {
		t.Fatalf("drain context = %q, want overlap", observation.DrainContext)
	}
}

func TestSessionDropObservationClassifiesProductionReportContexts(t *testing.T) {
	acceptedAt := time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC)
	reportedAt := acceptedAt.Add(-time.Minute)

	normal := sessionDropObservation("SRV01", &dc.CheckResult{
		Timestamp: reportedAt,
		Sessions:  &dc.SessionSummary{ActiveSessions: 2, DisconnectedSessions: 1},
	}, acceptedAt)
	if normal.SessionPresence != "present" || normal.FreshnessContext != "fresh" || normal.DrainContext != "none" || normal.ClassificationContext != "unexplained" {
		t.Fatalf("normal observation = %#v, want fresh unexplained typed session report", normal)
	}

	unavailable := sessionDropObservation("SRV01", &dc.CheckResult{Timestamp: reportedAt}, acceptedAt)
	if unavailable.SessionPresence != "nil" || unavailable.ClassificationContext != "not_scored" {
		t.Fatalf("unavailable observation = %#v, want not_scored", unavailable)
	}

	invalid := sessionDropObservation("SRV01", &dc.CheckResult{
		Timestamp:      reportedAt,
		DrainModeValue: uint32(dc.DrainPersistent),
		Sessions:       &dc.SessionSummary{ActiveSessions: -1, DisconnectedSessions: 0},
	}, acceptedAt)
	if invalid.SessionPresence != "invalid" || invalid.DrainContext != "overlap" || invalid.ClassificationContext != "not_scored" {
		t.Fatalf("invalid observation = %#v, want not_scored drain context", invalid)
	}

	draining := sessionDropObservation("SRV01", &dc.CheckResult{
		Timestamp:      reportedAt,
		DrainModeValue: uint32(dc.DrainPersistent),
		Sessions:       &dc.SessionSummary{ActiveSessions: 2, DisconnectedSessions: 1},
	}, acceptedAt)
	if draining.SessionPresence != "present" || draining.DrainContext != "overlap" || draining.ClassificationContext != "drain_associated" {
		t.Fatalf("draining observation = %#v, want drain-associated typed session report", draining)
	}
}

// ── All ───────────────────────────────────────────────────────────────────────

func TestAll_EmptyStateReturnsEmptySlice(t *testing.T) {
	s := newTestServerState(t)
	all := s.All()
	if all == nil {
		t.Error("All() should return a non-nil empty slice, got nil")
	}
	if len(all) != 0 {
		t.Errorf("All() = %d servers, want 0", len(all))
	}
}

func TestAll_SortedByHostname(t *testing.T) {
	s := newTestServerState(t)
	s.Register("ZETA")
	s.Register("ALPHA")
	s.Register("MANGO")

	all := s.All()
	if len(all) != 3 {
		t.Fatalf("All() = %d, want 3", len(all))
	}
	if all[0].Hostname != "ALPHA" || all[1].Hostname != "MANGO" || all[2].Hostname != "ZETA" {
		t.Errorf("servers not sorted: got [%s %s %s]", all[0].Hostname, all[1].Hostname, all[2].Hostname)
	}
}

// ── Persistence across ServerState instances (same DB file) ───────────────────

func TestPersistence_RegisterSurvivesReload(t *testing.T) {
	dir := t.TempDir()

	db1, err := telemetry.Open(dir)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	s1 := NewServerState(telemetry.NewServerStore(db1))
	s1.Register("SRV01")
	s1.Register("SRV02")
	_ = db1.Close()

	db2, err := telemetry.Open(dir)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })
	s2 := NewServerState(telemetry.NewServerStore(db2))

	if !s2.IsRegistered("SRV01") {
		t.Error("SRV01 should persist across reload")
	}
	if !s2.IsRegistered("SRV02") {
		t.Error("SRV02 should persist across reload")
	}
}

func TestPersistence_LastResultSurvivesReload(t *testing.T) {
	dir := t.TempDir()

	db1, err := telemetry.Open(dir)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	s1 := NewServerState(telemetry.NewServerStore(db1))
	s1.Register("SRV01")
	mustUpdate(t, s1, "SRV01", &dc.CheckResult{Host: "SRV01", Status: "Alert"})
	_ = db1.Close()

	db2, err := telemetry.Open(dir)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })
	s2 := NewServerState(telemetry.NewServerStore(db2))

	all := s2.All()
	if len(all) != 1 {
		t.Fatalf("All() = %d, want 1", len(all))
	}
	if all[0].LastResult == nil {
		t.Fatal("LastResult should persist across reload")
	}
	if all[0].LastResult.Status != "Alert" {
		t.Errorf("LastResult.Status = %q, want Alert", all[0].LastResult.Status)
	}
}

// ── Concurrent access ─────────────────────────────────────────────────────────

func TestServerState_ConcurrentAccess(t *testing.T) {
	s := newTestServerState(t)
	for i := range 5 {
		s.Register(hostname(i))
	}

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			host := hostname(n % 5)
			switch n % 4 {
			case 0:
				s.Register(host)
			case 1:
				mustUpdate(t, s, host, &dc.CheckResult{Host: host, Status: "Healthy"})
			case 2:
				s.IsRegistered(host)
			case 3:
				_ = s.All()
			}
		}(i)
	}
	wg.Wait()
}

// hostname returns a deterministic server name for concurrent tests.
func hostname(n int) string {
	return "SRV" + string(rune('A'+n))
}

// ── SSE hot-path cache (Step 7 / C2) ──────────────────────────────────────────

// TestUpdateLazilyLoadsRegisteredAt is a load-bearing assertion for the
// heartbeat hot-path optimization: after the first Update for a given host
// has cached its immutable registered_at, subsequent Updates must NOT touch
// the underlying ServerStore.Get. Without this property the C2 cache only
// shifts the per-heartbeat DB read from broadcastServerUpdate into Update —
// a wash. With it, the steady-state heartbeat is one DB write only.
func TestUpdateLazilyLoadsRegisteredAt(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")

	baseline := ds.state.storeGetCalls.Load()
	mustUpdate(t, ds.state, "SRV01", &dc.CheckResult{Host: "SRV01", Status: "Healthy"})
	afterFirst := ds.state.storeGetCalls.Load()

	if afterFirst != baseline+1 {
		t.Fatalf("first Update for SRV01: storeGetCalls baseline=%d after=%d, want exactly +1 (lazy registered_at load)", baseline, afterFirst)
	}

	for i := 0; i < 10; i++ {
		mustUpdate(t, ds.state, "SRV01", &dc.CheckResult{Host: "SRV01", Status: "Healthy"})
	}
	afterSteadyState := ds.state.storeGetCalls.Load()

	if afterSteadyState != afterFirst {
		t.Fatalf("steady-state Updates for SRV01: storeGetCalls afterFirst=%d afterSteadyState=%d, want equal — heartbeat path still hits ServerStore.Get", afterFirst, afterSteadyState)
	}
}

// TestBroadcastServerUpdateUsesCache is the load-bearing assertion for the
// per-host SSE cache: after Update has populated the cache, broadcastServerUpdate
// must NOT touch the underlying ServerStore. We probe this via the
// storeGetCalls counter that wraps (*ServerState).Get.
func TestBroadcastServerUpdateUsesCache(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	mustUpdate(t, ds.state, "SRV01", &dc.CheckResult{Host: "SRV01", Status: "Healthy"})

	before := ds.state.storeGetCalls.Load()
	ds.broadcastServerUpdate("SRV01")
	after := ds.state.storeGetCalls.Load()

	if after != before {
		t.Fatalf("broadcastServerUpdate hit store.Get: before=%d after=%d, want equal", before, after)
	}
	if ds.state.GetCached("SRV01") == nil {
		t.Error("GetCached(SRV01) returned nil after Update")
	}
}

// TestUpdateDoesNotPoisonCacheOnDBError forces the underlying telemetry DB
// closed so store.Update returns an error, then verifies the cache stays
// empty for the never-persisted hostname.
func TestUpdateDoesNotPoisonCacheOnDBError(t *testing.T) {
	db, err := telemetry.Open(t.TempDir())
	if err != nil {
		t.Fatalf("telemetry.Open: %v", err)
	}
	s := NewServerState(telemetry.NewServerStore(db))

	s.Register("SRV01") // succeeds while DB is open

	// Close the DB; subsequent store.Update calls must error out.
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}

	updated, err := s.Update("SRV01", &dc.CheckResult{Host: "SRV01", Status: "Alert"})
	if updated || err == nil {
		t.Fatalf("Update after DB close = (%v, %v), want failure", updated, err)
	}

	if got := s.GetCached("SRV01"); got != nil {
		t.Fatalf("GetCached after failed Update = %+v, want nil (cache must not be poisoned)", got)
	}
}

// TestRemoveClearsCache verifies Remove invalidates the cache and that a
// post-Remove broadcast falls through to store.Get (visible via the counter).
func TestRemoveClearsCache(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	mustUpdate(t, ds.state, "SRV01", &dc.CheckResult{Host: "SRV01", Status: "Healthy"})

	if ds.state.GetCached("SRV01") == nil {
		t.Fatal("precondition: cache should be populated after Update")
	}

	if !ds.state.Remove("SRV01") {
		t.Fatal("Remove returned false for a registered host")
	}

	if got := ds.state.GetCached("SRV01"); got != nil {
		t.Fatalf("GetCached after Remove = %+v, want nil", got)
	}

	before := ds.state.storeGetCalls.Load()
	ds.broadcastServerUpdate("SRV01")
	after := ds.state.storeGetCalls.Load()

	if after != before+1 {
		t.Fatalf("broadcastServerUpdate after Remove: storeGetCalls before=%d after=%d, want exactly +1 (cache miss → Get fallback)", before, after)
	}
}

// TestCachedServerInfoIsImmutable verifies that the cache stores an immutable
// snapshot — a second Update with a different *CheckResult must NOT mutate
// the *ServerInfo a previous GetCached caller is still holding.
func TestCachedServerInfoIsImmutable(t *testing.T) {
	s := newTestServerState(t)
	s.Register("SRV01")

	first := &dc.CheckResult{Host: "SRV01", Status: "Healthy"}
	mustUpdate(t, s, "SRV01", first)

	captured := s.GetCached("SRV01")
	if captured == nil || captured.LastResult == nil {
		t.Fatal("GetCached after first Update returned nil/empty")
	}
	if captured.LastResult.Status != "Healthy" {
		t.Fatalf("captured.LastResult.Status = %q, want Healthy", captured.LastResult.Status)
	}

	// Second Update with a DIFFERENT result.
	second := &dc.CheckResult{Host: "SRV01", Status: "Alert"}
	mustUpdate(t, s, "SRV01", second)

	if captured.LastResult.Status != "Healthy" {
		t.Errorf("first cached snapshot mutated by second Update: status=%q, want Healthy", captured.LastResult.Status)
	}

	// And the new GetCached call returns the new snapshot.
	now := s.GetCached("SRV01")
	if now == nil || now.LastResult == nil || now.LastResult.Status != "Alert" {
		t.Errorf("post-second-Update GetCached.LastResult.Status = %v, want Alert", now)
	}
}
