//go:build windows

package drainctl

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	modWtsapi32                     = windows.NewLazySystemDLL("wtsapi32.dll")
	procWTSEnumerateSessionsW       = modWtsapi32.NewProc("WTSEnumerateSessionsW")
	procWTSQuerySessionInformationW = modWtsapi32.NewProc("WTSQuerySessionInformationW")
	procWTSFreeMemory               = modWtsapi32.NewProc("WTSFreeMemory")
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

// WTS query classes.
const (
	wtsUserName = 5 // WTSInfoClass: WTSUserName
)

// wtsSessionInfoW matches the WTS_SESSION_INFOW structure layout.
type wtsSessionInfoW struct {
	SessionID      uint32
	WinStationName *uint16
	State          uint32
}

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
func EnumerateSessions() ([]SessionInfo, error) {
	var pSessionInfo unsafe.Pointer
	var count uint32

	ret, _, err := procWTSEnumerateSessionsW.Call(
		0, // WTS_CURRENT_SERVER_HANDLE
		0, // Reserved
		1, // Version (must be 1)
		uintptr(unsafe.Pointer(&pSessionInfo)),
		uintptr(unsafe.Pointer(&count)),
	)
	if ret == 0 {
		return nil, fmt.Errorf("WTSEnumerateSessionsW: %w", err)
	}
	defer func() { _, _, _ = procWTSFreeMemory.Call(uintptr(pSessionInfo)) }()

	entrySize := unsafe.Sizeof(wtsSessionInfoW{})
	var sessions []SessionInfo

	for i := range count {
		entry := (*wtsSessionInfoW)(unsafe.Add(pSessionInfo, uintptr(i)*entrySize))

		// Skip Services session and listener sessions.
		if entry.SessionID == 0 {
			continue
		}
		if entry.State == wtsListen {
			continue
		}

		station := ""
		if entry.WinStationName != nil {
			station = windows.UTF16PtrToString(entry.WinStationName)
		}

		si := SessionInfo{
			SessionID:  entry.SessionID,
			Station:    station,
			State:      wtsStateName(entry.State),
			StateValue: entry.State,
		}

		// Query username.
		si.UserName = querySessionString(entry.SessionID, wtsUserName)

		sessions = append(sessions, si)
	}

	return sessions, nil
}

// querySessionString queries a string property for the given session.
func querySessionString(sessionID uint32, infoClass uint32) string {
	var buf *uint16
	var bytesReturned uint32

	ret, _, _ := procWTSQuerySessionInformationW.Call(
		0, // WTS_CURRENT_SERVER_HANDLE
		uintptr(sessionID),
		uintptr(infoClass),
		uintptr(unsafe.Pointer(&buf)),
		uintptr(unsafe.Pointer(&bytesReturned)),
	)
	if ret == 0 || buf == nil {
		return ""
	}
	defer func() { _, _, _ = procWTSFreeMemory.Call(uintptr(unsafe.Pointer(buf))) }()

	return windows.UTF16PtrToString(buf)
}

const (
	unlimitedDWORDSessionLimit  = uint64(1<<32 - 1)
	unlimitedPolicySessionLimit = uint64(999999)
)

var sessionLimitLocations = [...]struct {
	path string
	name string
}{
	{`SOFTWARE\Policies\Microsoft\Windows NT\Terminal Services`, "MaxInstanceCount"},
	{`SYSTEM\CurrentControlSet\Control\Terminal Server\WinStations\RDP-Tcp`, "MaxInstanceCount"},
	{`SYSTEM\CurrentControlSet\Control\Terminal Server`, "MaxInstanceCount"},
	{`SYSTEM\CurrentControlSet\Control\Terminal Server`, "UserSessionLimit"},
}

// normalizeSessionLimit rejects the sentinel values Windows uses for an
// unlimited RD Session Host connection policy.
func normalizeSessionLimit(v uint64) (int, bool) {
	if v == 0 || v == unlimitedDWORDSessionLimit || v == unlimitedPolicySessionLimit {
		return 0, false
	}
	return int(v), true
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
		if limit, ok := normalizeSessionLimit(value); ok {
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
	summary := &SessionSummary{
		MaxSessions: maxSessions,
	}
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
