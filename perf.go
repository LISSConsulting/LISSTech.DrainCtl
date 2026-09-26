//go:build windows

package drainctl

import "math"

// PerfP50Presence identifies a P50 field that was read and validated. A zero
// mask is the wire-compatible representation emitted by older agents, whose
// zero-valued P50 fields must remain indistinguishable from absent values.
type PerfP50Presence uint16

const (
	PerfP50InputDelay PerfP50Presence = 1 << iota
	PerfP50SessionCPU
	PerfP50SessionMem
	PerfP50RFXFPSOut
	PerfP50RFXSkipServer
	PerfP50RFXSkipNet
	PerfP50RFXEncodeMS
	PerfP50RFXQuality
	PerfP50RFXRTT
	PerfP50RFXLoss
)

// PerfSnapshot holds one point-in-time performance sample.
type PerfSnapshot struct {
	// Host-level
	CPUPct     float64 `json:"cpu_pct"`     // average across samples
	CPUP95     float64 `json:"cpu_p95_pct"` // 95th percentile across samples
	MemAvailMB float64 `json:"mem_avail_mb"`
	MemTotalMB float64 `json:"mem_total_mb"`
	PagesSec   float64 `json:"pages_sec"`
	DiskQueue  float64 `json:"disk_queue"`
	TCPRetrans float64 `json:"tcp_retrans_sec"`

	// Per-session aggregates (User Input Delay)
	InputDelayP50 float64 `json:"input_delay_p50_ms"`
	InputDelayP95 float64 `json:"input_delay_p95_ms"`
	InputDelayMax float64 `json:"input_delay_max_ms"`

	// Per-session aggregates (Terminal Services Session)
	SessionCPUP95 float64 `json:"session_cpu_p95_pct,omitempty"`
	SessionCPUP50 float64 `json:"session_cpu_p50_pct,omitempty"`
	SessionMemP95 float64 `json:"session_mem_p95_bytes,omitempty"`
	SessionMemP50 float64 `json:"session_mem_p50_bytes,omitempty"`

	// P50Present marks P50 fields that were actually collected. It is omitted
	// for old-compatible reports that have no availability metadata.
	P50Present PerfP50Presence `json:"p50_present,omitempty"`

	// RemoteFX (zero-valued when unavailable). For lower-is-better metrics,
	// the primary field is the numeric P95. For higher-is-better FPS and frame
	// quality, it is the service P95 floor (numeric P5): 95% of sessions are at
	// or above it. The paired P50 field is always the numeric median.
	RFXAvailable     bool    `json:"rfx_available"`
	RFXFPSOut        float64 `json:"rfx_fps_out,omitempty"`
	RFXFPSOutP50     float64 `json:"rfx_fps_out_p50,omitempty"`
	RFXSkipServer    float64 `json:"rfx_skip_server_sec,omitempty"`
	RFXSkipServerP50 float64 `json:"rfx_skip_server_sec_p50,omitempty"`
	RFXSkipNet       float64 `json:"rfx_skip_net_sec,omitempty"`
	RFXSkipNetP50    float64 `json:"rfx_skip_net_sec_p50,omitempty"`
	RFXEncodeMS      float64 `json:"rfx_encode_ms,omitempty"`
	RFXEncodeMSP50   float64 `json:"rfx_encode_ms_p50,omitempty"`
	RFXQuality       float64 `json:"rfx_quality_pct,omitempty"`
	RFXQualityP50    float64 `json:"rfx_quality_pct_p50,omitempty"`
	RFXRTT           float64 `json:"rfx_rtt_ms,omitempty"`
	RFXRTTP50        float64 `json:"rfx_rtt_ms_p50,omitempty"`
	RFXLoss          float64 `json:"rfx_loss_pct,omitempty"`
	RFXLossP50       float64 `json:"rfx_loss_pct_p50,omitempty"`
}

// HasP50 reports whether a P50 was collected. Non-zero legacy values remain
// available while an unmarked zero from an older report remains absent.
func (p PerfSnapshot) HasP50(field PerfP50Presence, value float64) bool {
	return p.P50Present&field != 0 || value != 0
}

