//go:build windows

package svc

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/dashboard"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/debugpprof"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/logging"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/pipe"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/selfmetrics"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/spikereport"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/watcher"

	"golang.org/x/sys/windows/svc"
)

type dashboardStopper interface {
	Stop()
}

func disableDashboardRuntime(sub dashboardStopper, handler *serviceHandler, cfg dc.ServiceConfig) {
	sub.Stop()
	handler.publish(cfg, nil)
}

// Execute is the Windows service main loop.
func (s *drainService) Execute(args []string, r <-chan svc.ChangeRequest, statusCh chan<- svc.Status) (bool, uint32) {
	statusCh <- svc.Status{State: svc.StartPending, WaitHint: 10000}

	// Capture panics so the stack trace is written to both the file log and
	// the Windows Event Log before the process terminates.  Without this the
	// SCM records Event 7034 ("terminated unexpectedly") but we get no
	// indication of what actually went wrong.
	defer func() {
		if r := recover(); r != nil {
			stack := string(debug.Stack())
			slog.Error("PANIC", "panic", fmt.Sprintf("%v", r), "stack", stack, slog.Int("event_id", EvtServiceError))
			panic(r) // re-panic so the SCM sees the crash
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Load config from config.json (migrates from registry if needed).
	// Validate and write back so new fields appear with defaults.
	fullCfg, err := dc.LoadConfig()
	if err != nil {
		slog.Error("service=failed", "error", err)
		return false, 1
	}
	fullCfg.Validate()
	if err := dc.SaveConfig(fullCfg); err != nil {
		slog.Warn("config normalization failed", "error", err)
	}

	cfg := fullCfg.ToServiceConfig()
	dashCfg := fullCfg.ToDashboardConfig()
	prevEvtSpikeCfg := fullCfg.EvtSpike
	localUpdateCfg := fullCfg.Update

	// Apply Go runtime soft memory limit. Makes the GC more aggressive about
	// returning pages to the OS, which matters on memory-constrained RDS hosts.
	memLimitMB := fullCfg.MemoryLimitMB
	debug.SetMemoryLimit(int64(memLimitMB) << 20)

	notifyTargets := fullCfg.Notifications
	notifyExclusions := fullCfg.NotificationExclusions
	notifyState := &dc.NotifyState{
		LastAlertNotify:       make(map[string]time.Time),
		LastSessionWarnNotify: make(map[string]time.Time),
	}

	// evtspike spike dispatch is funnelled through spikeCh so notifyState stays
	// single-threaded (only the Execute goroutine calls SendNotification). A
	// nil channel never selects, so the branch is inert when the subsystem is
	// disabled.
	var spikeCh chan dc.SpikePayload

	slog.Info("service=starting", "version", dc.Version, "grace", cfg.GracePeriod, "poll", cfg.PollInterval, "retention_days", cfg.RetentionDays, "fetch_interval", dashCfg.FetchInterval, "memory_limit_mb", memLimitMB)

	// Optional runtime/pprof debug server. Off unless DRAINCTL_PPROF_PORT
	// is set. Loopback-only. Used for memory-leak and goroutine-leak
	// diagnosis on production boxes without rebuilding. LCI Subsystem so
	// the listener drains cleanly on SCM-stop instead of relying on
	// ctx-cancel alone.
	pprofSub := debugpprof.New()
	if err := pprofSub.Start(ctx); err != nil {
		slog.Warn("pprof failed to start", "error", err)
	}

	// Periodic self-metrics emitted at slog.Debug: runtime heap state,
	// goroutine count, RSS, GC stats, SSPI counters. Passive (no
	// listener); operators see the trend by setting log_file_level=debug
	// and grepping "selfmetrics=" out of the daily log. LCI Subsystem so
	// the goroutine drains cleanly on SCM-stop instead of relying on
	// ctx-cancel alone.
	selfMetricsSub := selfmetrics.New(selfmetrics.DefaultInterval)
	if err := selfMetricsSub.Start(ctx); err != nil {
		slog.Warn("selfmetrics failed to start", "error", err)
	}

	// Open SQLite telemetry store before the named pipe and HTTP servers.
	telDB, err := telemetry.Open(dc.DefaultDataDir())
	if err != nil {
		slog.Error("service=failed", "error", err)
		return false, 1
	}
	defer func() { _ = telDB.Close() }()

	// Import the legacy audit.jsonl into the SQLite audit table before any
	// audit writer (NewAuditStore + svcRunCheck) or drift reconciliation runs,
	// so LatestByHost baselines include pre-upgrade history (FR-020). Failure
	// is fatal: continuing would let live audit writes land before the legacy
	// import completes, contaminating the baseline that MigrateJSONL seeds
	// from latestNewStatePerHost on the next resume. The JSONL stays on disk
	// and the migration is idempotent, so the operator can restart and retry.
	migRes, migErr := telemetry.MigrateJSONL(ctx, telDB, dc.DefaultDataDir())
	if migErr != nil {
		slog.Error("service=failed", "error", fmt.Errorf("audit jsonl migration: %w", migErr))
		return false, 1
	}
	if migRes.JSONLFound {
		slog.Info("audit JSONL migration",
			"lines", migRes.LineCount,
			"imported", migRes.Imported,
			"observations", migRes.Observations,
			"skipped", migRes.Skipped,
			"backup", migRes.BackupPath,
			"duration", migRes.Duration)
	}

	metricsStore, err := telemetry.NewMetricsStore(ctx, telDB)
	if err != nil {
		slog.Error("service=failed", "error", err)
		return false, 1
	}
	// Defer ordering note: this Close runs BEFORE telDB.Close() because Go
	// defers fire LIFO; declaring this later puts it on top of the stack.
	// Required ordering: prepared stmt must be finalised before its writer
	// pool tears down.
	defer func() { _ = metricsStore.Close() }()
	maintenanceStore := telemetry.NewMaintenanceStore(telDB)
	serverStore := telemetry.NewServerStore(telDB)
	removalStore := telemetry.NewRemovalStore(telDB)
	eventSpikeStore := telemetry.NewEventSpikeStore(telDB)
	forceUpdateOutboxStore := telemetry.NewForceUpdateOutboxStore(telDB)
	freshnessStore := telemetry.NewFreshnessStore(telDB)
	auditStore, err := telemetry.NewAuditStore(ctx, telDB)
	if err != nil {
		slog.Error("service=failed", "error", err)
		return false, 1
	}
	defer func() { _ = auditStore.Close() }()

	// A dashboard-only host is a management server, not an RDSH. Do not
	// inspect its local drain registry value or create a synthetic audit row.
	// The stores remain available to the dashboard for reports received from
	// remote agents.
	if !cfg.DashboardOnly {
		// Drift reconciliation (T025, FR-001a): detect drain-state divergence
		// that happened while the service was down and emit exactly one
		// reconciliation audit row per host when needed.
		if regState, regErr := dc.ReadDrainMode(); regErr != nil {
			slog.Warn("drift_reconciliation=skipped reason=registry_read_failed", "error", regErr)
		} else {
			probe := telemetry.DrainProbe{
				Host:        regState.Host,
				ModeValue:   int(regState.Mode),
				KeyModified: regState.KeyModified,
			}
			reconcileStart := time.Now()
			if rerr := telemetry.Reconcile(ctx, telDB, auditStore, probe, reconcileStart); rerr != nil {
				slog.Warn("drift_reconciliation=failed", "error", rerr)
			} else {
				slog.Info("drift_reconciliation=complete",
					"host", regState.Host,
					"observed_mode", regState.Mode,
					"duration", time.Since(reconcileStart))
			}
		}
	}

	// Aggregator + retention workers run inside an LCI subsystem owned
	// by internal/telemetry. Shutdown is bounded at 10s via
	// waitWithTimeout so a stuck worker cannot stall svc.Stop past the
	// SCM's wait hint.
	telSub := telemetry.New(
		telemetry.Config{
			AggregatorIntervalSeconds: fullCfg.Telemetry.AggregatorIntervalSeconds,
			RetentionIntervalMinutes:  fullCfg.Telemetry.RetentionIntervalMinutes,
		},
		telemetry.Deps{
			Aggregator: telemetry.NewAggregator(telDB, fullCfg.Telemetry.AggregatorIntervalSeconds),
			Retention:  telemetry.NewRetention(telDB, fullCfg.Telemetry.RetentionIntervalMinutes, newRetentionProvider(fullCfg)),
		},
	)
	if err := telSub.Start(ctx); err != nil {
		slog.Error("service=failed", "error", fmt.Errorf("telemetry subsystem start: %w", err))
		return false, 1
	}

	// The following resources are strictly agent-only. A dashboard-only host
	// creates none of them; each dashboard_only transition gets a fresh,
	// independently cancellable context because their Stop methods are one-shot.
	var agentCtx context.Context
	var agentCancel context.CancelFunc
	var spikeSub *spikereport.Subsystem
	var regSub *watcher.RegistrySubsystem
	var regCh <-chan struct{}
	if !cfg.DashboardOnly {
		agentCtx, agentCancel = context.WithCancel(ctx)
		spikeSub = spikereport.New(dashboard.ReportSpike)
		if err := spikeSub.Start(agentCtx); err != nil {
			slog.Warn("spike report subsystem failed to start", "error", err)
		}
		regSub = watcher.NewRegistrySubsystem(dc.RegPath)
		regCh = regSub.Events()
		if err := regSub.Start(agentCtx); err != nil {
			slog.Warn("registry watcher failed, polling only", "error", err)
			regCh = nil
		}
	}

	// Dashboard-only hosts must not construct an updater. Agent-mode reloads
	// recreate one through the same reconciliation helper after a stop. The
	// MSI owns the service stop/install/start transition after a verified spawn.
	updaterSub, err := reconcileUpdaterForDashboardMode(ctx, nil, cfg.DashboardOnly, fullCfg.Update)
	if err != nil {
		slog.Warn("updater failed to start", "error", err)
	}

	// Config-file watcher subsystem. Start cannot fail (the underlying
	// FindFirstChangeNotification path silently drops to a 5 s mtime poll
	// fallback), but the err return is preserved for symmetry with the
	// other LCI subs and so future Start-side init can surface upward.
	configSub := watcher.NewConfigFileSubsystem(dc.DefaultConfigPath())
	configCh := configSub.Events()
	if err := configSub.Start(ctx); err != nil {
		slog.Warn("config watcher failed", "error", err)
		configCh = make(chan struct{}) // never fires
	}

	// Drain-state attribution is likewise local-monitor-only. It receives the
	// agent-only context so disabling agent mode releases the Security event-log
	// subscription immediately without stopping dashboard work.
	var evtSub *watcher.EventSubscriber
	if agentCtx != nil {
		if sub, err := watcher.NewEventSubscriber(agentCtx); err != nil {
			slog.Warn("event subscriber failed, attribution via wevtutil fallback", "error", err)
		} else {
			evtSub = sub
		}
	}

	// Start pipe server.
	handler := &serviceHandler{audit: auditStore, metrics: metricsStore}
	handler.publish(cfg, nil)

	// Performance monitoring is an agent responsibility. Do not construct it
	// on dashboard-only startup: its Stop method is one-shot and an agent-mode
	// transition must receive a fresh supervisor.
	var perfSub *performanceSubsystem
	if agentCtx != nil {
		perfSub = newPerformanceSubsystem(cfg.Performance, &handler.lastPerf)
		if err := perfSub.Start(agentCtx); err != nil {
			slog.Warn("performance monitoring failed to start", "error", err)
		}
	}

	// Seed the in-memory observation only for a locally monitored RDSH.
	if !cfg.DashboardOnly {
		if hostname, _ := os.Hostname(); hostname != "" {
			if latest, err := auditStore.LatestByHost(ctx); err != nil {
				slog.Warn("observation seed: audit query failed", "error", err)
			} else if row, ok := latest[hostname]; ok {
				handler.observed.Store(&observation{
					Timestamp:  row.Ts,
					DrainMode:  dc.DrainMode(row.NewState),
					StateSince: row.Ts,
					ChangedBy:  row.ChangedBy,
				})
			}
		}
	}

	var registrationSub *registrationSubsystem
	if agentCtx != nil {
		registrationSub = newRegistrationSubsystem(dashboard.Register)
		if err := registrationSub.Start(agentCtx); err != nil {
			slog.Warn("dashboard registration subsystem failed to start", "error", err)
		} else {
			handler.registration = registrationSub
		}
	}

	pipeSub := pipe.New(handler)
	if err := pipeSub.Start(ctx); err != nil {
		slog.Warn("pipe server failed to start", "error", err)
	}

	// Start dashboard if enabled. The Subsystem owns the HTTP server,
	// session/SSPI reapers, and broker; Stop drains them on SCM-stop.
	var dashState *dashboard.ServerState
	var dashSub *dashboard.Subsystem
	if dashCfg.Enabled {
		sub := dashboard.NewSubsystem(dashCfg, dc.DefaultDataDir(), metricsStore, auditStore, maintenanceStore, serverStore, eventSpikeStore, removalStore, forceUpdateOutboxStore, !cfg.DashboardOnly, freshnessStore)
		if err := sub.Start(ctx); err != nil {
			slog.Warn("dashboard failed to start", "error", err)
		} else {
			dashSub = sub
			dashState = sub.State()
			handler.publish(cfg, dashState)
			slog.Info("dashboard=started", "port", dashCfg.Port)
		}
	}

	// Auto-discover dashboard via SRV if URL is not explicitly configured.
	if dashCfg.URL == "" {
		if discovered := dashboard.DiscoverDashboardURL(); discovered != "" {
			dashCfg.URL = discovered
		}
	}

	// Prepare dashboard client and state; actual registration and config
	// fetch are deferred to the first poll tick so the service reports
	// Running to the SCM without waiting on network I/O.
	var useRemoteConfig bool
	var lastConfigFetch time.Time
	// Cached most-recent successful pull-config response. When the local
	// config.json watcher fires, we re-apply this cached remote on top of the
	// freshly-loaded local values so the dashboard's authoritative settings
	// aren't briefly overwritten by local defaults during the
	// local-reload to next-remote-fetch window (up to FetchInterval seconds).
	var lastRemote *dashboard.RemoteSettings
	dashConfigFailures := 0
	dashRegistered := false

	if dashCfg.URL != "" {
		dashboard.InitDashClient(dashCfg.TLSFingerprint)

		// Local self-registration is instant (no network); safe to do here.
		// Skip when dashboard_only is set (management server, not an RDSH).
		if dashState != nil && isLocalDashboard(dashCfg.URL) && !cfg.DashboardOnly {
			hostname, _ := os.Hostname()
			if hostname != "" {
				dashState.Register(hostname)
				dashRegistered = true
				slog.Info("dashboard=self-registered", "host", hostname)
			}
		}
		if cfg.DashboardOnly {
			slog.Info("dashboard_only=true, skipping local drain monitoring")
		}
		// Remote registration + config fetch happen on the first poll tick
		// (lastConfigFetch is zero, dashRegistered is false).
	}

	// Event-spike detection and status are local agent telemetry. A
	// dashboard-only host accepts and serves remote spike data but does not
	// subscribe to local event logs, score them, or publish a local detector.
	var evtSpikeSub *evtspike.Subsystem
	if !cfg.DashboardOnly {
		host, _ := os.Hostname()
		spikeCh = make(chan dc.SpikePayload, 16)
		evtSpikeSub = evtspike.New(fullCfg.EvtSpike, host)
		evtSpikeSub.OnSpike = func(p dc.SpikePayload) {
			if dashState != nil && dashState.OnEvtSpikeIngest != nil {
				dashState.OnEvtSpikeIngest(p)
			}
			select {
			case spikeCh <- p:
			default:
				slog.Warn("evtspike=spike_dropped reason=channel_full",
					"host", p.Host, "channel", p.Channel)
			}
		}
		evtSpikeSub.OnStatusChange = func(status evtspike.DetectorStatus) {
			if dashState != nil && dashState.OnEvtSpikeStatus != nil {
				dashState.OnEvtSpikeStatus(status)
			}
		}
		if dashState != nil && dashState.RegisterEvtSpikeStatusFunc != nil {
			localHost := host
			remoteLookup := dashState.GetRemoteEvtSpikeStatus
			dashState.RegisterEvtSpikeStatusFunc(func(h string) evtspike.DetectorStatus {
				if h != localHost {
					if remoteLookup != nil {
						return remoteLookup(h)
					}
					return evtspike.DetectorStatus{}
				}
				return evtSpikeSub.Status()
			})
		}
		if err := evtSpikeSub.Start(agentCtx); err != nil {
			slog.Warn("evtspike=start_failed", "error", err)
		} else if fullCfg.EvtSpike.Enabled {
			slog.Info("evtspike=enabled", "host", host)
		}
		handler.evtSpikeSub.Store(evtSpikeSub)
	}

	// Timers.
	pollTicker := time.NewTicker(cfg.PollInterval)
	defer pollTicker.Stop()

	// Report running immediately so the SCM doesn't wait on network I/O.
	// Dashboard registration + config fetch happen asynchronously on the
	// first poll tick (fired immediately below).
	statusCh <- svc.Status{
		State:   svc.Running,
		Accepts: svc.AcceptStop | svc.AcceptShutdown,
	}

	// Run initial check (skip in dashboard-only mode).
	if !cfg.DashboardOnly {
		svcRunCheck(ctx, handler, &cfg, notifyTargets, notifyExclusions, notifyState, &dashCfg, dashState, evtSub, perfSub.Collector(), perfSub.TriggerState(), evtSpikeSub, updaterSub)
	}
	slog.Info("service=running",
		slog.Int("event_id", EvtServiceStarted),
		"version", dc.Version,
		"poll", cfg.PollInterval,
		"grace", cfg.GracePeriod,
		"retention_days", cfg.RetentionDays)

	// Fire dashboard registration + config fetch shortly after startup
	// instead of waiting for the first full poll interval.
	dashBootstrapSub := newDashboardBootstrapSubsystem(!cfg.DashboardOnly && dashCfg.URL != "" && !dashRegistered, 5*time.Second)
	if err := dashBootstrapSub.Start(ctx); err != nil {
		slog.Warn("dashboard bootstrap subsystem failed to start", "error", err)
	}
	dashBootstrap := dashBootstrapSub.Events()

	// fetchRemoteConfig pulls from the dashboard, applies results, and
	// reconciles the evtspike/perf/ticker subsystems. Successful fetches obey
	// Dashboard.FetchInterval; failures use the existing exponential backoff.
	// The registration and config-reload paths can force an immediate fetch by
	// clearing lastConfigFetch before calling this closure.
	fetchRemoteConfig := func() {
		if cfg.DashboardOnly || dashCfg.URL == "" {
			return
		}
		fetchStarted := time.Now()
		if !remoteConfigFetchDue(fetchStarted, lastConfigFetch, dashConfigFailures, dashCfg.FetchInterval) {
			return
		}
		lastConfigFetch = fetchStarted
		slog.Debug("dashboard config fetch", "url", dashCfg.URL)
		var cfgRemote *dashboard.RemoteSettings
		var cfgErr error
		if dashState != nil {
			cfgRemote, cfgErr = dashboard.GetSettings()
		} else {
			cfgRemote, cfgErr = dashboard.FetchSettings(ctx, dashCfg.URL)
		}
		if cfgErr != nil {
			dashConfigFailures++
			nextIn := backoffDuration(dashCfg.FetchInterval, dashConfigFailures)
			slog.Warn("dashboard: settings refresh failed, using cached",
				"error", cfgErr, "next_retry_in", nextIn.Round(time.Second))
			return
		}
		dashConfigFailures = 0
		useRemoteConfig = true
		lastRemote = cfgRemote
		oldPollInterval := cfg.PollInterval
		effectiveEvtSpike := prevEvtSpikeCfg
		applyRemoteConfig(cfgRemote, &cfg, &notifyTargets, &effectiveEvtSpike, &notifyExclusions)
		if updaterSub != nil {
			updaterSub.UpdateConfig(effectiveUpdateConfig(localUpdateCfg, cfgRemote))
		}
		handler.publish(cfg, dashState)
		if perfSub != nil {
			perfSub.Reload(cfg.Performance)
		}
		if cfg.PollInterval != oldPollInterval {
			pollTicker.Reset(cfg.PollInterval)
			slog.Info("config=poll-interval-remote",
				"old", oldPollInterval, "new", cfg.PollInterval)
		}
		if evtSpikeSub != nil {
			if err := applyEvtSpikeConfigReload(evtSpikeSub, prevEvtSpikeCfg, effectiveEvtSpike); err != nil {
				slog.Warn("evtspike remote reload failed", "error", err)
			} else {
				prevEvtSpikeCfg = effectiveEvtSpike
			}
		}
	}

	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Stop, svc.Shutdown:
				slog.Info("service=stopping")
				statusCh <- svc.Status{State: svc.StopPending, WaitHint: 10000}
				cancel()
				if agentCancel != nil {
					agentCancel()
				}
				dashBootstrapSub.Stop()
				if evtSpikeSub != nil {
					evtSpikeSub.Stop()
				}
				if updaterSub != nil {
					updaterSub.Stop()
				}
				if perfSub != nil {
					perfSub.Stop()
				}
				selfMetricsSub.Stop()
				pprofSub.Stop()
				pipeSub.Stop()
				if registrationSub != nil {
					registrationSub.Stop()
				}
				if regSub != nil {
					regSub.Stop()
				}
				configSub.Stop()
				if dashSub != nil {
					dashSub.Stop()
				}
				waitWithTimeout("telemetry", telSub.Stop, 10*time.Second)
				if spikeSub != nil {
					waitWithTimeout("spike_report", spikeSub.Stop, 10*time.Second)
				}
				slog.Info("service=stopped", slog.Int("event_id", EvtServiceStopped))
				return false, 0
			case svc.Interrogate:
				statusCh <- c.CurrentStatus
			}

		case <-regCh:
			if !cfg.DashboardOnly {
				slog.Info("trigger=registry_change")
				svcRunCheck(ctx, handler, &cfg, notifyTargets, notifyExclusions, notifyState, &dashCfg, dashState, evtSub, perfSub.Collector(), perfSub.TriggerState(), evtSpikeSub, updaterSub)
			}

		case spike := <-spikeCh:
			spikeResult := &dc.CheckResult{
				Version:   dc.Version,
				Timestamp: time.Now(),
				Host:      spike.Host,
				Spike:     &spike,
			}
			dc.SendNotificationWithExclusions(notifyTargets, notifyExclusions, notifyState, spikeResult, dc.TriggerEventSpike, "")
			// Propagate to a remote central dashboard. The local-dashboard path
			// (dashState != nil) already appended via OnEvtSpikeIngest when the
			// subsystem fired OnSpike; reporting again would double-insert.
			if dashState == nil && dashCfg.URL != "" && dashRegistered {
				spikeCopy := spike
				spikeSub.Forward(dashCfg.URL, &spikeCopy)
			}

		case <-dashBootstrap:
			// First async dashboard registration attempt after startup.
			dashBootstrap = nil // one-shot
			if !cfg.DashboardOnly && dashCfg.URL != "" && !dashRegistered {
				if registerWithDashboard(ctx, &dashCfg) {
					dashRegistered = true
					dashConfigFailures = 0
					// Pull config immediately on successful registration so
					// the agent doesn't spend up to one PollInterval running
					// local defaults before the dashboard authoritative
					// settings arrive.
					fetchRemoteConfig()
				}
			}

		case <-pollTicker.C:
			// Retry registration if a previous attempt failed. Once registered,
			// dashRegistered stays true and this branch is skipped.
			if !cfg.DashboardOnly && dashCfg.URL != "" && !dashRegistered {
				if registerWithDashboard(ctx, &dashCfg) {
					dashRegistered = true
					dashConfigFailures = 0
					// Same register-triggered fetch as the bootstrap path.
					fetchRemoteConfig()
				}
			}

			// Check whether remote config is due. The closure returns without
			// network or JSON work until FetchInterval elapses; failures retain
			// exponential backoff.
			fetchRemoteConfig()
			if !cfg.DashboardOnly {
				slog.Debug("diag: step=svc_run_check")
				svcRunCheck(ctx, handler, &cfg, notifyTargets, notifyExclusions, notifyState, &dashCfg, dashState, evtSub, perfSub.Collector(), perfSub.TriggerState(), evtSpikeSub, updaterSub)
			}

		case <-configCh:
			newFullCfg, err := dc.LoadConfig()
			if err != nil {
				slog.Warn("config reload failed", "error", err)
				continue
			}
			// Apply log levels before emitting any log messages about the reload.
			if fl, err := logging.ParseLevel(newFullCfg.LogFileLevel); err == nil {
				s.fileLevel.Set(fl)
			}
			if el, err := logging.ParseLevel(newFullCfg.LogEventLevel); err == nil {
				s.etwLevel.Set(el)
			}
			if newFullCfg.MemoryLimitMB != memLimitMB {
				memLimitMB = newFullCfg.MemoryLimitMB
				debug.SetMemoryLimit(int64(memLimitMB) << 20)
				slog.Info("memory limit updated", "mb", memLimitMB)
			}
			newCfg := newFullCfg.ToServiceConfig()
			wasDashboardOnly := cfg.DashboardOnly
			effectiveEvtSpike := newFullCfg.EvtSpike
			localUpdateCfg = newFullCfg.Update
			effectiveUpdateCfg := localUpdateCfg
			// Synchronously refresh the dashboard-authoritative snapshot before
			// merging. The cached `lastRemote` is populated on the next poll
			// tick (up to one PollInterval away), so without this refresh the
			// newly-loaded local config runs with stale (or missing) remote
			// overrides; a server currently in ALERT on CPU thresholds would
			// report HEALTHY for one poll because the local config file
			// has no thresholds of its own. Dashboard URL unchanged from the
			// live config is a precondition: a URL change is handled below and
			// will re-initialize the client + re-register before the next
			// pollTicker fires its own fetchRemoteConfig.
			if !newCfg.DashboardOnly && newFullCfg.Dashboard.URL != "" && newFullCfg.Dashboard.URL == dashCfg.URL {
				var fresh *dashboard.RemoteSettings
				var ferr error
				if dashState != nil {
					fresh, ferr = dashboard.GetSettings()
				} else {
					fresh, ferr = dashboard.FetchSettings(ctx, newFullCfg.Dashboard.URL)
				}
				if ferr == nil {
					lastRemote = fresh
					useRemoteConfig = true
					dashConfigFailures = 0
					lastConfigFetch = time.Now()
				} else {
					slog.Warn("dashboard: reload-time fetch failed, using cached", "error", ferr)
				}
			}
			// Merge the dashboard-authoritative values on top of the freshly-
			// loaded local config. If the inline fetch above succeeded,
			// `lastRemote` is fresh; otherwise we fall back to the cached
			// snapshot (bounded by the existing up-to-FetchInterval staleness
			// window; no worse than the previous behavior).
			if useRemoteConfig && lastRemote != nil && newFullCfg.Dashboard.URL != "" {
				applyRemoteConfig(lastRemote, &newCfg, &notifyTargets, &effectiveEvtSpike, &notifyExclusions)
				effectiveUpdateCfg = effectiveUpdateConfig(localUpdateCfg, lastRemote)
			}
			if newCfg.PollInterval != cfg.PollInterval {
				pollTicker.Reset(newCfg.PollInterval)
			}
			cfg = newCfg
			handler.publish(cfg, dashState)
			// Reconcile the updater lifecycle with dashboard-only mode. A
			// dashboard-only host must never retain a sleeping updater goroutine;
			// enabling agent mode constructs and starts one with the effective
			// (local plus remote) policy before the next check can consume it.
			nextUpdater, uerr := reconcileUpdaterForDashboardMode(ctx, updaterSub, newCfg.DashboardOnly, effectiveUpdateCfg)
			if uerr != nil {
				slog.Warn("updater failed to start on config reload", "error", uerr)
			}
			updaterSub = nextUpdater
			newDashCfg := newFullCfg.ToDashboardConfig()

			// Handle dashboard URL or TLS fingerprint changes.
			if newDashCfg.URL != dashCfg.URL || newDashCfg.TLSFingerprint != dashCfg.TLSFingerprint {
				if newDashCfg.URL != "" {
					dashboard.InitDashClient(newDashCfg.TLSFingerprint)
					lastConfigFetch = time.Time{} // force re-fetch on next poll
					dashConfigFailures = 0        // reset backoff on URL change
					dashRegistered = false        // re-register against new URL
				}
			}

			// If dashboard URL was removed, revert to local targets.
			if newDashCfg.URL == "" && useRemoteConfig {
				useRemoteConfig = false
				notifyTargets = newFullCfg.Notifications
				notifyExclusions = newFullCfg.NotificationExclusions
				pruneNotifyState(notifyState, notifyTargets)
			} else if !useRemoteConfig {
				notifyTargets = newFullCfg.Notifications
				notifyExclusions = newFullCfg.NotificationExclusions
				pruneNotifyState(notifyState, notifyTargets)
			}

			if dashSub != nil {
				dashSub.UpdateHeartbeatInterval(newDashCfg.HeartbeatInterval)
				dashSub.SetLocalForceUpdateSupported(!cfg.DashboardOnly)
				if newDashCfg.RDConnectionBroker != dashCfg.RDConnectionBroker {
					dashSub.UpdateRDConnectionBroker(newDashCfg.RDConnectionBroker)
				}
			}

			// Reconcile the dashboard listener in both directions. A failed
			// enable is retried on the next config event because runtime state,
			// not the previous requested value, decides whether Start is needed.
			if !newDashCfg.Enabled && dashState != nil {
				disableDashboardRuntime(dashSub, handler, cfg)
				dashSub = nil
				dashState = nil
				dashRegistered = false
				slog.Info("dashboard=stopped", "reason", "config reload")
			} else if newDashCfg.Enabled && dashState == nil {
				sub := dashboard.NewSubsystem(newDashCfg, dc.DefaultDataDir(), metricsStore, auditStore, maintenanceStore, serverStore, eventSpikeStore, removalStore, forceUpdateOutboxStore, !cfg.DashboardOnly, freshnessStore)
				if err := sub.Start(ctx); err != nil {
					slog.Warn("dashboard failed to start on config reload", "error", err)
				} else {
					dashSub = sub
					dashState = sub.State()
					handler.publish(cfg, dashState)
					slog.Info("dashboard=started", "port", newDashCfg.Port, "reason", "late start")
					if !cfg.DashboardOnly && isLocalDashboard(newDashCfg.URL) {
						if h, _ := os.Hostname(); h != "" {
							dashState.Register(h)
							dashRegistered = true
							slog.Info("dashboard=self-registered", "host", h)
						}
					}
				}
			} else if dashState != nil && !dashRegistered && !cfg.DashboardOnly &&
				isLocalDashboard(newDashCfg.URL) && !isLocalDashboard(dashCfg.URL) {
				// URL just changed to point at this machine; self-register in
				// memory immediately rather than waiting for the next poll tick.
				if h, _ := os.Hostname(); h != "" {
					dashState.Register(h)
					dashRegistered = true
					slog.Info("dashboard=self-registered", "host", h)
				}
			}

			// Preserve SRV-discovered URL if the config file doesn't set one.
			if newDashCfg.URL == "" && dashCfg.URL != "" {
				newDashCfg.URL = dashCfg.URL
			}
			dashCfg = newDashCfg
			slog.Info("config=reloaded")

			if !wasDashboardOnly && cfg.DashboardOnly {
				// Stop every local-agent resource. These lifecycle types are
				// one-shot, so a later agent-mode transition must construct new
				// instances instead of attempting to restart stopped ones.
				if agentCancel != nil {
					agentCancel()
					agentCancel = nil
				}
				if evtSpikeSub != nil {
					evtSpikeSub.Stop()
					evtSpikeSub = nil
					handler.evtSpikeSub.Store(nil)
				}
				if perfSub != nil {
					perfSub.Stop()
					perfSub = nil
				}
				if registrationSub != nil {
					registrationSub.Stop()
					registrationSub = nil
					handler.registration = nil
				}
				if regSub != nil {
					regSub.Stop()
					regSub = nil
				}
				if spikeSub != nil {
					spikeSub.Stop()
					spikeSub = nil
				}
				evtSub = nil
				regCh = nil
				spikeCh = nil
			} else if wasDashboardOnly && !cfg.DashboardOnly {
				agentCtx, agentCancel = context.WithCancel(ctx)
				spikeSub = spikereport.New(dashboard.ReportSpike)
				if err := spikeSub.Start(agentCtx); err != nil {
					slog.Warn("spike report subsystem failed to start after dashboard-only reload", "error", err)
				}
				regSub = watcher.NewRegistrySubsystem(dc.RegPath)
				regCh = regSub.Events()
				if err := regSub.Start(agentCtx); err != nil {
					slog.Warn("registry watcher failed to start after dashboard-only reload", "error", err)
					regCh = nil
				}
				registrationSub = newRegistrationSubsystem(dashboard.Register)
				if err := registrationSub.Start(agentCtx); err != nil {
					slog.Warn("dashboard registration subsystem failed to start after dashboard-only reload", "error", err)
				} else {
					handler.registration = registrationSub
				}
				if sub, err := watcher.NewEventSubscriber(agentCtx); err != nil {
					slog.Warn("event subscriber failed to start after dashboard-only reload", "error", err)
				} else {
					evtSub = sub
				}
				perfSub = newPerformanceSubsystem(cfg.Performance, &handler.lastPerf)
				if err := perfSub.Start(agentCtx); err != nil {
					slog.Warn("performance monitoring failed to start after dashboard-only reload", "error", err)
				}
			}

			// A dashboard-only host intentionally has no local detector. If
			// monitoring is enabled at runtime, construct it before applying
			// the current config; the normal initial-start path did not create
			// an event-log subscriber for this management role.
			if !cfg.DashboardOnly && evtSpikeSub == nil {
				host, _ := os.Hostname()
				spikeCh = make(chan dc.SpikePayload, 16)
				evtSpikeSub = evtspike.New(effectiveEvtSpike, host)
				evtSpikeSub.OnSpike = func(p dc.SpikePayload) {
					if dashState != nil && dashState.OnEvtSpikeIngest != nil {
						dashState.OnEvtSpikeIngest(p)
					}
					select {
					case spikeCh <- p:
					default:
						slog.Warn("evtspike=spike_dropped reason=channel_full",
							"host", p.Host, "channel", p.Channel)
					}
				}
				evtSpikeSub.OnStatusChange = func(status evtspike.DetectorStatus) {
					if dashState != nil && dashState.OnEvtSpikeStatus != nil {
						dashState.OnEvtSpikeStatus(status)
					}
				}
				if dashState != nil && dashState.RegisterEvtSpikeStatusFunc != nil {
					localHost := host
					remoteLookup := dashState.GetRemoteEvtSpikeStatus
					dashState.RegisterEvtSpikeStatusFunc(func(h string) evtspike.DetectorStatus {
						if h != localHost {
							if remoteLookup != nil {
								return remoteLookup(h)
							}
							return evtspike.DetectorStatus{}
						}
						return evtSpikeSub.Status()
					})
				}
				if err := evtSpikeSub.Start(agentCtx); err != nil {
					slog.Warn("evtspike=start_failed", "error", err)
				} else {
					handler.evtSpikeSub.Store(evtSpikeSub)
				}
			}
			if !cfg.DashboardOnly && evtSpikeSub != nil {
				if err := applyEvtSpikeConfigReload(evtSpikeSub, prevEvtSpikeCfg, effectiveEvtSpike); err != nil {
					slog.Warn("evtspike reload failed", "error", err)
				}
				prevEvtSpikeCfg = effectiveEvtSpike
			}

			// A fresh supervisor already runs the effective configuration;
			// existing agent mode reloads in place and samples immediately when
			// settings changed.
			if !cfg.DashboardOnly && perfSub != nil {
				if perfSub.Reload(cfg.Performance) || wasDashboardOnly {
					svcRunCheck(ctx, handler, &cfg, notifyTargets, notifyExclusions, notifyState, &dashCfg, dashState, evtSub, perfSub.Collector(), perfSub.TriggerState(), evtSpikeSub, updaterSub)
				}
			}
			slog.Info("config=reloaded-etw", slog.Int("event_id", EvtConfigReloaded))
		}
	}
}
