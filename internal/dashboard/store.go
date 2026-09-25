//go:build windows

package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// exclusionReader is the narrow contract ServerState uses to gate
// re-registration against a tombstone. Production code supplies
// *telemetry.RemovalStore; tests may pass an in-memory stub via
// SetExclusionReader.
type exclusionReader interface {
	IsExcluded(ctx context.Context, hostname string) (bool, error)
}

// removalWriter is the narrow contract ServerState uses for permanent-remove
// and restore. Production code supplies *telemetry.RemovalStore; the same
// stub used as exclusionReader MAY implement both. Embedding exclusionReader
// here means a single *telemetry.RemovalStore instance can be passed to
// SetExclusionReader AND SetRemovalWriter without an extra adapter.
type removalWriter interface {
	exclusionReader
	Exclude(ctx context.Context, hostname, by, reason string) error
	PermanentRemove(ctx context.Context, hostname, by, reason string) error
	RegisterIfNotExcluded(ctx context.Context, hostname string) (accepted, excluded bool, err error)
	Restore(ctx context.Context, hostname string) (bool, error)
	AllExcluded(ctx context.Context) ([]telemetry.RemovalEntry, error)
}

// serverStoreOpTimeout bounds every underlying SQLite call so a pathological
// lock or disk stall can't wedge an HTTP handler. Matches the 5 s envelope
// used for metrics Append in Subsystem.Start.
const serverStoreOpTimeout = 5 * time.Second

type registrationResult uint8

const (
	registrationFailed registrationResult = iota
	registrationAccepted
	registrationExcluded
)

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
// SQLite `servers` table via a serverReader (typically *telemetry.ServerStore).
// Replaces the pre-009 servers.json file-based implementation.
type ServerState struct {
	store serverReader

	// exclusions is the durable tombstone store used to gate re-registration
	// against permanently-removed servers. May be nil for callers that do not
	// want this gate (e.g. tests for the legacy behavior). SetExclusionReader
	// is the safe way to attach a real *telemetry.RemovalStore in production.
	exclusions exclusionReader
	// removals is the durable tombstone store used for permanent-remove and
	// restore mutations. May be nil alongside exclusions; when set, both
	// references should point at the same *telemetry.RemovalStore instance.
	removals removalWriter

	// cache holds the most recent immutable *ServerInfo per host. Populated by
	// Update after a successful DB write; consulted by GetCached on the SSE
	// hot path so broadcastServerUpdate avoids a DB read + JSON unmarshal of
	// data we just persisted. Invalidated by Remove. Keys are hostnames,
	// values are *ServerInfo.
	cache sync.Map
	// registeredAt caches each host's immutable registered_at timestamp so
	// the heartbeat path can build cached *ServerInfo values without a
	// per-Update store.Get. Lazy-loaded on first Update per host (one Get
	// per host process-lifetime); subsequent Updates for that host skip the
	// DB read entirely. Invalidated on Remove so re-registration's fresh
	// timestamp is picked up on next Update. Keys are hostnames, values
	// are time.Time.
	registeredAt sync.Map
	// storeGetCalls counts every call this ServerState makes into the
	// underlying telemetry.ServerStore.Get — the public Get wrapper AND
	// the lazy lookupRegisteredAt path. Test seam used to prove the SSE
	// broadcast and the steady-state heartbeat both skip the DB. Production
	// overhead is one atomic add per underlying Get call.
	storeGetCalls atomic.Int64

	// OnUpdate, if non-nil, is called after a server state update with the hostname.
	// Used by DashboardServer to broadcast SSE events.
	OnUpdate func(hostname string)
	// OnMetrics, if non-nil, is called after every state update with the full
	// CheckResult so that both the HTTP and local-report paths write metrics.
	OnMetrics func(result dc.CheckResult)
	// OnEvtSpikeIngest, if non-nil, pushes a confirmed spike into the
	// dashboard's per-host spike store and emits a recent_spike SSE event.
	// Wired by Subsystem.Start; the evtspike subsystem calls it from its
	// OnSpike callback.
	OnEvtSpikeIngest func(spike evtspike.SpikePayload) evtspike.RecentSpikeEntry
	// OnEvtSpikeStatus, if non-nil, emits a detector_status SSE event. The
	// dashboard's broker dedups same-state emissions, so callers may invoke
	// this on every observation without flooding subscribers. Wired by
	// Subsystem.Start.
	OnEvtSpikeStatus func(status evtspike.DetectorStatus)
	// RegisterEvtSpikeStatusFunc, if non-nil, installs the pull-based status
	// lookup that backs GET /api/evtspike/status. Wired by Subsystem.Start; the
	// evtspike subsystem calls it once at Start with its Status method.
	RegisterEvtSpikeStatusFunc func(f EvtSpikeStatusFunc)
	// GetRemoteEvtSpikeStatus, if non-nil, returns the most recently reported
	// DetectorStatus for a remote agent. Wired by Subsystem.Start to the
	// DashboardServer's remote-status cache so the pull function can answer
	// for non-local hosts. Zero value when the host has never reported.
	GetRemoteEvtSpikeStatus func(host string) evtspike.DetectorStatus

	// ConsumeForceUpdate returns the next queued force-update command for
	// the named host. Wired by Subsystem.Start to a closure over
	// ds.forceUpdates.Consume so the local-report path (svcRunCheck
	// runs in the same process as the dashboard) can pull the pending
	// command without going through HTTP. Returns nil when the registry
	// has nothing queued; caller's ForceUpdate code path then no-ops.
	ConsumeForceUpdate func(host string) *ForceUpdatePendingCommand

	// OnLocalForceUpdateCompletion, if non-nil, is called by ReportLocal
	// with the parsed force-update completion payload when the local
	// agent posts a `force_update` block alongside its heartbeat. Wired
	// by Subsystem.Start so the same dashboard-side SSE broadcast runs
	// for both the remote-report path (handleReport) and the local-report
	// path (ReportLocal). Without this hook, a local agent that finishes
	// a forced update would never emit a force_update SSE event because
	// the HTTP report path is bypassed entirely.
	OnLocalForceUpdateCompletion func(payload ForceUpdateCompletionPayload)
}

