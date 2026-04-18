//go:build windows

package svc

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/perfmon"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// newHandlerStore opens a telemetry.DB backed by a fresh drainctl.db in a
// temp dir and returns a serviceHandler bound to its AuditStore. Caller
// must invoke the returned cleanup.
func newHandlerStore(t *testing.T) (*serviceHandler, *telemetry.AuditStore, func()) {
	t.Helper()
	dir := t.TempDir()
	db, err := telemetry.Open(dir)
	if err != nil {
		t.Fatalf("telemetry.Open: %v", err)
	}
	ctx := context.Background()
	audit, err := telemetry.NewAuditStore(ctx, db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("NewAuditStore: %v", err)
	}
	h := &serviceHandler{audit: audit}
	cfg := dc.DefaultConfig().ToServiceConfig()
	h.cfg.Store(&cfg)
	cleanup := func() {
		_ = audit.Close()
		_ = db.Close()
	}
	return h, audit, cleanup
}

// appendTransition is a test helper that writes a drain-mode transition audit
// row through the AuditStore, mirroring what svcRunCheck writes in production.
func appendTransition(t *testing.T, audit *telemetry.AuditStore, ts time.Time, host string, prev, next dc.DrainMode) {
	t.Helper()
	rec := telemetry.AuditRecord{
		Ts:        ts,
		Host:      host,
		PrevState: int(prev),
		NewState:  int(next),
	}
	if err := audit.Append(context.Background(), rec); err != nil {
		t.Fatalf("audit.Append: %v", err)
	}
}

// ── HandleHistory ─────────────────────────────────────────────────────────────

// TestHandleHistory_AllRecords verifies that changesOnly=false returns every
// audit row the store holds.
func TestHandleHistory_AllRecords(t *testing.T) {
	h, audit, cleanup := newHandlerStore(t)
	defer cleanup()

	base := time.Now().UTC().Truncate(time.Millisecond)
	appendTransition(t, audit, base, "srv", dc.AllowAll, dc.DrainPersistent)
	appendTransition(t, audit, base.Add(time.Second), "srv", dc.DrainPersistent, dc.AllowAll)
	appendTransition(t, audit, base.Add(2*time.Second), "srv", dc.AllowAll, dc.DrainPersistent)

	got := h.HandleHistory(0, false)
	if len(got) != 3 {
		t.Errorf("HandleHistory(0, false) returned %d records, want 3", len(got))
	}
}

// TestHandleHistory_ChangesOnly verifies the filter passes through to the
// store layer and excludes no-op rows (prev_state == new_state).
func TestHandleHistory_ChangesOnly(t *testing.T) {
	h, audit, cleanup := newHandlerStore(t)
	defer cleanup()

	base := time.Now().UTC().Truncate(time.Millisecond)
	appendTransition(t, audit, base, "srv", dc.AllowAll, dc.DrainPersistent)
	// No-op row (prev==next, e.g. a reconciliation where state didn't change).
	appendTransition(t, audit, base.Add(time.Second), "srv", dc.DrainPersistent, dc.DrainPersistent)
	appendTransition(t, audit, base.Add(2*time.Second), "srv", dc.DrainPersistent, dc.AllowAll)

	got := h.HandleHistory(0, true)
	if len(got) != 2 {
		t.Errorf("HandleHistory(0, true) returned %d records, want 2 (transitions only)", len(got))
	}
	for _, r := range got {
		if !r.Changed {
			t.Errorf("changesOnly=true returned record with Changed=false: %+v", r)
		}
	}
}

// TestHandleHistory_NilAuditReturnsNil verifies the pipe handler tolerates a
// construction path where the audit store is not wired (degraded mode).
func TestHandleHistory_NilAuditReturnsNil(t *testing.T) {
	h := &serviceHandler{}
	if got := h.HandleHistory(10, false); got != nil {
		t.Errorf("HandleHistory on nil-audit handler returned %v, want nil", got)
	}
}

