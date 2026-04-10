//go:build windows

package svc

import (
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/dashboard"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/perfmon"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/store"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/watcher"
)

// svcRunCheck performs a single check cycle in service mode.
// dashState is non-nil when the dashboard runs in this process (local reporting).
func svcRunCheck(st *store.MemAuditStore, cfg *dc.ServiceConfig, targets []dc.NotificationTarget, notifyState *dc.NotifyState, dashCfg *dc.DashboardConfig, dashState *dashboard.ServerState, evtSub *watcher.EventSubscriber, perfCollector *perfmon.Collector, perfTriggerState *perfmon.PerfTriggerState, lastPerf *atomic.Pointer[dc.PerfSnapshot], lastSessions *atomic.Pointer[dc.SessionSummary]) {
	checkStart := time.Now()
	slog.Debug("diag: check=read_drain_mode")
	state, err := dc.ReadDrainMode()
	if err != nil {
		slog.Error("registry read failed", "error", err, slog.Int("event_id", EvtRegistryFailed))
		return
	}

	transition := false
	transitionFrom := ""
	changedBy := ""

	if last := st.LastObservation(); last != nil && last.DrainMode != state.Mode {
		transition = true
		transitionFrom = last.DrainMode.String()
		slog.Warn("transition=true", "from", last.DrainMode, "to", state.Mode)

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
			slog.Info("changed_by", "user", changedBy)
		}
	}

	// Determine exit code and status.
	drainActive := state.Mode != dc.AllowAll
	stateDur := time.Duration(0)
	var stateSince *time.Time
	if since := st.StateSince(state.Mode); since != nil {
		stateDur = time.Since(*since).Truncate(time.Second)
		s := since.Local()
		stateSince = &s
	}

	var status, message string
	var exitCode int
	status, message, exitCode = dc.ClassifyState(drainActive, stateDur, cfg.GracePeriod)

	// Session tracking.
	slog.Debug("diag: check=get_sessions")
	sess := dc.GetSessionSummary()
	if sess == nil {
		slog.Warn("session enumeration failed", "hint", "verify service runs as LocalSystem")
	}
	if lastSessions != nil {
		lastSessions.Store(sess)
	}

	// Performance counters (service-mode only).
	slog.Debug("diag: check=perfmon", "collector_nil", perfCollector == nil)
	var perfSnap *dc.PerfSnapshot
	if perfCollector != nil {
		snap, err := perfCollector.Collect()
		if err != nil {
			slog.Warn("perfmon collect failed", "error", err)
		} else {
			perfSnap = snap
			if lastPerf != nil {
				lastPerf.Store(snap)
			}
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
	if sess != nil {
		rec.ActiveSessions = sess.ActiveSessions
		rec.DisconnectedSessions = sess.DisconnectedSessions
		rec.TotalSessions = sess.TotalSessions
		rec.MaxSessions = sess.MaxSessions
	}
	if perfSnap != nil {
		rec.CPUPct = perfSnap.CPUPct
		rec.InputDelayMax = perfSnap.InputDelayMax
		rec.MemAvailMB = perfSnap.MemAvailMB
		rec.MemTotalMB = perfSnap.MemTotalMB
		rec.DiskQueue = perfSnap.DiskQueue
		rec.TCPRetransSec = perfSnap.TCPRetrans
	}
	st.Append(rec)
	slog.Debug("diag: check=audit_appended")

	if state.Mode == dc.AllowAll {
		slog.Info("drain_mode", "mode", state.Mode, "exit", exitCode)
	} else {
		slog.Warn("drain_mode", "mode", state.Mode, "exit", exitCode)
	}

	// Emit state-specific ETW events via slog with event_id attribute.
	if transition {
		cb := changedBy
		if cb == "" {
			cb = "unknown"
		}
		slog.Info(fmt.Sprintf("State transition detected on %s: %s -> %s. Changed by: %s.",
			state.Host, transitionFrom, state.Mode, cb),
			slog.Int("event_id", EvtTransition))
	}

	switch status {
	case "Alert":
		cb := changedBy
		if cb == "" {
			cb = "unknown"
		}
		slog.Error(fmt.Sprintf("ALERT: Drain mode active on %s for %s, exceeding grace period of %s. Mode: %s. Changed by: %s.",
			state.Host, stateDur, cfg.GracePeriod, state.Mode, cb),
			slog.Int("event_id", EvtCheckAlert))
	case "Grace":
		remaining := cfg.GracePeriod - stateDur
		slog.Warn(fmt.Sprintf("Drain mode active on %s, within grace period (%s remaining). Mode: %s.",
			state.Host, remaining.Truncate(time.Second), state.Mode),
			slog.Int("event_id", EvtCheckGrace))
	default:
		slog.Info(fmt.Sprintf("Drain mode check: %s on %s. All connections allowed. State duration: %s.",
			state.Mode, state.Host, stateDur),
			slog.Int("event_id", EvtCheckHealthy))
	}

	// Build CheckResult for notifications and dashboard reporting.

	dur := stateDur.Seconds()
	connAllowed := !drainActive
	result := &dc.CheckResult{
		Version:              dc.Version,
		Timestamp:            time.Now(),
		Host:                 state.Host,
		DrainModeLabel:       state.Mode.String(),
		DrainModeValue:       uint32(state.Mode),
		GracePeriodSeconds:   int(cfg.GracePeriod.Seconds()),
		StateSince:           stateSince,
		StateDurationSeconds: &dur,
		Status:               status,
		ConnectionsAllowed:   &connAllowed,
		Transition:           transition,
		TransitionFrom:       transitionFrom,
		ChangedBy:            changedBy,
		Sessions:             sess,
		Performance:          perfSnap,
		Message:              message,
		ExitCode:             exitCode,
	}

	// Determine triggers and send notifications.
	slog.Debug("diag: check=notifications", "targets", len(targets))
	if len(targets) > 0 {
		var triggers []dc.Trigger

		if transition {
			if state.Mode != dc.AllowAll {
				triggers = append(triggers, dc.TriggerDrainOn)
			} else {
				triggers = append(triggers, dc.TriggerDrainOff)
			}
		}
		if status == "Grace" && transition {
			triggers = append(triggers, dc.TriggerGraceEntered)
		}
		if status == "Alert" {
			triggers = append(triggers, dc.TriggerAlert)
		}
		if status == "Healthy" && transition {
			triggers = append(triggers, dc.TriggerHealthy)
		}

		// Session utilization warning.
		if sess != nil && cfg.SessionWarningThreshold > 0 && sess.MaxSessions > 0 && sess.UtilizationPct >= cfg.SessionWarningThreshold {
			triggers = append(triggers, dc.TriggerSessionWarning)
		} else if sess != nil && sess.MaxSessions > 0 {
			// Session data is available and utilization is below threshold — reset
			// the per-target cooldown so the warning fires again the next time
			// utilization climbs above the threshold.
			// Do NOT reset when sess==nil (WTS API error) or MaxSessions==0
			// (uncapped server): in both cases we cannot confirm utilization is
			// safe, so keeping the cooldown avoids spurious re-notifications.
			resetSessionWarnCooldown(notifyState, cfg)
		}

		// Performance threshold evaluation.
		if perfSnap != nil && perfTriggerState != nil {
			perfTriggers := perfmon.EvaluateThresholds(perfSnap, cfg.Performance, perfTriggerState)
			for _, pt := range perfTriggers {
				triggers = append(triggers, pt)
				// Override message with performance-specific detail.
				result.Message = perfmon.TriggerMessage(pt, perfSnap, cfg.Performance)
			}
			// Reset cooldowns for perf triggers that are no longer active.
			dc.ResetPerfCooldown(notifyState, perfTriggers)
		}

		for _, trigger := range triggers {
			dc.SendNotification(targets, notifyState, result, trigger, changedBy)
		}
	}

	// Report to dashboard if configured.
	if dashCfg != nil && dashCfg.URL != "" {
		slog.Debug("diag: check=dashboard_report", "url", dashCfg.URL)
		if dashState != nil {
			if dashState.ReportLocal(result.Host, result) {
				slog.Debug("dashboard heartbeat (local)", "host", result.Host)
			} else {
				slog.Warn("dashboard heartbeat (local): host not registered", "host", result.Host)
			}
		} else {
			slog.Debug("dashboard heartbeat sending", "url", dashCfg.URL)
			dashboard.ReportState(dashCfg.URL, result)
		}
	}

	slog.Debug("poll tick", "mode", state.Mode, "status", status, "elapsed", time.Since(checkStart).Truncate(time.Microsecond))
}

// pruneNotifyState removes per-URL entries from state that no longer correspond
// to any active notification target, preventing stale rate-limit state from
// affecting new or renamed targets.
func pruneNotifyState(state *dc.NotifyState, targets []dc.NotificationTarget) {
	active := make(map[string]bool, len(targets))
	for _, t := range targets {
		if t.URL != "" {
			active[t.URL] = true
		}
	}
	for k := range state.LastAlertNotify {
		if !active[k] {
			delete(state.LastAlertNotify, k)
		}
	}
	for k := range state.LastSessionWarnNotify {
		if !active[k] {
			delete(state.LastSessionWarnNotify, k)
		}
	}
	for k := range state.LastPerfNotify {
		if !active[k] {
			delete(state.LastPerfNotify, k)
		}
	}
}

// applyRemoteConfig updates service config from dashboard-sourced notification settings.
// Values are clamped to their valid ranges so a misconfigured or compromised dashboard
// cannot inject out-of-range values into the service.
func applyRemoteConfig(remote *dashboard.RemoteNotifyConfig, cfg *dc.ServiceConfig, targets *[]dc.NotificationTarget) {
	*targets = remote.Notifications
	if remote.SessionWarningThreshold >= 0 {
		t := remote.SessionWarningThreshold
		if t > 100 {
			t = 100
		}
		cfg.SessionWarningThreshold = t
	}
	if remote.GracePeriod > 0 {
		gp := remote.GracePeriod
		if gp > 1440 {
			gp = 1440
		}
		cfg.GracePeriod = time.Duration(gp) * time.Minute
	}
	// Apply remote performance config unless locally force-disabled.
	if remote.Performance != nil && !cfg.Performance.ForceDisabled {
		remote.Performance.ForceDisabled = cfg.Performance.ForceDisabled // preserve local flag
		cfg.Performance = *remote.Performance
	}
}

// resetSessionWarnCooldown clears per-target session-warning cooldown entries
// when session monitoring is enabled but utilization is currently at or below
// the threshold. This ensures the warning fires again the next time utilization
// rises above the threshold, rather than remaining suppressed indefinitely.
func resetSessionWarnCooldown(state *dc.NotifyState, cfg *dc.ServiceConfig) {
	if cfg.SessionWarningThreshold <= 0 {
		return
	}
	clear(state.LastSessionWarnNotify)
}
