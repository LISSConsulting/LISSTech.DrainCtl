//go:build windows

package perfmon

import (
	"fmt"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// PerfTriggerState tracks consecutive-poll state for performance triggers.
type PerfTriggerState struct {
	CPUWarnCount int // consecutive polls CPU >= warn threshold
	CPUCritCount int // consecutive polls CPU >= crit threshold
	MemWarnCount int // consecutive polls mem <= warn threshold
	MemCritCount int // consecutive polls mem <= crit threshold
}

// ConsecutiveThreshold is the number of consecutive polls required before
// CPU and memory triggers fire (to avoid flapping).
const ConsecutiveThreshold = 2

// EvaluateThresholds checks the PerfSnapshot against configured thresholds
// and returns the set of triggers that should fire.
// triggerState is updated in-place to track consecutive breaches.
func EvaluateThresholds(snap *dc.PerfSnapshot, cfg dc.PerformanceConfig, state *PerfTriggerState) []dc.Trigger {
	if snap == nil {
		return nil
	}

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

	if cpuCrit > 0 && state.CPUCritCount >= ConsecutiveThreshold {
		triggers = append(triggers, dc.TriggerCPUCritical)
	} else if cpuWarn > 0 && state.CPUWarnCount >= ConsecutiveThreshold {
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

	if memCrit > 0 && state.MemCritCount >= ConsecutiveThreshold {
		triggers = append(triggers, dc.TriggerMemoryCritical)
	} else if memWarn > 0 && state.MemWarnCount >= ConsecutiveThreshold {
		triggers = append(triggers, dc.TriggerMemoryWarning)
	}

	// Input delay thresholds (fire on single breach).
	idWarn := resolveThreshold(cfg.InputDelayWarnMS, 50)
	idCrit := resolveThreshold(cfg.InputDelayCritMS, 100)

	if idCrit > 0 && snap.InputDelayP95 >= float64(idCrit) {
		triggers = append(triggers, dc.TriggerInputDelayCritical)
	} else if idWarn > 0 && snap.InputDelayP95 >= float64(idWarn) {
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
		return fmt.Sprintf("Available memory %.0f MB (%.0f%% free, threshold: %d%% free)", snap.MemAvailMB, memFreePercent(snap), resolveThreshold(cfg.MemWarnPct, 20))
	case dc.TriggerMemoryCritical:
		return fmt.Sprintf("Available memory %.0f MB (%.0f%% free, critical threshold: %d%% free)", snap.MemAvailMB, memFreePercent(snap), resolveThreshold(cfg.MemCritPct, 10))
	case dc.TriggerInputDelayWarning:
		return fmt.Sprintf("Input delay P95 %.0fms (threshold: %dms)", snap.InputDelayP95, resolveThreshold(cfg.InputDelayWarnMS, 50))
	case dc.TriggerInputDelayCritical:
		return fmt.Sprintf("Input delay P95 %.0fms (critical threshold: %dms)", snap.InputDelayP95, resolveThreshold(cfg.InputDelayCritMS, 100))
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
