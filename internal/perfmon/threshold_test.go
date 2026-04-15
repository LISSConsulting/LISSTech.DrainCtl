//go:build windows

package perfmon

import (
	"testing"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

func defaultPerfCfg() dc.PerformanceConfig {
	return dc.PerformanceConfig{
		Enabled:                 true,
		CollectPerSession:       true,
		SampleIntervalSec:       dc.DefaultSampleInterval,
		LoadAlertDelaySec:       dc.DefaultLoadAlertDelaySec,
		InputDelayAlertDelaySec: dc.DefaultInputDelayAlertDelaySec,
	}
}

// Default consecutive poll counts derived from duration / interval.
var (
	defaultLoadPolls  = ceilDiv(dc.DefaultLoadAlertDelaySec, dc.DefaultSampleInterval)       // 2
	defaultDelayPolls = ceilDiv(dc.DefaultInputDelayAlertDelaySec, dc.DefaultSampleInterval) // 3
)

func hasTrigger(triggers []dc.Trigger, want dc.Trigger) bool {
	for _, t := range triggers {
		if t == want {
			return true
		}
	}
	return false
}

// ── CPU thresholds ──────────────────────────────────────────────────────────

func TestCPUWarning_RequiresConsecutivePolls(t *testing.T) {
	cfg := defaultPerfCfg()
	state := &PerfTriggerState{}

	snap := &dc.PerfSnapshot{CPUPct: 75, MemTotalMB: 16000, MemAvailMB: 10000}
	for i := 0; i < defaultLoadPolls-1; i++ {
		triggers := EvaluateThresholds(snap, cfg, state)
		if hasTrigger(triggers, dc.TriggerCPUWarning) {
			t.Errorf("CPU warning should not fire on poll %d (threshold=%d)", i+1, defaultLoadPolls)
		}
	}

	triggers := EvaluateThresholds(snap, cfg, state)
	if !hasTrigger(triggers, dc.TriggerCPUWarning) {
		t.Errorf("CPU warning should fire on poll %d", defaultLoadPolls)
	}
}

func TestCPUWarning_ResetsOnDrop(t *testing.T) {
	cfg := defaultPerfCfg()
	state := &PerfTriggerState{}

	snap := &dc.PerfSnapshot{CPUPct: 75, MemTotalMB: 16000, MemAvailMB: 10000}
	EvaluateThresholds(snap, cfg, state)

	snap.CPUPct = 50
	EvaluateThresholds(snap, cfg, state)

	snap.CPUPct = 75
	triggers := EvaluateThresholds(snap, cfg, state)
	if hasTrigger(triggers, dc.TriggerCPUWarning) {
		t.Error("CPU warning should not fire after reset")
	}
}

func TestCPUCritical_OverridesWarning(t *testing.T) {
	cfg := defaultPerfCfg()
	state := &PerfTriggerState{}

	snap := &dc.PerfSnapshot{CPUPct: 90, MemTotalMB: 16000, MemAvailMB: 10000}
	for i := 0; i < defaultLoadPolls-1; i++ {
		EvaluateThresholds(snap, cfg, state)
	}
	triggers := EvaluateThresholds(snap, cfg, state)

	if hasTrigger(triggers, dc.TriggerCPUWarning) {
		t.Error("CPU warning should not fire when critical is active")
	}
	if !hasTrigger(triggers, dc.TriggerCPUCritical) {
		t.Error("CPU critical should fire")
	}
}

// ── Memory thresholds ───────────────────────────────────────────────────────

func TestMemoryWarning_RequiresConsecutivePolls(t *testing.T) {
	cfg := defaultPerfCfg()
	state := &PerfTriggerState{}

	snap := &dc.PerfSnapshot{MemTotalMB: 16000, MemAvailMB: 2400}
	for i := 0; i < defaultLoadPolls-1; i++ {
		triggers := EvaluateThresholds(snap, cfg, state)
		if hasTrigger(triggers, dc.TriggerMemoryWarning) {
			t.Errorf("Memory warning should not fire on poll %d (threshold=%d)", i+1, defaultLoadPolls)
		}
	}

	triggers := EvaluateThresholds(snap, cfg, state)
	if !hasTrigger(triggers, dc.TriggerMemoryWarning) {
		t.Errorf("Memory warning should fire on poll %d", defaultLoadPolls)
	}
}

func TestMemoryCritical_FivePercentFree(t *testing.T) {
	cfg := defaultPerfCfg()
	state := &PerfTriggerState{}

	snap := &dc.PerfSnapshot{MemTotalMB: 16000, MemAvailMB: 800}
	for i := 0; i < defaultLoadPolls-1; i++ {
		EvaluateThresholds(snap, cfg, state)
	}
	triggers := EvaluateThresholds(snap, cfg, state)

	if !hasTrigger(triggers, dc.TriggerMemoryCritical) {
		t.Error("Memory critical should fire at 5% free")
	}
}

func TestMemory_HealthyDoesNotFire(t *testing.T) {
	cfg := defaultPerfCfg()
	state := &PerfTriggerState{}

	snap := &dc.PerfSnapshot{MemTotalMB: 16000, MemAvailMB: 8000}
	EvaluateThresholds(snap, cfg, state)
	triggers := EvaluateThresholds(snap, cfg, state)

	if hasTrigger(triggers, dc.TriggerMemoryWarning) || hasTrigger(triggers, dc.TriggerMemoryCritical) {
		t.Error("No memory trigger should fire at 50% free")
	}
}

// ── Input delay thresholds ──────────────────────────────────────────────────

func TestInputDelayWarning_RequiresConsecutivePolls(t *testing.T) {
	cfg := defaultPerfCfg()
	state := &PerfTriggerState{}

	snap := &dc.PerfSnapshot{InputDelayP95: 60, MemTotalMB: 16000, MemAvailMB: 10000}

	// Should NOT fire before reaching defaultDelayPolls.
	for i := 0; i < defaultDelayPolls-1; i++ {
		triggers := EvaluateThresholds(snap, cfg, state)
		if hasTrigger(triggers, dc.TriggerInputDelayWarning) {
			t.Errorf("Input delay warning should not fire on poll %d (threshold=%d)", i+1, defaultDelayPolls)
		}
	}

	// Should fire on the Nth consecutive poll.
	triggers := EvaluateThresholds(snap, cfg, state)
	if !hasTrigger(triggers, dc.TriggerInputDelayWarning) {
		t.Errorf("Input delay warning should fire on poll %d", defaultDelayPolls)
	}
}

func TestInputDelayWarning_ResetsOnDrop(t *testing.T) {
	cfg := defaultPerfCfg()
	state := &PerfTriggerState{}

	snap := &dc.PerfSnapshot{InputDelayP95: 60, MemTotalMB: 16000, MemAvailMB: 10000}
	EvaluateThresholds(snap, cfg, state) // count=1

	snap.InputDelayP95 = 10              // drop below threshold
	EvaluateThresholds(snap, cfg, state) // count reset to 0

	snap.InputDelayP95 = 60
	triggers := EvaluateThresholds(snap, cfg, state) // count=1 again
	if hasTrigger(triggers, dc.TriggerInputDelayWarning) {
		t.Error("Input delay warning should not fire after reset")
	}
}

func TestInputDelayCritical_OverridesWarning(t *testing.T) {
	cfg := defaultPerfCfg()
	state := &PerfTriggerState{}

	snap := &dc.PerfSnapshot{InputDelayP95: 120, MemTotalMB: 16000, MemAvailMB: 10000}
	for i := 0; i < defaultDelayPolls-1; i++ {
		EvaluateThresholds(snap, cfg, state)
	}
	triggers := EvaluateThresholds(snap, cfg, state) // Nth poll — fires

	if hasTrigger(triggers, dc.TriggerInputDelayWarning) {
		t.Error("Input delay warning should not fire when critical is active")
	}
	if !hasTrigger(triggers, dc.TriggerInputDelayCritical) {
		t.Error("Input delay critical should fire")
	}
}

func TestInputDelay_BelowThreshold(t *testing.T) {
	cfg := defaultPerfCfg()
	state := &PerfTriggerState{}

	snap := &dc.PerfSnapshot{InputDelayP95: 30, MemTotalMB: 16000, MemAvailMB: 10000}
	EvaluateThresholds(snap, cfg, state)
	triggers := EvaluateThresholds(snap, cfg, state)

	if hasTrigger(triggers, dc.TriggerInputDelayWarning) || hasTrigger(triggers, dc.TriggerInputDelayCritical) {
		t.Error("No input delay trigger should fire below threshold")
	}
}

func TestInputDelay_P50Evaluation(t *testing.T) {
	cfg := defaultPerfCfg()
	cfg.InputDelayPercentile = "p50"
	state := &PerfTriggerState{}

	snap := &dc.PerfSnapshot{
		InputDelayP50: 60, // above warn threshold
		InputDelayP95: 30, // below warn threshold
		MemTotalMB:    16000,
		MemAvailMB:    10000,
	}
	for i := 0; i < defaultDelayPolls; i++ {
		EvaluateThresholds(snap, cfg, state)
	}
	triggers := EvaluateThresholds(snap, cfg, state)

	if !hasTrigger(triggers, dc.TriggerInputDelayWarning) {
		t.Error("Input delay warning should fire based on P50 when configured")
	}
}

func TestInputDelay_P95IsDefault(t *testing.T) {
	cfg := defaultPerfCfg()
	// InputDelayPercentile is "" (zero value) — should default to P95.
	state := &PerfTriggerState{}

	snap := &dc.PerfSnapshot{
		InputDelayP50: 60, // above threshold
		InputDelayP95: 30, // below threshold
		MemTotalMB:    16000,
		MemAvailMB:    10000,
	}
	for i := 0; i < defaultDelayPolls; i++ {
		EvaluateThresholds(snap, cfg, state)
	}
	triggers := EvaluateThresholds(snap, cfg, state)

	if hasTrigger(triggers, dc.TriggerInputDelayWarning) {
		t.Error("Input delay warning should evaluate P95 by default, not P50")
	}
}

// ── Disabled thresholds ─────────────────────────────────────────────────────

func TestDisabledThreshold_NegativeOne(t *testing.T) {
	cfg := defaultPerfCfg()
	cfg.CPUWarnPct = -1
	cfg.CPUCritPct = -1
	state := &PerfTriggerState{}

	snap := &dc.PerfSnapshot{CPUPct: 99, MemTotalMB: 16000, MemAvailMB: 10000}
	for i := 0; i < defaultLoadPolls; i++ {
		EvaluateThresholds(snap, cfg, state)
	}
	triggers := EvaluateThresholds(snap, cfg, state)

	if hasTrigger(triggers, dc.TriggerCPUWarning) || hasTrigger(triggers, dc.TriggerCPUCritical) {
		t.Error("CPU triggers should not fire when disabled (-1)")
	}
}

// ── Custom thresholds ───────────────────────────────────────────────────────

func TestCustomThresholds(t *testing.T) {
	cfg := defaultPerfCfg()
	cfg.CPUWarnPct = 50
	cfg.CPUCritPct = 60
	state := &PerfTriggerState{}

	snap := &dc.PerfSnapshot{CPUPct: 55, MemTotalMB: 16000, MemAvailMB: 10000}
	for i := 0; i < defaultLoadPolls-1; i++ {
		EvaluateThresholds(snap, cfg, state)
	}
	triggers := EvaluateThresholds(snap, cfg, state)

	if !hasTrigger(triggers, dc.TriggerCPUWarning) {
		t.Error("CPU warning should fire with custom 50% threshold at 55% CPU")
	}
}

// ── Nil snapshot ────────────────────────────────────────────────────────────

func TestEvaluateThresholds_NilSnapshot(t *testing.T) {
	cfg := defaultPerfCfg()
	state := &PerfTriggerState{}

	triggers := EvaluateThresholds(nil, cfg, state)
	if len(triggers) != 0 {
		t.Error("nil snapshot should return no triggers")
	}
}

// ── TriggerMessage ──────────────────────────────────────────────────────────

func TestTriggerMessage_CPU(t *testing.T) {
	cfg := defaultPerfCfg()
	snap := &dc.PerfSnapshot{CPUPct: 78}
	msg := TriggerMessage(dc.TriggerCPUWarning, snap, cfg)
	if msg == "" {
		t.Error("TriggerMessage should return non-empty string")
	}
}
