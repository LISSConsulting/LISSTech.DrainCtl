//go:build windows

package drainctl

import (
	"bytes"
	"encoding/json"
	"strings"
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

// ── WriteHistory ──────────────────────────────────────────────────────────────

func makeAuditRecords() []AuditRecord {
	ts := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	return []AuditRecord{
		{
			Timestamp:  ts.Add(time.Minute),
			Host:       "SRV01",
			DrainMode:  PreventNewLogon,
			DrainLabel: "PreventNewLogon",
			Changed:    true,
			ChangedBy:  "admin",
			ExitCode:   1,
		},
		{
			Timestamp:  ts,
			Host:       "SRV01",
			DrainMode:  AllowAll,
			DrainLabel: "AllowAll",
			Changed:    false,
			ExitCode:   0,
		},
	}
}

func TestWriteHistory_JSON(t *testing.T) {
	var buf bytes.Buffer
	WriteHistory(&buf, makeAuditRecords(), FormatJSON)
	out := buf.String()
	if !strings.Contains(out, `"PreventNewLogon"`) {
		t.Errorf("JSON output missing drain_mode: %s", out)
	}
	if !strings.Contains(out, `"changed": true`) {
		t.Errorf("JSON output missing changed field: %s", out)
	}
	if !strings.Contains(out, `"admin"`) {
		t.Errorf("JSON output missing changed_by: %s", out)
	}
	// Verify it's valid JSON.
	var records []HistoryRecord
	if err := json.Unmarshal([]byte(strings.TrimSuffix(out, "\n")), &records); err != nil {
		t.Fatalf("WriteHistory JSON is not valid JSON: %v\n%s", err, out)
	}
	if len(records) != 2 {
		t.Errorf("JSON record count = %d, want 2", len(records))
	}
}

func TestWriteHistory_CSV(t *testing.T) {
	var buf bytes.Buffer
	WriteHistory(&buf, makeAuditRecords(), FormatCSV)
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 3 { // header + 2 records
		t.Fatalf("CSV line count = %d, want ≥ 3; output:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "timestamp") {
		t.Errorf("CSV missing header; first line: %q", lines[0])
	}
	if !strings.Contains(out, "PreventNewLogon") {
		t.Errorf("CSV missing drain mode: %s", out)
	}
	if !strings.Contains(out, "admin") {
		t.Errorf("CSV missing changed_by: %s", out)
	}
}

func TestWriteHistory_Table(t *testing.T) {
	var buf bytes.Buffer
	WriteHistory(&buf, makeAuditRecords(), FormatTable)
	out := buf.String()
	if !strings.Contains(out, "DRAIN MODE") {
		t.Errorf("table missing DRAIN MODE column header: %s", out)
	}
	if !strings.Contains(out, "STATE DURATION") {
		t.Errorf("table missing STATE DURATION column header: %s", out)
	}
	if !strings.Contains(out, "PreventNewLogon") {
		t.Errorf("table missing drain mode value: %s", out)
	}
	if !strings.Contains(out, "YES") {
		t.Errorf("table missing YES for changed record: %s", out)
	}
	if !strings.Contains(out, "admin") {
		t.Errorf("table missing changed_by: %s", out)
	}
}

func TestWriteHistory_Plain(t *testing.T) {
	var buf bytes.Buffer
	WriteHistory(&buf, makeAuditRecords(), FormatPlain)
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("plain line count = %d, want 2; output:\n%s", len(lines), out)
	}
	// First record is drain-active → exit=1 → ERR level.
	if !strings.Contains(lines[0], "[ERR]") {
		t.Errorf("alert record should have ERR level: %s", lines[0])
	}
	if !strings.Contains(lines[0], "drain_mode=PreventNewLogon") {
		t.Errorf("plain missing drain_mode: %s", lines[0])
	}
	if !strings.Contains(lines[0], "changed=true") {
		t.Errorf("plain missing changed=true: %s", lines[0])
	}
	if !strings.Contains(lines[0], "changed_by=admin") {
		t.Errorf("plain missing changed_by: %s", lines[0])
	}
	// Second record is healthy → exit=0 → INF level.
	if !strings.Contains(lines[1], "[INF]") {
		t.Errorf("healthy record should have INF level: %s", lines[1])
	}
}

func TestWriteHistory_Empty(t *testing.T) {
	for _, fmt := range []OutputFormat{FormatJSON, FormatCSV, FormatTable, FormatPlain} {
		var buf bytes.Buffer
		WriteHistory(&buf, nil, fmt)
		// No panic — that's all we require.
		_ = buf.String()
	}
}

// ── WriteHistoryRecords ───────────────────────────────────────────────────────

func makeHistoryRecords() []HistoryRecord {
	dur0, dur60 := 0, 60
	return []HistoryRecord{
		{
			Timestamp:            "2026-04-01T10:01:00+00:00",
			Host:                 "SRV01",
			DrainMode:            "PreventNewLogon",
			DrainValue:           1,
			StateDurationSeconds: &dur0,
			Changed:              true,
			ChangedBy:            "operator",
			ExitCode:             1,
		},
		{
			Timestamp:            "2026-04-01T10:00:00+00:00",
			Host:                 "SRV01",
			DrainMode:            "AllowAll",
			DrainValue:           0,
			StateDurationSeconds: &dur60,
			Changed:              false,
			ExitCode:             0,
		},
	}
}

func TestWriteHistoryRecords_JSON(t *testing.T) {
	var buf bytes.Buffer
	WriteHistoryRecords(&buf, makeHistoryRecords(), FormatJSON)
	out := buf.String()
	if !strings.Contains(out, `"PreventNewLogon"`) {
		t.Errorf("JSON missing drain_mode: %s", out)
	}
	if !strings.Contains(out, `"operator"`) {
		t.Errorf("JSON missing changed_by: %s", out)
	}
	var records []HistoryRecord
	if err := json.Unmarshal([]byte(strings.TrimSuffix(out, "\n")), &records); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if len(records) != 2 {
		t.Errorf("record count = %d, want 2", len(records))
	}
}

func TestWriteHistoryRecords_CSV(t *testing.T) {
	var buf bytes.Buffer
	WriteHistoryRecords(&buf, makeHistoryRecords(), FormatCSV)
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("CSV line count = %d, want ≥ 3; output:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "timestamp") {
		t.Errorf("CSV missing header; first line: %q", lines[0])
	}
	if !strings.Contains(out, "PreventNewLogon") {
		t.Errorf("CSV missing drain mode: %s", out)
	}
	if !strings.Contains(out, "operator") {
		t.Errorf("CSV missing changed_by: %s", out)
	}
}

func TestWriteHistoryRecords_Table(t *testing.T) {
	var buf bytes.Buffer
	WriteHistoryRecords(&buf, makeHistoryRecords(), FormatTable)
	out := buf.String()
	if !strings.Contains(out, "DRAIN MODE") {
		t.Errorf("table missing DRAIN MODE column: %s", out)
	}
	if !strings.Contains(out, "STATE DURATION") {
		t.Errorf("table missing STATE DURATION column: %s", out)
	}
	if !strings.Contains(out, "PreventNewLogon") {
		t.Errorf("table missing drain mode value: %s", out)
	}
	if !strings.Contains(out, "YES") {
		t.Errorf("table missing YES for changed record: %s", out)
	}
	if !strings.Contains(out, "operator") {
		t.Errorf("table missing changed_by: %s", out)
	}
	// Timestamp must be reformatted from RFC3339 to local datetime (no "T" separator).
	if strings.Contains(out, "2026-04-01T10:01:00") {
		t.Errorf("table timestamp should be reformatted (no RFC3339 T separator): %s", out)
	}
	if !strings.Contains(out, "2026-04-01") {
		t.Errorf("table missing date portion of reformatted timestamp: %s", out)
	}
}

func TestWriteHistoryRecords_Plain(t *testing.T) {
	var buf bytes.Buffer
	WriteHistoryRecords(&buf, makeHistoryRecords(), FormatPlain)
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("plain line count = %d, want 2; output:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "drain_mode=PreventNewLogon") {
		t.Errorf("plain missing drain_mode: %s", lines[0])
	}
	if !strings.Contains(lines[0], "changed=true") {
		t.Errorf("plain missing changed=true: %s", lines[0])
	}
	if !strings.Contains(lines[0], "changed_by=operator") {
		t.Errorf("plain missing changed_by: %s", lines[0])
	}
}

func TestWriteHistoryRecords_Plain_ErrorLevel(t *testing.T) {
	var buf bytes.Buffer
	WriteHistoryRecords(&buf, makeHistoryRecords(), FormatPlain)
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// First record: exit_code=1 → ERR level.
	if !strings.Contains(lines[0], "[ERR]") {
		t.Errorf("non-zero exit record should have ERR level: %s", lines[0])
	}
	// Second record: exit_code=0 → INF level.
	if !strings.Contains(lines[1], "[INF]") {
		t.Errorf("zero-exit record should have INF level: %s", lines[1])
	}
}

// ── CheckResult.Write ─────────────────────────────────────────────────────────

func makeCheckResult() *CheckResult {
	ts := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	stateSince := ts.Add(-5 * time.Minute)
	dur := 300.0
	connAllowed := false
	return &CheckResult{
		Version:              "26.91.0",
		Timestamp:            ts,
		Host:                 "SRV01",
		DrainModeLabel:       "PreventNewLogon",
		DrainModeValue:       1,
		GracePeriodSeconds:   120,
		StateSince:           &stateSince,
		StateDurationSeconds: &dur,
		Status:               "Alert",
		ConnectionsAllowed:   &connAllowed,
		Transition:           true,
		TransitionFrom:       "AllowAll",
		ChangedBy:            "admin",
		Message:              "Drain mode active for 5m0s, exceeding grace period.",
		ExitCode:             1,
	}
}

func TestCheckResultWrite_JSON(t *testing.T) {
	var buf bytes.Buffer
	makeCheckResult().Write(&buf, FormatJSON)
	out := buf.String()
	if !strings.Contains(out, `"Alert"`) {
		t.Errorf("JSON missing status: %s", out)
	}
	if !strings.Contains(out, `"SRV01"`) {
		t.Errorf("JSON missing host: %s", out)
	}
	if !strings.Contains(out, `"PreventNewLogon"`) {
		t.Errorf("JSON missing drain_mode: %s", out)
	}
	if !strings.Contains(out, `"admin"`) {
		t.Errorf("JSON missing changed_by: %s", out)
	}
	var result CheckResult
	if err := json.Unmarshal([]byte(strings.TrimSuffix(out, "\n")), &result); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if result.Status != "Alert" {
		t.Errorf("status = %q, want Alert", result.Status)
	}
	if result.ExitCode != 1 {
		t.Errorf("exit_code = %d, want 1", result.ExitCode)
	}
}

func TestCheckResultWrite_CSV(t *testing.T) {
	var buf bytes.Buffer
	makeCheckResult().Write(&buf, FormatCSV)
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("CSV line count = %d, want ≥ 2; output:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "timestamp") {
		t.Errorf("CSV missing header; first line: %q", lines[0])
	}
	if !strings.Contains(out, "Alert") {
		t.Errorf("CSV missing status value: %s", out)
	}
	if !strings.Contains(out, "SRV01") {
		t.Errorf("CSV missing host: %s", out)
	}
	if !strings.Contains(out, "admin") {
		t.Errorf("CSV missing changed_by: %s", out)
	}
	// Session columns present in header (even when Sessions is nil).
	if !strings.Contains(lines[0], "active_sessions") {
		t.Errorf("CSV missing active_sessions header: %s", lines[0])
	}
	if !strings.Contains(lines[0], "max_sessions") {
		t.Errorf("CSV missing max_sessions header: %s", lines[0])
	}
}

func TestCheckResultWrite_CSV_WithSessions(t *testing.T) {
	r := makeCheckResult()
	r.Sessions = &SessionSummary{
		ActiveSessions:       3,
		DisconnectedSessions: 1,
		TotalSessions:        4,
		MaxSessions:          10,
	}
	var buf bytes.Buffer
	r.Write(&buf, FormatCSV)
	out := buf.String()
	if !strings.Contains(out, ",3,") {
		t.Errorf("CSV missing active_sessions value (3): %s", out)
	}
	if !strings.Contains(out, ",1,") {
		t.Errorf("CSV missing disconnected_sessions value (1): %s", out)
	}
	if !strings.Contains(out, ",10") {
		t.Errorf("CSV missing max_sessions value (10): %s", out)
	}
}

func TestCheckResultWrite_CSV_NilSessionsAreEmpty(t *testing.T) {
	r := makeCheckResult() // Sessions is nil by default
	var buf bytes.Buffer
	r.Write(&buf, FormatCSV)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("CSV line count = %d, want ≥ 2", len(lines))
	}
	// Data row should end with four empty fields: ,,,,
	if !strings.HasSuffix(lines[1], ",,,,") {
		t.Errorf("nil Sessions should produce empty session columns; data row: %q", lines[1])
	}
}

