//go:build windows

package main

import (
	"os"
	"testing"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/spf13/cobra"
)

// flagsCmd builds a minimal *cobra.Command with all configure flags registered
// and parses args into it, marking each provided flag as Changed.
func flagsCmd(t *testing.T, args []string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "configure"}
	cmd.Flags().String("mode", "standalone", "")
	cmd.Flags().String("webhook-url", "", "")
	cmd.Flags().String("ntfy-url", "", "")
	cmd.Flags().String("dashboard-url", "", "")
	cmd.Flags().Int("dashboard-port", dc.DefaultDashboardPort, "")
	cmd.Flags().String("dashboard-group", dc.DefaultDashboardGroup, "")
	cmd.Flags().Int("grace-period", dc.DefaultGracePeriod, "")
	cmd.Flags().Int("session-warning-threshold", dc.DefaultSessionWarningThreshold, "")
	cmd.Flags().Int("poll-interval", dc.DefaultPollInterval, "")
	cmd.Flags().Int("retention-days", dc.DefaultRetentionDays, "")
	cmd.Flags().Bool("auto-pin", false, "")
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	return cmd
}

// ── runConfigureFlags ─────────────────────────────────────────────────────────

// TestRunConfigureFlags_GracePeriodChanged verifies that --grace-period updates
// fileCfg.GracePeriod and persists the change.
func TestRunConfigureFlags_GracePeriodChanged(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	cfg := dc.DefaultConfig()
	cmd := flagsCmd(t, []string{"--grace-period=30"})
	var lc logCapture
	if err := runConfigureFlags(cmd, cfg, lc.logFunc); err != nil {
		t.Fatalf("runConfigureFlags: %v", err)
	}
	if cfg.GracePeriod != 30 {
		t.Errorf("GracePeriod = %d, want 30", cfg.GracePeriod)
	}
	if !lc.contains("configure=done") {
		t.Errorf("expected 'configure=done' in log, got %v", lc.lines)
	}
}

// TestRunConfigureFlags_GracePeriodUnchanged verifies that when no flags are
// changed, fileCfg.GracePeriod retains its initial value.
func TestRunConfigureFlags_GracePeriodUnchanged(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	cfg := dc.DefaultConfig()
	cfg.GracePeriod = 45
	cmd := flagsCmd(t, []string{}) // no flags changed
	if err := runConfigureFlags(cmd, cfg, dc.DiscardLogger()); err != nil {
		t.Fatalf("runConfigureFlags: %v", err)
	}
	if cfg.GracePeriod != 45 {
		t.Errorf("GracePeriod = %d, want 45 (should be unchanged)", cfg.GracePeriod)
	}
}

// TestRunConfigureFlags_DashboardMode_EnablesDashboard verifies that
// --mode=dashboard sets Dashboard.Enabled to true.
func TestRunConfigureFlags_DashboardMode_EnablesDashboard(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	cfg := dc.DefaultConfig()
	cfg.Dashboard.Enabled = false
	cmd := flagsCmd(t, []string{"--mode=dashboard"})
	if err := runConfigureFlags(cmd, cfg, dc.DiscardLogger()); err != nil {
		t.Fatalf("runConfigureFlags: %v", err)
	}
	if !cfg.Dashboard.Enabled {
		t.Error("Dashboard.Enabled = false, want true after --mode=dashboard")
	}
}

// TestRunConfigureFlags_DashboardMode_SetsPort verifies that
// --mode=dashboard --dashboard-port=8443 updates Dashboard.Port.
func TestRunConfigureFlags_DashboardMode_SetsPort(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	cfg := dc.DefaultConfig()
	cmd := flagsCmd(t, []string{"--mode=dashboard", "--dashboard-port=8443"})
	if err := runConfigureFlags(cmd, cfg, dc.DiscardLogger()); err != nil {
		t.Fatalf("runConfigureFlags: %v", err)
	}
	if cfg.Dashboard.Port != 8443 {
		t.Errorf("Dashboard.Port = %d, want 8443", cfg.Dashboard.Port)
	}
}

// TestRunConfigureFlags_DashboardMode_SetsGroup verifies that
// --mode=dashboard --dashboard-group updates Dashboard.Group.
func TestRunConfigureFlags_DashboardMode_SetsGroup(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	cfg := dc.DefaultConfig()
	cmd := flagsCmd(t, []string{"--mode=dashboard", "--dashboard-group=IT Admins"})
	if err := runConfigureFlags(cmd, cfg, dc.DiscardLogger()); err != nil {
		t.Fatalf("runConfigureFlags: %v", err)
	}
	if cfg.Dashboard.Group != "IT Admins" {
		t.Errorf("Dashboard.Group = %q, want %q", cfg.Dashboard.Group, "IT Admins")
	}
}

