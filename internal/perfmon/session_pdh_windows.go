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
// session IDs. It uses the collector's existing V2 query and dedicated worker;
// it neither opens queries nor retains counter handles. PDH failures are
// deliberately non-fatal: their affected values remain nil and the applicable
// capability is false.
func (c *Collector) CollectSessionPDH(sessionIDs []uint32) (map[uint32]SessionPDHMetrics, SessionPDHCapabilities) {
	out := make(map[uint32]SessionPDHMetrics)
	if c == nil || len(sessionIDs) > maxSessionPDHEntries {
		return out, SessionPDHCapabilities{}
	}

	ids := make(map[uint32]struct{}, len(sessionIDs))
	for _, id := range sessionIDs {
		if id == 0 {
			continue
		}
		ids[id] = struct{}{}
		out[id] = SessionPDHMetrics{}
	}
	if len(ids) == 0 || c.sessionQuery == 0 {
		return out, SessionPDHCapabilities{}
	}

	var capabilities SessionPDHCapabilities
	if !c.do(func() {
		if err := pdhCollectQueryData(c.sessionQuery); err != nil {
			return
		}
		out, capabilities = c.collectSessionPDH(ids, out)
	}) {
		return out, SessionPDHCapabilities{}
	}
	return out, capabilities
}

func (c *Collector) collectSessionPDH(ids map[uint32]struct{}, out map[uint32]SessionPDHMetrics) (map[uint32]SessionPDHMetrics, SessionPDHCapabilities) {
	read := func(handle syscall.Handle) ([]pdhInstanceValue, bool) {
		if handle == 0 {
			return nil, false
		}
		values, err := pdhGetDoubleArrayInstances(handle)
		return values, err == nil
	}

	if values, ok := read(c.sessCPUH); ok {
		c.applySessionCPU(out, ids, values)
	}
	if values, ok := read(c.sessMemH); ok {
		applySessionFloat(out, ids, values, 0, float64(math.MaxInt64), true, func(metrics *SessionPDHMetrics, value float64) {
			metrics.WorkingSetBytes = new(sessiondata.DecimalUint64(uint64(value)))
		})
	}

	var caps SessionPDHCapabilities
	if values, ok := read(c.inputDelayH); ok {
		caps.InputDelay = c.inputDelayAvail
		applySessionFloat(out, ids, values, 0, 600000, true, func(metrics *SessionPDHMetrics, value float64) {
			metrics.InputDelayMS = new(uint32(value))
		})
	}

	rfxOK := c.rfxAvailable
	if values, ok := read(c.rfxFPSH); !collectRemoteFX(out, ids, values, ok, math.SmallestNonzeroFloat64, 240, false, func(metrics *SessionPDHMetrics, value float64) {
		ensureRemoteFX(metrics).FPS = new(RoundTo(value, 1))
	}) {
		rfxOK = false
	}
	if values, ok := read(c.rfxQualH); !collectRemoteFX(out, ids, values, ok, math.SmallestNonzeroFloat64, 100, false, func(metrics *SessionPDHMetrics, value float64) {
		ensureRemoteFX(metrics).QualityPercent = new(RoundTo(value, 1))
	}) {
		rfxOK = false
	}
	if values, ok := read(c.rfxEncH); !collectRemoteFX(out, ids, values, ok, 0, 60000, false, func(metrics *SessionPDHMetrics, value float64) {
		ensureRemoteFX(metrics).EncodeTimeMS = new(RoundTo(value, 1))
	}) {
		rfxOK = false
	}
	if values, ok := read(c.rfxRTTH); !collectRemoteFX(out, ids, values, ok, 0, 60000, false, func(metrics *SessionPDHMetrics, value float64) {
		ensureRemoteFX(metrics).RTTMS = new(RoundTo(value, 1))
	}) {
		rfxOK = false
	}
	if values, ok := read(c.rfxLossH); !collectRemoteFX(out, ids, values, ok, 0, 100, false, func(metrics *SessionPDHMetrics, value float64) {
		ensureRemoteFX(metrics).LossPercent = new(RoundTo(value, 2))
	}) {
		rfxOK = false
	}
	if values, ok := read(c.rfxSkipSrvH); !collectRemoteFX(out, ids, values, ok, 0, 1000000, false, func(metrics *SessionPDHMetrics, value float64) {
		ensureRemoteFX(metrics).ServerSkippedFPS = new(RoundTo(value, 1))
	}) {
		rfxOK = false
	}
	if values, ok := read(c.rfxSkipNetH); !collectRemoteFX(out, ids, values, ok, 0, 1000000, false, func(metrics *SessionPDHMetrics, value float64) {
		ensureRemoteFX(metrics).NetworkSkippedFPS = new(RoundTo(value, 1))
	}) {
		rfxOK = false
	}
	caps.RemoteFX = rfxOK
	return out, caps
}

func (c *Collector) applySessionCPU(out map[uint32]SessionPDHMetrics, ids map[uint32]struct{}, values []pdhInstanceValue) {
	applySessionFloat(out, ids, values, 0, c.sessionCPUPercentMax(), false, func(metrics *SessionPDHMetrics, value float64) {
		metrics.CPUPercent = new(RoundTo(value, 1))
	})
}

func (c *Collector) sessionCPUPercentMax() float64 {
	return float64(boundedLogicalCPUCount(int(c.logicalCPUCount))) * 100
}

func collectRemoteFX(out map[uint32]SessionPDHMetrics, ids map[uint32]struct{}, values []pdhInstanceValue, ok bool, min, max float64, integer bool, apply func(*SessionPDHMetrics, float64)) bool {
	if !ok {
		return false
	}
	applySessionFloat(out, ids, values, min, max, integer, apply)
	return true
}

func applySessionFloat(out map[uint32]SessionPDHMetrics, ids map[uint32]struct{}, values []pdhInstanceValue, min, max float64, integer bool, apply func(*SessionPDHMetrics, float64)) {
	for _, item := range values {
		id, ok := parseSessionInstanceID(item.Instance)
		if !ok {
			continue
		}
		if _, ok := ids[id]; !ok || !validSessionPDHValue(item.Value, min, max, integer) {
			continue
		}
		metrics := out[id]
		apply(&metrics, item.Value)
		out[id] = metrics
	}
}

// parseSessionInstanceID accepts only the canonical decimal spelling of a
// uint32 session ID. In particular, display names, suffixes, and leading-zero
// aliases are never allowed to correlate a PDH sample with a WTS session.
func parseSessionInstanceID(instance string) (uint32, bool) {
	if instance == "" || (len(instance) > 1 && instance[0] == '0') {
		return 0, false
	}
	var value uint64
	for i := range instance {
		digit := instance[i]
		if digit < '0' || digit > '9' {
			return 0, false
		}
		value = value*10 + uint64(digit-'0')
		if value > math.MaxUint32 {
			return 0, false
		}
	}
	return uint32(value), true
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
