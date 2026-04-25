//go:build windows

package svc

import (
	"context"
	"log/slog"
	"runtime"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const selfMetricsInterval = 60 * time.Second

// processMemoryCounters mirrors the Windows PROCESS_MEMORY_COUNTERS struct
// (psapi.h). Used by GetProcessMemoryInfo to expose RSS / private bytes —
// the same numbers Task Manager shows under "Memory (private working set)"
// and that operators reach for first when checking process footprint.
type processMemoryCounters struct {
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

var (
	psapi                    = windows.NewLazySystemDLL("psapi.dll")
	procGetProcessMemoryInfo = psapi.NewProc("GetProcessMemoryInfo")
)

// startSelfMetricsLogger runs a ticker that emits a runtime + process
// snapshot at slog.Debug every selfMetricsInterval. Lets operators trend
// memory and goroutine behaviour without pprof — grep "selfmetrics=" out
// of the daily file log when log_file_level=debug.
//
// The first sample fires at startup (gives operators a baseline anchor),
// then every selfMetricsInterval until ctx is cancelled.
//
// Cost: runtime.ReadMemStats stops the world for ~hundreds of microseconds
// on a healthy heap. At 60 s cadence that's ~1.7 ppm of CPU. The
// GetProcessMemoryInfo syscall is a fast user-mode kernel call. When
// log_file_level is info or higher, slog.Debug returns immediately
// without formatting attributes — but ReadMemStats still runs. Cost is
// nominal even when the level filters output.
func startSelfMetricsLogger(ctx context.Context) {
	go func() {
		emitSelfMetrics()
		t := time.NewTicker(selfMetricsInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				emitSelfMetrics()
			}
		}
	}()
}

// emitSelfMetrics is also called from the rare hot-spot diagnostic paths
// where a synchronous "log it now" snapshot is more useful than waiting
// for the next tick. Exposed via the package boundary only via the ticker
// goroutine for now; future code can call this directly when needed.
func emitSelfMetrics() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	rss, pagefile := readProcessMemory()

	slog.Debug("selfmetrics",
		"heap_alloc_mb", m.HeapAlloc>>20,
		"heap_sys_mb", m.HeapSys>>20,
		"heap_inuse_mb", m.HeapInuse>>20,
		"heap_released_mb", m.HeapReleased>>20,
		"heap_objects", m.HeapObjects,
		"stack_inuse_mb", m.StackInuse>>20,
		"go_sys_mb", m.Sys>>20,
		"goroutines", runtime.NumGoroutine(),
		"num_gc", m.NumGC,
		"next_gc_mb", m.NextGC>>20,
		"gc_cpu_pct", uint32(m.GCCPUFraction*100),
		"rss_mb", rss>>20,
		"pagefile_mb", pagefile>>20,
		"cgo_calls", runtime.NumCgoCall(),
	)
}

// processMemoryFailedLogged ensures that GetProcessMemoryInfo failure
// only logs once. The OS-level numbers are nice-to-have; their absence
// shouldn't spam the log every minute.
var processMemoryFailedLogged atomic.Bool

func readProcessMemory() (rss, pagefile uint64) {
	var counters processMemoryCounters
	counters.CB = uint32(unsafe.Sizeof(counters))
	r, _, err := procGetProcessMemoryInfo.Call(
		uintptr(windows.CurrentProcess()),
		uintptr(unsafe.Pointer(&counters)),
		uintptr(counters.CB),
	)
	if r == 0 {
		if !processMemoryFailedLogged.Swap(true) {
			slog.Warn("selfmetrics: GetProcessMemoryInfo failed; rss/pagefile will report as 0", "error", err)
		}
		return 0, 0
	}
	return uint64(counters.WorkingSetSize), uint64(counters.PagefileUsage)
}
