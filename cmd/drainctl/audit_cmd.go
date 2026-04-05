//go:build windows

package main

import (
	"os"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/spf13/cobra"
)

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
