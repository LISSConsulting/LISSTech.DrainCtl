//go:build windows

package svc

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/sys/windows/svc"
)

// ── svcStateString ────────────────────────────────────────────────────────────

func TestSvcStateString_AllNamedStates(t *testing.T) {
	tests := []struct {
		state svc.State
		want  string
	}{
		{svc.Running, "Running"},
		{svc.Stopped, "Stopped"},
		{svc.StartPending, "StartPending"},
		{svc.StopPending, "StopPending"},
		{svc.PausePending, "PausePending"},
		{svc.Paused, "Paused"},
		{svc.ContinuePending, "ContinuePending"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := svcStateString(tc.state)
			if got != tc.want {
				t.Errorf("svcStateString(%d) = %q, want %q", tc.state, got, tc.want)
			}
		})
	}
}

// TestSvcStateString_UnknownStateUsesNumericFallback verifies that an
// unrecognised state value produces "Unknown(N)" rather than an empty string
// or a panic.
func TestSvcStateString_UnknownStateUsesNumericFallback(t *testing.T) {
	unknown := svc.State(99)
	got := svcStateString(unknown)

	if !strings.HasPrefix(got, "Unknown(") {
		t.Errorf("svcStateString(%d) = %q, want 'Unknown(...)' prefix", unknown, got)
	}
	if !strings.Contains(got, "99") {
		t.Errorf("svcStateString(%d) = %q, want numeric value 99 in result", unknown, got)
	}
	// Sanity-check that fmt.Sprintf produced the expected value directly.
	want := fmt.Sprintf("Unknown(%d)", unknown)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
