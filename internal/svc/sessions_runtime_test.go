//go:build windows

package svc

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/dashboard"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/perfmon"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

type runtimeActionLedger struct {
	acknowledged []string
	cleanupCalls int
}

func (l *runtimeActionLedger) Acknowledge(_ context.Context, actionID string) error {
	l.acknowledged = append(l.acknowledged, actionID)
	return nil
}

func (l *runtimeActionLedger) Cleanup(_ context.Context, _ time.Time, _ time.Duration, _ int) (int, error) {
	l.cleanupCalls++
	return 0, nil
}

func TestSessionRuntimeReportsEnrichedSnapshotsSequentially(t *testing.T) {
	user, client := "alice", "rdp-client"
	workingSet := sessiondata.DecimalUint64(4096)
	cpu := 12.5
	runtime := &sessionRuntime{
		agentInstanceID: "0195a584-5b25-7000-8000-000000000001",
		host:            "host.example.test",
		logicalCPUs:     4,
		config: dc.SessionsConfig{Enabled: true, CollectProcesses: true, TopProcesses: 2,
			IdentityVisibility: dc.SessionVisibilityMasked, ClientVisibility: dc.SessionVisibilityHidden, ProcessVisibility: dc.SessionVisibilityMasked},
		collectPDH: func(ids []uint32) (map[uint32]perfmon.SessionPDHMetrics, perfmon.SessionPDHCapabilities) {
			if len(ids) != 1 || ids[0] != 9 {
				t.Fatalf("PDH session IDs = %v", ids)
			}
			return map[uint32]perfmon.SessionPDHMetrics{9: {CPUPercent: &cpu, WorkingSetBytes: &workingSet}}, perfmon.SessionPDHCapabilities{InputDelay: true}
		},
		collectProcesses: func(ids []uint32, topN uint8) (map[uint32][]sessiondata.SessionProcess, bool) {
			if topN != 2 {
				t.Fatalf("topN = %d, want 2", topN)
			}
			return map[uint32][]sessiondata.SessionProcess{9: {{PID: 44, ImageName: "app.exe", WorkingSetBytes: 100}}}, true
		},
	}

	var received []sessiondata.SessionSnapshot
	state := &dashboard.ServerState{OnSessionSnapshot: func(snapshot sessiondata.SessionSnapshot) error {
		received = append(received, snapshot)
		return nil
	}}
	records := []sessiondata.SessionRecord{{SessionID: 9, User: &user, ClientName: &client, State: sessiondata.SessionActive}}
	runtime.Report(context.Background(), records, nil, nil, nil, state)
	runtime.Report(context.Background(), records, nil, nil, nil, state)

	if len(received) != 2 || received[0].Sequence != 1 || received[1].Sequence != 2 {
		t.Fatalf("sequences = %#v", received)
	}
	got := received[0].Sessions[0]
	if got.CPUPercent == nil || *got.CPUPercent != cpu || got.WorkingSetBytes == nil || *got.WorkingSetBytes != workingSet {
		t.Fatalf("PDH enrichment missing: %#v", got)
	}
	if got.User == nil || *got.User != "***" || got.ClientName != nil || len(got.Processes) != 1 || got.Processes[0].ImageName != "***" {
		t.Fatalf("privacy projection = %#v", got)
	}
	if err := sessiondata.ValidateSnapshot(received[0]); err != nil {
		t.Fatalf("success snapshot invalid: %v", err)
	}
	if !received[0].Capabilities.InputDelay || !received[0].Capabilities.Processes || received[0].Capabilities.RemoteFX {
		t.Fatalf("capabilities = %#v", received[0].Capabilities)
	}
}

