//go:build windows

package pipe

import (
	"errors"
	"net"
	"testing"
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
