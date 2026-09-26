//go:build windows

package pipe

import (
	"errors"
	"net"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCallerIsPrivileged(t *testing.T) {
	tests := []struct {
		name    string
		info    callerTokenResult
		err     error
		wantOK  bool
		wantSID string
	}{
		{name: "system", info: callerTokenResult{sidString: "S-1-5-18", isSystem: true}, wantOK: true, wantSID: "S-1-5-18"},
		{name: "admin", info: callerTokenResult{sidString: "S-1-5-21-1-2-3-1001", isAdmin: true}, wantOK: true, wantSID: "S-1-5-21-1-2-3-1001"},
		{name: "non admin", info: callerTokenResult{sidString: "S-1-5-21-1-2-3-1002"}, wantSID: "S-1-5-21-1-2-3-1002"},
		{name: "filtered admin", info: callerTokenResult{sidString: "S-1-5-21-1-2-3-1003"}, wantSID: "S-1-5-21-1-2-3-1003"},
		{name: "unknown sid", info: callerTokenResult{sidString: "S-1-0-0"}, wantSID: "S-1-0-0"},
		{name: "token open error", info: callerTokenResult{sidString: "S-1-5-21-1-2-3-1004"}, err: errors.New("OpenProcessToken: access denied"), wantSID: "S-1-5-21-1-2-3-1004"},
		{name: "process open error", err: errors.New("OpenProcess: process exited")},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			oldRead := readCallerToken
			readCallerToken = func(net.Conn) (callerTokenResult, error) {
				return tc.info, tc.err
			}
			t.Cleanup(func() { readCallerToken = oldRead })

			gotOK, gotSID, err := callerIsPrivileged(nil)
			if gotOK != tc.wantOK {
				t.Fatalf("callerIsPrivileged OK = %v, want %v", gotOK, tc.wantOK)
			}
			if gotSID != tc.wantSID {
				t.Fatalf("callerIsPrivileged SID = %q, want %q", gotSID, tc.wantSID)
			}
			if tc.err == nil && err != nil {
				t.Fatalf("callerIsPrivileged error = %v, want nil", err)
			}
			if tc.err != nil {
				if err == nil {
					t.Fatal("callerIsPrivileged error = nil, want non-nil")
				}
				if err.Error() != tc.err.Error() {
					t.Fatalf("callerIsPrivileged error = %q, want %q", err.Error(), tc.err.Error())
				}
			}
		})
	}
}

func TestTokenIsAdministratorDuplicatesPrimaryToken(t *testing.T) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &token); err != nil {
		t.Fatalf("OpenProcessToken(current process): %v", err)
	}
	defer func() { _ = token.Close() }()

	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatalf("CreateWellKnownSid(Administrators): %v", err)
	}

	_, err = tokenIsAdministrator(token, adminSID)
	if errors.Is(err, windows.ERROR_NO_IMPERSONATION_TOKEN) {
		t.Fatalf("tokenIsAdministrator returned ERROR_NO_IMPERSONATION_TOKEN: %v", err)
	}
	if err != nil {
		t.Fatalf("tokenIsAdministrator: %v", err)
	}
}

func TestTokenIsAdministratorReturnsDuplicateError(t *testing.T) {
	oldDuplicateTokenEx := duplicateTokenEx
	duplicateTokenEx = func(
		windows.Token,
		uint32,
		*windows.SecurityAttributes,
		uint32,
		uint32,
		*windows.Token,
	) error {
		return windows.ERROR_ACCESS_DENIED
	}
	t.Cleanup(func() { duplicateTokenEx = oldDuplicateTokenEx })

	isAdmin, err := tokenIsAdministrator(0, nil)
	if isAdmin {
		t.Fatal("tokenIsAdministrator returned admin membership after DuplicateTokenEx failure")
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("tokenIsAdministrator error = %v, want wrapped ERROR_ACCESS_DENIED", err)
	}
	if !strings.Contains(err.Error(), "DuplicateTokenEx") {
		t.Fatalf("tokenIsAdministrator error = %q, want DuplicateTokenEx context", err)
	}
}

func TestTokenIsAdministratorReturnsMemberError(t *testing.T) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &token); err != nil {
		t.Fatalf("OpenProcessToken(current process): %v", err)
	}
	defer func() { _ = token.Close() }()

	oldTokenIsMember := tokenIsMember
	tokenIsMember = func(windows.Token, *windows.SID) (bool, error) {
		return false, windows.ERROR_ACCESS_DENIED
	}
	t.Cleanup(func() { tokenIsMember = oldTokenIsMember })

	isAdmin, err := tokenIsAdministrator(token, nil)
	if isAdmin {
		t.Fatal("tokenIsAdministrator returned admin membership after Token.IsMember failure")
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("tokenIsAdministrator error = %v, want wrapped ERROR_ACCESS_DENIED", err)
	}
	if !strings.Contains(err.Error(), "Token.IsMember") {
		t.Fatalf("tokenIsAdministrator error = %q, want Token.IsMember context", err)
	}
}
