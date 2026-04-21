//go:build windows

package svc

import (
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/dashboard"
)

// ── applyRemoteConfig ─────────────────────────────────────────────────────────

func TestApplyRemoteConfig_SetsTargets(t *testing.T) {
	cfg := &dc.ServiceConfig{}
	targets := []dc.NotificationTarget{}
	remote := &dashboard.RemoteSettings{
		Notifications:           []dc.NotificationTarget{{Type: "webhook", URL: "https://example.com"}},
		SessionWarningThreshold: -1, // below zero — should not update
		GracePeriod:             0,  // zero — should not update
	}
	applyRemoteConfig(remote, cfg, &targets, nil)
	if len(targets) != 1 {
		t.Errorf("targets len = %d, want 1", len(targets))
	}
}

func TestApplyRemoteConfig_SessionThresholdClampsAbove100(t *testing.T) {
	cfg := &dc.ServiceConfig{SessionWarningThreshold: 80}
	targets := []dc.NotificationTarget{}
	remote := &dashboard.RemoteSettings{SessionWarningThreshold: 999, GracePeriod: 0}
	applyRemoteConfig(remote, cfg, &targets, nil)
	if cfg.SessionWarningThreshold != 100 {
		t.Errorf("SessionWarningThreshold = %d, want 100 (clamped from 999)", cfg.SessionWarningThreshold)
	}
}

func TestApplyRemoteConfig_SessionThresholdZeroAllowed(t *testing.T) {
	cfg := &dc.ServiceConfig{SessionWarningThreshold: 80}
	targets := []dc.NotificationTarget{}
	remote := &dashboard.RemoteSettings{SessionWarningThreshold: 0, GracePeriod: 0}
	applyRemoteConfig(remote, cfg, &targets, nil)
	if cfg.SessionWarningThreshold != 0 {
		t.Errorf("SessionWarningThreshold = %d, want 0 (zero disables session warnings)", cfg.SessionWarningThreshold)
	}
}

func TestApplyRemoteConfig_SessionThresholdNegativeSkipped(t *testing.T) {
	cfg := &dc.ServiceConfig{SessionWarningThreshold: 80}
	targets := []dc.NotificationTarget{}
	remote := &dashboard.RemoteSettings{SessionWarningThreshold: -1, GracePeriod: 0}
	applyRemoteConfig(remote, cfg, &targets, nil)
	if cfg.SessionWarningThreshold != 80 {
		t.Errorf("SessionWarningThreshold = %d, want 80 (negative should be skipped)", cfg.SessionWarningThreshold)
	}
}

func TestApplyRemoteConfig_GracePeriodClampsAbove1440(t *testing.T) {
	cfg := &dc.ServiceConfig{GracePeriod: 30 * time.Minute}
	targets := []dc.NotificationTarget{}
	remote := &dashboard.RemoteSettings{SessionWarningThreshold: -1, GracePeriod: 9999}
	applyRemoteConfig(remote, cfg, &targets, nil)
	if cfg.GracePeriod != 1440*time.Minute {
		t.Errorf("GracePeriod = %v, want %v (clamped from 9999 min)", cfg.GracePeriod, 1440*time.Minute)
	}
}

func TestApplyRemoteConfig_GracePeriodValidValue(t *testing.T) {
	cfg := &dc.ServiceConfig{GracePeriod: 30 * time.Minute}
	targets := []dc.NotificationTarget{}
	remote := &dashboard.RemoteSettings{SessionWarningThreshold: -1, GracePeriod: 60}
	applyRemoteConfig(remote, cfg, &targets, nil)
	if cfg.GracePeriod != 60*time.Minute {
		t.Errorf("GracePeriod = %v, want %v", cfg.GracePeriod, 60*time.Minute)
	}
}

func TestApplyRemoteConfig_GracePeriodZeroSkipped(t *testing.T) {
	cfg := &dc.ServiceConfig{GracePeriod: 30 * time.Minute}
	targets := []dc.NotificationTarget{}
	remote := &dashboard.RemoteSettings{SessionWarningThreshold: -1, GracePeriod: 0}
	applyRemoteConfig(remote, cfg, &targets, nil)
	if cfg.GracePeriod != 30*time.Minute {
		t.Errorf("GracePeriod = %v, want %v (zero should be skipped)", cfg.GracePeriod, 30*time.Minute)
	}
}

func TestApplyRemoteConfig_PollIntervalAppliedAndClamped(t *testing.T) {
	cfg := &dc.ServiceConfig{PollInterval: 60 * time.Second}
	targets := []dc.NotificationTarget{}
	// In-range value is applied verbatim.
	remote := &dashboard.RemoteSettings{PollInterval: 30}
	applyRemoteConfig(remote, cfg, &targets, nil)
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %v, want 30s", cfg.PollInterval)
	}
	// Below floor clamps to 10.
	remote = &dashboard.RemoteSettings{PollInterval: 1}
	applyRemoteConfig(remote, cfg, &targets, nil)
	if cfg.PollInterval != 10*time.Second {
		t.Errorf("PollInterval = %v, want 10s (clamped from 1)", cfg.PollInterval)
	}
	// Above ceiling clamps to MaxPollInterval.
	remote = &dashboard.RemoteSettings{PollInterval: dc.MaxPollInterval + 1000}
	applyRemoteConfig(remote, cfg, &targets, nil)
	if cfg.PollInterval != time.Duration(dc.MaxPollInterval)*time.Second {
		t.Errorf("PollInterval = %v, want %ds (clamped)", cfg.PollInterval, dc.MaxPollInterval)
	}
	// Zero is treated as "field absent" — don't overwrite.
	cfg.PollInterval = 45 * time.Second
	remote = &dashboard.RemoteSettings{PollInterval: 0}
	applyRemoteConfig(remote, cfg, &targets, nil)
	if cfg.PollInterval != 45*time.Second {
		t.Errorf("PollInterval = %v, want 45s (zero should not overwrite)", cfg.PollInterval)
	}
}

