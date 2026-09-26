//go:build windows

package drainctl

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/logging"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/winacl"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	ServiceName        = "DrainCtl"
	ServiceDisplayName = "Remote Desktop Drain Mode Monitor"
	ServiceDescription = "Monitors Remote Desktop Session Host drain mode (TSServerDrainMode) and maintains an audit trail of state changes."
	ParametersKeyPath  = `SYSTEM\CurrentControlSet\Services\DrainCtl\Parameters`

	DefaultGracePeriod    = 60 // minutes
	DefaultRetentionDays  = 90
	MaxRetentionDays      = 365
	MinRetentionDays      = 1
	MaxAuditRetentionDays = 3650

	DefaultMetricsDays               = 30
	DefaultAuditDays                 = 365
	DefaultAggregatorIntervalSeconds = 300
	DefaultRetentionIntervalMinutes  = 60
	DefaultPollInterval              = 300   // seconds
	MaxPollInterval                  = 86400 // seconds (1 day)
	DefaultDashboardPort             = 49470
	DefaultDashboardGroup            = "Domain Admins"
	DefaultDashboardFetchInterval    = 300 // seconds (5 minutes)

	DefaultSessionWarningThreshold = 80 // percent

	DefaultSampleInterval          = 60  // seconds
	DefaultLoadAlertDelaySec       = 120 // seconds — two samples before CPU/memory alert
	DefaultInputDelayAlertDelaySec = 180 // seconds — three samples before input delay alert

	DefaultMemoryLimitMB          = 32   // MiB — soft GOMEMLIMIT for lightweight agents
	DefaultDashboardMemoryLimitMB = 512  // MiB — higher limit when running as dashboard server
	MinMemoryLimitMB              = 32   // MiB — floor
	MaxMemoryLimitMB              = 4096 // MiB — ceiling (4 GiB)

	// MaxRepeatMinutes caps notification repeat intervals. Values above this
	// would overflow time.Duration when multiplied by time.Minute.
	MaxRepeatMinutes = 10080 // 1 week

	// EvtSpike defaults and clamp boundaries (data-model.md §1).
	DefaultEvtSpikeMinCount                 = 10
	MinEvtSpikeMinCount                     = 1
	MaxEvtSpikeMinCount                     = 10000
	DefaultEvtSpikeThreshold                = 1e-4
	MinEvtSpikeThreshold                    = 1e-9
	MaxEvtSpikeThreshold                    = 0.1
	DefaultEvtSpikeCooldownMinutes          = 60
	MinEvtSpikeCooldownMinutes              = 1
	MaxEvtSpikeCooldownMinutes              = 1440
	DefaultEvtSpikeSlotMaturityObservations = 630
	MinEvtSpikeSlotMaturityObservations     = 1
	MaxEvtSpikeSlotMaturityObservations     = 10000
	DefaultEvtSpikePersistIntervalSeconds   = 900
	MinEvtSpikePersistIntervalSeconds       = 60
	MaxEvtSpikePersistIntervalSeconds       = 86400
	DefaultEvtSpikeHalfLifeBuckets          = 630
	MinEvtSpikeHalfLifeBuckets              = 60
	MaxEvtSpikeHalfLifeBuckets              = 10000
	DefaultEvtSpikePriorStrength            = 60.0
	MinEvtSpikePriorStrength                = 1.0
	MaxEvtSpikePriorStrength                = 10000.0
	DefaultEvtSpikeMeanPerBucketPrior       = 0.1
	MinEvtSpikeMeanPerBucketPrior           = 0.0
	MaxEvtSpikeMeanPerBucketPrior           = 1000.0

	configMutexName = `Global\DrainCtlConfig`

	// dpapiPrefix marks a secret as DPAPI-encrypted in config.json.
	dpapiPrefix = "dpapi:"
)

