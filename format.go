//go:build windows

package drainctl

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// OutputFormat controls how results are rendered.
type OutputFormat string

const (
	FormatPlain OutputFormat = "plain"
	FormatTable OutputFormat = "table"
	FormatCSV   OutputFormat = "csv"
	FormatJSON  OutputFormat = "json"
)

func ParseFormat(s string) (OutputFormat, error) {
	switch s {
	case "plain", "":
		return FormatPlain, nil
	case "table":
		return FormatTable, nil
	case "csv":
		return FormatCSV, nil
	case "json":
		return FormatJSON, nil
	default:
		return "", fmt.Errorf("unknown format %q (valid: plain, table, csv, json)", s)
	}
}

// ---------------------------------------------------------------------------
// Check result
// ---------------------------------------------------------------------------

// CheckResult holds the complete outcome of a single check run.
type CheckResult struct {
	Version              string          `json:"version"`
	Timestamp            time.Time       `json:"timestamp"`
	Host                 string          `json:"host"`
	DrainModeLabel       string          `json:"drain_mode"`
	DrainModeValue       uint32          `json:"drain_mode_value"`
	GracePeriodSeconds   int             `json:"grace_period_seconds"`
	StateSince           *time.Time      `json:"state_since"`
	StateDurationSeconds *float64        `json:"state_duration_seconds"`
	Status               string          `json:"status"`
	ConnectionsAllowed   *bool           `json:"connections_allowed"`
	Transition           bool            `json:"transition"`
	TransitionFrom       string          `json:"transition_from,omitempty"`
	ChangedBy            string          `json:"changed_by,omitempty"`
	Sessions             *SessionSummary `json:"sessions,omitempty"`
	Performance          *PerfSnapshot   `json:"performance,omitempty"`
	Message              string          `json:"message"`
	ExitCode             int             `json:"exit_code"`
}