func TestApplyRemoteConfig_EvtSpikeEnabledTogglesSubsystemCfg(t *testing.T) {
	cfg := &dc.ServiceConfig{}
	targets := []dc.NotificationTarget{}
	evt := &dc.EvtSpikeConfig{Enabled: false}
	trueVal := true
	remote := &dashboard.RemoteSettings{EvtSpikeEnabled: &trueVal}
	applyRemoteConfig(remote, cfg, &targets, evt)
	if !evt.Enabled {
		t.Error("EvtSpikeEnabled not propagated: evt.Enabled still false")
	}
	// Flip back to false via explicit pointer.
	falseVal := false
	remote = &dashboard.RemoteSettings{EvtSpikeEnabled: &falseVal}
	applyRemoteConfig(remote, cfg, &targets, evt)
	if evt.Enabled {
		t.Error("EvtSpikeEnabled=false not applied: evt.Enabled still true")
	}
	// Nil pointer → do not change.
	evt.Enabled = true
	remote = &dashboard.RemoteSettings{EvtSpikeEnabled: nil}
	applyRemoteConfig(remote, cfg, &targets, evt)
	if !evt.Enabled {
		t.Error("nil EvtSpikeEnabled should preserve existing value (true)")
	}
}

// ── pruneNotifyState ──────────────────────────────────────────────────────────

func TestPruneNotifyState_RemovesStaleAlertEntry(t *testing.T) {
	state := &dc.NotifyState{
		LastAlertNotify:       map[string]time.Time{"https://old.example.com": time.Now()},
		LastSessionWarnNotify: map[string]time.Time{},
	}
	targets := []dc.NotificationTarget{
		{URL: "https://active.example.com"},
	}
	pruneNotifyState(state, targets)
	if _, ok := state.LastAlertNotify["https://old.example.com"]; ok {
		t.Error("stale alert entry should have been pruned")
	}
}

func TestPruneNotifyState_KeepsActiveAlertEntry(t *testing.T) {
	state := &dc.NotifyState{
		LastAlertNotify:       map[string]time.Time{"https://active.example.com": time.Now()},
		LastSessionWarnNotify: map[string]time.Time{},
	}
	targets := []dc.NotificationTarget{
		{URL: "https://active.example.com"},
	}
	pruneNotifyState(state, targets)
	if _, ok := state.LastAlertNotify["https://active.example.com"]; !ok {
		t.Error("active alert entry should have been kept")
	}
}

func TestPruneNotifyState_RemovesStaleSessionWarnEntry(t *testing.T) {
	state := &dc.NotifyState{
		LastAlertNotify:       map[string]time.Time{},
		LastSessionWarnNotify: map[string]time.Time{"https://old.example.com": time.Now()},
	}
	targets := []dc.NotificationTarget{
		{URL: "https://active.example.com"},
	}
	pruneNotifyState(state, targets)
	if _, ok := state.LastSessionWarnNotify["https://old.example.com"]; ok {
		t.Error("stale session_warning entry should have been pruned")
	}
}

func TestPruneNotifyState_KeepsActiveSessionWarnEntry(t *testing.T) {
	state := &dc.NotifyState{
		LastAlertNotify:       map[string]time.Time{},
		LastSessionWarnNotify: map[string]time.Time{"https://active.example.com": time.Now()},
	}
	targets := []dc.NotificationTarget{
		{URL: "https://active.example.com"},
	}
	pruneNotifyState(state, targets)
	if _, ok := state.LastSessionWarnNotify["https://active.example.com"]; !ok {
		t.Error("active session_warning entry should have been kept")
	}
}

func TestPruneNotifyState_EmptyTargets_ClearsAll(t *testing.T) {
	state := &dc.NotifyState{
		LastAlertNotify:       map[string]time.Time{"https://a.example.com": time.Now()},
		LastSessionWarnNotify: map[string]time.Time{"https://b.example.com": time.Now()},
	}
	pruneNotifyState(state, nil)
	if len(state.LastAlertNotify) != 0 {
		t.Errorf("LastAlertNotify should be empty after prune with no targets, got %d entries", len(state.LastAlertNotify))
	}
	if len(state.LastSessionWarnNotify) != 0 {
		t.Errorf("LastSessionWarnNotify should be empty after prune with no targets, got %d entries", len(state.LastSessionWarnNotify))
	}
}

func TestPruneNotifyState_SkipsBlankURLTargets(t *testing.T) {
	state := &dc.NotifyState{
		LastAlertNotify:       map[string]time.Time{"https://a.example.com": time.Now()},
		LastSessionWarnNotify: map[string]time.Time{},
	}
	// A target with an empty URL should NOT be treated as active.
	targets := []dc.NotificationTarget{{URL: ""}}
	pruneNotifyState(state, targets)
	if _, ok := state.LastAlertNotify["https://a.example.com"]; ok {
		t.Error("entry for non-blank URL should be pruned when only blank-URL targets remain")
	}
}
