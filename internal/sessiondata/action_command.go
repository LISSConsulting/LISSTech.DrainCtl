package sessiondata

import (
	"fmt"

	"github.com/google/uuid"
)

// MaxSessionActionCommands is the maximum number of action commands included in
// one report response or completion batch.
const MaxSessionActionCommands = 20

// PendingSessionAction is a bounded command delivered to an agent in a normal
// report response. Message is present only for message actions.
type PendingSessionAction struct {
	ActionID          string            `json:"action_id"`
	Type              SessionActionType `json:"type"`
	SessionID         uint32            `json:"session_id"`
	ExpectedLogonAtMS int64             `json:"expected_logon_at_ms"`
	ExpiresAtMS       int64             `json:"expires_at_ms"`
	Message           *string           `json:"message,omitempty"`
}

// CompletedSessionAction is the privacy-safe terminal action result reported
// by an agent. It intentionally has no diagnostics or target details.
type CompletedSessionAction struct {
	ActionID string               `json:"action_id"`
	Outcome  SessionActionOutcome `json:"outcome"`
}

func ValidatePendingSessionActions(actions []PendingSessionAction) error {
	if len(actions) > MaxSessionActionCommands {
		return fmt.Errorf("too many pending session actions")
	}
	for i := range actions {
		if err := actions[i].Validate(); err != nil {
			return fmt.Errorf("pending session action %d: %w", i, err)
		}
	}
	return nil
}

func ValidateCompletedSessionActions(actions []CompletedSessionAction) error {
	if len(actions) > MaxSessionActionCommands {
		return fmt.Errorf("too many completed session actions")
	}
	for i := range actions {
		if err := actions[i].Validate(); err != nil {
			return fmt.Errorf("completed session action %d: %w", i, err)
		}
	}
	return nil
}

// ValidateAcknowledgedSessionActionIDs validates the bounded, privacy-safe
// identifiers returned by the dashboard after it has recorded completions.
func ValidateAcknowledgedSessionActionIDs(ids []string) error {
	if len(ids) > MaxSessionActionCommands {
		return fmt.Errorf("too many acknowledged session action IDs")
	}
	for i, id := range ids {
		if _, err := uuid.Parse(id); err != nil {
			return fmt.Errorf("acknowledged session action ID %d is invalid", i)
		}
	}
	return nil
}

func (action PendingSessionAction) Validate() error {
	if _, err := uuid.Parse(action.ActionID); err != nil {
		return fmt.Errorf("action ID is invalid")
	}
	if err := validateTimestamp(action.ExpectedLogonAtMS); err != nil {
		return fmt.Errorf("expected logon time: %w", err)
	}
	if err := validateTimestamp(action.ExpiresAtMS); err != nil {
		return fmt.Errorf("expiry time: %w", err)
	}
	if action.ExpiresAtMS < action.ExpectedLogonAtMS {
		return fmt.Errorf("expiry time precedes expected logon time")
	}
	switch action.Type {
	case SessionActionLogoff, SessionActionDisconnect:
		if action.Message != nil && *action.Message != "" {
			return fmt.Errorf("%s message must be empty", action.Type)
		}
	case SessionActionMessage:
		if action.Message == nil {
			return fmt.Errorf("message is required")
		}
		if err := validateActionMessage(*action.Message); err != nil {
			return err
		}
	default:
		return fmt.Errorf("action type is invalid")
	}
	return nil
}

func (action CompletedSessionAction) Validate() error {
	if _, err := uuid.Parse(action.ActionID); err != nil {
		return fmt.Errorf("action ID is invalid")
	}
	if !action.Outcome.Valid() {
		return fmt.Errorf("action outcome is invalid")
	}
	return nil
}
