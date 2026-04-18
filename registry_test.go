//go:build windows

package drainctl

import "testing"

func TestDrainMode_String(t *testing.T) {
	cases := []struct {
		m    DrainMode
		want string
	}{
		{AllowAll, "ALLOW_ALL_CONNECTIONS"},
		// Windows `TSServerDrainMode` semantics: value 1 = until reboot
		// (temporary), value 2 = until manually cleared (persistent). The
		// drainctl labels follow the same convention — value 1 carries the
		// "_UNTIL_RESTART" suffix, value 2 does not.
		{DrainUntilBoot, "ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS_UNTIL_RESTART"},
		{DrainPersistent, "ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS"},
		{DenyAll, "DENY_ALL_CONNECTIONS"},
		{DrainMode(99), "UNKNOWN(99)"},
		// Deprecated alias compatibility: the old names should still resolve
		// to the same integer values (no silent behavior shift for callers
		// migrating across releases).
		{PreventNewLogon, "ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS_UNTIL_RESTART"},
		{PreventUntilRST, "ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS"},
	}
	for _, c := range cases {
		if got := c.m.String(); got != c.want {
			t.Errorf("DrainMode(%d).String() = %q, want %q", c.m, got, c.want)
		}
	}
}
