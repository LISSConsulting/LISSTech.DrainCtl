//go:build windows

package drainctl

// InstallerConfigInput carries the machine-level settings the MSI owns.
// Notification targets are intentionally excluded; they are configured
// post-install through the dashboard or CLI.
type InstallerConfigInput struct {
	Mode             string
	DashboardURL     *string
	DashboardPort    *int
	DashboardGroup   *string
	GracePeriod      *int
	PollInterval     *int
	SessionThreshold *int
	PerfEnabled      *bool
	PerfDisabled     *bool
	PerfRFX          *bool
	LogFileLevel     *string
	LogEventLevel    *string
	DashboardOnly    *bool
	MemoryLimitMB    *int
}

// ApplyInstallerConfig mutates cfg using the machine settings collected by the
// MSI or other installer-owned entrypoints. Call SaveConfig after this returns.
func ApplyInstallerConfig(cfg *Config, in InstallerConfigInput) {
	if in.GracePeriod != nil {
		cfg.GracePeriod = *in.GracePeriod
	}
	if in.PollInterval != nil {
		cfg.PollInterval = *in.PollInterval
	}
	if in.SessionThreshold != nil {
		cfg.SessionWarningThreshold = *in.SessionThreshold
	}
	if in.MemoryLimitMB != nil {
		cfg.MemoryLimitMB = *in.MemoryLimitMB
	}

	switch in.Mode {
	case "dashboard":
		cfg.Dashboard.Enabled = true
		if in.DashboardPort != nil {
			cfg.Dashboard.Port = *in.DashboardPort
		}
		if in.DashboardGroup != nil {
			cfg.Dashboard.Group = *in.DashboardGroup
		}
		if in.MemoryLimitMB == nil && cfg.MemoryLimitMB < DefaultDashboardMemoryLimitMB {
			cfg.MemoryLimitMB = DefaultDashboardMemoryLimitMB
		}
	case "registration":
		if in.DashboardURL != nil {
			cfg.Dashboard.URL = *in.DashboardURL
		}
	}

	if in.PerfEnabled != nil {
		cfg.Performance.Enabled = *in.PerfEnabled
		if *in.PerfEnabled {
			cfg.Performance.ForceDisabled = false
			if !cfg.Performance.CollectPerSession {
				cfg.Performance.CollectPerSession = true
			}
		}
	}
	if in.PerfDisabled != nil && *in.PerfDisabled {
		cfg.Performance.Enabled = false
		cfg.Performance.ForceDisabled = true
	}
	if in.PerfRFX != nil {
		cfg.Performance.CollectRemoteFX = *in.PerfRFX
	}
	if in.LogFileLevel != nil {
		cfg.LogFileLevel = *in.LogFileLevel
	}
	if in.LogEventLevel != nil {
		cfg.LogEventLevel = *in.LogEventLevel
	}
	if in.DashboardOnly != nil {
		cfg.DashboardOnly = *in.DashboardOnly
	}

	cfg.Validate()
}
