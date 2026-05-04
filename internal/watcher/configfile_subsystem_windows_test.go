//go:build windows

package watcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestConfigFileSubsystem_FiresOnWrite covers the happy path: a mtime
// change to the watched file produces exactly one tick on Events(),
// reachable within the 15 s budget that bounds both event-mode (~100 ms)
// and the 5 s poll-fallback path.
func TestConfigFileSubsystem_FiresOnWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	s := NewConfigFileSubsystem(path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	// Mtime granularity on the poll fallback is 1 s; sleep past the boundary
	// so the second write is unambiguously later than the first.
	time.Sleep(1100 * time.Millisecond)
	if err := os.WriteFile(path, []byte(`{"a":2}`), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	select {
	case <-s.Events():
	case <-time.After(15 * time.Second):
		t.Fatal("Events did not fire within 15s of config rewrite")
	}
}

// TestConfigFileSubsystem_StopDrains — ctx cancel + Stop drain the
// goroutine within a tight bound.
func TestConfigFileSubsystem_StopDrains(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	_ = os.WriteFile(path, []byte("{}"), 0o644)

	s := NewConfigFileSubsystem(path)
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

// TestConfigFileSubsystem_StopCancelsItsOwnCtx — Stop is self-contained
// per the LCI house pattern.
func TestConfigFileSubsystem_StopCancelsItsOwnCtx(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	_ = os.WriteFile(path, []byte("{}"), 0o644)

	s := NewConfigFileSubsystem(path)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not drain on a non-cancellable parent ctx")
	}
}

// TestConfigFileSubsystem_StopWithoutStartIsSafe — LCI contract.
func TestConfigFileSubsystem_StopWithoutStartIsSafe(t *testing.T) {
	s := NewConfigFileSubsystem(filepath.Join(t.TempDir(), "x.json"))
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop on never-started subsystem did not return")
	}
}

// TestConfigFileSubsystem_DoubleStopIsIdempotent — second Stop returns
// immediately.
func TestConfigFileSubsystem_DoubleStopIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	_ = os.WriteFile(path, []byte("{}"), 0o644)

	s := NewConfigFileSubsystem(path)
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
