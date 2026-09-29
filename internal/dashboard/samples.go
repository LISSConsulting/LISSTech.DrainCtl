//go:build windows

package dashboard

import (
	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// checkResultSamples converts the numeric fields of a CheckResult into telemetry
// samples for storage. Only Performance and Sessions fields are extracted; Status,
// DrainMode, and other categorical fields are not stored as metrics.
func checkResultSamples(r dc.CheckResult) []telemetry.Sample {
	ts := r.Timestamp
	host := r.Host

	out := make([]telemetry.Sample, 0, 36)
	add := func(counter string, value float64) {
		switch counter {
		case "sessions_total", "sessions_active", "sessions_disconnected", "sessions_max":
			out = append(out, telemetry.Sample{Ts: ts, Host: host, Counter: counter, Value: value})
		default:
			if value, ok := dc.SanitizePerfField(counter, value); ok {
				out = append(out, telemetry.Sample{Ts: ts, Host: host, Counter: counter, Value: value})
			}
		}
	}

	if p := r.Performance; p != nil {
		add("cpu_pct", p.CPUPct)
		add("cpu_p95_pct", p.CPUP95)
		add("mem_avail_mb", p.MemAvailMB)
		add("mem_total_mb", p.MemTotalMB)
		add("pages_sec", p.PagesSec)
		add("disk_queue", p.DiskQueue)
		add("tcp_retrans_sec", p.TCPRetrans)
		if p.HasP50(dc.PerfP50InputDelay, p.InputDelayP50) {
			add("input_delay_p50_ms", p.InputDelayP50)
		}
		add("input_delay_p95_ms", p.InputDelayP95)
		add("input_delay_max_ms", p.InputDelayMax)
		if p.SessionCPUP95 != 0 {
			add("session_cpu_p95_pct", p.SessionCPUP95)
		}
		if p.SessionMemP95 != 0 {
			add("session_mem_p95_bytes", p.SessionMemP95)
		}
		if p.SessionCPUActivityCollected {
			add("session_cpu_observed_count", float64(p.SessionCPUObservedCount))
			add("session_cpu_ge_5_count", float64(p.SessionCPUAtOrAbove5Count))
			add("session_cpu_ge_20_count", float64(p.SessionCPUAtOrAbove20Count))
		}
		if p.RFXAvailable {
			// Zero FPS/quality means no active RemoteFX stream, not a poor
			// percentile. Omit it so higher-is-better charts render a gap.
			if p.RFXFPSOut > 0 {
				add("rfx_fps_out", p.RFXFPSOut)
			}
			if p.HasP50(dc.PerfP50RFXFPSOut, p.RFXFPSOutP50) && p.RFXFPSOutP50 > 0 {
				add("rfx_fps_out_p50", p.RFXFPSOutP50)
			}
			if p.HasP50(dc.PerfP50RFXSkipServer, p.RFXSkipServerP50) {
				add("rfx_skip_server_sec", p.RFXSkipServer)
				add("rfx_skip_server_sec_p50", p.RFXSkipServerP50)
			}
			if p.HasP50(dc.PerfP50RFXSkipNet, p.RFXSkipNetP50) {
				add("rfx_skip_net_sec", p.RFXSkipNet)
				add("rfx_skip_net_sec_p50", p.RFXSkipNetP50)
			}
			if p.HasP50(dc.PerfP50RFXEncodeMS, p.RFXEncodeMSP50) {
				add("rfx_encode_ms", p.RFXEncodeMS)
				add("rfx_encode_ms_p50", p.RFXEncodeMSP50)
			}
			if p.RFXQuality > 0 {
				add("rfx_quality_pct", p.RFXQuality)
			}
			if p.HasP50(dc.PerfP50RFXQuality, p.RFXQualityP50) && p.RFXQualityP50 > 0 {
				add("rfx_quality_pct_p50", p.RFXQualityP50)
			}
			if p.HasP50(dc.PerfP50RFXRTT, p.RFXRTTP50) {
				add("rfx_rtt_ms", p.RFXRTT)
				add("rfx_rtt_ms_p50", p.RFXRTTP50)
			}
			if p.HasP50(dc.PerfP50RFXLoss, p.RFXLossP50) {
				add("rfx_loss_pct", p.RFXLoss)
				add("rfx_loss_pct_p50", p.RFXLossP50)
			}
		}
	}

	if s := r.Sessions; s != nil {
		add("sessions_total", float64(s.TotalSessions))
		add("sessions_active", float64(s.ActiveSessions))
		add("sessions_disconnected", float64(s.DisconnectedSessions))
		if s.MaxSessions > 0 {
			add("sessions_max", float64(s.MaxSessions))
		}
	}

	return out
}
