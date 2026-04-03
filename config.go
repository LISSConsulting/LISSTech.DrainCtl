//go:build windows

package drainctl

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows/registry"
)

const (
	ServiceName        = "DrainCtl"
	ServiceDisplayName = "Remote Desktop Drain Mode Monitor"
	ServiceDescription = "Monitors Remote Desktop Session Host drain mode (TSServerDrainMode) and maintains an audit trail of state changes."
	ParametersKeyPath  = `SYSTEM\CurrentControlSet\Services\DrainCtl\Parameters`

	DefaultGracePeriod   = 60 // minutes
	DefaultRetentionDays = 90
	MaxRetentionDays     = 365
	MinRetentionDays     = 1
	DefaultPollInterval  = 300 // seconds
)

// ServiceConfig holds all tunable parameters read from the registry.
type ServiceConfig struct {
	GracePeriod   time.Duration
	RetentionDays int
	PollInterval  time.Duration
	AuditPath     string
}

// ReadServiceConfig reads configuration from the service's Parameters
// registry key. Missing values get defaults. Individual read errors are
// non-fatal (use default and log a warning).
func ReadServiceConfig(log LogFunc) ServiceConfig {
	cfg := ServiceConfig{
		GracePeriod:   DefaultGracePeriod * time.Minute,
		RetentionDays: DefaultRetentionDays,
		PollInterval:  time.Duration(DefaultPollInterval) * time.Second,
		AuditPath:     DefaultAuditPath(),
	}

	key, err := registry.OpenKey(registry.LOCAL_MACHINE, ParametersKeyPath, registry.QUERY_VALUE)
	if err != nil {
		LogMsg(log, LvlWRN, "parameters key not found, using defaults", "path="+ParametersKeyPath)
		return cfg
	}
	defer func() { _ = key.Close() }()

	if v, _, err := key.GetIntegerValue("GracePeriod"); err == nil {
		cfg.GracePeriod = time.Duration(v) * time.Minute
	}
	if v, _, err := key.GetIntegerValue("RetentionDays"); err == nil {
		cfg.RetentionDays = ClampRetention(int(v), log)
	}
	if v, _, err := key.GetIntegerValue("PollInterval"); err == nil {
		cfg.PollInterval = time.Duration(v) * time.Second
	}
	if v, _, err := key.GetStringValue("AuditPath"); err == nil && v != "" {
		cfg.AuditPath = v
	}

	return cfg
}

// WriteDefaultParameters creates the Parameters registry key with default
// values if it doesn't already exist. Called during service installation.
func WriteDefaultParameters(log LogFunc) error {
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, ParametersKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer func() { _ = key.Close() }()

	// Only write defaults if values don't already exist
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

	// Notification defaults.
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

	log(LvlINF, "parameters=defaults_written", "path="+ParametersKeyPath)
	return nil
}

// ReadNotifyConfig reads notification configuration from the service's
// Parameters registry key. Missing values get defaults.
func ReadNotifyConfig(log LogFunc) NotifyConfig {
	cfg := NotifyConfig{
		OnTransition:    true,
		OnGraceExceeded: true,
		RepeatInterval:  0,
	}

	key, err := registry.OpenKey(registry.LOCAL_MACHINE, ParametersKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return cfg
	}
	defer func() { _ = key.Close() }()

	if v, _, err := key.GetStringValue("WebhookURL"); err == nil {
		cfg.WebhookURL = v
	}
	if v, _, err := key.GetStringValue("NtfyURL"); err == nil {
		cfg.NtfyURL = v
	}
	if v, _, err := key.GetIntegerValue("NotifyOnTransition"); err == nil {
		cfg.OnTransition = v != 0
	}
	if v, _, err := key.GetIntegerValue("NotifyOnGraceExceeded"); err == nil {
		cfg.OnGraceExceeded = v != 0
	}
	if v, _, err := key.GetIntegerValue("NotifyRepeatMinutes"); err == nil {
		cfg.RepeatInterval = time.Duration(v) * time.Minute
	}

	return cfg
}

// WriteNotifyConfig writes notification configuration to the service's
// Parameters registry key.
func WriteNotifyConfig(cfg NotifyConfig, log LogFunc) error {
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, ParametersKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open parameters key: %w", err)
	}
	defer func() { _ = key.Close() }()

	if err := key.SetStringValue("WebhookURL", cfg.WebhookURL); err != nil {
		return fmt.Errorf("set WebhookURL: %w", err)
	}
	if err := key.SetStringValue("NtfyURL", cfg.NtfyURL); err != nil {
		return fmt.Errorf("set NtfyURL: %w", err)
	}

	onTransition := uint32(0)
	if cfg.OnTransition {
		onTransition = 1
	}
	if err := key.SetDWordValue("NotifyOnTransition", onTransition); err != nil {
		return fmt.Errorf("set NotifyOnTransition: %w", err)
	}

	onGrace := uint32(0)
	if cfg.OnGraceExceeded {
		onGrace = 1
	}
	if err := key.SetDWordValue("NotifyOnGraceExceeded", onGrace); err != nil {
		return fmt.Errorf("set NotifyOnGraceExceeded: %w", err)
	}

	repeatMin := uint32(cfg.RepeatInterval.Minutes())
	if err := key.SetDWordValue("NotifyRepeatMinutes", repeatMin); err != nil {
		return fmt.Errorf("set NotifyRepeatMinutes: %w", err)
	}

	LogMsg(log, LvlINF, "notification config written", "path="+ParametersKeyPath)
	return nil
}

// ClampRetention enforces the retention boundary (1–365 days). Values
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
