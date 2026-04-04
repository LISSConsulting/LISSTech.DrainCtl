//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/dashboard"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/pipe"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/svc"
	"github.com/spf13/cobra"
)

var cfg struct {
	DB            string
	Format        string
	Quiet         bool
	Grace         int
	RetentionDays int
}

func main() {
	root := &cobra.Command{
		Use:     "drainctl",
		Short:   "Remote Desktop Session Host drain mode monitor",
		Version: dc.Version,
	}

	pf := root.PersistentFlags()
	pf.StringVar(&cfg.DB, "db", dc.DefaultAuditPath(), "Path to audit trail file")
	pf.StringVar(&cfg.Format, "format", "", "Output format: plain, table, csv, json")
	pf.BoolVar(&cfg.Quiet, "quiet", false, "Suppress log output, only emit final status")

	root.AddCommand(checkCmd())
	root.AddCommand(historyCmd())
	root.AddCommand(auditSetupCmd())
	root.AddCommand(serviceCmd())
	root.AddCommand(notifyCmd())
	root.AddCommand(registerCmd())
	root.AddCommand(dashboardCmd())
	root.AddCommand(configureCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "drainctl: %v\n", err)
		os.Exit(2)
	}
}

func getFormat(defaultFmt dc.OutputFormat) (dc.OutputFormat, error) {
	if cfg.Format == "" {
		return defaultFmt, nil
	}
	return dc.ParseFormat(cfg.Format)
}

// -- check ------------------------------------------------------------------

func checkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check current drain mode state (N-central monitoring entry point)",
		RunE:  runCheck,
	}

	cmd.Flags().IntVar(&cfg.Grace, "grace", 60, "Minutes drain mode must persist before alerting")
	cmd.Flags().IntVar(&cfg.RetentionDays, "retention", 90, "Days to retain audit records")

	return cmd
}

func runCheck(cmd *cobra.Command, args []string) error {
	format, err := getFormat(dc.FormatPlain)
	if err != nil {
		return err
	}

	// Try the service pipe first.
	if result, err := pipe.CheckViaPipe(); err == nil {
		if format == dc.FormatPlain {
			log := dc.DefaultLogger(os.Stdout, cfg.Quiet)
			log(dc.LvlINF, "source=service")
			log(dc.LvlINF, fmt.Sprintf("host=%s", result.Host))
			if result.DrainModeValue == 0 {
				log(dc.LvlINF, fmt.Sprintf("drain_mode=%s", result.DrainModeLabel), fmt.Sprintf("value=%d", result.DrainModeValue))
			} else {
				log(dc.LvlWRN, fmt.Sprintf("drain_mode=%s", result.DrainModeLabel), fmt.Sprintf("value=%d", result.DrainModeValue))
			}
			if result.StateSince != nil {
				dur := time.Duration(0)
				if result.StateDurationSeconds != nil {
					dur = time.Duration(*result.StateDurationSeconds) * time.Second
				}
				log(dc.LvlINF,
					fmt.Sprintf("state_since=%s", result.StateSince.Format(time.RFC3339)),
					fmt.Sprintf("state_duration=%s", dur.Truncate(time.Second)),
				)
			}
			log(dc.LvlINF, fmt.Sprintf("grace_period=%s", (time.Duration(result.GracePeriodSeconds)*time.Second).String()))

			// Final status line
			switch result.Status {
			case "Alert":
				log(dc.LvlERR, fmt.Sprintf("status=%s", result.Status),
					fmt.Sprintf("connections_allowed=%s", dc.FormatBool(result.ConnectionsAllowed)),
					fmt.Sprintf("exit=%d", result.ExitCode))
			case "Grace":
				log(dc.LvlWRN, fmt.Sprintf("status=%s", result.Status),
					fmt.Sprintf("connections_allowed=%s", dc.FormatBool(result.ConnectionsAllowed)),
					fmt.Sprintf("exit=%d", result.ExitCode))
			default:
				log(dc.LvlOK, fmt.Sprintf("status=%s", result.Status),
					fmt.Sprintf("connections_allowed=%s", dc.FormatBool(result.ConnectionsAllowed)),
					fmt.Sprintf("exit=%d", result.ExitCode))
			}
		} else {
			result.Write(os.Stdout, format)
		}
		os.Exit(result.ExitCode)
		return nil
	}

	// Fallback: direct registry read + file-based audit.
	var log dc.LogFunc
	if format == dc.FormatPlain {
		log = dc.DefaultLogger(os.Stdout, cfg.Quiet)
	} else {
		log = dc.DiscardLogger()
	}

	out, err := dc.Check(dc.CheckOptions{
		DBPath:        cfg.DB,
		GracePeriod:   time.Duration(cfg.Grace) * time.Minute,
		RetentionDays: dc.ClampRetention(cfg.RetentionDays, log),
		Log:           log,
	})
	if err != nil {
		return err
	}

	if format != dc.FormatPlain {
		out.Result.Write(os.Stdout, format)
	}

	os.Exit(out.ExitCode)
	return nil
}

