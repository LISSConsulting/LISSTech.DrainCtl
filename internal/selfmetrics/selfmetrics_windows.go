//go:build windows

// Package selfmetrics emits a periodic runtime + process snapshot at
// slog.Debug so operators can grep "selfmetrics=" out of the daily file
// log to trend memory, goroutines, GC, and SSPI counters without
// attaching pprof. See docs/architecture/lifecycle.md for the LCI rules
// this subsystem follows.
package selfmetrics

import (
	"context"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/lifecycle"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sspimetrics"
	"golang.org/x/sys/windows"
)

// Compile-time assertion: *Subsystem satisfies lifecycle.Subsystem.
var _ lifecycle.Subsystem = (*Subsystem)(nil)

// DefaultInterval is the production tick cadence. ReadMemStats stops the
// world for hundreds of microseconds on a healthy heap; at 60 s that's
// ~1.7 ppm of CPU.
const DefaultInterval = 60 * time.Second

// Subsystem owns the selfmetrics ticker goroutine. Construct via New,
// register alongside other lifecycle.Subsystems, call Start with a
// service-scoped ctx, call Stop on shutdown.
type Subsystem struct {
	interval time.Duration

	wg       sync.WaitGroup
	stopOnce sync.Once
}

// New constructs the Subsystem with the given tick interval. Pass
// DefaultInterval for production cadence; tests pass a smaller value to
// drive multiple emissions in a deterministic window.
func New(interval time.Duration) *Subsystem {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Subsystem{interval: interval}
}

// Start launches the ticker goroutine. Returns nil — there's no
// synchronous init that can fail. Goroutine exits when ctx is cancelled
// (the service is responsible for cancelling per LCI Stop contract).
func (s *Subsystem) Start(ctx context.Context) error {
	s.wg.Add(1)
	go s.run(ctx)
	return nil
}

// Stop blocks until the ticker goroutine has exited. Idempotent.
func (s *Subsystem) Stop() {
	s.stopOnce.Do(func() {
		s.wg.Wait()
	})
}

func (s *Subsystem) run(ctx context.Context) {
	defer s.wg.Done()
	emit()
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			emit()
		}
	}
}

// processMemoryCounters mirrors the Windows PROCESS_MEMORY_COUNTERS
// struct (psapi.h). Used by GetProcessMemoryInfo to expose RSS / private
// bytes — the same numbers Task Manager shows under "Memory (private
// working set)" and that operators reach for first when checking process
// footprint.
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

// emit writes one selfmetrics record at slog.Debug. Exposed within the
// package so tests can drive it synchronously without standing up the
// ticker.
func emit() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	rss, pagefile := readProcessMemory()
	sspiLive, sspiPending := sspimetrics.Snapshot()

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
		"sspi_live_contexts", sspiLive,
		"sspi_pending_contexts", sspiPending,
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