func TestCheckResultWrite_Table(t *testing.T) {
	var buf bytes.Buffer
	makeCheckResult().Write(&buf, FormatTable)
	out := buf.String()
	if !strings.Contains(out, "HOST") {
		t.Errorf("table missing HOST column: %s", out)
	}
	if !strings.Contains(out, "STATUS") {
		t.Errorf("table missing STATUS column: %s", out)
	}
	if !strings.Contains(out, "SRV01") {
		t.Errorf("table missing host value: %s", out)
	}
	if !strings.Contains(out, "Alert") {
		t.Errorf("table missing status value: %s", out)
	}
	if !strings.Contains(out, "admin") {
		t.Errorf("table missing changed_by: %s", out)
	}
}

func TestCheckResultWrite_Plain_NoOp(t *testing.T) {
	var buf bytes.Buffer
	makeCheckResult().Write(&buf, FormatPlain)
	if buf.Len() != 0 {
		t.Errorf("FormatPlain Write should produce no output, got: %q", buf.String())
	}
}

func TestCheckResultWrite_CSV_NilPointers(t *testing.T) {
	// StateSince and StateDurationSeconds are nil — exercises the nil path in
	// formatTimePtr and formatFloatPtr.
	r := &CheckResult{
		Version:        "26.91.0",
		Timestamp:      time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
		Host:           "SRV01",
		DrainModeLabel: "AllowAll",
		Status:         "Healthy",
		// StateSince and StateDurationSeconds intentionally nil
	}
	var buf bytes.Buffer
	r.Write(&buf, FormatCSV)
	out := buf.String()

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("CSV line count = %d, want ≥ 2", len(lines))
	}
	// Nil fields should produce empty CSV columns, not panic or "nil".
	if strings.Contains(out, "<nil>") {
		t.Errorf("CSV contains literal <nil>: %s", out)
	}
	// state_since and state_duration_seconds columns should be empty strings.
	fields := strings.Split(lines[1], ",")
	// CSV: timestamp,host,drain_mode,drain_mode_value,state_since(4),state_duration(5),...
	if len(fields) < 6 {
		t.Fatalf("CSV data row has too few fields (%d): %s", len(fields), lines[1])
	}
	if fields[4] != "" {
		t.Errorf("state_since column = %q, want empty string for nil", fields[4])
	}
	if fields[5] != "" {
		t.Errorf("state_duration_seconds column = %q, want empty string for nil", fields[5])
	}
}

