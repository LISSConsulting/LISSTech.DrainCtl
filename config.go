//go:build windows

package drainctl

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/logging"
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
	DefaultAggregatorIntervalSeconds = 60
	DefaultRetentionIntervalMinutes  = 15
	DefaultPollInterval              = 60    // seconds
	MaxPollInterval                  = 86400 // seconds (1 day)
	DefaultDashboardPort             = 49470
	DefaultDashboardGroup            = "Domain Admins"
	DefaultDashboardFetchInterval    = 300 // seconds (5 minutes)

	DefaultSessionWarningThreshold = 80 // percent

	DefaultSampleInterval          = 30 // seconds
	DefaultLoadAlertDelaySec       = 60 // seconds — CPU/memory alert sustain window
	DefaultInputDelayAlertDelaySec = 90 // seconds — input delay alert sustain window

	DefaultMemoryLimitMB          = 256  // MiB — soft GOMEMLIMIT for the service process
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
	DefaultEvtSpikeCooldownMinutes          = 10
	MinEvtSpikeCooldownMinutes              = 1
	MaxEvtSpikeCooldownMinutes              = 1440
	DefaultEvtSpikeSlotMaturityObservations = 7
	MinEvtSpikeSlotMaturityObservations     = 1
	MaxEvtSpikeSlotMaturityObservations     = 100
	DefaultEvtSpikePersistIntervalSeconds   = 900
	MinEvtSpikePersistIntervalSeconds       = 60
	MaxEvtSpikePersistIntervalSeconds       = 86400
	DefaultEvtSpikeHalfLifeBuckets          = 360
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

// NotificationTarget describes a single notification endpoint.
type NotificationTarget struct {
	Type          string    `json:"type"` // "webhook", "ntfy", or "email"
	URL           string    `json:"url"`
	Triggers      []Trigger `json:"triggers"`                 // empty = DefaultTriggers
	RepeatMinutes int       `json:"repeat_minutes,omitempty"` // 0 = once only
	Secret        string    `json:"secret,omitempty"`         // HMAC-SHA256 signing secret for webhooks; SMTP password for email
	To            []string  `json:"to,omitempty"`             // email recipients
	From          string    `json:"from,omitempty"`           // email sender
	Enabled       *bool     `json:"enabled,omitempty"`        // nil or true = enabled (default); false = disabled
	Severity      string    `json:"severity,omitempty"`       // "warning" or "alert" — event_spike target wiring (FR-011a); empty defaults to "warning"
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
	LoadAlertDelaySec       int    `json:"load_alert_delay_sec"`             // seconds before CPU/memory alert fires (default: 60)
	InputDelayAlertDelaySec int    `json:"input_delay_alert_delay_sec"`      // seconds before input delay alert fires (default: 90)
	CollectRemoteFX         bool   `json:"collect_remotefx"`                 // default: false
	CollectPerSession       bool   `json:"collect_per_session"`              // default: true
	SampleIntervalSec       int    `json:"sample_interval_sec"`              // default: 30, range 10–300
}

// RetentionConfig holds per-tier retention windows for the telemetry store.
type RetentionConfig struct {
	MetricsDays int `json:"metrics_days"` // hourly-tier retention, 1–365 days (default 30)
	AuditDays   int `json:"audit_days"`   // audit-trail retention, 1–3650 days (default 365)
}

// TelemetryConfig holds background-worker cadence settings.
type TelemetryConfig struct {
	AggregatorIntervalSeconds int `json:"aggregator_interval_seconds"` // aggregator tick, default 60
	RetentionIntervalMinutes  int `json:"retention_interval_minutes"`  // retention sweep cadence, default 15
}

