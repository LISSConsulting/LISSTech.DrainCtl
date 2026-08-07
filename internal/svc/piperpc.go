//go:build windows

package svc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/dashboard"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// observation is the latest per-tick sample of the local host's drain mode.
// The persistent record of transitions lives in the SQLite audit table; this
// struct is an in-memory cache used by the pipe handler to answer live status
// queries and by svcRunCheck to detect transitions between ticks.
//
// ChangedBy is carried across non-transition ticks so the dashboard's
// `Changed By` column reflects "who set the current state", not "who did
// anything on this tick" (which is only populated when a transition is
// detected; every subsequent tick would otherwise clear the display).
type observation struct {
	Timestamp  time.Time
	DrainMode  dc.DrainMode
	StateSince time.Time
	ChangedBy  string
}

// handlerSnapshot is immutable after publication. Config values are copied so
// reloads cannot race pipe requests; dashboard state is replaced atomically
// when the listener starts or stops.
type handlerSnapshot struct {
	cfg       dc.ServiceConfig
	dashState *dashboard.ServerState
}

// serviceHandler is the pipe handler bridge between the service state
// and the named pipe server.
type serviceHandler struct {
	audit        *telemetry.AuditStore
	metrics      *telemetry.MetricsStore // nil is tolerated in tests; HandleHistory skips perf enrichment
	observed     atomic.Pointer[observation]
	state        atomic.Pointer[handlerSnapshot]
	lastPerf     atomic.Pointer[dc.PerfSnapshot]
	lastSessions atomic.Pointer[dc.SessionSummary]
	evtSpikeSub  atomic.Pointer[evtspike.Subsystem] // nil while evtspike subsystem has not been constructed
	registration *registrationSubsystem
}

func (h *serviceHandler) publish(cfg dc.ServiceConfig, dashState *dashboard.ServerState) {
	h.state.Store(&handlerSnapshot{cfg: cfg, dashState: dashState})
}

func (h *serviceHandler) HandleStatus() *dc.CheckResult {
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

	snapshot := h.state.Load()
	if snapshot == nil {
		return &dc.CheckResult{
			Version:   dc.Version,
			Timestamp: time.Now(),
			Status:    "Error",
			Message:   "service configuration unavailable",
			ExitCode:  2,
		}
	}
	gp := snapshot.cfg.GracePeriod

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

	// State duration from the observed cache — only trust it when the cached
	// mode still matches what we just read from the registry. A mismatch means
	// the state changed between the last tick and this pipe request; the next
	// tick will refresh the cache.
	obs := h.observed.Load()
	if obs != nil && obs.DrainMode == state.Mode {
		s := obs.StateSince.Local()
		res.StateSince = &s
		dur := time.Since(obs.StateSince).Seconds()
		res.StateDurationSeconds = &dur
	}

	// Determine status.
	stateDur := time.Duration(0)
	if res.StateDurationSeconds != nil {
		stateDur = time.Duration(*res.StateDurationSeconds) * time.Second
	}

	res.Status, res.Message, res.ExitCode = dc.ClassifyState(drainActive, stateDur, gp)

	// Use cached session and performance data from the last poll cycle.
	// GetSessionSummary() may fail in the pipe handler goroutine context
	// (different thread security token), so prefer the cached snapshot.
	if sess := h.lastSessions.Load(); sess != nil {
		res.Sessions = sess
	}
	res.Performance = h.lastPerf.Load()

	// Check for transition.
	if obs != nil && obs.DrainMode != state.Mode {
		res.Transition = true
		res.TransitionFrom = obs.DrainMode.String()
	}

	return res
}

// historyPerfCounters names the metrics_raw counters that populate the perf
// and session fields on a `drainctl history` audit row. Kept in sync with
// the writer side in internal/dashboard/server.go `checkResultSamples`.
var historyPerfCounters = []string{
	"cpu_pct",
	"input_delay_max_ms",
	"mem_avail_mb",
	"mem_total_mb",
	"disk_queue",
	"tcp_retrans_sec",
	"sessions_active",
	"sessions_disconnected",
	"sessions_total",
	"sessions_max",
}

// historyPerfToleranceMs is the ±window used when joining a history row
// against metrics_raw. Two minutes covers a sampler interval up to ~60 s
// with one missed tick; counters outside this window render as `-`.
const historyPerfToleranceMs int64 = 2 * 60 * 1000