func TestCheckResultWrite_Table_NilDuration(t *testing.T) {
	// StateDurationSeconds is nil — exercises the nil path in formatAge.
	r := &CheckResult{
		Version:        "26.91.0",
		Timestamp:      time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
		Host:           "SRV01",
		DrainModeLabel: "AllowAll",
		Status:         "Healthy",
		// StateDurationSeconds intentionally nil
	}
	var buf bytes.Buffer
	r.Write(&buf, FormatTable)
	out := buf.String()

	if !strings.Contains(out, "SRV01") {
		t.Errorf("table missing host: %s", out)
	}
	// Nil duration should render as "n/a", not panic.
	if !strings.Contains(out, "n/a") {
		t.Errorf("table should show n/a for nil duration: %s", out)
	}
}

// ── WriteSessions ─────────────────────────────────────────────────────────────

func makeTestSessions() ([]SessionInfo, *SessionSummary) {
	sessions := []SessionInfo{
		{SessionID: 1, UserName: "DOMAIN\\alice", Station: "RDP-Tcp#0", State: "Active", StateValue: wtsActive},
		{SessionID: 2, UserName: "DOMAIN\\bob", Station: "RDP-Tcp#1", State: "Disconnected", StateValue: wtsDisconnected},
	}
	summary := &SessionSummary{
		ActiveSessions:       1,
		DisconnectedSessions: 1,
		TotalSessions:        2,
		MaxSessions:          10,
		UtilizationPct:       20,
	}
	return sessions, summary
}

