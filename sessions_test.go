//go:build windows

package drainctl

import "testing"

// ── wtsStateName ──────────────────────────────────────────────────────────────

func TestWtsStateName_KnownStates(t *testing.T) {
	cases := []struct {
		state uint32
		want  string
	}{
		{wtsActive, "Active"},
		{wtsConnected, "Connected"},
		{wtsConnectQuery, "ConnectQuery"},
		{wtsShadow, "Shadow"},
		{wtsDisconnected, "Disconnected"},
		{wtsIdle, "Idle"},
		{wtsListen, "Listen"},
		{wtsReset, "Reset"},
		{wtsDown, "Down"},
		{wtsInit, "Init"},
	}
	for _, tc := range cases {
		got := wtsStateName(tc.state)
		if got != tc.want {
			t.Errorf("wtsStateName(%d) = %q, want %q", tc.state, got, tc.want)
		}
	}
}

func TestWtsStateName_Unknown(t *testing.T) {
	got := wtsStateName(99)
	if got != "Unknown(99)" {
		t.Errorf("wtsStateName(99) = %q, want %q", got, "Unknown(99)")
	}
}

// ── ComputeSessionSummary ────────────────────────────────────────────────────

func TestComputeSessionSummary_Empty(t *testing.T) {
	s := ComputeSessionSummary(nil, 0)
	if s == nil {
		t.Fatal("expected non-nil summary for empty session list")
	}
	if s.ActiveSessions != 0 || s.DisconnectedSessions != 0 || s.TotalSessions != 0 {
		t.Errorf("unexpected counts: %+v", s)
	}
	if s.UtilizationPct != 0 {
		t.Errorf("UtilizationPct = %d, want 0", s.UtilizationPct)
	}
}

func TestComputeSessionSummary_ActiveAndDisconnected(t *testing.T) {
	sessions := []SessionInfo{
		{SessionID: 1, State: "Active", StateValue: wtsActive},
		{SessionID: 2, State: "Active", StateValue: wtsActive},
		{SessionID: 3, State: "Disconnected", StateValue: wtsDisconnected},
	}
	s := ComputeSessionSummary(sessions, 10)
	if s.ActiveSessions != 2 {
		t.Errorf("ActiveSessions = %d, want 2", s.ActiveSessions)
	}
	if s.DisconnectedSessions != 1 {
		t.Errorf("DisconnectedSessions = %d, want 1", s.DisconnectedSessions)
	}
	if s.TotalSessions != 3 {
		t.Errorf("TotalSessions = %d, want 3", s.TotalSessions)
	}
	if s.MaxSessions != 10 {
		t.Errorf("MaxSessions = %d, want 10", s.MaxSessions)
	}
	if s.UtilizationPct != 30 {
		t.Errorf("UtilizationPct = %d, want 30", s.UtilizationPct)
	}
}

func TestComputeSessionSummary_IgnoresNonCountedStates(t *testing.T) {
	// Listen, Idle, etc. should not increment TotalSessions.
	sessions := []SessionInfo{
		{SessionID: 0, State: "Listen", StateValue: wtsListen},
		{SessionID: 1, State: "Idle", StateValue: wtsIdle},
		{SessionID: 2, State: "Active", StateValue: wtsActive},
	}
	s := ComputeSessionSummary(sessions, 0)
	if s.TotalSessions != 1 {
		t.Errorf("TotalSessions = %d, want 1", s.TotalSessions)
	}
	if s.ActiveSessions != 1 {
		t.Errorf("ActiveSessions = %d, want 1", s.ActiveSessions)
	}
}

func TestComputeSessionSummary_UtilizationCappedAt100(t *testing.T) {
	// More sessions than max (e.g. admin override) should cap at 100%.
	sessions := []SessionInfo{
		{SessionID: 1, StateValue: wtsActive},
		{SessionID: 2, StateValue: wtsActive},
		{SessionID: 3, StateValue: wtsActive},
	}
	s := ComputeSessionSummary(sessions, 2)
	if s.UtilizationPct != 100 {
		t.Errorf("UtilizationPct = %d, want 100", s.UtilizationPct)
	}
}

func TestComputeSessionSummary_NoMaxSessions(t *testing.T) {
	// MaxSessions == 0 means unlimited; UtilizationPct should stay 0.
	sessions := []SessionInfo{
		{SessionID: 1, StateValue: wtsActive},
	}
	s := ComputeSessionSummary(sessions, 0)
	if s.UtilizationPct != 0 {
		t.Errorf("UtilizationPct = %d, want 0 when MaxSessions=0", s.UtilizationPct)
	}
}
