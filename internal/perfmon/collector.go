//go:build windows

package perfmon

import (
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"syscall"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"golang.org/x/sys/windows"
)

// getThreadID returns the current OS thread ID for diagnostic logging.
func getThreadID() uint32 {
	return windows.GetCurrentThreadId()
}

// Counter paths. V1 (PerfLib) and V2 (PCW/ETW) counters must live in
// separate PDH queries — on Server 2022+, mixing them in one query
// causes PDH to skip V1 provider DLL loading.
const (
	// V1 host-level counters.
	counterCPU        = `\Processor Information(_Total)\% Processor Time`
	counterMemAvail   = `\Memory\Available MBytes`
	counterPagesSec   = `\Memory\Pages/sec`
	counterDiskQueue  = `\PhysicalDisk(_Total)\Avg. Disk Queue Length`
	counterTCPRetrans = `\TCPv4\Segments Retransmitted/sec`

	// V2 per-session counters.
	counterInputDelay = `\User Input Delay per Session(*)\Max Input Delay`
	counterSessCPU    = `\Terminal Services Session(*)\% Processor Time`
	counterSessMem    = `\Terminal Services Session(*)\Working Set`

	// V2 RemoteFX counters.
	counterRFXFPS     = `\RemoteFX Graphics(*)\Output Frames/Second`
	counterRFXSkipSrv = `\RemoteFX Graphics(*)\Frames Skipped/Second - Insufficient Server Resources`
	counterRFXSkipNet = `\RemoteFX Graphics(*)\Frames Skipped/Second - Insufficient Network Resources`
	counterRFXEncode  = `\RemoteFX Graphics(*)\Average Encoding Time`
	counterRFXQuality = `\RemoteFX Graphics(*)\Frame Quality`
	counterRFXRTT     = `\RemoteFX Network(*)\Current TCP RTT`
	counterRFXLoss    = `\RemoteFX Network(*)\Loss Rate`
)

// request is a command dispatched to the PDH worker goroutine.
type request struct {
	fn   func()
	done chan struct{}
}

// Collector manages PDH performance counter queries. All PDH syscalls run
// on a dedicated goroutine pinned to a Go-created OS thread — Windows
// services run their handler on an SCM-created thread that cannot load
// V1 PerfLib provider DLLs.
type Collector struct {
	hostQuery    syscall.Handle // V1: Processor, Memory, PhysicalDisk, TCPv4
	sessionQuery syscall.Handle // V2: per-session, RemoteFX

	cpuH, memH, pagesH, diskH, retransH syscall.Handle

	collectPerSession bool
	inputDelayH       syscall.Handle
	sessCPUH          syscall.Handle
	sessMemH          syscall.Handle
	inputDelayAvail   bool

	collectRemoteFX bool
	rfxAvailable    bool
	rfxFPSH         syscall.Handle
	rfxSkipSrvH     syscall.Handle
	rfxSkipNetH     syscall.Handle
	rfxEncH         syscall.Handle
	rfxQualH        syscall.Handle
	rfxRTTH         syscall.Handle
	rfxLossH        syscall.Handle

	memTotalMB      float64
	primed          bool
	skipNextCollect bool

	sampleInterval time.Duration     // how often the sampler collects (default 30s)
	accum          []dc.PerfSnapshot // samples accumulated between Collect() calls

	loggedErrors map[string]bool
	reqCh        chan request
	stopCh       chan struct{} // closed by Close() to stop sampler
}

// do dispatches fn to the dedicated PDH thread and blocks until completion.
func (c *Collector) do(fn func()) {
	r := request{fn: fn, done: make(chan struct{})}
	c.reqCh <- r
	<-r.done
}

func startWorker() chan request {
	ch := make(chan request)
	go func() {
		runtime.LockOSThread()
		for r := range ch {
			r.fn()
			close(r.done)
		}
	}()
	return ch
}

// Open creates the PDH queries and adds counters.
func Open(cfg dc.PerformanceConfig) (*Collector, error) {
	sampleInterval := time.Duration(cfg.SampleIntervalSec) * time.Second
	if sampleInterval < 10*time.Second || sampleInterval > 300*time.Second {
		sampleInterval = 30 * time.Second
	}
	c := &Collector{
		collectPerSession: cfg.CollectPerSession,
		collectRemoteFX:   cfg.CollectRemoteFX,
		memTotalMB:        totalPhysicalMemoryMB(),
		sampleInterval:    sampleInterval,
		loggedErrors:      make(map[string]bool),
		reqCh:             startWorker(),
		stopCh:            make(chan struct{}),
	}
	var err error
	c.do(func() { err = c.open() })
	if err != nil {
		close(c.reqCh)
		return nil, err
	}
	return c, nil
}

