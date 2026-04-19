//go:build windows

package evtspike

import (
	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// SpikePayload aliases the canonical drainctl.SpikePayload so detector code
// can use a local name. The canonical definition lives in drainctl root to
// let notify.go embed it without creating a cycle with this package.
type SpikePayload = dc.SpikePayload
