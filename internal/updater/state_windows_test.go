//go:build windows

package updater

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// withTempStatePath redirects updateStatePath to t.TempDir() for the
// test's duration. Used by tests that need to exercise the real
// load/save filesystem path against an isolated dir, NOT the in-memory
// fakes installed by runUpdater.
func withTempStatePath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "update-state.json")
	prev := updateStatePath
	t.Cleanup(func() { updateStatePath = prev })
	updateStatePath = func() string { return path }
	return path
}

// withRealStateFns restores the production load/save implementations
// for tests that need to exercise the real atomic-write code path
// (concurrent writes, corrupt-file handling). Pairs with
// withTempStatePath.
func withRealStateFns(t *testing.T) {
	t.Helper()
	prevLoad := loadUpdateState
	prevSave := saveUpdateState
	t.Cleanup(func() { loadUpdateState = prevLoad; saveUpdateState = prevSave })
	loadUpdateState = loadUpdateStateImpl
	saveUpdateState = saveUpdateStateImpl
}

// TestStartSeedsHighestSeenFromVersion proves Subsystem.Start records
// dc.Version when the state file is empty, defending against a
// fresh-install rollback.
func TestStartSeedsHighestSeenFromVersion(t *testing.T) {
	withTempStatePath(t)
	withRealStateFns(t)
	pinVersion(t, "26.6.50")

	// Independently call seed (Start would call this).
	seedHighestSeenFromVersion()

	state, err := loadUpdateStateImpl()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if state.HighestSeenVersion != "26.6.50" {
		t.Errorf("HighestSeenVersion = %q, want %q after seed", state.HighestSeenVersion, "26.6.50")
	}
}

// TestUpdateState_RejectsReplayBelowHighestSeen proves the gate fires
// when remote < highSeen. Install seam never runs.
func TestUpdateState_RejectsReplayBelowHighestSeen(t *testing.T) {
	pinVersion(t, "26.6.20")
	cfg := dc.UpdateConfig{Enabled: true, Channel: dc.ChannelStable, PollInterval: dc.Duration(time.Hour)}

	var spawned atomic.Int32
	f := fakes{
		initialState: updateState{HighestSeenVersion: "26.6.50"},
		decodeKeys:   func() ([]ed25519.PublicKey, error) { return nil, nil },
		fetchRelease: func(_ context.Context, _ *http.Client, _, _ string) (release, error) {
			// Attacker replays a signed-but-stale older release.
			return release{tag: "26.6.10", assetURL: "https://test/msi", etag: "e"}, nil
		},
		downloadMSI: func(_ context.Context, _ *http.Client, _ string) (string, error) {
			return fakeMSI(t), nil
		},
		verifyMSI: func(string) error { return nil },
		spawnMSI: func(string) error {
			spawned.Add(1)
			return nil
		},
	}
	s, shutdownFired := runUpdater(t, cfg, f)
	t.Cleanup(s.Stop)

	select {
	case <-shutdownFired:
		t.Fatal("install fired despite replay refusal")
	case <-time.After(150 * time.Millisecond):
		// Expected: gate refused, no install.
	}
	if spawned.Load() != 0 {
		t.Errorf("spawnMSI called %d times despite replay refusal, want 0", spawned.Load())
	}
}

// TestUpdateState_AcceptsForwardProgress proves a remote strictly
// greater than highSeen is accepted, the install path runs, and the
// new highest-seen is persisted.
func TestUpdateState_AcceptsForwardProgress(t *testing.T) {
	pinVersion(t, "26.6.20")
	cfg := dc.UpdateConfig{Enabled: true, Channel: dc.ChannelStable, PollInterval: dc.Duration(time.Hour)}

	var spawned atomic.Int32
	f := fakes{
		initialState: updateState{HighestSeenVersion: "26.6.20"},
		decodeKeys:   func() ([]ed25519.PublicKey, error) { return nil, nil },
		fetchRelease: func(_ context.Context, _ *http.Client, _, _ string) (release, error) {
			return release{tag: "26.6.50", assetURL: "https://test/msi", etag: "e"}, nil
		},
		downloadMSI: func(_ context.Context, _ *http.Client, _ string) (string, error) {
			return fakeMSI(t), nil
		},
		verifyMSI: func(string) error { return nil },
		spawnMSI: func(string) error {
			spawned.Add(1)
			return nil
		},
	}
	s, shutdownFired := runUpdater(t, cfg, f)
	t.Cleanup(s.Stop)

	select {
	case <-shutdownFired:
	case <-time.After(2 * time.Second):
		t.Fatal("install never fired on forward-progress remote")
	}
	if spawned.Load() == 0 {
		t.Fatal("spawnMSI never called despite forward-progress remote")
	}

	final, _ := loadUpdateState()
	if final.HighestSeenVersion != "26.6.50" {
		t.Errorf("HighestSeenVersion = %q, want %q after install", final.HighestSeenVersion, "26.6.50")
	}
}

