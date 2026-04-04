//go:build windows

package drainctl

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

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
	DefaultPollInterval   = 300 // seconds
	DefaultDashboardPort  = 49470
	DefaultDashboardGroup = "Domain Admins"

	DefaultSessionWarningThreshold = 80 // percent

	configMutexName = `Global\DrainCtlConfig`
)

// ── Trigger type ──────────────────────────────────────────────────────────

// Trigger represents a granular notification event type.
type Trigger string

const (
	TriggerDrainOn        Trigger = "drain_on"
	TriggerDrainOff       Trigger = "drain_off"
	TriggerGraceEntered   Trigger = "grace_entered"
	TriggerAlert          Trigger = "alert"
	TriggerHealthy        Trigger = "healthy"
	TriggerSessionWarning Trigger = "session_warning"
)

// DefaultTriggers is used when a target specifies no triggers.
var DefaultTriggers = []Trigger{TriggerDrainOn, TriggerDrainOff, TriggerAlert, TriggerHealthy}

// ValidTriggers is the set of all recognized trigger names.
var ValidTriggers = map[Trigger]bool{
	TriggerDrainOn: true, TriggerDrainOff: true,
	TriggerGraceEntered: true, TriggerAlert: true,
	TriggerHealthy: true, TriggerSessionWarning: true,
}

// ── NotificationTarget ───────────────────────────────────────────────────

// NotificationTarget describes a single notification endpoint.
type NotificationTarget struct {
	Type          string    `json:"type"` // "webhook" or "ntfy"
	URL           string    `json:"url"`
	Triggers      []Trigger `json:"triggers"`                 // empty = DefaultTriggers
	RepeatMinutes int       `json:"repeat_minutes,omitempty"` // 0 = once only
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

// Config is the top-level config file structure (config.json).
type Config struct {
	GracePeriod   int    `json:"grace_period"` // minutes
	RetentionDays int    `json:"retention_days"`
	PollInterval  int    `json:"poll_interval"` // seconds
	AuditPath     string `json:"audit_path"`

	Notifications []NotificationTarget `json:"notifications"`

	Dashboard DashboardJSON `json:"dashboard"`

	SessionWarningThreshold int `json:"session_warning_threshold"` // 0=disabled, 1-100
}

// DashboardJSON holds dashboard settings in config.json.
type DashboardJSON struct {
	Enabled bool   `json:"enabled"`
	Port    int    `json:"port"`
	Group   string `json:"group"`
	URL     string `json:"url,omitempty"`
	TLSCert string `json:"tls_cert,omitempty"` // path to PEM certificate file
	TLSKey  string `json:"tls_key,omitempty"`  // path to PEM private key file
}

// ── Runtime config structs (converted from Config) ──────────────────────

// ServiceConfig holds runtime service parameters with parsed durations.
type ServiceConfig struct {
	GracePeriod             time.Duration
	RetentionDays           int
	PollInterval            time.Duration
	AuditPath               string
	SessionWarningThreshold int
}

// DashboardConfig holds runtime dashboard parameters.
type DashboardConfig struct {
	Enabled bool
	Port    int
	Group   string
	URL     string // agent-side: dashboard URL to report to
	TLSCert string // path to PEM certificate file
	TLSKey  string // path to PEM private key file
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
		Dashboard:               DashboardJSON{Port: DefaultDashboardPort, Group: DefaultDashboardGroup},
		SessionWarningThreshold: DefaultSessionWarningThreshold,
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
	}
}

