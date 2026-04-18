//go:build windows

package drainctl

import "time"

// AuditRecord represents a single drain-mode observation. The service and
// telemetry store are the canonical write paths; this type is retained in
// the root package because the public DLL and CLI surfaces marshal it to
// external callers (P/Invoke, PowerShell module).
type AuditRecord struct {
	Timestamp            time.Time `json:"ts"`
	Host                 string    `json:"host"`
	DrainMode            DrainMode `json:"mode"`
	DrainLabel           string    `json:"mode_label"`
	KeyModified          time.Time `json:"key_modified,omitempty"`
	Changed              bool      `json:"changed,omitempty"`
	ChangedBy            string    `json:"changed_by,omitempty"`
	ActiveSessions       int       `json:"active_sessions,omitempty"`
	DisconnectedSessions int       `json:"disconnected_sessions,omitempty"`
	TotalSessions        int       `json:"total_sessions,omitempty"`
	MaxSessions          int       `json:"max_sessions,omitempty"`
	CPUPct               float64   `json:"cpu_pct,omitempty"`
	InputDelayMax        float64   `json:"input_delay_max_ms,omitempty"`
	MemAvailMB           float64   `json:"mem_avail_mb,omitempty"`
	MemTotalMB           float64   `json:"mem_total_mb,omitempty"`
	DiskQueue            float64   `json:"disk_queue,omitempty"`
	TCPRetransSec        float64   `json:"tcp_retrans_sec,omitempty"`
	ExitCode             int       `json:"exit"`
	// Reconciliation marks rows written by the drift reconciler at service
	// startup (T025 / FR-001a): the service was down, so the state change is
	// observed only as a before/after delta with no principal.
	Reconciliation bool   `json:"reconciliation,omitempty"`
	Reason         string `json:"reason,omitempty"`
}
