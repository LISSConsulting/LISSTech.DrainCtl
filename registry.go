//go:build windows

package drainctl

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows/registry"
)

const (
	RegPath   = `SYSTEM\CurrentControlSet\Control\Terminal Server`
	ValueName = "TSServerDrainMode"
)

// DrainMode mirrors the Windows Terminal Server drain mode enum.
type DrainMode uint32

const (
	AllowAll        DrainMode = 0
	PreventNewLogon DrainMode = 1
	PreventUntilRST DrainMode = 2
)

func (m DrainMode) String() string {
	switch m {
	case AllowAll:
		return "ALLOW_ALL_CONNECTIONS"
	case PreventNewLogon:
		return "ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS"
	case PreventUntilRST:
		return "ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS_UNTIL_RESTART"
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
// current drain mode value along with the key's last-write timestamp.
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

	val, _, err := key.GetIntegerValue(ValueName)
	if err != nil {
		if err == registry.ErrNotExist {
			state.Mode = AllowAll
			state.ValuePresent = false
			return state, nil
		}
		return nil, fmt.Errorf("read %s: %w", ValueName, err)
	}

	state.Mode = DrainMode(val)
	state.ValuePresent = true

	ki, err := key.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat key: %w", err)
	}
	state.KeyModified = ki.ModTime()

	return state, nil
}
