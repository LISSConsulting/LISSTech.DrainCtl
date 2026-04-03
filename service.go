//go:build windows

package drainctl

import (
	"context"
	"fmt"
	"strings"
	"time"

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
func EventLogLogger(elog *eventlog.Log) LogFunc {
	return func(l Level, fields ...string) {
		msg := strings.Join(fields, " ")
		switch l {
		case LvlERR:
			_ = elog.Error(3, msg)
		case LvlWRN:
			_ = elog.Warning(2, msg)
		default:
			_ = elog.Info(1, msg)
		}
	}
}

// drainService implements svc.Handler.
type drainService struct {
	log  LogFunc
	elog *eventlog.Log
}

// serviceHandler is the pipe handler bridge between the service state
// and the named pipe server.
type serviceHandler struct {
	store *MemAuditStore
	cfg   *ServiceConfig
}

func (h *serviceHandler) HandleStatus(gracePeriod time.Duration) *CheckResult {
	state, err := ReadDrainMode()
	if err != nil {
		return &CheckResult{
			Version:   Version,
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

	res := &CheckResult{
		Version:            Version,
		Timestamp:          time.Now(),
		Host:               state.Host,
		DrainModeLabel:     state.Mode.String(),
		DrainModeValue:     uint32(state.Mode),
		GracePeriodSeconds: int(gp.Seconds()),
	}

	drainActive := state.Mode != AllowAll
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

func (h *serviceHandler) HandleHistory(limit int, changesOnly bool) []AuditRecord {
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
	_ = WriteDefaultParameters(s.log)

	// Load config.
	cfg := ReadServiceConfig(s.log)
	notifyCfg := ReadNotifyConfig(s.log)
	notifyState := &NotifyState{}
	s.log(LvlINF, fmt.Sprintf("service=starting version=%s grace=%s poll=%s retention=%dd",
		Version, cfg.GracePeriod, cfg.PollInterval, cfg.RetentionDays))

	// Open in-memory audit store.
	store, err := OpenMemAuditStore(cfg.AuditPath, s.log)
	if err != nil {
		s.log(LvlERR, fmt.Sprintf("service=failed error=%q", err))
		return false, 1
	}
	defer func() { _ = store.Close() }()

	// Prune on startup.
	retention := time.Duration(cfg.RetentionDays) * 24 * time.Hour
	if pruned, err := store.Prune(retention); err != nil {
		LogMsg(s.log, LvlWRN, "startup prune failed", fmt.Sprintf("error=%q", err))
	} else if pruned > 0 {
		s.log(LvlINF, fmt.Sprintf("startup_prune=%d", pruned))
	}

	// Start registry watcher.
	regCh, err := WatchDrainModeKey(ctx, s.log)
	if err != nil {
		LogMsg(s.log, LvlWRN, "registry watcher failed, polling only", fmt.Sprintf("error=%q", err))
		regCh = make(chan struct{}) // never fires
	}

	// Start parameters watcher for hot-reload.
	paramCh, err := WatchParametersKey(ctx, s.log)
	if err != nil {
		LogMsg(s.log, LvlWRN, "parameters watcher failed", fmt.Sprintf("error=%q", err))
		paramCh = make(chan struct{}) // never fires
	}

	// Start event log subscriber for change attribution.
	var evtSub *EventSubscriber
	if sub, err := NewEventSubscriber(ctx, s.log); err != nil {
		LogMsg(s.log, LvlWRN, "event subscriber failed, attribution via wevtutil fallback", fmt.Sprintf("error=%q", err))
	} else {
		evtSub = sub
	}

	// Start pipe server.
	handler := &serviceHandler{store: store, cfg: &cfg}
	go ServePipe(ctx, handler, s.log)

	// Timers.
	pollTicker := time.NewTicker(cfg.PollInterval)
	defer pollTicker.Stop()

	flushTicker := time.NewTicker(30 * time.Second)
	defer flushTicker.Stop()

	// Run initial check.
	svcRunCheck(store, &cfg, &notifyCfg, notifyState, evtSub, s.log, s.elog)

	// Report running.
	statusCh <- svc.Status{
		State:   svc.Running,
		Accepts: svc.AcceptStop | svc.AcceptShutdown,
	}
	s.log(LvlOK, "service=running")
	_ = s.elog.Info(EvtServiceStarted, fmt.Sprintf(
		"Service started.\nVersion: %s\nPoll interval: %s\nGrace period: %s\nRetention: %d days",
		Version, cfg.PollInterval, cfg.GracePeriod, cfg.RetentionDays))

	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Stop, svc.Shutdown:
				s.log(LvlINF, "service=stopping")
				statusCh <- svc.Status{State: svc.StopPending, WaitHint: 10000}
				cancel()
				_ = store.Flush()
				_ = s.elog.Info(EvtServiceStopped, "Service stopped.")
				s.log(LvlOK, "service=stopped")
				return false, 0
			case svc.Interrogate:
				statusCh <- c.CurrentStatus
			}

		case <-regCh:
			s.log(LvlINF, "trigger=registry_change")
			svcRunCheck(store, &cfg, &notifyCfg, notifyState, evtSub, s.log, s.elog)
			_ = store.Flush() // immediate flush on change

		case <-pollTicker.C:
			svcRunCheck(store, &cfg, &notifyCfg, notifyState, evtSub, s.log, s.elog)

		case <-paramCh:
			newCfg := ReadServiceConfig(s.log)
			if newCfg.PollInterval != cfg.PollInterval {
				pollTicker.Reset(newCfg.PollInterval)
			}
			cfg = newCfg
			handler.cfg = &cfg
			notifyCfg = ReadNotifyConfig(s.log)
			s.log(LvlINF, "config=reloaded")

		case <-flushTicker.C:
			_ = store.FlushIfDirty()
		}
	}
}

// svcRunCheck performs a single check cycle in service mode.
func svcRunCheck(store *MemAuditStore, cfg *ServiceConfig, notifyCfg *NotifyConfig, notifyState *NotifyState, evtSub *EventSubscriber, log LogFunc, elog *eventlog.Log) {
	state, err := ReadDrainMode()
	if err != nil {
		LogMsg(log, LvlERR, "registry read failed", fmt.Sprintf("error=%q", err))
		_ = elog.Error(EvtRegistryFailed, fmt.Sprintf("Failed to read drain mode registry value: %s", err))
		return
	}

	transition := false
	transitionFrom := ""
	changedBy := ""

	if last := store.LastObservation(); last != nil && last.DrainMode != state.Mode {
		transition = true
		transitionFrom = last.DrainMode.String()
		log(LvlWRN,
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
				changedBy = QueryRegistryChangeUser(last.Timestamp)
			}
		}
		if changedBy != "" {
			log(LvlINF, fmt.Sprintf("changed_by=%s", changedBy))
		}
	}

	// Determine exit code and status.
	drainActive := state.Mode != AllowAll
	exitCode := 0
	stateDur := time.Duration(0)
	if since := store.StateSince(state.Mode); since != nil {
		stateDur = time.Since(*since).Truncate(time.Second)
	}
	if drainActive {
		if stateDur > cfg.GracePeriod {
			exitCode = 1
		}
	}

	rec := &AuditRecord{
		Timestamp:   time.Now(),
		Host:        state.Host,
		DrainMode:   state.Mode,
		DrainLabel:  state.Mode.String(),
		KeyModified: state.KeyModified,
		Changed:     transition,
		ChangedBy:   changedBy,
		ExitCode:    exitCode,
	}
	store.Append(rec)

	if state.Mode == AllowAll {
		log(LvlINF, fmt.Sprintf("drain_mode=%s", state.Mode), fmt.Sprintf("exit=%d", exitCode))
	} else {
		log(LvlWRN, fmt.Sprintf("drain_mode=%s", state.Mode), fmt.Sprintf("exit=%d", exitCode))
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
		notifyResult := &CheckResult{
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
		SendNotification(*notifyCfg, notifyState, notifyResult, transition, changedBy, log)
	}
}

// RunService starts the Windows service. Called by the CLI's hidden
// "service run" subcommand.
func RunService() error {
	elog, err := eventlog.Open(ServiceName)
	if err != nil {
		return fmt.Errorf("open event log: %w", err)
	}
	defer func() { _ = elog.Close() }()

	log := EventLogLogger(elog)
	return svc.Run(ServiceName, &drainService{log: log, elog: elog})
}

// InstallService registers the service with SCM.
func InstallService(exePath string, log LogFunc) error {
	// Imported here to avoid pulling in mgr for non-service builds.
	// Actually, we need mgr. Import is at package level already via svc.
	// We'll use a helper that imports mgr.
	return installServiceImpl(exePath, log)
}

// UninstallService removes the service from SCM.
func UninstallService(log LogFunc) error {
	return uninstallServiceImpl(log)
}