// SanitizePerfField validates one persisted performance field. It is shared by
// local collection and report ingestion so they enforce identical bounds.
func SanitizePerfField(field string, value float64) (float64, bool) {
	var max float64
	switch field {
	case "cpu_pct", "cpu_p95_pct", "session_cpu_p50_pct", "session_cpu_p95_pct", "rfx_quality_pct", "rfx_quality_pct_p50", "rfx_loss_pct", "rfx_loss_pct_p50":
		max = 100
	case "mem_avail_mb", "mem_total_mb":
		max = 1 << 50
	case "pages_sec", "disk_queue", "tcp_retrans_sec", "rfx_skip_server_sec", "rfx_skip_server_sec_p50", "rfx_skip_net_sec", "rfx_skip_net_sec_p50":
		max = 1e6
	case "input_delay_p50_ms", "input_delay_p95_ms", "input_delay_max_ms", "rfx_encode_ms", "rfx_encode_ms_p50", "rfx_rtt_ms", "rfx_rtt_ms_p50":
		max = 60000
	case "session_mem_p50_bytes", "session_mem_p95_bytes":
		max = 1 << 60
	case "rfx_fps_out", "rfx_fps_out_p50":
		max = 240
	default:
		return 0, false
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > max {
		return 0, false
	}
	return value, true
}

// SanitizePerfSnapshot removes invalid numeric values from a report before it
// is used or persisted. Valid zeroes are retained; P50Present distinguishes a
// collected zero from a missing legacy P50.
func SanitizePerfSnapshot(p PerfSnapshot) PerfSnapshot {
	sanitize := func(field string, value *float64) {
		if sanitized, ok := SanitizePerfField(field, *value); ok {
			*value = sanitized
		} else {
			*value = 0
		}
	}
	sanitizeP50 := func(field string, presence PerfP50Presence, value *float64) {
		if sanitized, ok := SanitizePerfField(field, *value); ok {
			*value = sanitized
		} else {
			*value = 0
			p.P50Present &^= presence
		}
	}
	sanitize("cpu_pct", &p.CPUPct)
	sanitize("cpu_p95_pct", &p.CPUP95)
	sanitize("mem_avail_mb", &p.MemAvailMB)
	sanitize("mem_total_mb", &p.MemTotalMB)
	sanitize("pages_sec", &p.PagesSec)
	sanitize("disk_queue", &p.DiskQueue)
	sanitize("tcp_retrans_sec", &p.TCPRetrans)
	sanitizeP50("input_delay_p50_ms", PerfP50InputDelay, &p.InputDelayP50)
	sanitize("input_delay_p95_ms", &p.InputDelayP95)
	sanitize("input_delay_max_ms", &p.InputDelayMax)
	sanitizeP50("session_cpu_p50_pct", PerfP50SessionCPU, &p.SessionCPUP50)
	sanitize("session_cpu_p95_pct", &p.SessionCPUP95)
	sanitizeP50("session_mem_p50_bytes", PerfP50SessionMem, &p.SessionMemP50)
	sanitize("session_mem_p95_bytes", &p.SessionMemP95)
	sanitize("rfx_fps_out", &p.RFXFPSOut)
	sanitizeP50("rfx_fps_out_p50", PerfP50RFXFPSOut, &p.RFXFPSOutP50)
	sanitize("rfx_skip_server_sec", &p.RFXSkipServer)
	sanitizeP50("rfx_skip_server_sec_p50", PerfP50RFXSkipServer, &p.RFXSkipServerP50)
	sanitize("rfx_skip_net_sec", &p.RFXSkipNet)
	sanitizeP50("rfx_skip_net_sec_p50", PerfP50RFXSkipNet, &p.RFXSkipNetP50)
	sanitize("rfx_encode_ms", &p.RFXEncodeMS)
	sanitizeP50("rfx_encode_ms_p50", PerfP50RFXEncodeMS, &p.RFXEncodeMSP50)
	sanitize("rfx_quality_pct", &p.RFXQuality)
	sanitizeP50("rfx_quality_pct_p50", PerfP50RFXQuality, &p.RFXQualityP50)
	sanitize("rfx_rtt_ms", &p.RFXRTT)
	sanitizeP50("rfx_rtt_ms_p50", PerfP50RFXRTT, &p.RFXRTTP50)
	sanitize("rfx_loss_pct", &p.RFXLoss)
	sanitizeP50("rfx_loss_pct_p50", PerfP50RFXLoss, &p.RFXLossP50)
	return p
}
