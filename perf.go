//go:build windows

package drainctl

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

	// RemoteFX (zero-valued when unavailable)
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