// EvtSpikeConfig holds event-log anomaly detection settings. Zero value is
// fully disabled; numeric fields are promoted to defaults by ClampEvtSpike.
type EvtSpikeConfig struct {
	Enabled                  bool     `json:"enabled"`
	MinCount                 int      `json:"min_count"`
	Threshold                float64  `json:"threshold"`
	CooldownMinutes          int      `json:"cooldown_minutes"`
	SlotMaturityObservations int      `json:"slot_maturity_observations"`
	PersistIntervalSeconds   int      `json:"persist_interval_seconds"`
	HalfLifeBuckets          int      `json:"half_life_buckets"`
	PriorStrength            float64  `json:"prior_strength"`
	MeanPerBucketPrior       float64  `json:"mean_per_bucket_prior"`
	BaselinePath             string   `json:"baseline_path"`
	DisabledChannels         []string `json:"disabled_channels"`
	AddedChannels            []string `json:"added_channels"`
	SecurityChannelEnabled   bool     `json:"security_channel_enabled"`
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

	Notifications []NotificationTarget `json:"notifications"`

	DashboardOnly bool          `json:"dashboard_only"` // true = run dashboard but skip local drain monitoring
	Dashboard     DashboardJSON `json:"dashboard"`

	SessionWarningThreshold int `json:"session_warning_threshold"` // 0=disabled, 1-100

	Performance PerformanceConfig `json:"performance"`
	Retention   RetentionConfig   `json:"retention"`
	Telemetry   TelemetryConfig   `json:"telemetry"`

	EvtSpike EvtSpikeConfig `json:"evtspike"`
}