func TestWriteSessions_JSON_ValidJSON(t *testing.T) {
	sessions, summary := makeTestSessions()
	var buf bytes.Buffer
	WriteSessions(&buf, sessions, summary, FormatJSON)
	var out struct {
		Sessions []SessionInfo   `json:"sessions"`
		Summary  *SessionSummary `json:"summary"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("JSON unmarshal error: %v\noutput: %s", err, buf.String())
	}
	if len(out.Sessions) != 2 {
		t.Errorf("sessions len = %d, want 2", len(out.Sessions))
	}
	if out.Sessions[0].UserName != "DOMAIN\\alice" {
		t.Errorf("sessions[0].UserName = %q, want %q", out.Sessions[0].UserName, "DOMAIN\\alice")
	}
	if out.Summary == nil {
		t.Fatal("summary is nil")
	}
	if out.Summary.MaxSessions != 10 {
		t.Errorf("summary.MaxSessions = %d, want 10", out.Summary.MaxSessions)
	}
}

func TestWriteSessions_JSON_EmptySessionsArray(t *testing.T) {
	var buf bytes.Buffer
	WriteSessions(&buf, nil, nil, FormatJSON)
	var out struct {
		Sessions []SessionInfo `json:"sessions"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("JSON unmarshal error: %v", err)
	}
	if out.Sessions == nil {
		t.Error("sessions field should be [] not null for empty input")
	}
}

