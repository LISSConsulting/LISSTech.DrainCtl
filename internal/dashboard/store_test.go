//go:build windows

package dashboard

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// captureErrLog installs a temporary slog handler that records whether any
// ERROR-level records were emitted. Returns a cleanup func and a pointer to
// the captured flag. The previous default logger is restored on cleanup.
func captureErrLog(t *testing.T) *bool {
	t.Helper()
	prev := slog.Default()
	var got bool
	slog.SetDefault(slog.New(&errCapHandler{flag: &got}))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &got
}

// errCapHandler is a minimal slog.Handler that sets *flag when an ERROR-level
// record is received. It does NOT forward to any other handler to avoid the
// log.Logger mutex re-entrancy deadlock that occurs when the default slog
// handler routes back through log.Logger while that mutex is already held.
type errCapHandler struct {
	flag *bool
}

func (h *errCapHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *errCapHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError {
		*h.flag = true
	}
	return nil
}
func (h *errCapHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *errCapHandler) WithGroup(_ string) slog.Handler      { return h }

// ── NewServerState ────────────────────────────────────────────────────────────

func TestNewServerState_EmptyDir(t *testing.T) {
	s := NewServerState(t.TempDir())
	if got := s.All(); len(got) != 0 {
		t.Errorf("All() = %d servers, want 0 for empty dir", len(got))
	}
}

func TestNewServerState_LoadsExistingFile(t *testing.T) {
	dir := t.TempDir()
	s1 := NewServerState(dir)
	s1.Register("SRV01")
	s1.Register("SRV02")

	// Second instance reads the same file.
	s2 := NewServerState(dir)
	all := s2.All()
	if len(all) != 2 {
		t.Fatalf("loaded state has %d servers, want 2", len(all))
	}
}

func TestNewServerState_IgnoresMissingFile(t *testing.T) {
	dir := t.TempDir()
	// Remove any file that might exist.
	_ = os.Remove(filepath.Join(dir, "servers.json"))

	s := NewServerState(dir)
	if len(s.All()) != 0 {
		t.Error("expected empty state when servers.json is absent")
	}
}

func TestNewServerState_IgnoresCorruptFile(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "servers.json"), []byte("not json {{{"), 0o644)

	// Must not panic or return an error — simply starts empty.
	s := NewServerState(dir)
	if len(s.All()) != 0 {
		t.Error("expected empty state when servers.json is corrupt")
	}
}

// ── Register ──────────────────────────────────────────────────────────────────

func TestRegister_AddsServer(t *testing.T) {
	s := NewServerState(t.TempDir())
	s.Register("SRV01")

	if !s.IsRegistered("SRV01") {
		t.Error("SRV01 should be registered after Register()")
	}
}

func TestRegister_SetsRegisteredAt(t *testing.T) {
	before := time.Now()
	s := NewServerState(t.TempDir())
	s.Register("SRV01")

	all := s.All()
	if len(all) != 1 {
		t.Fatalf("All() = %d, want 1", len(all))
	}
	if all[0].RegisteredAt.Before(before) {
		t.Error("RegisteredAt should be set to approximately now")
	}
}

func TestRegister_Idempotent(t *testing.T) {
	s := NewServerState(t.TempDir())
	s.Register("SRV01")
	original := s.All()[0].RegisteredAt

	// Small delay to ensure time.Now() would differ.
	time.Sleep(time.Millisecond)
	s.Register("SRV01") // re-register

	all := s.All()
	if len(all) != 1 {
		t.Fatalf("All() = %d after re-register, want 1", len(all))
	}
	if !all[0].RegisteredAt.Equal(original) {
		t.Error("RegisteredAt should not change on idempotent re-register")
	}
}

// ── Remove ────────────────────────────────────────────────────────────────────

func TestRemove_KnownHostReturnsTrue(t *testing.T) {
	s := NewServerState(t.TempDir())
	s.Register("SRV01")

	if !s.Remove("SRV01") {
		t.Error("Remove() should return true for a registered host")
	}
	if s.IsRegistered("SRV01") {
		t.Error("SRV01 should not be registered after Remove()")
	}
}

func TestRemove_UnknownHostReturnsFalse(t *testing.T) {
	s := NewServerState(t.TempDir())

	if s.Remove("GHOST") {
		t.Error("Remove() should return false for an unknown host")
	}
}

