//go:build windows

package evtspike

import (
	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

const (
	StateHealthy  = "healthy"
	StateTraining = "training"
	StateDisabled = "disabled"
	StateError    = "error"
)

// DetectorStatus aliases the canonical drainctl.DetectorStatus so detector code
// can use a local name. The canonical definition lives in drainctl root so
// CheckResult can embed it (via EvtSpikeStatus) without creating a cycle with
// this package.
type DetectorStatus = dc.DetectorStatus

type RecentSpikeEntry struct {
	ID int64 `json:"id"`
	SpikePayload
}

// DeriveState implements the priority-ordered state table in data-model.md §6.
func DeriveState(enabled bool, enabledChannels, matureChannels int, startupErr error) string {
	if !enabled {
		return StateDisabled
	}
	if startupErr != nil || enabledChannels == 0 {
		return StateError
	}
	if matureChannels*2 >= enabledChannels {
		return StateHealthy
	}
	return StateTraining
}
