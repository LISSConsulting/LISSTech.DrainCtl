//go:build windows

package pipe

import (
	"fmt"
	"net"

	"golang.org/x/sys/windows"
)

type callerTokenResult struct {
	sidString string
	isSystem  bool
	isAdmin   bool
}

var readCallerToken = readCallerTokenWindows

func callerIsPrivileged(conn net.Conn) (bool, string, error) {
	info, err := readCallerToken(conn)
	if err != nil {
		return false, info.sidString, err
	}
	return info.isSystem || info.isAdmin, info.sidString, nil
}

func readCallerTokenWindows(conn net.Conn) (callerTokenResult, error) {
	pc, ok := conn.(*pipeConn)
	if !ok {
		return callerTokenResult{}, fmt.Errorf("pipe auth: unsupported conn type %T", conn)
	}

	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(pc.handle, &pid); err != nil {
		return callerTokenResult{}, fmt.Errorf("pipe auth: GetNamedPipeClientProcessId: %w", err)
	}

	procHandle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return callerTokenResult{}, fmt.Errorf("pipe auth: OpenProcess(%d): %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(procHandle) }()

	var token windows.Token
	if err := windows.OpenProcessToken(procHandle, windows.TOKEN_QUERY, &token); err != nil {
		return callerTokenResult{}, fmt.Errorf("pipe auth: OpenProcessToken(%d): %w", pid, err)
	}
	defer func() { _ = token.Close() }()

	tokenUser, err := token.GetTokenUser()
	if err != nil {
		return callerTokenResult{}, fmt.Errorf("pipe auth: GetTokenUser(%d): %w", pid, err)
	}

	sidStr := tokenUser.User.Sid.String()

	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return callerTokenResult{sidString: sidStr}, fmt.Errorf("pipe auth: CreateWellKnownSid(SYSTEM): %w", err)
	}
	isSystem := tokenUser.User.Sid.Equals(systemSID)

	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return callerTokenResult{sidString: sidStr}, fmt.Errorf("pipe auth: CreateWellKnownSid(Administrators): %w", err)
	}
	isAdmin, err := token.IsMember(adminSID)
	if err != nil {
		return callerTokenResult{sidString: sidStr}, fmt.Errorf("pipe auth: Token.IsMember(%d): %w", pid, err)
	}

	return callerTokenResult{
		sidString: sidStr,
		isSystem:  isSystem,
		isAdmin:   isAdmin,
	}, nil
}