func TestWriteSessions_CSV_HeaderAndData(t *testing.T) {
	sessions, summary := makeTestSessions()
	var buf bytes.Buffer
	WriteSessions(&buf, sessions, summary, FormatCSV)
	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 CSV lines, got %d: %s", len(lines), out)
	}
	if !strings.Contains(lines[0], "session_id") {
		t.Errorf("CSV missing header; first line: %q", lines[0])
	}
	if !strings.Contains(out, "alice") {
		t.Errorf("CSV missing alice: %s", out)
	}
	if !strings.Contains(out, "Disconnected") {
		t.Errorf("CSV missing Disconnected state: %s", out)
	}
}

func TestWriteSessions_Table_ColumnsAndValues(t *testing.T) {
	sessions, summary := makeTestSessions()
	var buf bytes.Buffer
	WriteSessions(&buf, sessions, summary, FormatTable)
	out := buf.String()
	if !strings.Contains(out, "SESSION ID") {
		t.Errorf("table missing SESSION ID column: %s", out)
	}
	if !strings.Contains(out, "USER NAME") {
		t.Errorf("table missing USER NAME column: %s", out)
	}
	if !strings.Contains(out, "alice") {
		t.Errorf("table missing alice: %s", out)
	}
	if !strings.Contains(out, "Active") {
		t.Errorf("table missing Active state: %s", out)
	}
	// Summary line should appear after the table.
	if !strings.Contains(out, "sessions") {
		t.Errorf("table missing summary line: %s", out)
	}
	if !strings.Contains(out, "10") {
		t.Errorf("table summary missing max_sessions=10: %s", out)
	}
}

func TestWriteSessions_Plain_LogLines(t *testing.T) {
	sessions, summary := makeTestSessions()
	var buf bytes.Buffer
	WriteSessions(&buf, sessions, summary, FormatPlain)
	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// 2 session lines + 1 summary line
	if len(lines) != 3 {
		t.Fatalf("expected 3 plain lines, got %d: %s", len(lines), out)
	}
	if !strings.Contains(lines[0], "alice") {
		t.Errorf("first plain line missing alice: %s", lines[0])
	}
	if !strings.Contains(lines[0], "session_id=1") {
		t.Errorf("first plain line missing session_id: %s", lines[0])
	}
	if !strings.Contains(lines[0], "station=RDP-Tcp#0") {
		t.Errorf("first plain line missing station: %s", lines[0])
	}
	if !strings.Contains(lines[2], "sessions") {
		t.Errorf("summary line missing 'sessions': %s", lines[2])
	}
}

func TestWriteSessions_Table_NoSummaryWhenNil(t *testing.T) {
	sessions := []SessionInfo{
		{SessionID: 1, UserName: "user", Station: "RDP-Tcp#0", State: "Active", StateValue: wtsActive},
	}
	var buf bytes.Buffer
	WriteSessions(&buf, sessions, nil, FormatTable)
	out := buf.String()
	// Should have the table header and one data row but no summary line.
	if !strings.Contains(out, "SESSION ID") {
		t.Errorf("table missing header: %s", out)
	}
	if strings.Contains(out, "sessions") {
		t.Errorf("table should have no summary when summary is nil: %s", out)
	}
}