// GetConsumeForceUpdate returns the wired ConsumeForceUpdate closure.
// Exported accessor used by the local-report path's svc loop to pull
// the next pending force-update command from the dashboard's
// in-process registry. The accessor is exported because the
// dashboard-side registry types are package-private and the svc loop
// lives in a different package; the closure itself returns the
// wire-side ForceUpdatePendingCommand so consumers don't need access
// to the registry's internal struct layout.
func (s *ServerState) GetConsumeForceUpdate() func(string) *ForceUpdatePendingCommand {
	return s.ConsumeForceUpdate
}

// NewServerState wraps a serverReader (typically *telemetry.ServerStore) with
// the callback plumbing the dashboard needs. The store must be open for the
// lifetime of the returned ServerState.
func NewServerState(store serverReader) *ServerState {
	return &ServerState{store: store}
}

// SetExclusionReader wires the durable tombstone gate. Once set, Register
// rejects re-registration of any host that has an entry in the exclusion
// store; IsExcluded can be checked directly via ServerState.IsExcluded().
// Typically called once at Subsystem.Start with a *telemetry.RemovalStore.
func (s *ServerState) SetExclusionReader(r exclusionReader) {
	s.exclusions = r
}

// SetRemovalWriter wires the durable tombstone store used by the
// permanent-remove / restore HTTP handlers. Typically called once at
// Subsystem.Start with the same *telemetry.RemovalStore passed to
// SetExclusionReader.
func (s *ServerState) SetRemovalWriter(w removalWriter) {
	s.removals = w
}

