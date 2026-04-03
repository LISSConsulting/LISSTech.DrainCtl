//go:build windows

package svc

import (
	"context"
	"fmt"
	"strings"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/pipe"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/store"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/watcher"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
)

// Event IDs matching assets/drainctl.mc message file.
const (
	EvtServiceStarted = 1000
	EvtServiceStopped = 1001
	EvtCheckHealthy   = 1002
	EvtConfigReloaded = 1003
	EvtTransition     = 1004
	EvtCheckGrace     = 2000
	EvtCheckAlert     = 3000
	EvtRegistryFailed = 3001
	EvtServiceError   = 3002
)

// EventLogLogger returns a LogFunc that writes to the Windows Event Log
// using generic event ID 1/2/3 for structured log messages.
func EventLogLogger(elog *eventlog.Log) dc.LogFunc {
	return func(l dc.Level, fields ...string) {
		msg := strings.Join(fields, " ")
		switch l {
		case dc.LvlERR:
			_ = elog.Error(3, msg)
		case dc.LvlWRN:
			_ = elog.Warning(2, msg)
		default:
			_ = elog.Info(1, msg)
		}
	}
}

// drainService implements svc.Handler.
type drainService struct {
	log  dc.LogFunc
	elog *eventlog.Log
}

// serviceHandler is the pipe handler bridge between the service state
// and the named pipe server.
type serviceHandler struct {
	store *store.MemAuditStore
	cfg   *dc.ServiceConfig
}

func (h *serviceHandler) HandleStatus(gracePeriod time.Duration) *dc.CheckResult {
	state, err := dc.ReadDrainMode()
	if err != nil {
		return &dc.CheckResult{
			Version:   dc.Version,
			Timestamp: time.Now(),
			Status:    "Error",
			Message:   err.Error(),
			ExitCode:  2,
		}
	}

	gp := h.cfg.GracePeriod
	if gracePeriod > 0 {
		gp = gracePeriod
	}

	res := &dc.CheckResult{
		Version:            dc.Version,
		Timestamp:          time.Now(),
		Host:               state.Host,
		DrainModeLabel:     state.Mode.String(),
		DrainModeValue:     uint32(state.Mode),
		GracePeriodSeconds: int(gp.Seconds()),
	}

	drainActive := state.Mode != dc.AllowAll
	connAllowed := !drainActive
	res.ConnectionsAllowed = &connAllowed

	// State duration from in-memory store.
	if since := h.store.StateSince(state.Mode); since != nil {
		s := since.Local()
		res.StateSince = &s
		dur := time.Since(*since).Seconds()
		res.StateDurationSeconds = &dur
	}

	// Determine status.
	stateDur := time.Duration(0)
	if res.StateDurationSeconds != nil {
		stateDur = time.Duration(*res.StateDurationSeconds) * time.Second
	}

	if drainActive && stateDur > gp {
		res.Status = "Alert"
		res.Message = fmt.Sprintf("Drain mode active for %s, exceeding grace period of %s. New connections are blocked.",
			stateDur.Truncate(time.Second), gp)
		res.ExitCode = 1
	} else if drainActive {
		remaining := gp - stateDur
		res.Status = "Grace"
		res.Message = fmt.Sprintf("Drain mode active, within grace period (%s remaining).", remaining.Truncate(time.Second))
		res.ExitCode = 0
	} else {
		res.Status = "Healthy"
		res.Message = "All connections allowed."
		res.ExitCode = 0
	}

	// Check for transition.
	if last := h.store.LastObservation(); last != nil && last.DrainMode != state.Mode {
		res.Transition = true
		res.TransitionFrom = last.DrainMode.String()
	}

	return res
}

func (h *serviceHandler) HandleHistory(limit int, changesOnly bool) []dc.AuditRecord {
	if changesOnly {
		return h.store.Changes(limit)
	}
	return h.store.History(limit)
}

