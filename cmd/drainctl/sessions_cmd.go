//go:build windows

package main

import (
	"fmt"
	"os"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/spf13/cobra"
)

func sessionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sessions",
		Short: "List current RDS sessions on this server",
		RunE:  runSessions,
	}
}

func runSessions(_ *cobra.Command, _ []string) error {
	format, err := getFormat(dc.FormatTable)
	if err != nil {
		return err
	}

	sessions, err := dc.EnumerateSessions()
	if err != nil {
		return fmt.Errorf("enumerate sessions: %w", err)
	}
	summary := dc.GetSessionSummary()

	if format == dc.FormatPlain {
		log := dc.DefaultLogger(os.Stdout, cfg.Quiet)
		if len(sessions) == 0 {
			log(dc.LvlINF, "sessions=none")
			return nil
		}
		for _, s := range sessions {
			fields := []string{
				fmt.Sprintf("session_id=%d", s.SessionID),
				fmt.Sprintf("station=%s", s.Station),
				fmt.Sprintf("state=%s", s.State),
			}
			if s.UserName != "" {
				// prepend user field
				fields = append([]string{fmt.Sprintf("user=%s", s.UserName)}, fields...)
			}
			log(dc.LvlINF, fields...)
		}
		if summary != nil {
			if summary.MaxSessions > 0 {
				log(dc.LvlINF,
					fmt.Sprintf("active=%d", summary.ActiveSessions),
					fmt.Sprintf("disconnected=%d", summary.DisconnectedSessions),
					fmt.Sprintf("total=%d/%d", summary.TotalSessions, summary.MaxSessions),
					fmt.Sprintf("utilization=%d%%", summary.UtilizationPct),
				)
			} else {
				sessStr := fmt.Sprintf("active=%d", summary.ActiveSessions)
				if summary.DisconnectedSessions > 0 {
					sessStr += fmt.Sprintf(" disconnected=%d", summary.DisconnectedSessions)
				}
				log(dc.LvlINF, "sessions="+sessStr)
			}
		}
		return nil
	}

	if len(sessions) == 0 && format == dc.FormatTable {
		fmt.Println("No active sessions.")
		return nil
	}

	dc.WriteSessions(os.Stdout, sessions, summary, format)
	return nil
}