// IsExcluded returns true if hostname has a tombstone. Mirrors
// exclusionReader.IsExcluded for handler-side checks; returns false when no
// exclusion store has been wired (preserves legacy behavior in tests).
func (s *ServerState) IsExcluded(hostname string) bool {
	hostname = telemetry.CanonicalHostname(hostname)
	if s.exclusions == nil {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), serverStoreOpTimeout)
	defer cancel()
	excluded, err := s.exclusions.IsExcluded(ctx, hostname)
	if err != nil {
		slog.Error("dashboard: is_excluded failed", "host", hostname, "error", err) //nolint:gosec // host is an internal identifier, not attacker-controlled log injection
		return false
	}
	return excluded
}

// Register adds (or re-registers) a hostname. Its result tells the HTTP
// handler whether the durable tombstone gate accepted, rejected, or could not
// persist the registration.
func (s *ServerState) Register(hostname string) registrationResult {
	hostname = telemetry.CanonicalHostname(hostname)
	ctx, cancel := context.WithTimeout(context.Background(), serverStoreOpTimeout)
	defer cancel()
	if s.removals != nil {
		accepted, excluded, err := s.removals.RegisterIfNotExcluded(ctx, hostname)
		if err != nil {
			slog.Error("dashboard: register failed", "host", hostname, "error", err) //nolint:gosec
			return registrationFailed
		}
		if excluded {
			return registrationExcluded
		}
		if accepted {
			return registrationAccepted
		}
		return registrationFailed
	}
	if s.exclusions != nil {
		excluded, err := s.exclusions.IsExcluded(ctx, hostname)
		if err != nil {
			slog.Error("dashboard: register pre-check failed", "host", hostname, "error", err) //nolint:gosec
			return registrationFailed
		}
		if excluded {
			return registrationExcluded
		}
	}
	if err := s.store.Register(ctx, hostname); err != nil {
		slog.Error("dashboard: register failed", "host", hostname, "error", err) //nolint:gosec
		return registrationFailed
	}
	return registrationAccepted
}

// Remove deletes a server by hostname. Returns true if found. Invalidates the
// per-host cache so a subsequent re-register starts from a clean slate and a
// stale cached *ServerInfo can't outlive the row it described.
func (s *ServerState) Remove(hostname string) bool {
	hostname = telemetry.CanonicalHostname(hostname)
	ctx, cancel := context.WithTimeout(context.Background(), serverStoreOpTimeout)
	defer cancel()
	found, err := s.store.Remove(ctx, hostname)
	if err != nil {
		slog.Error("dashboard: remove failed", "host", hostname, "error", err) //nolint:gosec // host is an internal identifier, not attacker-controlled log injection
		return false
	}
	s.cache.Delete(hostname)
	s.registeredAt.Delete(hostname)
	return found
}

