//go:build windows

package drainctl

import (
	"fmt"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessionlimit"

	"golang.org/x/sys/windows/registry"
)

// WTS session states.
const (
	wtsActive       = 0
	wtsConnected    = 1
	wtsConnectQuery = 2
	wtsShadow       = 3
	wtsDisconnected = 4
	wtsIdle         = 5
	wtsListen       = 6
	wtsReset        = 7
	wtsDown         = 8
	wtsInit         = 9
)

// SessionInfo describes a single RDS session.
type SessionInfo struct {
	SessionID  uint32 `json:"session_id"`
	UserName   string `json:"user_name,omitempty"`
	Station    string `json:"station"`
	State      string `json:"state"`
	StateValue uint32 `json:"state_value"`
}

// SessionSummary is the aggregate session data included in CheckResult.
type SessionSummary struct {
	ActiveSessions       int `json:"active_sessions"`
	DisconnectedSessions int `json:"disconnected_sessions"`
	TotalSessions        int `json:"total_sessions"`
	MaxSessions          int `json:"max_sessions"`
	UtilizationPct       int `json:"utilization_pct"`
}

func wtsStateName(state uint32) string {
	switch state {
	case wtsActive:
		return "Active"
	case wtsConnected:
		return "Connected"
	case wtsConnectQuery:
		return "ConnectQuery"
	case wtsShadow:
		return "Shadow"
	case wtsDisconnected:
		return "Disconnected"
	case wtsIdle:
		return "Idle"
	case wtsListen:
		return "Listen"
	case wtsReset:
		return "Reset"
	case wtsDown:
		return "Down"
	case wtsInit:
		return "Init"
	default:
		return fmt.Sprintf("Unknown(%d)", state)
	}
}

// EnumerateSessions returns the current RDS sessions on this server.
// Filters out the Services session (ID 0) and listener sessions.
//
// This compatibility view intentionally retains the fields used by the CLI and
// summary callers. New collectors should use EnumerateSessionRecords.
func EnumerateSessions() ([]SessionInfo, error) {
	records, err := EnumerateSessionRecords()
	if err != nil {
		return nil, err
	}

	sessions := make([]SessionInfo, 0, len(records))
	for _, record := range records {
		userName := ""
		if record.User != nil {
			userName = *record.User
		}
		sessions = append(sessions, SessionInfo{
			SessionID:  record.SessionID,
			UserName:   userName,
			Station:    stringValue(record.Station),
			State:      wtsStateNameFromSessionState(string(record.State)),
			StateValue: wtsStateValue(string(record.State)),
		})
	}
	return sessions, nil
}

var sessionLimitLocations = [...]struct {
	path string
	name string
}{
	{`SOFTWARE\Policies\Microsoft\Windows NT\Terminal Services`, "MaxInstanceCount"},
	{`SYSTEM\CurrentControlSet\Control\Terminal Server\WinStations\RDP-Tcp`, "MaxInstanceCount"},
	{`SYSTEM\CurrentControlSet\Control\Terminal Server`, "MaxInstanceCount"},
	{`SYSTEM\CurrentControlSet\Control\Terminal Server`, "UserSessionLimit"},
}

// ReadMaxSessions reads the effective finite RD Session Host connection limit.
// Policy is authoritative, followed by the listener's runtime configuration,
// the legacy root MaxInstanceCount, and UserSessionLimit. Returns 0 when no
// finite limit is configured.
func ReadMaxSessions() int {
	for _, location := range sessionLimitLocations {
		key, err := registry.OpenKey(registry.LOCAL_MACHINE, location.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		value, _, valueErr := key.GetIntegerValue(location.name)
		_ = key.Close()
		if valueErr != nil {
			continue
		}
		if limit, ok := sessionlimit.Normalize(value); ok {
			return limit
		}
	}
	return 0
}

// ComputeSessionSummary builds a SessionSummary from a pre-enumerated session
// list and the configured max-sessions cap. Callers that already hold the
// session list should use this instead of GetSessionSummary to avoid a second
// WTS API call.
func ComputeSessionSummary(sessions []SessionInfo, maxSessions int) *SessionSummary {
	maxSessions, _ = sessionlimit.Normalize(uint64(maxSessions))
	summary := &SessionSummary{MaxSessions: maxSessions}
	for _, s := range sessions {
		switch s.StateValue {
		case wtsActive:
			summary.ActiveSessions++
			summary.TotalSessions++
		case wtsDisconnected:
			summary.DisconnectedSessions++
			summary.TotalSessions++
		}
	}
	if summary.MaxSessions > 0 && summary.TotalSessions > 0 {
		summary.UtilizationPct = (summary.TotalSessions * 100) / summary.MaxSessions
		if summary.UtilizationPct > 100 {
			summary.UtilizationPct = 100
		}
	}
	return summary
}

// GetSessionSummary returns an aggregate summary of current RDS sessions.
// Returns nil (not an error) if session enumeration fails (e.g., non-admin).
func GetSessionSummary() *SessionSummary {
	sessions, err := EnumerateSessions()
	if err != nil {
		return nil
	}
	return ComputeSessionSummary(sessions, ReadMaxSessions())
}
