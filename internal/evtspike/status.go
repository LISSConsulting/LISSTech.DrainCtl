//go:build windows

package evtspike

import "time"

const (
	StateHealthy  = "healthy"
	StateTraining = "training"
	StateDisabled = "disabled"
	StateError    = "error"
)

type DetectorStatus struct {
	Host            string     `json:"host"`
	State           string     `json:"state"`
	EnabledChannels int        `json:"enabled_channels"`
	MatureChannels  int        `json:"mature_channels"`
	ErrorReason     string     `json:"error_reason,omitempty"`
	LastSpikeAt     *time.Time `json:"last_spike_at,omitempty"`
}

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
