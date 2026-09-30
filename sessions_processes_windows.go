//go:build windows

package drainctl

import (
	"fmt"
	"path/filepath"
	"time"
	"unsafe"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
	"golang.org/x/sys/windows"
)

const processQueryLimitedInformation = 0x1000

var (
	modKernel32Processes               = windows.NewLazySystemDLL("kernel32.dll")
	procCreateToolhelp32Snapshot       = modKernel32Processes.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW                = modKernel32Processes.NewProc("Process32FirstW")
	procProcess32NextW                 = modKernel32Processes.NewProc("Process32NextW")
	procProcessIDToSessionID           = modKernel32Processes.NewProc("ProcessIdToSessionId")
	procOpenProcess                    = modKernel32Processes.NewProc("OpenProcess")
	modPSAPIProcesses                  = windows.NewLazySystemDLL("psapi.dll")
	procGetProcessMemoryInfoForSession = modPSAPIProcesses.NewProc("GetProcessMemoryInfo")
)

const th32csSnapProcess = 0x00000002

type processEntry32W struct {
	Size              uint32
	Usage             uint32
	ProcessID         uint32
	DefaultHeapID     uintptr
	ModuleID          uint32
	ThreadCount       uint32
	ParentProcessID   uint32
	PriorityClassBase int32
	Flags             uint32
	ExeFile           [windows.MAX_PATH]uint16
}

type sessionProcessMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

type processEntry struct {
	pid       uint32
	imageName string
}

type processSnapshot interface {
	Next() (processEntry, bool, error)
	Close() error
}

type processHandle interface {
	Times() (creation, cpu uint64, err error)
	WorkingSet() (uint64, error)
	Close() error
}

type processCollectionAPI interface {
	Snapshot() (processSnapshot, error)
	SessionID(pid uint32) (uint32, error)
	Open(pid uint32) (processHandle, error)
	Now() time.Time
}

type processBaselineKey struct {
	pid      uint32
	creation uint64
}

type processBaseline struct {
	cpu uint64
	at  time.Time
}

// ProcessCollector collects session-attributed processes without retaining OS
// handles. It retains only the current pass's scalar PID+creation CPU baselines.
type ProcessCollector struct {
	api         processCollectionAPI
	logicalCPUs uint16
	baselines   map[processBaselineKey]processBaseline
}

// NewProcessCollector constructs a Toolhelp-backed process collector. The CPU
// count is clamped to one because a zero divisor would fabricate CPU values.
func NewProcessCollector(logicalCPUs uint16) *ProcessCollector {
	return newProcessCollector(windowsProcessAPI{}, logicalCPUs)
}

func newProcessCollector(api processCollectionAPI, logicalCPUs uint16) *ProcessCollector {
	if logicalCPUs == 0 {
		logicalCPUs = 1
	}
	return &ProcessCollector{
		api:         api,
		logicalCPUs: logicalCPUs,
		baselines:   make(map[processBaselineKey]processBaseline),
	}
}

// CollectTopProcesses returns deterministic bounded process lists for exactly
// the supplied session IDs. A Toolhelp enumeration failure marks the optional
// process capability unavailable while preserving any caller-owned WTS rows.
func (c *ProcessCollector) CollectTopProcesses(sessionIDs []uint32, topN uint8) (map[uint32][]sessiondata.SessionProcess, bool) {
	processes := make(map[uint32][]sessiondata.SessionProcess, len(sessionIDs))
	wanted := make(map[uint32]struct{}, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		wanted[sessionID] = struct{}{}
		processes[sessionID] = []sessiondata.SessionProcess{}
	}
	if topN == 0 || len(wanted) == 0 {
		return processes, true
	}
	if topN > 5 {
		topN = 5
	}

	snapshot, err := c.api.Snapshot()
	if err != nil {
		return processes, false
	}
	defer func() { _ = snapshot.Close() }()

	heaps := make(map[uint32]sessiondata.ProcessHeap, len(wanted))
	for sessionID := range wanted {
		heaps[sessionID] = sessiondata.NewProcessHeap(int(topN))
	}
	nextBaselines := make(map[processBaselineKey]processBaseline)
	now := c.api.Now()
	for {
		entry, ok, nextErr := snapshot.Next()
		if nextErr != nil {
			return processes, false
		}
		if !ok {
			break
		}
		sessionID, sessionErr := c.api.SessionID(entry.pid)
		if sessionErr != nil {
			continue
		}
		if _, wantedSession := wanted[sessionID]; !wantedSession {
			continue
		}
		handle, openErr := c.api.Open(entry.pid)
		if openErr != nil {
			continue
		}
		candidate, baseline, include := c.readProcess(handle, entry, now)
		_ = handle.Close()
		if baseline != nil {
			nextBaselines[baseline.key] = baseline.value
		}
		if include {
			heap := heaps[sessionID]
			heap.Push(candidate)
			heaps[sessionID] = heap
		}
	}
	c.baselines = nextBaselines
	for sessionID, heap := range heaps {
		processes[sessionID] = heap.Processes()
	}
	return processes, true
}

