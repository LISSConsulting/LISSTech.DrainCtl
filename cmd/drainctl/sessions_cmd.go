//go:build windows

package main

import (
	"fmt"
	"log/slog"
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
	summary := dc.ComputeSessionSummary(sessions, dc.ReadMaxSessions())

	if format == dc.FormatPlain {
		if len(sessions) == 0 {
			slog.Info("sessions=none")
			return nil
		}
		for _, s := range sessions {
			fields := []string{
				fmt.Sprintf("session_id=%d", s.SessionID),
				fmt.Sprintf("station=%s", s.Station),
				fmt.Sprintf("state=%s", s.State),
			}
			if s.UserName != "" {
				fields = append([]string{fmt.Sprintf("user=%s", s.UserName)}, fields...)
			}
			slog.Info(joinFields(fields...))
		}
		if summary != nil {
			if summary.MaxSessions > 0 {
				slog.Info("",
					"active", summary.ActiveSessions,
					"disconnected", summary.DisconnectedSessions,
					"total", fmt.Sprintf("%d/%d", summary.TotalSessions, summary.MaxSessions),
					"utilization", fmt.Sprintf("%d%%", summary.UtilizationPct),
				)
			} else {
				sessStr := fmt.Sprintf("active=%d", summary.ActiveSessions)
				if summary.DisconnectedSessions > 0 {
					sessStr += fmt.Sprintf(" disconnected=%d", summary.DisconnectedSessions)
				}
				slog.Info("sessions=" + sessStr)
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

// joinFields joins key=value fields into a single string for use as a slog message.
func joinFields(fields ...string) string {
	if len(fields) == 0 {
		return ""
	}
	result := fields[0]
	for _, f := range fields[1:] {
		result += " " + f
	}
	return result
}