// -- history ----------------------------------------------------------------

func historyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Display audit trail",
		RunE:  runHistory,
	}

	cmd.Flags().Int("limit", 50, "Maximum records to display")
	cmd.Flags().Bool("changes-only", false, "Show only state transitions")

	return cmd
}

func runHistory(cmd *cobra.Command, args []string) error {
	format, err := getFormat(dc.FormatTable)
	if err != nil {
		return err
	}

	limit, _ := cmd.Flags().GetInt("limit")
	changesOnly, _ := cmd.Flags().GetBool("changes-only")

	// Try the service pipe first.
	if histRecs, err := pipe.HistoryViaPipe(limit, changesOnly); err == nil {
		if len(histRecs) == 0 {
			fmt.Println("No records found.")
			return nil
		}
		dc.WriteHistoryRecords(os.Stdout, histRecs, format)
		return nil
	}

	// Fallback: direct file-based audit store.
	records, err := dc.GetHistory(dc.HistoryOptions{
		DBPath:      cfg.DB,
		Limit:       limit,
		ChangesOnly: changesOnly,
	})
	if err != nil {
		return fmt.Errorf("query history: %w", err)
	}

	if len(records) == 0 {
		fmt.Println("No records found.")
		return nil
	}

	dc.WriteHistory(os.Stdout, records, format)
	return nil
}

// -- audit-setup ------------------------------------------------------------

func auditSetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "audit-setup",
		Short: "Configure registry auditing SACL for change attribution (run once, requires admin)",
		Long: `Enables Object Access auditing and sets a SACL on the Terminal Server
registry key so that Windows records Event ID 4657 whenever TSServerDrainMode
is modified. This allows drainctl to attribute changes to specific users.

Must be run elevated (as Administrator or SYSTEM).

On domain-joined machines, Group Policy may override the local auditpol
settings on the next GP refresh (~90 minutes). See the warnings emitted
by this command for the GPO path to configure.`,
		RunE: runAuditSetup,
	}
}

func runAuditSetup(cmd *cobra.Command, args []string) error {
	return dc.RunAuditSetup(dc.DefaultLogger(os.Stdout, cfg.Quiet))
}

// -- notify -----------------------------------------------------------------

func notifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Manage notification settings",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show current notification configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			log := dc.DefaultLogger(os.Stdout, cfg.Quiet)
			fileCfg, err := dc.LoadConfig(log)
			if err != nil {
				return err
			}
			if len(fileCfg.Notifications) == 0 {
				log(dc.LvlWRN, "notifications=disabled (no targets configured)")
				return nil
			}
			for i, t := range fileCfg.Notifications {
				triggers := make([]string, len(t.Triggers))
				for j, tr := range t.Triggers {
					triggers[j] = string(tr)
				}
				log(dc.LvlINF, fmt.Sprintf("target[%d] type=%s url=%q triggers=[%s] repeat_minutes=%d",
					i, t.Type, t.URL, strings.Join(triggers, ","), t.RepeatMinutes))
			}
			if fileCfg.HasTargets() {
				log(dc.LvlOK, "notifications=enabled")
			} else {
				log(dc.LvlWRN, "notifications=disabled (no targets with URLs configured)")
			}
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "set-webhook [url]",
		Short: "Set webhook URL (empty to disable)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			log := dc.DefaultLogger(os.Stdout, cfg.Quiet)
			fileCfg, err := dc.LoadConfig(log)
			if err != nil {
				return err
			}

			url := ""
			if len(args) > 0 {
				url = args[0]
			}

			if url == "" {
				// Remove all webhook targets.
				filtered := fileCfg.Notifications[:0]
				for _, t := range fileCfg.Notifications {
					if t.Type != "webhook" {
						filtered = append(filtered, t)
					}
				}
				fileCfg.Notifications = filtered
				if err := dc.SaveConfig(fileCfg, log); err != nil {
					return err
				}
				log(dc.LvlINF, "webhook=disabled")
				return nil
			}

			// Find first webhook target or create one.
			found := false
			for i := range fileCfg.Notifications {
				if fileCfg.Notifications[i].Type == "webhook" {
					fileCfg.Notifications[i].URL = url
					found = true
					break
				}
			}
			if !found {
				fileCfg.Notifications = append(fileCfg.Notifications, dc.NotificationTarget{
					Type: "webhook",
					URL:  url,
				})
			}

			if err := dc.SaveConfig(fileCfg, log); err != nil {
				return err
			}
			log(dc.LvlOK, fmt.Sprintf("webhook_url=%q", url))
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "set-ntfy [url]",
		Short: "Set ntfy URL (empty to disable)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			log := dc.DefaultLogger(os.Stdout, cfg.Quiet)
			fileCfg, err := dc.LoadConfig(log)
			if err != nil {
				return err
			}

			url := ""
			if len(args) > 0 {
				url = args[0]
			}

			if url == "" {
				// Remove all ntfy targets.
				filtered := fileCfg.Notifications[:0]
				for _, t := range fileCfg.Notifications {
					if t.Type != "ntfy" {
						filtered = append(filtered, t)
					}
				}
				fileCfg.Notifications = filtered
				if err := dc.SaveConfig(fileCfg, log); err != nil {
					return err
				}
				log(dc.LvlINF, "ntfy=disabled")
				return nil
			}

			// Find first ntfy target or create one.
			found := false
			for i := range fileCfg.Notifications {
				if fileCfg.Notifications[i].Type == "ntfy" {
					fileCfg.Notifications[i].URL = url
					found = true
					break
				}
			}
			if !found {
				fileCfg.Notifications = append(fileCfg.Notifications, dc.NotificationTarget{
					Type: "ntfy",
					URL:  url,
				})
			}

			if err := dc.SaveConfig(fileCfg, log); err != nil {
				return err
			}
			log(dc.LvlOK, fmt.Sprintf("ntfy_url=%q", url))
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "test",
		Short: "Send a test notification to all configured backends",
		RunE: func(cmd *cobra.Command, args []string) error {
			log := dc.DefaultLogger(os.Stdout, cfg.Quiet)
			fileCfg, err := dc.LoadConfig(log)
			if err != nil {
				return err
			}
			return dc.SendTestNotification(fileCfg.Notifications, log)
		},
	})

	return cmd
}

// -- service ----------------------------------------------------------------

func serviceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Manage the DrainCtl Windows service",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Install DrainCtl as a Windows service (requires admin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			log := dc.DefaultLogger(os.Stdout, false)
			exePath, err := os.Executable()
			if err != nil {
				return fmt.Errorf("get executable path: %w", err)
			}
			return svc.InstallService(exePath, log)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall the DrainCtl Windows service (requires admin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			log := dc.DefaultLogger(os.Stdout, false)
			return svc.UninstallService(log)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "start",
		Short: "Start the DrainCtl service",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Starting service... (use 'sc start DrainCtl' or 'Start-Service DrainCtl')")
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "stop",
		Short: "Stop the DrainCtl service",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Stopping service... (use 'sc stop DrainCtl' or 'Stop-Service DrainCtl')")
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:    "run",
		Short:  "Run as a Windows service (invoked by SCM)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return svc.RunService()
		},
	})

	return cmd
}