// TestHandleHistory_EnrichesFromMetrics verifies that audit rows returned
// through the pipe carry CPU / input-delay / session counters joined from
// metrics_raw. Covers the FR-026 regression where `drainctl history`
// always rendered `-` in those columns because the audit table schema
// narrowed to transition metadata only.
func TestHandleHistory_EnrichesFromMetrics(t *testing.T) {
	dir := t.TempDir()
	db, err := telemetry.Open(dir)
	if err != nil {
		t.Fatalf("telemetry.Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	audit, err := telemetry.NewAuditStore(ctx, db)
	if err != nil {
		t.Fatalf("NewAuditStore: %v", err)
	}
	defer func() { _ = audit.Close() }()
	ms := telemetry.NewMetricsStore(db)

	h := &serviceHandler{audit: audit, metrics: ms}
	cfg := dc.DefaultConfig().ToServiceConfig()
	h.cfg.Store(&cfg)

	transitionTs := time.Now().UTC().Truncate(time.Millisecond)
	host := "SRV01"
	appendTransition(t, audit, transitionTs, host, dc.AllowAll, dc.DrainPersistent)

	// Perf sample 5 s before the transition — inside the 2-minute tolerance.
	if err := ms.Append(ctx, []telemetry.Sample{
		{Ts: transitionTs.Add(-5 * time.Second), Host: host, Counter: "cpu_pct", Value: 37},
		{Ts: transitionTs.Add(-5 * time.Second), Host: host, Counter: "input_delay_max_ms", Value: 210},
		{Ts: transitionTs.Add(-5 * time.Second), Host: host, Counter: "sessions_total", Value: 8},
		{Ts: transitionTs.Add(-5 * time.Second), Host: host, Counter: "sessions_active", Value: 6},
	}); err != nil {
		t.Fatalf("ms.Append: %v", err)
	}

	got := h.HandleHistory(10, false)
	if len(got) != 1 {
		t.Fatalf("HandleHistory returned %d records, want 1", len(got))
	}
	r := got[0]
	if r.CPUPct != 37 {
		t.Errorf("CPUPct = %v, want 37 (joined from metrics_raw)", r.CPUPct)
	}
	if r.InputDelayMax != 210 {
		t.Errorf("InputDelayMax = %v, want 210", r.InputDelayMax)
	}
	if r.TotalSessions != 8 {
		t.Errorf("TotalSessions = %d, want 8", r.TotalSessions)
	}
	if r.ActiveSessions != 6 {
		t.Errorf("ActiveSessions = %d, want 6", r.ActiveSessions)
	}
}

// TestHandleHistory_NilMetricsStillReturnsAudit verifies that enrichment is
// best-effort — a handler constructed without a MetricsStore still returns
// audit rows, just with empty perf fields (zero-valued).
func TestHandleHistory_NilMetricsStillReturnsAudit(t *testing.T) {
	h, audit, cleanup := newHandlerStore(t)
	defer cleanup()
	// h.metrics is intentionally nil in this helper.

	base := time.Now().UTC().Truncate(time.Millisecond)
	appendTransition(t, audit, base, "srv", dc.AllowAll, dc.DrainPersistent)

	got := h.HandleHistory(10, false)
	if len(got) != 1 {
		t.Fatalf("HandleHistory returned %d records, want 1", len(got))
	}
	if got[0].CPUPct != 0 || got[0].InputDelayMax != 0 {
		t.Errorf("expected zero-valued perf fields with nil metrics store, got %+v", got[0])
	}
}

// ── syncPerfCollector ────────────────────────────────────────────────────────

func TestSyncPerfCollector_NoChangeIsNoop(t *testing.T) {
	cfg := dc.PerformanceConfig{Enabled: true, CPUWarnPct: 70}
	var collector *perfmon.Collector
	var triggerState *perfmon.PerfTriggerState
	var lastPerf atomic.Pointer[dc.PerfSnapshot]

	// Store a snapshot to verify it is NOT cleared on no-op.
	snap := &dc.PerfSnapshot{CPUPct: 42}
	lastPerf.Store(snap)

	changed := syncPerfCollector(cfg, cfg, &collector, &triggerState, &lastPerf)
	if changed {
		t.Error("syncPerfCollector returned changed=true for identical configs")
	}
	if lastPerf.Load() != snap {
		t.Error("lastPerf was cleared despite no config change")
	}
}

func TestSyncPerfCollector_DisableClearsLastPerf(t *testing.T) {
	oldCfg := dc.PerformanceConfig{Enabled: true, CPUWarnPct: 70}
	newCfg := dc.PerformanceConfig{Enabled: false}
	var collector *perfmon.Collector
	var triggerState *perfmon.PerfTriggerState
	var lastPerf atomic.Pointer[dc.PerfSnapshot]

	// Simulate cached snapshot from when perfmon was enabled.
	lastPerf.Store(&dc.PerfSnapshot{CPUPct: 42})

	changed := syncPerfCollector(oldCfg, newCfg, &collector, &triggerState, &lastPerf)
	if !changed {
		t.Error("syncPerfCollector returned changed=false when disabling perfmon")
	}
	if lastPerf.Load() != nil {
		t.Error("lastPerf not cleared after disabling perfmon")
	}
}

func TestSyncPerfCollector_ThresholdChangeClearsLastPerf(t *testing.T) {
	oldCfg := dc.PerformanceConfig{Enabled: true, CPUWarnPct: 70}
	newCfg := dc.PerformanceConfig{Enabled: true, CPUWarnPct: 80}
	var collector *perfmon.Collector
	var triggerState *perfmon.PerfTriggerState
	var lastPerf atomic.Pointer[dc.PerfSnapshot]

	lastPerf.Store(&dc.PerfSnapshot{CPUPct: 42})

	changed := syncPerfCollector(oldCfg, newCfg, &collector, &triggerState, &lastPerf)
	if !changed {
		t.Error("syncPerfCollector returned changed=false when thresholds changed")
	}
	if lastPerf.Load() != nil {
		t.Error("lastPerf not cleared after threshold change")
	}
}
