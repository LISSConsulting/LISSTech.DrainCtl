//go:build windows

package updater

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// TestSubsystemImplementsLifecycle is the runtime cousin of the
// compile-time assertion in updater_windows.go. If the LCI ever drifts,
// the assertion fails the build before this test runs; this test is
// just a documented sanity check.
func TestSubsystemImplementsLifecycle(t *testing.T) {
	s := New(dc.UpdateConfig{})
	_ = s.Start
	_ = s.Stop
}

func TestTriggerCheckNowBypassesDisabledPeriodicPolicyAndSuppressesDuplicate(t *testing.T) {
	var calls atomic.Int32
	s, _ := runUpdater(t, dc.UpdateConfig{Enabled: false, PollInterval: dc.Duration(time.Hour)}, fakes{
		fetchRelease: func(_ context.Context, _ *http.Client, _, _ string) (release, error) {
			calls.Add(1)
			return release{noReleases: true}, nil
		},
	})
	defer s.Stop()

	first := s.TriggerCheckNow(context.Background(), "command-0001")
	if first.Decision != "no_stable_release" {
		t.Fatalf("first decision = %q, want no_stable_release", first.Decision)
	}
	second := s.TriggerCheckNow(context.Background(), "command-0001")
	if second.Decision != "duplicate" {
		t.Fatalf("second decision = %q, want duplicate", second.Decision)
	}
	if calls.Load() != 1 {
		t.Fatalf("fetch calls = %d, want 1", calls.Load())
	}
}

func TestPruneForceUpdateCommandsCapsDeterministically(t *testing.T) {
	now := time.Now().UTC()
	state := updateState{ForceUpdateCommands: make(map[string]time.Time)}
	for i := range forceUpdateCommandLedgerCap + 10 {
		state.ForceUpdateCommands[fmt.Sprintf("command-%03d", i)] = now.Add(time.Duration(i) * time.Second)
	}
	pruneForceUpdateCommands(&state, now.Add(time.Hour))
	if len(state.ForceUpdateCommands) != forceUpdateCommandLedgerCap-1 {
		t.Fatalf("ledger size = %d, want %d", len(state.ForceUpdateCommands), forceUpdateCommandLedgerCap-1)
	}
	if _, retained := state.ForceUpdateCommands["command-000"]; retained {
		t.Fatal("oldest command retained after deterministic cap pruning")
	}
	if _, retained := state.ForceUpdateCommands["command-011"]; !retained {
		t.Fatal("newest command was not retained after cap pruning")
	}
}

func TestDownloadToTempUsesCanonicalProgramDataDirectory(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "signed-msi-payload")
	}))
	defer server.Close()

	path, err := downloadToTemp(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("downloadToTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	if got, want := filepath.Clean(filepath.Dir(path)), filepath.Clean(dc.DefaultUpdatesDir()); got != want {
		t.Fatalf("download directory = %q, want %q", got, want)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read downloaded MSI: %v", err)
	}
	if string(body) != "signed-msi-payload" {
		t.Fatalf("downloaded payload = %q", body)
	}
}

// TestStart_DisabledLaunchesGoroutineButSkipsFetch verifies the
// post-codex-review semantics: Start always launches the poll goroutine,
// but tick() short-circuits when cfg.Enabled is false. fetchRelease
// is therefore never called even though the goroutine is alive. The
// always-launch design is what makes UpdateConfig live-reload work
// (the goroutine is parked in sleepCtx, ready to wake on a flip).
func TestStart_DisabledLaunchesGoroutineButSkipsFetch(t *testing.T) {
	var fetchCalls atomic.Int32
	prev := fetchRelease
	t.Cleanup(func() { fetchRelease = prev })
	fetchRelease = func(_ context.Context, _ *http.Client, _, _ string) (release, error) {
		fetchCalls.Add(1)
		return release{notModified: true}, nil
	}
	prevDelay := initialPollDelay
	t.Cleanup(func() { initialPollDelay = prevDelay })
	initialPollDelay = func() time.Duration { return time.Millisecond }

	s := New(dc.UpdateConfig{Enabled: false, Channel: dc.ChannelStable, PollInterval: dc.Duration(time.Hour)})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Give the goroutine ample time to tick — it should observe
	// Enabled=false and reschedule without calling fetchRelease.
	time.Sleep(50 * time.Millisecond)
	stopDone := make(chan struct{})
	go func() { s.Stop(); close(stopDone) }()
	select {
	case <-stopDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop did not return promptly on a disabled Subsystem")
	}
	if got := fetchCalls.Load(); got != 0 {
		t.Errorf("fetchRelease called %d times on a disabled Subsystem, want 0", got)
	}
}