func TestSessionRuntimeFatalDisabledAndRemoteIsolation(t *testing.T) {
	runtime := &sessionRuntime{
		agentInstanceID: "0195a584-5b25-7000-8000-000000000002",
		host:            "host.example.test", logicalCPUs: 1,
		config: dc.SessionsConfig{Enabled: true, IdentityVisibility: dc.SessionVisibilityFull, ClientVisibility: dc.SessionVisibilityFull, ProcessVisibility: dc.SessionVisibilityFull},
	}
	var remote []sessiondata.SessionSnapshot
	runtime.reportRemote = func(_ context.Context, url string, snapshot sessiondata.SessionSnapshot) {
		if url != "https://dashboard.example.test" {
			t.Fatalf("URL = %q", url)
		}
		remote = append(remote, snapshot)
	}
	runtime.Report(context.Background(), nil, errors.New("WTS unavailable"), nil, &dc.DashboardConfig{URL: "https://dashboard.example.test"}, nil)
	if len(remote) != 1 || remote[0].CollectionError == nil || remote[0].CollectionError.Code != sessiondata.CollectionErrorWTSEnumerationFailed || len(remote[0].Sessions) != 0 {
		t.Fatalf("fatal snapshot = %#v", remote)
	}

	if err := sessiondata.ValidateSnapshot(remote[0]); err != nil {
		t.Fatalf("fatal snapshot invalid: %v", err)
	}
	runtime.UpdateConfig(dc.SessionsConfig{Enabled: false})
	runtime.Report(context.Background(), nil, nil, nil, &dc.DashboardConfig{URL: "https://dashboard.example.test"}, nil)
	if len(remote) != 1 || runtime.sequence != 1 {
		t.Fatalf("disabled runtime reported: sequence=%d reports=%d", runtime.sequence, len(remote))
	}
}

func TestSessionRuntimeCompactsWorstCaseSnapshotWithoutDroppingSessions(t *testing.T) {
	text := strings.Repeat("<", 256)
	records := make([]sessiondata.SessionRecord, sessiondata.MaxSessions)
	for i := range records {
		records[i] = sessiondata.SessionRecord{
			SessionID:     uint32(i + 1),
			State:         sessiondata.SessionActive,
			User:          &text,
			Domain:        &text,
			Station:       &text,
			ClientName:    &text,
			ClientAddress: &text,
		}
	}
	runtime := &sessionRuntime{
		agentInstanceID: "0195a584-5b25-7000-8000-000000000006",
		host:            "host.example.test",
		logicalCPUs:     1,
		config:          dc.SessionsConfig{Enabled: true, IdentityVisibility: dc.SessionVisibilityFull, ClientVisibility: dc.SessionVisibilityFull, ProcessVisibility: dc.SessionVisibilityFull},
	}
	var received sessiondata.SessionSnapshot
	state := &dashboard.ServerState{OnSessionSnapshot: func(snapshot sessiondata.SessionSnapshot) error {
		received = snapshot
		return nil
	}}
	runtime.Report(context.Background(), records, nil, nil, nil, state)

	payload, err := json.Marshal(received)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > sessiondata.MaxSnapshotBytes {
		t.Fatalf("snapshot = %d bytes, want at most %d", len(payload), sessiondata.MaxSnapshotBytes)
	}
	if received.CollectionError != nil || len(received.Sessions) != sessiondata.MaxSessions {
		t.Fatalf("snapshot status/count = %#v / %d", received.CollectionError, len(received.Sessions))
	}
}

func TestSessionRuntimeReportsFatalStatusWhenSnapshotCannotMarshal(t *testing.T) {
	invalid := math.NaN()
	runtime := &sessionRuntime{
		agentInstanceID: "0195a584-5b25-7000-8000-000000000007",
		host:            "host.example.test",
		logicalCPUs:     1,
		config:          dc.SessionsConfig{Enabled: true},
	}
	var received sessiondata.SessionSnapshot
	state := &dashboard.ServerState{OnSessionSnapshot: func(snapshot sessiondata.SessionSnapshot) error {
		received = snapshot
		return nil
	}}
	runtime.Report(context.Background(), []sessiondata.SessionRecord{{SessionID: 1, State: sessiondata.SessionActive, CPUPercent: &invalid}}, nil, nil, nil, state)
	if received.CollectionError == nil || received.CollectionError.Code != sessiondata.CollectionErrorTimeout || len(received.Sessions) != 0 || received.Capabilities != (sessiondata.SessionCapabilities{}) {
		t.Fatalf("fatal snapshot = %#v", received)
	}
}

