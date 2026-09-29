package sessiondata

import (
	"fmt"
	"strings"
	"testing"
)

func TestPendingSessionActionValidate_MessageBounds(t *testing.T) {
	validID := "0195a584-5b25-7a00-91a5-7cbb4dac92d9"
	cases := []struct {
		name    string
		action  PendingSessionAction
		wantErr bool
	}{
		{"message valid", PendingSessionAction{ActionID: validID, Type: SessionActionMessage, ExpectedLogonAtMS: 1, ExpiresAtMS: 2, Message: new("Please save your work.\nThanks.")}, false},
		{"message required", PendingSessionAction{ActionID: validID, Type: SessionActionMessage, ExpectedLogonAtMS: 1, ExpiresAtMS: 2}, true},
		{"message empty", PendingSessionAction{ActionID: validID, Type: SessionActionMessage, ExpectedLogonAtMS: 1, ExpiresAtMS: 2, Message: new("")}, true},
		{"message rune limit", PendingSessionAction{ActionID: validID, Type: SessionActionMessage, ExpectedLogonAtMS: 1, ExpiresAtMS: 2, Message: new(strings.Repeat("😀", 257))}, true},
		{"message control", PendingSessionAction{ActionID: validID, Type: SessionActionMessage, ExpectedLogonAtMS: 1, ExpiresAtMS: 2, Message: new("unsafe\x00")}, true},
		{"logoff empty message", PendingSessionAction{ActionID: validID, Type: SessionActionLogoff, ExpectedLogonAtMS: 1, ExpiresAtMS: 2, Message: new("")}, false},
		{"logoff text rejected", PendingSessionAction{ActionID: validID, Type: SessionActionLogoff, ExpectedLogonAtMS: 1, ExpiresAtMS: 2, Message: new("no")}, true},
		{"disconnect no message", PendingSessionAction{ActionID: validID, Type: SessionActionDisconnect, ExpectedLogonAtMS: 1, ExpiresAtMS: 2}, false},
		{"disconnect empty message", PendingSessionAction{ActionID: validID, Type: SessionActionDisconnect, ExpectedLogonAtMS: 1, ExpiresAtMS: 2, Message: new("")}, false},
		{"disconnect text rejected", PendingSessionAction{ActionID: validID, Type: SessionActionDisconnect, ExpectedLogonAtMS: 1, ExpiresAtMS: 2, Message: new("no")}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.action.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, want error=%v", err, tc.wantErr)
			}
		})
	}
}

func TestSessionActionWireBatchBounds(t *testing.T) {
	pending := make([]PendingSessionAction, MaxSessionActionCommands+1)
	completed := make([]CompletedSessionAction, MaxSessionActionCommands+1)
	for i := range pending {
		id := fmt.Sprintf("0195a584-5b25-7a00-91a5-%012d", i)
		pending[i] = PendingSessionAction{ActionID: id, Type: SessionActionLogoff, ExpectedLogonAtMS: 1, ExpiresAtMS: 2}
		completed[i] = CompletedSessionAction{ActionID: id, Outcome: SessionActionOutcomeCompleted}
	}
	if err := ValidatePendingSessionActions(pending); err == nil {
		t.Fatal("ValidatePendingSessionActions accepted 21 commands")
	}
	if err := ValidateCompletedSessionActions(completed); err == nil {
		t.Fatal("ValidateCompletedSessionActions accepted 21 commands")
	}
}

func TestValidateAcknowledgedSessionActionIDs(t *testing.T) {
	validID := "0195a584-5b25-7a00-91a5-7cbb4dac92d9"
	if err := ValidateAcknowledgedSessionActionIDs([]string{validID}); err != nil {
		t.Fatalf("ValidateAcknowledgedSessionActionIDs() error = %v", err)
	}
	if err := ValidateAcknowledgedSessionActionIDs([]string{"not-an-id"}); err == nil {
		t.Fatal("ValidateAcknowledgedSessionActionIDs accepted invalid ID")
	}
	ids := make([]string, MaxSessionActionCommands+1)
	for i := range ids {
		ids[i] = validID
	}
	if err := ValidateAcknowledgedSessionActionIDs(ids); err == nil {
		t.Fatal("ValidateAcknowledgedSessionActionIDs accepted 21 IDs")
	}
}