// IsRegistered returns true if the hostname is known. On storage error, logs
// and returns false — denying the calling handler is safer than falsely
// accepting an unregistered host.
func (s *ServerState) IsRegistered(hostname string) bool {
	hostname = telemetry.CanonicalHostname(hostname)
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
//
// Caching contract: on a successful DB write Update populates an internal
// cache with an immutable *ServerInfo for hostname. The cached value is built
// from a top-level value-copy of *result wrapped in a fresh ServerInfo, which
// breaks aliasing on the CheckResult struct itself. Inner pointer fields of
// CheckResult (notably *PerfSnapshot, *Sessions, *EvtSpikeStatus, *time.Time
// fields) are NOT deep-cloned — they are shared with the caller. The contract
// is therefore: callers MUST NOT mutate *result, *result.Performance, or any
// other inner pointer they handed to Update after the call returns. Both
// in-tree callers (the HTTP /report handler in server.go and ReportLocal via
// internal/svc/check.go) build a fresh CheckResult per heartbeat and do not
// retain or mutate it after Update, so the contract holds.
//
// On store.Update error the cache is left untouched: a failed write must not
// poison the cache with an unpersisted snapshot.
func (s *ServerState) Update(hostname string, result *dc.CheckResult) {
	hostname = telemetry.CanonicalHostname(hostname)
	// Exclusion gate: refuse to write new metric data for tombstoned hosts.
	// The /report HTTP handler is expected to call IsRegistered first, but
	// ReportLocal races with PermanentRemove can drop a stale Update through
	// the live IsRegistered check — this second gate catches the race so
	// excluded hosts cannot ingest reports until restored.
	if s.IsExcluded(hostname) {
		slog.Info("dashboard: update refused — host is permanently removed", "host", hostname) //nolint:gosec
		return
	}
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
		// DB write failed — do NOT populate the cache. The DB is the source
		// of truth; caching unpersisted data would let GetCached return a
		// view that diverges from what Get/All/the SQLite row would yield.
		slog.Error("dashboard: update failed", "host", hostname, "error", err) //nolint:gosec // host is an internal identifier, not attacker-controlled log injection
		return
	}
	if !updated {
		return
	}

	// Populate the SSE hot-path cache with an immutable *ServerInfo. The
	// registered_at timestamp is immutable post-Register, so we cache it
	// per-host on first Update (one DB read per host process-lifetime) and
	// reuse for every subsequent heartbeat — that's the actual hot-path
	// DB-read elimination this commit was meant to deliver. LastSeen is
	// generated locally from time.Now() to match the timestamp ServerStore
	// just wrote (modulo sub-millisecond skew, inconsequential for stale-
	// status display).
	registeredAt, ok := s.lookupRegisteredAt(ctx, hostname)
	if !ok {
		// Couldn't determine registered_at (DB error or row vanished
		// between Update and Get). Skip caching this round; GetCached
		// falls back to Get on the next broadcast.
		if s.OnUpdate != nil {
			s.OnUpdate(hostname)
		}
		if s.OnMetrics != nil && result != nil {
			s.OnMetrics(*result)
		}
		return
	}
	info := &ServerInfo{
		Hostname:     hostname,
		RegisteredAt: registeredAt,
		LastSeen:     time.Now().UTC(),
	}
	if result != nil {
		// Top-level deep-copy: take a value copy of *result and wrap
		// the address of that copy. This breaks aliasing on the
		// CheckResult struct so a later call that replaces fields on
		// the caller's *CheckResult cannot mutate the cached entry.
		// See the godoc above for the full contract on inner pointers.
		cached := *result
		info.LastResult = &cached
	}
	s.cache.Store(hostname, info)

	if s.OnUpdate != nil {
		s.OnUpdate(hostname)
	}
	if s.OnMetrics != nil && result != nil {
		s.OnMetrics(*result)
	}
}

// lookupRegisteredAt returns the cached registered_at for hostname or, on a
// cache miss, performs ONE store.Get to read it and populate the cache. After
// the first Update for a given host, every subsequent Update for that host
// hits the cache and skips the DB. Returns (zero, false) on any DB error or
// when the row is missing.
func (s *ServerState) lookupRegisteredAt(ctx context.Context, hostname string) (time.Time, bool) {
	if v, ok := s.registeredAt.Load(hostname); ok {
		return v.(time.Time), true
	}
	s.storeGetCalls.Add(1)
	row, err := s.store.Get(ctx, hostname)
	if err != nil {
		slog.Warn("dashboard: lookupRegisteredAt failed", "host", hostname, "error", err) //nolint:gosec // host is an internal identifier, not attacker-controlled log injection
		return time.Time{}, false
	}
	if row == nil {
		return time.Time{}, false
	}
	s.registeredAt.Store(hostname, row.RegisteredAt)
	return row.RegisteredAt, true
}

// GetCached returns the cached *ServerInfo for hostname populated by the most
// recent successful Update, or nil on miss. Callers that need a guaranteed
// fallback (e.g. first broadcast after restart, before any Update has run)
// must invoke Get themselves on nil. By design GetCached does NOT touch the
// DB — that's the point of the cache and the property
// TestBroadcastServerUpdateUsesCache asserts.
func (s *ServerState) GetCached(hostname string) *ServerInfo {
	hostname = telemetry.CanonicalHostname(hostname)
	v, ok := s.cache.Load(hostname)
	if !ok {
		return nil
	}
	info, ok := v.(*ServerInfo)
	if !ok {
		return nil
	}
	return info
}

