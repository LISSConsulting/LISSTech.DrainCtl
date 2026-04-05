//go:build windows

package main

import (
	"fmt"
	"os"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/pipe"
	"github.com/spf13/cobra"
)

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
	if result, err := pipe.CheckViaPipe(); err == nil {
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
			if sess := result.Sessions; sess != nil {
				if sess.MaxSessions > 0 {
					log(dc.LvlINF, fmt.Sprintf("sessions=%d/%d", sess.TotalSessions, sess.MaxSessions),
						fmt.Sprintf("utilization=%d%%", sess.UtilizationPct))
				} else {
					sessStr := fmt.Sprintf("active=%d", sess.ActiveSessions)
					if sess.DisconnectedSessions > 0 {
						sessStr += fmt.Sprintf(" disconnected=%d", sess.DisconnectedSessions)
					}
					log(dc.LvlINF, "sessions="+sessStr)
				}
			}

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
