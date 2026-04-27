//go:build windows

package updater

import (
	"context"
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
var execCommand = exec.Command

// spawnInstall launches msiexec on the supplied MSI path as a detached
// process so it survives the service's exit. The Go process MUST NOT Wait
// on the returned cmd — Wait would block until msiexec finishes, but
// msiexec wants to stop the service (us) as part of finishing, which
// would deadlock. Instead we Start, Release, and return.
//
// The caller is expected to immediately trigger the service's own clean
// shutdown so the binary files are unlocked before msiexec asks SCM to
// stop the service. See triggerSelfShutdown.
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

// triggerSelfShutdown invokes the supplied service-level cancel func.
// Wrapping it here keeps the spawnInstall caller (the updater Subsystem)
// free of direct context handling at the install boundary, and makes the
// "we exit so msiexec can replace files" intent explicit at the point
// of decision.
func triggerSelfShutdown(cancel context.CancelFunc) {
	if cancel == nil {
		slog.Warn("update: triggerSelfShutdown called with nil cancel — service ctx not wired")
		return
	}
	slog.Info("update: triggering service shutdown so msiexec can replace files")
	cancel()
}
