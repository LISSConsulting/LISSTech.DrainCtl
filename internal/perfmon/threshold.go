//go:build windows

package perfmon

import (
	"fmt"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// PerfTriggerState tracks consecutive-evaluation state for performance triggers.
type PerfTriggerState struct {
	CPUWarnCount        int // consecutive evaluations CPU >= warn threshold
	CPUCritCount        int // consecutive evaluations CPU >= crit threshold
	MemWarnCount        int // consecutive evaluations mem <= warn threshold
	MemCritCount        int // consecutive evaluations mem <= crit threshold
	InputDelayWarnCount int // consecutive evaluations input delay >= warn threshold
	InputDelayCritCount int // consecutive evaluations input delay >= crit threshold
}

// ceilDiv returns ⌈a/b⌉ for positive integers.
func ceilDiv(a, b int) int {
	if b <= 0 {
		return 1
	}
	return (a + b - 1) / b
}

// EvaluateThresholds checks the PerfSnapshot against configured thresholds
// and returns the set of triggers that should fire. evaluationInterval is the
// cadence at which this function is called, not the collector's sampling
// cadence; the collector may aggregate several samples into one evaluation.
// triggerState is updated in-place to track consecutive breaches.
func EvaluateThresholds(
	snap *dc.PerfSnapshot,
	cfg dc.PerformanceConfig,
	state *PerfTriggerState,
	evaluationInterval time.Duration,
) []dc.Trigger {
	if snap == nil {
		return nil
	}

	intervalSeconds := int(evaluationInterval / time.Second)
	if intervalSeconds <= 0 {
		intervalSeconds = cfg.SampleIntervalSec
	}
	if intervalSeconds <= 0 {
		intervalSeconds = dc.DefaultSampleInterval
	}
	cpuMemPolls := ceilDiv(resolveThreshold(cfg.LoadAlertDelaySec, dc.DefaultLoadAlertDelaySec), intervalSeconds)
	idPolls := ceilDiv(resolveThreshold(cfg.InputDelayAlertDelaySec, dc.DefaultInputDelayAlertDelaySec), intervalSeconds)

	var triggers []dc.Trigger

	// CPU thresholds (require consecutive polls).
	cpuWarn := resolveThreshold(cfg.CPUWarnPct, 70)
	cpuCrit := resolveThreshold(cfg.CPUCritPct, 85)

	if cpuCrit > 0 && snap.CPUPct >= float64(cpuCrit) {
		state.CPUCritCount++
		state.CPUWarnCount++ // crit implies warn
	} else if cpuWarn > 0 && snap.CPUPct >= float64(cpuWarn) {
		state.CPUCritCount = 0
		state.CPUWarnCount++
	} else {
		state.CPUCritCount = 0
		state.CPUWarnCount = 0
	}

	if cpuCrit > 0 && state.CPUCritCount >= cpuMemPolls {
		triggers = append(triggers, dc.TriggerCPUCritical)
	} else if cpuWarn > 0 && state.CPUWarnCount >= cpuMemPolls {
		triggers = append(triggers, dc.TriggerCPUWarning)
	}

	// Memory thresholds (% free, require consecutive polls).
	memWarn := resolveThreshold(cfg.MemWarnPct, 20)
	memCrit := resolveThreshold(cfg.MemCritPct, 10)

	memFreePct := memFreePercent(snap)

	if memCrit > 0 && memFreePct <= float64(memCrit) {
		state.MemCritCount++
		state.MemWarnCount++
	} else if memWarn > 0 && memFreePct <= float64(memWarn) {
		state.MemCritCount = 0
		state.MemWarnCount++
	} else {
		state.MemCritCount = 0
		state.MemWarnCount = 0
	}

	if memCrit > 0 && state.MemCritCount >= cpuMemPolls {
		triggers = append(triggers, dc.TriggerMemoryCritical)
	} else if memWarn > 0 && state.MemWarnCount >= cpuMemPolls {
		triggers = append(triggers, dc.TriggerMemoryWarning)
	}

	// Input delay thresholds (require consecutive polls).
	idWarn := resolveThreshold(cfg.InputDelayWarnMS, 50)
	idCrit := resolveThreshold(cfg.InputDelayCritMS, 100)

	idValue := inputDelayValue(snap, cfg)

	if idCrit > 0 && idValue >= float64(idCrit) {
		state.InputDelayCritCount++
		state.InputDelayWarnCount++ // crit implies warn
	} else if idWarn > 0 && idValue >= float64(idWarn) {
		state.InputDelayCritCount = 0
		state.InputDelayWarnCount++
	} else {
		state.InputDelayCritCount = 0
		state.InputDelayWarnCount = 0
	}

	if idCrit > 0 && state.InputDelayCritCount >= idPolls {
		triggers = append(triggers, dc.TriggerInputDelayCritical)
	} else if idWarn > 0 && state.InputDelayWarnCount >= idPolls {
		triggers = append(triggers, dc.TriggerInputDelayWarning)
	}

	return triggers
}

// TriggerMessage returns a human-readable message for a performance trigger.
func TriggerMessage(trigger dc.Trigger, snap *dc.PerfSnapshot, cfg dc.PerformanceConfig) string {
	switch trigger {
	case dc.TriggerCPUWarning:
		return fmt.Sprintf("CPU at %.0f%% (threshold: %d%%)", snap.CPUPct, resolveThreshold(cfg.CPUWarnPct, 70))
	case dc.TriggerCPUCritical:
		return fmt.Sprintf("CPU at %.0f%% (critical threshold: %d%%)", snap.CPUPct, resolveThreshold(cfg.CPUCritPct, 85))
	case dc.TriggerMemoryWarning:
		return fmt.Sprintf("Memory at %.0f%% (threshold: %d%%)", memUsedPercent(snap), 100-resolveThreshold(cfg.MemWarnPct, 20))
	case dc.TriggerMemoryCritical:
		return fmt.Sprintf("Memory at %.0f%% (critical threshold: %d%%)", memUsedPercent(snap), 100-resolveThreshold(cfg.MemCritPct, 10))
	case dc.TriggerInputDelayWarning:
		pct := inputDelayPercentileLabel(cfg)
		return fmt.Sprintf("Input delay %s %.0fms (threshold: %dms)", pct, inputDelayValue(snap, cfg), resolveThreshold(cfg.InputDelayWarnMS, 50))
	case dc.TriggerInputDelayCritical:
		pct := inputDelayPercentileLabel(cfg)
		return fmt.Sprintf("Input delay %s %.0fms (critical threshold: %dms)", pct, inputDelayValue(snap, cfg), resolveThreshold(cfg.InputDelayCritMS, 100))
	default:
		return string(trigger)
	}
}

// resolveThreshold returns the configured threshold, defaulting to defVal
// when the configured value is 0. Returns -1 (disabled) when configured as -1.
func resolveThreshold(configured, defVal int) int {
	if configured == 0 {
		return defVal
	}
	if configured == -1 {
		return 0 // disabled
	}
	return configured
}

// memFreePercent returns the percentage of total memory that is available.
func memFreePercent(snap *dc.PerfSnapshot) float64 {
	if snap.MemTotalMB <= 0 {
		return 100 // can't determine — assume healthy
	}
	return (snap.MemAvailMB / snap.MemTotalMB) * 100
}

// memUsedPercent returns the percentage of total memory that is in use.
func memUsedPercent(snap *dc.PerfSnapshot) float64 {
	return 100 - memFreePercent(snap)
}

// inputDelayValue returns the input delay metric based on the configured percentile.
func inputDelayValue(snap *dc.PerfSnapshot, cfg dc.PerformanceConfig) float64 {
	if cfg.InputDelayPercentile == "p50" {
		return snap.InputDelayP50
	}
	return snap.InputDelayP95 // default
}

// inputDelayPercentileLabel returns a display label for the configured percentile.
func inputDelayPercentileLabel(cfg dc.PerformanceConfig) string {
	if cfg.InputDelayPercentile == "p50" {
		return "P50"
	}
	return "P95"
}

// IsPerfTrigger returns true for performance-related triggers.
func IsPerfTrigger(t dc.Trigger) bool {
	switch t {
	case dc.TriggerCPUWarning, dc.TriggerCPUCritical,
		dc.TriggerMemoryWarning, dc.TriggerMemoryCritical,
		dc.TriggerInputDelayWarning, dc.TriggerInputDelayCritical:
		return true
	}
	return false
}
