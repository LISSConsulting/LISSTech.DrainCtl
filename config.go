//go:build windows

package drainctl

import (
	"context"
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

	log(LvlINF, "parameters=defaults_written", "path="+ParametersKeyPath)
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

// WatchParametersKey monitors the service's Parameters registry key for
// changes. Sends on the returned channel when any parameter value changes.
// Used for hot-reloading configuration. Exits when ctx is cancelled.
func WatchParametersKey(ctx context.Context, log LogFunc) (<-chan struct{}, error) {
	return watchRegistryKey(ctx, ParametersKeyPath, log)
}
