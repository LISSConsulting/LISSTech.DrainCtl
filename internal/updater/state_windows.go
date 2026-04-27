//go:build windows

package updater

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"golang.org/x/sys/windows"
)

// updateState is the persisted-across-restarts replay-defense state.
// HighestSeenVersion is the highest CalVer tag the updater has ever
// successfully verified-and-installed (or seeded from dc.Version on
// first Start). The replay/freeze gate refuses any remote release
// strictly lower than this value, so a network-positioned attacker
// cannot stall a fleet on a previously-published-but-stale signed MSI.
type updateState struct {
	HighestSeenVersion string `json:"highest_seen_version"`
}

// updateStateMutexName is deliberately distinct from
// internal/evtspike's baselineMutexName and config.go's configMutexName
// — sharing one mutex across unrelated state files would needlessly
// serialise unrelated writes.
const updateStateMutexName = `Global\DrainCtlUpdaterState`

// updateStateFilename lives next to config.json under
// %ProgramData%\LISS Technologies\LISSTech DrainCtl\.
const updateStateFilename = "update-state.json"

// maxUpdateStateFileSize caps how much we'll read off disk. The actual
// file is tiny (a single string field); this guards against a poisoned
// file on disk consuming memory.
const maxUpdateStateFileSize = 64 * 1024

// Test seams. Default to the production paths/implementations; tests
// override via the standard prev := X; t.Cleanup(...) ; X = fake pattern.
var (
	updateStatePath = defaultUpdateStatePath
	loadUpdateState = loadUpdateStateImpl
	saveUpdateState = saveUpdateStateImpl
)

func defaultUpdateStatePath() string {
	return filepath.Join(dc.DefaultDataDir(), updateStateFilename)
}

// loadUpdateStateImpl reads the on-disk state, returning the zero value
// (no warning) if the file is absent and the zero value plus a slog.Warn
// on any other read/parse failure. NEVER returns an error: a corrupt
// state file MUST NOT brick the updater. The next saveUpdateState call
// overwrites whatever was on disk.
func loadUpdateStateImpl() (updateState, error) {
	path := updateStatePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return updateState{}, nil
		}
		slog.Warn("update=state_unreadable", "path", path, "error", err.Error())
		return updateState{}, nil
	}
	if int64(len(data)) > maxUpdateStateFileSize {
		slog.Warn("update=state_oversize", "path", path, "size", len(data))
		return updateState{}, nil
	}
	var s updateState
	if err := json.Unmarshal(data, &s); err != nil {
		slog.Warn("update=state_corrupt", "path", path, "error", err.Error())
		return updateState{}, nil
	}
	return s, nil
}

// saveUpdateStateImpl writes s atomically using the named-mutex +
// MoveFileEx + WRITE_THROUGH pattern from internal/evtspike/baseline.go.
// Caller's responsibility to compute the correct value (see Step 8 of
// the remediation plan for the persistence rule).
func saveUpdateStateImpl(s updateState) error {
	path := updateStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	// Windows named mutexes are thread-owned: WaitForSingleObject and
	// ReleaseMutex must run on the same OS thread, so pin the goroutine
	// (mirrors evtspike/baseline.go:78).
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	mutexName, _ := windows.UTF16PtrFromString(updateStateMutexName)
	mutex, err := windows.CreateMutex(nil, false, mutexName)
	if err != nil && err != windows.ERROR_ALREADY_EXISTS {
		return fmt.Errorf("create update state mutex: %w", err)
	}
	defer func() { _ = windows.CloseHandle(mutex) }()

	event, _ := windows.WaitForSingleObject(mutex, 5000)
	if event == uint32(windows.WAIT_TIMEOUT) {
		return fmt.Errorf("update state mutex timeout")
	}
	defer func() { _ = windows.ReleaseMutex(mutex) }()

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal update state: %w", err)
	}
	data = append(data, '\n')

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("write temp update state: %w", err)
	}

	src, _ := windows.UTF16PtrFromString(tmpPath)
	dst, _ := windows.UTF16PtrFromString(path)
	if err := windows.MoveFileEx(src, dst, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		// Fallback: best-effort os.Rename. The MoveFileEx path is
		// preferred for the WRITE_THROUGH guarantee, but we don't want
		// a transient flakiness in the syscall to block a state save.
		if renameErr := os.Rename(tmpPath, path); renameErr != nil {
			return fmt.Errorf("rename update state: %w (movefileex: %v)", renameErr, err)
		}
	}
	return nil
}

// seedHighestSeenFromVersion advances HighestSeenVersion to dc.Version
// if dc.Version is greater. Called once at Subsystem.Start so a fresh
// install at v50 cannot be rolled back to a signed v40 before any poll
// has run. Failure to load or save is logged and ignored — replay
// defense is best-effort, not a service-block.
func seedHighestSeenFromVersion() {
	cur, err := parseVersion(dc.Version)
	if err != nil {
		// dc.Version is "dev" under `go test`; legitimate parse failures
		// in production would be a build-time bug. Either way, no seed.
		return
	}
	state, _ := loadUpdateState() // never returns err today
	if state.HighestSeenVersion != "" {
		if hs, err := parseVersion(state.HighestSeenVersion); err == nil {
			if !hs.less(cur) {
				return // existing highest already >= cur
			}
		}
	}
	state.HighestSeenVersion = cur.String()
	if err := saveUpdateState(state); err != nil {
		slog.Warn("update=state_seed_failed", "error", err.Error())
	}
}
