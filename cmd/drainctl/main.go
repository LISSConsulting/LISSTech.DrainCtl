//go:build windows

package main

import (
	"fmt"
	"log/slog"
	"os"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/logging"
	"github.com/spf13/cobra"
)

var cfg struct {
	DB            string
	Format        string
	LogLevel      string
	Grace         int
	RetentionDays int
}

func main() {
	root := &cobra.Command{
		Use:     "drainctl",
		Short:   "Remote Desktop Session Host drain mode monitor",
		Version: dc.Version,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			level, err := logging.ParseLevel(cfg.LogLevel)
			if err != nil {
				return fmt.Errorf("invalid log level %q; valid levels: debug, info, warn, error", cfg.LogLevel)
			}
			lv := &slog.LevelVar{}
			lv.Set(level)
			handler := logging.NewCLIHandler(os.Stderr, lv)
			slog.SetDefault(slog.New(handler))
			return nil
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&cfg.DB, "db", dc.DefaultAuditPath(), "Path to audit trail file")
	pf.StringVar(&cfg.Format, "format", "", "Output format: plain, table, csv, json")
	pf.StringVar(&cfg.LogLevel, "log-level", "info", "Log verbosity: debug, info, warn, error")

	root.AddCommand(checkCmd())
	root.AddCommand(historyCmd())
	root.AddCommand(sessionsCmd())
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
