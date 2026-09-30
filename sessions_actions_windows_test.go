//go:build windows

package drainctl

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
)

type memorySessionActionLedger struct {
	entries     map[string]sessiondata.SessionActionLedgerEntry
	completions []sessiondata.CompletedSessionAction
}

func (ledger *memorySessionActionLedger) Claim(_ context.Context, actionID string, expiresAt, claimedAt time.Time) (bool, sessiondata.SessionActionLedgerEntry, error) {
	if entry, exists := ledger.entries[actionID]; exists {
		return false, entry, nil
	}
	entry := sessiondata.SessionActionLedgerEntry{ActionID: actionID, State: sessiondata.SessionActionLedgerClaimed, ClaimedAtMS: claimedAt.UnixMilli(), ExpiresAtMS: expiresAt.UnixMilli()}
	ledger.entries[actionID] = entry
	return true, entry, nil
}

func (ledger *memorySessionActionLedger) Complete(_ context.Context, actionID string, outcome sessiondata.SessionActionOutcome, completedAt time.Time) error {
	entry := ledger.entries[actionID]
	entry.State = sessiondata.SessionActionLedgerTerminal
	entry.Outcome = &outcome
	entry.CompletedAtMS = new(completedAt.UnixMilli())
	ledger.entries[actionID] = entry
	ledger.completions = append(ledger.completions, completedSessionAction(actionID, outcome))
	return nil
}

type readableMemorySessionActionLedger struct{ *memorySessionActionLedger }

func (ledger *readableMemorySessionActionLedger) Entry(_ context.Context, actionID string) (sessiondata.SessionActionLedgerEntry, error) {
	entry, ok := ledger.entries[actionID]
	if !ok {
		return sessiondata.SessionActionLedgerEntry{}, errors.New("missing entry")
	}
	return entry, nil
}

