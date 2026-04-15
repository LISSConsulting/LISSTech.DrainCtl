//go:build windows

package main

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/spf13/cobra"
)

func configureCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "configure",
		Short: "Configure DrainCtl settings",
		Long: `Interactive configuration wizard when run without flags.

When flags are provided (e.g. by the MSI installer), applies them directly
and saves config.json without prompting.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fileCfg, err := dc.LoadConfig()
			if err != nil {
				slog.Warn("config unreadable; starting from defaults", "error", err)
				fileCfg = dc.DefaultConfig()
			}

			// No flags explicitly set → interactive wizard for operators.
			if cmd.Flags().NFlag() == 0 {
				return runConfigureInteractive(fileCfg)
			}
			return runConfigureFlags(cmd, fileCfg)
		},
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Show current configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			fileCfg, err := dc.LoadConfig()
			if err != nil {
				return err
			}

			slog.Info("version and config path", "version", dc.Version, "config_path", dc.DefaultConfigPath())

			// Service settings
			slog.Info("service settings",
				"grace_period_m", fileCfg.GracePeriod,
				"poll_interval_s", fileCfg.PollInterval,
				"retention_days", fileCfg.RetentionDays,
				"memory_limit_mb", fileCfg.MemoryLimitMB,
			)
			if fileCfg.SessionWarningThreshold > 0 {
				slog.Info("session warning threshold", "pct", fileCfg.SessionWarningThreshold)
			} else {
				slog.Info("session_warning_threshold=disabled")
			}

			// Dashboard server (this machine serves the dashboard)
			if fileCfg.Dashboard.Enabled {
				slog.Info("dashboard_server=enabled", "port", fileCfg.Dashboard.Port, "group", fileCfg.Dashboard.Group)
			} else {
				slog.Info("dashboard_server=disabled")
			}

			// Dashboard agent registration (this machine reports to a dashboard)
			if fileCfg.Dashboard.URL != "" {
				certNote := "not_pinned"
				if fileCfg.Dashboard.TLSFingerprint != "" {
					certNote = "pinned"
				}
				autoPin := false
				if fileCfg.Dashboard.AutoPin != nil && *fileCfg.Dashboard.AutoPin {
					autoPin = true
				}
				slog.Info("dashboard agent registration",
					"url", fileCfg.Dashboard.URL,
					"cert", certNote,
					"fingerprint", fileCfg.Dashboard.TLSFingerprint,
					"auto_pin", autoPin,
				)
			}

			// Performance monitoring
			perf := fileCfg.Performance
			if perf.ForceDisabled {
				slog.Info("performance=force_disabled")
			} else if perf.Enabled {
				slog.Info("performance=enabled",
					"cpu_warn_pct", perf.CPUWarnPct,
					"cpu_crit_pct", perf.CPUCritPct,
					"mem_warn_pct", perf.MemWarnPct,
					"mem_crit_pct", perf.MemCritPct,
					"input_delay_warn_ms", perf.InputDelayWarnMS,
					"input_delay_crit_ms", perf.InputDelayCritMS,
					"input_delay_percentile", perf.InputDelayPercentile,
					"remotefx", perf.CollectRemoteFX,
					"per_session", perf.CollectPerSession,
				)
			} else {
				slog.Info("performance=disabled")
			}

			// Notification targets
			printNotifyTargets(fileCfg.Notifications, fileCfg.HasTargets())
			return nil
		},
	})

	cmd.Flags().String("mode", "standalone", "Install mode: dashboard, registration, standalone")
	cmd.Flags().String("webhook-url", "", "Webhook notification URL")
	cmd.Flags().String("ntfy-url", "", "ntfy.sh notification URL")
	cmd.Flags().String("dashboard-url", "", "Dashboard URL for agent registration")
	cmd.Flags().Int("dashboard-port", dc.DefaultDashboardPort, "Dashboard listen port")
	cmd.Flags().String("dashboard-group", dc.DefaultDashboardGroup, "AD group for dashboard access")
	cmd.Flags().Int("grace-period", dc.DefaultGracePeriod, "Grace period in minutes (1–1440)")
	cmd.Flags().Int("session-warning-threshold", dc.DefaultSessionWarningThreshold, "Session utilization warning threshold percent (0=disabled, 1–100)")
	cmd.Flags().Int("poll-interval", dc.DefaultPollInterval, "Poll interval in seconds (≥10)")
	cmd.Flags().Int("retention-days", dc.DefaultRetentionDays, "Audit retention in days (1–365)")
	cmd.Flags().Bool("auto-pin", false, "Auto-pin dashboard TLS certificate on registration")
	cmd.Flags().Bool("perf-enabled", false, "Enable performance monitoring (PDH counters)")
	cmd.Flags().Bool("perf-disabled", false, "Force-disable performance monitoring (blocks dashboard override)")
	cmd.Flags().Bool("perf-rfx", false, "Enable RemoteFX counter collection")
	cmd.Flags().String("input-delay-percentile", "", "Input delay percentile for threshold evaluation (p50 or p95, default: p95)")
	cmd.Flags().Int("load-alert-delay", 0, "Seconds CPU/memory must breach before alert fires (default: 60)")
	cmd.Flags().Int("input-delay-alert-delay", 0, "Seconds input delay must breach before alert fires (default: 90)")
	cmd.Flags().Int("memory-limit", dc.DefaultMemoryLimitMB, "Go runtime memory limit in MiB (32–4096)")

	return cmd
}

// runConfigureInteractive prompts the operator for each setting, showing the
// current value as the default.  Press Enter to keep a value unchanged.
func runConfigureInteractive(fileCfg *dc.Config) error {
	scanner := bufio.NewScanner(os.Stdin)

	// prompt prints a labelled prompt with the current default and reads one
	// line.  An empty line returns def unchanged.
	prompt := func(label, def string) string {
		if def != "" {
			fmt.Printf("  %s [%s]: ", label, def)
		} else {
			fmt.Printf("  %s: ", label)
		}
		if !scanner.Scan() {
			return def
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			return def
		}
		return line
	}

	fmt.Println("DrainCtl Configuration Wizard")
	fmt.Printf("Config: %s\n\n", dc.DefaultConfigPath())

	// [Service]
	fmt.Println("[Service]")
	if v, err := strconv.Atoi(prompt("Grace period (minutes)", strconv.Itoa(fileCfg.GracePeriod))); err == nil {
		fileCfg.GracePeriod = v
	}
	if v, err := strconv.Atoi(prompt("Poll interval (seconds)", strconv.Itoa(fileCfg.PollInterval))); err == nil {
		fileCfg.PollInterval = v
	}
	if v, err := strconv.Atoi(prompt("Retention days", strconv.Itoa(fileCfg.RetentionDays))); err == nil {
		fileCfg.RetentionDays = v
	}
	if v, err := strconv.Atoi(prompt("Session warning threshold, 0=disabled (%)", strconv.Itoa(fileCfg.SessionWarningThreshold))); err == nil {
		fileCfg.SessionWarningThreshold = v
	}

	// [Dashboard]
	fmt.Println("\n[Dashboard]")
	dashEnabledStr := prompt("Enable dashboard (true/false)", strconv.FormatBool(fileCfg.Dashboard.Enabled))
	fileCfg.Dashboard.Enabled = dashEnabledStr == "true" || dashEnabledStr == "yes" || dashEnabledStr == "1"
	if fileCfg.Dashboard.Enabled {
		if v, err := strconv.Atoi(prompt("Dashboard port", strconv.Itoa(fileCfg.Dashboard.Port))); err == nil {
			fileCfg.Dashboard.Port = v
		}
		fileCfg.Dashboard.Group = prompt("AD group for access", fileCfg.Dashboard.Group)
	}
	fileCfg.Dashboard.URL = prompt("Agent registration URL (empty=standalone)", fileCfg.Dashboard.URL)

	// [Notifications]
	fmt.Println("\n[Notifications]")

	// Extract the first webhook and ntfy URL from existing targets.
	webhookURL, ntfyURL := "", ""
	for _, t := range fileCfg.Notifications {
		if t.Type == "webhook" && webhookURL == "" {
			webhookURL = t.URL
		}
		if t.Type == "ntfy" && ntfyURL == "" {
			ntfyURL = t.URL
		}
	}

	newWebhook := prompt("Webhook URL (empty to disable)", webhookURL)
	newNtfy := prompt("ntfy URL (empty to disable)", ntfyURL)

	// Rebuild the notification slice: update existing targets in place,
	// remove them when the URL is cleared, append new ones when added.
	var updated []dc.NotificationTarget
	webhookHandled, ntfyHandled := false, false
	for _, t := range fileCfg.Notifications {
		switch t.Type {
		case "webhook":
			if !webhookHandled {
				webhookHandled = true
				if newWebhook != "" {
					t.URL = newWebhook
					updated = append(updated, t)
				}
			}
		case "ntfy":
			if !ntfyHandled {
				ntfyHandled = true
				if newNtfy != "" {
					t.URL = newNtfy
					updated = append(updated, t)
				}
			}
		default:
			updated = append(updated, t)
		}
	}
	if !webhookHandled && newWebhook != "" {
		updated = append(updated, dc.NotificationTarget{Type: "webhook", URL: newWebhook, Triggers: dc.DefaultTriggers})
	}
	if !ntfyHandled && newNtfy != "" {
		updated = append(updated, dc.NotificationTarget{Type: "ntfy", URL: newNtfy, Triggers: dc.DefaultTriggers})
	}
	fileCfg.Notifications = updated

	fmt.Println()
	if err := dc.SaveConfig(fileCfg); err != nil {
		return err
	}
	dc.PrintResult(os.Stdout, "configure=done", "mode", "interactive")
	return nil
}

// runConfigureFlags applies flag-based configuration (MSI installer / scripted use).
func runConfigureFlags(cmd *cobra.Command, fileCfg *dc.Config) error {
	mode, _ := cmd.Flags().GetString("mode")
	webhookURL, _ := cmd.Flags().GetString("webhook-url")
	ntfyURL, _ := cmd.Flags().GetString("ntfy-url")
	dashURL, _ := cmd.Flags().GetString("dashboard-url")
	dashPort, _ := cmd.Flags().GetInt("dashboard-port")
	dashGroup, _ := cmd.Flags().GetString("dashboard-group")
	grace, _ := cmd.Flags().GetInt("grace-period")
	sessionThreshold, _ := cmd.Flags().GetInt("session-warning-threshold")
	pollInterval, _ := cmd.Flags().GetInt("poll-interval")
	retentionDays, _ := cmd.Flags().GetInt("retention-days")
	autoPin, _ := cmd.Flags().GetBool("auto-pin")

	if cmd.Flags().Changed("grace-period") {
		fileCfg.GracePeriod = grace
	}
	if cmd.Flags().Changed("session-warning-threshold") {
		fileCfg.SessionWarningThreshold = sessionThreshold
	}
	if cmd.Flags().Changed("poll-interval") {
		fileCfg.PollInterval = pollInterval
	}
	if cmd.Flags().Changed("retention-days") {
		fileCfg.RetentionDays = retentionDays
	}

	switch mode {
	case "dashboard":
		fileCfg.Dashboard.Enabled = true
		if cmd.Flags().Changed("dashboard-port") {
			fileCfg.Dashboard.Port = dashPort
		}
		if cmd.Flags().Changed("dashboard-group") {
			fileCfg.Dashboard.Group = dashGroup
		}
		// Dashboard servers need more headroom for HTTP/TLS and multi-server state.
		if !cmd.Flags().Changed("memory-limit") && fileCfg.MemoryLimitMB < dc.DefaultDashboardMemoryLimitMB {
			fileCfg.MemoryLimitMB = dc.DefaultDashboardMemoryLimitMB
		}
	case "registration":
		if dashURL != "" {
			fileCfg.Dashboard.URL = dashURL
		}
	}

	if cmd.Flags().Changed("memory-limit") {
		memLimit, _ := cmd.Flags().GetInt("memory-limit")
		fileCfg.MemoryLimitMB = memLimit
	}

	if cmd.Flags().Changed("auto-pin") {
		fileCfg.Dashboard.AutoPin = &autoPin
	}

	if cmd.Flags().Changed("perf-enabled") {
		perfEnabled, _ := cmd.Flags().GetBool("perf-enabled")
		fileCfg.Performance.Enabled = perfEnabled
		if perfEnabled {
			fileCfg.Performance.ForceDisabled = false
			if !fileCfg.Performance.CollectPerSession {
				fileCfg.Performance.CollectPerSession = true // default on
			}
		}
	}
	if cmd.Flags().Changed("perf-disabled") {
		perfDisabled, _ := cmd.Flags().GetBool("perf-disabled")
		if perfDisabled {
			fileCfg.Performance.Enabled = false
			fileCfg.Performance.ForceDisabled = true
		}
	}
	if cmd.Flags().Changed("perf-rfx") {
		perfRFX, _ := cmd.Flags().GetBool("perf-rfx")
		fileCfg.Performance.CollectRemoteFX = perfRFX
	}
	if cmd.Flags().Changed("input-delay-percentile") {
		pct, _ := cmd.Flags().GetString("input-delay-percentile")
		fileCfg.Performance.InputDelayPercentile = pct
	}
	if cmd.Flags().Changed("load-alert-delay") {
		v, _ := cmd.Flags().GetInt("load-alert-delay")
		fileCfg.Performance.LoadAlertDelaySec = v
	}
	if cmd.Flags().Changed("input-delay-alert-delay") {
		v, _ := cmd.Flags().GetInt("input-delay-alert-delay")
		fileCfg.Performance.InputDelayAlertDelaySec = v
	}

	// Upsert rather than append: running configure twice with the same URL
	// should not produce duplicate notification targets.
	if webhookURL != "" {
		fileCfg.Notifications = upsertNotifyTarget(fileCfg.Notifications, "webhook", webhookURL)
	}
	if ntfyURL != "" {
		fileCfg.Notifications = upsertNotifyTarget(fileCfg.Notifications, "ntfy", ntfyURL)
	}

	fileCfg.Validate()

	if err := dc.SaveConfig(fileCfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	dc.PrintResult(os.Stdout, "configure=done", fmt.Sprintf("mode=%s", mode))
	return nil
}