func TestFormatSessionSummaryLine_Capped(t *testing.T) {
	s := &SessionSummary{
		ActiveSessions:       3,
		DisconnectedSessions: 1,
		TotalSessions:        4,
		MaxSessions:          10,
		UtilizationPct:       40,
	}
	got := formatSessionSummaryLine(s)
	if !strings.Contains(got, "active=3") {
		t.Errorf("missing active=3: %s", got)
	}
	if !strings.Contains(got, "disconnected=1") {
		t.Errorf("missing disconnected=1: %s", got)
	}
	if !strings.Contains(got, "10") {
		t.Errorf("missing max_sessions: %s", got)
	}
	if !strings.Contains(got, "40%") {
		t.Errorf("missing utilization: %s", got)
	}
}

func TestFormatSessionSummaryLine_Uncapped(t *testing.T) {
	s := &SessionSummary{
		ActiveSessions:       5,
		DisconnectedSessions: 0,
		TotalSessions:        5,
		MaxSessions:          0,
	}
	got := formatSessionSummaryLine(s)
	if !strings.Contains(got, "active=5") {
		t.Errorf("missing active=5: %s", got)
	}
	if strings.Contains(got, "disconnected") {
		t.Errorf("should omit disconnected when 0: %s", got)
	}
	if strings.Contains(got, "%") {
		t.Errorf("should omit utilization when uncapped: %s", got)
	}
}

func TestFormatSessionSummaryLine_Nil(t *testing.T) {
	got := formatSessionSummaryLine(nil)
	if got != "" {
		t.Errorf("formatSessionSummaryLine(nil) = %q, want empty string", got)
	}
}

func TestWriteSessions_Plain_NilSummary(t *testing.T) {
	sessions, _ := makeTestSessions()
	var buf bytes.Buffer
	WriteSessions(&buf, sessions, nil, FormatPlain)
	out := buf.String()
	// Each session line is present; no summary line (nil summary → empty string
	// from formatSessionSummaryLine, still written as a blank line by Fprintf).
	if !strings.Contains(out, "alice") {
		t.Errorf("missing alice session: %s", out)
	}
	// The summary line will be an empty string (no "sessions" keyword).
	if strings.Contains(out, "sessions active=") {
		t.Errorf("nil summary should not emit session counts: %s", out)
	}
}

// ── DisconnectedSessions in AuditRecord / HistoryRecord ───────────────────────

func TestAuditToHistory_CopiesDisconnectedSessions(t *testing.T) {
	dur := 30
	rec := AuditRecord{
		Timestamp:            time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		Host:                 "SRV01",
		DrainMode:            AllowAll,
		DrainLabel:           "AllowAll",
		ActiveSessions:       3,
		DisconnectedSessions: 2,
		TotalSessions:        5,
		MaxSessions:          20,
		ExitCode:             0,
	}
	hr := AuditToHistory(rec, &dur)
	if hr.ActiveSessions != 3 {
		t.Errorf("ActiveSessions = %d, want 3", hr.ActiveSessions)
	}
	if hr.DisconnectedSessions != 2 {
		t.Errorf("DisconnectedSessions = %d, want 2", hr.DisconnectedSessions)
	}
	if hr.TotalSessions != 5 {
		t.Errorf("TotalSessions = %d, want 5", hr.TotalSessions)
	}
	if hr.MaxSessions != 20 {
		t.Errorf("MaxSessions = %d, want 20", hr.MaxSessions)
	}
}

func TestWriteHistory_CSV_IncludesSessionColumns(t *testing.T) {
	ts := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	records := []AuditRecord{
		{
			Timestamp:            ts,
			Host:                 "SRV01",
			DrainMode:            AllowAll,
			DrainLabel:           "AllowAll",
			ActiveSessions:       4,
			DisconnectedSessions: 2,
			TotalSessions:        6,
			MaxSessions:          20,
			ExitCode:             0,
		},
	}
	var buf bytes.Buffer
	WriteHistory(&buf, records, FormatCSV)
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("CSV line count = %d, want ≥ 2; output:\n%s", len(lines), out)
	}
	for _, col := range []string{"active_sessions", "disconnected_sessions", "total_sessions", "max_sessions"} {
		if !strings.Contains(lines[0], col) {
			t.Errorf("CSV header missing %q; header: %s", col, lines[0])
		}
	}
	if !strings.Contains(lines[1], "4") {
		t.Errorf("CSV data row missing active_sessions=4: %s", lines[1])
	}
	if !strings.Contains(lines[1], "2") {
		t.Errorf("CSV data row missing disconnected_sessions=2: %s", lines[1])
	}
}