// -- register ---------------------------------------------------------------

func registerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "register <dashboard-url>",
		Short: "Register this server with a DrainCtl dashboard",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			log := dc.DefaultLogger(os.Stdout, cfg.Quiet)
			dashURL := args[0]

			// Persist DashboardURL to config.json.
			fileCfg, err := dc.LoadConfig(log)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			fileCfg.Dashboard.URL = dashURL
			if err := dc.SaveConfig(fileCfg, log); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			log(dc.LvlINF, fmt.Sprintf("config=DashboardURL set to %q", dashURL))

			// Register with the dashboard.
			if err := dashboard.Register(dashURL, log); err != nil {
				dc.LogMsg(log, dc.LvlWRN, "registration failed (service will retry on next check)", fmt.Sprintf("error=%q", err))
				return nil
			}
			log(dc.LvlOK, "registered with dashboard")
			return nil
		},
	}
}

// -- dashboard --------------------------------------------------------------

func dashboardCmd() *cobra.Command {
	dcmd := &cobra.Command{
		Use:   "dashboard",
		Short: "Open the DrainCtl dashboard in your browser",
		RunE: func(cmd *cobra.Command, args []string) error {
			fileCfg, err := dc.LoadConfig(dc.DiscardLogger())
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if !fileCfg.Dashboard.Enabled {
				fmt.Println("Dashboard is not enabled on this server.")
				fmt.Println("Enable it with: drainctl dashboard enable")
				fmt.Println("Or edit config.json and set dashboard.enabled = true")
				return nil
			}
			url := fmt.Sprintf("http://localhost:%d", fileCfg.Dashboard.Port)
			fmt.Printf("Opening %s ...\n", url)
			return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
		},
	}

	dcmd.AddCommand(&cobra.Command{
		Use:   "list-servers",
		Short: "List registered servers",
		RunE: func(cmd *cobra.Command, args []string) error {
			fileCfg, err := dc.LoadConfig(dc.DiscardLogger())
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if !fileCfg.Dashboard.Enabled {
				return fmt.Errorf("dashboard not enabled on this server")
			}
			url := fmt.Sprintf("http://localhost:%d/api/v1/servers", fileCfg.Dashboard.Port)
			resp, err := dashboard.FetchServers(url)
			if err != nil {
				return fmt.Errorf("fetch servers: %w", err)
			}
			if len(resp) == 0 {
				fmt.Println("No registered servers.")
				return nil
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		},
	})

	dcmd.AddCommand(&cobra.Command{
		Use:   "remove-server <hostname>",
		Short: "Remove a server from the dashboard",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fileCfg, err := dc.LoadConfig(dc.DiscardLogger())
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if !fileCfg.Dashboard.Enabled {
				return fmt.Errorf("dashboard not enabled on this server")
			}
			url := fmt.Sprintf("http://localhost:%d/api/v1/servers/%s", fileCfg.Dashboard.Port, args[0])
			if err := dashboard.RemoveServer(url); err != nil {
				return fmt.Errorf("remove server: %w", err)
			}
			fmt.Printf("Server %s removed.\n", args[0])
			return nil
		},
	})

	enableCmd := &cobra.Command{
		Use:   "enable",
		Short: "Enable the dashboard on this server",
		RunE: func(cmd *cobra.Command, args []string) error {
			log := dc.DefaultLogger(os.Stdout, cfg.Quiet)
			port, _ := cmd.Flags().GetInt("port")
			group, _ := cmd.Flags().GetString("group")

			fileCfg, err := dc.LoadConfig(log)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			fileCfg.Dashboard.Enabled = true
			fileCfg.Dashboard.Port = port
			fileCfg.Dashboard.Group = group
			if err := dc.SaveConfig(fileCfg, log); err != nil {
				return fmt.Errorf("save config: %w", err)
			}

			log(dc.LvlOK, fmt.Sprintf("dashboard=enabled port=%d group=%q", port, group))
			log(dc.LvlINF, "Restart the DrainCtl service to activate: Restart-Service DrainCtl")
			return nil
		},
	}
	enableCmd.Flags().Int("port", dc.DefaultDashboardPort, "Dashboard port")
	enableCmd.Flags().String("group", dc.DefaultDashboardGroup, "AD group for dashboard access")
	dcmd.AddCommand(enableCmd)

	dcmd.AddCommand(&cobra.Command{
		Use:   "disable",
		Short: "Disable the dashboard on this server",
		RunE: func(cmd *cobra.Command, args []string) error {
			log := dc.DefaultLogger(os.Stdout, cfg.Quiet)
			fileCfg, err := dc.LoadConfig(log)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			fileCfg.Dashboard.Enabled = false
			if err := dc.SaveConfig(fileCfg, log); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			log(dc.LvlOK, "dashboard=disabled")
			log(dc.LvlINF, "Restart the DrainCtl service to apply: Restart-Service DrainCtl")
			return nil
		},
	})

	return dcmd
}

