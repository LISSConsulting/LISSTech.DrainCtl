//go:build windows

package drainctl

import (
	"context"
	"errors"
	"time"
	"unsafe"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"

	"golang.org/x/sys/windows"
)

const (
	wtsMessageTitle          = "DrainCtl"
	wtsMessageTimeoutSeconds = 30
)

var (
	modWtsapi32Actions               = windows.NewLazySystemDLL("wtsapi32.dll")
	procWTSDisconnectSession         = modWtsapi32Actions.NewProc("WTSDisconnectSession")
	procWTSLogoffSession             = modWtsapi32Actions.NewProc("WTSLogoffSession")
	procWTSSendMessageW              = modWtsapi32Actions.NewProc("WTSSendMessageW")
	wtsDisconnectSessionCall wtsCall = procWTSDisconnectSession.Call
	wtsLogoffSessionCall     wtsCall = procWTSLogoffSession.Call
	wtsSendMessageCall       wtsCall = procWTSSendMessageW.Call
)

// SessionActionLedger is the durable, claim-before-WTS action ledger. Claim
// must atomically persist a claimed entry and report false for an existing ID.
type SessionActionLedger interface {
	Claim(context.Context, string, time.Time, time.Time) (bool, sessiondata.SessionActionLedgerEntry, error)
	Complete(context.Context, string, sessiondata.SessionActionOutcome, time.Time) error
}

// sessionActionLedgerEntryReader is implemented by the durable telemetry
// ledger. It lets a restarted runtime re-report the original terminal outcome
// for a redelivered command without invoking WTS again.
type sessionActionLedgerEntryReader interface {
	Entry(context.Context, string) (sessiondata.SessionActionLedgerEntry, error)
}

type sessionActionLookup func(uint32) (*int64, error)
type sessionActionDisconnect func(uint32, bool) error
type sessionActionLogoff func(uint32, bool) error
type sessionActionMessage func(uint32, string) error

// SessionActionExecutor executes locally delivered session commands exactly
// once. Its dependencies are injected so callers and tests can use a durable
// ledger and deterministic WTS/clock seams.
type SessionActionExecutor struct {
	ledger     SessionActionLedger
	now        func() time.Time
	lookup     sessionActionLookup
	disconnect sessionActionDisconnect
	logoff     sessionActionLogoff
	message    sessionActionMessage
}

func NewSessionActionExecutor(ledger SessionActionLedger) *SessionActionExecutor {
	return newSessionActionExecutorWithDisconnect(ledger, time.Now, lookupSessionActionLogon, disconnectSessionAction, logoffSessionAction, sendSessionActionMessage)
}

func newSessionActionExecutor(ledger SessionActionLedger, now func() time.Time, lookup sessionActionLookup, logoff sessionActionLogoff, message sessionActionMessage) *SessionActionExecutor {
	return newSessionActionExecutorWithDisconnect(ledger, now, lookup, disconnectSessionAction, logoff, message)
}

func newSessionActionExecutorWithDisconnect(ledger SessionActionLedger, now func() time.Time, lookup sessionActionLookup, disconnect sessionActionDisconnect, logoff sessionActionLogoff, message sessionActionMessage) *SessionActionExecutor {
	return &SessionActionExecutor{ledger: ledger, now: now, lookup: lookup, disconnect: disconnect, logoff: logoff, message: message}
}