// TestUpdateConfig_WakesPolledLoop verifies the live-reload contract:
// a Subsystem started with Enabled=false sleeps in sleepCtx; calling
// UpdateConfig with Enabled=true wakes the loop via wakeCh and the
// next tick observes the new config without waiting for the configured
// poll_interval. This is the load-bearing assertion that the
// docs-promised "edit config.json, takes effect within seconds"
// semantics actually hold end-to-end.
func TestUpdateConfig_WakesPolledLoop(t *testing.T) {
	var fetchCalls atomic.Int32
	fetched := make(chan struct{}, 1)

	prev := fetchRelease
	t.Cleanup(func() { fetchRelease = prev })
	fetchRelease = func(_ context.Context, _ *http.Client, _, _ string) (release, error) {
		fetchCalls.Add(1)
		select {
		case fetched <- struct{}{}:
		default:
		}
		return release{notModified: true}, nil
	}
	prevDelay := initialPollDelay
	t.Cleanup(func() { initialPollDelay = prevDelay })
	initialPollDelay = func() time.Duration { return time.Millisecond }

	// Start disabled with a large poll_interval. If wakeCh weren't
	// wired, this test would hang waiting for the next sleep (1 hour).
	s := New(dc.UpdateConfig{Enabled: false, Channel: dc.ChannelStable, PollInterval: dc.Duration(time.Hour)})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(s.Stop)

	// Confirm no fetch has happened yet (Enabled=false).
	time.Sleep(50 * time.Millisecond)
	if got := fetchCalls.Load(); got != 0 {
		t.Fatalf("fetchRelease called %d times pre-flip, want 0", got)
	}

	// Flip to enabled. wakeCh should interrupt the sleep within ms.
	s.UpdateConfig(dc.UpdateConfig{Enabled: true, Channel: dc.ChannelStable, PollInterval: dc.Duration(time.Hour)})

	select {
	case <-fetched:
		// expected — wakeCh fired, tick saw Enabled=true, fetchRelease called.
	case <-time.After(2 * time.Second):
		t.Fatalf("fetchRelease did not run within 2s of UpdateConfig(Enabled=true) — wakeCh wiring broken")
	}
}

func TestUpdateConfig_UnchangedPolicyDoesNotWakePollLoop(t *testing.T) {
	var fetchCalls atomic.Int32
	prevFetch := fetchRelease
	t.Cleanup(func() { fetchRelease = prevFetch })
	fetchRelease = func(_ context.Context, _ *http.Client, _, _ string) (release, error) {
		fetchCalls.Add(1)
		return release{notModified: true}, nil
	}
	prevDelay := initialPollDelay
	t.Cleanup(func() { initialPollDelay = prevDelay })
	initialPollDelay = func() time.Duration { return time.Hour }

	cfg := dc.UpdateConfig{
		Enabled:      true,
		Channel:      dc.ChannelStable,
		PollInterval: dc.Duration(time.Hour),
	}
	s := New(cfg)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(s.Stop)

	s.UpdateConfig(cfg)
	time.Sleep(100 * time.Millisecond)
	if got := fetchCalls.Load(); got != 0 {
		t.Errorf("unchanged UpdateConfig woke poll loop: fetchRelease called %d times", got)
	}
}

// TestSubsystem_StopReturnsBeforeFirstPoll asserts the LCI Stop contract
// under load: an enabled Subsystem mid-initial-delay must drain within
// the bounded window when ctx is cancelled. We use a long initial-delay
// so the goroutine is parked in sleepCtx when Stop fires.
func TestSubsystem_StopReturnsBeforeFirstPoll(t *testing.T) {
	prev := initialPollDelay
	t.Cleanup(func() { initialPollDelay = prev })
	initialPollDelay = func() time.Duration { return time.Hour }

	prevFetch := fetchRelease
	t.Cleanup(func() { fetchRelease = prevFetch })
	fetchRelease = func(_ context.Context, _ *http.Client, _, _ string) (release, error) {
		t.Error("fetchRelease should not run before initial delay completes")
		return release{}, nil
	}

	s := New(dc.UpdateConfig{Enabled: true, Channel: dc.ChannelStable, PollInterval: dc.Duration(time.Hour)})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	stopDone := make(chan struct{})
	start := time.Now()
	go func() { s.Stop(); close(stopDone) }()
	select {
	case <-stopDone:
		if d := time.Since(start); d > 200*time.Millisecond {
			t.Errorf("Stop took %v, want <200ms — sleepCtx did not respect ctx.Done", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return — Subsystem leaked a goroutine")
	}
}

// TestSubsystem_StopIsIdempotent verifies the LCI Stop contract: a
// second Stop is a no-op (not a panic, not a hang).
func TestSubsystem_StopIsIdempotent(t *testing.T) {
	prev := initialPollDelay
	t.Cleanup(func() { initialPollDelay = prev })
	initialPollDelay = func() time.Duration { return time.Hour }

	s := New(dc.UpdateConfig{Enabled: true, Channel: dc.ChannelStable, PollInterval: dc.Duration(time.Hour)})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	s.Stop()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("second Stop panicked: %v", r)
		}
	}()
	s.Stop() // must not panic, must return promptly
}

// TestSubsystem_StopToleratesDisabledStart verifies the LCI contract:
// Stop after a disabled-Start must return cleanly even though no
// goroutine was launched.
func TestSubsystem_StopToleratesDisabledStart(t *testing.T) {
	s := New(dc.UpdateConfig{Enabled: false})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Stop after disabled Start panicked: %v", r)
		}
	}()
	s.Stop()
}
