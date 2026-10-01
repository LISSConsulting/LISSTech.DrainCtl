//go:build windows

package perfmon

import (
	"math"
	"syscall"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
)

const maxSessionPDHEntries = 500

// SessionPDHMetrics contains the optional PDH values for one WTS session.
// A nil field means the counter was unavailable or did not have a valid value.
type SessionPDHMetrics struct {
	CPUPercent      *float64
	WorkingSetBytes *sessiondata.DecimalUint64
	InputDelayMS    *uint32
	RemoteFX        *sessiondata.RemoteFXMetrics
}

// SessionRemoteFXMetrics aliases the shared wire model for compatibility with
// callers that consume this adapter directly.
type SessionRemoteFXMetrics = sessiondata.RemoteFXMetrics

// SessionPDHCapabilities reports whether the optional counter families were
// fully available for this collection. Individual values can still be present
// when a RemoteFX counter is unavailable.
type SessionPDHCapabilities struct {
	InputDelay bool
	RemoteFX   bool
}

// CollectSessionPDH collects named per-session values for the supplied WTS
// sessions. It uses the collector's existing V2 query and dedicated worker;
// it neither opens queries nor retains counter handles. PDH failures are
// deliberately non-fatal: their affected values remain nil and the applicable
// capability is false.
//
// The records must include each session's WinStation name (Station) so the
// collector can correlate PDH instances like "RDP-Tcp 6", "Console", and
// "Services" back to their session IDs. The legacy Collector.collect() path
// uses the same stationInstanceKey matcher; consolidating here keeps the
// two readers from drifting apart again.
func (c *Collector) CollectSessionPDH(records []sessiondata.SessionRecord) (map[uint32]SessionPDHMetrics, SessionPDHCapabilities) {
	out := make(map[uint32]SessionPDHMetrics)
	if c == nil || len(records) > maxSessionPDHEntries {
		return out, SessionPDHCapabilities{}
	}

	// stationKey → sessionID, so we can route PDH instances (named by
	// station) back to the originating WTS session.
	stationToID := make(map[string]uint32, len(records))
	for _, record := range records {
		if record.SessionID == 0 {
			continue
		}
		out[record.SessionID] = SessionPDHMetrics{}
		if record.Station == nil || *record.Station == "" {
			continue
		}
		stationToID[sessionInstanceKey(*record.Station)] = record.SessionID
	}
	if len(stationToID) == 0 || c.sessionQuery == 0 {
		return out, SessionPDHCapabilities{}
	}

	var capabilities SessionPDHCapabilities
	if !c.do(func() {
		if err := pdhCollectQueryData(c.sessionQuery); err != nil {
			return
		}
		out, capabilities = c.collectSessionPDH(stationToID, out)
	}) {
		return out, capabilities
	}
	return out, capabilities
}

