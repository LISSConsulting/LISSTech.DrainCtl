//go:build windows

package selfmetrics

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestEmit_LogsAtDebug locks the contract between selfmetrics and any
// operator scripts that grep the file log: required keys must be present
// in the structured output.
func TestEmit_LogsAtDebug(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	emit()

	out := buf.String()
	if !strings.Contains(out, "msg=selfmetrics") {
		t.Fatalf("missing selfmetrics msg; got: %s", out)
	}
	wantKeys := []string{
		"heap_alloc_mb=",
		"heap_inuse_mb=",
		"heap_objects=",
		"stack_inuse_mb=",
		"goroutines=",
		"num_gc=",
		"gc_cpu_pct=",
		"rss_mb=",
		"pagefile_mb=",
		"sspi_live_contexts=",
		"sspi_pending_contexts=",
	}
	for _, k := range wantKeys {
		if !strings.Contains(out, k) {
			t.Errorf("missing structured field %q in: %s", k, out)
		}
	}
}

// TestEmit_SilentAtInfo verifies the emit is cheap when operators
// haven't opted into debug logging — debug-level messages are filtered
// before any field formatting.
func TestEmit_SilentAtInfo(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	emit()

	if buf.Len() != 0 {
		t.Errorf("expected no log output at info level; got: %s", buf.String())
	}
}

// TestReadProcessMemory_ReturnsNonZero — GetProcessMemoryInfo against
// the current process must succeed in normal test contexts. Catches a
// regression where the syscall wiring breaks (wrong DLL, wrong arg
// order, struct size mismatch).
func TestReadProcessMemory_ReturnsNonZero(t *testing.T) {
	rss, pagefile := readProcessMemory()
	if rss == 0 {
		t.Errorf("rss = 0; the test process must have a non-zero working set")
	}
	if pagefile == 0 {
		t.Errorf("pagefile = 0; the test process must have non-zero committed memory")
	}
}

// TestSubsystem_StartStopDrains exercises the LCI contract: Start
// returns nil, the goroutine emits at least one record, ctx cancel +
// Stop drain it cleanly without leaking.
func TestSubsystem_StartStopDrains(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithCancel(context.Background())
	s := New(20 * time.Millisecond)
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Wait long enough for the initial emit + at least one tick.
	time.Sleep(50 * time.Millisecond)
	cancel()

	done := make(chan struct{})
	go func() {
		s.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not drain within 2s after ctx cancel")
	}

	if !strings.Contains(buf.String(), "msg=selfmetrics") {
		t.Errorf("expected at least one selfmetrics emission; got: %s", buf.String())
	}
}

// TestStopWithoutStartIsSafe — LCI contract: Stop tolerates a Start that
// never ran (e.g., earlier subsystem failed first). No panic, no hang.
func TestStopWithoutStartIsSafe(t *testing.T) {
	s := New(0)
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop on never-started subsystem did not return")
	}
}
