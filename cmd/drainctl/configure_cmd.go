//go:build windows

package main

import (
	"bufio"
	"fmt"
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
			log := dc.DefaultLogger(os.Stdout, cfg.Quiet)

			fileCfg, err := dc.LoadConfig(log)
			if err != nil {
				fileCfg = dc.DefaultConfig()
			}

			// No flags explicitly set → interactive wizard for operators.
			if cmd.Flags().NFlag() == 0 {
				return runConfigureInteractive(fileCfg, log)
			}
			return runConfigureFlags(cmd, fileCfg, log)
		},
	}

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

	return cmd
}

// runConfigureInteractive prompts the operator for each setting, showing the
// current value as the default.  Press Enter to keep a value unchanged.
func runConfigureInteractive(fileCfg *dc.Config, log dc.LogFunc) error {
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
	if err := dc.SaveConfig(fileCfg, log); err != nil {
		return err
	}
	log(dc.LvlOK, "configure=done mode=interactive")
	return nil
}

// runConfigureFlags applies flag-based configuration (MSI installer / scripted use).
func runConfigureFlags(cmd *cobra.Command, fileCfg *dc.Config, log dc.LogFunc) error {
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
	case "registration":
		if dashURL != "" {
			fileCfg.Dashboard.URL = dashURL
		}
	}

	if cmd.Flags().Changed("auto-pin") {
		fileCfg.Dashboard.AutoPin = &autoPin
	}

	// Upsert rather than append: running configure twice with the same URL
	// should not produce duplicate notification targets.
	if webhookURL != "" {
		fileCfg.Notifications = upsertNotifyTarget(fileCfg.Notifications, "webhook", webhookURL)
	}
	if ntfyURL != "" {
		fileCfg.Notifications = upsertNotifyTarget(fileCfg.Notifications, "ntfy", ntfyURL)
	}

	fileCfg.Validate(log)

	if err := dc.SaveConfig(fileCfg, log); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	log(dc.LvlOK, fmt.Sprintf("configure=done mode=%s", mode))
	return nil
}
