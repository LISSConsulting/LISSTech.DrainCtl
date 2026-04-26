//go:build windows

package dashboard

import (
	"sync"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// newTestServerState builds a ServerState backed by a fresh on-disk SQLite
// telemetry DB rooted at t.TempDir(). The DB is closed on test cleanup.
func newTestServerState(t *testing.T) *ServerState {
	t.Helper()
	db, err := telemetry.Open(t.TempDir())
	if err != nil {
		t.Fatalf("telemetry.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewServerState(telemetry.NewServerStore(db))
}

// ── Construction ──────────────────────────────────────────────────────────────

func TestServerState_FreshIsEmpty(t *testing.T) {
	s := newTestServerState(t)
	if got := s.All(); len(got) != 0 {
		t.Errorf("All() = %d servers, want 0 for fresh store", len(got))
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
	s.Update("SRV01", result)

	all := s.All()
	if all[0].LastResult == nil {
		t.Fatal("LastResult should be set after Update()")
	}
	if all[0].LastResult.Status != "Alert" {
		t.Errorf("LastResult.Status = %q, want Alert", all[0].LastResult.Status)
	}
}

func TestUpdate_SetsLastSeen(t *testing.T) {
	s := newTestServerState(t)
	s.Register("SRV01")
	before := time.Now()

	s.Update("SRV01", &dc.CheckResult{Host: "SRV01", Status: "Healthy"})

	all := s.All()
	if all[0].LastSeen.Before(before.Add(-time.Second)) {
		t.Error("LastSeen should be updated to approximately now after Update()")
	}
}

func TestUpdate_UnregisteredHostNoSideEffect(t *testing.T) {
	s := newTestServerState(t)
	s.Update("GHOST", &dc.CheckResult{Host: "GHOST", Status: "Healthy"})

	if len(s.All()) != 0 {
		t.Error("Update() on unregistered host should not create a ServerInfo entry")
	}
}

func TestUpdate_FiresOnUpdateOnlyForRegisteredHost(t *testing.T) {
	s := newTestServerState(t)
	var fired []string
	s.OnUpdate = func(h string) { fired = append(fired, h) }

	s.Update("GHOST", &dc.CheckResult{Host: "GHOST"})
	if len(fired) != 0 {
		t.Errorf("OnUpdate fired for unregistered host: %v", fired)
	}

	s.Register("SRV01")
	s.Update("SRV01", &dc.CheckResult{Host: "SRV01"})
	if len(fired) != 1 || fired[0] != "SRV01" {
		t.Errorf("OnUpdate fired = %v, want [SRV01]", fired)
	}
}

func TestUpdate_FiresOnMetricsOnlyForRegisteredHost(t *testing.T) {
	s := newTestServerState(t)
	var count int
	s.OnMetrics = func(r dc.CheckResult) { count++ }

	s.Update("GHOST", &dc.CheckResult{Host: "GHOST"})
	if count != 0 {
		t.Errorf("OnMetrics fired for unregistered host: count=%d", count)
	}

	s.Register("SRV01")
	s.Update("SRV01", &dc.CheckResult{Host: "SRV01"})
	if count != 1 {
		t.Errorf("OnMetrics count=%d, want 1", count)
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
	s1.Update("SRV01", &dc.CheckResult{Host: "SRV01", Status: "Alert"})
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
				s.Update(host, &dc.CheckResult{Host: host, Status: "Healthy"})
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

// TestBroadcastServerUpdateUsesCache is the load-bearing assertion for the
// per-host SSE cache: after Update has populated the cache, broadcastServerUpdate
// must NOT touch the underlying ServerStore. We probe this via the
// storeGetCalls counter that wraps (*ServerState).Get.
func TestBroadcastServerUpdateUsesCache(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	ds.state.Update("SRV01", &dc.CheckResult{Host: "SRV01", Status: "Healthy"})

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

	s.Update("SRV01", &dc.CheckResult{Host: "SRV01", Status: "Alert"})

	if got := s.GetCached("SRV01"); got != nil {
		t.Fatalf("GetCached after failed Update = %+v, want nil (cache must not be poisoned)", got)
	}
}

// TestRemoveClearsCache verifies Remove invalidates the cache and that a
// post-Remove broadcast falls through to store.Get (visible via the counter).
func TestRemoveClearsCache(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	ds.state.Update("SRV01", &dc.CheckResult{Host: "SRV01", Status: "Healthy"})

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
	s.Update("SRV01", first)

	captured := s.GetCached("SRV01")
	if captured == nil || captured.LastResult == nil {
		t.Fatal("GetCached after first Update returned nil/empty")
	}
	if captured.LastResult.Status != "Healthy" {
		t.Fatalf("captured.LastResult.Status = %q, want Healthy", captured.LastResult.Status)
	}

	// Second Update with a DIFFERENT result.
	second := &dc.CheckResult{Host: "SRV01", Status: "Alert"}
	s.Update("SRV01", second)

	if captured.LastResult.Status != "Healthy" {
		t.Errorf("first cached snapshot mutated by second Update: status=%q, want Healthy", captured.LastResult.Status)
	}

	// And the new GetCached call returns the new snapshot.
	now := s.GetCached("SRV01")
	if now == nil || now.LastResult == nil || now.LastResult.Status != "Alert" {
		t.Errorf("post-second-Update GetCached.LastResult.Status = %v, want Alert", now)
	}
}
