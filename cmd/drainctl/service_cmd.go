//go:build windows

package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

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
			cliPath, err := os.Executable()
			if err != nil {
				return fmt.Errorf("get executable path: %w", err)
			}
			servicePath := filepath.Join(filepath.Dir(cliPath), dc.ServiceBinaryName)
			if _, err := os.Stat(servicePath); err != nil {
				return fmt.Errorf("service binary %q not found: %w", servicePath, err)
			}
			return svc.InstallService(servicePath)
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
			switch state {
			case "Running":
				slog.Info("service status", "state", state)
			case "Stopped":
				slog.Warn("service status", "state", state)
			default:
				slog.Info("service status", "state", state)
			}
			return nil
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