// Write renders the check result to w in the specified format.
func (r *CheckResult) Write(w io.Writer, format OutputFormat) {
	switch format {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(r)

	case FormatCSV:
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{
			"timestamp", "host", "drain_mode", "drain_mode_value",
			"state_since", "state_duration_seconds",
			"grace_period_seconds", "status", "connections_allowed",
			"transition", "transition_from", "changed_by", "message", "exit_code",
			"active_sessions", "disconnected_sessions", "total_sessions", "max_sessions",
			"cpu_pct", "mem_avail_mb", "mem_total_mb", "pages_sec", "disk_queue",
			"tcp_retrans_sec", "input_delay_p50_ms", "input_delay_p95_ms", "input_delay_max_ms",
			"session_cpu_p95_pct", "session_mem_p95_bytes",
			"rfx_available", "rfx_fps_out", "rfx_encode_ms", "rfx_quality_pct",
			"rfx_rtt_ms", "rfx_loss_pct", "rfx_skip_server_sec", "rfx_skip_net_sec",
		})
		activeSess, disconnSess, totalSess, maxSess := "", "", "", ""
		if r.Sessions != nil {
			activeSess = fmt.Sprintf("%d", r.Sessions.ActiveSessions)
			disconnSess = fmt.Sprintf("%d", r.Sessions.DisconnectedSessions)
			totalSess = fmt.Sprintf("%d", r.Sessions.TotalSessions)
			maxSess = fmt.Sprintf("%d", r.Sessions.MaxSessions)
		}
		perfCSV := make([]string, 14)
		if p := r.Performance; p != nil {
			perfCSV[0] = fmt.Sprintf("%.1f", p.CPUPct)
			perfCSV[1] = fmt.Sprintf("%.0f", p.MemAvailMB)
			perfCSV[2] = fmt.Sprintf("%.0f", p.MemTotalMB)
			perfCSV[3] = fmt.Sprintf("%.1f", p.PagesSec)
			perfCSV[4] = fmt.Sprintf("%.2f", p.DiskQueue)
			perfCSV[5] = fmt.Sprintf("%.1f", p.TCPRetrans)
			perfCSV[6] = fmt.Sprintf("%.1f", p.InputDelayP50)
			perfCSV[7] = fmt.Sprintf("%.1f", p.InputDelayP95)
			perfCSV[8] = fmt.Sprintf("%.1f", p.InputDelayMax)
			perfCSV[9] = fmt.Sprintf("%.1f", p.SessionCPUP95)
			perfCSV[10] = fmt.Sprintf("%.0f", p.SessionMemP95)
			perfCSV[11] = fmt.Sprintf("%t", p.RFXAvailable)
			perfCSV[12] = fmt.Sprintf("%.1f", p.RFXFPSOut)
			perfCSV[13] = fmt.Sprintf("%.1f", p.RFXEncodeMS)
		}
		row := []string{
			r.Timestamp.Format(time.RFC3339),
			r.Host,
			r.DrainModeLabel,
			fmt.Sprintf("%d", r.DrainModeValue),
			formatTimePtr(r.StateSince),
			formatFloatPtr(r.StateDurationSeconds),
			fmt.Sprintf("%d", r.GracePeriodSeconds),
			r.Status,
			formatBoolPtr(r.ConnectionsAllowed),
			fmt.Sprintf("%t", r.Transition),
			r.TransitionFrom,
			r.ChangedBy,
			r.Message,
			fmt.Sprintf("%d", r.ExitCode),
			activeSess, disconnSess, totalSess, maxSess,
		}
		row = append(row, perfCSV...)
		if p := r.Performance; p != nil {
			row = append(row,
				fmt.Sprintf("%.1f", p.RFXQuality),
				fmt.Sprintf("%.1f", p.RFXRTT),
				fmt.Sprintf("%.1f", p.RFXLoss),
				fmt.Sprintf("%.1f", p.RFXSkipServer),
				fmt.Sprintf("%.1f", p.RFXSkipNet),
			)
		} else {
			row = append(row, "", "", "", "", "")
		}
		_ = cw.Write(row)
		cw.Flush()

	case FormatTable:
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "HOST\tDRAIN MODE\tSTATUS\tCONNECTIONS\tSTATE DURATION\tCHANGED BY\tCPU%\tMEM FREE\tINPUT DLY\tDISK Q\tEXIT")
		_, _ = fmt.Fprintln(tw, "----\t----------\t------\t-----------\t--------------\t----------\t----\t--------\t---------\t------\t----")
		cpuCol, memCol, delayCol, diskCol := "-", "-", "-", "-"
		if p := r.Performance; p != nil {
			cpuCol = fmt.Sprintf("%.0f%%", p.CPUPct)
			if p.MemTotalMB > 0 {
				memCol = fmt.Sprintf("%.0f%%", (p.MemAvailMB/p.MemTotalMB)*100)
			}
			if p.InputDelayMax > 0 {
				delayCol = fmt.Sprintf("%.0fms", p.InputDelayMax)
			}
			diskCol = fmt.Sprintf("%.1f", p.DiskQueue)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
			r.Host,
			r.DrainModeLabel,
			r.Status,
			formatBoolPtr(r.ConnectionsAllowed),
			formatAge(r.StateDurationSeconds),
			or(r.ChangedBy, "-"),
			cpuCol, memCol, delayCol, diskCol,
			r.ExitCode,
		)
		_ = tw.Flush()

	case FormatPlain:
		// plain is handled inline by log calls in Check() — this is the fallback.
	}
}

// ---------------------------------------------------------------------------
// History results
// ---------------------------------------------------------------------------

