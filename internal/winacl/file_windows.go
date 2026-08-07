//go:build windows

// Package winacl applies Windows security descriptors without spawning helper
// processes. Callers choose the exact well-known principals and access masks.
package winacl

import (
	"fmt"
	"runtime"

	"golang.org/x/sys/windows"
)

// Grant describes one allowed well-known Windows principal.
type Grant struct {
	SIDType     windows.WELL_KNOWN_SID_TYPE
	Permissions windows.ACCESS_MASK
}

// RestrictFile replaces path's DACL with explicit grants and disables inherited
// ACEs. An empty grant set is rejected to avoid accidentally creating an
// inaccessible file.
func RestrictFile(path string, grants ...Grant) error {
	if len(grants) == 0 {
		return fmt.Errorf("restrict file ACL: no grants")
	}

	entries := make([]windows.EXPLICIT_ACCESS, len(grants))
	sids := make([]*windows.SID, len(grants))
	var pinner runtime.Pinner
	defer pinner.Unpin()

	for i, grant := range grants {
		sid, err := windows.CreateWellKnownSid(grant.SIDType)
		if err != nil {
			return fmt.Errorf("create well-known SID %d: %w", grant.SIDType, err)
		}
		sids[i] = sid
		pinner.Pin(sid)
		entries[i] = windows.EXPLICIT_ACCESS{
			AccessPermissions: grant.Permissions,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		}
	}

	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("build file DACL: %w", err)
	}
	securityInfo := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, securityInfo, nil, nil, acl, nil); err != nil {
		return fmt.Errorf("set file DACL: %w", err)
	}
	return nil
}
