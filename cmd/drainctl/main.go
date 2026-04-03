//go:build windows

package main

import (
	"fmt"
	"os"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
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

// ── check ──────────────────────────────────────────────────────────────────

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
	if result, err := dc.CheckViaPipe(); err == nil {
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

// ── history ────────────────────────────────────────────────────────────────

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
	if histRecs, err := dc.HistoryViaPipe(limit, changesOnly); err == nil {
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

// ── audit-setup ────────────────────────────────────────────────────────────

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

// ── notify ────────────────────────────────────────────────────────────────

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
			ncfg := dc.ReadNotifyConfig(log)
			log(dc.LvlINF, fmt.Sprintf("webhook_url=%q", ncfg.WebhookURL))
			log(dc.LvlINF, fmt.Sprintf("ntfy_url=%q", ncfg.NtfyURL))
			log(dc.LvlINF, fmt.Sprintf("on_transition=%t", ncfg.OnTransition))
			log(dc.LvlINF, fmt.Sprintf("on_grace_exceeded=%t", ncfg.OnGraceExceeded))
			log(dc.LvlINF, fmt.Sprintf("repeat_interval=%s", ncfg.RepeatInterval))
			if ncfg.Enabled() {
				log(dc.LvlOK, "notifications=enabled")
			} else {
				log(dc.LvlWRN, "notifications=disabled (no backends configured)")
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
			ncfg := dc.ReadNotifyConfig(log)
			if len(args) > 0 {
				ncfg.WebhookURL = args[0]
			} else {
				ncfg.WebhookURL = ""
			}
			if err := dc.WriteNotifyConfig(ncfg, log); err != nil {
				return err
			}
			if ncfg.WebhookURL != "" {
				log(dc.LvlOK, fmt.Sprintf("webhook_url=%q", ncfg.WebhookURL))
			} else {
				log(dc.LvlINF, "webhook=disabled")
			}
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "set-ntfy [url]",
		Short: "Set ntfy URL (empty to disable)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			log := dc.DefaultLogger(os.Stdout, cfg.Quiet)
			ncfg := dc.ReadNotifyConfig(log)
			if len(args) > 0 {
				ncfg.NtfyURL = args[0]
			} else {
				ncfg.NtfyURL = ""
			}
			if err := dc.WriteNotifyConfig(ncfg, log); err != nil {
				return err
			}
			if ncfg.NtfyURL != "" {
				log(dc.LvlOK, fmt.Sprintf("ntfy_url=%q", ncfg.NtfyURL))
			} else {
				log(dc.LvlINF, "ntfy=disabled")
			}
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "test",
		Short: "Send a test notification to all configured backends",
		RunE: func(cmd *cobra.Command, args []string) error {
			log := dc.DefaultLogger(os.Stdout, cfg.Quiet)
			ncfg := dc.ReadNotifyConfig(log)
			return dc.SendTestNotification(ncfg, log)
		},
	})

	return cmd
}

// ── service ───────────────────────────────────────────────────────────────

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
			return dc.InstallService(exePath, log)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall the DrainCtl Windows service (requires admin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			log := dc.DefaultLogger(os.Stdout, false)
			return dc.UninstallService(log)
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
			return dc.RunService()
		},
	})

	return cmd
}