// TestUpdateState_DoesNotPersistOnSpawnFailure proves a failed spawn
// does not advance the highest-seen pin.
func TestUpdateState_DoesNotPersistOnSpawnFailure(t *testing.T) {
	pinVersion(t, "26.6.20")
	cfg := dc.UpdateConfig{Enabled: true, Channel: dc.ChannelStable, PollInterval: dc.Duration(time.Hour)}

	f := fakes{
		initialState: updateState{HighestSeenVersion: "26.6.20"},
		decodeKeys:   func() ([]ed25519.PublicKey, error) { return nil, nil },
		fetchRelease: func(_ context.Context, _ *http.Client, _, _ string) (release, error) {
			return release{tag: "26.6.50", assetURL: "https://test/msi", etag: "e"}, nil
		},
		downloadMSI: func(_ context.Context, _ *http.Client, _ string) (string, error) {
			return fakeMSI(t), nil
		},
		verifyMSI: func(string) error { return nil },
		spawnMSI:  func(string) error { return errors.New("spawn boom") },
	}
	s, _ := runUpdater(t, cfg, f)
	t.Cleanup(s.Stop)

	time.Sleep(150 * time.Millisecond)

	final, _ := loadUpdateState()
	if final.HighestSeenVersion != "26.6.20" {
		t.Errorf("HighestSeenVersion advanced to %q despite spawn failure; want unchanged 26.6.20", final.HighestSeenVersion)
	}
}

// TestUpdateState_VersionComparisonHandlesPrefixForms proves the gate
// uses parsed comparison so "v26.6.50" persisted equals "26.6.50"
// observed remote (no false-positive replay refusal).
func TestUpdateState_VersionComparisonHandlesPrefixForms(t *testing.T) {
	pinVersion(t, "26.6.20")
	cfg := dc.UpdateConfig{Enabled: true, Channel: dc.ChannelStable, PollInterval: dc.Duration(time.Hour)}

	var spawned atomic.Int32
	f := fakes{
		// Persisted highest uses "v" prefix. Remote omits it. The gate
		// must parse both before comparing, otherwise a no-op re-poll
		// would be falsely refused as replay.
		initialState: updateState{HighestSeenVersion: "v26.6.50"},
		decodeKeys:   func() ([]ed25519.PublicKey, error) { return nil, nil },
		fetchRelease: func(_ context.Context, _ *http.Client, _, _ string) (release, error) {
			return release{tag: "26.6.50", assetURL: "https://test/msi", etag: "e"}, nil
		},
		downloadMSI: func(_ context.Context, _ *http.Client, _ string) (string, error) {
			return fakeMSI(t), nil
		},
		verifyMSI: func(string) error { return nil },
		spawnMSI: func(string) error {
			spawned.Add(1)
			return nil
		},
	}
	s, _ := runUpdater(t, cfg, f)
	t.Cleanup(s.Stop)

	// remote.equals(highSeen) → carve-out passes through. current is
	// 26.6.20 < remote 26.6.50, so install would proceed if the
	// gate didn't refuse. The point is the gate must NOT flag
	// "v26.6.50" vs "26.6.50" as a replay (false positive).
	time.Sleep(300 * time.Millisecond)
	if spawned.Load() == 0 {
		t.Errorf("spawnMSI never called; gate likely flagged 'v26.6.50' as different from '26.6.50'")
	}
}

// TestUpdateState_CorruptFileFailsOpen — load returns zero value (no
// error) after writing garbage to disk; next save replaces it. Bricking
// the updater on a corrupt state file would defeat the whole purpose.
func TestUpdateState_CorruptFileFailsOpen(t *testing.T) {
	path := withTempStatePath(t)
	withRealStateFns(t)

	if err := os.WriteFile(path, []byte("not json!!!"), 0o600); err != nil {
		t.Fatal(err)
	}

	state, err := loadUpdateState()
	if err != nil {
		t.Fatalf("load returned err on corrupt file (must fail-open): %v", err)
	}
	if state.HighestSeenVersion != "" {
		t.Errorf("expected zero HighestSeenVersion after corrupt load; got %q", state.HighestSeenVersion)
	}

	// Next save must overwrite the garbage with valid JSON.
	if err := saveUpdateState(updateState{HighestSeenVersion: "26.6.50"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s updateState
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("rewritten file not valid JSON: %v\ncontent: %s", err, body)
	}
	if s.HighestSeenVersion != "26.6.50" {
		t.Errorf("rewritten HighestSeenVersion = %q, want 26.6.50", s.HighestSeenVersion)
	}
}

// TestUpdateState_ConcurrentSaves_NoTornWrite — N goroutines hammering
// the named-mutex+MoveFileEx atomic-write path. The post-condition is
// the JSON parses cleanly and matches one of the inputs (no torn or
// partial bytes).
func TestUpdateState_ConcurrentSaves_NoTornWrite(t *testing.T) {
	path := withTempStatePath(t)
	withRealStateFns(t)

	const N = 10
	inputs := make([]string, N)
	var wg sync.WaitGroup
	for i := range N {
		v := "26.6." + itoa(i+1)
		inputs[i] = v
		wg.Add(1)
		go func(version string) {
			defer wg.Done()
			_ = saveUpdateState(updateState{HighestSeenVersion: version})
		}(v)
	}
	wg.Wait()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("post-save read: %v", err)
	}
	var s updateState
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("torn write — JSON parse failed: %v\ncontent: %s", err, body)
	}
	matched := false
	for _, want := range inputs {
		if s.HighestSeenVersion == want {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("HighestSeenVersion = %q does not match any of the %d inputs", s.HighestSeenVersion, N)
	}
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}
