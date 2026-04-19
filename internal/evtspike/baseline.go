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

// maxBaselineFileSize caps how large a baseline file may grow before we
// consider it tampered and refuse to load. 16 MB is ~60x the natural upper
// bound of a 54-channel baseline (~260 KB) — big enough to never trip on
// legitimate growth, small enough that an oversize file cannot pressure
// memory during load.
const maxBaselineFileSize = 16 << 20

// gammaStateMaxAlphaBeta clamps loaded Alpha/Beta so a tampered file cannot
// poison the detector with values that would overflow later arithmetic.
// 1e9 is ~300 years of worst-case observations, far above any real run.
// Zero is a legitimate un-observed-slot value and is not clamped up.
const gammaStateMaxAlphaBeta = 1e9

// gammaStateMaxN caps N similarly. Observation counts in realistic runs
// stay under 1M; 1e9 is defensive.
const gammaStateMaxN = 1_000_000_000

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

// LoadBaseline reads and deserializes a baseline from path per data-model.md
// §3's startup protocol. The error return is reserved for future fatal
// conditions (e.g. empty path) — today every recoverable case returns a
// fresh baseline so subsystem start is never blocked on a missing or
// damaged file.
func LoadBaseline(path string) (*BaselineFile, error) {
	if path == "" {
		return nil, fmt.Errorf("load baseline: empty path")
	}

	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			slog.Warn("", "evtspike", "baseline_missing", "path", path)
			return freshBaseline(), nil
		}
		slog.Warn("", "evtspike", "baseline_unreadable", "path", path, "error", err.Error())
		return freshBaseline(), nil
	}
	if fi.Size() > maxBaselineFileSize {
		renamed := renameWithSuffix(path, "oversize")
		slog.Warn("", "evtspike", "baseline_oversize", "path", path, "renamed", renamed, "size", fi.Size())
		return freshBaseline(), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("", "evtspike", "baseline_unreadable", "path", path, "error", err.Error())
		return freshBaseline(), nil
	}

	var bf BaselineFile
	if err := json.Unmarshal(data, &bf); err != nil {
		renamed := renameWithSuffix(path, "corrupt")
		slog.Warn("", "evtspike", "baseline_corrupt", "path", path, "renamed", renamed, "error", err.Error())
		return freshBaseline(), nil
	}

	// Strict schema equality: reject future AND malformed (zero/negative)
	// versions. All three end up archived as .incompat-<ts>.bak.
	if bf.SchemaVersion != SchemaVersion {
		renamed := renameWithSuffix(path, "incompat")
		slog.Warn("", "evtspike", "baseline_incompat", "path", path, "renamed", renamed, "schema_version", bf.SchemaVersion)
		return freshBaseline(), nil
	}

	clampLoadedBaseline(&bf, path)
	return &bf, nil
}

// clampLoadedBaseline guards the detector against tampered/out-of-band
// values from a loaded baseline file. Zero values are LEGITIMATE — an
// un-observed slot naturally has Alpha=Beta=N=0 — so we only clamp
// negative or overflow. Any clamp emits a single WARN with the path.
func clampLoadedBaseline(bf *BaselineFile, path string) {
	clamped := 0
	clampGamma := func(g *GammaState) {
		if g.Alpha < 0 {
			g.Alpha = 0
			clamped++
		} else if g.Alpha > gammaStateMaxAlphaBeta {
			g.Alpha = gammaStateMaxAlphaBeta
			clamped++
		}
		if g.Beta < 0 {
			g.Beta = 0
			clamped++
		} else if g.Beta > gammaStateMaxAlphaBeta {
			g.Beta = gammaStateMaxAlphaBeta
			clamped++
		}
		if g.N < 0 {
			g.N = 0
			clamped++
		} else if g.N > gammaStateMaxN {
			g.N = gammaStateMaxN
			clamped++
		}
	}
	for name, cs := range bf.Channels {
		for i := range cs.Slots {
			clampGamma(&cs.Slots[i])
		}
		clampGamma(&cs.Global)
		bf.Channels[name] = cs
	}
	if clamped > 0 {
		slog.Warn("", "evtspike", "baseline_clamped", "path", path, "values_clamped", clamped)
	}
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
