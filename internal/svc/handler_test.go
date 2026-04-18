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
	appendTransition(t, audit, base, "srv", dc.AllowAll, dc.PreventNewLogon)
	appendTransition(t, audit, base.Add(time.Second), "srv", dc.PreventNewLogon, dc.AllowAll)
	appendTransition(t, audit, base.Add(2*time.Second), "srv", dc.AllowAll, dc.PreventNewLogon)

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
	appendTransition(t, audit, base, "srv", dc.AllowAll, dc.PreventNewLogon)
	// No-op row (prev==next, e.g. a reconciliation where state didn't change).
	appendTransition(t, audit, base.Add(time.Second), "srv", dc.PreventNewLogon, dc.PreventNewLogon)
	appendTransition(t, audit, base.Add(2*time.Second), "srv", dc.PreventNewLogon, dc.AllowAll)

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
