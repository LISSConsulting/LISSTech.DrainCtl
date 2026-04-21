//go:build windows

package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// serverStoreOpTimeout bounds every underlying SQLite call so a pathological
// lock or disk stall can't wedge an HTTP handler. Matches the 5 s envelope
// used for metrics Append in StartDashboard.
const serverStoreOpTimeout = 5 * time.Second

// ServerInfo describes a registered server and its last known state. JSON
// field names are the dashboard wire contract — do not change without a
// coordinated frontend update.
type ServerInfo struct {
	Hostname     string          `json:"hostname"`
	RegisteredAt time.Time       `json:"registered_at"`
	LastResult   *dc.CheckResult `json:"last_result,omitempty"`
	LastSeen     time.Time       `json:"last_seen,omitempty"`
}

// ServerState manages the set of registered servers, persisted to the
// SQLite `servers` table via telemetry.ServerStore. Replaces the pre-009
// servers.json file-based implementation.
type ServerState struct {
	store *telemetry.ServerStore

	// OnUpdate, if non-nil, is called after a server state update with the hostname.
	// Used by DashboardServer to broadcast SSE events.
	OnUpdate func(hostname string)
	// OnMetrics, if non-nil, is called after every state update with the full
	// CheckResult so that both the HTTP and local-report paths write metrics.
	OnMetrics func(result dc.CheckResult)
	// OnEvtSpikeIngest, if non-nil, pushes a confirmed spike into the
	// dashboard's per-host spike store and emits a recent_spike SSE event.
	// Wired by StartDashboard; the evtspike subsystem calls it from its
	// OnSpike callback.
	OnEvtSpikeIngest func(spike evtspike.SpikePayload) evtspike.RecentSpikeEntry
	// OnEvtSpikeStatus, if non-nil, emits a detector_status SSE event. The
	// dashboard's broker dedups same-state emissions, so callers may invoke
	// this on every observation without flooding subscribers. Wired by
	// StartDashboard.
	OnEvtSpikeStatus func(status evtspike.DetectorStatus)
	// RegisterEvtSpikeStatusFunc, if non-nil, installs the pull-based status
	// lookup that backs GET /api/evtspike/status. Wired by StartDashboard; the
	// evtspike subsystem calls it once at Start with its Status method.
	RegisterEvtSpikeStatusFunc func(f EvtSpikeStatusFunc)
	// GetRemoteEvtSpikeStatus, if non-nil, returns the most recently reported
	// DetectorStatus for a remote agent. Wired by StartDashboard to the
	// DashboardServer's remote-status cache so the pull function can answer
	// for non-local hosts. Zero value when the host has never reported.
	GetRemoteEvtSpikeStatus func(host string) evtspike.DetectorStatus
}

// NewServerState wraps a telemetry.ServerStore with the callback plumbing the
// dashboard needs. The store must be open for the lifetime of the returned
// ServerState.
func NewServerState(store *telemetry.ServerStore) *ServerState {
	return &ServerState{store: store}
}

// Register adds (or re-registers) a hostname. Idempotent — re-registering an
// existing host preserves its original RegisteredAt timestamp.
func (s *ServerState) Register(hostname string) {
	ctx, cancel := context.WithTimeout(context.Background(), serverStoreOpTimeout)
	defer cancel()
	if err := s.store.Register(ctx, hostname); err != nil {
		slog.Error("dashboard: register failed", "host", hostname, "error", err) //nolint:gosec // host is an internal identifier, not attacker-controlled log injection
	}
}

// Remove deletes a server by hostname. Returns true if found.
func (s *ServerState) Remove(hostname string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), serverStoreOpTimeout)
	defer cancel()
	found, err := s.store.Remove(ctx, hostname)
	if err != nil {
		slog.Error("dashboard: remove failed", "host", hostname, "error", err) //nolint:gosec // host is an internal identifier, not attacker-controlled log injection
		return false
	}
	return found
}

// IsRegistered returns true if the hostname is known. On storage error, logs
// and returns false — denying the calling handler is safer than falsely
// accepting an unregistered host.
func (s *ServerState) IsRegistered(hostname string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), serverStoreOpTimeout)
	defer cancel()
	ok, err := s.store.IsRegistered(ctx, hostname)
	if err != nil {
		slog.Error("dashboard: is_registered failed", "host", hostname, "error", err) //nolint:gosec // host is an internal identifier, not attacker-controlled log injection
		return false
	}
	return ok
}