// Execute is the Windows service main loop.
func (s *drainService) Execute(args []string, r <-chan svc.ChangeRequest, statusCh chan<- svc.Status) (bool, uint32) {
	statusCh <- svc.Status{State: svc.StartPending, WaitHint: 10000}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Ensure default parameters exist (safety net if MSI didn't create them).
	_ = dc.WriteDefaultParameters(s.log)

	// Load config.
	cfg := dc.ReadServiceConfig(s.log)
	notifyCfg := dc.ReadNotifyConfig(s.log)
	notifyState := &dc.NotifyState{}
	s.log(dc.LvlINF, fmt.Sprintf("service=starting version=%s grace=%s poll=%s retention=%dd",
		dc.Version, cfg.GracePeriod, cfg.PollInterval, cfg.RetentionDays))

	// Open in-memory audit store.
	st, err := store.OpenMemAuditStore(cfg.AuditPath, s.log)
	if err != nil {
		s.log(dc.LvlERR, fmt.Sprintf("service=failed error=%q", err))
		return false, 1
	}
	defer func() { _ = st.Close() }()

	// Prune on startup.
	retention := time.Duration(cfg.RetentionDays) * 24 * time.Hour
	if pruned, err := st.Prune(retention); err != nil {
		dc.LogMsg(s.log, dc.LvlWRN, "startup prune failed", fmt.Sprintf("error=%q", err))
	} else if pruned > 0 {
		s.log(dc.LvlINF, fmt.Sprintf("startup_prune=%d", pruned))
	}

	// Start registry watcher.
	regCh, err := watcher.WatchDrainModeKey(ctx, s.log)
	if err != nil {
		dc.LogMsg(s.log, dc.LvlWRN, "registry watcher failed, polling only", fmt.Sprintf("error=%q", err))
		regCh = make(chan struct{}) // never fires
	}

	// Start parameters watcher for hot-reload.
	paramCh, err := watcher.WatchParametersKey(ctx, s.log)
	if err != nil {
		dc.LogMsg(s.log, dc.LvlWRN, "parameters watcher failed", fmt.Sprintf("error=%q", err))
		paramCh = make(chan struct{}) // never fires
	}

	// Start event log subscriber for change attribution.
	var evtSub *watcher.EventSubscriber
	if sub, err := watcher.NewEventSubscriber(ctx, s.log); err != nil {
		dc.LogMsg(s.log, dc.LvlWRN, "event subscriber failed, attribution via wevtutil fallback", fmt.Sprintf("error=%q", err))
	} else {
		evtSub = sub
	}

	// Start pipe server.
	handler := &serviceHandler{store: st, cfg: &cfg}
	go pipe.ServePipe(ctx, handler, s.log)

	// Timers.
	pollTicker := time.NewTicker(cfg.PollInterval)
	defer pollTicker.Stop()

	flushTicker := time.NewTicker(30 * time.Second)
	defer flushTicker.Stop()

	// Run initial check.
	svcRunCheck(st, &cfg, &notifyCfg, notifyState, evtSub, s.log, s.elog)

	// Report running.
	statusCh <- svc.Status{
		State:   svc.Running,
		Accepts: svc.AcceptStop | svc.AcceptShutdown,
	}
	s.log(dc.LvlOK, "service=running")
	_ = s.elog.Info(EvtServiceStarted, fmt.Sprintf(
		"Service started.\nVersion: %s\nPoll interval: %s\nGrace period: %s\nRetention: %d days",
		dc.Version, cfg.PollInterval, cfg.GracePeriod, cfg.RetentionDays))

	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Stop, svc.Shutdown:
				s.log(dc.LvlINF, "service=stopping")
				statusCh <- svc.Status{State: svc.StopPending, WaitHint: 10000}
				cancel()
				_ = st.Flush()
				_ = s.elog.Info(EvtServiceStopped, "Service stopped.")
				s.log(dc.LvlOK, "service=stopped")
				return false, 0
			case svc.Interrogate:
				statusCh <- c.CurrentStatus
			}

		case <-regCh:
			s.log(dc.LvlINF, "trigger=registry_change")
			svcRunCheck(st, &cfg, &notifyCfg, notifyState, evtSub, s.log, s.elog)
			_ = st.Flush() // immediate flush on change

		case <-pollTicker.C:
			svcRunCheck(st, &cfg, &notifyCfg, notifyState, evtSub, s.log, s.elog)

		case <-paramCh:
			newCfg := dc.ReadServiceConfig(s.log)
			if newCfg.PollInterval != cfg.PollInterval {
				pollTicker.Reset(newCfg.PollInterval)
			}
			cfg = newCfg
			handler.cfg = &cfg
			notifyCfg = dc.ReadNotifyConfig(s.log)
			s.log(dc.LvlINF, "config=reloaded")

		case <-flushTicker.C:
			_ = st.FlushIfDirty()
		}
	}
}

