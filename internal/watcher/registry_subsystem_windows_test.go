//go:build windows

package watcher

import (
	"context"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// TestRegistrySubsystem_StartStopDrains exercises the LCI happy path:
// Start opens the key, the watch goroutine reaches WaitForMultipleObjects,
// ctx cancel + Stop drain cleanly within a tight bound.
func TestRegistrySubsystem_StartStopDrains(t *testing.T) {
	s := NewRegistrySubsystem(dc.RegPath)
	ctx, cancel := context.WithCancel(context.Background())
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	time.Sleep(20 * time.Millisecond)

	cancel()
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not drain within 2s after ctx cancel")
	}
}

// TestRegistrySubsystem_StopCancelsItsOwnCtx — Stop is self-contained per
// the LCI house pattern. Pass context.Background() and assert Stop still
// drains.
func TestRegistrySubsystem_StopCancelsItsOwnCtx(t *testing.T) {
	s := NewRegistrySubsystem(dc.RegPath)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not drain within 2s on a non-cancellable parent ctx")
	}
}

// TestRegistrySubsystem_StopWithoutStartIsSafe — LCI contract: Stop
// tolerates a Start that never ran (e.g. earlier subsystem failed first).
func TestRegistrySubsystem_StopWithoutStartIsSafe(t *testing.T) {
	s := NewRegistrySubsystem(dc.RegPath)
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop on never-started subsystem did not return")
	}
}

// TestRegistrySubsystem_DoubleStopIsIdempotent — second Stop returns
// immediately; doesn't double-cancel or double-Wait.
func TestRegistrySubsystem_DoubleStopIsIdempotent(t *testing.T) {
	s := NewRegistrySubsystem(dc.RegPath)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	s.Stop()
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("second Stop did not return immediately")
	}
}

// TestRegistrySubsystem_StartFailsOnUnknownKey — Start surfaces the Win32
// open error synchronously so the caller can fall back to poll-only.
func TestRegistrySubsystem_StartFailsOnUnknownKey(t *testing.T) {
	s := NewRegistrySubsystem(`SOFTWARE\LISS-Technologies\does-not-exist-` + t.Name())
	err := s.Start(context.Background())
	if err == nil {
		s.Stop()
		t.Fatal("expected Start to fail on a missing key")
	}
	// Stop must still be safe after a failed Start.
	s.Stop()
}