// HistoryRecord is the JSON/CSV-serializable form of an audit record.
type HistoryRecord struct {
	Timestamp            string   `json:"timestamp"`
	Host                 string   `json:"host"`
	DrainMode            string   `json:"drain_mode"`
	DrainValue           uint32   `json:"drain_mode_value"`
	KeyModified          string   `json:"key_modified,omitempty"`
	StateDurationSeconds *int     `json:"state_duration_seconds"`
	Changed              bool     `json:"changed"`
	ChangedBy            string   `json:"changed_by,omitempty"`
	ActiveSessions       int      `json:"active_sessions,omitempty"`
	DisconnectedSessions int      `json:"disconnected_sessions,omitempty"`
	TotalSessions        int      `json:"total_sessions,omitempty"`
	MaxSessions          int      `json:"max_sessions,omitempty"`
	CPUPct               *float64 `json:"cpu_pct,omitempty"`
	InputDelayMax        *float64 `json:"input_delay_max_ms,omitempty"`
	MemAvailMB           *float64 `json:"mem_avail_mb,omitempty"`
	MemTotalMB           *float64 `json:"mem_total_mb,omitempty"`
	DiskQueue            *float64 `json:"disk_queue,omitempty"`
	TCPRetransSec        *float64 `json:"tcp_retrans_sec,omitempty"`
	ExitCode             int      `json:"exit_code"`
}

// ComputeStateDurations annotates records (newest-first) with state duration.
// For each record, duration = time in the current mode at that observation.
func ComputeStateDurations(records []AuditRecord) []int {
	if len(records) == 0 {
		return nil
	}

	// Records are newest-first; reverse to chronological order for computation.
	n := len(records)
	durations := make([]int, n)

	// Walk chronologically (oldest to newest = index n-1 down to 0).
	var modeStart time.Time
	var lastMode DrainMode
	first := true

	for i := n - 1; i >= 0; i-- {
		r := records[i]
		if first || r.DrainMode != lastMode {
			modeStart = r.Timestamp
			lastMode = r.DrainMode
			first = false
		}
		durations[i] = int(r.Timestamp.Sub(modeStart).Seconds())
	}
	return durations
}

func AuditToHistory(rec AuditRecord, stateDur *int) HistoryRecord {
	hr := HistoryRecord{
		Timestamp:            rec.Timestamp.Local().Format(time.RFC3339),
		Host:                 rec.Host,
		DrainMode:            rec.DrainLabel,
		DrainValue:           uint32(rec.DrainMode),
		Changed:              rec.Changed,
		ChangedBy:            rec.ChangedBy,
		StateDurationSeconds: stateDur,
		ActiveSessions:       rec.ActiveSessions,
		DisconnectedSessions: rec.DisconnectedSessions,
		TotalSessions:        rec.TotalSessions,
		MaxSessions:          rec.MaxSessions,
		ExitCode:             rec.ExitCode,
	}
	if rec.CPUPct != 0 {
		v := rec.CPUPct
		hr.CPUPct = &v
	}
	if rec.InputDelayMax != 0 {
		v := rec.InputDelayMax
		hr.InputDelayMax = &v
	}
	if rec.MemAvailMB != 0 {
		v := rec.MemAvailMB
		hr.MemAvailMB = &v
	}
	if rec.MemTotalMB != 0 {
		v := rec.MemTotalMB
		hr.MemTotalMB = &v
	}
	if rec.DiskQueue != 0 {
		v := rec.DiskQueue
		hr.DiskQueue = &v
	}
	if rec.TCPRetransSec != 0 {
		v := rec.TCPRetransSec
		hr.TCPRetransSec = &v
	}
	if !rec.KeyModified.IsZero() {
		hr.KeyModified = rec.KeyModified.Local().Format(time.RFC3339)
	}
	return hr
}

