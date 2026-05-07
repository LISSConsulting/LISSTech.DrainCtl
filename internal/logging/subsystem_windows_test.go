//go:build windows

package logging

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
)

// TestSubsystem_StartIsNoop — there are no goroutines to launch; Start
// returns nil and Stop drains immediately. Mirrors the contract used by
// the deferred-Stop wiring in RunService.
func TestSubsystem_StartIsNoop(t *testing.T) {
	dir := t.TempDir()
	fileLevel := &slog.LevelVar{}
	etwLevel := &slog.LevelVar{}
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	s := NewSubsystem(filepath.Join(dir, "drainctl.log"), 7, fileLevel, etwLevel)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	s.Stop()
}

// TestSubsystem_StopIsIdempotent — second Stop must be a no-op. Both
// underlying Close paths (filelog + ETW) are themselves idempotent, but
// the subsystem's stopOnce is the contract guarantee.
func TestSubsystem_StopIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	fileLevel := &slog.LevelVar{}
	etwLevel := &slog.LevelVar{}
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	s := NewSubsystem(filepath.Join(dir, "drainctl.log"), 7, fileLevel, etwLevel)
	s.Stop()
	s.Stop() // must not panic, must not double-close
}

// TestSubsystem_StopWithoutStartIsSafe — LCI contract: Stop tolerates a
// Start that never ran. Equivalent here to "constructed but never
// Start()-ed" since New does the real work.
func TestSubsystem_StopWithoutStartIsSafe(t *testing.T) {
	dir := t.TempDir()
	fileLevel := &slog.LevelVar{}
	etwLevel := &slog.LevelVar{}
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	s := NewSubsystem(filepath.Join(dir, "drainctl.log"), 7, fileLevel, etwLevel)
	s.Stop()
}

// TestSubsystem_FileOpenFailureDegradesToETWOnly — when filelog.New
// returns an error (here: unwritable directory path), the subsystem
// must still be constructible, slog.Default must be installed (ETW
// only), and Stop must remain safe.
func TestSubsystem_FileOpenFailureDegradesToETWOnly(t *testing.T) {
	fileLevel := &slog.LevelVar{}
	etwLevel := &slog.LevelVar{}
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	// A path under a non-existent directory makes filelog.New fail.
	bogus := filepath.Join(t.TempDir(), "no-such-dir", "drainctl.log")
	s := NewSubsystem(bogus, 7, fileLevel, etwLevel)
	if s.file != nil {
		t.Fatal("expected file writer to be nil after open failure")
	}
	if s.etw == nil {
		t.Fatal("expected ETW handler to be present in degraded mode")
	}
	s.Stop()
}
