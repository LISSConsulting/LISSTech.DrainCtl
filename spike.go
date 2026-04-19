//go:build windows

package drainctl

import "time"

// SpikePayload carries a confirmed event-log anomaly across three consumers:
// notification dispatch, the dashboard ring buffer, and SSE. Severity is
// assigned by notification-target wiring per FR-011a; this struct is
// deliberately severity-free.
//
// The canonical definition lives in the root drainctl package so notify.go can
// embed it in webhook payloads without creating an import cycle with
// internal/evtspike (which imports drainctl for EvtSpikeConfig). The
// internal/evtspike package re-exports it as a type alias for in-package
// readability.
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
