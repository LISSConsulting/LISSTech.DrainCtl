//go:build windows

package evtspike

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ErrPrivilegeNotAssigned signals that the current process token does not hold
// SeSecurityPrivilege. AdjustTokenPrivileges returns success with
// ERROR_NOT_ALL_ASSIGNED in this case; callers treat it as "skip Security
// subscription" rather than a fatal startup error.
var ErrPrivilegeNotAssigned = errors.New("evtspike: SeSecurityPrivilege not assigned to process token")

var (
	advapi32                  = windows.NewLazySystemDLL("advapi32.dll")
	procAdjustTokenPrivileges = advapi32.NewProc("AdjustTokenPrivileges")
)

// EnableSecurityPrivilege enables SeSecurityPrivilege on the current process
// token. LocalSystem (the default DrainCtl service account) has this privilege
// present in the Disabled state by default; dedicated service accounts must be
// granted the right manually. When the privilege is absent from the token,
// AdjustTokenPrivileges reports success but GetLastError returns
// ERROR_NOT_ALL_ASSIGNED — the x/sys wrapper silently ignores that, so this
// function calls the procedure directly to surface it as
// ErrPrivilegeNotAssigned.
func EnableSecurityPrivilege() error {
	return adjustSecurityPrivilege(windows.SE_PRIVILEGE_ENABLED)
}

// DisableSecurityPrivilege clears SE_PRIVILEGE_ENABLED on SeSecurityPrivilege
// so the process token reverts to not holding the privilege while still
// retaining it in its "available" set. Called on Security-channel opt-out
// (plan Phase A3) so a later AddedChannels=Security attempt is rejected at
// EvtSubscribe time with ErrPrivilegeNotAssigned instead of silently
// succeeding.
func DisableSecurityPrivilege() error {
	return adjustSecurityPrivilege(0)
}

// adjustSecurityPrivilege is the shared AdjustTokenPrivileges body used by
// both Enable and Disable. attrs is SE_PRIVILEGE_ENABLED or 0.
func adjustSecurityPrivilege(attrs uint32) error {
	var token windows.Token
	if err := windows.OpenProcessToken(
		windows.CurrentProcess(),
		windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY,
		&token,
	); err != nil {
		return fmt.Errorf("OpenProcessToken: %w", err)
	}
	defer func() { _ = token.Close() }()

	name, err := windows.UTF16PtrFromString("SeSecurityPrivilege")
	if err != nil {
		return fmt.Errorf("UTF16PtrFromString: %w", err)
	}

	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, name, &luid); err != nil {
		return fmt.Errorf("LookupPrivilegeValue SeSecurityPrivilege: %w", err)
	}

	tp := windows.Tokenprivileges{
		PrivilegeCount: 1,
		Privileges: [1]windows.LUIDAndAttributes{
			{Luid: luid, Attributes: attrs},
		},
	}

	r, _, e := procAdjustTokenPrivileges.Call(
		uintptr(token),
		0,
		uintptr(unsafe.Pointer(&tp)),
		unsafe.Sizeof(tp),
		0, 0,
	)
	if r == 0 {
		return fmt.Errorf("AdjustTokenPrivileges: %w", e)
	}
	if errors.Is(e, windows.ERROR_NOT_ALL_ASSIGNED) {
		return ErrPrivilegeNotAssigned
	}
	return nil
}