func TestSessionActionExecutor_LogoffSuccessIsNonForcedAndTerminal(t *testing.T) {
	ledger := &memorySessionActionLedger{entries: make(map[string]sessiondata.SessionActionLedgerEntry)}
	now := time.UnixMilli(2_000)
	logoffCalls := 0
	wait := true
	executor := newSessionActionExecutor(ledger, func() time.Time { return now }, func(uint32) (*int64, error) { return new(int64(100)), nil }, func(_ uint32, gotWait bool) error {
		logoffCalls++
		wait = gotWait
		return nil
	}, func(uint32, string) error { t.Fatal("message called for logoff"); return nil })

	got, err := executor.Execute(context.Background(), testPendingAction(sessiondata.SessionActionLogoff, 3_000, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != sessiondata.SessionActionOutcomeCompleted || logoffCalls != 1 || wait {
		t.Fatalf("completion=%#v logoff calls=%d wait=%v", got, logoffCalls, wait)
	}
	assertActionTerminal(t, ledger, got.ActionID, sessiondata.SessionActionOutcomeCompleted)
}

func TestSessionActionExecutor_DisconnectIsNonWaitingAndGuarded(t *testing.T) {
	now := time.UnixMilli(2_000)
	newExecutor := func(ledger SessionActionLedger, lookup sessionActionLookup, disconnect sessionActionDisconnect) *SessionActionExecutor {
		return newSessionActionExecutorWithDisconnect(ledger, func() time.Time { return now }, lookup, disconnect, func(uint32, bool) error {
			t.Fatal("logoff called for disconnect")
			return nil
		}, func(uint32, string) error {
			t.Fatal("message called for disconnect")
			return nil
		})
	}
	t.Run("calls WTS disconnect after claim and logon guard", func(t *testing.T) {
		ledger := &memorySessionActionLedger{entries: make(map[string]sessiondata.SessionActionLedgerEntry)}
		calls := 0
		wait := true
		got, err := newExecutor(ledger, func(uint32) (*int64, error) { return new(int64(100)), nil }, func(_ uint32, gotWait bool) error {
			calls++
			wait = gotWait
			return nil
		}).Execute(context.Background(), testPendingAction(sessiondata.SessionActionDisconnect, 3_000, nil))
		if err != nil || got.Outcome != sessiondata.SessionActionOutcomeCompleted || calls != 1 || wait {
			t.Fatalf("completion=%#v err=%v calls=%d wait=%v", got, err, calls, wait)
		}
		assertActionTerminal(t, ledger, got.ActionID, sessiondata.SessionActionOutcomeCompleted)
	})
	for _, tc := range []struct {
		name    string
		action  sessiondata.PendingSessionAction
		lookup  sessionActionLookup
		outcome sessiondata.SessionActionOutcome
	}{
		{"expired", testPendingAction(sessiondata.SessionActionDisconnect, 2_000, nil), func(uint32) (*int64, error) { t.Fatal("lookup called after expiry"); return nil, nil }, sessiondata.SessionActionOutcomeExpired},
		{"reused", testPendingAction(sessiondata.SessionActionDisconnect, 3_000, nil), func(uint32) (*int64, error) { return new(int64(101)), nil }, sessiondata.SessionActionOutcomeSessionChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger := &memorySessionActionLedger{entries: make(map[string]sessiondata.SessionActionLedgerEntry)}
			calls := 0
			got, err := newExecutor(ledger, tc.lookup, func(uint32, bool) error { calls++; return nil }).Execute(context.Background(), tc.action)
			if err != nil || got.Outcome != tc.outcome || calls != 0 {
				t.Fatalf("completion=%#v err=%v calls=%d", got, err, calls)
			}
		})
	}
	t.Run("duplicate never calls WTS", func(t *testing.T) {
		ledger := &memorySessionActionLedger{entries: make(map[string]sessiondata.SessionActionLedgerEntry)}
		calls := 0
		action := testPendingAction(sessiondata.SessionActionDisconnect, 3_000, nil)
		executor := newExecutor(ledger, func(uint32) (*int64, error) { return new(int64(100)), nil }, func(uint32, bool) error { calls++; return nil })
		if _, err := executor.Execute(context.Background(), action); err != nil {
			t.Fatal(err)
		}
		got, err := executor.Execute(context.Background(), action)
		if err != nil || got.Outcome != sessiondata.SessionActionOutcomeDuplicate || calls != 1 {
			t.Fatalf("completion=%#v err=%v calls=%d", got, err, calls)
		}
	})
}

func TestDisconnectSessionActionPassesSessionAndNonWaitingFlag(t *testing.T) {
	originalCall := wtsDisconnectSessionCall
	defer func() { wtsDisconnectSessionCall = originalCall }()
	var gotSessionID, gotWait uintptr
	wtsDisconnectSessionCall = func(args ...uintptr) (uintptr, uintptr, error) {
		gotSessionID = args[1]
		gotWait = args[2]
		return 1, 0, nil
	}
	if err := disconnectSessionAction(42, false); err != nil {
		t.Fatal(err)
	}
	if gotSessionID != 42 || gotWait != 0 {
		t.Fatalf("WTSDisconnectSession(session=%d, wait=%d), want (42, 0)", gotSessionID, gotWait)
	}
}

func TestSessionActionExecutor_MessageSuccess(t *testing.T) {
	ledger := &memorySessionActionLedger{entries: make(map[string]sessiondata.SessionActionLedgerEntry)}
	var delivered string
	executor := newSessionActionExecutor(ledger, func() time.Time { return time.UnixMilli(2_000) }, func(uint32) (*int64, error) { return new(int64(100)), nil }, func(uint32, bool) error { t.Fatal("logoff called"); return nil }, func(_ uint32, message string) error {
		delivered = message
		return nil
	})
	got, err := executor.Execute(context.Background(), testPendingAction(sessiondata.SessionActionMessage, 3_000, new("Please save your work.")))
	if err != nil || got.Outcome != sessiondata.SessionActionOutcomeCompleted || delivered != "Please save your work." {
		t.Fatalf("completion=%#v err=%v message=%q", got, err, delivered)
	}
	assertActionTerminal(t, ledger, got.ActionID, sessiondata.SessionActionOutcomeCompleted)
}

func TestSendSessionActionMessagePassesUTF16ByteLengthsWithoutNUL(t *testing.T) {
	tests := []struct {
		name             string
		message          string
		wantTitleBytes   uintptr
		wantMessageBytes uintptr
	}{
		{name: "ASCII", message: "Save now", wantTitleBytes: 16, wantMessageBytes: 16},
		{name: "surrogate pair", message: "Ready 😀", wantTitleBytes: 16, wantMessageBytes: 16},
	}

	originalCall := wtsSendMessageCall
	defer func() { wtsSendMessageCall = originalCall }()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotTitleBytes, gotMessageBytes uintptr
			wtsSendMessageCall = func(args ...uintptr) (uintptr, uintptr, error) {
				gotTitleBytes = args[3]
				gotMessageBytes = args[5]
				return 1, 0, nil
			}

			if err := sendSessionActionMessage(4, tc.message); err != nil {
				t.Fatal(err)
			}
			if gotTitleBytes != tc.wantTitleBytes {
				t.Errorf("title length = %d bytes, want %d (excluding NUL)", gotTitleBytes, tc.wantTitleBytes)
			}
			if gotMessageBytes != tc.wantMessageBytes {
				t.Errorf("message length = %d bytes, want %d (excluding NUL)", gotMessageBytes, tc.wantMessageBytes)
			}
		})
	}
}