// WriteHistory renders audit records to w in the specified format.
func WriteHistory(w io.Writer, records []AuditRecord, format OutputFormat) {
	durations := ComputeStateDurations(records)

	switch format {
	case FormatJSON:
		out := make([]HistoryRecord, len(records))
		for i, r := range records {
			out[i] = AuditToHistory(r, &durations[i])
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)

	case FormatCSV:
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{
			"timestamp", "host", "drain_mode", "drain_mode_value",
			"key_modified", "state_duration_seconds",
			"changed", "changed_by",
			"active_sessions", "disconnected_sessions", "total_sessions", "max_sessions",
			"cpu_pct", "input_delay_max_ms",
			"exit_code",
		})
		for i, r := range records {
			hr := AuditToHistory(r, &durations[i])
			ch := ""
			if hr.Changed {
				ch = "true"
			}
			cpuStr, delayStr := "", ""
			if hr.CPUPct != nil {
				cpuStr = fmt.Sprintf("%.1f", *hr.CPUPct)
			}
			if hr.InputDelayMax != nil {
				delayStr = fmt.Sprintf("%.1f", *hr.InputDelayMax)
			}
			_ = cw.Write([]string{
				hr.Timestamp, hr.Host, hr.DrainMode,
				fmt.Sprintf("%d", hr.DrainValue),
				hr.KeyModified,
				fmt.Sprintf("%d", durations[i]),
				ch, hr.ChangedBy,
				fmt.Sprintf("%d", hr.ActiveSessions),
				fmt.Sprintf("%d", hr.DisconnectedSessions),
				fmt.Sprintf("%d", hr.TotalSessions),
				fmt.Sprintf("%d", hr.MaxSessions),
				cpuStr, delayStr,
				fmt.Sprintf("%d", hr.ExitCode),
			})
		}
		cw.Flush()

	case FormatTable:
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "TIMESTAMP\tDRAIN MODE\tSTATE DURATION\tCHANGED\tCHANGED BY\tSESSIONS\tCPU%\tINPUT DLY\tEXIT")
		_, _ = fmt.Fprintln(tw, "---------\t----------\t--------------\t-------\t----------\t--------\t----\t---------\t----")
		for i, r := range records {
			ch := ""
			if r.Changed {
				ch = "YES"
			}
			by := r.ChangedBy
			if by == "" {
				by = "-"
			}
			dur := (time.Duration(durations[i]) * time.Second).String()
			sess := "-"
			if r.MaxSessions > 0 {
				sess = fmt.Sprintf("%d/%d", r.TotalSessions, r.MaxSessions)
			} else if r.TotalSessions > 0 {
				sess = fmt.Sprintf("%d", r.TotalSessions)
			}
			cpuStr, delayStr := "-", "-"
			if r.CPUPct != 0 {
				cpuStr = fmt.Sprintf("%.0f%%", r.CPUPct)
			}
			if r.InputDelayMax != 0 {
				delayStr = fmt.Sprintf("%.0fms", r.InputDelayMax)
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
				r.Timestamp.Local().Format("2006-01-02 15:04:05"),
				r.DrainLabel, dur, ch, by, sess, cpuStr, delayStr, r.ExitCode,
			)
		}
		_ = tw.Flush()

	case FormatPlain:
		for _, r := range records {
			fields := []string{
				fmt.Sprintf("drain_mode=%s", r.DrainLabel),
				fmt.Sprintf("value=%d", r.DrainMode),
			}
			if r.Changed {
				fields = append(fields, "changed=true")
			}
			if r.ChangedBy != "" {
				fields = append(fields, fmt.Sprintf("changed_by=%s", r.ChangedBy))
			}
			if r.TotalSessions > 0 || r.MaxSessions > 0 {
				fields = append(fields,
					fmt.Sprintf("sessions=%d/%d", r.TotalSessions, r.MaxSessions),
					fmt.Sprintf("active=%d", r.ActiveSessions),
					fmt.Sprintf("disconnected=%d", r.DisconnectedSessions),
				)
			}
			fields = append(fields, fmt.Sprintf("exit=%d", r.ExitCode))

			ts := r.Timestamp.Local().Format(time.RFC3339)
			tag := "INF"
			if r.ExitCode > 0 {
				tag = "ERR"
			}
			_, _ = fmt.Fprintf(w, "%s [%s] %s\n", ts, tag, strings.Join(fields, " "))
		}
	}
}