// ToDashboardConfig converts the JSON config to runtime DashboardConfig.
func (c *Config) ToDashboardConfig() DashboardConfig {
	return DashboardConfig{
		Enabled: c.Dashboard.Enabled,
		Port:    c.Dashboard.Port,
		Group:   c.Dashboard.Group,
		URL:     c.Dashboard.URL,
		TLSCert: c.Dashboard.TLSCert,
		TLSKey:  c.Dashboard.TLSKey,
	}
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

// Validate clamps and corrects config values in place.
func (c *Config) Validate(log LogFunc) {
	c.RetentionDays = ClampRetention(c.RetentionDays, log)

	if c.GracePeriod < 1 {
		c.GracePeriod = DefaultGracePeriod
	}
	if c.PollInterval < 10 {
		c.PollInterval = DefaultPollInterval
	}
	if c.AuditPath == "" {
		c.AuditPath = DefaultAuditPath()
	}
	if c.Dashboard.Port == 0 {
		c.Dashboard.Port = DefaultDashboardPort
	}
	if c.Dashboard.Group == "" {
		c.Dashboard.Group = DefaultDashboardGroup
	}
	if c.SessionWarningThreshold < 0 {
		c.SessionWarningThreshold = 0
	}
	if c.SessionWarningThreshold > 100 {
		c.SessionWarningThreshold = 100
	}

	// Default empty triggers to DefaultTriggers, strip invalid trigger names.
	for i := range c.Notifications {
		if len(c.Notifications[i].Triggers) == 0 {
			c.Notifications[i].Triggers = append([]Trigger{}, DefaultTriggers...)
		} else {
			c.Notifications[i].Triggers = slices.DeleteFunc(c.Notifications[i].Triggers, func(tr Trigger) bool {
				if !ValidTriggers[tr] {
					if log != nil {
						LogMsg(log, LvlWRN, "unknown trigger ignored", fmt.Sprintf("trigger=%s url=%s", tr, c.Notifications[i].URL))
					}
					return true
				}
				return false
			})
		}
	}
}

// ClampRetention enforces the retention boundary (1-365 days). Values
// outside the range are clamped and a warning is logged. Pass nil for log
// to clamp silently.
func ClampRetention(days int, log LogFunc) int {
	if days < MinRetentionDays {
		if log != nil {
			LogMsg(log, LvlWRN, "retention below minimum, clamping",
				fmt.Sprintf("requested=%d min=%d", days, MinRetentionDays))
		}
		return MinRetentionDays
	}
	if days > MaxRetentionDays {
		if log != nil {
			LogMsg(log, LvlWRN, "retention exceeds maximum, clamping",
				fmt.Sprintf("requested=%d max=%d", days, MaxRetentionDays))
		}
		return MaxRetentionDays
	}
	return days
}

// ── Load / Save ─────────────────────────────────────────────────────────

// LoadConfig reads config.json from the default path. If the file does not
// exist it attempts a one-time migration from registry values. If the
// registry has no config either, a default config is written and returned.
func LoadConfig(log LogFunc) (*Config, error) {
	path := DefaultConfigPath()
	data, err := os.ReadFile(path)
	if err == nil {
		cfg := DefaultConfig()
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		cfg.Validate(log)
		return cfg, nil
	}

	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	// File does not exist — try migration from registry.
	if cfg, migErr := MigrateFromRegistry(log); migErr == nil {
		return cfg, nil
	}

	// No registry config either — fresh install.
	cfg := DefaultConfig()
	if err := saveConfigToFile(cfg, log); err != nil {
		return nil, fmt.Errorf("write default config: %w", err)
	}
	LogMsg(log, LvlINF, "default config written", "path="+path)
	return cfg, nil
}

// SaveConfig writes the full config to disk. Only admin/CLI/service callers
// should use this. Dashboard API should use scoped updaters instead.
func SaveConfig(cfg *Config, log LogFunc) error {
	cfg.Validate(log)
	return saveConfigToFile(cfg, log)
}

// saveConfigToFile atomically writes config.json using a named mutex for
// cross-process serialization.
func saveConfigToFile(cfg *Config, log LogFunc) error {
	path := DefaultConfigPath()
	tmpPath := path + ".tmp"

	// Ensure data directory exists.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	// Acquire cross-process mutex.
	mutexName, _ := windows.UTF16PtrFromString(configMutexName)
	mutex, err := windows.CreateMutex(nil, false, mutexName)
	if err != nil {
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
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
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

	return nil
}

// ── Scoped updaters (dashboard API) ─────────────────────────────────────

// UpdateNotifications replaces the notification targets in config.json.
// This is the only way the dashboard API should modify notifications.
func UpdateNotifications(targets []NotificationTarget, log LogFunc) error {
	cfg, err := LoadConfig(log)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg.Notifications = targets
	cfg.Validate(log)
	return saveConfigToFile(cfg, log)
}

// UpdateSessionThreshold sets the session warning threshold in config.json.
// This is the only way the dashboard API should modify the threshold.
func UpdateSessionThreshold(pct int, log LogFunc) error {
	if pct < 0 || pct > 100 {
		return fmt.Errorf("threshold must be 0-100, got %d", pct)
	}
	cfg, err := LoadConfig(log)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg.SessionWarningThreshold = pct
	return saveConfigToFile(cfg, log)
}

// ── Registry migration ──────────────────────────────────────────────────

// MigrateFromRegistry reads the old registry-based config and writes it to
// config.json. Returns the migrated config. Called automatically by
// LoadConfig when config.json does not exist.
func MigrateFromRegistry(log LogFunc) (*Config, error) {
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

	cfg.Validate(log)

	if err := saveConfigToFile(cfg, log); err != nil {
		return nil, fmt.Errorf("write migrated config: %w", err)
	}

	LogMsg(log, LvlINF, "config migrated from registry to config.json", "path="+DefaultConfigPath())
	return cfg, nil
}

// ── Legacy compat (keep WriteDefaultParameters for installer transition) ─

// WriteDefaultParameters creates the Parameters registry key with default
// values if it doesn't already exist. Kept for backwards compatibility
// during the transition period — new installs use config.json instead.
func WriteDefaultParameters(log LogFunc) error {
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

	log(LvlINF, "parameters=defaults_written", "path="+ParametersKeyPath)
	return nil
}
