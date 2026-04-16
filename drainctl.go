//go:build windows

// Package drainctl monitors Remote Desktop Session Host drain mode
// (TSServerDrainMode registry value) on Windows servers.
package drainctl

import "os"

// Version is the library version. Overridable via ldflags.
var Version = "26.105.47"

// ETW audit event IDs (5xxx) — routed to the Audit channel.
// Defined here so both internal/svc and internal/dashboard can reference them.
const (
	EvtDashboardAccess       = 5000 // user authenticated and accessed dashboard
	EvtDashboardConfigChange = 5001 // notification/perf config modified via dashboard
	EvtServerRegistered      = 5002 // host registered with dashboard
	EvtServerRemoved         = 5003 // host removed from dashboard
	EvtAccessDenied          = 5004 // authentication or authorization failure
	EvtGenericAudit          = 5099 // generic audit event
)

// DefaultDataDir returns the default directory for drainctl data files.
func DefaultDataDir() string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return pd + `\LISS Technologies\LISSTech DrainCtl`
}

// DefaultAuditPath returns the default path for the JSONL audit trail.
func DefaultAuditPath() string {
	return DefaultDataDir() + `\audit.jsonl`
}
