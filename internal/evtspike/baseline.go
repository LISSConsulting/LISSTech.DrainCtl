//go:build windows

package evtspike

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
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

// LoadBaseline reads and deserializes a baseline from path, handling the four
// startup cases in data-model.md §3 load protocol:
//
//  1. File missing — warn, return fresh BaselineFile, NO rename (FR-019).
//  2. File unreadable (AV lock / transient EACCES) — warn, return fresh, NO
//     rename so the next load can retry once the lock clears.
//  3. JSON unmarshal error — warn, rename to .corrupt-YYYYMMDD-HHMMSS.bak,
//     return fresh.
//  4. SchemaVersion > 1 — warn, rename to .incompat-YYYYMMDD-HHMMSS.bak,
//     return fresh.
//
// LoadBaseline never returns a non-nil error in current code paths; the
// signature reserves room for future fatal conditions (e.g., empty path) that
// should block subsystem start rather than silently rebuild state.
func LoadBaseline(path string) (*BaselineFile, error) {
	if path == "" {
		return nil, fmt.Errorf("load baseline: empty path")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			slog.Warn("", "evtspike", "baseline_missing", "path", path)
			return freshBaseline(), nil
		}
		slog.Warn("", "evtspike", "baseline_unreadable", "path", path, "error", err.Error())
		return freshBaseline(), nil
	}

	var bf BaselineFile
	if err := json.Unmarshal(data, &bf); err != nil {
		renamed := renameWithSuffix(path, "corrupt")
		slog.Warn("", "evtspike", "baseline_corrupt", "path", path, "renamed", renamed, "error", err.Error())
		return freshBaseline(), nil
	}

	if bf.SchemaVersion > SchemaVersion {
		renamed := renameWithSuffix(path, "incompat")
		slog.Warn("", "evtspike", "baseline_incompat", "path", path, "renamed", renamed, "schema_version", bf.SchemaVersion)
		return freshBaseline(), nil
	}

	return &bf, nil
}

func freshBaseline() *BaselineFile {
	return &BaselineFile{
		SchemaVersion: SchemaVersion,
		Channels:      make(map[string]ChannelState),
	}
}

// renameWithSuffix moves path aside to path.<kind>-YYYYMMDD-HHMMSS.bak. Best
// effort: if the rename fails (antivirus still holds the handle, permissions
// changed out from under us), we log and keep going — the caller's goal is to
// rebuild fresh state, not to guarantee the bad file is archived.
func renameWithSuffix(path, kind string) string {
	ts := time.Now().UTC().Format("20060102-150405")
	renamed := fmt.Sprintf("%s.%s-%s.bak", path, kind, ts)
	if err := os.Rename(path, renamed); err != nil {
		slog.Warn("", "evtspike", "baseline_rename_failed", "path", path, "target", renamed, "error", err.Error())
		return ""
	}
	return renamed
}