// ── Reconciliation row rendering (T030) ───────────────────────────────────────

// makeReconciliationRecords returns two audit records, one live transition and
// one reconciliation row written by the drift reconciler at service startup.
func makeReconciliationRecords() []AuditRecord {
	ts := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	return []AuditRecord{
		{
			Timestamp:      ts.Add(5 * time.Minute),
			Host:           "SRV01",
			DrainMode:      PreventNewLogon,
			DrainLabel:     "PreventNewLogon",
			Changed:        true,
			Reconciliation: true,
			Reason:         "service-downtime drift: last-known AllowAll, observed PreventNewLogon",
			ExitCode:       1,
		},
		{
			Timestamp:  ts,
			Host:       "SRV01",
			DrainMode:  AllowAll,
			DrainLabel: "AllowAll",
			Changed:    false,
			ExitCode:   0,
		},
	}
}

func TestWriteHistory_Plain_ReconciliationTag(t *testing.T) {
	var buf bytes.Buffer
	WriteHistory(&buf, makeReconciliationRecords(), FormatPlain)
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("plain line count = %d, want 2; output:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "[DRIFT]") {
		t.Errorf("reconciliation row should carry [DRIFT] tag: %s", lines[0])
	}
	if strings.Contains(lines[0], "[INF]") || strings.Contains(lines[0], "[ERR]") {
		t.Errorf("reconciliation row should not carry INF/ERR level: %s", lines[0])
	}
	if !strings.Contains(lines[0], "service-downtime drift") {
		t.Errorf("reconciliation row should include reason: %s", lines[0])
	}
	if strings.Contains(lines[1], "[DRIFT]") {
		t.Errorf("live row should not carry [DRIFT] tag: %s", lines[1])
	}
}

func TestWriteHistory_Table_ReconciliationPrefix(t *testing.T) {
	var buf bytes.Buffer
	WriteHistory(&buf, makeReconciliationRecords(), FormatTable)
	out := buf.String()
	if !strings.Contains(out, "[DRIFT] PreventNewLogon") {
		t.Errorf("table DRAIN MODE column should prefix reconciliation row with [DRIFT]: %s", out)
	}
	if !strings.Contains(out, "DRIFT") {
		t.Errorf("CHANGED column should show DRIFT for reconciliation row: %s", out)
	}
	if !strings.Contains(out, "service-downtime drift") {
		t.Errorf("CHANGED BY column should show reason for reconciliation row: %s", out)
	}
}

func TestWriteHistory_CSV_ReconciliationColumns(t *testing.T) {
	var buf bytes.Buffer
	WriteHistory(&buf, makeReconciliationRecords(), FormatCSV)
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("CSV line count = %d, want ≥ 3; output:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "reconciliation") {
		t.Errorf("CSV header missing reconciliation column: %s", lines[0])
	}
	if !strings.Contains(lines[0], "reason") {
		t.Errorf("CSV header missing reason column: %s", lines[0])
	}
	if !strings.Contains(lines[1], "true") {
		t.Errorf("CSV data row should have reconciliation=true: %s", lines[1])
	}
	if !strings.Contains(lines[1], "service-downtime drift") {
		t.Errorf("CSV data row should carry reason text: %s", lines[1])
	}
}

func TestWriteHistory_JSON_ReconciliationFields(t *testing.T) {
	var buf bytes.Buffer
	WriteHistory(&buf, makeReconciliationRecords(), FormatJSON)
	var records []HistoryRecord
	if err := json.Unmarshal([]byte(strings.TrimSuffix(buf.String(), "\n")), &records); err != nil {
		t.Fatalf("JSON unmarshal error: %v\n%s", err, buf.String())
	}
	if len(records) != 2 {
		t.Fatalf("record count = %d, want 2", len(records))
	}
	if !records[0].Reconciliation {
		t.Errorf("first record should have Reconciliation=true")
	}
	if records[0].Reason == "" {
		t.Errorf("first record should have Reason set")
	}
	if records[1].Reconciliation {
		t.Errorf("second (live) record should have Reconciliation=false")
	}
}