func (c *Collector) collectSessionPDH(stationToID map[string]uint32, out map[uint32]SessionPDHMetrics) (map[uint32]SessionPDHMetrics, SessionPDHCapabilities) {
	read := func(handle syscall.Handle) ([]pdhInstanceValue, bool) {
		if handle == 0 {
			return nil, false
		}
		values, err := pdhGetDoubleArrayInstances(handle)
		return values, err == nil
	}

	if values, ok := read(c.sessCPUH); ok {
		c.applySessionCPU(out, stationToID, values)
	}
	if values, ok := read(c.sessMemH); ok {
		applySessionFloat(out, stationToID, values, 0, float64(math.MaxInt64), true, func(metrics *SessionPDHMetrics, value float64) {
			metrics.WorkingSetBytes = new(sessiondata.DecimalUint64(uint64(value)))
		})
	}

	var caps SessionPDHCapabilities
	if values, ok := read(c.inputDelayH); ok {
		caps.InputDelay = c.inputDelayAvail
		applySessionFloat(out, stationToID, values, 0, 600000, true, func(metrics *SessionPDHMetrics, value float64) {
			metrics.InputDelayMS = new(uint32(value))
		})
	}

	rfxOK := c.rfxAvailable
	if values, ok := read(c.rfxFPSH); !collectRemoteFX(out, stationToID, values, ok, math.SmallestNonzeroFloat64, 240, false, func(metrics *SessionPDHMetrics, value float64) {
		ensureRemoteFX(metrics).FPS = new(RoundTo(value, 1))
	}) {
		rfxOK = false
	}
	if values, ok := read(c.rfxQualH); !collectRemoteFX(out, stationToID, values, ok, math.SmallestNonzeroFloat64, 100, false, func(metrics *SessionPDHMetrics, value float64) {
		ensureRemoteFX(metrics).QualityPercent = new(RoundTo(value, 1))
	}) {
		rfxOK = false
	}
	if values, ok := read(c.rfxEncH); !collectRemoteFX(out, stationToID, values, ok, 0, 60000, false, func(metrics *SessionPDHMetrics, value float64) {
		ensureRemoteFX(metrics).EncodeTimeMS = new(RoundTo(value, 1))
	}) {
		rfxOK = false
	}
	if values, ok := read(c.rfxRTTH); !collectRemoteFX(out, stationToID, values, ok, 0, 60000, false, func(metrics *SessionPDHMetrics, value float64) {
		ensureRemoteFX(metrics).RTTMS = new(RoundTo(value, 1))
	}) {
		rfxOK = false
	}
	if values, ok := read(c.rfxLossH); !collectRemoteFX(out, stationToID, values, ok, 0, 100, false, func(metrics *SessionPDHMetrics, value float64) {
		ensureRemoteFX(metrics).LossPercent = new(RoundTo(value, 2))
	}) {
		rfxOK = false
	}
	if values, ok := read(c.rfxSkipSrvH); !collectRemoteFX(out, stationToID, values, ok, 0, 1000000, false, func(metrics *SessionPDHMetrics, value float64) {
		ensureRemoteFX(metrics).ServerSkippedFPS = new(RoundTo(value, 1))
	}) {
		rfxOK = false
	}
	if values, ok := read(c.rfxSkipNetH); !collectRemoteFX(out, stationToID, values, ok, 0, 1000000, false, func(metrics *SessionPDHMetrics, value float64) {
		ensureRemoteFX(metrics).NetworkSkippedFPS = new(RoundTo(value, 1))
	}) {
		rfxOK = false
	}
	caps.RemoteFX = rfxOK
	return out, caps
}

func (c *Collector) applySessionCPU(out map[uint32]SessionPDHMetrics, stationToID map[string]uint32, values []pdhInstanceValue) {
	applySessionFloat(out, stationToID, values, 0, c.sessionCPUPercentMax(), false, func(metrics *SessionPDHMetrics, value float64) {
		metrics.CPUPercent = new(RoundTo(value, 1))
	})
}

func (c *Collector) sessionCPUPercentMax() float64 {
	return float64(boundedLogicalCPUCount(int(c.logicalCPUCount))) * 100
}

func collectRemoteFX(out map[uint32]SessionPDHMetrics, stationToID map[string]uint32, values []pdhInstanceValue, ok bool, min, max float64, integer bool, apply func(*SessionPDHMetrics, float64)) bool {
	if !ok {
		return false
	}
	applySessionFloat(out, stationToID, values, min, max, integer, apply)
	return true
}

// applySessionFloat correlates each PDH sample back to a WTS session via the
// shared stationInstanceKey matcher (lowercased, trimmed, with '#' and ' '
// stripped) so the \Terminal Services Session(*)\… instances like
// "RDP-Tcp 6", "Console", and "Services" resolve correctly. Sessions whose
// Station is empty or whose PDH instance fails validation are skipped.
func applySessionFloat(out map[uint32]SessionPDHMetrics, stationToID map[string]uint32, values []pdhInstanceValue, min, max float64, integer bool, apply func(*SessionPDHMetrics, float64)) {
	for _, item := range values {
		id, ok := stationToID[sessionInstanceKey(item.Instance)]
		if !ok || !validSessionPDHValue(item.Value, min, max, integer) {
			continue
		}
		metrics := out[id]
		apply(&metrics, item.Value)
		out[id] = metrics
	}
}

func validSessionPDHValue(value, min, max float64, integer bool) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= min && value <= max && (!integer || math.Trunc(value) == value)
}

func ensureRemoteFX(metrics *SessionPDHMetrics) *SessionRemoteFXMetrics {
	if metrics.RemoteFX == nil {
		metrics.RemoteFX = &SessionRemoteFXMetrics{}
	}
	return metrics.RemoteFX
}
