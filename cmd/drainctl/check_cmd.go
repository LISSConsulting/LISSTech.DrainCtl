//go:build windows

package main

import (
	"fmt"
	"log/slog"
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
			slog.Info("", "source", "service")
			slog.Info("", "host", result.Host)
			if result.DrainModeValue == 0 {
				slog.Info("", "drain_mode", result.DrainModeLabel, "value", result.DrainModeValue)
			} else {
				slog.Warn("", "drain_mode", result.DrainModeLabel, "value", result.DrainModeValue)
			}
			if result.StateSince != nil {
				dur := time.Duration(0)
				if result.StateDurationSeconds != nil {
					dur = time.Duration(*result.StateDurationSeconds) * time.Second
				}
				slog.Info("",
					"state_since", result.StateSince.Format(time.RFC3339),
					"state_duration", dur.Truncate(time.Second).String(),
				)
			}
			slog.Info("", "grace_period", (time.Duration(result.GracePeriodSeconds) * time.Second).String())
			if sess := result.Sessions; sess != nil {
				if sess.MaxSessions > 0 {
					slog.Info("",
						"sessions", fmt.Sprintf("%d/%d", sess.TotalSessions, sess.MaxSessions),
						"utilization", fmt.Sprintf("%d%%", sess.UtilizationPct))
				} else {
					sessStr := fmt.Sprintf("active=%d", sess.ActiveSessions)
					if sess.DisconnectedSessions > 0 {
						sessStr += fmt.Sprintf(" disconnected=%d", sess.DisconnectedSessions)
					}
					slog.Info("sessions=" + sessStr)
				}
			}
			if perf := result.Performance; perf != nil {
				slog.Info("",
					"cpu", fmt.Sprintf("%.1f%%", perf.CPUPct),
					"mem_avail", fmt.Sprintf("%.0fMB/%.0fMB", perf.MemAvailMB, perf.MemTotalMB),
					"pages_sec", fmt.Sprintf("%.1f", perf.PagesSec),
					"disk_queue", fmt.Sprintf("%.2f", perf.DiskQueue),
				)
				if perf.InputDelayP50 > 0 || perf.InputDelayP95 > 0 || perf.InputDelayMax > 0 {
					slog.Info(fmt.Sprintf("input_delay p50=%.0fms p95=%.0fms max=%.0fms",
						perf.InputDelayP50, perf.InputDelayP95, perf.InputDelayMax))
				}
				if perf.SessionCPUP95 > 0 || perf.SessionMemP95 > 0 {
					slog.Info("",
						"session_cpu_p95", fmt.Sprintf("%.0f%%", perf.SessionCPUP95),
						"session_mem_p95", fmt.Sprintf("%.0fB", perf.SessionMemP95),
					)
				}
				if perf.TCPRetrans > 0 {
					slog.Info("", "tcp_retrans", fmt.Sprintf("%.1f/s", perf.TCPRetrans))
				}
				if perf.RFXAvailable {
					slog.Info(fmt.Sprintf("rfx fps=%.0f encode=%.1fms quality=%.0f%% rtt=%.0fms loss=%.1f%%",
						perf.RFXFPSOut, perf.RFXEncodeMS, perf.RFXQuality, perf.RFXRTT, perf.RFXLoss))
				}
			}

			// Final status line — goes to stdout, not the log.
			switch result.Status {
			case "Alert":
				dc.PrintResult(os.Stdout, "status=alert",
					fmt.Sprintf("connections_allowed=%s", dc.FormatBool(result.ConnectionsAllowed)),
					fmt.Sprintf("exit=%d", result.ExitCode))
			case "Grace":
				dc.PrintResult(os.Stdout, "status=grace",
					fmt.Sprintf("connections_allowed=%s", dc.FormatBool(result.ConnectionsAllowed)),
					fmt.Sprintf("exit=%d", result.ExitCode))
			default:
				dc.PrintResult(os.Stdout, "status=healthy",
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
	if format == dc.FormatPlain {
		slog.Info("", "source", "direct")
	}

	out, err := dc.Check(dc.CheckOptions{
		DBPath:        cfg.DB,
		GracePeriod:   time.Duration(cfg.Grace) * time.Minute,
		RetentionDays: dc.ClampRetention(cfg.RetentionDays),
	})
	if err != nil {
		return err
	}

	if format == dc.FormatPlain {
		// Result line already logged inside Check(); just emit the final --- line.
		switch out.Result.Status {
		case "Alert":
			dc.PrintResult(os.Stdout, "status=alert",
				fmt.Sprintf("connections_allowed=%s", dc.FormatBool(out.Result.ConnectionsAllowed)),
				fmt.Sprintf("exit=%d", out.Result.ExitCode))
		case "Grace":
			dc.PrintResult(os.Stdout, "status=grace",
				fmt.Sprintf("connections_allowed=%s", dc.FormatBool(out.Result.ConnectionsAllowed)),
				fmt.Sprintf("exit=%d", out.Result.ExitCode))
		default:
			dc.PrintResult(os.Stdout, "status=healthy",
				fmt.Sprintf("connections_allowed=%s", dc.FormatBool(out.Result.ConnectionsAllowed)),
				fmt.Sprintf("exit=%d", out.Result.ExitCode))
		}
	} else {
		out.Result.Write(os.Stdout, format)
	}

	os.Exit(out.ExitCode)
	return nil
}