func TestAuditToHistory_CopiesReconciliationAndReason(t *testing.T) {
	dur := 0
	rec := AuditRecord{
		Timestamp:      time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		Host:           "SRV01",
		DrainMode:      PreventNewLogon,
		DrainLabel:     "PreventNewLogon",
		Reconciliation: true,
		Reason:         "service-downtime drift",
	}
	hr := AuditToHistory(rec, &dur)
	if !hr.Reconciliation {
		t.Error("HistoryRecord.Reconciliation should be true")
	}
	if hr.Reason != "service-downtime drift" {
		t.Errorf("HistoryRecord.Reason = %q, want %q", hr.Reason, "service-downtime drift")
	}
}

func makeReconciliationHistoryRecords() []HistoryRecord {
	dur0, dur300 := 0, 300
	return []HistoryRecord{
		{
			Timestamp:            "2026-04-01T10:05:00Z",
			Host:                 "SRV01",
			DrainMode:            "PreventNewLogon",
			DrainValue:           1,
			StateDurationSeconds: &dur0,
			Changed:              true,
			Reconciliation:       true,
			Reason:               "service-downtime drift: last-known AllowAll, observed PreventNewLogon",
			ExitCode:             1,
		},
		{
			Timestamp:            "2026-04-01T10:00:00Z",
			Host:                 "SRV01",
			DrainMode:            "AllowAll",
			DrainValue:           0,
			StateDurationSeconds: &dur300,
			Changed:              false,
			ExitCode:             0,
		},
	}
}

func TestWriteHistoryRecords_Plain_ReconciliationTag(t *testing.T) {
	var buf bytes.Buffer
	WriteHistoryRecords(&buf, makeReconciliationHistoryRecords(), FormatPlain)
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("plain line count = %d, want 2; output:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "[DRIFT]") {
		t.Errorf("reconciliation row should carry [DRIFT] tag: %s", lines[0])
	}
	if !strings.Contains(lines[0], "service-downtime drift") {
		t.Errorf("reconciliation row should include reason: %s", lines[0])
	}
}

func TestWriteHistoryRecords_Table_ReconciliationPrefix(t *testing.T) {
	var buf bytes.Buffer
	WriteHistoryRecords(&buf, makeReconciliationHistoryRecords(), FormatTable)
	out := buf.String()
	if !strings.Contains(out, "[DRIFT] PreventNewLogon") {
		t.Errorf("table should prefix reconciliation row drain mode with [DRIFT]: %s", out)
	}
	if !strings.Contains(out, "DRIFT") {
		t.Errorf("CHANGED column should show DRIFT for reconciliation row: %s", out)
	}
}

func TestWriteHistoryRecords_CSV_ReconciliationColumns(t *testing.T) {
	var buf bytes.Buffer
	WriteHistoryRecords(&buf, makeReconciliationHistoryRecords(), FormatCSV)
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("CSV line count = %d, want ≥ 3; output:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "reconciliation") {
		t.Errorf("CSV header missing reconciliation column: %s", lines[0])
	}
	if !strings.Contains(lines[1], "true") {
		t.Errorf("CSV data row should have reconciliation=true: %s", lines[1])
	}
	if !strings.Contains(lines[1], "service-downtime drift") {
		t.Errorf("CSV data row should carry reason text: %s", lines[1])
	}
}

func TestWriteHistoryRecords_CSV_IncludesSessionColumns(t *testing.T) {
	dur := 0
	records := []HistoryRecord{
		{
			Timestamp:            "2026-04-01T10:00:00Z",
			Host:                 "SRV01",
			DrainMode:            "AllowAll",
			DrainValue:           0,
			StateDurationSeconds: &dur,
			ActiveSessions:       4,
			DisconnectedSessions: 2,
			TotalSessions:        6,
			MaxSessions:          20,
			ExitCode:             0,
		},
	}
	var buf bytes.Buffer
	WriteHistoryRecords(&buf, records, FormatCSV)
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("CSV line count = %d, want ≥ 2; output:\n%s", len(lines), out)
	}
	for _, col := range []string{"active_sessions", "disconnected_sessions", "total_sessions", "max_sessions"} {
		if !strings.Contains(lines[0], col) {
			t.Errorf("CSV header missing %q; header: %s", col, lines[0])
		}
	}
	if !strings.Contains(lines[1], "2") {
		t.Errorf("CSV data row missing disconnected_sessions=2: %s", lines[1])
	}
}
