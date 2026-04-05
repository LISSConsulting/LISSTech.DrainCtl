//go:build windows

package svc

import (
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/dashboard"
)

// ── classifyState ─────────────────────────────────────────────────────────────

func TestClassifyState_Healthy(t *testing.T) {
	status, msg, code := classifyState(false, 0, 30*time.Minute)
	if status != "Healthy" {
		t.Errorf("status = %q, want Healthy", status)
	}
	if code != 0 {
		t.Errorf("exitCode = %d, want 0", code)
	}
	if msg == "" {
		t.Error("message should be non-empty")
	}
}

func TestClassifyState_GraceWithinPeriod(t *testing.T) {
	status, _, code := classifyState(true, 10*time.Minute, 30*time.Minute)
	if status != "Grace" {
		t.Errorf("status = %q, want Grace", status)
	}
	if code != 0 {
		t.Errorf("exitCode = %d, want 0", code)
	}
}

func TestClassifyState_AlertExceedsPeriod(t *testing.T) {
	status, _, code := classifyState(true, 60*time.Minute, 30*time.Minute)
	if status != "Alert" {
		t.Errorf("status = %q, want Alert", status)
	}
	if code != 1 {
		t.Errorf("exitCode = %d, want 1", code)
	}
}

func TestClassifyState_GraceAtBoundary(t *testing.T) {
	// stateDur == gracePeriod: not strictly greater, so Grace.
	status, _, _ := classifyState(true, 30*time.Minute, 30*time.Minute)
	if status != "Grace" {
		t.Errorf("status = %q, want Grace at boundary", status)
	}
}

func TestClassifyState_AlertJustOverBoundary(t *testing.T) {
	status, _, _ := classifyState(true, 30*time.Minute+time.Second, 30*time.Minute)
	if status != "Alert" {
		t.Errorf("status = %q, want Alert just past boundary", status)
	}
}

// ── applyRemoteConfig ─────────────────────────────────────────────────────────

func TestApplyRemoteConfig_SetsTargets(t *testing.T) {
	cfg := &dc.ServiceConfig{}
	targets := []dc.NotificationTarget{}
	remote := &dashboard.RemoteNotifyConfig{
		Notifications:           []dc.NotificationTarget{{Type: "webhook", URL: "https://example.com"}},
		SessionWarningThreshold: -1, // below zero — should not update
		GracePeriod:             0,  // zero — should not update
	}
	applyRemoteConfig(remote, cfg, &targets)
	if len(targets) != 1 {
		t.Errorf("targets len = %d, want 1", len(targets))
	}
}

func TestApplyRemoteConfig_SessionThresholdClampsAbove100(t *testing.T) {
	cfg := &dc.ServiceConfig{SessionWarningThreshold: 80}
	targets := []dc.NotificationTarget{}
	remote := &dashboard.RemoteNotifyConfig{SessionWarningThreshold: 999, GracePeriod: 0}
	applyRemoteConfig(remote, cfg, &targets)
	if cfg.SessionWarningThreshold != 100 {
		t.Errorf("SessionWarningThreshold = %d, want 100 (clamped from 999)", cfg.SessionWarningThreshold)
	}
}

func TestApplyRemoteConfig_SessionThresholdZeroAllowed(t *testing.T) {
	cfg := &dc.ServiceConfig{SessionWarningThreshold: 80}
	targets := []dc.NotificationTarget{}
	remote := &dashboard.RemoteNotifyConfig{SessionWarningThreshold: 0, GracePeriod: 0}
	applyRemoteConfig(remote, cfg, &targets)
	if cfg.SessionWarningThreshold != 0 {
		t.Errorf("SessionWarningThreshold = %d, want 0 (zero disables session warnings)", cfg.SessionWarningThreshold)
	}
}

func TestApplyRemoteConfig_SessionThresholdNegativeSkipped(t *testing.T) {
	cfg := &dc.ServiceConfig{SessionWarningThreshold: 80}
	targets := []dc.NotificationTarget{}
	remote := &dashboard.RemoteNotifyConfig{SessionWarningThreshold: -1, GracePeriod: 0}
	applyRemoteConfig(remote, cfg, &targets)
	if cfg.SessionWarningThreshold != 80 {
		t.Errorf("SessionWarningThreshold = %d, want 80 (negative should be skipped)", cfg.SessionWarningThreshold)
	}
}

func TestApplyRemoteConfig_GracePeriodClampsAbove1440(t *testing.T) {
	cfg := &dc.ServiceConfig{GracePeriod: 30 * time.Minute}
	targets := []dc.NotificationTarget{}
	remote := &dashboard.RemoteNotifyConfig{SessionWarningThreshold: -1, GracePeriod: 9999}
	applyRemoteConfig(remote, cfg, &targets)
	if cfg.GracePeriod != 1440*time.Minute {
		t.Errorf("GracePeriod = %v, want %v (clamped from 9999 min)", cfg.GracePeriod, 1440*time.Minute)
	}
}

func TestApplyRemoteConfig_GracePeriodValidValue(t *testing.T) {
	cfg := &dc.ServiceConfig{GracePeriod: 30 * time.Minute}
	targets := []dc.NotificationTarget{}
	remote := &dashboard.RemoteNotifyConfig{SessionWarningThreshold: -1, GracePeriod: 60}
	applyRemoteConfig(remote, cfg, &targets)
	if cfg.GracePeriod != 60*time.Minute {
		t.Errorf("GracePeriod = %v, want %v", cfg.GracePeriod, 60*time.Minute)
	}
}

func TestApplyRemoteConfig_GracePeriodZeroSkipped(t *testing.T) {
	cfg := &dc.ServiceConfig{GracePeriod: 30 * time.Minute}
	targets := []dc.NotificationTarget{}
	remote := &dashboard.RemoteNotifyConfig{SessionWarningThreshold: -1, GracePeriod: 0}
	applyRemoteConfig(remote, cfg, &targets)
	if cfg.GracePeriod != 30*time.Minute {
		t.Errorf("GracePeriod = %v, want %v (zero should be skipped)", cfg.GracePeriod, 30*time.Minute)
	}
}
