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

	out := make([]telemetry.Sample, 0, 32)
	add := func(counter string, v float64) {
		out = append(out, telemetry.Sample{Ts: ts, Host: host, Counter: counter, Value: v})
	}

	if p := r.Performance; p != nil {
		add("cpu_pct", p.CPUPct)
		add("cpu_p95_pct", p.CPUP95)
		add("mem_avail_mb", p.MemAvailMB)
		add("mem_total_mb", p.MemTotalMB)
		add("pages_sec", p.PagesSec)
		add("disk_queue", p.DiskQueue)
		add("tcp_retrans_sec", p.TCPRetrans)
		add("input_delay_p50_ms", p.InputDelayP50)
		add("input_delay_p95_ms", p.InputDelayP95)
		add("input_delay_max_ms", p.InputDelayMax)
		if p.SessionCPUP95 != 0 {
			add("session_cpu_p95_pct", p.SessionCPUP95)
		}
		if p.SessionCPUP50 != 0 {
			add("session_cpu_p50_pct", p.SessionCPUP50)
		}
		if p.SessionMemP95 != 0 {
			add("session_mem_p95_bytes", p.SessionMemP95)
		}
		if p.SessionMemP50 != 0 {
			add("session_mem_p50_bytes", p.SessionMemP50)
		}
		if p.RFXAvailable {
			add("rfx_fps_out", p.RFXFPSOut)
			add("rfx_fps_out_p50", p.RFXFPSOutP50)
			add("rfx_skip_server_sec", p.RFXSkipServer)
			add("rfx_skip_net_sec", p.RFXSkipNet)
			add("rfx_encode_ms", p.RFXEncodeMS)
			add("rfx_quality_pct", p.RFXQuality)
			add("rfx_rtt_ms", p.RFXRTT)
			add("rfx_loss_pct", p.RFXLoss)
		}
	}

	if s := r.Sessions; s != nil {
		add("sessions_total", float64(s.TotalSessions))
		add("sessions_active", float64(s.ActiveSessions))
		add("sessions_disconnected", float64(s.DisconnectedSessions))
		add("sessions_max", float64(s.MaxSessions))
	}

	return out
}