// Get returns a snapshot of the named server, or nil if not registered.
func (s *ServerState) Get(hostname string) *ServerInfo {
	hostname = telemetry.CanonicalHostname(hostname)
	s.storeGetCalls.Add(1)
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

// PermanentRemove atomically removes the live row and writes its tombstone.
// The removal store owns both tables on the same SQLite connection, so a
// failing tombstone insert rolls back the deletion.
func (s *ServerState) PermanentRemove(hostname, by, reason string) (time.Time, error) {
	hostname = telemetry.CanonicalHostname(hostname)
	if s.removals == nil {
		return time.Time{}, fmt.Errorf("dashboard: no removal store wired")
	}
	ctx, cancel := context.WithTimeout(context.Background(), serverStoreOpTimeout)
	defer cancel()
	if err := s.removals.PermanentRemove(ctx, hostname, by, reason); err != nil {
		return time.Time{}, fmt.Errorf("dashboard: permanent remove: %w", err)
	}
	s.cache.Delete(hostname)
	s.registeredAt.Delete(hostname)
	return time.Now().UTC(), nil
}

// RestoreRemoved deletes the tombstone for hostname. The live `servers` row
// is NOT auto-recreated — the agent's next /api/v1/register will create a
// fresh row with the current timestamp. Returns true when a tombstone row
// was removed.
func (s *ServerState) RestoreRemoved(hostname string) (bool, time.Time, error) {
	hostname = telemetry.CanonicalHostname(hostname)
	if s.removals == nil {
		return false, time.Time{}, fmt.Errorf("dashboard: no removal store wired")
	}
	ctx, cancel := context.WithTimeout(context.Background(), serverStoreOpTimeout)
	defer cancel()
	found, err := s.removals.Restore(ctx, hostname)
	if err != nil {
		return false, time.Time{}, fmt.Errorf("dashboard: restore tombstone: %w", err)
	}
	return found, time.Now().UTC(), nil
}

// AllRemoved returns every tombstoned host, newest first. Returns an empty
// slice when no removals store is wired. The slice is freshly allocated so
// the caller may retain it.
func (s *ServerState) AllRemoved() ([]telemetry.RemovalEntry, error) {
	if s.removals == nil {
		return []telemetry.RemovalEntry{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), serverStoreOpTimeout)
	defer cancel()
	return s.removals.AllExcluded(ctx)
}

// ReportLocal processes a check result for the local host without HTTP.
// Returns false if the host is not registered OR has a tombstone. Permanent
// removal removes the host from the live roster first, so the more common
// path here is "host is not registered" — but when an excluded report races
// with PermanentRemove, the second gate below refuses it.
//
// forceUpdate may be nil; when non-nil it represents the local agent's
// terminal updater decision and is forwarded to the dashboard's SSE pipeline.
func (s *ServerState) ReportLocal(hostname string, result *dc.CheckResult, forceUpdate *ForceUpdateCompletionPayload) bool {
	hostname = telemetry.CanonicalHostname(hostname)
	if !s.IsRegistered(hostname) {
		return false
	}
	if s.IsExcluded(hostname) {
		slog.Info("dashboard: report refused — host is permanently removed", "host", hostname) //nolint:gosec
		return false
	}
	s.Update(hostname, result)
	if forceUpdate != nil && s.OnLocalForceUpdateCompletion != nil {
		s.OnLocalForceUpdateCompletion(*forceUpdate)
	}
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
	exclusions := slices.Clone(cfg.NotificationExclusions)
	if exclusions == nil {
		exclusions = []string{}
	}
	view := buildEvtSpikeView(cfg.EvtSpike)
	remoteEvtSpike := remoteEvtSpikeFromView(view)
	return &RemoteSettings{
		Notifications:           notifications,
		NotificationExclusions:  exclusions,
		SessionWarningThreshold: cfg.SessionWarningThreshold,
		GracePeriod:             cfg.GracePeriod,
		PollInterval:            cfg.PollInterval,
		Performance:             &cfg.Performance,
		EvtSpike:                &remoteEvtSpike,
		Update:                  &cfg.Update,
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
