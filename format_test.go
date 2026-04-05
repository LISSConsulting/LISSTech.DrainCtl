//go:build windows

package drainctl

import (
	"testing"
	"time"
)

// ── ParseFormat ───────────────────────────────────────────────────────────────

func TestParseFormat_ValidInputs(t *testing.T) {
	cases := []struct {
		in   string
		want OutputFormat
	}{
		{"plain", FormatPlain},
		{"", FormatPlain},
		{"table", FormatTable},
		{"csv", FormatCSV},
		{"json", FormatJSON},
	}
	for _, tc := range cases {
		got, err := ParseFormat(tc.in)
		if err != nil {
			t.Errorf("ParseFormat(%q) unexpected error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ParseFormat(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseFormat_InvalidInput(t *testing.T) {
	_, err := ParseFormat("xml")
	if err == nil {
		t.Error("ParseFormat(\"xml\") expected error, got nil")
	}
}

// ── ComputeStateDurations ─────────────────────────────────────────────────────

func TestComputeStateDurations_Empty(t *testing.T) {
	if got := ComputeStateDurations(nil); got != nil {
		t.Errorf("expected nil for empty input, got %v", got)
	}
	if got := ComputeStateDurations([]AuditRecord{}); got != nil {
		t.Errorf("expected nil for empty slice, got %v", got)
	}
}

func TestComputeStateDurations_SingleRecord(t *testing.T) {
	ts := time.Now()
	recs := []AuditRecord{
		{Timestamp: ts, DrainMode: AllowAll},
	}
	durs := ComputeStateDurations(recs)
	if len(durs) != 1 {
		t.Fatalf("len = %d, want 1", len(durs))
	}
	if durs[0] != 0 {
		t.Errorf("single record duration = %d, want 0", durs[0])
	}
}

func TestComputeStateDurations_SameModeAccumulates(t *testing.T) {
	// Records are newest-first. Three Healthy records 10 s apart.
	base := time.Now()
	recs := []AuditRecord{
		{Timestamp: base.Add(20 * time.Second), DrainMode: AllowAll}, // index 0 (newest)
		{Timestamp: base.Add(10 * time.Second), DrainMode: AllowAll}, // index 1
		{Timestamp: base, DrainMode: AllowAll},                       // index 2 (oldest)
	}
	durs := ComputeStateDurations(recs)
	if len(durs) != 3 {
		t.Fatalf("len = %d, want 3", len(durs))
	}
	// Oldest record: 0 s since mode start
	if durs[2] != 0 {
		t.Errorf("oldest record duration = %d, want 0", durs[2])
	}
	// Middle: 10 s into the mode
	if durs[1] != 10 {
		t.Errorf("middle record duration = %d, want 10", durs[1])
	}
	// Newest: 20 s into the mode
	if durs[0] != 20 {
		t.Errorf("newest record duration = %d, want 20", durs[0])
	}
}

func TestComputeStateDurations_ModeTransitionResets(t *testing.T) {
	// Three records: AllowAll at t=0, transition to PreventNewLogon at t=5s,
	// still PreventNewLogon at t=15s. Records are newest-first.
	base := time.Now()
	recs := []AuditRecord{
		{Timestamp: base.Add(15 * time.Second), DrainMode: PreventNewLogon}, // index 0 (newest)
		{Timestamp: base.Add(5 * time.Second), DrainMode: PreventNewLogon},  // index 1 (transition point)
		{Timestamp: base, DrainMode: AllowAll},                              // index 2 (oldest)
	}
	durs := ComputeStateDurations(recs)
	if len(durs) != 3 {
		t.Fatalf("len = %d, want 3", len(durs))
	}
	// Oldest (AllowAll): 0 s since mode start
	if durs[2] != 0 {
		t.Errorf("oldest record duration = %d, want 0", durs[2])
	}
	// Transition point: 0 s in new mode (mode just changed here)
	if durs[1] != 0 {
		t.Errorf("transition record duration = %d, want 0", durs[1])
	}
	// Newest: 10 s since transition (15s - 5s)
	if durs[0] != 10 {
		t.Errorf("newest record duration = %d, want 10", durs[0])
	}
}

// ── AuditToHistory ────────────────────────────────────────────────────────────

func TestAuditToHistory_BasicFields(t *testing.T) {
	ts := time.Date(2025, 4, 1, 12, 0, 0, 0, time.UTC)
	dur := 42
	rec := AuditRecord{
		Timestamp:      ts,
		Host:           "SRV01",
		DrainMode:      AllowAll,
		DrainLabel:     "AllowAll",
		Changed:        true,
		ChangedBy:      "admin",
		ExitCode:       0,
		ActiveSessions: 3,
		TotalSessions:  10,
		MaxSessions:    20,
	}
	hr := AuditToHistory(rec, &dur)

	if hr.Host != "SRV01" {
		t.Errorf("Host = %q, want %q", hr.Host, "SRV01")
	}
	if hr.DrainMode != "AllowAll" {
		t.Errorf("DrainMode = %q, want %q", hr.DrainMode, "AllowAll")
	}
	if hr.DrainValue != 0 {
		t.Errorf("DrainValue = %d, want 0", hr.DrainValue)
	}
	if !hr.Changed {
		t.Error("Changed should be true")
	}
	if hr.ChangedBy != "admin" {
		t.Errorf("ChangedBy = %q, want %q", hr.ChangedBy, "admin")
	}
	if hr.StateDurationSeconds == nil || *hr.StateDurationSeconds != 42 {
		t.Errorf("StateDurationSeconds = %v, want 42", hr.StateDurationSeconds)
	}
	if hr.ActiveSessions != 3 {
		t.Errorf("ActiveSessions = %d, want 3", hr.ActiveSessions)
	}
}

func TestAuditToHistory_KeyModifiedOmittedWhenZero(t *testing.T) {
	dur := 0
	rec := AuditRecord{
		Timestamp: time.Now(),
		// KeyModified left as zero value
	}
	hr := AuditToHistory(rec, &dur)
	if hr.KeyModified != "" {
		t.Errorf("KeyModified should be empty for zero time, got %q", hr.KeyModified)
	}
}

func TestAuditToHistory_KeyModifiedIncludedWhenSet(t *testing.T) {
	dur := 0
	km := time.Date(2025, 3, 15, 8, 0, 0, 0, time.UTC)
	rec := AuditRecord{
		Timestamp:   time.Now(),
		KeyModified: km,
	}
	hr := AuditToHistory(rec, &dur)
	if hr.KeyModified == "" {
		t.Error("KeyModified should be non-empty when set")
	}
}

// ── FormatBool ────────────────────────────────────────────────────────────────

func TestFormatBool_Nil(t *testing.T) {
	if got := FormatBool(nil); got != "unknown" {
		t.Errorf("FormatBool(nil) = %q, want %q", got, "unknown")
	}
}

func TestFormatBool_True(t *testing.T) {
	b := true
	if got := FormatBool(&b); got != "true" {
		t.Errorf("FormatBool(&true) = %q, want %q", got, "true")
	}
}

func TestFormatBool_False(t *testing.T) {
	b := false
	if got := FormatBool(&b); got != "false" {
		t.Errorf("FormatBool(&false) = %q, want %q", got, "false")
	}
}

// ── or ────────────────────────────────────────────────────────────────────────

func TestOr_FirstNonEmpty(t *testing.T) {
	if got := or("a", "b"); got != "a" {
		t.Errorf("or(\"a\",\"b\") = %q, want %q", got, "a")
	}
}

func TestOr_FallsBackToSecond(t *testing.T) {
	if got := or("", "b"); got != "b" {
		t.Errorf("or(\"\",\"b\") = %q, want %q", got, "b")
	}
}

// ── joinFields ────────────────────────────────────────────────────────────────

func TestJoinFields_Empty(t *testing.T) {
	if got := joinFields(nil); got != "" {
		t.Errorf("joinFields(nil) = %q, want %q", got, "")
	}
}

func TestJoinFields_Single(t *testing.T) {
	if got := joinFields([]string{"a"}); got != "a" {
		t.Errorf("joinFields([\"a\"]) = %q, want %q", got, "a")
	}
}

func TestJoinFields_Multiple(t *testing.T) {
	if got := joinFields([]string{"a", "b", "c"}); got != "a b c" {
		t.Errorf("joinFields([\"a\",\"b\",\"c\"]) = %q, want %q", got, "a b c")
	}
}
