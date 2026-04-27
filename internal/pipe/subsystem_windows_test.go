//go:build windows

package pipe

import (
	"context"
	"testing"
	"time"
)

// TestSubsystem_StartStopDrains exercises the LCI contract on the
// happy path: Start returns nil, the accept loop is alive, ctx cancel
// + Stop drain cleanly within a tight bound.
func TestSubsystem_StartStopDrains(t *testing.T) {
	t.Cleanup(uniqueTestPipeName(t))

	s := New(&mockHandler{})
	ctx, cancel := context.WithCancel(context.Background())
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Let the accept loop reach its blocking ConnectNamedPipe call.
	time.Sleep(50 * time.Millisecond)

	cancel()
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not drain within 2s after ctx cancel")
	}
}

// TestSubsystem_StopCancelsItsOwnCtx — Stop is self-contained per the
// LCI house pattern. Pass context.Background() (non-cancellable) and
// assert Stop still drains.
func TestSubsystem_StopCancelsItsOwnCtx(t *testing.T) {
	t.Cleanup(uniqueTestPipeName(t))

	s := New(&mockHandler{})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not drain within 2s on a non-cancellable parent ctx")
	}
}

// TestSubsystem_StopWithoutStartIsSafe — LCI contract: Stop tolerates
// a Start that never ran (e.g. earlier subsystem failed first). No
// panic, no hang.
func TestSubsystem_StopWithoutStartIsSafe(t *testing.T) {
	s := New(&mockHandler{})
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop on never-started subsystem did not return")
	}
}

// TestSubsystem_DoubleStopIsIdempotent — second Stop returns
// immediately, doesn't double-close cancel or double-Wait.
func TestSubsystem_DoubleStopIsIdempotent(t *testing.T) {
	t.Cleanup(uniqueTestPipeName(t))

	s := New(&mockHandler{})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	s.Stop()
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("second Stop did not return immediately")
	}
}

// uniqueTestPipeName swaps PipeName to a per-test value so concurrent
// tests don't collide on the global drainctl pipe; returns a cleanup
// closure that restores the prior value.
func uniqueTestPipeName(t *testing.T) func() {
	t.Helper()
	prev := PipeName
	PipeName = `\\.\pipe\drainctl-test-` + t.Name()
	return func() { PipeName = prev }
}