// Execute claims the action before inspecting or affecting the target. A
// duplicate claim never invokes WTS. Returned results contain only the action
// ID and safe closed-vocabulary outcome; WTS diagnostics are deliberately not
// propagated.
func (executor *SessionActionExecutor) Execute(ctx context.Context, action sessiondata.PendingSessionAction) (sessiondata.CompletedSessionAction, error) {
	now := executor.now()
	claimed, _, err := executor.ledger.Claim(ctx, action.ActionID, time.UnixMilli(action.ExpiresAtMS), now)
	if err != nil {
		return sessiondata.CompletedSessionAction{}, errors.New("claim session action")
	}
	if !claimed {
		if reader, ok := executor.ledger.(sessionActionLedgerEntryReader); ok {
			entry, entryErr := reader.Entry(ctx, action.ActionID)
			if entryErr == nil && entry.State == sessiondata.SessionActionLedgerTerminal && entry.Outcome != nil {
				return completedSessionAction(action.ActionID, *entry.Outcome), nil
			}
		}
		return completedSessionAction(action.ActionID, sessiondata.SessionActionOutcomeDuplicate), nil
	}

	if now.UnixMilli() >= action.ExpiresAtMS {
		return executor.recordTerminal(ctx, action.ActionID, sessiondata.SessionActionOutcomeExpired)
	}

	logonAtMS, err := executor.lookup(action.SessionID)
	if err != nil || logonAtMS == nil || *logonAtMS != action.ExpectedLogonAtMS {
		if err != nil || logonAtMS == nil {
			return executor.recordTerminal(ctx, action.ActionID, sessiondata.SessionActionOutcomeFailed)
		}
		return executor.recordTerminal(ctx, action.ActionID, sessiondata.SessionActionOutcomeSessionChanged)
	}

	switch action.Type {
	case sessiondata.SessionActionDisconnect:
		if executor.now().UnixMilli() >= action.ExpiresAtMS {
			return executor.recordTerminal(ctx, action.ActionID, sessiondata.SessionActionOutcomeExpired)
		}
		if err := executor.disconnect(action.SessionID, false); err != nil {
			return executor.recordTerminal(ctx, action.ActionID, sessiondata.SessionActionOutcomeFailed)
		}
	case sessiondata.SessionActionLogoff:
		if executor.now().UnixMilli() >= action.ExpiresAtMS {
			return executor.recordTerminal(ctx, action.ActionID, sessiondata.SessionActionOutcomeExpired)
		}
		if err := executor.logoff(action.SessionID, false); err != nil {
			return executor.recordTerminal(ctx, action.ActionID, sessiondata.SessionActionOutcomeFailed)
		}
	case sessiondata.SessionActionMessage:
		if action.Message == nil {
			return executor.recordTerminal(ctx, action.ActionID, sessiondata.SessionActionOutcomeUnsupported)
		}
		if executor.now().UnixMilli() >= action.ExpiresAtMS {
			return executor.recordTerminal(ctx, action.ActionID, sessiondata.SessionActionOutcomeExpired)
		}
		if err := executor.message(action.SessionID, *action.Message); err != nil {
			return executor.recordTerminal(ctx, action.ActionID, sessiondata.SessionActionOutcomeFailed)
		}
	default:
		return executor.recordTerminal(ctx, action.ActionID, sessiondata.SessionActionOutcomeUnsupported)
	}
	return executor.recordTerminal(ctx, action.ActionID, sessiondata.SessionActionOutcomeCompleted)
}

func (executor *SessionActionExecutor) recordTerminal(ctx context.Context, actionID string, outcome sessiondata.SessionActionOutcome) (sessiondata.CompletedSessionAction, error) {
	if err := executor.ledger.Complete(ctx, actionID, outcome, executor.now()); err != nil {
		return sessiondata.CompletedSessionAction{}, errors.New("complete session action")
	}
	return completedSessionAction(actionID, outcome), nil
}

func completedSessionAction(actionID string, outcome sessiondata.SessionActionOutcome) sessiondata.CompletedSessionAction {
	return sessiondata.CompletedSessionAction{ActionID: actionID, Outcome: outcome}
}

func lookupSessionActionLogon(sessionID uint32) (*int64, error) {
	records, err := EnumerateSessionRecords()
	if err != nil {
		return nil, err
	}
	for i := range records {
		if records[i].SessionID == sessionID {
			return records[i].LogonAtMS, nil
		}
	}
	return nil, nil
}

func disconnectSessionAction(sessionID uint32, wait bool) error {
	waitFlag := uintptr(0)
	if wait {
		waitFlag = 1
	}
	ret, _, err := wtsDisconnectSessionCall(0, uintptr(sessionID), waitFlag)
	if ret == 0 {
		return err
	}
	return nil
}

func logoffSessionAction(sessionID uint32, wait bool) error {
	waitFlag := uintptr(0)
	if wait {
		waitFlag = 1
	}
	ret, _, err := wtsLogoffSessionCall(0, uintptr(sessionID), waitFlag)
	if ret == 0 {
		return err
	}
	return nil
}

func sendSessionActionMessage(sessionID uint32, message string) error {
	title, err := windows.UTF16FromString(wtsMessageTitle)
	if err != nil {
		return err
	}
	text, err := windows.UTF16FromString(message)
	if err != nil {
		return err
	}
	var response uint32
	ret, _, callErr := wtsSendMessageCall(
		0,
		uintptr(sessionID),
		uintptr(unsafe.Pointer(&title[0])),
		uintptr((len(title)-1)*2),
		uintptr(unsafe.Pointer(&text[0])),
		uintptr((len(text)-1)*2),
		0,
		wtsMessageTimeoutSeconds,
		uintptr(unsafe.Pointer(&response)),
		0,
	)
	if ret == 0 {
		return callErr
	}
	return nil
}
