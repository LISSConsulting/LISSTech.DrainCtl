//go:build windows

package perfmon

import (
	"fmt"
	"syscall"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// Counter paths (English canonical names for PdhAddEnglishCounterW).
const (
	counterCPU        = `\Processor(_Total)\% Processor Time`
	counterMemAvail   = `\Memory\Available MBytes`
	counterPagesSec   = `\Memory\Pages/sec`
	counterDiskQueue  = `\PhysicalDisk(_Total)\Avg. Disk Queue Length`
	counterTCPRetrans = `\TCPv4\Segments Retransmitted/sec`

	counterInputDelay = `\User Input Delay per Session(*)\Max Input Delay`
	counterSessCPU    = `\Terminal Services Session(*)\% Processor Time`
	counterSessMem    = `\Terminal Services Session(*)\Working Set`

	counterRFXFPS     = `\RemoteFX Graphics(*)\Output Frames/Second`
	counterRFXSkipSrv = `\RemoteFX Graphics(*)\Frames Skipped/Second - Insufficient Server Resources`
	counterRFXSkipNet = `\RemoteFX Graphics(*)\Frames Skipped/Second - Insufficient Network Resources`
	counterRFXEncode  = `\RemoteFX Graphics(*)\Average Encoding Time`
	counterRFXQuality = `\RemoteFX Graphics(*)\Frame Quality`
	counterRFXRTT     = `\RemoteFX Network(*)\Current TCP RTT`
	counterRFXLoss    = `\RemoteFX Network(*)\Loss Rate`
)

// Collector manages a PDH query with pre-added counter handles.
type Collector struct {
	query syscall.Handle

	// Host-level counters (always added)
	cpuH     syscall.Handle
	memH     syscall.Handle
	pagesH   syscall.Handle
	diskH    syscall.Handle
	retransH syscall.Handle

	// Per-session counters (optional)
	collectPerSession bool
	inputDelayH       syscall.Handle
	sessCPUH          syscall.Handle
	sessMemH          syscall.Handle
	inputDelayAvail   bool

	// RemoteFX counters (optional, may be unavailable)
	collectRemoteFX bool
	rfxAvailable    bool
	rfxFPSH         syscall.Handle
	rfxSkipSrvH     syscall.Handle
	rfxSkipNetH     syscall.Handle
	rfxEncH         syscall.Handle
	rfxQualH        syscall.Handle
	rfxRTTH         syscall.Handle
	rfxLossH        syscall.Handle

	// Total physical memory in MB (queried once at open).
	memTotalMB float64

	// Track whether we've collected at least once (rate counters need 2 samples).
	primed bool

	log          dc.LogFunc
	loggedErrors map[string]bool // track which counter errors we've already logged (avoid spam)
}

// Open creates the PDH query and adds counters based on the config.
// Counters that fail to add (e.g., RemoteFX not installed, User Input Delay
// not available) are logged at LvlWRN and skipped.
func Open(cfg dc.PerformanceConfig, log dc.LogFunc) (*Collector, error) {
	if log == nil {
		log = dc.DiscardLogger()
	}

	query, err := pdhOpenQuery()
	if err != nil {
		return nil, err
	}

	c := &Collector{
		query:             query,
		collectPerSession: cfg.CollectPerSession,
		collectRemoteFX:   cfg.CollectRemoteFX,
		memTotalMB:        totalPhysicalMemoryMB(),
		log:               log,
		loggedErrors:      make(map[string]bool),
	}

	// Host-level counters (required).
	must := func(path string, dest *syscall.Handle) error {
		h, err := pdhAddEnglishCounter(query, path)
		if err != nil {
			return fmt.Errorf("required counter %s: %w", path, err)
		}
		*dest = h
		return nil
	}

	if err := must(counterCPU, &c.cpuH); err != nil {
		pdhCloseQuery(query)
		return nil, err
	}
	if err := must(counterMemAvail, &c.memH); err != nil {
		pdhCloseQuery(query)
		return nil, err
	}
	if err := must(counterPagesSec, &c.pagesH); err != nil {
		pdhCloseQuery(query)
		return nil, err
	}
	if err := must(counterDiskQueue, &c.diskH); err != nil {
		pdhCloseQuery(query)
		return nil, err
	}

	// TCP retransmits — optional (counter set may not exist on some configs).
	if h, err := pdhAddEnglishCounter(query, counterTCPRetrans); err != nil {
		dc.LogMsg(log, dc.LvlWRN, "TCP retransmit counter unavailable, skipping", fmt.Sprintf("error=%q", err))
	} else {
		c.retransH = h
	}

	// Per-session counters.
	if cfg.CollectPerSession {
		if h, err := pdhAddEnglishCounter(query, counterInputDelay); err != nil {
			dc.LogMsg(log, dc.LvlWRN, "User Input Delay counter unavailable — requires Server 2019+ or registry key HKLM\\System\\CurrentControlSet\\Control\\Terminal Server\\EnableLagCounter=1", fmt.Sprintf("error=%q", err))
		} else {
			c.inputDelayH = h
			c.inputDelayAvail = true
		}

		if h, err := pdhAddEnglishCounter(query, counterSessCPU); err != nil {
			dc.LogMsg(log, dc.LvlWRN, "Terminal Services Session CPU counter unavailable", fmt.Sprintf("error=%q", err))
		} else {
			c.sessCPUH = h
		}

		if h, err := pdhAddEnglishCounter(query, counterSessMem); err != nil {
			dc.LogMsg(log, dc.LvlWRN, "Terminal Services Session memory counter unavailable", fmt.Sprintf("error=%q", err))
		} else {
			c.sessMemH = h
		}
	}

	// RemoteFX counters.
	if cfg.CollectRemoteFX {
		c.rfxAvailable = true
		optRFX := func(path string, dest *syscall.Handle) {
			h, err := pdhAddEnglishCounter(query, path)
			if err != nil {
				dc.LogMsg(log, dc.LvlWRN, "RemoteFX counter unavailable", fmt.Sprintf("counter=%q error=%q", path, err))
				c.rfxAvailable = false
			} else {
				*dest = h
			}
		}
		optRFX(counterRFXFPS, &c.rfxFPSH)
		optRFX(counterRFXSkipSrv, &c.rfxSkipSrvH)
		optRFX(counterRFXSkipNet, &c.rfxSkipNetH)
		optRFX(counterRFXEncode, &c.rfxEncH)
		optRFX(counterRFXQuality, &c.rfxQualH)
		optRFX(counterRFXRTT, &c.rfxRTTH)
		optRFX(counterRFXLoss, &c.rfxLossH)
	}

	return c, nil
}

// Prime performs the first PDH collection to seed rate counters.
// Must be called once before Collect() returns meaningful data.
func (c *Collector) Prime() error {
	if err := pdhCollectQueryData(c.query); err != nil {
		return err
	}
	c.primed = true
	return nil
}

// Collect samples all counters and returns a PerfSnapshot.
// Returns partial data if the collector has not been primed (rate counters
// require two samples). Callers should call Prime() once at startup.
// logCounterError logs a PDH counter error once per counter name to avoid spam.
func (c *Collector) logCounterError(name string, err error) {
	if c.log == nil || c.loggedErrors[name] {
		return
	}
	c.loggedErrors[name] = true
	dc.LogMsg(c.log, dc.LvlWRN, fmt.Sprintf("perfmon counter %s read failed (will not repeat)", name), fmt.Sprintf("error=%q", err))
}

func (c *Collector) Collect() (*dc.PerfSnapshot, error) {
	if err := pdhCollectQueryData(c.query); err != nil {
		return nil, fmt.Errorf("collect query data: %w", err)
	}

	snap := &dc.PerfSnapshot{
		MemTotalMB: c.memTotalMB,
	}

	// Host-level counters.
	if v, err := pdhGetFormattedDouble(c.cpuH); err == nil {
		snap.CPUPct = RoundTo(v, 1)
	} else {
		c.logCounterError("cpu", err)
	}
	if v, err := pdhGetFormattedDouble(c.memH); err == nil {
		snap.MemAvailMB = RoundTo(v, 0)
	} else {
		c.logCounterError("mem_avail", err)
	}
	if v, err := pdhGetFormattedDouble(c.pagesH); err == nil {
		snap.PagesSec = RoundTo(v, 1)
	} else {
		c.logCounterError("pages_sec", err)
	}
	if v, err := pdhGetFormattedDouble(c.diskH); err == nil {
		snap.DiskQueue = RoundTo(v, 2)
	} else {
		c.logCounterError("disk_queue", err)
	}
	if c.retransH != 0 {
		if v, err := pdhGetFormattedDouble(c.retransH); err == nil {
			snap.TCPRetrans = RoundTo(v, 1)
		} else {
			c.logCounterError("tcp_retrans", err)
		}
	}

	// Per-session counters.
	if c.collectPerSession {
		if c.inputDelayAvail && c.inputDelayH != 0 {
			if values, err := pdhGetFormattedDoubleArray(c.inputDelayH); err == nil && len(values) > 0 {
				snap.InputDelayP50, snap.InputDelayP95, snap.InputDelayMax = AggregateValues(values)
				snap.InputDelayP50 = RoundTo(snap.InputDelayP50, 1)
				snap.InputDelayP95 = RoundTo(snap.InputDelayP95, 1)
				snap.InputDelayMax = RoundTo(snap.InputDelayMax, 1)
			}
		}

		if c.sessCPUH != 0 {
			if values, err := pdhGetFormattedDoubleArray(c.sessCPUH); err == nil && len(values) > 0 {
				_, p95, _ := AggregateValues(values)
				snap.SessionCPUP95 = RoundTo(p95, 1)
			}
		}

		if c.sessMemH != 0 {
			if values, err := pdhGetFormattedDoubleArray(c.sessMemH); err == nil && len(values) > 0 {
				_, p95, _ := AggregateValues(values)
				snap.SessionMemP95 = RoundTo(p95, 0)
			}
		}
	}

	// RemoteFX counters.
	if c.collectRemoteFX && c.rfxAvailable {
		snap.RFXAvailable = true
		c.collectRFXScalar(c.rfxEncH, &snap.RFXEncodeMS, 1)
		c.collectRFXScalar(c.rfxQualH, &snap.RFXQuality, 1)
		c.collectRFXScalar(c.rfxRTTH, &snap.RFXRTT, 1)
		c.collectRFXScalar(c.rfxLossH, &snap.RFXLoss, 2)

		// Array counters — aggregate across sessions.
		if c.rfxFPSH != 0 {
			if values, err := pdhGetFormattedDoubleArray(c.rfxFPSH); err == nil && len(values) > 0 {
				_, p95, _ := AggregateValues(values)
				snap.RFXFPSOut = RoundTo(p95, 1)
			}
		}
		if c.rfxSkipSrvH != 0 {
			if values, err := pdhGetFormattedDoubleArray(c.rfxSkipSrvH); err == nil && len(values) > 0 {
				_, p95, _ := AggregateValues(values)
				snap.RFXSkipServer = RoundTo(p95, 1)
			}
		}
		if c.rfxSkipNetH != 0 {
			if values, err := pdhGetFormattedDoubleArray(c.rfxSkipNetH); err == nil && len(values) > 0 {
				_, p95, _ := AggregateValues(values)
				snap.RFXSkipNet = RoundTo(p95, 1)
			}
		}
	}

	return snap, nil
}

// collectRFXScalar reads a scalar RemoteFX counter into dst.
func (c *Collector) collectRFXScalar(h syscall.Handle, dst *float64, places int) {
	if h == 0 {
		return
	}
	if v, err := pdhGetFormattedDouble(h); err == nil {
		*dst = RoundTo(v, places)
	}
}

// Close releases the PDH query and all counter handles.
func (c *Collector) Close() {
	if c != nil && c.query != 0 {
		pdhCloseQuery(c.query)
		c.query = 0
	}
}
