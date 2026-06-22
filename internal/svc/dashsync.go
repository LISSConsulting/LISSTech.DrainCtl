//go:build windows

package svc

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/dashboard"
)

// configFetchBase is the baseline interval between dashboard notification-config
// refreshes. The actual interval grows exponentially after consecutive failures
// up to configFetchMax.
const (
	configFetchBase = 5 * time.Minute   // baseline re-fetch interval (overridden by dashboard.fetch_interval)
	configFetchMax  = 160 * time.Minute // ceiling (~maxConfigFetchInterval * 30s)
)

// backoffDuration returns the time interval to wait before the next dashboard
// config fetch attempt. base is the configured fetch interval; failures is the
// count of consecutive failures. The interval doubles per failure up to
// configFetchMax.
func backoffDuration(base time.Duration, failures int) time.Duration {
	if base <= 0 {
		base = configFetchBase
	}
	if failures <= 0 {
		return base
	}
	shift := failures
	if shift > 5 {
		shift = 5 // cap doubling at 2^5 = 32×
	}
	d := base << shift
	if d >= configFetchMax {
		return configFetchMax
	}
	return d
}

// registerWithDashboard registers this host with the dashboard and performs
// auto-pin if enabled. Returns true on success. Safe to call multiple times;
// the dashboard treats re-registration as a no-op for already-known hosts.
func registerWithDashboard(ctx context.Context, dashCfg *dc.DashboardConfig) bool {
	slog.Debug("dashboard registration attempt", "url", dashCfg.URL)
	regResult, err := dashboard.Register(ctx, dashCfg.URL)
	if err != nil {
		slog.Warn("dashboard: registration failed, will retry on next poll", "error", err)
		return false
	}
	slog.Info("dashboard=registered")
	if refuse, _ := shouldRefuseFingerprintUpdate(dashCfg.TLSFingerprint, regResult.TLSFingerprint); refuse {
		slog.Error("dashboard=fingerprint-mismatch", "saved", dashCfg.TLSFingerprint, "offered", regResult.TLSFingerprint)
		return false
	}
	// Auto-pin: save the dashboard's TLS fingerprint if enabled and we don't have one yet.
	if dashCfg.AutoPin && dashCfg.TLSFingerprint == "" && regResult.TLSFingerprint != "" {
		dashCfg.TLSFingerprint = regResult.TLSFingerprint
		dashboard.InitDashClient(dashCfg.TLSFingerprint)
		slog.Info("dashboard=auto-pinned", "fingerprint", dashCfg.TLSFingerprint)
		// Persist to config.json so pinning survives restarts.
		if fileCfg, err := dc.LoadConfig(); err == nil {
			fileCfg.Dashboard.TLSFingerprint = dashCfg.TLSFingerprint
			if err := dc.SaveConfig(fileCfg); err != nil {
				slog.Warn("dashboard: failed to save fingerprint to config", "error", err)
			}
		}
	}
	return true
}

func shouldRefuseFingerprintUpdate(saved, offered string) (bool, error) {
	if saved != "" && offered != "" && saved != offered {
		return true, fmt.Errorf("dashboard fingerprint mismatch: saved=%s  offered=%s", saved, offered)
	}
	return false, nil
}

// isLocalDashboard returns true if the dashboard URL points to this machine.
func isLocalDashboard(dashURL string) bool {
	u, err := url.Parse(dashURL)
	if err != nil {
		return false
	}
	local, err := os.Hostname()
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), local)
}