func (c *Collector) open() error {
	slog.Debug("perfmon: open", "tid", getThreadID())
	// --- V1 host query ---
	hq, err := pdhOpenQuery()
	if err != nil {
		return err
	}
	c.hostQuery = hq
	slog.Debug(fmt.Sprintf("perfmon: host_query_opened handle=0x%X", hq))

	add := func(path string) (syscall.Handle, error) { return pdhAddCounter(hq, path) }

	if c.cpuH, err = add(counterCPU); err != nil {
		pdhCloseQuery(hq)
		return fmt.Errorf("required counter %s: %w", counterCPU, err)
	}
	if c.memH, err = add(counterMemAvail); err != nil {
		pdhCloseQuery(hq)
		return fmt.Errorf("required counter %s: %w", counterMemAvail, err)
	}
	if c.pagesH, err = add(counterPagesSec); err != nil {
		pdhCloseQuery(hq)
		return fmt.Errorf("required counter %s: %w", counterPagesSec, err)
	}
	if c.diskH, err = add(counterDiskQueue); err != nil {
		pdhCloseQuery(hq)
		return fmt.Errorf("required counter %s: %w", counterDiskQueue, err)
	}
	if h, err := add(counterTCPRetrans); err != nil {
		slog.Warn("perfmon: TCPv4 retransmit counter unavailable", "error", err)
	} else {
		c.retransH = h
	}

	slog.Info("perfmon: host counters added (V1 query)")

	// --- V2 session/RFX query ---
	if c.collectPerSession || c.collectRemoteFX {
		sq, err := pdhOpenQuery()
		if err != nil {
			slog.Warn("perfmon: session query open failed", "error", err)
		} else {
			c.sessionQuery = sq
			addS := func(path string) (syscall.Handle, error) { return pdhAddCounter(sq, path) }

			if c.collectPerSession {
				if h, err := addS(counterInputDelay); err == nil {
					c.inputDelayH = h
					c.inputDelayAvail = true
					slog.Debug("perfmon: input_delay counter added OK")
				} else {
					slog.Warn("perfmon: input_delay counter add failed", "error", err)
				}
				if h, err := addS(counterSessCPU); err == nil {
					c.sessCPUH = h
				}
				if h, err := addS(counterSessMem); err == nil {
					c.sessMemH = h
				}
			}
			if c.collectRemoteFX {
				c.rfxAvailable = true
				rfx := func(path string, dest *syscall.Handle) {
					if h, err := addS(path); err != nil {
						c.rfxAvailable = false
					} else {
						*dest = h
					}
				}
				rfx(counterRFXFPS, &c.rfxFPSH)
				rfx(counterRFXSkipSrv, &c.rfxSkipSrvH)
				rfx(counterRFXSkipNet, &c.rfxSkipNetH)
				rfx(counterRFXEncode, &c.rfxEncH)
				rfx(counterRFXQuality, &c.rfxQualH)
				rfx(counterRFXRTT, &c.rfxRTTH)
				rfx(counterRFXLoss, &c.rfxLossH)
			}
			slog.Info("perfmon: session counters added (V2 query)")
		}
	}
	return nil
}

// Prime collects two samples with a 1-second gap to seed rate counters.
func (c *Collector) Prime() error {
	var err error
	c.do(func() { err = c.prime() })
	return err
}

func (c *Collector) prime() error {
	slog.Debug(fmt.Sprintf("perfmon: prime host_handle=0x%X session_handle=0x%X tid=%d",
		c.hostQuery, c.sessionQuery, getThreadID()))
	if err := pdhCollectQueryData(c.hostQuery); err != nil {
		return err
	}
	if c.sessionQuery != 0 {
		_ = pdhCollectQueryData(c.sessionQuery)
	}
	time.Sleep(time.Second)
	if err := pdhCollectQueryData(c.hostQuery); err != nil {
		return err
	}
	if c.sessionQuery != 0 {
		_ = pdhCollectQueryData(c.sessionQuery)
	}
	c.primed = true
	c.skipNextCollect = true

	// Start background sampler: collects every sampleInterval on the PDH
	// thread, accumulating snapshots. Collect() aggregates and drains them.
	// Also keeps the V1 PerfLib provider warm — on memory-constrained DCs,
	// idle gaps between PdhCollectQueryData calls cause access violations.
	slog.Info("perfmon: sampler started", "interval", c.sampleInterval)
	go c.sampler()
	return nil
}

// sampler periodically collects a full perfmon sample on the PDH worker
// thread and appends it to the accumulator. Collect() drains and aggregates.
func (c *Collector) sampler() {
	ticker := time.NewTicker(c.sampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.do(func() {
				snap, err := c.collect()
				if err != nil {
					slog.Warn("perfmon: sampler collect failed", "error", err)
					return
				}
				c.accum = append(c.accum, *snap)
			})
		}
	}
}