// svcRunCheck performs a single check cycle in service mode.
func svcRunCheck(st *store.MemAuditStore, cfg *dc.ServiceConfig, notifyCfg *dc.NotifyConfig, notifyState *dc.NotifyState, evtSub *watcher.EventSubscriber, log dc.LogFunc, elog *eventlog.Log) {
	state, err := dc.ReadDrainMode()
	if err != nil {
		dc.LogMsg(log, dc.LvlERR, "registry read failed", fmt.Sprintf("error=%q", err))
		_ = elog.Error(EvtRegistryFailed, fmt.Sprintf("Failed to read drain mode registry value: %s", err))
		return
	}

	transition := false
	transitionFrom := ""
	changedBy := ""

	if last := st.LastObservation(); last != nil && last.DrainMode != state.Mode {
		transition = true
		transitionFrom = last.DrainMode.String()
		log(dc.LvlWRN,
			"transition=true",
			fmt.Sprintf("from=%s", last.DrainMode),
			fmt.Sprintf("to=%s", state.Mode),
		)

		beforeTransition := time.Now().Add(-5 * time.Second)

		if evtSub != nil {
			// Wait up to 3 seconds for the 4657 event to arrive via push.
			changedBy = evtSub.WaitAttribution(beforeTransition, 3*time.Second)
		}
		if changedBy == "" {
			// Fallback: query wevtutil (covers cases where EvtSubscribe
			// missed the event or wasn't available).
			if lookback := time.Since(last.Timestamp); lookback < 24*time.Hour {
				changedBy = dc.QueryRegistryChangeUser(last.Timestamp)
			}
		}
		if changedBy != "" {
			log(dc.LvlINF, fmt.Sprintf("changed_by=%s", changedBy))
		}
	}

	// Determine exit code and status.
	drainActive := state.Mode != dc.AllowAll
	exitCode := 0
	stateDur := time.Duration(0)
	if since := st.StateSince(state.Mode); since != nil {
		stateDur = time.Since(*since).Truncate(time.Second)
	}
	if drainActive {
		if stateDur > cfg.GracePeriod {
			exitCode = 1
		}
	}

	rec := &dc.AuditRecord{
		Timestamp:   time.Now(),
		Host:        state.Host,
		DrainMode:   state.Mode,
		DrainLabel:  state.Mode.String(),
		KeyModified: state.KeyModified,
		Changed:     transition,
		ChangedBy:   changedBy,
		ExitCode:    exitCode,
	}
	st.Append(rec)

	if state.Mode == dc.AllowAll {
		log(dc.LvlINF, fmt.Sprintf("drain_mode=%s", state.Mode), fmt.Sprintf("exit=%d", exitCode))
	} else {
		log(dc.LvlWRN, fmt.Sprintf("drain_mode=%s", state.Mode), fmt.Sprintf("exit=%d", exitCode))
	}

	// Write state-specific event log entries.
	if transition {
		cb := changedBy
		if cb == "" {
			cb = "unknown"
		}
		_ = elog.Info(EvtTransition, fmt.Sprintf(
			"State transition detected on %s: %s -> %s. Changed by: %s.",
			state.Host, transitionFrom, state.Mode, cb))
	}

	if drainActive && exitCode == 1 {
		// Alert: drain mode exceeded grace period.
		cb := changedBy
		if cb == "" {
			cb = "unknown"
		}
		_ = elog.Error(EvtCheckAlert, fmt.Sprintf(
			"ALERT: Drain mode active on %s for %s, exceeding grace period of %s. Mode: %s. Changed by: %s.",
			state.Host, stateDur, cfg.GracePeriod, state.Mode, cb))
	} else if drainActive {
		// Grace: within grace period.
		remaining := cfg.GracePeriod - stateDur
		_ = elog.Warning(EvtCheckGrace, fmt.Sprintf(
			"Drain mode active on %s, within grace period (%s remaining). Mode: %s.",
			state.Host, remaining.Truncate(time.Second), state.Mode))
	} else {
		// Healthy.
		_ = elog.Info(EvtCheckHealthy, fmt.Sprintf(
			"Drain mode check: %s on %s. All connections allowed. State duration: %s.",
			state.Mode, state.Host, stateDur))
	}

	// Send notifications if configured.
	if notifyCfg != nil && notifyCfg.Enabled() {
		// Build a CheckResult for the notification system.
		status := "Healthy"
		message := "All connections allowed."
		if drainActive && exitCode == 1 {
			status = "Alert"
			message = fmt.Sprintf("Drain mode active for %s, exceeding grace period of %s. New connections are blocked.",
				stateDur, cfg.GracePeriod)
		} else if drainActive {
			remaining := cfg.GracePeriod - stateDur
			status = "Grace"
			message = fmt.Sprintf("Drain mode active, within grace period (%s remaining).", remaining.Truncate(time.Second))
		}

		dur := stateDur.Seconds()
		connAllowed := !drainActive
		notifyResult := &dc.CheckResult{
			Timestamp:            time.Now(),
			Host:                 state.Host,
			DrainModeLabel:       state.Mode.String(),
			DrainModeValue:       uint32(state.Mode),
			GracePeriodSeconds:   int(cfg.GracePeriod.Seconds()),
			StateDurationSeconds: &dur,
			Status:               status,
			ConnectionsAllowed:   &connAllowed,
			Transition:           transition,
			TransitionFrom:       transitionFrom,
			ChangedBy:            changedBy,
			Message:              message,
			ExitCode:             exitCode,
		}
		dc.SendNotification(*notifyCfg, notifyState, notifyResult, transition, changedBy, log)
	}
}

// RunService starts the Windows service. Called by the CLI's hidden
// "service run" subcommand.
func RunService() error {
	elog, err := eventlog.Open(dc.ServiceName)
	if err != nil {
		return fmt.Errorf("open event log: %w", err)
	}
	defer func() { _ = elog.Close() }()

	log := EventLogLogger(elog)
	return svc.Run(dc.ServiceName, &drainService{log: log, elog: elog})
}