// TestRunConfigureFlags_RegistrationMode_SetsDashboardURL verifies that
// --mode=registration --dashboard-url sets Dashboard.URL.
func TestRunConfigureFlags_RegistrationMode_SetsDashboardURL(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	cfg := dc.DefaultConfig()
	cmd := flagsCmd(t, []string{"--mode=registration", "--dashboard-url=https://dash.example.com:49470"})
	if err := runConfigureFlags(cmd, cfg, dc.DiscardLogger()); err != nil {
		t.Fatalf("runConfigureFlags: %v", err)
	}
	if cfg.Dashboard.URL != "https://dash.example.com:49470" {
		t.Errorf("Dashboard.URL = %q, want https://dash.example.com:49470", cfg.Dashboard.URL)
	}
}

// TestRunConfigureFlags_WebhookURL_UpsertTarget verifies that --webhook-url
// adds a webhook notification target to the config.
func TestRunConfigureFlags_WebhookURL_UpsertTarget(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	cfg := dc.DefaultConfig()
	cmd := flagsCmd(t, []string{"--webhook-url=https://hook.example.com/"})
	if err := runConfigureFlags(cmd, cfg, dc.DiscardLogger()); err != nil {
		t.Fatalf("runConfigureFlags: %v", err)
	}
	if len(cfg.Notifications) == 0 {
		t.Fatal("expected at least one notification target, got none")
	}
	if cfg.Notifications[0].Type != "webhook" || cfg.Notifications[0].URL != "https://hook.example.com/" {
		t.Errorf("notification target = %+v, want webhook https://hook.example.com/", cfg.Notifications[0])
	}
}

// TestRunConfigureFlags_NtfyURL_UpsertTarget verifies that --ntfy-url adds
// an ntfy notification target to the config.
func TestRunConfigureFlags_NtfyURL_UpsertTarget(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	cfg := dc.DefaultConfig()
	cmd := flagsCmd(t, []string{"--ntfy-url=https://ntfy.sh/alerts"})
	if err := runConfigureFlags(cmd, cfg, dc.DiscardLogger()); err != nil {
		t.Fatalf("runConfigureFlags: %v", err)
	}
	if len(cfg.Notifications) == 0 {
		t.Fatal("expected at least one notification target, got none")
	}
	if cfg.Notifications[0].Type != "ntfy" || cfg.Notifications[0].URL != "https://ntfy.sh/alerts" {
		t.Errorf("notification target = %+v, want ntfy https://ntfy.sh/alerts", cfg.Notifications[0])
	}
}

// TestRunConfigureFlags_AutoPin_SetsPointer verifies that --auto-pin=true sets
// Dashboard.AutoPin to a non-nil pointer with value true.
func TestRunConfigureFlags_AutoPin_SetsPointer(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	cfg := dc.DefaultConfig()
	cmd := flagsCmd(t, []string{"--auto-pin=true"})
	if err := runConfigureFlags(cmd, cfg, dc.DiscardLogger()); err != nil {
		t.Fatalf("runConfigureFlags: %v", err)
	}
	if cfg.Dashboard.AutoPin == nil {
		t.Fatal("Dashboard.AutoPin = nil, want non-nil pointer")
	}
	if !*cfg.Dashboard.AutoPin {
		t.Error("*Dashboard.AutoPin = false, want true")
	}
}

// TestRunConfigureFlags_ServiceSettings verifies that --poll-interval,
// --retention-days, and --session-warning-threshold are applied.
func TestRunConfigureFlags_ServiceSettings(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	cfg := dc.DefaultConfig()
	cmd := flagsCmd(t, []string{
		"--poll-interval=600",
		"--retention-days=180",
		"--session-warning-threshold=90",
	})
	if err := runConfigureFlags(cmd, cfg, dc.DiscardLogger()); err != nil {
		t.Fatalf("runConfigureFlags: %v", err)
	}
	if cfg.PollInterval != 600 {
		t.Errorf("PollInterval = %d, want 600", cfg.PollInterval)
	}
	if cfg.RetentionDays != 180 {
		t.Errorf("RetentionDays = %d, want 180", cfg.RetentionDays)
	}
	if cfg.SessionWarningThreshold != 90 {
		t.Errorf("SessionWarningThreshold = %d, want 90", cfg.SessionWarningThreshold)
	}
}

// TestRunConfigureFlags_SaveError_ReturnsError verifies that a SaveConfig
// failure propagates as an error. Triggered by placing a directory at the
// config.json.tmp path so os.WriteFile fails.
func TestRunConfigureFlags_SaveError_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)

	// Block the tmp write by creating a directory at that path. MkdirAll
	// also creates the parent data dir so saveConfigToFile's own MkdirAll
	// succeeds and the error is produced by os.WriteFile.
	if err := os.MkdirAll(dc.DefaultConfigPath()+".tmp", 0o755); err != nil {
		t.Fatalf("setup blocking dir: %v", err)
	}

	cfg := dc.DefaultConfig()
	cmd := flagsCmd(t, []string{"--grace-period=30"})
	if err := runConfigureFlags(cmd, cfg, dc.DiscardLogger()); err == nil {
		t.Fatal("expected SaveConfig error, got nil")
	}
}
