//go:build windows

package perfmon

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// Counter paths. V1 (PerfLib) and V2 (PCW/ETW) counters must live in
// separate PDH queries — on Server 2022+, mixing them in one query
// causes PDH to skip V1 provider DLL loading.
const (
	// V1 host-level counters.
	counterCPU        = `\Processor Information(_Total)\% Processor Utility`
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

	log          dc.LogFunc
	loggedErrors map[string]bool
	reqCh        chan request
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
func Open(cfg dc.PerformanceConfig, log dc.LogFunc) (*Collector, error) {
	if log == nil {
		log = dc.DiscardLogger()
	}
	c := &Collector{
		collectPerSession: cfg.CollectPerSession,
		collectRemoteFX:   cfg.CollectRemoteFX,
		memTotalMB:        totalPhysicalMemoryMB(),
		log:               log,
		loggedErrors:      make(map[string]bool),
		reqCh:             startWorker(),
	}
	var err error
	c.do(func() { err = c.open(log) })
	if err != nil {
		close(c.reqCh)
		return nil, err
	}
	return c, nil
}

func (c *Collector) open(log dc.LogFunc) error {
	// --- V1 host query ---
	hq, err := pdhOpenQuery()
	if err != nil {
		return err
	}
	c.hostQuery = hq

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
		dc.LogMsg(log, dc.LvlWRN, "perfmon: TCPv4 retransmit counter unavailable", fmt.Sprintf("error=%q", err))
	} else {
		c.retransH = h
	}

	dc.LogMsg(log, dc.LvlINF, "perfmon: host counters added (V1 query)")

	// --- V2 session/RFX query ---
	if c.collectPerSession || c.collectRemoteFX {
		sq, err := pdhOpenQuery()
		if err != nil {
			dc.LogMsg(log, dc.LvlWRN, "perfmon: session query open failed", fmt.Sprintf("error=%q", err))
		} else {
			c.sessionQuery = sq
			addS := func(path string) (syscall.Handle, error) { return pdhAddCounter(sq, path) }

			if c.collectPerSession {
				if h, err := addS(counterInputDelay); err == nil {
					c.inputDelayH = h
					c.inputDelayAvail = true
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
			dc.LogMsg(log, dc.LvlINF, "perfmon: session counters added (V2 query)")
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
	return nil
}

// Collect samples all counters and returns a snapshot.
func (c *Collector) Collect() (*dc.PerfSnapshot, error) {
	var snap *dc.PerfSnapshot
	var err error
	c.do(func() { snap, err = c.collect() })
	return snap, err
}

func (c *Collector) collect() (*dc.PerfSnapshot, error) {
	if c.skipNextCollect {
		c.skipNextCollect = false
	} else {
		if err := pdhCollectQueryData(c.hostQuery); err != nil {
			return nil, fmt.Errorf("host query collect: %w", err)
		}
		if c.sessionQuery != 0 {
			_ = pdhCollectQueryData(c.sessionQuery)
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
	if c.collectPerSession {
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
	if c.collectRemoteFX && c.rfxAvailable {
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

	dc.LogMsg(c.log, dc.LvlDBG,
		fmt.Sprintf("perfmon: cpu=%.1f%% mem=%0.fMB/%0.fMB pages=%.1f disk=%.2f tcp=%.1f",
			snap.CPUPct, snap.MemAvailMB, snap.MemTotalMB, snap.PagesSec, snap.DiskQueue, snap.TCPRetrans))

	return snap, nil
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
			dc.LogMsg(c.log, dc.LvlWRN, fmt.Sprintf("perfmon: %s read failed", name), fmt.Sprintf("error=%q", err))
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
	c.do(func() {
		pdhCloseQuery(c.hostQuery)
		c.hostQuery = 0
		pdhCloseQuery(c.sessionQuery)
		c.sessionQuery = 0
	})
	close(c.reqCh)
	c.reqCh = nil
}
