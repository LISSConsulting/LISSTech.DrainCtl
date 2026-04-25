//go:build windows

package svc

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestEmitSelfMetrics_LogsAtDebug — emit one snapshot through a captured
// debug-level slog handler and assert the structured fields are present.
// Locks the contract between selfmetrics and any operator scripts that
// grep the file log.
func TestEmitSelfMetrics_LogsAtDebug(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	emitSelfMetrics()

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
	}
	for _, k := range wantKeys {
		if !strings.Contains(out, k) {
			t.Errorf("missing structured field %q in: %s", k, out)
		}
	}
}

// TestEmitSelfMetrics_SilentAtInfo — when log level is info, debug-level
// messages MUST be filtered before any field formatting. This verifies
// the emit is cheap when operators haven't opted into debug logging.
func TestEmitSelfMetrics_SilentAtInfo(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	emitSelfMetrics()

	if buf.Len() != 0 {
		t.Errorf("expected no log output at info level; got: %s", buf.String())
	}
}

// TestReadProcessMemory_ReturnsNonZero — GetProcessMemoryInfo against
// the current process must succeed in normal test contexts. Catches a
// regression where the syscall wiring breaks (wrong DLL, wrong arg order,
// struct size mismatch).
func TestReadProcessMemory_ReturnsNonZero(t *testing.T) {
	rss, pagefile := readProcessMemory()
	if rss == 0 {
		t.Errorf("rss = 0; the test process must have a non-zero working set")
	}
	if pagefile == 0 {
		t.Errorf("pagefile = 0; the test process must have non-zero committed memory")
	}
}