func TestSessionRuntimeReportsFatalStatusForMoreThanMaxSessions(t *testing.T) {
	records := make([]sessiondata.SessionRecord, sessiondata.MaxSessions+1)
	for i := range records {
		records[i] = sessiondata.SessionRecord{SessionID: uint32(i + 1), State: sessiondata.SessionActive}
	}
	runtime := &sessionRuntime{
		agentInstanceID: "0195a584-5b25-7000-8000-000000000008",
		host:            "host.example.test",
		logicalCPUs:     1,
		config:          dc.SessionsConfig{Enabled: true},
		collectPDH: func([]uint32) (map[uint32]perfmon.SessionPDHMetrics, perfmon.SessionPDHCapabilities) {
			t.Fatal("PDH collection ran for an oversized session set")
			return nil, perfmon.SessionPDHCapabilities{}
		},
	}
	var received sessiondata.SessionSnapshot
	state := &dashboard.ServerState{OnSessionSnapshot: func(snapshot sessiondata.SessionSnapshot) error {
		received = snapshot
		return nil
	}}
	runtime.Report(context.Background(), records, nil, nil, nil, state)
	if received.CollectionError == nil || received.CollectionError.Code != sessiondata.CollectionErrorTimeout || len(received.Sessions) != 0 || received.Capabilities != (sessiondata.SessionCapabilities{}) {
		t.Fatalf("fatal snapshot = %#v", received)
	}
	payload, err := json.Marshal(received)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > sessiondata.MaxSnapshotBytes {
		t.Fatalf("fatal payload = %d bytes, want at most %d", len(payload), sessiondata.MaxSnapshotBytes)
	}
}

func TestSessionRuntimeRequiredArraysOnEveryReportPath(t *testing.T) {
	record := sessiondata.SessionRecord{SessionID: 1, State: sessiondata.SessionActive}
	oversized := make([]sessiondata.SessionRecord, sessiondata.MaxSessions+1)
	for i := range oversized {
		oversized[i] = sessiondata.SessionRecord{SessionID: uint32(i + 1), State: sessiondata.SessionActive}
	}
	compacted := make([]sessiondata.SessionRecord, sessiondata.MaxSessions)
	text := strings.Repeat("<", 256)
	for i := range compacted {
		compacted[i] = sessiondata.SessionRecord{
			SessionID: uint32(i + 1), State: sessiondata.SessionActive,
			User: &text, Domain: &text, Station: &text, ClientName: &text, ClientAddress: &text,
		}
	}
	notANumber := math.NaN()
	cases := []struct {
		name              string
		config            dc.SessionsConfig
		records           []sessiondata.SessionRecord
		collectionErr     error
		collectProcesses  func([]uint32, uint8) (map[uint32][]sessiondata.SessionProcess, bool)
		wantCollectionErr bool
	}{
		{name: "process collection disabled", config: dc.SessionsConfig{Enabled: true}, records: []sessiondata.SessionRecord{record}},
		{name: "process collection unavailable", config: dc.SessionsConfig{Enabled: true, CollectProcesses: true}, records: []sessiondata.SessionRecord{record}, collectProcesses: func([]uint32, uint8) (map[uint32][]sessiondata.SessionProcess, bool) { return nil, false }},
		{name: "fatal collection", config: dc.SessionsConfig{Enabled: true}, collectionErr: errors.New("WTS unavailable"), wantCollectionErr: true},
		{name: "over session limit", config: dc.SessionsConfig{Enabled: true}, records: oversized, wantCollectionErr: true},
		{name: "successful compaction", config: dc.SessionsConfig{Enabled: true, IdentityVisibility: dc.SessionVisibilityFull, ClientVisibility: dc.SessionVisibilityFull, ProcessVisibility: dc.SessionVisibilityFull}, records: compacted},
		{name: "compaction fallback", config: dc.SessionsConfig{Enabled: true}, records: []sessiondata.SessionRecord{{SessionID: 1, State: sessiondata.SessionActive, CPUPercent: &notANumber}}, wantCollectionErr: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			runtime := &sessionRuntime{
				agentInstanceID:  "0195a584-5b25-7000-8000-000000000009",
				host:             "host.example.test",
				logicalCPUs:      1,
				config:           test.config,
				collectProcesses: test.collectProcesses,
			}
			var received sessiondata.SessionSnapshot
			state := &dashboard.ServerState{OnSessionSnapshot: func(snapshot sessiondata.SessionSnapshot) error {
				received = snapshot
				return nil
			}}
			runtime.Report(context.Background(), test.records, test.collectionErr, nil, nil, state)
			if (received.CollectionError != nil) != test.wantCollectionErr {
				t.Fatalf("collection error = %#v, want present=%t", received.CollectionError, test.wantCollectionErr)
			}
			assertSessionSnapshotRequiredArraysOnWire(t, received)
		})
	}
}