func TestRemove_OnlyRemovesTargetServer(t *testing.T) {
	s := NewServerState(t.TempDir())
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
	s := NewServerState(t.TempDir())
	s.Register("SRV01")
	if !s.IsRegistered("SRV01") {
		t.Error("IsRegistered() should return true for a registered host")
	}
}

func TestIsRegistered_FalseForUnknown(t *testing.T) {
	s := NewServerState(t.TempDir())
	if s.IsRegistered("NOBODY") {
		t.Error("IsRegistered() should return false for an unknown host")
	}
}

// ── Update ────────────────────────────────────────────────────────────────────

func TestUpdate_SetsLastResult(t *testing.T) {
	s := NewServerState(t.TempDir())
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
	s := NewServerState(t.TempDir())
	s.Register("SRV01")
	before := time.Now()

	s.Update("SRV01", &dc.CheckResult{Host: "SRV01", Status: "Healthy"})

	all := s.All()
	if all[0].LastSeen.Before(before) {
		t.Error("LastSeen should be updated to approximately now after Update()")
	}
}

func TestUpdate_UnregisteredHostNoSideEffect(t *testing.T) {
	s := NewServerState(t.TempDir())
	// Update a host that was never registered → no panic, All() still empty.
	s.Update("GHOST", &dc.CheckResult{Host: "GHOST", Status: "Healthy"})

	if len(s.All()) != 0 {
		t.Error("Update() on unregistered host should not create a ServerInfo entry")
	}
}

// ── All ───────────────────────────────────────────────────────────────────────

func TestAll_EmptyStateReturnsEmptySlice(t *testing.T) {
	s := NewServerState(t.TempDir())
	all := s.All()
	if all == nil {
		t.Error("All() should return a non-nil empty slice, got nil")
	}
	if len(all) != 0 {
		t.Errorf("All() = %d servers, want 0", len(all))
	}
}

func TestAll_SortedByHostname(t *testing.T) {
	s := NewServerState(t.TempDir())
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

func TestAll_ReturnsSnapshot(t *testing.T) {
	s := NewServerState(t.TempDir())
	s.Register("SRV01")

	snapshot := s.All()
	// Mutating the snapshot should not affect the internal state.
	snapshot[0].Hostname = "MUTATED"

	all2 := s.All()
	if all2[0].Hostname != "SRV01" {
		t.Error("All() should return a copy, not a reference to internal state")
	}
}

// ── Persistence ───────────────────────────────────────────────────────────────

func TestPersistence_RegisterSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	s1 := NewServerState(dir)
	s1.Register("SRV01")
	s1.Register("SRV02")

	s2 := NewServerState(dir)
	if !s2.IsRegistered("SRV01") {
		t.Error("SRV01 should persist across reload")
	}
	if !s2.IsRegistered("SRV02") {
		t.Error("SRV02 should persist across reload")
	}
}

func TestPersistence_RemoveSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	s1 := NewServerState(dir)
	s1.Register("SRV01")
	s1.Register("SRV02")
	s1.Remove("SRV01")

	s2 := NewServerState(dir)
	if s2.IsRegistered("SRV01") {
		t.Error("removed SRV01 should not persist across reload")
	}
	if !s2.IsRegistered("SRV02") {
		t.Error("SRV02 should remain after SRV01 removal")
	}
}

func TestPersistence_LastResultSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	s1 := NewServerState(dir)
	s1.Register("SRV01")
	s1.Update("SRV01", &dc.CheckResult{Host: "SRV01", Status: "Alert"})

	s2 := NewServerState(dir)
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

func TestPersistence_WritesTmpThenRenames(t *testing.T) {
	dir := t.TempDir()
	s := NewServerState(dir)
	s.Register("SRV01")

	// After save, .tmp file must not exist (it was renamed).
	tmpPath := filepath.Join(dir, "servers.json.tmp")
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Error("servers.json.tmp should not exist after successful save")
	}
	// But the final file must exist.
	if _, err := os.Stat(filepath.Join(dir, "servers.json")); os.IsNotExist(err) {
		t.Error("servers.json should exist after Register()")
	}
}

