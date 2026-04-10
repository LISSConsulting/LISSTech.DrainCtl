//go:build windows

package main

import (
	"fmt"
	"log/slog"
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
			exePath, err := os.Executable()
			if err != nil {
				return fmt.Errorf("get executable path: %w", err)
			}
			return svc.InstallService(exePath)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall the DrainCtl Windows service (requires admin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return svc.UninstallService()
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "start",
		Short: "Start the DrainCtl service (requires admin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return svc.StartService()
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "stop",
		Short: "Stop the DrainCtl service (requires admin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return svc.StopService()
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
			format, _ := getFormat(dc.FormatPlain)
			switch state {
			case "Running":
				if format == dc.FormatPlain {
					dc.PrintResult(os.Stdout, fmt.Sprintf("service=%s", state))
				}
			case "Stopped":
				slog.Warn(fmt.Sprintf("service=%s", state))
			default:
				slog.Info(fmt.Sprintf("service=%s", state))
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

	cmd.AddCommand(&cobra.Command{
		Use:    "grant-eventlog",
		Short:  "Add the service account to Event Log Readers group",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return svc.GrantEventLogAccess()
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:    "restart",
		Short:  "Restart the DrainCtl service",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return svc.RestartService()
		},
	})

	return cmd
}
