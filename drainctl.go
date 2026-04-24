//go:build windows

// Package drainctl monitors Remote Desktop Session Host drain mode
// (TSServerDrainMode registry value) on Windows servers.
package drainctl

import "os"

// Version is the library version. Injected at build time via ldflags
// (see scripts/version.ps1 and the `cli` / `dll` recipes in justfile).
// The default "dev" shows up only for plain `go build` invocations that
// skip the just recipes.
var Version = "dev"

const ServiceBinaryName = "drainctld.exe"

// DefaultDataDir returns the default directory for drainctl data files.
func DefaultDataDir() string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return pd + `\LISS Technologies\LISSTech DrainCtl`
}

// DefaultDBDir returns the default directory containing the drainctl SQLite DB.
func DefaultDBDir() string {
	return DefaultDataDir()
}

// DefaultDBPath returns the default path for the drainctl SQLite DB.
func DefaultDBPath() string {
	return DefaultDataDir() + `\drainctl.db`
}

// Deprecated: use DefaultDBPath.
func DefaultAuditPath() string {
	return DefaultDBPath()
}
