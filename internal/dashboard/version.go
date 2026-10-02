//go:build windows

package dashboard

// packageVersion is the ldflags injection target mirroring drainctl.go's
// `var Version = "dev"`. The release justfile injects the git-derived
// CalVer string at build time (-X ...internal/dashboard.packageVersion=$ver).
// At test time it defaults to "dev".
var packageVersion = "dev"

// ResolveGeneratorVersion returns the version string embedded by the
// justfile at build time, prefixed with "DrainCtl v". Used as the
// `generator` field on the Excel Context sheet.
func ResolveGeneratorVersion() string {
	return "DrainCtl v" + packageVersion
}