var rfc1123HostnameRE = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)*$`)

// NormalizeRDConnectionBroker trims broker and verifies that it is empty or an
// RFC 1123 hostname/FQDN no longer than 253 characters.
func NormalizeRDConnectionBroker(broker string) (string, error) {
	broker = strings.TrimSpace(broker)
	if broker == "" {
		return "", nil
	}
	if len(broker) > 253 || !rfc1123HostnameRE.MatchString(broker) {
		return "", errors.New("must be empty or an RFC 1123 hostname (max 253 characters)")
	}
	return broker, nil
}

// ── Trigger type ──────────────────────────────────────────────────────────

// Trigger represents a granular notification event type.
type Trigger string

const (
	TriggerDrainOn            Trigger = "drain_on"
	TriggerDrainOff           Trigger = "drain_off"
	TriggerGraceEntered       Trigger = "grace_entered"
	TriggerAlert              Trigger = "alert"
	TriggerHealthy            Trigger = "healthy"
	TriggerSessionWarning     Trigger = "session_warning"
	TriggerCPUWarning         Trigger = "cpu_warning"
	TriggerCPUCritical        Trigger = "cpu_critical"
	TriggerInputDelayWarning  Trigger = "input_delay_warning"
	TriggerInputDelayCritical Trigger = "input_delay_critical"
	TriggerMemoryWarning      Trigger = "memory_warning"
	TriggerMemoryCritical     Trigger = "memory_critical"
	TriggerEventSpike         Trigger = "event_spike"
)

// DefaultTriggers is used when a target specifies no triggers.
var DefaultTriggers = []Trigger{TriggerDrainOn, TriggerDrainOff, TriggerAlert, TriggerHealthy}

// ValidTriggers is the set of all recognized trigger names.
var ValidTriggers = map[Trigger]bool{
	TriggerDrainOn: true, TriggerDrainOff: true,
	TriggerGraceEntered: true, TriggerAlert: true,
	TriggerHealthy: true, TriggerSessionWarning: true,
	TriggerCPUWarning: true, TriggerCPUCritical: true,
	TriggerInputDelayWarning: true, TriggerInputDelayCritical: true,
	TriggerMemoryWarning: true, TriggerMemoryCritical: true,
	TriggerEventSpike: true,
}

// ── NotificationTarget ───────────────────────────────────────────────────

// NotificationServerExclusion suppresses selected triggers for one server on
// one notification target. Server matching is case-insensitive and accepts a
// short hostname for a reported FQDN.
type NotificationServerExclusion struct {
	Server   string    `json:"server"`
	Triggers []Trigger `json:"triggers"`
}

// NotificationTarget describes a single notification endpoint.
type NotificationTarget struct {
	Type             string                        `json:"type"` // "webhook", "ntfy", or "email"
	URL              string                        `json:"url"`
	Triggers         []Trigger                     `json:"triggers"`                 // empty = DefaultTriggers
	RepeatMinutes    int                           `json:"repeat_minutes,omitempty"` // 0 = once only
	Secret           string                        `json:"secret,omitempty"`         // HMAC-SHA256 signing secret for webhooks; SMTP password for email
	To               []string                      `json:"to,omitempty"`             // email recipients
	From             string                        `json:"from,omitempty"`           // email sender
	Enabled          *bool                         `json:"enabled,omitempty"`        // nil or true = enabled (default); false = disabled
	Severity         string                        `json:"severity,omitempty"`       // "warning" or "alert" — event_spike target wiring (FR-011a); empty defaults to "warning"
	ServerExclusions []NotificationServerExclusion `json:"server_exclusions,omitempty"`
}

// HasTrigger returns true if the target subscribes to the given trigger.
func (t NotificationTarget) HasTrigger(trigger Trigger) bool {
	triggers := t.Triggers
	if len(triggers) == 0 {
		triggers = DefaultTriggers
	}
	for _, tr := range triggers {
		if tr == trigger {
			return true
		}
	}
	return false
}

// ExcludesServer reports whether this target suppresses trigger for server.
func (t NotificationTarget) ExcludesServer(server string, trigger Trigger) bool {
	server = strings.TrimSuffix(strings.TrimSpace(server), ".")
	serverShort, _, _ := strings.Cut(server, ".")
	for _, exclusion := range t.ServerExclusions {
		excluded := strings.TrimSuffix(strings.TrimSpace(exclusion.Server), ".")
		excludedShort, _, _ := strings.Cut(excluded, ".")
		sameServer := strings.EqualFold(excluded, server) ||
			((!strings.Contains(excluded, ".") || !strings.Contains(server, ".")) &&
				strings.EqualFold(excludedShort, serverShort))
		if !sameServer {
			continue
		}
		for _, excludedTrigger := range exclusion.Triggers {
			if excludedTrigger == trigger {
				return true
			}
		}
	}
	return false
}

// CanonicalNotificationHost is the durable representation of a host exclusion.
// Hosts are recorded exactly as reported (apart from whitespace, a terminal DNS
// dot, and case) so an exclusion cannot accidentally affect another host that
// merely shares its short name in a different domain.
func CanonicalNotificationHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}

// IsNotificationExcluded reports whether host is globally excluded from every
// notification target and trigger.
func IsNotificationExcluded(exclusions []string, host string) bool {
	host = CanonicalNotificationHost(host)
	if host == "" {
		return false
	}
	for _, exclusion := range exclusions {
		if CanonicalNotificationHost(exclusion) == host {
			return true
		}
	}
	return false
}

// ── Config (JSON file) ──────────────────────────────────────────────────

// PerformanceConfig holds performance monitoring settings.
type PerformanceConfig struct {
	Enabled                 bool   `json:"enabled"`                          // default: false
	ForceDisabled           bool   `json:"force_disabled"`                   // when true, dashboard cannot enable perf on this server
	CPUWarnPct              int    `json:"cpu_warn_pct"`                     // default: 70, -1=disabled
	CPUCritPct              int    `json:"cpu_crit_pct"`                     // default: 85, -1=disabled
	MemWarnPct              int    `json:"mem_warn_pct"`                     // default: 20 (% free), -1=disabled
	MemCritPct              int    `json:"mem_crit_pct"`                     // default: 10 (% free), -1=disabled
	InputDelayWarnMS        int    `json:"input_delay_warn_ms"`              // default: 50, -1=disabled
	InputDelayCritMS        int    `json:"input_delay_crit_ms"`              // default: 100, -1=disabled
	InputDelayPercentile    string `json:"input_delay_percentile,omitempty"` // "p50" or "p95" (default: "p95")
	LoadAlertDelaySec       int    `json:"load_alert_delay_sec"`             // seconds before CPU/memory alert fires (default: 120)
	InputDelayAlertDelaySec int    `json:"input_delay_alert_delay_sec"`      // seconds before input delay alert fires (default: 180)
	CollectRemoteFX         bool   `json:"collect_remotefx"`                 // default: false
	CollectPerSession       bool   `json:"collect_per_session"`              // default: true
	SampleIntervalSec       int    `json:"sample_interval_sec"`              // default: 60, range 10–300
}

// RetentionConfig holds per-tier retention windows for the telemetry store.
type RetentionConfig struct {
	MetricsDays int `json:"metrics_days"` // hourly-tier retention, 1–365 days (default 30)
	AuditDays   int `json:"audit_days"`   // audit-trail retention, 1–3650 days (default 365)
}

// TelemetryConfig holds background-worker cadence settings.
type TelemetryConfig struct {
	AggregatorIntervalSeconds int `json:"aggregator_interval_seconds"` // aggregator tick, default 300
	RetentionIntervalMinutes  int `json:"retention_interval_minutes"`  // retention sweep cadence, default 60
}

// EvtSpikeConfig holds event-log anomaly detection settings. Zero value is
// fully disabled; numeric fields are promoted to defaults by ClampEvtSpike.
type EvtSpikeConfig struct {
	Enabled                  bool           `json:"enabled"`
	MinCount                 int            `json:"min_count"`
	Threshold                float64        `json:"threshold"`
	CooldownMinutes          int            `json:"cooldown_minutes"`
	ChannelCooldownMinutes   map[string]int `json:"channel_cooldown_minutes"`
	SlotMaturityObservations int            `json:"slot_maturity_observations"`
	PersistIntervalSeconds   int            `json:"persist_interval_seconds"`
	HalfLifeBuckets          int            `json:"half_life_buckets"`
	PriorStrength            float64        `json:"prior_strength"`
	MeanPerBucketPrior       float64        `json:"mean_per_bucket_prior"`
	BaselinePath             string         `json:"baseline_path"`
	DisabledChannels         []string       `json:"disabled_channels"`
	AddedChannels            []string       `json:"added_channels"`
	SecurityChannelEnabled   bool           `json:"security_channel_enabled"`
}

// Config is the top-level config file structure (config.json).
type Config struct {
	GracePeriod   int    `json:"grace_period"` // minutes
	RetentionDays int    `json:"retention_days"`
	PollInterval  int    `json:"poll_interval"` // seconds
	AuditPath     string `json:"audit_path"`
	MemoryLimitMB int    `json:"memory_limit_mb"` // Go runtime soft memory limit (MiB); 0 → default

	LogFileLevel  string `json:"log_file_level,omitempty"`  // min level for file sink: debug|info|warn|error (default: info)
	LogEventLevel string `json:"log_event_level,omitempty"` // min level for event log sink: debug|info|warn|error (default: info)

	Notifications          []NotificationTarget `json:"notifications"`
	NotificationExclusions []string             `json:"notification_exclusions"`

	DashboardOnly bool          `json:"dashboard_only"` // true = run dashboard but skip local drain monitoring
	Dashboard     DashboardJSON `json:"dashboard"`

	SessionWarningThreshold int `json:"session_warning_threshold"` // 0=disabled, 1-100

	Performance PerformanceConfig `json:"performance"`
	Retention   RetentionConfig   `json:"retention"`
	Telemetry   TelemetryConfig   `json:"telemetry"`

	EvtSpike EvtSpikeConfig `json:"evtspike"`

	Update UpdateConfig `json:"update"`
}

// UpdateConfig controls the agent's self-poll auto-update behavior. The
// updater subsystem (internal/updater) reads this config at Start and
// re-reads Enabled and Channel on every poll tick so an operator can
// disable or change channel mid-run without restart.
//
// Defaults: Enabled=false (opt-in), Channel="stable", PollInterval=24h.
// Validate replaces empty Channel with "stable" and zero PollInterval
// with the default; unknown Channel values are clamped to "stable" with
// a slog.Warn. A fresh install does not contact GitHub until the operator
// explicitly flips Enabled=true.
//
// See specs/010-auto-update/spec.md for the full feature contract.
type UpdateConfig struct {
	Enabled      bool     `json:"enabled"`
	Channel      string   `json:"channel"`
	PollInterval Duration `json:"poll_interval"`
}

// Channel string constants — exported so callers and tests can reference
// the recognized values without string-literal duplication.
const (
	ChannelStable     = "stable"
	ChannelPrerelease = "prerelease"
)

// Update-config defaults and bounds.
const (
	DefaultUpdatePollInterval = 24 * time.Hour
	MinUpdatePollInterval     = 1 * time.Hour
)

// DashboardJSON holds dashboard settings in config.json.
type DashboardJSON struct {
	Enabled            bool   `json:"enabled"`
	Port               int    `json:"port"`
	Group              string `json:"group"`
	URL                string `json:"url,omitempty"`
	RDConnectionBroker string `json:"rd_connection_broker,omitempty"` // empty = local host
	TLSCert            string `json:"tls_cert,omitempty"`             // path to PEM certificate file
	TLSKey             string `json:"tls_key,omitempty"`              // path to PEM private key file
	TLSFingerprint     string `json:"tls_fingerprint,omitempty"`      // SHA-256 cert fingerprint for pinning (agent-side)
	AutoPin            *bool  `json:"auto_pin,omitempty"`             // auto-pin dashboard cert on register (default false)
	FetchInterval      int    `json:"fetch_interval,omitempty"`       // seconds between config fetches from dashboard (default 300)
}

// ── Runtime config structs (converted from Config) ──────────────────────

// ServiceConfig holds runtime service parameters with parsed durations.
type ServiceConfig struct {
	GracePeriod             time.Duration
	RetentionDays           int
	PollInterval            time.Duration
	AuditPath               string
	SessionWarningThreshold int
	Performance             PerformanceConfig
	DashboardOnly           bool // run dashboard but skip local drain monitoring
}

// DashboardConfig holds runtime dashboard parameters.
type DashboardConfig struct {
	Enabled            bool
	Port               int
	Group              string
	URL                string        // agent-side: dashboard URL to report to
	RDConnectionBroker string        // empty = local host
	TLSCert            string        // path to PEM certificate file
	TLSKey             string        // path to PEM private key file
	TLSFingerprint     string        // SHA-256 cert fingerprint for pinning (agent-side)
	AutoPin            bool          // auto-pin dashboard cert on register (default false)
	FetchInterval      time.Duration // interval between config fetches from dashboard
	HeartbeatInterval  time.Duration // expected interval between agent reports
}

// ── Defaults ────────────────────────────────────────────────────────────

// DefaultConfigPath returns the path to config.json.
func DefaultConfigPath() string {
	return DefaultDataDir() + `\config.json`
}

// DefaultConfig returns a Config with all defaults populated.
func DefaultConfig() *Config {
	return &Config{
		GracePeriod:             DefaultGracePeriod,
		RetentionDays:           DefaultRetentionDays,
		PollInterval:            DefaultPollInterval,
		AuditPath:               DefaultDBPath(),
		Notifications:           []NotificationTarget{},
		NotificationExclusions:  []string{},
		Dashboard:               DashboardJSON{Port: DefaultDashboardPort, Group: DefaultDashboardGroup, FetchInterval: DefaultDashboardFetchInterval},
		SessionWarningThreshold: DefaultSessionWarningThreshold,
		MemoryLimitMB:           DefaultMemoryLimitMB,
		Performance:             PerformanceConfig{CollectPerSession: true},
		Retention:               RetentionConfig{MetricsDays: DefaultMetricsDays, AuditDays: DefaultAuditDays},
		Telemetry:               TelemetryConfig{AggregatorIntervalSeconds: DefaultAggregatorIntervalSeconds, RetentionIntervalMinutes: DefaultRetentionIntervalMinutes},
		EvtSpike:                EvtSpikeConfig{ChannelCooldownMinutes: map[string]int{}, DisabledChannels: []string{}, AddedChannels: []string{}},
		Update:                  UpdateConfig{Enabled: false, Channel: ChannelStable, PollInterval: Duration(DefaultUpdatePollInterval)},
	}
}

// ── Conversion helpers ──────────────────────────────────────────────────

// ToServiceConfig converts the JSON config to runtime ServiceConfig.
func (c *Config) ToServiceConfig() ServiceConfig {
	return ServiceConfig{
		GracePeriod:             time.Duration(c.GracePeriod) * time.Minute,
		RetentionDays:           c.RetentionDays,
		PollInterval:            time.Duration(c.PollInterval) * time.Second,
		AuditPath:               c.AuditPath,
		SessionWarningThreshold: c.SessionWarningThreshold,
		Performance:             c.Performance,
		DashboardOnly:           c.DashboardOnly,
	}
}

// ToDashboardConfig converts the JSON config to runtime DashboardConfig.
func (c *Config) ToDashboardConfig() DashboardConfig {
	return DashboardConfig{
		Enabled:            c.Dashboard.Enabled,
		Port:               c.Dashboard.Port,
		Group:              c.Dashboard.Group,
		URL:                c.Dashboard.URL,
		RDConnectionBroker: c.Dashboard.RDConnectionBroker,
		TLSCert:            c.Dashboard.TLSCert,
		TLSKey:             c.Dashboard.TLSKey,
		TLSFingerprint:     c.Dashboard.TLSFingerprint,
		AutoPin:            c.Dashboard.AutoPin != nil && *c.Dashboard.AutoPin,
		FetchInterval:      c.dashFetchInterval(),
		HeartbeatInterval:  time.Duration(c.PollInterval) * time.Second,
	}
}

func (c *Config) dashFetchInterval() time.Duration {
	s := c.Dashboard.FetchInterval
	if s < 10 || s > 86400 {
		s = DefaultDashboardFetchInterval
	}
	return time.Duration(s) * time.Second
}

// HasTargets returns true if at least one notification target is configured.
func (c *Config) HasTargets() bool {
	for _, t := range c.Notifications {
		if t.URL != "" {
			return true
		}
	}
	return false
}

// ── Validation ──────────────────────────────────────────────────────────

// validateLogLevel returns the canonical lowercase level string if valid, or the
// provided fallback if the input is missing/empty/invalid. A warning is logged
// for invalid (non-empty) values.
func validateLogLevel(val, fieldName, fallback string) string {
	if val == "" {
		return fallback
	}
	if _, err := logging.ParseLevel(val); err != nil {
		slog.Default().Warn("invalid log level in config, using default", "field", fieldName, "value", val, "default", fallback)
		return fallback
	}
	return strings.ToLower(val)
}

// Validate clamps and corrects config values in place.
func (c *Config) Validate() {
	c.RetentionDays = ClampRetention(c.RetentionDays)
	c.Retention.MetricsDays = ClampRetention(c.Retention.MetricsDays)
	ClampEvtSpike(&c.EvtSpike)
	if c.EvtSpike.BaselinePath != "" &&
		!strings.EqualFold(filepath.Clean(c.EvtSpike.BaselinePath), filepath.Clean(DefaultBaselinePath())) {
		slog.Default().Warn("evtspike baseline_path outside the canonical data directory ignored",
			"requested", c.EvtSpike.BaselinePath, "canonical", DefaultBaselinePath())
	}
	c.EvtSpike.BaselinePath = DefaultBaselinePath()
	if c.Retention.AuditDays < MinRetentionDays {
		slog.Default().Warn("audit retention below minimum, clamping", "requested", c.Retention.AuditDays, "min", MinRetentionDays)
		c.Retention.AuditDays = MinRetentionDays
	}
	if c.Retention.AuditDays > MaxAuditRetentionDays {
		slog.Default().Warn("audit retention exceeds maximum, clamping", "requested", c.Retention.AuditDays, "max", MaxAuditRetentionDays)
		c.Retention.AuditDays = MaxAuditRetentionDays
	}
	if c.Telemetry.AggregatorIntervalSeconds < 10 || c.Telemetry.AggregatorIntervalSeconds > 3600 {
		c.Telemetry.AggregatorIntervalSeconds = DefaultAggregatorIntervalSeconds
	}
	if c.Telemetry.RetentionIntervalMinutes < 1 || c.Telemetry.RetentionIntervalMinutes > 1440 {
		c.Telemetry.RetentionIntervalMinutes = DefaultRetentionIntervalMinutes
	}

	c.LogFileLevel = validateLogLevel(c.LogFileLevel, "log_file_level", "info")
	c.LogEventLevel = validateLogLevel(c.LogEventLevel, "log_event_level", "info")

	if c.GracePeriod < 1 || c.GracePeriod > 1440 {
		c.GracePeriod = DefaultGracePeriod
	}
	if c.PollInterval < 10 || c.PollInterval > MaxPollInterval {
		c.PollInterval = DefaultPollInterval
	}
	// The telemetry writer is single-location. Keeping the deprecated
	// audit_path field canonical prevents configuration from advertising a
	// second data root that the service no longer uses.
	if c.AuditPath != "" && !strings.EqualFold(filepath.Clean(c.AuditPath), filepath.Clean(DefaultDBPath())) {
		slog.Default().Warn("audit_path outside the canonical data directory ignored",
			"requested", c.AuditPath, "canonical", DefaultDBPath())
	}
	c.AuditPath = DefaultDBPath()
	if c.Dashboard.Port < 1 || c.Dashboard.Port > 65535 {
		c.Dashboard.Port = DefaultDashboardPort
	}
	c.Dashboard.Group = strings.TrimSpace(c.Dashboard.Group)
	broker, err := NormalizeRDConnectionBroker(c.Dashboard.RDConnectionBroker)
	if err != nil {
		slog.Default().Warn("invalid RD Connection Broker ignored", "requested", c.Dashboard.RDConnectionBroker, "error", err)
	}
	c.Dashboard.RDConnectionBroker = broker
	if c.Dashboard.Group == "" {
		c.Dashboard.Group = DefaultDashboardGroup
	}
	if c.SessionWarningThreshold < 0 {
		c.SessionWarningThreshold = 0
	}
	if c.SessionWarningThreshold > 100 {
		c.SessionWarningThreshold = 100
	}
	if c.MemoryLimitMB < MinMemoryLimitMB {
		c.MemoryLimitMB = DefaultMemoryLimitMB
	}
	if c.MemoryLimitMB > MaxMemoryLimitMB {
		slog.Default().Warn("memory limit exceeds maximum, clamping", "requested", c.MemoryLimitMB, "max", MaxMemoryLimitMB)
		c.MemoryLimitMB = MaxMemoryLimitMB
	}

	// Normalize input delay percentile.
	switch strings.ToLower(c.Performance.InputDelayPercentile) {
	case "p50":
		c.Performance.InputDelayPercentile = "p50"
	case "p95", "":
		c.Performance.InputDelayPercentile = "p95"
	default:
		slog.Default().Warn("invalid input_delay_percentile, defaulting to p95",
			"value", c.Performance.InputDelayPercentile)
		c.Performance.InputDelayPercentile = "p95"
	}

	// Populate zero-value performance fields with their effective defaults
	// so the config file is self-documenting after normalization.
	if c.Performance.SampleIntervalSec == 0 {
		c.Performance.SampleIntervalSec = DefaultSampleInterval
	}
	if c.Performance.SampleIntervalSec < 10 {
		c.Performance.SampleIntervalSec = 10
	}
	if c.Performance.SampleIntervalSec > 300 {
		c.Performance.SampleIntervalSec = 300
	}

	if c.Performance.LoadAlertDelaySec == 0 {
		c.Performance.LoadAlertDelaySec = DefaultLoadAlertDelaySec
	}
	if c.Performance.InputDelayAlertDelaySec == 0 {
		c.Performance.InputDelayAlertDelaySec = DefaultInputDelayAlertDelaySec
	}

	// Strip notification targets with unknown types (must be "webhook" or "ntfy").
	// A target with an unrecognised type would silently never fire — reject it early.
	c.Notifications = slices.DeleteFunc(c.Notifications, func(t NotificationTarget) bool {
		if t.Type != "webhook" && t.Type != "ntfy" && t.Type != "email" {
			slog.Default().Warn("notification target has unknown type, ignored", "type", t.Type, "url", t.URL)
			return true
		}
		return false
	})

	// Strip notification targets with invalid URL schemes (must be http or https).
	c.Notifications = slices.DeleteFunc(c.Notifications, func(t NotificationTarget) bool {
		if t.URL == "" {
			return false // empty URL is handled elsewhere
		}
		lower := strings.ToLower(t.URL)
		validScheme := strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") ||
			strings.HasPrefix(lower, "smtp://") || strings.HasPrefix(lower, "smtps://")
		if !validScheme {
			slog.Default().Warn("notification target has invalid URL scheme, ignored", "url", t.URL)
			return true
		}
		return false
	})

	// Default empty triggers to DefaultTriggers, strip invalid trigger names.
	// Clamp RepeatMinutes to [0, MaxRepeatMinutes] to prevent time.Duration overflow.
	for i := range c.Notifications {
		if len(c.Notifications[i].Triggers) == 0 {
			c.Notifications[i].Triggers = append([]Trigger{}, DefaultTriggers...)
		} else {
			c.Notifications[i].Triggers = slices.DeleteFunc(c.Notifications[i].Triggers, func(tr Trigger) bool {
				if !ValidTriggers[tr] {
					slog.Default().Warn("unknown trigger ignored", "trigger", tr, "url", c.Notifications[i].URL)
					return true
				}
				return false
			})
		}
		normalizedExclusions := make([]NotificationServerExclusion, 0, len(c.Notifications[i].ServerExclusions))
		for _, exclusion := range c.Notifications[i].ServerExclusions {
			exclusion.Server = strings.TrimSuffix(strings.TrimSpace(exclusion.Server), ".")
			if exclusion.Server == "" {
				slog.Default().Warn("notification server exclusion with empty server ignored", "url", c.Notifications[i].URL)
				continue
			}
			exclusion.Triggers = slices.DeleteFunc(exclusion.Triggers, func(tr Trigger) bool {
				if !ValidTriggers[tr] || !c.Notifications[i].HasTrigger(tr) {
					slog.Default().Warn("notification server exclusion trigger ignored",
						"server", exclusion.Server, "trigger", tr, "url", c.Notifications[i].URL)
					return true
				}
				return false
			})
			if len(exclusion.Triggers) == 0 {
				continue
			}
			normalizedExclusions = append(normalizedExclusions, exclusion)
		}
		c.Notifications[i].ServerExclusions = normalizedExclusions
		if c.Notifications[i].RepeatMinutes < 0 {
			c.Notifications[i].RepeatMinutes = 0
		}
		if c.Notifications[i].RepeatMinutes > MaxRepeatMinutes {
			c.Notifications[i].RepeatMinutes = MaxRepeatMinutes
		}
		switch strings.ToLower(c.Notifications[i].Severity) {
		case "", "warning", "alert":
			c.Notifications[i].Severity = strings.ToLower(c.Notifications[i].Severity)
		default:
			slog.Default().Warn("notification target severity must be 'warning' or 'alert', reset to default",
				"severity", c.Notifications[i].Severity, "url", c.Notifications[i].URL)
			c.Notifications[i].Severity = ""
		}
	}

	// Global exclusions are a catch-all routing policy, deliberately separate
	// from per-target trigger exclusions. Normalize and de-duplicate them so a
	// single canonical host controls every current and future target/trigger.
	exclusions := make([]string, 0, len(c.NotificationExclusions))
	seenExclusions := make(map[string]struct{}, len(c.NotificationExclusions))
	for _, exclusion := range c.NotificationExclusions {
		host := CanonicalNotificationHost(exclusion)
		if host == "" {
			slog.Default().Warn("empty notification exclusion ignored")
			continue
		}
		if _, seen := seenExclusions[host]; seen {
			continue
		}
		seenExclusions[host] = struct{}{}
		exclusions = append(exclusions, host)
	}
	c.NotificationExclusions = exclusions

	// Validate email targets: require smtp(s):// URL, from, and at least one to.
	for i := range c.Notifications {
		t := &c.Notifications[i]
		if t.Type != "email" {
			continue
		}
		lower := strings.ToLower(t.URL)
		if !strings.HasPrefix(lower, "smtp://") && !strings.HasPrefix(lower, "smtps://") {
			slog.Default().Warn("email target requires smtp:// or smtps:// URL, ignored", "url", t.URL)
			t.URL = ""
		}
		if t.From == "" {
			slog.Default().Warn("email target missing 'from' address, ignored", "url", t.URL)
			t.URL = ""
		}
		if len(t.To) == 0 {
			slog.Default().Warn("email target missing 'to' addresses, ignored", "url", t.URL)
			t.URL = ""
		}
	}

	// Secrets remain plaintext in runtime Config values. Persistence encrypts a
	// private copy so saving cannot corrupt notification credentials still in
	// use by the service.

	// UpdateConfig: clamp Channel and PollInterval per spec 010 FR-001a / FR-003.
	switch c.Update.Channel {
	case ChannelStable, ChannelPrerelease:
		// recognized — leave as-is
	case "":
		c.Update.Channel = ChannelStable
	default:
		slog.Default().Warn("update.channel unknown value, clamping to stable",
			"requested", c.Update.Channel, "clamped", ChannelStable)
		c.Update.Channel = ChannelStable
	}
	if time.Duration(c.Update.PollInterval) == 0 {
		c.Update.PollInterval = Duration(DefaultUpdatePollInterval)
	} else if time.Duration(c.Update.PollInterval) < MinUpdatePollInterval {
		slog.Default().Warn("update.poll_interval below minimum, clamping",
			"requested", time.Duration(c.Update.PollInterval).String(),
			"clamped", MinUpdatePollInterval.String())
		c.Update.PollInterval = Duration(MinUpdatePollInterval)
	}
}

// DecryptSecrets replaces DPAPI-encrypted secrets with their plaintext values
// so the in-memory Config is ready for use by notification senders.
// Call after Validate() when the config will be used at runtime (not when
// preparing to write it back to disk).
func (c *Config) DecryptSecrets() {
	for i := range c.Notifications {
		s := c.Notifications[i].Secret
		if !strings.HasPrefix(s, dpapiPrefix) {
			continue
		}
		ct, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, dpapiPrefix))
		if err != nil {
			slog.Default().Warn("failed to base64-decode secret", "error", err)
			c.Notifications[i].Secret = ""
			continue
		}
		pt, err := DPAPIDecrypt(ct)
		if err != nil {
			slog.Default().Warn("failed to DPAPI-decrypt secret", "error", err)
			c.Notifications[i].Secret = ""
			continue
		}
		c.Notifications[i].Secret = string(pt)
	}
}

// ClampRetention enforces the retention boundary (1-365 days). Values
// outside the range are clamped and a warning is logged.
func ClampRetention(days int) int {
	if days < MinRetentionDays {
		slog.Default().Warn("retention below minimum, clamping", "requested", days, "min", MinRetentionDays)
		return MinRetentionDays
	}
	if days > MaxRetentionDays {
		slog.Default().Warn("retention exceeds maximum, clamping", "requested", days, "max", MaxRetentionDays)
		return MaxRetentionDays
	}
	return days
}

// ClampEvtSpike normalises EvtSpikeConfig per data-model.md §1: zero-value
// numeric fields are promoted to defaults silently (treated as "not set"),
// non-zero out-of-range values are clamped to the nearest endpoint with a
// warning, and PersistIntervalSeconds emits a coherence warning when it is
// not a multiple of 900 (slot rollover boundary).
func ClampEvtSpike(cfg *EvtSpikeConfig) {
	if cfg == nil {
		return
	}

	cfg.MinCount = clampIntField("min_count", cfg.MinCount,
		DefaultEvtSpikeMinCount, MinEvtSpikeMinCount, MaxEvtSpikeMinCount)
	cfg.Threshold = clampFloatField("threshold", cfg.Threshold,
		DefaultEvtSpikeThreshold, MinEvtSpikeThreshold, MaxEvtSpikeThreshold)
	cfg.CooldownMinutes = clampIntField("cooldown_minutes", cfg.CooldownMinutes,
		DefaultEvtSpikeCooldownMinutes, MinEvtSpikeCooldownMinutes, MaxEvtSpikeCooldownMinutes)
	cfg.SlotMaturityObservations = clampIntField("slot_maturity_observations", cfg.SlotMaturityObservations,
		DefaultEvtSpikeSlotMaturityObservations, MinEvtSpikeSlotMaturityObservations, MaxEvtSpikeSlotMaturityObservations)
	cfg.PersistIntervalSeconds = clampIntField("persist_interval_seconds", cfg.PersistIntervalSeconds,
		DefaultEvtSpikePersistIntervalSeconds, MinEvtSpikePersistIntervalSeconds, MaxEvtSpikePersistIntervalSeconds)
	cfg.HalfLifeBuckets = clampIntField("half_life_buckets", cfg.HalfLifeBuckets,
		DefaultEvtSpikeHalfLifeBuckets, MinEvtSpikeHalfLifeBuckets, MaxEvtSpikeHalfLifeBuckets)
	cfg.PriorStrength = clampFloatField("prior_strength", cfg.PriorStrength,
		DefaultEvtSpikePriorStrength, MinEvtSpikePriorStrength, MaxEvtSpikePriorStrength)
	cfg.MeanPerBucketPrior = clampFloatField("mean_per_bucket_prior", cfg.MeanPerBucketPrior,
		DefaultEvtSpikeMeanPerBucketPrior, MinEvtSpikeMeanPerBucketPrior, MaxEvtSpikeMeanPerBucketPrior)

	// PersistIntervalSeconds is at this point in [60, 86400]. Slot rollover
	// (R1) is preserved only when the cadence is a multiple of 900 s.
	if cfg.PersistIntervalSeconds%DefaultEvtSpikePersistIntervalSeconds != 0 {
		slog.Default().Warn("evtspike: persist_interval_seconds not aligned to slot rollover (multiple of 900); R1 coherence not guaranteed",
			"value", cfg.PersistIntervalSeconds)
	}

	if cfg.DisabledChannels == nil {
		cfg.DisabledChannels = []string{}
	}
	if cfg.AddedChannels == nil {
		cfg.AddedChannels = []string{}
	}
	normalizeEvtSpikeChannelCooldowns(cfg)
}

// normalizeEvtSpikeChannelCooldowns canonicalizes direct config-file values.
// Keys are sorted before trimming. If two keys trim to the same exact channel
// name, the lexicographically first raw key wins and later keys are discarded;
// dashboard patches reject that ambiguity before persistence.
func normalizeEvtSpikeChannelCooldowns(cfg *EvtSpikeConfig) {
	if len(cfg.ChannelCooldownMinutes) == 0 {
		cfg.ChannelCooldownMinutes = map[string]int{}
		return
	}

	keys := make([]string, 0, len(cfg.ChannelCooldownMinutes))
	for channel := range cfg.ChannelCooldownMinutes {
		keys = append(keys, channel)
	}
	sort.Strings(keys)

	normalized := make(map[string]int, len(keys))
	for _, rawChannel := range keys {
		channel := strings.TrimSpace(rawChannel)
		if channel == "" {
			slog.Default().Warn("evtspike: ignoring blank channel cooldown override")
			continue
		}
		if _, duplicate := normalized[channel]; duplicate {
			slog.Default().Warn("evtspike: ignoring duplicate normalized channel cooldown override", "channel", channel)
			continue
		}
		if len(normalized) >= 256 {
			slog.Default().Warn("evtspike: ignoring channel cooldown overrides above limit", "max", 256)
			break
		}
		normalized[channel] = clampIntField(
			"channel_cooldown_minutes["+channel+"]",
			cfg.ChannelCooldownMinutes[rawChannel],
			DefaultEvtSpikeCooldownMinutes,
			MinEvtSpikeCooldownMinutes,
			MaxEvtSpikeCooldownMinutes,
		)
	}
	cfg.ChannelCooldownMinutes = normalized
}

func cloneChannelCooldownMinutes(overrides map[string]int) map[string]int {
	clone := make(map[string]int, len(overrides))
	for channel, minutes := range overrides {
		clone[channel] = minutes
	}
	return clone
}

func clampIntField(name string, v, def, min, max int) int {
	if v == 0 {
		return def
	}
	if v < min {
		slog.Default().Warn("evtspike: field clamped to minimum", "field", name, "requested", v, "min", min)
		return min
	}
	if v > max {
		slog.Default().Warn("evtspike: field clamped to maximum", "field", name, "requested", v, "max", max)
		return max
	}
	return v
}

func clampFloatField(name string, v, def, min, max float64) float64 {
	if v == 0 {
		return def
	}
	if v < min {
		slog.Default().Warn("evtspike: field clamped to minimum", "field", name, "requested", v, "min", min)
		return min
	}
	if v > max {
		slog.Default().Warn("evtspike: field clamped to maximum", "field", name, "requested", v, "max", max)
		return max
	}
	return v
}

// ── Load / Save ─────────────────────────────────────────────────────────

// LoadConfig reads config.json from the default path. If the file does not
// exist it attempts a one-time migration from registry values. If the
// registry has no config either, a default config is written and returned.
func LoadConfig() (*Config, error) {
	return withConfigFileLockValue(loadConfigLocked)
}

func loadConfigLocked() (*Config, error) {
	path := DefaultConfigPath()
	data, err := os.ReadFile(path)
	if err == nil {
		cfg := DefaultConfig()
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		migrateLegacyBaseline(cfg.EvtSpike.BaselinePath)
		cfg.Validate()
		cfg.DecryptSecrets()
		return cfg, nil
	}

	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	// File does not exist — try migration from registry.
	if cfg, migErr := MigrateFromRegistry(); migErr == nil {
		return cfg, nil
	}

	// No registry config either — fresh install.
	cfg := DefaultConfig()
	if err := saveConfigToFileLocked(cfg); err != nil {
		return nil, fmt.Errorf("write default config: %w", err)
	}
	slog.Default().Info("default config written", "path", path)
	return cfg, nil
}

func migrateLegacyBaseline(source string) {
	target := DefaultBaselinePath()
	if source == "" || strings.EqualFold(filepath.Clean(source), filepath.Clean(target)) {
		return
	}
	if _, err := os.Stat(target); err == nil {
		return
	} else if !os.IsNotExist(err) {
		slog.Default().Warn("evtspike baseline migration target unavailable", "path", target, "error", err)
		return
	}

	src, err := os.Open(source)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Default().Warn("evtspike baseline migration source unavailable", "path", source, "error", err)
		}
		return
	}
	defer func() { _ = src.Close() }()

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		slog.Default().Warn("evtspike baseline migration failed", "source", source, "target", target, "error", err)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "baseline.migration-*")
	if err != nil {
		slog.Default().Warn("evtspike baseline migration failed", "source", source, "target", target, "error", err)
		return
	}
	tmpPath := tmp.Name()
	published := false
	defer func() {
		_ = tmp.Close()
		if !published {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := io.Copy(tmp, src); err != nil {
		slog.Default().Warn("evtspike baseline migration failed", "source", source, "target", target, "error", err)
		return
	}
	if err := tmp.Sync(); err != nil {
		slog.Default().Warn("evtspike baseline migration failed", "source", source, "target", target, "error", err)
		return
	}
	if err := tmp.Close(); err != nil {
		slog.Default().Warn("evtspike baseline migration failed", "source", source, "target", target, "error", err)
		return
	}
	if err := src.Close(); err != nil {
		slog.Default().Warn("evtspike baseline migration failed", "source", source, "target", target, "error", err)
		return
	}
	if err := os.Rename(tmpPath, target); err != nil {
		slog.Default().Warn("evtspike baseline migration failed", "source", source, "target", target, "error", err)
		return
	}
	published = true
	if err := os.Remove(source); err != nil {
		slog.Default().Warn("evtspike legacy baseline removal failed", "path", source, "error", err)
	}
	slog.Default().Info("evtspike baseline migrated", "source", source, "target", target)
}
func cloneConfig(cfg *Config) *Config {
	clone := *cfg
	clone.Notifications = slices.Clone(cfg.Notifications)
	clone.NotificationExclusions = slices.Clone(cfg.NotificationExclusions)
	for i := range clone.Notifications {
		clone.Notifications[i].Triggers = slices.Clone(cfg.Notifications[i].Triggers)
		clone.Notifications[i].To = slices.Clone(cfg.Notifications[i].To)
		if enabled := cfg.Notifications[i].Enabled; enabled != nil {
			value := *enabled
			clone.Notifications[i].Enabled = &value
		}
	}
	clone.EvtSpike.DisabledChannels = slices.Clone(cfg.EvtSpike.DisabledChannels)
	clone.EvtSpike.AddedChannels = slices.Clone(cfg.EvtSpike.AddedChannels)
	clone.EvtSpike.ChannelCooldownMinutes = cloneChannelCooldownMinutes(cfg.EvtSpike.ChannelCooldownMinutes)
	if autoPin := cfg.Dashboard.AutoPin; autoPin != nil {
		value := *autoPin
		clone.Dashboard.AutoPin = &value
	}
	return &clone
}

func configForPersistence(cfg *Config) (*Config, error) {
	persisted := cloneConfig(cfg)
	for i := range persisted.Notifications {
		secret := persisted.Notifications[i].Secret
		if secret == "" || strings.HasPrefix(secret, dpapiPrefix) {
			continue
		}
		ciphertext, err := DPAPIEncrypt([]byte(secret))
		if err != nil {
			return nil, fmt.Errorf("encrypt notification secret %d: %w", i, err)
		}
		persisted.Notifications[i].Secret = dpapiPrefix + base64.StdEncoding.EncodeToString(ciphertext)
	}
	return persisted, nil
}

// SaveConfig writes the full config to disk. Only admin/CLI/service callers
// should use this. Dashboard API should use scoped updaters instead.
func SaveConfig(cfg *Config) error {
	cfg.Validate()
	return saveConfigToFile(cfg)
}

// saveConfigToFile atomically writes config.json using a named mutex for
// cross-process serialization.
func saveConfigToFile(cfg *Config) error {
	return withConfigFileLock(func() error {
		return saveConfigToFileLocked(cfg)
	})
}

func saveConfigToFileLocked(cfg *Config) error {
	path := DefaultConfigPath()
	tmpPath := path + ".tmp"

	// Ensure data directory exists.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	// Marshal an encrypted copy. Runtime callers keep normalized plaintext
	// credentials after SaveConfig returns.
	persisted, err := configForPersistence(cfg)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	data = append(data, '\n')

	// Write to temp file.
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}

	// Apply ACL to the tmp file BEFORE the rename. NTFS same-volume rename
	// preserves the source file's explicit security descriptor, so a tight
	// ACL on tmp carries into the final path. Tightening AFTER the rename
	// leaves a window where %ProgramData%-inherited ACLs (which grant Users
	// read+execute) make notification secrets briefly readable by any local
	// user.
	if aclErr := restrictConfigACL(tmpPath); aclErr != nil {
		// Don't leak a tmp file that may already contain unprotected secrets.
		if rmErr := os.Remove(tmpPath); rmErr != nil && !os.IsNotExist(rmErr) {
			return errors.Join(
				fmt.Errorf("restrict acl on tmp config: %w", aclErr),
				fmt.Errorf("cleanup tmp: %w", rmErr),
			)
		}
		return fmt.Errorf("restrict acl on tmp config: %w", aclErr)
	}

	// Atomic rename. The tight ACL just applied to tmpPath carries into path
	// because NTFS same-volume rename preserves explicit DACLs.
	src, _ := windows.UTF16PtrFromString(tmpPath)
	dst, _ := windows.UTF16PtrFromString(path)
	if err := windows.MoveFileEx(src, dst, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		// Fallback: plain rename (works if no other process holds the file).
		if renameErr := os.Rename(tmpPath, path); renameErr != nil {
			// Both rename attempts failed. Clean up the tmp file so a
			// world-readable config.json.tmp doesn't accumulate in
			// %ProgramData% across retries.
			renameWrap := fmt.Errorf("rename config: %w (movefileex: %v)", renameErr, err)
			if rmErr := os.Remove(tmpPath); rmErr != nil && !os.IsNotExist(rmErr) {
				return errors.Join(renameWrap, fmt.Errorf("cleanup tmp: %w", rmErr))
			}
			return renameWrap
		}
	}

	return nil
}

func withConfigFileLock(fn func() error) error {
	// Pin this goroutine to its current OS thread for the entire acquire-use-
	// release window. Windows named mutexes are owned by the THREAD that called
	// WaitForSingleObject; only that thread may call ReleaseMutex. Go's
	// scheduler migrates goroutines freely across OS threads, so without
	// LockOSThread the deferred ReleaseMutex can land on a different thread,
	// fail with ERROR_NOT_OWNER (silently discarded below), and strand the
	// named mutex until process exit. Concurrent SaveConfig calls then block
	// forever waiting on an abandoned kernel object — observed as a CI-only
	// test flake on GitHub Actions' slower Windows runners.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Acquire cross-process mutex.
	// windows.CreateMutex returns ERROR_ALREADY_EXISTS (as an error) when the
	// named mutex already exists in the kernel object namespace — the handle is
	// still valid in that case, so treat it as a success.
	mutexName, _ := windows.UTF16PtrFromString(configMutexName)
	mutex, err := windows.CreateMutex(nil, false, mutexName)
	if err != nil && err != windows.ERROR_ALREADY_EXISTS {
		return fmt.Errorf("create config mutex: %w", err)
	}
	defer func() { _ = windows.CloseHandle(mutex) }()

	event, _ := windows.WaitForSingleObject(mutex, 5000) // 5s timeout
	if event == uint32(windows.WAIT_TIMEOUT) {
		return fmt.Errorf("config mutex timeout")
	}
	defer func() { _ = windows.ReleaseMutex(mutex) }()

	return fn()
}

func withConfigFileLockValue[T any](fn func() (T, error)) (T, error) {
	var out T
	err := withConfigFileLock(func() error {
		var err error
		out, err = fn()
		return err
	})
	return out, err
}

func readModifyWrite(f func(*Config) error) error {
	return withConfigFileLock(func() error {
		cfg, err := loadConfigLocked()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		if err := f(cfg); err != nil {
			return err
		}
		cfg.Validate()
		if err := saveConfigToFileLocked(cfg); err != nil {
			return err
		}
		return nil
	})
}

// PersistRDConnectionBroker atomically updates the dashboard Connection Broker.
// Callers MUST first successfully probe the broker while running under the
// service identity; this helper only validates and persists that proven value.
// The broker is normalized and validated before the locked read-modify-write so
// invalid input cannot cause a config-file mutation.
func PersistRDConnectionBroker(broker string) error {
	normalized, err := NormalizeRDConnectionBroker(broker)
	if err != nil {
		return fmt.Errorf("rd connection broker: %w", err)
	}
	return readModifyWrite(func(cfg *Config) error {
		cfg.Dashboard.RDConnectionBroker = normalized
		return nil
	})
}

// restrictConfigACL limits config files to SYSTEM and Administrators. Runtime
// callers skip this when not elevated so local development does not lock the
// developer out of config.json.
func restrictConfigACL(path string) error {
	if !isElevated() {
		return nil
	}
	return winacl.RestrictFile(path,
		winacl.Grant{SIDType: windows.WinLocalSystemSid, Permissions: windows.GENERIC_ALL},
		winacl.Grant{SIDType: windows.WinBuiltinAdministratorsSid, Permissions: windows.GENERIC_ALL},
	)
}

// isElevated returns true if the current process token is a member of the
// built-in Administrators group.
func isElevated() bool {
	var sid *windows.SID
	err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY,
		2,
		windows.SECURITY_BUILTIN_DOMAIN_RID,
		windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0,
		&sid,
	)
	if err != nil {
		return false
	}
	defer func() { _ = windows.FreeSid(sid) }()
	member, err := windows.Token(0).IsMember(sid)
	if err != nil {
		return false
	}
	return member
}

// UpdateNotifySettings atomically updates notification targets, session warning
// threshold, grace period, poll interval, and performance config in a single
// config load+save cycle. Any nil argument is left unchanged. This is the
// preferred API for the dashboard PUT /api/v1/settings handler.
func UpdateNotifySettings(notifications *[]NotificationTarget, sessionThreshold *int, gracePeriod *int, pollInterval *int, performance *PerformanceConfig) error {
	return readModifyWrite(func(cfg *Config) error {
		if notifications != nil {
			cfg.Notifications = *notifications
		}
		if sessionThreshold != nil {
			if *sessionThreshold < 0 || *sessionThreshold > 100 {
				return fmt.Errorf("threshold must be 0-100, got %d", *sessionThreshold)
			}
			cfg.SessionWarningThreshold = *sessionThreshold
		}
		if gracePeriod != nil {
			if *gracePeriod < 1 || *gracePeriod > 1440 {
				return fmt.Errorf("grace period must be 1-1440 minutes, got %d", *gracePeriod)
			}
			cfg.GracePeriod = *gracePeriod
		}
		if pollInterval != nil {
			if *pollInterval < 10 || *pollInterval > MaxPollInterval {
				return fmt.Errorf("poll interval must be 10-%d seconds, got %d", MaxPollInterval, *pollInterval)
			}
			cfg.PollInterval = *pollInterval
		}
		if performance != nil {
			cfg.Performance = *performance
		}
		return nil
	})
}

// UpdateUpdateConfig atomically replaces the self-update settings. A nil
// argument is a no-op so dashboard PATCH-like requests can omit the block.
func UpdateUpdateConfig(update *UpdateConfig) error {
	if update == nil {
		return nil
	}
	if update.Channel != ChannelStable && update.Channel != ChannelPrerelease {
		return fmt.Errorf("update channel must be %q or %q, got %q", ChannelStable, ChannelPrerelease, update.Channel)
	}
	if interval := time.Duration(update.PollInterval); interval < MinUpdatePollInterval {
		return fmt.Errorf("update poll interval must be at least %s, got %s", MinUpdatePollInterval, interval)
	}
	return readModifyWrite(func(cfg *Config) error {
		cfg.Update = *update
		return nil
	})
}

// ReadModifyWriteNotifications atomically modifies the notifications slice

// under the same cross-process config lock used by every other config writer.
// The mutate callback receives a copy of the current targets and returns the
// new slice; returning an error aborts the write. This is the building block
// used by the dashboard's per-target REST endpoints (POST/PUT/DELETE
// /api/v1/settings/notifications) — they need a single load+save cycle that's
// indistinguishable from the bulk PUT path so the SSE broadcast and disk ACLs
// behave identically.
func ReadModifyWriteNotifications(mutate func(targets []NotificationTarget) ([]NotificationTarget, error)) error {
	return readModifyWrite(func(cfg *Config) error {
		// Pass a defensive copy so callers can't accidentally mutate the
		// original slice header before deciding to abort.
		current := make([]NotificationTarget, len(cfg.Notifications))
		copy(current, cfg.Notifications)
		next, err := mutate(current)
		if err != nil {
			return err
		}
		if next == nil {
			next = []NotificationTarget{}
		}
		cfg.Notifications = next
		return nil
	})
}

// ToggleNotificationExclusion atomically adds or removes host from the global
// notification exclusion list. Its return value is the resulting excluded
// state. Target-specific exclusions are intentionally untouched.
func ToggleNotificationExclusion(host string) (bool, error) {
	host = CanonicalNotificationHost(host)
	if host == "" {
		return false, fmt.Errorf("host is required")
	}
	excluded := false
	err := readModifyWrite(func(cfg *Config) error {
		for i, existing := range cfg.NotificationExclusions {
			if CanonicalNotificationHost(existing) == host {
				cfg.NotificationExclusions = append(
					cfg.NotificationExclusions[:i],
					cfg.NotificationExclusions[i+1:]...,
				)
				return nil
			}
		}
		cfg.NotificationExclusions = append(cfg.NotificationExclusions, host)
		excluded = true
		return nil
	})
	return excluded, err
}

// UpdateEvtSpikeEnabled flips only the evtspike.enabled flag, leaving all
// other evtspike fields (thresholds, channel lists, baseline_path, security
// channel gate) untouched. Retained as a thin wrapper for callers that only
// need the toggle (legacy tests and dashboard fallbacks). New code should
// prefer UpdateEvtSpike, which propagates the full operator-safe surface.
//
// Passing nil is a no-op. The live-reload path in internal/svc picks up the
// change via the config-file watcher and calls evtspike.Subsystem.Reload per
// the matrix in contracts/evtspike-config.md.
func UpdateEvtSpikeEnabled(enabled *bool) error {
	if enabled == nil {
		return nil
	}
	return UpdateEvtSpike(&EvtSpikeConfigPatch{Enabled: enabled})
}

// EvtSpikeConfigPatch is the operator-safe, dashboard-writeable subset of
// EvtSpikeConfig. Every pointer distinguishes "absent → leave unchanged"
// from "explicit value" so the bulk PUT handler can layer each field on
// top of an existing config without clobbering un-touched knobs.
//
// Admin-only fields (BaselinePath) are not exposed here; editing them
// requires editing config.json directly.
type EvtSpikeConfigPatch struct {
	Enabled                  *bool           `json:"enabled,omitempty"`
	MinCount                 *int            `json:"min_count,omitempty"`
	Threshold                *float64        `json:"threshold,omitempty"`
	CooldownMinutes          *int            `json:"cooldown_minutes,omitempty"`
	ChannelCooldownMinutes   *map[string]int `json:"channel_cooldown_minutes,omitempty"`
	SlotMaturityObservations *int            `json:"slot_maturity_observations,omitempty"`
	PersistIntervalSeconds   *int            `json:"persist_interval_seconds,omitempty"`
	HalfLifeBuckets          *int            `json:"half_life_buckets,omitempty"`
	PriorStrength            *float64        `json:"prior_strength,omitempty"`
	MeanPerBucketPrior       *float64        `json:"mean_per_bucket_prior,omitempty"`
	DisabledChannels         *[]string       `json:"disabled_channels,omitempty"`
	AddedChannels            *[]string       `json:"added_channels,omitempty"`
	SecurityChannelEnabled   *bool           `json:"security_channel_enabled,omitempty"`
}

// ValidateEvtSpikePatch returns an error describing the first out-of-range
// field in patch, or nil if all provided fields are within bounds. nil
// patch fields are skipped; absent fields mean "leave unchanged".
//
// Bounds mirror ClampEvtSpike — validation here lets the dashboard return
// a 400 with a clear message before the load-modify-save cycle, instead of
// silently clamping (which would mislead an operator who clicked Save on
// what they thought was 5 minutes but got 1).
func ValidateEvtSpikePatch(patch *EvtSpikeConfigPatch) error {
	if patch == nil {
		return nil
	}
	if v := patch.MinCount; v != nil && (*v < MinEvtSpikeMinCount || *v > MaxEvtSpikeMinCount) {
		return fmt.Errorf("evtspike.min_count must be %d-%d", MinEvtSpikeMinCount, MaxEvtSpikeMinCount)
	}
	if v := patch.Threshold; v != nil && (*v < MinEvtSpikeThreshold || *v > MaxEvtSpikeThreshold) {
		return fmt.Errorf("evtspike.threshold must be %g-%g", MinEvtSpikeThreshold, MaxEvtSpikeThreshold)
	}
	if v := patch.CooldownMinutes; v != nil && (*v < MinEvtSpikeCooldownMinutes || *v > MaxEvtSpikeCooldownMinutes) {
		return fmt.Errorf("evtspike.cooldown_minutes must be %d-%d", MinEvtSpikeCooldownMinutes, MaxEvtSpikeCooldownMinutes)
	}
	if v := patch.SlotMaturityObservations; v != nil && (*v < MinEvtSpikeSlotMaturityObservations || *v > MaxEvtSpikeSlotMaturityObservations) {
		return fmt.Errorf("evtspike.slot_maturity_observations must be %d-%d", MinEvtSpikeSlotMaturityObservations, MaxEvtSpikeSlotMaturityObservations)
	}
	if v := patch.PersistIntervalSeconds; v != nil && (*v < MinEvtSpikePersistIntervalSeconds || *v > MaxEvtSpikePersistIntervalSeconds) {
		return fmt.Errorf("evtspike.persist_interval_seconds must be %d-%d", MinEvtSpikePersistIntervalSeconds, MaxEvtSpikePersistIntervalSeconds)
	}
	if v := patch.HalfLifeBuckets; v != nil && (*v < MinEvtSpikeHalfLifeBuckets || *v > MaxEvtSpikeHalfLifeBuckets) {
		return fmt.Errorf("evtspike.half_life_buckets must be %d-%d", MinEvtSpikeHalfLifeBuckets, MaxEvtSpikeHalfLifeBuckets)
	}
	if v := patch.PriorStrength; v != nil && (*v < MinEvtSpikePriorStrength || *v > MaxEvtSpikePriorStrength) {
		return fmt.Errorf("evtspike.prior_strength must be %g-%g", MinEvtSpikePriorStrength, MaxEvtSpikePriorStrength)
	}
	if v := patch.MeanPerBucketPrior; v != nil && (*v < MinEvtSpikeMeanPerBucketPrior || *v > MaxEvtSpikeMeanPerBucketPrior) {
		return fmt.Errorf("evtspike.mean_per_bucket_prior must be %g-%g", MinEvtSpikeMeanPerBucketPrior, MaxEvtSpikeMeanPerBucketPrior)
	}
	if patch.DisabledChannels != nil && len(*patch.DisabledChannels) > 256 {
		return fmt.Errorf("evtspike.disabled_channels: too many entries (max 256)")
	}
	if patch.AddedChannels != nil && len(*patch.AddedChannels) > 256 {
		return fmt.Errorf("evtspike.added_channels: too many entries (max 256)")
	}
	if v := patch.ChannelCooldownMinutes; v != nil {
		if len(*v) > 256 {
			return fmt.Errorf("evtspike.channel_cooldown_minutes: too many entries (max 256)")
		}
		normalized := make(map[string]struct{}, len(*v))
		for channel, minutes := range *v {
			channel = strings.TrimSpace(channel)
			if channel == "" {
				return fmt.Errorf("evtspike.channel_cooldown_minutes: channel name must not be blank")
			}
			if _, duplicate := normalized[channel]; duplicate {
				return fmt.Errorf("evtspike.channel_cooldown_minutes: duplicate normalized channel %q", channel)
			}
			normalized[channel] = struct{}{}
			if minutes < MinEvtSpikeCooldownMinutes || minutes > MaxEvtSpikeCooldownMinutes {
				return fmt.Errorf("evtspike.channel_cooldown_minutes[%q] must be %d-%d", channel, MinEvtSpikeCooldownMinutes, MaxEvtSpikeCooldownMinutes)
			}
		}
	}
	return nil
}

// ApplyEvtSpikePatch layers patch onto dst, leaving untouched fields alone.
// Slice fields use the standard "absent slice pointer" sentinel — nil means
// don't change; non-nil (including length-0) replaces the slice. This mirrors
// the bulk PUT convention used by the notifications block.
func ApplyEvtSpikePatch(dst *EvtSpikeConfig, patch *EvtSpikeConfigPatch) {
	if dst == nil || patch == nil {
		return
	}
	if patch.Enabled != nil {
		dst.Enabled = *patch.Enabled
	}
	if patch.MinCount != nil {
		dst.MinCount = *patch.MinCount
	}
	if patch.Threshold != nil {
		dst.Threshold = *patch.Threshold
	}
	if patch.CooldownMinutes != nil {
		dst.CooldownMinutes = *patch.CooldownMinutes
	}
	if patch.ChannelCooldownMinutes != nil {
		dst.ChannelCooldownMinutes = cloneChannelCooldownMinutes(*patch.ChannelCooldownMinutes)
	}
	if patch.SlotMaturityObservations != nil {
		dst.SlotMaturityObservations = *patch.SlotMaturityObservations
	}
	if patch.PersistIntervalSeconds != nil {
		dst.PersistIntervalSeconds = *patch.PersistIntervalSeconds
	}
	if patch.HalfLifeBuckets != nil {
		dst.HalfLifeBuckets = *patch.HalfLifeBuckets
	}
	if patch.PriorStrength != nil {
		dst.PriorStrength = *patch.PriorStrength
	}
	if patch.MeanPerBucketPrior != nil {
		dst.MeanPerBucketPrior = *patch.MeanPerBucketPrior
	}
	if patch.DisabledChannels != nil {
		// Defensive copy so the caller can reuse its slice buffer.
		cp := append([]string{}, *patch.DisabledChannels...)
		dst.DisabledChannels = cp
	}
	if patch.AddedChannels != nil {
		cp := append([]string{}, *patch.AddedChannels...)
		dst.AddedChannels = cp
	}
	if patch.SecurityChannelEnabled != nil {
		dst.SecurityChannelEnabled = *patch.SecurityChannelEnabled
	}
	// Clamp + normalise zero slices so the persisted JSON stays canonical.
	ClampEvtSpike(dst)
}

// IsEvtSpikePatchEmpty reports whether patch is nil or every field pointer
// is nil (so the caller can short-circuit a no-op read-modify-write).
func IsEvtSpikePatchEmpty(patch *EvtSpikeConfigPatch) bool {
	if patch == nil {
		return true
	}
	return patch.Enabled == nil &&
		patch.MinCount == nil &&
		patch.Threshold == nil &&
		patch.CooldownMinutes == nil &&
		patch.SlotMaturityObservations == nil &&
		patch.PersistIntervalSeconds == nil &&
		patch.HalfLifeBuckets == nil &&
		patch.PriorStrength == nil &&
		patch.MeanPerBucketPrior == nil &&
		patch.ChannelCooldownMinutes == nil &&
		patch.DisabledChannels == nil &&
		patch.AddedChannels == nil &&
		patch.SecurityChannelEnabled == nil
}

// UpdateEvtSpike atomically writes the operator-safe evtspike fields
// described by patch. A nil patch (or one whose every pointer is nil) is a
// no-op. Range-validated by ValidateEvtSpikePatch before the load-modify-save
// cycle runs so a 400 returns immediately with a clear field-level message.
//
// BaselinePath is intentionally NOT in the patch — it is admin-only (lives in
// config.json) and is preserved verbatim across dashboard edits.
//
// Rolling-upgrade behavior: this writes the same EvtSpikeConfig shape that
// pre-existing config.json files already use, so older binaries that read
// only the documented fields round-trip without surprises. Newer fields
// (security_channel_enabled, half_life_buckets, prior_strength,
// mean_per_bucket_prior) are also already on the EvtSpikeConfig struct, so
// no schema migration is required.
func UpdateEvtSpike(patch *EvtSpikeConfigPatch) error {
	if err := ValidateEvtSpikePatch(patch); err != nil {
		return err
	}
	if IsEvtSpikePatchEmpty(patch) {
		return nil
	}
	return readModifyWrite(func(cfg *Config) error {
		ApplyEvtSpikePatch(&cfg.EvtSpike, patch)
		return nil
	})
}

// InstallCertificate copies a PEM cert and key into the data directory and
// updates config.json so the dashboard uses them. The key file is written
// with restrictive permissions (0600).
func InstallCertificate(certPath, keyPath string) error {
	// Verify source files.
	for _, f := range []string{certPath, keyPath} {
		if _, err := os.Stat(f); err != nil {
			return fmt.Errorf("file not found: %s", f)
		}
	}

	dataDir := DefaultDataDir()
	dstCert := dataDir + `\dashboard-tls.crt`
	dstKey := dataDir + `\dashboard-tls.key`

	certData, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("read cert: %w", err)
	}
	if err := os.WriteFile(dstCert, certData, 0644); err != nil {
		return fmt.Errorf("write cert: %w", err)
	}

	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("read key: %w", err)
	}
	if err := os.WriteFile(dstKey, keyData, 0600); err != nil {
		return fmt.Errorf("write key: %w", err)
	}

	return readModifyWrite(func(cfg *Config) error {
		cfg.Dashboard.TLSCert = dstCert
		cfg.Dashboard.TLSKey = dstKey
		return nil
	})
}

// ── Registry migration ──────────────────────────────────────────────────

// Installer-only; do not call from runtime code.
//
// MigrateFromRegistry reads the old registry-based config and writes it to
// config.json. Returns the migrated config. Called automatically by
// LoadConfig when config.json does not exist.
func MigrateFromRegistry() (*Config, error) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, ParametersKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return nil, fmt.Errorf("open registry: %w", err)
	}
	defer func() { _ = key.Close() }()

	cfg := DefaultConfig()

	// Service parameters.
	if v, _, err := key.GetIntegerValue("GracePeriod"); err == nil {
		cfg.GracePeriod = int(v)
	}
	if v, _, err := key.GetIntegerValue("RetentionDays"); err == nil {
		cfg.RetentionDays = int(v)
	}
	if v, _, err := key.GetIntegerValue("PollInterval"); err == nil {
		cfg.PollInterval = int(v)
	}
	if v, _, err := key.GetStringValue("AuditPath"); err == nil && v != "" {
		cfg.AuditPath = v
	}

	// Notification parameters — convert single URL to target.
	webhookURL := ""
	ntfyURL := ""
	onTransition := true
	onGraceExceeded := true
	repeatMin := 0

	if v, _, err := key.GetStringValue("WebhookURL"); err == nil {
		webhookURL = v
	}
	if v, _, err := key.GetStringValue("NtfyURL"); err == nil {
		ntfyURL = v
	}
	if v, _, err := key.GetIntegerValue("NotifyOnTransition"); err == nil {
		onTransition = v != 0
	}
	if v, _, err := key.GetIntegerValue("NotifyOnGraceExceeded"); err == nil {
		onGraceExceeded = v != 0
	}
	if v, _, err := key.GetIntegerValue("NotifyRepeatMinutes"); err == nil {
		repeatMin = int(v)
	}

	// Build triggers from legacy booleans.
	var triggers []Trigger
	if onTransition {
		triggers = append(triggers, TriggerDrainOn, TriggerDrainOff, TriggerGraceEntered, TriggerHealthy)
	}
	if onGraceExceeded {
		triggers = append(triggers, TriggerAlert)
	}
	if len(triggers) == 0 {
		triggers = append([]Trigger{}, DefaultTriggers...)
	}

	if webhookURL != "" {
		cfg.Notifications = append(cfg.Notifications, NotificationTarget{
			Type: "webhook", URL: webhookURL, Triggers: triggers, RepeatMinutes: repeatMin,
		})
	}
	if ntfyURL != "" {
		cfg.Notifications = append(cfg.Notifications, NotificationTarget{
			Type: "ntfy", URL: ntfyURL, Triggers: triggers, RepeatMinutes: repeatMin,
		})
	}

	// Dashboard parameters.
	if v, _, err := key.GetIntegerValue("DashboardEnabled"); err == nil {
		cfg.Dashboard.Enabled = v != 0
	}
	if v, _, err := key.GetIntegerValue("DashboardPort"); err == nil {
		cfg.Dashboard.Port = int(v)
	}
	if v, _, err := key.GetStringValue("DashboardGroup"); err == nil && v != "" {
		cfg.Dashboard.Group = v
	}
	if v, _, err := key.GetStringValue("DashboardURL"); err == nil {
		cfg.Dashboard.URL = v
	}

	cfg.Validate()

	if err := saveConfigToFile(cfg); err != nil {
		return nil, fmt.Errorf("write migrated config: %w", err)
	}

	slog.Default().Info("config migrated from registry to config.json", "path", DefaultConfigPath())
	cfg.DecryptSecrets()
	return cfg, nil
}

// ── Legacy compat (keep WriteDefaultParameters for installer transition) ─

// Installer-only; do not call from runtime code.
//
// WriteDefaultParameters creates the Parameters registry key with default
// values if it doesn't already exist. Kept for backwards compatibility
// during the transition period — new installs use config.json instead.
func WriteDefaultParameters() error {
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, ParametersKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer func() { _ = key.Close() }()

	if _, _, err := key.GetIntegerValue("GracePeriod"); err != nil {
		_ = key.SetDWordValue("GracePeriod", DefaultGracePeriod)
	}
	if _, _, err := key.GetIntegerValue("RetentionDays"); err != nil {
		_ = key.SetDWordValue("RetentionDays", DefaultRetentionDays)
	}
	if _, _, err := key.GetIntegerValue("PollInterval"); err != nil {
		_ = key.SetDWordValue("PollInterval", DefaultPollInterval)
	}
	if _, _, err := key.GetStringValue("AuditPath"); err != nil {
		_ = key.SetStringValue("AuditPath", DefaultDBPath())
	}

	if _, _, err := key.GetStringValue("WebhookURL"); err != nil {
		_ = key.SetStringValue("WebhookURL", "")
	}
	if _, _, err := key.GetStringValue("NtfyURL"); err != nil {
		_ = key.SetStringValue("NtfyURL", "")
	}
	if _, _, err := key.GetIntegerValue("NotifyOnTransition"); err != nil {
		_ = key.SetDWordValue("NotifyOnTransition", 1)
	}
	if _, _, err := key.GetIntegerValue("NotifyOnGraceExceeded"); err != nil {
		_ = key.SetDWordValue("NotifyOnGraceExceeded", 1)
	}
	if _, _, err := key.GetIntegerValue("NotifyRepeatMinutes"); err != nil {
		_ = key.SetDWordValue("NotifyRepeatMinutes", 0)
	}

	if _, _, err := key.GetIntegerValue("DashboardEnabled"); err != nil {
		_ = key.SetDWordValue("DashboardEnabled", 0)
	}
	if _, _, err := key.GetIntegerValue("DashboardPort"); err != nil {
		_ = key.SetDWordValue("DashboardPort", DefaultDashboardPort)
	}
	if _, _, err := key.GetStringValue("DashboardGroup"); err != nil {
		_ = key.SetStringValue("DashboardGroup", DefaultDashboardGroup)
	}
	if _, _, err := key.GetStringValue("DashboardURL"); err != nil {
		_ = key.SetStringValue("DashboardURL", "")
	}

	slog.Info("", "parameters", "defaults_written", "path", ParametersKeyPath)
	return nil
}