// HandleHistory returns the most recent audit rows from the SQLite audit
// table. Only drain-mode transitions (and startup reconciliation rows) are
// stored — per-tick observations are no longer persisted (T059). The
// changesOnly filter drops reconciliation-style no-op rows.
//
// Perf and session counters are NOT stored on audit rows; they are joined
// from metrics_raw at read time, picking the closest sample to each
// transition's timestamp. This preserves the pre-007 `drainctl history`
// column shape (CPU%, INPUT DLY, SESSIONS) while keeping the audit schema
// narrow and append-only.
func (h *serviceHandler) HandleHistory(limit int, changesOnly bool) []dc.AuditRecord {
	if h.audit == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	recs, _, err := h.audit.QueryRange(ctx, telemetry.QueryFilter{
		Limit:       limit,
		ChangesOnly: changesOnly,
	})
	if err != nil {
		slog.Warn("pipe history query failed", "error", err)
		return nil
	}
	out := make([]dc.AuditRecord, len(recs))
	for i, r := range recs {
		out[i] = auditFromTelemetry(r)
		if h.metrics != nil {
			if values, mErr := h.metrics.NearestCounters(ctx, r.Host, r.Ts, historyPerfToleranceMs, historyPerfCounters); mErr == nil {
				applyPerfCounters(&out[i], values)
			} else {
				slog.Debug("pipe history perf enrich failed",
					"host", r.Host, "ts", r.Ts, "error", mErr)
			}
		}
	}
	return out
}

// applyPerfCounters copies values from a metrics_raw lookup into the
// per-row perf/session fields on an AuditRecord. Missing counters are
// left at the zero value so the CLI formatter renders them as `-`.
func applyPerfCounters(rec *dc.AuditRecord, values map[string]float64) {
	if v, ok := values["cpu_pct"]; ok {
		rec.CPUPct = v
	}
	if v, ok := values["input_delay_max_ms"]; ok {
		rec.InputDelayMax = v
	}
	if v, ok := values["mem_avail_mb"]; ok {
		rec.MemAvailMB = v
	}
	if v, ok := values["mem_total_mb"]; ok {
		rec.MemTotalMB = v
	}
	if v, ok := values["disk_queue"]; ok {
		rec.DiskQueue = v
	}
	if v, ok := values["tcp_retrans_sec"]; ok {
		rec.TCPRetransSec = v
	}
	if v, ok := values["sessions_active"]; ok {
		rec.ActiveSessions = int(v)
	}
	if v, ok := values["sessions_disconnected"]; ok {
		rec.DisconnectedSessions = int(v)
	}
	if v, ok := values["sessions_total"]; ok {
		rec.TotalSessions = int(v)
	}
	if v, ok := values["sessions_max"]; ok {
		rec.MaxSessions = int(v)
	}
}

// auditFromTelemetry maps a telemetry audit row to the public dc.AuditRecord
// shape for the pipe handler. Internal-package mirror of the identically-named
// helper in root-package history.go; duplicated because internal/svc cannot
// reach unexported root helpers.
func auditFromTelemetry(r telemetry.AuditRecord) dc.AuditRecord {
	rec := dc.AuditRecord{
		Timestamp:      r.Ts,
		Host:           r.Host,
		DrainMode:      dc.DrainMode(r.NewState),
		ChangedBy:      r.ChangedBy,
		Changed:        r.PrevState != r.NewState,
		Reconciliation: r.Reconciliation,
		Reason:         r.Reason,
	}
	rec.DrainLabel = rec.DrainMode.String()
	if r.KeyModifiedTs != nil {
		rec.KeyModified = *r.KeyModifiedTs
	}
	return rec
}

func (h *serviceHandler) HandleServers() json.RawMessage {
	snapshot := h.state.Load()
	if snapshot == nil || snapshot.dashState == nil {
		return nil
	}
	all := snapshot.dashState.All()
	raw, _ := json.Marshal(all)
	return raw
}

func (h *serviceHandler) HandleRemoveServer(hostname string) error {
	snapshot := h.state.Load()
	if snapshot == nil || snapshot.dashState == nil {
		return fmt.Errorf("dashboard not enabled")
	}
	if !snapshot.dashState.Remove(hostname) {
		return fmt.Errorf("host not found: %s", hostname)
	}
	return nil
}

// HandleRegister runs dashboard.Register under the service's machine-account
// identity so the CLI can route around the dashboard's requireMachineAccount
// gate. See BUGS.md #1.
func (h *serviceHandler) HandleRegister(dashboardURL string) (json.RawMessage, error) {
	if dashboardURL == "" {
		return nil, fmt.Errorf("dashboard url required")
	}
	// HandleRegister is invoked from the named-pipe server goroutine, which has
	// no service-scoped ctx threaded through. Bound the call locally so a stuck
	// SSPI/HTTP exchange cannot wedge the pipe handler indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if h.registration == nil {
		return nil, fmt.Errorf("registration subsystem not running")
	}
	result, err := h.registration.Register(ctx, dashboardURL)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal register result: %w", err)
	}
	return raw, nil
}

func (h *serviceHandler) HandleBaselineReset() error {
	sub := h.evtSpikeSub.Load()
	if sub == nil {
		return fmt.Errorf("evtspike subsystem not running")
	}
	return sub.ResetBaseline()
}
