//go:build windows

package etwids

// Audit ETW event IDs (5xxx) routed to the Audit channel.
const (
	EvtDashboardAccess       = 5000 // user authenticated and accessed dashboard
	EvtDashboardConfigChange = 5001 // notification/perf config modified via dashboard
	EvtServerRegistered      = 5002 // host registered with dashboard
	EvtServerRemoved         = 5003 // host removed from dashboard
	EvtAccessDenied          = 5004 // authentication or authorization failure
	EvtGenericAudit          = 5099 // generic audit event
)
