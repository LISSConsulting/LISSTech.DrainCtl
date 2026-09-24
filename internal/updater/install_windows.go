//go:build windows

package updater

import (
	"fmt"
	"log/slog"
	"os/exec"

	"golang.org/x/sys/windows"
)

// Win32 process-creation flags (mirrored from windows.h since
// golang.org/x/sys/windows exports CREATE_NEW_PROCESS_GROUP but not
// DETACHED_PROCESS as of the version pinned in go.mod).
const (
	createNewProcessGroup = windows.CREATE_NEW_PROCESS_GROUP
	detachedProcess       = 0x00000008
)

// execCommand is a test seam wrapping exec.Command — tests swap it for a
// fake that records arguments without actually launching msiexec.
//
// NOT goroutine-safe. Tests that swap execCommand MUST NOT call
// t.Parallel(); the package-level var is shared. The integration test
// suite in this package is sequential by convention.
var execCommand = exec.Command

// spawnInstall launches msiexec on the supplied MSI path as a detached
// process and returns without waiting. Windows Installer owns the service
// stop/install/start sequence through the MSI's ServiceControl and custom
// actions; the running service must not preemptively stop itself because a
// later installer failure would leave it offline.
func spawnInstall(msiPath string) error {
	cmd := execCommand("msiexec", "/i", msiPath, "/quiet", "/norestart")
	cmd.SysProcAttr = &windows.SysProcAttr{
		CreationFlags: createNewProcessGroup | detachedProcess,
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("update: spawn msiexec: %w", err)
	}
	if cmd.Process != nil {
		// Release the OS handle so the Go runtime stops tracking the
		// child. Without this, the child becomes a zombie when our
		// process exits before it finishes (because we never Wait).
		if err := cmd.Process.Release(); err != nil {
			slog.Warn("update: msiexec Process.Release failed", "error", err)
		}
	}
	return nil
}
