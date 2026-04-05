//go:build windows

package main

import (
	"fmt"
	"os"

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
