//go:build windows

package main

import (
	"fmt"
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
			log := dc.DefaultLogger(os.Stdout, cfg.Quiet)
			auto, _ := cmd.Flags().GetBool("auto")
			pin, _ := cmd.Flags().GetBool("pin")

			var dashURL string
			if len(args) > 0 {
				dashURL = args[0]
			} else if auto {
				dashURL = dashboard.DiscoverDashboardURL(log)
				if dashURL == "" {
					return fmt.Errorf("no dashboard found via SRV lookup (_drainctl._tcp.<domain>)")
				}
				log(dc.LvlINF, fmt.Sprintf("discovered dashboard: %s", dashURL))
			} else {
				// Fall back to URL already saved in config.json.
				existingCfg, loadErr := dc.LoadConfig(log)
				if loadErr == nil && existingCfg.Dashboard.URL != "" {
					dashURL = existingCfg.Dashboard.URL
					log(dc.LvlINF, fmt.Sprintf("using configured dashboard: %s", dashURL))
				} else {
					return fmt.Errorf("provide a dashboard URL, use --auto for SRV discovery, or configure dashboard.url in config.json")
				}
			}

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
			regResult, regErr := dashboard.Register(dashURL, log)
			if regErr != nil {
				dc.LogMsg(log, dc.LvlWRN, "registration failed (service will retry on next check)", fmt.Sprintf("error=%q", regErr))
				return nil
			}

			// Auto-pin: save the dashboard's TLS fingerprint (unless --pin=false).
			if pin && regResult.TLSFingerprint != "" && fileCfg.Dashboard.TLSFingerprint == "" {
				fileCfg.Dashboard.TLSFingerprint = regResult.TLSFingerprint
				if err := dc.SaveConfig(fileCfg, log); err != nil {
					dc.LogMsg(log, dc.LvlWRN, "failed to save fingerprint", fmt.Sprintf("error=%q", err))
				} else {
					log(dc.LvlINF, fmt.Sprintf("auto-pinned fingerprint=%s", regResult.TLSFingerprint))
				}
			}

			log(dc.LvlOK, "registered with dashboard")
			return nil
		},
	}
	cmd.Flags().Bool("auto", false, "Discover dashboard via DNS SRV record (_drainctl._tcp.<domain>)")
	cmd.Flags().Bool("pin", false, "Auto-pin the dashboard's TLS certificate fingerprint on first registration")
	return cmd
}