func assertSessionSnapshotRequiredArraysOnWire(t *testing.T, snapshot sessiondata.SessionSnapshot) {
	t.Helper()
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatalf("decode snapshot wire: %v", err)
	}
	if got := string(wire["sessions"]); got == "null" || got == "" {
		t.Fatalf("sessions wire value = %s, want array", got)
	} else if len(snapshot.Sessions) == 0 && got != "[]" {
		t.Fatalf("empty sessions wire value = %s, want []", got)
	}
	var sessions []map[string]json.RawMessage
	if err := json.Unmarshal(wire["sessions"], &sessions); err != nil {
		t.Fatalf("decode sessions wire: %v", err)
	}
	for i, session := range sessions {
		if got := string(session["processes"]); got == "null" || got == "" {
			t.Fatalf("session %d processes wire value = %s, want array", i, got)
		} else if len(snapshot.Sessions[i].Processes) == 0 && got != "[]" {
			t.Fatalf("session %d empty processes wire value = %s, want []", i, got)
		}
	}
	if err := sessiondata.ValidateSnapshot(snapshot); err != nil {
		t.Fatalf("snapshot validation: %v", err)
	}
}

func TestReservedSessionRuntimeUsesIncreasingGenerationAndResetsSequence(t *testing.T) {
	originalHostname := sessionRuntimeHostname
	originalReserve := reserveSessionRuntimeInstanceID
	t.Cleanup(func() {
		sessionRuntimeHostname = originalHostname
		reserveSessionRuntimeInstanceID = originalReserve
	})

	sessionRuntimeHostname = func() (string, error) { return " RDSH-07.EXAMPLE.TEST. ", nil }
	ids := []string{
		"0195a584-5b25-7000-8000-000000000003",
		"0195a584-5b26-7000-8000-000000000004",
	}
	var reservations int
	reserveSessionRuntimeInstanceID = func(_ context.Context, _ *telemetry.DB, host string, _ time.Time) (string, error) {
		if host != "rdsh-07.example.test" {
			t.Fatalf("reservation host = %q", host)
		}
		id := ids[reservations]
		reservations++
		return id, nil
	}

	first, err := newReservedSessionRuntime(context.Background(), nil, dc.SessionsConfig{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := newReservedSessionRuntime(context.Background(), nil, dc.SessionsConfig{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	firstID, _ := sessiondata.ParseUUIDv7(first.agentInstanceID)
	secondID, _ := sessiondata.ParseUUIDv7(second.agentInstanceID)
	if reservations != 2 || sessiondata.CompareUUIDv7(secondID, firstID) <= 0 {
		t.Fatalf("reserved generations = %q, %q", first.agentInstanceID, second.agentInstanceID)
	}

	var snapshots []sessiondata.SessionSnapshot
	report := func(_ context.Context, _ string, snapshot sessiondata.SessionSnapshot) {
		snapshots = append(snapshots, snapshot)
	}
	first.reportRemote = report
	second.reportRemote = report
	dashCfg := &dc.DashboardConfig{URL: "https://dashboard.example.test"}
	first.Report(context.Background(), nil, nil, nil, dashCfg, nil)
	second.Report(context.Background(), nil, nil, nil, dashCfg, nil)
	if len(snapshots) != 2 || snapshots[0].Sequence != 1 || snapshots[1].Sequence != 1 {
		t.Fatalf("restart sequences = %#v", snapshots)
	}
}

func TestReservedSessionRuntimeReservationFailureIsIsolated(t *testing.T) {
	originalHostname := sessionRuntimeHostname
	originalReserve := reserveSessionRuntimeInstanceID
	t.Cleanup(func() {
		sessionRuntimeHostname = originalHostname
		reserveSessionRuntimeInstanceID = originalReserve
	})

	sessionRuntimeHostname = func() (string, error) { return "rdsh-07.example.test", nil }
	reserveSessionRuntimeInstanceID = func(context.Context, *telemetry.DB, string, time.Time) (string, error) {
		return "", errors.New("disk unavailable")
	}
	runtime, err := newReservedSessionRuntime(context.Background(), nil, dc.SessionsConfig{})
	if runtime != nil || err == nil || !strings.Contains(err.Error(), "session runtime reserve generation: disk unavailable") {
		t.Fatalf("reservation failure runtime=%#v error=%v", runtime, err)
	}
}

func TestReservedSessionRuntimeHostnameValidation(t *testing.T) {
	originalHostname := sessionRuntimeHostname
	originalReserve := reserveSessionRuntimeInstanceID
	t.Cleanup(func() {
		sessionRuntimeHostname = originalHostname
		reserveSessionRuntimeInstanceID = originalReserve
	})
	reserveSessionRuntimeInstanceID = func(context.Context, *telemetry.DB, string, time.Time) (string, error) {
		return "0195a584-5b25-7000-8000-000000000005", nil
	}

	tests := []struct {
		name     string
		hostname func() (string, error)
		wantHost string
		wantErr  string
	}{
		{name: "lookup error", hostname: func() (string, error) { return "", errors.New("lookup failed") }, wantErr: "session runtime hostname: lookup failed"},
		{name: "empty hostname", hostname: func() (string, error) { return "", nil }, wantErr: "session runtime hostname \"\" is invalid"},
		{name: "underscore hostname", hostname: func() (string, error) { return "rdsh_07", nil }, wantErr: "session runtime hostname \"rdsh_07\" is invalid"},
		{name: "normalizes valid hostname", hostname: func() (string, error) { return " RDSH-07.EXAMPLE.TEST. ", nil }, wantHost: "rdsh-07.example.test"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sessionRuntimeHostname = test.hostname
			runtime, err := newReservedSessionRuntime(context.Background(), nil, dc.SessionsConfig{})
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("newReservedSessionRuntime() error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("newReservedSessionRuntime(): %v", err)
			}
			if runtime.host != test.wantHost {
				t.Fatalf("host = %q, want %q", runtime.host, test.wantHost)
			}
		})
	}
}

func TestSessionSummaryFromRecordsKeepsHeartbeatSemantics(t *testing.T) {
	summary := sessionSummaryFromRecords([]sessiondata.SessionRecord{
		{State: sessiondata.SessionActive}, {State: sessiondata.SessionDisconnected}, {State: sessiondata.SessionIdle},
	}, 1)
	if summary.ActiveSessions != 1 || summary.DisconnectedSessions != 1 || summary.TotalSessions != 2 || summary.UtilizationPct != 100 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestSessionRuntimeRemoteActionsAcknowledgeBeforeExecuteAndRetainCompletion(t *testing.T) {
	ledger := &runtimeActionLedger{}
	executed := []string{}
	runtime := &sessionRuntime{
		config:       dc.SessionsConfig{RetentionHours: 1},
		actionLedger: ledger,
		completions: []sessiondata.CompletedSessionAction{{
			ActionID: "old", Outcome: sessiondata.SessionActionOutcomeCompleted,
		}},
		executeAction: func(_ context.Context, action sessiondata.PendingSessionAction) (sessiondata.CompletedSessionAction, error) {
			executed = append(executed, action.ActionID)
			return sessiondata.CompletedSessionAction{ActionID: action.ActionID, Outcome: sessiondata.SessionActionOutcomeCompleted}, nil
		},
	}
	runtime.applyRemoteSessionActions(context.Background(), &dashboard.ReportWithSessionActionsResult{
		ReportSessionActionResult: dashboard.ReportSessionActionResult{
			AcknowledgedSessionActionIDs: []string{"old"},
			PendingSessionActions:        []sessiondata.PendingSessionAction{{ActionID: "new"}},
		},
	})
	if got := ledger.acknowledged; len(got) != 1 || got[0] != "old" {
		t.Fatalf("acknowledged = %v", got)
	}
	if len(executed) != 1 || executed[0] != "new" {
		t.Fatalf("executed = %v", executed)
	}
	if got := runtime.pendingCompletions(); len(got) != 1 || got[0].ActionID != "new" {
		t.Fatalf("queued completions = %#v", got)
	}
}

func TestSessionRuntimeLocalActionsRetainCompletionOnFailure(t *testing.T) {
	ledger := &runtimeActionLedger{}
	runtime := &sessionRuntime{
		host:         "host.example.test",
		config:       dc.SessionsConfig{RetentionHours: 1},
		actionLedger: ledger,
		completions: []sessiondata.CompletedSessionAction{{
			ActionID: "old", Outcome: sessiondata.SessionActionOutcomeCompleted,
		}},
	}
	state := &dashboard.ServerState{
		CompleteSessionActions: func(_ string, completed []sessiondata.CompletedSessionAction) ([]string, error) {
			if len(completed) != 1 || completed[0].ActionID != "old" {
				t.Fatalf("reported completions = %#v", completed)
			}
			return nil, errors.New("temporary dashboard failure")
		},
		DeliverSessionActions: func(string) ([]sessiondata.PendingSessionAction, error) {
			t.Fatal("delivery must wait for completion retry")
			return nil, nil
		},
	}
	runtime.applyLocalSessionActions(context.Background(), state)
	if len(runtime.completions) != 1 || len(ledger.acknowledged) != 0 {
		t.Fatalf("completion failure lost state: completions=%d acknowledgements=%v", len(runtime.completions), ledger.acknowledged)
	}
}
