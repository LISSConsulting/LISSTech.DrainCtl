//go:build windows

package perfmon

import (
	"testing"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

func defaultPerfCfg() dc.PerformanceConfig {
	return dc.PerformanceConfig{
		Enabled:           true,
		CollectPerSession: true,
	}
}

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
	triggers := EvaluateThresholds(snap, cfg, state)
	if hasTrigger(triggers, dc.TriggerCPUWarning) {
		t.Error("CPU warning should not fire on first breach")
	}

	triggers = EvaluateThresholds(snap, cfg, state)
	if !hasTrigger(triggers, dc.TriggerCPUWarning) {
		t.Error("CPU warning should fire on second consecutive breach")
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
	EvaluateThresholds(snap, cfg, state)
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
	triggers := EvaluateThresholds(snap, cfg, state)
	if hasTrigger(triggers, dc.TriggerMemoryWarning) {
		t.Error("Memory warning should not fire on first breach")
	}

	triggers = EvaluateThresholds(snap, cfg, state)
	if !hasTrigger(triggers, dc.TriggerMemoryWarning) {
		t.Error("Memory warning should fire on second consecutive breach")
	}
}

func TestMemoryCritical_FivePercentFree(t *testing.T) {
	cfg := defaultPerfCfg()
	state := &PerfTriggerState{}

	snap := &dc.PerfSnapshot{MemTotalMB: 16000, MemAvailMB: 800}
	EvaluateThresholds(snap, cfg, state)
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

func TestInputDelayWarning_FiresImmediately(t *testing.T) {
	cfg := defaultPerfCfg()
	state := &PerfTriggerState{}

	snap := &dc.PerfSnapshot{InputDelayP95: 60, MemTotalMB: 16000, MemAvailMB: 10000}
	triggers := EvaluateThresholds(snap, cfg, state)

	if !hasTrigger(triggers, dc.TriggerInputDelayWarning) {
		t.Error("Input delay warning should fire on first breach (no consecutive requirement)")
	}
}

func TestInputDelayCritical_OverridesWarning(t *testing.T) {
	cfg := defaultPerfCfg()
	state := &PerfTriggerState{}

	snap := &dc.PerfSnapshot{InputDelayP95: 120, MemTotalMB: 16000, MemAvailMB: 10000}
	triggers := EvaluateThresholds(snap, cfg, state)

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
	triggers := EvaluateThresholds(snap, cfg, state)

	if hasTrigger(triggers, dc.TriggerInputDelayWarning) || hasTrigger(triggers, dc.TriggerInputDelayCritical) {
		t.Error("No input delay trigger should fire below threshold")
	}
}

// ── Disabled thresholds ─────────────────────────────────────────────────────

func TestDisabledThreshold_NegativeOne(t *testing.T) {
	cfg := defaultPerfCfg()
	cfg.CPUWarnPct = -1
	cfg.CPUCritPct = -1
	state := &PerfTriggerState{}

	snap := &dc.PerfSnapshot{CPUPct: 99, MemTotalMB: 16000, MemAvailMB: 10000}
	EvaluateThresholds(snap, cfg, state)
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
	EvaluateThresholds(snap, cfg, state)
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
