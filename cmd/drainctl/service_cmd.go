//go:build windows

package main

import (
	"fmt"
	"os"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/svc"
	"github.com/spf13/cobra"
)

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
		Short: "Start the DrainCtl service (requires admin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			log := dc.DefaultLogger(os.Stdout, false)
			return svc.StartService(log)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "stop",
		Short: "Stop the DrainCtl service (requires admin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			log := dc.DefaultLogger(os.Stdout, false)
			return svc.StopService(log)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show the current state of the DrainCtl service",
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := svc.ServiceStatus()
			if err != nil {
				return err
			}
			log := dc.DefaultLogger(os.Stdout, cfg.Quiet)
			switch state {
			case "Running":
				log(dc.LvlOK, fmt.Sprintf("service=%s", state))
			case "Stopped":
				log(dc.LvlWRN, fmt.Sprintf("service=%s", state))
			default:
				log(dc.LvlINF, fmt.Sprintf("service=%s", state))
			}
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
