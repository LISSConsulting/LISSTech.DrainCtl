//go:build windows

package evtspike

import "time"

// SchemaVersion is bumped on any breaking change to BaselineFile so LoadBaseline
// can reject mismatched files rather than silently misinterpret them.
const SchemaVersion = 1

// RecentFlags is intentionally omitted — the 2-of-3 confirmation window refills
// within 30 s of restart, so persisting it buys nothing.
type ChannelState struct {
	Slots     [slotsPerDay]GammaState `json:"slots"`
	Global    GammaState              `json:"global"`
	LastAlert time.Time               `json:"last_alert"`
}

type BaselineFile struct {
	SchemaVersion int                     `json:"schema_version"`
	WrittenAt     time.Time               `json:"written_at"`
	Host          string                  `json:"host"`
	Channels      map[string]ChannelState `json:"channels"`
}
