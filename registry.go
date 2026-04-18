//go:build windows

package drainctl

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows/registry"
)

const (
	RegPath       = `SYSTEM\CurrentControlSet\Control\Terminal Server`
	ValueName     = "TSServerDrainMode"
	DenyValueName = "fDenyTSConnections"
)

// DrainMode is the unified state drainctl tracks for a host. Values 0..2
// mirror the Windows `TSServerDrainMode` registry enum. Value 3 (DenyAll) is
// synthesized: it means `fDenyTSConnections=1` is set, which bypasses the
// drain enum and blocks *all* connections (including reconnects to existing
// sessions). fDenyTSConnections takes precedence over TSServerDrainMode
// because its effect is stricter.
type DrainMode uint32

const (
	// AllowAll is the only connection-permitting state; all other DrainMode
	// values block new logons to some degree.
	AllowAll DrainMode = 0

	// DrainUntilBoot (registry value 1) prevents new logons but allows
	// reconnections to existing sessions. The drain resets automatically at
	// the next reboot — `Set-RDSessionHost -NewConnectionAllowed NotUntilReboot`
	// and `change logon /drainuntilrestart` both write this value.
	DrainUntilBoot DrainMode = 1

	// DrainPersistent (registry value 2) prevents new logons but allows
	// reconnections; survives reboot until an operator explicitly clears it.
	// `Set-RDSessionHost -NewConnectionAllowed No` and `change logon /drain`
	// both write this value.
	DrainPersistent DrainMode = 2

	// DenyAll is synthetic — it does not correspond to any TSServerDrainMode
	// value Windows writes. drainctl emits it when fDenyTSConnections=1
	// regardless of the underlying drain enum, matching a hard admin deny
	// (all logons blocked, no reconnection allowed).
	DenyAll DrainMode = 3

	// Deprecated: the old name was a misnomer — value 1 is the *temporary*
	// drain that resets at reboot, not a generic "prevent new logons" state.
	// Use DrainUntilBoot. Kept as an alias so external callers (DLL /
	// PowerShell module) that still reference the old name compile cleanly;
	// will be removed in a future release.
	PreventNewLogon = DrainUntilBoot

	// Deprecated: the old name was a misnomer — value 2 is the *persistent*
	// drain that survives reboot, not a "drain until restart" state.
	// Use DrainPersistent. Kept as an alias for the same reason as
	// PreventNewLogon; see that constant's doc.
	PreventUntilRST = DrainPersistent
)

func (m DrainMode) String() string {
	switch m {
	case AllowAll:
		return "ALLOW_ALL_CONNECTIONS"
	case DrainUntilBoot:
		return "ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS_UNTIL_RESTART"
	case DrainPersistent:
		return "ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS"
	case DenyAll:
		return "DENY_ALL_CONNECTIONS"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", m)
	}
}

// RegistryState holds the result of a registry probe.
type RegistryState struct {
	Host         string
	Mode         DrainMode
	KeyModified  time.Time
	ValuePresent bool
}

// ReadDrainMode opens the Terminal Server registry key and returns the
// effective drain mode along with the key's last-write timestamp.
//
// The effective mode composes two registry values: fDenyTSConnections takes
// precedence (1 → DenyAll) because its effect is strictly broader than any
// TSServerDrainMode setting — `change logon /disable` sets fDenyTSConnections
// directly and must appear in the history. When fDenyTSConnections is 0 or
// absent, the TSServerDrainMode enum value is returned unchanged.
func ReadDrainMode() (*RegistryState, error) {
	host, _ := os.Hostname()
	if host == "" {
		host = "UNKNOWN"
	}

	key, err := registry.OpenKey(registry.LOCAL_MACHINE, RegPath, registry.QUERY_VALUE)
	if err != nil {
		return nil, fmt.Errorf("open registry key: %w", err)
	}
	defer func() { _ = key.Close() }()

	state := &RegistryState{Host: host}

	// fDenyTSConnections takes precedence. Absent value is treated as 0.
	denyVal, _, denyErr := key.GetIntegerValue(DenyValueName)
	if denyErr != nil && denyErr != registry.ErrNotExist {
		return nil, fmt.Errorf("read %s: %w", DenyValueName, denyErr)
	}
	denyActive := denyErr == nil && denyVal != 0

	val, _, err := key.GetIntegerValue(ValueName)
	switch {
	case err == registry.ErrNotExist:
		state.ValuePresent = false
		if denyActive {
			state.Mode = DenyAll
		} else {
			state.Mode = AllowAll
		}
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", ValueName, err)
	default:
		state.ValuePresent = true
		if denyActive {
			state.Mode = DenyAll
		} else {
			state.Mode = DrainMode(val)
		}
	}

	ki, err := key.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat key: %w", err)
	}
	state.KeyModified = ki.ModTime()

	return state, nil
}