// Update sets the last result and last-seen time for a registered host. No-op
// for unregistered hosts. After persistence, fires OnUpdate + OnMetrics in
// order (both may be nil).
func (s *ServerState) Update(hostname string, result *dc.CheckResult) {
	lastJSON := ""
	if result != nil {
		data, err := json.Marshal(result)
		if err != nil {
			slog.Error("dashboard: update marshal failed", "host", hostname, "error", err) //nolint:gosec // host is an internal identifier, not attacker-controlled log injection
			return
		}
		lastJSON = string(data)
	}

	ctx, cancel := context.WithTimeout(context.Background(), serverStoreOpTimeout)
	defer cancel()
	updated, err := s.store.Update(ctx, hostname, lastJSON)
	if err != nil {
		slog.Error("dashboard: update failed", "host", hostname, "error", err) //nolint:gosec // host is an internal identifier, not attacker-controlled log injection
		return
	}
	if !updated {
		return
	}

	if s.OnUpdate != nil {
		s.OnUpdate(hostname)
	}
	if s.OnMetrics != nil && result != nil {
		s.OnMetrics(*result)
	}
}

// Get returns a snapshot of the named server, or nil if not registered.
func (s *ServerState) Get(hostname string) *ServerInfo {
	ctx, cancel := context.WithTimeout(context.Background(), serverStoreOpTimeout)
	defer cancel()
	info, err := s.store.Get(ctx, hostname)
	if err != nil {
		slog.Error("dashboard: get failed", "host", hostname, "error", err) //nolint:gosec // host is an internal identifier, not attacker-controlled log injection
		return nil
	}
	if info == nil {
		return nil
	}
	out, err := toDashboardServerInfo(*info)
	if err != nil {
		slog.Error("dashboard: get unmarshal failed", "host", hostname, "error", err) //nolint:gosec // host is an internal identifier, not attacker-controlled log injection
		return nil
	}
	return &out
}

// All returns a snapshot of all servers sorted by hostname.
func (s *ServerState) All() []ServerInfo {
	ctx, cancel := context.WithTimeout(context.Background(), serverStoreOpTimeout)
	defer cancel()
	rows, err := s.store.All(ctx)
	if err != nil {
		slog.Error("dashboard: all failed", "error", err)
		return []ServerInfo{}
	}
	out := make([]ServerInfo, 0, len(rows))
	for _, row := range rows {
		info, err := toDashboardServerInfo(row)
		if err != nil {
			slog.Warn("dashboard: all unmarshal skipped row",
				"host", row.Hostname, "error", err)
			continue
		}
		out = append(out, info)
	}
	return out
}

// ReportLocal processes a check result for the local host without HTTP.
// Returns false if the host is not registered.
func (s *ServerState) ReportLocal(hostname string, result *dc.CheckResult) bool {
	if !s.IsRegistered(hostname) {
		return false
	}
	s.Update(hostname, result)
	return true
}

// GetSettings reads dashboard settings from config.json.
// This is the in-process equivalent of GET /api/v1/config.
func GetSettings() (*RemoteSettings, error) {
	cfg, err := dc.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	notifications := cfg.Notifications
	if notifications == nil {
		notifications = []dc.NotificationTarget{}
	}
	evtEnabled := cfg.EvtSpike.Enabled
	return &RemoteSettings{
		Notifications:           notifications,
		SessionWarningThreshold: cfg.SessionWarningThreshold,
		GracePeriod:             cfg.GracePeriod,
		PollInterval:            cfg.PollInterval,
		Performance:             &cfg.Performance,
		EvtSpikeEnabled:         &evtEnabled,
	}, nil
}

// toDashboardServerInfo lifts a telemetry.ServerInfo into the dashboard-shape
// ServerInfo, unmarshalling the stored CheckResult JSON blob when present.
func toDashboardServerInfo(row telemetry.ServerInfo) (ServerInfo, error) {
	out := ServerInfo{
		Hostname:     row.Hostname,
		RegisteredAt: row.RegisteredAt,
		LastSeen:     row.LastSeen,
	}
	if row.LastResultJSON == "" {
		return out, nil
	}
	var r dc.CheckResult
	if err := json.Unmarshal([]byte(row.LastResultJSON), &r); err != nil {
		return ServerInfo{}, fmt.Errorf("unmarshal last_result_json: %w", err)
	}
	out.LastResult = &r
	return out, nil
}