// WriteHistoryRecords renders pre-computed HistoryRecord values to w.
// Used when records come from the pipe (already have durations computed).
func WriteHistoryRecords(w io.Writer, records []HistoryRecord, format OutputFormat) {
	switch format {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(records)

	case FormatCSV:
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{
			"timestamp", "host", "drain_mode", "drain_mode_value",
			"key_modified", "state_duration_seconds",
			"changed", "changed_by",
			"active_sessions", "disconnected_sessions", "total_sessions", "max_sessions",
			"cpu_pct", "input_delay_max_ms",
			"exit_code",
		})
		for _, hr := range records {
			ch := ""
			if hr.Changed {
				ch = "true"
			}
			dur := ""
			if hr.StateDurationSeconds != nil {
				dur = fmt.Sprintf("%d", *hr.StateDurationSeconds)
			}
			cpuStr, delayStr := "", ""
			if hr.CPUPct != nil {
				cpuStr = fmt.Sprintf("%.1f", *hr.CPUPct)
			}
			if hr.InputDelayMax != nil {
				delayStr = fmt.Sprintf("%.1f", *hr.InputDelayMax)
			}
			_ = cw.Write([]string{
				hr.Timestamp, hr.Host, hr.DrainMode,
				fmt.Sprintf("%d", hr.DrainValue),
				hr.KeyModified, dur,
				ch, hr.ChangedBy,
				fmt.Sprintf("%d", hr.ActiveSessions),
				fmt.Sprintf("%d", hr.DisconnectedSessions),
				fmt.Sprintf("%d", hr.TotalSessions),
				fmt.Sprintf("%d", hr.MaxSessions),
				cpuStr, delayStr,
				fmt.Sprintf("%d", hr.ExitCode),
			})
		}
		cw.Flush()

	case FormatTable:
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "TIMESTAMP\tDRAIN MODE\tSTATE DURATION\tCHANGED\tCHANGED BY\tSESSIONS\tCPU%\tINPUT DLY\tEXIT")
		_, _ = fmt.Fprintln(tw, "---------\t----------\t--------------\t-------\t----------\t--------\t----\t---------\t----")
		for _, hr := range records {
			ch := ""
			if hr.Changed {
				ch = "YES"
			}
			by := hr.ChangedBy
			if by == "" {
				by = "-"
			}
			dur := "0s"
			if hr.StateDurationSeconds != nil {
				dur = (time.Duration(*hr.StateDurationSeconds) * time.Second).String()
			}
			ts := hr.Timestamp
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				ts = t.Local().Format("2006-01-02 15:04:05")
			}
			sess := "-"
			if hr.MaxSessions > 0 {
				sess = fmt.Sprintf("%d/%d", hr.TotalSessions, hr.MaxSessions)
			} else if hr.TotalSessions > 0 {
				sess = fmt.Sprintf("%d", hr.TotalSessions)
			}
			cpuStr, delayStr := "-", "-"
			if hr.CPUPct != nil {
				cpuStr = fmt.Sprintf("%.0f%%", *hr.CPUPct)
			}
			if hr.InputDelayMax != nil {
				delayStr = fmt.Sprintf("%.0fms", *hr.InputDelayMax)
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
				ts, hr.DrainMode, dur, ch, by, sess, cpuStr, delayStr, hr.ExitCode,
			)
		}
		_ = tw.Flush()

	case FormatPlain:
		for _, hr := range records {
			fields := []string{
				fmt.Sprintf("drain_mode=%s", hr.DrainMode),
				fmt.Sprintf("value=%d", hr.DrainValue),
			}
			if hr.Changed {
				fields = append(fields, "changed=true")
			}
			if hr.ChangedBy != "" {
				fields = append(fields, fmt.Sprintf("changed_by=%s", hr.ChangedBy))
			}
			if hr.CPUPct != nil {
				fields = append(fields, fmt.Sprintf("cpu=%.0f%%", *hr.CPUPct))
			}
			if hr.InputDelayMax != nil {
				fields = append(fields, fmt.Sprintf("input_delay=%.0fms", *hr.InputDelayMax))
			}
			fields = append(fields, fmt.Sprintf("exit=%d", hr.ExitCode))
			tag := "INF"
			if hr.ExitCode > 0 {
				tag = "ERR"
			}
			_, _ = fmt.Fprintf(w, "%s [%s] %s\n", hr.Timestamp, tag, strings.Join(fields, " "))
		}
	}
}

