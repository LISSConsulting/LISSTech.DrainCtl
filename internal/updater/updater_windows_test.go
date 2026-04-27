//go:build windows

package updater

import (
	"context"
	"net/http"
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
	s := New(dc.UpdateConfig{}, nil)
	_ = s.Start
	_ = s.Stop
}

// TestNew_DisabledIsNoOpStart verifies the FR-001 contract: with
// Enabled=false, Start launches no goroutine and Stop is immediate.
// We assert by ensuring no fetchRelease call happens during a brief
// observation window.
func TestNew_DisabledIsNoOpStart(t *testing.T) {
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

	s := New(dc.UpdateConfig{Enabled: false, Channel: dc.ChannelStable, PollInterval: dc.Duration(time.Hour)}, nil)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Give a goroutine, if one were started, ample time to call fetchRelease.
	time.Sleep(50 * time.Millisecond)
	stopDone := make(chan struct{})
	go func() { s.Stop(); close(stopDone) }()
	select {
	case <-stopDone:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Stop did not return promptly on a disabled Subsystem")
	}
	if got := fetchCalls.Load(); got != 0 {
		t.Errorf("fetchRelease called %d times on a disabled Subsystem, want 0", got)
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

	s := New(dc.UpdateConfig{Enabled: true, Channel: dc.ChannelStable, PollInterval: dc.Duration(time.Hour)}, nil)
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

	s := New(dc.UpdateConfig{Enabled: true, Channel: dc.ChannelStable, PollInterval: dc.Duration(time.Hour)}, nil)
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
	s := New(dc.UpdateConfig{Enabled: false}, nil)
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