func TestSessionActionExecutor_RecordsSafeFailureWithoutDiagnostics(t *testing.T) {
	ledger := &memorySessionActionLedger{entries: make(map[string]sessiondata.SessionActionLedgerEntry)}
	executor := newSessionActionExecutor(ledger, func() time.Time { return time.UnixMilli(2_000) }, func(uint32) (*int64, error) {
		return nil, errors.New("alice confidential WTS failure")
	}, func(uint32, bool) error { t.Fatal("logoff called"); return nil }, func(uint32, string) error { t.Fatal("message called"); return nil })

	got, err := executor.Execute(context.Background(), testPendingAction(sessiondata.SessionActionLogoff, 3_000, nil))
	if err != nil || got.Outcome != sessiondata.SessionActionOutcomeFailed {
		t.Fatalf("completion=%#v err=%v", got, err)
	}
	if strings.Contains(got.ActionID+string(got.Outcome), "alice") {
		t.Fatalf("completion leaks lookup diagnostic: %#v", got)
	}
	assertActionTerminal(t, ledger, got.ActionID, sessiondata.SessionActionOutcomeFailed)
}

func TestSessionActionExecutor_ExpiredAndUnsupportedNeverExecuteWTS(t *testing.T) {
	for _, tc := range []struct {
		name    string
		action  sessiondata.PendingSessionAction
		outcome sessiondata.SessionActionOutcome
	}{
		{"expired", testPendingAction(sessiondata.SessionActionLogoff, 2_000, nil), sessiondata.SessionActionOutcomeExpired},
		{"unsupported", testPendingAction(sessiondata.SessionActionType("restart"), 3_000, nil), sessiondata.SessionActionOutcomeUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger := &memorySessionActionLedger{entries: make(map[string]sessiondata.SessionActionLedgerEntry)}
			executor := newSessionActionExecutor(ledger, func() time.Time { return time.UnixMilli(2_000) }, func(uint32) (*int64, error) { return new(int64(100)), nil }, func(uint32, bool) error { t.Fatal("logoff called"); return nil }, func(uint32, string) error { t.Fatal("message called"); return nil })
			got, err := executor.Execute(context.Background(), tc.action)
			if err != nil || got.Outcome != tc.outcome {
				t.Fatalf("completion=%#v err=%v", got, err)
			}
			assertActionTerminal(t, ledger, got.ActionID, tc.outcome)
		})
	}
}

