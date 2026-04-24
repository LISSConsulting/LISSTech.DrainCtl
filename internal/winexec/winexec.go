//go:build windows

// Package winexec wraps os/exec with Windows console-window suppression.
//
// Every Go binary in this tree runs on Windows Server, usually inside an
// MSI custom action or the background service. Those contexts have no
// owning console, so when a Go program shells out to net.exe, icacls.exe,
// powershell.exe, auditpol.exe, wevtutil.exe, etc., Windows briefly pops
// a new console window on the interactive desktop — the "flashing black
// box" operators see during install. Adding CREATE_NO_WINDOW to every
// child process suppresses it.
//
// Use winexec.Command / winexec.CommandContext anywhere the code was
// previously calling exec.Command / exec.CommandContext on a native
// Windows tool. The interface is otherwise identical.
package winexec

import (
	"context"
	"os/exec"
	"syscall"
)

// createNoWindow is the Windows process-creation flag that keeps the
// kernel from allocating a console for the child — not currently
// exported by the stdlib syscall package, so inline the literal
// (documented value; see MSDN CreateProcess flags).
const createNoWindow uint32 = 0x08000000

// hiddenAttr is the SysProcAttr that both hides the startup-info window
// AND passes CREATE_NO_WINDOW. HideWindow alone governs the legacy
// wShowWindow channel; CREATE_NO_WINDOW tells the kernel not to
// allocate a console at all. Belt-and-suspenders — some tools honour
// one flag and not the other depending on subsystem.
func hiddenAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}

// Command is a drop-in replacement for exec.Command with console-window
// suppression pre-set.
func Command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = hiddenAttr()
	return cmd
}

// CommandContext is a drop-in replacement for exec.CommandContext with
// console-window suppression pre-set.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = hiddenAttr()
	return cmd
}