// ---------------------------------------------------------------------------
// Session list
// ---------------------------------------------------------------------------

// WriteSessions renders a session list to w in the specified format.
// summary may be nil if session counts are unavailable.
func WriteSessions(w io.Writer, sessions []SessionInfo, summary *SessionSummary, format OutputFormat) {
	switch format {
	case FormatJSON:
		out := struct {
			Sessions []SessionInfo   `json:"sessions"`
			Summary  *SessionSummary `json:"summary,omitempty"`
		}{
			Sessions: sessions,
			Summary:  summary,
		}
		if out.Sessions == nil {
			out.Sessions = []SessionInfo{}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)

	case FormatCSV:
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"session_id", "user_name", "station", "state", "state_value"})
		for _, s := range sessions {
			_ = cw.Write([]string{
				fmt.Sprintf("%d", s.SessionID),
				s.UserName,
				s.Station,
				s.State,
				fmt.Sprintf("%d", s.StateValue),
			})
		}
		cw.Flush()

	case FormatTable:
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "SESSION ID\tUSER NAME\tSTATION\tSTATE")
		_, _ = fmt.Fprintln(tw, "----------\t---------\t-------\t-----")
		for _, s := range sessions {
			_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n",
				s.SessionID,
				or(s.UserName, "-"),
				or(s.Station, "-"),
				s.State,
			)
		}
		_ = tw.Flush()
		if summary != nil {
			_, _ = fmt.Fprintln(w, formatSessionSummaryLine(summary))
		}

	case FormatPlain:
		for _, s := range sessions {
			fields := []string{
				fmt.Sprintf("session_id=%d", s.SessionID),
				fmt.Sprintf("station=%s", or(s.Station, "-")),
				fmt.Sprintf("state=%s", s.State),
			}
			if s.UserName != "" {
				fields = append([]string{fmt.Sprintf("user=%s", s.UserName)}, fields...)
			}
			_, _ = fmt.Fprintf(w, "[INF] %s\n", strings.Join(fields, " "))
		}
		if summary != nil {
			_, _ = fmt.Fprintf(w, "[INF] %s\n", formatSessionSummaryLine(summary))
		}
	}
}

// formatSessionSummaryLine returns a human-readable summary of session counts.
func formatSessionSummaryLine(s *SessionSummary) string {
	if s == nil {
		return ""
	}
	line := fmt.Sprintf("sessions active=%d", s.ActiveSessions)
	if s.DisconnectedSessions > 0 {
		line += fmt.Sprintf(" disconnected=%d", s.DisconnectedSessions)
	}
	line += fmt.Sprintf(" total=%d", s.TotalSessions)
	if s.MaxSessions > 0 {
		line += fmt.Sprintf("/%d utilization=%d%%", s.MaxSessions, s.UtilizationPct)
	}
	return line
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func formatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Local().Format(time.RFC3339)
}

func formatFloatPtr(f *float64) string {
	if f == nil {
		return ""
	}
	return fmt.Sprintf("%.0f", *f)
}

func formatBoolPtr(b *bool) string {
	if b == nil {
		return "unknown"
	}
	return fmt.Sprintf("%t", *b)
}

// FormatBool formats an optional bool pointer for display.
func FormatBool(b *bool) string {
	return formatBoolPtr(b)
}

func formatAge(ageSeconds *float64) string {
	if ageSeconds == nil {
		return "n/a"
	}
	d := time.Duration(*ageSeconds) * time.Second
	return d.Truncate(time.Second).String()
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
