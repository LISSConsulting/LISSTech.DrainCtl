//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/dashboard"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/pipe"
	"github.com/spf13/cobra"
)

var registerWithDashboardFunc = registerThroughServiceOrDirect

func dashboardFingerprintMismatchError(saved, offered string) error {
	return errors.New(
		fmt.Sprintf("dashboard fingerprint mismatch: saved=%s  offered=%s", saved, offered) +
			"\nrefusing to overwrite. Clear Dashboard.TLSFingerprint in config.json before re-registering.",
	)
}

func registerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "register [dashboard-url]",
		Short: "Register this server with a DrainCtl dashboard",
		Long: `Register this server with a DrainCtl dashboard.

Provide a URL explicitly, or use --auto to discover the dashboard via
DNS SRV record (_drainctl._tcp.<domain>).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			auto, _ := cmd.Flags().GetBool("auto")
			pin, _ := cmd.Flags().GetBool("pin")

			var dashURL string
			if len(args) > 0 {
				dashURL = args[0]
			} else if auto {
				dashURL = dashboard.DiscoverDashboardURL()
				if dashURL == "" {
					return fmt.Errorf("no dashboard found via SRV lookup (_drainctl._tcp.<domain>)")
				}
				slog.Info(fmt.Sprintf("discovered dashboard: %s", dashURL))
			} else {
				// Fall back to URL already saved in config.json.
				existingCfg, loadErr := dc.LoadConfig()
				if loadErr == nil && existingCfg.Dashboard.URL != "" {
					dashURL = existingCfg.Dashboard.URL
					slog.Info(fmt.Sprintf("using configured dashboard: %s", dashURL))
				} else {
					return fmt.Errorf("provide a dashboard URL, use --auto for SRV discovery, or configure dashboard.url in config.json")
				}
			}

			// Persist DashboardURL to config.json.
			fileCfg, err := dc.LoadConfig()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			fileCfg.Dashboard.URL = dashURL
			if err := dc.SaveConfig(fileCfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			slog.Info(fmt.Sprintf("config=DashboardURL set to %q", dashURL))

			// Register with the dashboard. Prefer routing through the service
			// pipe so the registration call runs under the machine-account
			// identity the dashboard's requireMachineAccount middleware
			// demands. Fall back to a direct HTTP call (CLI's own token) only
			// when the service pipe is unavailable — e.g. the bootstrap case
			// where DrainCtl has just been installed and the service hasn't
			// started yet, or the caller is running as SYSTEM.
			regResult, regErr := registerWithDashboardFunc(dashURL)
			if regErr != nil {
				slog.Warn("registration failed (service will retry on next check)", "error", regErr)
				return nil
			}

			if fileCfg.Dashboard.TLSFingerprint != "" && regResult.TLSFingerprint != "" &&
				fileCfg.Dashboard.TLSFingerprint != regResult.TLSFingerprint {
				return dashboardFingerprintMismatchError(
					fileCfg.Dashboard.TLSFingerprint,
					regResult.TLSFingerprint,
				)
			}

			// Auto-pin: save the dashboard's TLS fingerprint (unless --pin=false).
			if pin && regResult.TLSFingerprint != "" && fileCfg.Dashboard.TLSFingerprint == "" {
				fileCfg.Dashboard.TLSFingerprint = regResult.TLSFingerprint
				if err := dc.SaveConfig(fileCfg); err != nil {
					slog.Warn("failed to save fingerprint", "error", err)
				} else {
					slog.Info(fmt.Sprintf("auto-pinned fingerprint=%s", regResult.TLSFingerprint))
				}
			}

			slog.Info("registered with dashboard")
			return nil
		},
	}
	cmd.Flags().Bool("auto", false, "Discover dashboard via DNS SRV record (_drainctl._tcp.<domain>)")
	cmd.Flags().Bool("pin", false, "Auto-pin the dashboard's TLS certificate fingerprint on first registration")
	return cmd
}

// registerThroughServiceOrDirect routes the dashboard registration call
// through the service pipe when the service is reachable. The service holds
// the machine-account credentials the dashboard's register endpoint requires;
// calling dashboard.Register() from a human user's CLI process yields a 401.
//
// Fallback is narrow: only when the pipe itself is unreachable (service not
// installed / not started — the bootstrap case) do we fall back to a direct
// HTTP call. Errors returned by the service over a working pipe — e.g. the
// dashboard rejected the request, the HTTPS call timed out, TLS pinning
// mismatched — are surfaced as-is. Retrying those under the caller's user
// token would either duplicate a registration the service already attempted
// or swap a genuine failure mode for a spurious 401.
func registerThroughServiceOrDirect(dashURL string) (*dashboard.RegisterResult, error) {
	raw, err := pipe.RegisterViaPipe(dashURL)
	if err != nil {
		if errors.Is(err, pipe.ErrPipeUnavailable) {
			slog.Info("register: service pipe unreachable, falling back to direct call", "error", err)
			return dashboard.Register(dashURL)
		}
		return nil, err
	}
	slog.Info("registered via service pipe")
	var res dashboard.RegisterResult
	if len(raw) == 0 {
		return &res, nil
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("decode register response: %w", err)
	}
	return &res, nil
}