func TestPersistence_FileIsValidJSON(t *testing.T) {
	dir := t.TempDir()
	s := NewServerState(dir)
	s.Register("SRV01")
	s.Update("SRV01", &dc.CheckResult{Host: "SRV01", Status: "Healthy"})

	data, err := os.ReadFile(filepath.Join(dir, "servers.json"))
	if err != nil {
		t.Fatalf("read servers.json: %v", err)
	}
	var list []ServerInfo
	if err := json.Unmarshal(data, &list); err != nil {
		t.Errorf("servers.json is not valid JSON: %v", err)
	}
}

// ── Concurrent access ─────────────────────────────────────────────────────────

func TestServerState_ConcurrentAccess(t *testing.T) {
	s := NewServerState(t.TempDir())

	// Pre-register hosts so Update() can write LastResult.
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

// hostname returns a deterministic server name for use in concurrent tests.
func hostname(n int) string {
	return "SRV" + string(rune('A'+n))
}

// ── save() error paths ────────────────────────────────────────────────────────

// TestSave_WriteFileError verifies that save() handles the case where the tmp
// file cannot be written (here: a directory already exists at the tmp path).
// The call must complete without panic and emit an ERR log.
func TestSave_WriteFileError(t *testing.T) {
	dir := t.TempDir()
	// NewServerState stores data at filepath.Join(dir, "servers.json");
	// the tmp path is filepath.Join(dir, "servers.json.tmp").
	tmpPath := filepath.Join(dir, "servers.json.tmp")

	// Create a directory at the tmp path so os.WriteFile fails.
	if err := os.MkdirAll(tmpPath, 0o755); err != nil {
		t.Fatalf("setup: mkdir %s: %v", tmpPath, err)
	}

	errLogged := captureErrLog(t)

	s := NewServerState(dir)
	s.Register("SRV01") // triggers save() → WriteFile should fail

	if !*errLogged {
		t.Error("expected ERR log from save() WriteFile failure, got none")
	}
	// servers.json must not exist (Rename was never reached).
	jsonPath := filepath.Join(dir, "servers.json")
	if _, err := os.Stat(jsonPath); !os.IsNotExist(err) {
		t.Error("servers.json should not exist after WriteFile failure")
	}
}

// TestSave_RenameError verifies that save() handles the case where the rename
// from tmp to final path fails (here: the target path is a directory).
// The call must complete without panic and emit an ERR log.
func TestSave_RenameError(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "servers.json")

	// Create a directory at the final path so os.Rename fails.
	if err := os.MkdirAll(jsonPath, 0o755); err != nil {
		t.Fatalf("setup: mkdir %s: %v", jsonPath, err)
	}

	errLogged := captureErrLog(t)

	s := NewServerState(dir)
	s.Register("SRV01") // triggers save() → WriteFile OK, Rename fails

	if !*errLogged {
		t.Error("expected ERR log from save() Rename failure, got none")
	}
}

// TestSave_MkdirAllError verifies that save() handles the case where the data
// directory cannot be created (here: a regular file blocks the directory path).
func TestSave_MkdirAllError(t *testing.T) {
	parent := t.TempDir()
	// Create a regular file where the data directory should be.
	blocker := filepath.Join(parent, "subdir")
	if err := os.WriteFile(blocker, []byte{}, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	errLogged := captureErrLog(t)

	// Bypass NewServerState to avoid triggering load(); set path directly so
	// filepath.Dir(s.path) == blocker (a file, not a directory).
	s := &ServerState{
		servers: make(map[string]*ServerInfo),
		path:    filepath.Join(blocker, "servers.json"),
	}
	s.servers["SRV01"] = &ServerInfo{Hostname: "SRV01", RegisteredAt: time.Now()}
	s.save()

	if !*errLogged {
		t.Error("expected ERR log from save() MkdirAll failure, got none")
	}
}

// TestSave_MarshalError verifies that save() handles the case where
// json.MarshalIndent fails (here: a NaN float64 in a server's last result).
func TestSave_MarshalError(t *testing.T) {
	dir := t.TempDir()

	errLogged := captureErrLog(t)

	s := NewServerState(dir)
	s.Register("SRV01")

	// Inject a NaN float64 into the server's last result — json.MarshalIndent
	// returns an error for NaN/Inf values, exercising the marshal-error path.
	nan := math.NaN()
	s.mu.Lock()
	s.servers["SRV01"].LastResult = &dc.CheckResult{StateDurationSeconds: &nan}
	s.mu.Unlock()

	s.save()

	if !*errLogged {
		t.Error("expected ERR log from save() marshal failure, got none")
	}
}
