//go:build windows

package dashboard

import (
	"context"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// metricsReader is the subset of *telemetry.MetricsStore the dashboard uses.
// Defining it on the consumer side lets tests substitute a fake store and
// keeps the dashboard from coupling to write paths it doesn't drive.
type metricsReader interface {
	Append(ctx context.Context, samples []telemetry.Sample) error
	BoundsForTier(ctx context.Context, host string, tier telemetry.Tier) (*time.Time, *time.Time, error)
	BoundsForTierFleet(ctx context.Context, hosts []string, tier telemetry.Tier) (*time.Time, *time.Time, error)
	QueryRange(ctx context.Context, host string, from, to time.Time, tier telemetry.Tier, counters []string) (*telemetry.Series, error)
	QueryRangeFleet(ctx context.Context, hosts []string, from, to time.Time, tier telemetry.Tier, counters []string, rawBucketMs int64) (*telemetry.Series, error)
}

// auditReader is the subset of *telemetry.AuditStore the dashboard uses.
type auditReader interface {
	QueryRange(ctx context.Context, filter telemetry.QueryFilter) ([]telemetry.AuditRecord, string, error)
}

// maintenanceReader is the subset of *telemetry.MaintenanceStore the dashboard
// uses.
type maintenanceReader interface {
	ListJobs(ctx context.Context) ([]telemetry.Job, error)
}

// serverReader is the subset of *telemetry.ServerStore that ServerState wraps.
// Import is included because MigrateLegacyServersJSON drives the same store.
type serverReader interface {
	Register(ctx context.Context, hostname string) error
	Remove(ctx context.Context, hostname string) (bool, error)
	IsRegistered(ctx context.Context, hostname string) (bool, error)
	Update(ctx context.Context, hostname, lastResultJSON string) (bool, error)
	Get(ctx context.Context, hostname string) (*telemetry.ServerInfo, error)
	All(ctx context.Context) ([]telemetry.ServerInfo, error)
	Import(ctx context.Context, infos []telemetry.ServerInfo) (int, error)
}

// eventSpikeReader is the subset of *telemetry.EventSpikeStore the dashboard
// uses. Insert is part of this set because the dashboard owns the ingestion
// path that funnels SSE events.
type eventSpikeReader interface {
	Insert(ctx context.Context, spike telemetry.EventSpike) (telemetry.EventSpike, bool, error)
	Recent(ctx context.Context, host string, limit int) ([]telemetry.EventSpike, error)
	Range(ctx context.Context, host string, from, to time.Time, maxLimit int) ([]telemetry.EventSpike, error)
}
