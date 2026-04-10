//go:build windows

package main

import (
	"fmt"
	"log/slog"
	"os"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/dashboard"
	"github.com/spf13/cobra"
)

func registerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "register [dashboard-url]",
		Short: "Register this server with a DrainCtl dashboard",
		Long: `Register this server with a DrainCtl dashboard.

Provide a URL explicitly, or use --auto to discover the dashboard via
DNS SRV record (_drainctl._tcp.<domain>).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			auto, _ := cmd.Flags().GetBool("auto")
			pin, _ := cmd.Flags().GetBool("pin")

			var dashURL string
			if len(args) > 0 {
				dashURL = args[0]
			} else if auto {
				dashURL = dashboard.DiscoverDashboardURL()
				if dashURL == "" {
					return fmt.Errorf("no dashboard found via SRV lookup (_drainctl._tcp.<domain>)")
				}
				slog.Info(fmt.Sprintf("discovered dashboard: %s", dashURL))
			} else {
				// Fall back to URL already saved in config.json.
				existingCfg, loadErr := dc.LoadConfig()
				if loadErr == nil && existingCfg.Dashboard.URL != "" {
					dashURL = existingCfg.Dashboard.URL
					slog.Info(fmt.Sprintf("using configured dashboard: %s", dashURL))
				} else {
					return fmt.Errorf("provide a dashboard URL, use --auto for SRV discovery, or configure dashboard.url in config.json")
				}
			}

			// Persist DashboardURL to config.json.
			fileCfg, err := dc.LoadConfig()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			fileCfg.Dashboard.URL = dashURL
			if err := dc.SaveConfig(fileCfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			slog.Info(fmt.Sprintf("config=DashboardURL set to %q", dashURL))

			// Register with the dashboard.
			regResult, regErr := dashboard.Register(dashURL)
			if regErr != nil {
				slog.Warn("registration failed (service will retry on next check)", "error", regErr)
				return nil
			}

			// Auto-pin: save the dashboard's TLS fingerprint (unless --pin=false).
			if pin && regResult.TLSFingerprint != "" && fileCfg.Dashboard.TLSFingerprint == "" {
				fileCfg.Dashboard.TLSFingerprint = regResult.TLSFingerprint
				if err := dc.SaveConfig(fileCfg); err != nil {
					slog.Warn("failed to save fingerprint", "error", err)
				} else {
					slog.Info(fmt.Sprintf("auto-pinned fingerprint=%s", regResult.TLSFingerprint))
				}
			}

			if format, _ := getFormat(dc.FormatPlain); format == dc.FormatPlain {
				dc.PrintResult(os.Stdout, "registered with dashboard")
			}
			return nil
		},
	}
	cmd.Flags().Bool("auto", false, "Discover dashboard via DNS SRV record (_drainctl._tcp.<domain>)")
	cmd.Flags().Bool("pin", false, "Auto-pin the dashboard's TLS certificate fingerprint on first registration")
	return cmd
}
