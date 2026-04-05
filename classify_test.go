//go:build windows

package drainctl

import (
	"strings"
	"testing"
	"time"
)

// ── ClassifyState ─────────────────────────────────────────────────────────────

func TestClassifyState_Healthy(t *testing.T) {
	status, msg, code := ClassifyState(false, 0, 30*time.Minute)
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
	status, msg, code := ClassifyState(true, 10*time.Minute, 30*time.Minute)
	if status != "Grace" {
		t.Errorf("status = %q, want Grace", status)
	}
	if code != 0 {
		t.Errorf("exitCode = %d, want 0", code)
	}
	if !strings.Contains(msg, "remaining") {
		t.Errorf("Grace message should mention remaining time, got %q", msg)
	}
}

func TestClassifyState_AlertExceedsPeriod(t *testing.T) {
	status, _, code := ClassifyState(true, 60*time.Minute, 30*time.Minute)
	if status != "Alert" {
		t.Errorf("status = %q, want Alert", status)
	}
	if code != 1 {
		t.Errorf("exitCode = %d, want 1", code)
	}
}

func TestClassifyState_GraceAtBoundary(t *testing.T) {
	// stateDur == gracePeriod: not strictly greater, so Grace.
	status, _, _ := ClassifyState(true, 30*time.Minute, 30*time.Minute)
	if status != "Grace" {
		t.Errorf("status = %q, want Grace at boundary (stateDur == gracePeriod)", status)
	}
}

func TestClassifyState_AlertJustOverBoundary(t *testing.T) {
	status, _, _ := ClassifyState(true, 30*time.Minute+time.Second, 30*time.Minute)
	if status != "Alert" {
		t.Errorf("status = %q, want Alert just past boundary", status)
	}
}

func TestClassifyState_ZeroStateDur_GivesGrace(t *testing.T) {
	// stateDur == 0 with drain active — conservatively Grace (KeyModified unknown).
	status, _, code := ClassifyState(true, 0, 30*time.Minute)
	if status != "Grace" {
		t.Errorf("status = %q, want Grace when stateDur=0", status)
	}
	if code != 0 {
		t.Errorf("exitCode = %d, want 0", code)
	}
}

func TestClassifyState_AlertMessageContainsDurations(t *testing.T) {
	_, msg, _ := ClassifyState(true, 2*time.Hour, 30*time.Minute)
	if !strings.Contains(msg, "2h0m0s") {
		t.Errorf("Alert message should contain state duration, got %q", msg)
	}
	if !strings.Contains(msg, "30m0s") {
		t.Errorf("Alert message should contain grace period, got %q", msg)
	}
}
