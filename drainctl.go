//go:build windows

// Package drainctl monitors Remote Desktop Session Host drain mode
// (TSServerDrainMode registry value) on Windows servers.
package drainctl

import "os"

// Version is the library version. Overridable via ldflags.
var Version = "26.94.6"

// DefaultDataDir returns the default directory for drainctl data files.
func DefaultDataDir() string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return pd + `\LISS Technologies\LISSTech DrainCtl`
}

// DefaultAuditPath returns the default path for the JSONL audit trail.
func DefaultAuditPath() string {
	return DefaultDataDir() + `\audit.jsonl`
}
