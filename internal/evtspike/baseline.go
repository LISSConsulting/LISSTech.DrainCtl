//go:build windows

package evtspike

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/sys/windows"
)

// SchemaVersion is bumped on any breaking change to BaselineFile so LoadBaseline
// can reject mismatched files rather than silently misinterpret them.
const SchemaVersion = 1

// baselineMutexName is deliberately distinct from config.go's configMutexName —
// coupling baseline writes to config writes would block each other despite
// protecting unrelated state.
const baselineMutexName = `Global\DrainCtlEvtSpikeBaseline`

// RecentFlags is intentionally omitted — the 2-of-3 confirmation window refills
// within 30 s of restart, so persisting it buys nothing.
type ChannelState struct {
	Slots     [slotsPerDay]GammaState `json:"slots"`
	Global    GammaState              `json:"global"`
	LastAlert time.Time               `json:"last_alert"`
}

type BaselineFile struct {
	SchemaVersion int                     `json:"schema_version"`
	WrittenAt     time.Time               `json:"written_at"`
	Host          string                  `json:"host"`
	Channels      map[string]ChannelState `json:"channels"`
}

// WriteBaseline atomically serializes bf to path using the same pattern as
// config.go's saveConfigToFile: named-mutex guard, temp file, MoveFileEx with
// REPLACE_EXISTING | WRITE_THROUGH. Caller owns WrittenAt / Host / SchemaVersion
// population.
func WriteBaseline(path string, bf *BaselineFile) error {
	if bf == nil {
		return fmt.Errorf("write baseline: nil baseline")
	}
	if path == "" {
		return fmt.Errorf("write baseline: empty path")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create baseline dir: %w", err)
	}

	// Windows named mutexes are thread-owned: WaitForSingleObject and
	// ReleaseMutex must run on the same OS thread, so pin the goroutine.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	mutexName, _ := windows.UTF16PtrFromString(baselineMutexName)
	mutex, err := windows.CreateMutex(nil, false, mutexName)
	if err != nil && err != windows.ERROR_ALREADY_EXISTS {
		return fmt.Errorf("create baseline mutex: %w", err)
	}
	defer func() { _ = windows.CloseHandle(mutex) }()

	event, _ := windows.WaitForSingleObject(mutex, 5000)
	if event == uint32(windows.WAIT_TIMEOUT) {
		return fmt.Errorf("baseline mutex timeout")
	}
	defer func() { _ = windows.ReleaseMutex(mutex) }()

	data, err := json.MarshalIndent(bf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal baseline: %w", err)
	}
	data = append(data, '\n')

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("write temp baseline: %w", err)
	}

	src, _ := windows.UTF16PtrFromString(tmpPath)
	dst, _ := windows.UTF16PtrFromString(path)
	if err := windows.MoveFileEx(src, dst, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		if renameErr := os.Rename(tmpPath, path); renameErr != nil {
			return fmt.Errorf("rename baseline: %w (movefileex: %v)", renameErr, err)
		}
	}

	return nil
}