// Collect returns an aggregated snapshot from all samples accumulated since
// the last Collect call. If no samples have been accumulated yet (e.g. first
// call before the sampler has ticked), it falls back to a direct collect.
func (c *Collector) Collect() (*dc.PerfSnapshot, error) {
	var snap *dc.PerfSnapshot
	var err error
	c.do(func() {
		if n := len(c.accum); n > 0 {
			agg := aggregate(c.accum)
			c.accum = c.accum[:0]
			snap = &agg
			slog.Debug("perfmon: aggregated samples", "count", n)
		} else {
			snap, err = c.collect()
		}
	})
	return snap, err
}

func (c *Collector) collect() (*dc.PerfSnapshot, error) {
	sessionCollectOK := true
	if c.skipNextCollect {
		c.skipNextCollect = false
	} else {
		if err := pdhCollectQueryData(c.hostQuery); err != nil {
			return nil, fmt.Errorf("host query collect: %w", err)
		}
		if c.sessionQuery != 0 {
			if err := pdhCollectQueryData(c.sessionQuery); err != nil {
				slog.Warn("perfmon: session query collect failed", "error", err)
				sessionCollectOK = false
			}
		}
	}

	snap := &dc.PerfSnapshot{MemTotalMB: c.memTotalMB}

	// Host-level (V1).
	if v, ok := c.scalar(c.cpuH, "cpu"); ok {
		snap.CPUPct = RoundTo(v, 1)
	}
	if v, ok := c.scalar(c.memH, "mem_avail"); ok {
		snap.MemAvailMB = RoundTo(v, 0)
	}
	if v, ok := c.scalar(c.pagesH, "pages_sec"); ok {
		snap.PagesSec = RoundTo(v, 1)
	}
	if v, ok := c.scalar(c.diskH, "disk_queue"); ok {
		snap.DiskQueue = RoundTo(v, 2)
	}
	if v, ok := c.scalar(c.retransH, "tcp_retrans"); ok {
		snap.TCPRetrans = RoundTo(v, 1)
	}

	// Per-session (V2).
	if c.collectPerSession && sessionCollectOK {
		if c.inputDelayAvail && c.inputDelayH != 0 {
			if vals, err := pdhGetDoubleArray(c.inputDelayH); err == nil && len(vals) > 0 {
				snap.InputDelayP50, snap.InputDelayP95, snap.InputDelayMax = AggregateValues(vals)
				snap.InputDelayP50 = RoundTo(snap.InputDelayP50, 1)
				snap.InputDelayP95 = RoundTo(snap.InputDelayP95, 1)
				snap.InputDelayMax = RoundTo(snap.InputDelayMax, 1)
			}
		}
		if c.sessCPUH != 0 {
			if vals, err := pdhGetDoubleArray(c.sessCPUH); err == nil && len(vals) > 0 {
				_, p95, _ := AggregateValues(vals)
				snap.SessionCPUP95 = RoundTo(p95, 1)
			}
		}
		if c.sessMemH != 0 {
			if vals, err := pdhGetDoubleArray(c.sessMemH); err == nil && len(vals) > 0 {
				_, p95, _ := AggregateValues(vals)
				snap.SessionMemP95 = RoundTo(p95, 0)
			}
		}
	}

	// RemoteFX (V2).
	if c.collectRemoteFX && c.rfxAvailable && sessionCollectOK {
		snap.RFXAvailable = true
		c.rfxScalar(c.rfxEncH, &snap.RFXEncodeMS, 1)
		c.rfxScalar(c.rfxQualH, &snap.RFXQuality, 1)
		c.rfxScalar(c.rfxRTTH, &snap.RFXRTT, 1)
		c.rfxScalar(c.rfxLossH, &snap.RFXLoss, 2)
		if c.rfxFPSH != 0 {
			if vals, err := pdhGetDoubleArray(c.rfxFPSH); err == nil && len(vals) > 0 {
				_, p95, _ := AggregateValues(vals)
				snap.RFXFPSOut = RoundTo(p95, 1)
			}
		}
		if c.rfxSkipSrvH != 0 {
			if vals, err := pdhGetDoubleArray(c.rfxSkipSrvH); err == nil && len(vals) > 0 {
				_, p95, _ := AggregateValues(vals)
				snap.RFXSkipServer = RoundTo(p95, 1)
			}
		}
		if c.rfxSkipNetH != 0 {
			if vals, err := pdhGetDoubleArray(c.rfxSkipNetH); err == nil && len(vals) > 0 {
				_, p95, _ := AggregateValues(vals)
				snap.RFXSkipNet = RoundTo(p95, 1)
			}
		}
	}

	slog.Debug(fmt.Sprintf("perfmon: cpu=%.1f%% mem=%0.fMB/%0.fMB pages=%.1f disk=%.2f tcp=%.1f input_delay_max=%.1f",
		snap.CPUPct, snap.MemAvailMB, snap.MemTotalMB, snap.PagesSec, snap.DiskQueue, snap.TCPRetrans, snap.InputDelayMax))

	return snap, nil
}

