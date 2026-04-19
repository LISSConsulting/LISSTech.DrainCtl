//go:build windows

package evtspike

import "time"

// SpikePayload carries a confirmed anomaly across three consumers:
// notification dispatch, the dashboard ring buffer, and SSE. It deliberately
// omits a Severity field — severity is assigned by the notification target's
// wiring (FR-011a), not by the detector.
type SpikePayload struct {
	Host              string    `json:"host"`
	Channel           string    `json:"channel"`
	WindowStart       time.Time `json:"window_start"`
	WindowEnd         time.Time `json:"window_end"`
	Observed          int       `json:"observed"`
	Expected          float64   `json:"expected"`
	TailProbability   float64   `json:"tail_probability"`
	ConfirmationCount int       `json:"confirmation_count"`
	FirstSeenAt       time.Time `json:"first_seen_at"`
}