func TestSessionActionExecutor_RefusesReusedSession(t *testing.T) {
	ledger := &memorySessionActionLedger{entries: make(map[string]sessiondata.SessionActionLedgerEntry)}
	executor := newSessionActionExecutor(ledger, func() time.Time { return time.UnixMilli(2_000) }, func(uint32) (*int64, error) { return new(int64(101)), nil }, func(uint32, bool) error { t.Fatal("logoff called for reused session"); return nil }, func(uint32, string) error { t.Fatal("message called for reused session"); return nil })
	got, err := executor.Execute(context.Background(), testPendingAction(sessiondata.SessionActionLogoff, 3_000, nil))
	if err != nil || got.Outcome != sessiondata.SessionActionOutcomeSessionChanged {
		t.Fatalf("completion=%#v err=%v", got, err)
	}
	assertActionTerminal(t, ledger, got.ActionID, sessiondata.SessionActionOutcomeSessionChanged)
}

func TestSessionActionExecutor_DuplicateAfterRestartDoesNotRepeatWTS(t *testing.T) {
	ledger := &memorySessionActionLedger{entries: make(map[string]sessiondata.SessionActionLedgerEntry)}
	wtsCalls := 0
	build := func() *SessionActionExecutor {
		return newSessionActionExecutor(ledger, func() time.Time { return time.UnixMilli(2_000) }, func(uint32) (*int64, error) { return new(int64(100)), nil }, func(uint32, bool) error { wtsCalls++; return nil }, func(uint32, string) error { t.Fatal("message called"); return nil })
	}
	action := testPendingAction(sessiondata.SessionActionLogoff, 3_000, nil)
	first, err := build().Execute(context.Background(), action)
	if err != nil || first.Outcome != sessiondata.SessionActionOutcomeCompleted {
		t.Fatalf("first completion=%#v err=%v", first, err)
	}
	second, err := build().Execute(context.Background(), action)
	if err != nil || second.Outcome != sessiondata.SessionActionOutcomeDuplicate || wtsCalls != 1 {
		t.Fatalf("second completion=%#v err=%v WTS calls=%d", second, err, wtsCalls)
	}
	if len(ledger.completions) != 1 {
		t.Fatalf("durable completions=%d, want 1", len(ledger.completions))
	}
}

func TestSessionActionExecutor_RedeliveredTerminalActionReplaysOriginalOutcome(t *testing.T) {
	base := &memorySessionActionLedger{entries: make(map[string]sessiondata.SessionActionLedgerEntry)}
	ledger := &readableMemorySessionActionLedger{memorySessionActionLedger: base}
	action := testPendingAction(sessiondata.SessionActionLogoff, 3_000, nil)
	first := newSessionActionExecutor(ledger, func() time.Time { return time.UnixMilli(2_000) }, func(uint32) (*int64, error) {
		return new(int64(100)), nil
	}, func(uint32, bool) error { return nil }, func(uint32, string) error { return nil })
	if _, err := first.Execute(context.Background(), action); err != nil {
		t.Fatal(err)
	}
	second := newSessionActionExecutor(ledger, func() time.Time { return time.UnixMilli(2_000) }, func(uint32) (*int64, error) {
		t.Fatal("lookup called for terminal action")
		return nil, nil
	}, func(uint32, bool) error { t.Fatal("logoff called twice"); return nil }, func(uint32, string) error { return nil })
	got, err := second.Execute(context.Background(), action)
	if err != nil || got.Outcome != sessiondata.SessionActionOutcomeCompleted {
		t.Fatalf("redelivered completion=%#v err=%v", got, err)
	}
}

func testPendingAction(actionType sessiondata.SessionActionType, expiresAtMS int64, message *string) sessiondata.PendingSessionAction {
	return sessiondata.PendingSessionAction{ActionID: "0195a584-5b25-7a00-91a5-7cbb4dac92d9", Type: actionType, SessionID: 4, ExpectedLogonAtMS: 100, ExpiresAtMS: expiresAtMS, Message: message}
}

func assertActionTerminal(t *testing.T, ledger *memorySessionActionLedger, actionID string, want sessiondata.SessionActionOutcome) {
	t.Helper()
	entry := ledger.entries[actionID]
	if entry.State != sessiondata.SessionActionLedgerTerminal || entry.Outcome == nil || *entry.Outcome != want || entry.CompletedAtMS == nil {
		t.Fatalf("ledger entry = %#v, want terminal %q", entry, want)
	}
}