// aggregate combines multiple PerfSnapshot samples into one.
// Averages rate/gauge counters, takes max for latency/worst-case metrics.
func aggregate(samples []dc.PerfSnapshot) dc.PerfSnapshot {
	n := float64(len(samples))
	var agg dc.PerfSnapshot
	for i := range samples {
		s := &samples[i]
		agg.CPUPct += s.CPUPct
		agg.MemAvailMB += s.MemAvailMB
		agg.PagesSec += s.PagesSec
		agg.DiskQueue += s.DiskQueue
		agg.TCPRetrans += s.TCPRetrans
		agg.RFXEncodeMS += s.RFXEncodeMS
		agg.RFXRTT += s.RFXRTT

		// Worst-case metrics: keep the max across samples.
		if s.InputDelayP50 > agg.InputDelayP50 {
			agg.InputDelayP50 = s.InputDelayP50
		}
		if s.InputDelayP95 > agg.InputDelayP95 {
			agg.InputDelayP95 = s.InputDelayP95
		}
		if s.InputDelayMax > agg.InputDelayMax {
			agg.InputDelayMax = s.InputDelayMax
		}
		if s.SessionCPUP95 > agg.SessionCPUP95 {
			agg.SessionCPUP95 = s.SessionCPUP95
		}
		if s.SessionMemP95 > agg.SessionMemP95 {
			agg.SessionMemP95 = s.SessionMemP95
		}
		if s.RFXFPSOut > agg.RFXFPSOut {
			agg.RFXFPSOut = s.RFXFPSOut
		}
		if s.RFXSkipServer > agg.RFXSkipServer {
			agg.RFXSkipServer = s.RFXSkipServer
		}
		if s.RFXSkipNet > agg.RFXSkipNet {
			agg.RFXSkipNet = s.RFXSkipNet
		}
		if s.RFXLoss > agg.RFXLoss {
			agg.RFXLoss = s.RFXLoss
		}
		if s.RFXQuality > agg.RFXQuality {
			agg.RFXQuality = s.RFXQuality
		}
		if s.RFXAvailable {
			agg.RFXAvailable = true
		}
	}

	// Average the rate/gauge counters.
	agg.CPUPct = RoundTo(agg.CPUPct/n, 1)
	agg.MemAvailMB = RoundTo(agg.MemAvailMB/n, 0)
	agg.PagesSec = RoundTo(agg.PagesSec/n, 1)
	agg.DiskQueue = RoundTo(agg.DiskQueue/n, 2)
	agg.TCPRetrans = RoundTo(agg.TCPRetrans/n, 1)
	agg.RFXEncodeMS = RoundTo(agg.RFXEncodeMS/n, 1)
	agg.RFXRTT = RoundTo(agg.RFXRTT/n, 1)

	// MemTotalMB is constant — take from last sample.
	agg.MemTotalMB = samples[len(samples)-1].MemTotalMB

	return agg
}

// scalar reads a single counter value, logging permanent errors once.
func (c *Collector) scalar(h syscall.Handle, name string) (float64, bool) {
	if h == 0 {
		return 0, false
	}
	v, err := pdhGetDouble(h)
	if err == nil {
		return v, true
	}
	if !errors.Is(err, errCounterNotReady) {
		if !c.loggedErrors[name] {
			c.loggedErrors[name] = true
			slog.Warn("perfmon: counter read failed", "name", name, "error", err)
		}
	}
	return 0, false
}

func (c *Collector) rfxScalar(h syscall.Handle, dst *float64, places int) {
	if h == 0 {
		return
	}
	if v, err := pdhGetDouble(h); err == nil {
		*dst = RoundTo(v, places)
	}
}

// Close releases PDH queries and stops the worker goroutine.
func (c *Collector) Close() {
	if c == nil || c.reqCh == nil {
		return
	}
	// Stop sampler goroutine before closing queries.
	select {
	case <-c.stopCh:
		// already closed
	default:
		close(c.stopCh)
	}
	c.do(func() {
		pdhCloseQuery(c.hostQuery)
		c.hostQuery = 0
		pdhCloseQuery(c.sessionQuery)
		c.sessionQuery = 0
	})
	close(c.reqCh)
	c.reqCh = nil
}