// DashboardJSON holds dashboard settings in config.json.
type DashboardJSON struct {
	Enabled        bool   `json:"enabled"`
	Port           int    `json:"port"`
	Group          string `json:"group"`
	URL            string `json:"url,omitempty"`
	TLSCert        string `json:"tls_cert,omitempty"`        // path to PEM certificate file
	TLSKey         string `json:"tls_key,omitempty"`         // path to PEM private key file
	TLSFingerprint string `json:"tls_fingerprint,omitempty"` // SHA-256 cert fingerprint for pinning (agent-side)
	AutoPin        *bool  `json:"auto_pin,omitempty"`        // auto-pin dashboard cert on register (default false)
	FetchInterval  int    `json:"fetch_interval,omitempty"`  // seconds between config fetches from dashboard (default 300)
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
	Enabled        bool
	Port           int
	Group          string
	URL            string        // agent-side: dashboard URL to report to
	TLSCert        string        // path to PEM certificate file
	TLSKey         string        // path to PEM private key file
	TLSFingerprint string        // SHA-256 cert fingerprint for pinning (agent-side)
	AutoPin        bool          // auto-pin dashboard cert on register (default false)
	FetchInterval  time.Duration // interval between config fetches from dashboard
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
		AuditPath:               DefaultAuditPath(),
		Notifications:           []NotificationTarget{},
		Dashboard:               DashboardJSON{Port: DefaultDashboardPort, Group: DefaultDashboardGroup, FetchInterval: DefaultDashboardFetchInterval},
		SessionWarningThreshold: DefaultSessionWarningThreshold,
		MemoryLimitMB:           DefaultMemoryLimitMB,
		Performance:             PerformanceConfig{CollectPerSession: true},
		Retention:               RetentionConfig{MetricsDays: DefaultMetricsDays, AuditDays: DefaultAuditDays},
		Telemetry:               TelemetryConfig{AggregatorIntervalSeconds: DefaultAggregatorIntervalSeconds, RetentionIntervalMinutes: DefaultRetentionIntervalMinutes},
		EvtSpike:                EvtSpikeConfig{DisabledChannels: []string{}, AddedChannels: []string{}},
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
		Enabled:        c.Dashboard.Enabled,
		Port:           c.Dashboard.Port,
		Group:          c.Dashboard.Group,
		URL:            c.Dashboard.URL,
		TLSCert:        c.Dashboard.TLSCert,
		TLSKey:         c.Dashboard.TLSKey,
		TLSFingerprint: c.Dashboard.TLSFingerprint,
		AutoPin:        c.Dashboard.AutoPin != nil && *c.Dashboard.AutoPin,
		FetchInterval:  c.dashFetchInterval(),
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
	if c.AuditPath == "" {
		c.AuditPath = DefaultAuditPath()
	}
	if c.Dashboard.Port < 1 || c.Dashboard.Port > 65535 {
		c.Dashboard.Port = DefaultDashboardPort
	}
	c.Dashboard.Group = strings.TrimSpace(c.Dashboard.Group)
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

	// DPAPI-encrypt any plaintext secrets before writing to disk.
	for i := range c.Notifications {
		s := c.Notifications[i].Secret
		if s == "" || strings.HasPrefix(s, dpapiPrefix) {
			continue
		}
		ct, err := DPAPIEncrypt([]byte(s))
		if err != nil {
			slog.Default().Error("DPAPI encryption failed, secret will not be saved", "error", err)
			c.Notifications[i].Secret = ""
			continue
		}
		c.Notifications[i].Secret = dpapiPrefix + base64.StdEncoding.EncodeToString(ct)
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
	path := DefaultConfigPath()
	data, err := os.ReadFile(path)
	if err == nil {
		cfg := DefaultConfig()
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
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
	if err := saveConfigToFile(cfg); err != nil {
		return nil, fmt.Errorf("write default config: %w", err)
	}
	slog.Default().Info("default config written", "path", path)
	return cfg, nil
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
	path := DefaultConfigPath()
	tmpPath := path + ".tmp"

	// Ensure data directory exists.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

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

	// Marshal with indentation for human readability.
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	data = append(data, '\n')

	// Write to temp file.
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}

	// Atomic rename.
	src, _ := windows.UTF16PtrFromString(tmpPath)
	dst, _ := windows.UTF16PtrFromString(path)
	if err := windows.MoveFileEx(src, dst, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		// Fallback: plain rename (works if no other process holds the file).
		if renameErr := os.Rename(tmpPath, path); renameErr != nil {
			return fmt.Errorf("rename config: %w (movefileex: %v)", renameErr, err)
		}
	}

	// Restrict ACL after rename — config.json may contain notification secrets.
	restrictConfigACL(path)

	return nil
}

// restrictConfigACL sets the ACL on a config file to SYSTEM + Administrators +
// SERVICE (for the virtual service account). Best-effort — errors are ignored
// since the file is still functional with inherited ACLs.
func restrictConfigACL(path string) {
	// Only restrict ACLs when running as SYSTEM or an elevated admin.
	// In non-elevated contexts (tests, dev), icacls /inheritance:r would
	// lock out the current user.
	if !isElevated() {
		return
	}
	cmds := [][]string{
		{"icacls", path, "/inheritance:r"},
		{"icacls", path, "/grant", "SYSTEM:(F)"},
		{"icacls", path, "/grant", "*S-1-5-32-544:(F)"}, // Administrators
		{"icacls", path, "/grant", "*S-1-5-6:(M)"},      // SERVICE — modify (read+write)
	}
	for _, args := range cmds {
		_ = exec.Command(args[0], args[1:]...).Run()
	}
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

// ── Scoped updaters (dashboard API) ─────────────────────────────────────

// UpdateNotifications replaces the notification targets in config.json.
// This is the only way the dashboard API should modify notifications.
func UpdateNotifications(targets []NotificationTarget) error {
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg.Notifications = targets
	cfg.Validate()
	return saveConfigToFile(cfg)
}

// UpdateSessionThreshold sets the session warning threshold in config.json.
// This is the only way the dashboard API should modify the threshold.
func UpdateSessionThreshold(pct int) error {
	if pct < 0 || pct > 100 {
		return fmt.Errorf("threshold must be 0-100, got %d", pct)
	}
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg.SessionWarningThreshold = pct
	return saveConfigToFile(cfg)
}

// UpdateGracePeriod sets the grace period (minutes) in config.json.
func UpdateGracePeriod(minutes int) error {
	if minutes < 1 || minutes > 1440 {
		return fmt.Errorf("grace period must be 1-1440 minutes, got %d", minutes)
	}
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg.GracePeriod = minutes
	return saveConfigToFile(cfg)
}

// UpdatePerformanceConfig replaces the performance monitoring settings in config.json.
func UpdatePerformanceConfig(perf PerformanceConfig) error {
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg.Performance = perf
	cfg.Validate()
	return saveConfigToFile(cfg)
}

// UpdateNotifySettings atomically updates notification targets, session warning
// threshold, grace period, poll interval, and/or performance config in a single
// config load+save cycle. Any nil argument is left unchanged. This is the
// preferred API for the dashboard PUT /api/v1/settings handler.
func UpdateNotifySettings(notifications *[]NotificationTarget, sessionThreshold *int, gracePeriod *int, pollInterval *int, performance *PerformanceConfig) error {
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
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
	cfg.Validate()
	return saveConfigToFile(cfg)
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

	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg.Dashboard.TLSCert = dstCert
	cfg.Dashboard.TLSKey = dstKey
	return saveConfigToFile(cfg)
}

// ── Registry migration ──────────────────────────────────────────────────

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
		_ = key.SetStringValue("AuditPath", DefaultAuditPath())
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