// -- configure ---------------------------------------------------------------

func configureCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "configure",
		Short:  "Write config.json from flags (used by MSI installer)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			log := dc.DefaultLogger(os.Stdout, cfg.Quiet)

			fileCfg, err := dc.LoadConfig(log)
			if err != nil {
				// If config doesn't exist yet, start with defaults.
				fileCfg = &dc.Config{}
				fileCfg.Validate(log)
			}

			mode, _ := cmd.Flags().GetString("mode")
			webhookURL, _ := cmd.Flags().GetString("webhook-url")
			ntfyURL, _ := cmd.Flags().GetString("ntfy-url")
			dashURL, _ := cmd.Flags().GetString("dashboard-url")
			dashPort, _ := cmd.Flags().GetInt("dashboard-port")
			dashGroup, _ := cmd.Flags().GetString("dashboard-group")
			grace, _ := cmd.Flags().GetInt("grace-period")

			if cmd.Flags().Changed("grace-period") {
				fileCfg.GracePeriod = grace
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

			// Add notification targets from flags (only if URLs are provided).
			if webhookURL != "" {
				fileCfg.Notifications = append(fileCfg.Notifications, dc.NotificationTarget{
					Type:     "webhook",
					URL:      webhookURL,
					Triggers: dc.DefaultTriggers,
				})
			}
			if ntfyURL != "" {
				fileCfg.Notifications = append(fileCfg.Notifications, dc.NotificationTarget{
					Type:     "ntfy",
					URL:      ntfyURL,
					Triggers: dc.DefaultTriggers,
				})
			}

			fileCfg.Validate(log)

			if err := dc.SaveConfig(fileCfg, log); err != nil {
				return fmt.Errorf("save config: %w", err)
			}

			log(dc.LvlOK, fmt.Sprintf("configure=done mode=%s", mode))
			return nil
		},
	}

	cmd.Flags().String("mode", "standalone", "Install mode: dashboard, registration, standalone")
	cmd.Flags().String("webhook-url", "", "Webhook notification URL")
	cmd.Flags().String("ntfy-url", "", "ntfy.sh notification URL")
	cmd.Flags().String("dashboard-url", "", "Dashboard URL for agent registration")
	cmd.Flags().Int("dashboard-port", dc.DefaultDashboardPort, "Dashboard listen port")
	cmd.Flags().String("dashboard-group", dc.DefaultDashboardGroup, "AD group for dashboard access")
	cmd.Flags().Int("grace-period", 60, "Grace period in minutes")

	return cmd
}