type collectedBaseline struct {
	key   processBaselineKey
	value processBaseline
}

func (c *ProcessCollector) readProcess(handle processHandle, entry processEntry, now time.Time) (sessiondata.SessionProcess, *collectedBaseline, bool) {
	workingSet, memoryErr := handle.WorkingSet()
	if memoryErr != nil {
		return sessiondata.SessionProcess{}, nil, false
	}
	candidate := sessiondata.SessionProcess{
		PID:             entry.pid,
		ImageName:       filepath.Base(entry.imageName),
		WorkingSetBytes: sessiondata.DecimalUint64(workingSet),
	}
	creation, cpu, timesErr := handle.Times()
	if timesErr != nil {
		return candidate, nil, true
	}
	key := processBaselineKey{pid: entry.pid, creation: creation}
	baseline := collectedBaseline{key: key, value: processBaseline{cpu: cpu, at: now}}
	previous, exists := c.baselines[key]
	if !exists || !now.After(previous.at) || cpu < previous.cpu {
		return candidate, &baseline, true
	}
	elapsedTicks := float64(now.Sub(previous.at)) / 100
	if elapsedTicks <= 0 {
		return candidate, &baseline, true
	}
	cpuPercent := float64(cpu-previous.cpu) * 100 / elapsedTicks / float64(c.logicalCPUs)
	candidate.CPUPercent = &cpuPercent
	return candidate, &baseline, true
}

type windowsProcessAPI struct{}

func (windowsProcessAPI) Snapshot() (processSnapshot, error) {
	handle, _, err := procCreateToolhelp32Snapshot.Call(th32csSnapProcess, 0)
	if windows.Handle(handle) == windows.InvalidHandle {
		return nil, fmt.Errorf("CreateToolhelp32Snapshot: %w", err)
	}
	return &windowsProcessSnapshot{handle: windows.Handle(handle)}, nil
}

func (windowsProcessAPI) SessionID(pid uint32) (uint32, error) {
	var sessionID uint32
	result, _, err := procProcessIDToSessionID.Call(uintptr(pid), uintptr(unsafe.Pointer(&sessionID)))
	if result == 0 {
		return 0, fmt.Errorf("ProcessIdToSessionId: %w", err)
	}
	return sessionID, nil
}

func (windowsProcessAPI) Open(pid uint32) (processHandle, error) {
	handle, _, err := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if handle == 0 {
		return nil, fmt.Errorf("OpenProcess: %w", err)
	}
	return windowsSessionProcessHandle{handle: windows.Handle(handle)}, nil
}

func (windowsProcessAPI) Now() time.Time { return time.Now() }

type windowsProcessSnapshot struct {
	handle windows.Handle
	first  bool
}

func (s *windowsProcessSnapshot) Next() (processEntry, bool, error) {
	entry := processEntry32W{Size: uint32(unsafe.Sizeof(processEntry32W{}))}
	var result uintptr
	var err error
	if !s.first {
		s.first = true
		result, _, err = procProcess32FirstW.Call(uintptr(s.handle), uintptr(unsafe.Pointer(&entry)))
	} else {
		result, _, err = procProcess32NextW.Call(uintptr(s.handle), uintptr(unsafe.Pointer(&entry)))
	}
	if result == 0 {
		if err == windows.ERROR_NO_MORE_FILES {
			return processEntry{}, false, nil
		}
		return processEntry{}, false, fmt.Errorf("Process32Next: %w", err)
	}
	return processEntry{pid: entry.ProcessID, imageName: windows.UTF16ToString(entry.ExeFile[:])}, true, nil
}

func (s *windowsProcessSnapshot) Close() error { return windows.CloseHandle(s.handle) }

type windowsSessionProcessHandle struct{ handle windows.Handle }

func (h windowsSessionProcessHandle) Times() (uint64, uint64, error) {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h.handle, &creation, &exit, &kernel, &user); err != nil {
		return 0, 0, err
	}
	return filetimeTicks(creation), filetimeTicks(kernel) + filetimeTicks(user), nil
}

func (h windowsSessionProcessHandle) WorkingSet() (uint64, error) {
	counters := sessionProcessMemoryCounters{CB: uint32(unsafe.Sizeof(sessionProcessMemoryCounters{}))}
	result, _, err := procGetProcessMemoryInfoForSession.Call(
		uintptr(h.handle),
		uintptr(unsafe.Pointer(&counters)),
		uintptr(counters.CB),
	)
	if result == 0 {
		return 0, fmt.Errorf("GetProcessMemoryInfo: %w", err)
	}
	return uint64(counters.WorkingSetSize), nil
}

func (h windowsSessionProcessHandle) Close() error { return windows.CloseHandle(h.handle) }

func filetimeTicks(value windows.Filetime) uint64 {
	return uint64(value.HighDateTime)<<32 | uint64(value.LowDateTime)
}
