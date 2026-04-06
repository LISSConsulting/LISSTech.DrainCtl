//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/dashboard"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/svc"
	"github.com/spf13/cobra"
)

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
			url := fmt.Sprintf("https://localhost:%d", fileCfg.Dashboard.Port)
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
			url := fmt.Sprintf("https://localhost:%d/api/v1/servers", fileCfg.Dashboard.Port)
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
			url := fmt.Sprintf("https://localhost:%d/api/v1/servers/%s", fileCfg.Dashboard.Port, args[0])
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

			// Restart the service so the dashboard listener starts.
			if err := svc.RestartService(log); err != nil {
				log(dc.LvlWRN, fmt.Sprintf("msg=%q error=%q", "service restart failed, restart manually", err))
			}
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

			// Restart the service to stop the dashboard listener.
			if err := svc.RestartService(log); err != nil {
				log(dc.LvlWRN, fmt.Sprintf("msg=%q error=%q", "service restart failed, restart manually", err))
			}
			return nil
		},
	})

	dcmd.AddCommand(&cobra.Command{
		Use:   "fingerprint",
		Short: "Show the dashboard TLS certificate fingerprint",
		RunE: func(cmd *cobra.Command, args []string) error {
			dataDir := dc.DefaultDataDir()
			fp, err := dashboard.CertFingerprint(dataDir)
			if err != nil {
				return fmt.Errorf("read certificate: %w", err)
			}
			fmt.Println(fp)
			return nil
		},
	})

	installCertCmd := &cobra.Command{
		Use:   "install-cert <cert.pem> <key.pem>",
		Short: "Install a custom TLS certificate for the dashboard",
		Long: `Copy a PEM certificate and private key into the DrainCtl data directory
and update config.json so the dashboard uses them instead of the
auto-generated self-signed certificate.

The files are copied to:
  %ProgramData%\LISS Technologies\LISSTech DrainCtl\dashboard-tls.crt
  %ProgramData%\LISS Technologies\LISSTech DrainCtl\dashboard-tls.key

Restart the service after installing a new certificate.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			log := dc.DefaultLogger(os.Stdout, cfg.Quiet)
			if err := dc.InstallCertificate(args[0], args[1], log); err != nil {
				return err
			}
			log(dc.LvlOK, "certificate installed")
			log(dc.LvlINF, "restart the DrainCtl service to use the new certificate")
			return nil
		},
	}
	dcmd.AddCommand(installCertCmd)

	return dcmd
}
