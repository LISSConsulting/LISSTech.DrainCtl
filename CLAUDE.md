# CLAUDE.md

## Build
```
just all        # unsigned: CLI + DLL + PS module + MSI
just release    # signed (needs CODE_SIGNING_CERTIFICATE_THUMBPRINT in .env)
just lint       # go vet + gofmt + golangci-lint
just resource   # recompile .syso after icon/version changes
```
Requires: Go 1.26+, MinGW, WiX 5, .NET SDK 8+.

## Key Rules
- Every `.go` file needs `//go:build windows`
- Version is CalVer `YY.DOY.patch` — update in **7 places**: `drainctl.go`, `drainctl.rc`, `.psd1`, `.wixproj`, `README.md`, `CLAUDE.md`, `docs/index.html`
- After changing `.rc`: run `just resource` to recompile `.syso`
- Company: "LISS Consulting, Corp." (legal), "LISS Technologies" (d/b/a)
- No viper — config lives in `%ProgramData%\LISS Technologies\LISSTech DrainCtl\config.json` (encoding/json)
- Retention capped 1–365 days via `ClampRetention()`
- Branches: `trunk` (protected) ← PR from `develop` ← feature branches
- Pre-commit: `prek` runs gofmt, go vet, golangci-lint, gitleaks
- Signing order: sign binaries → build MSI → sign MSI (`just release` handles this)

## Gotchas
- **No `structuredClone()` on Svelte 5 state** — `$state` objects are Proxies; `structuredClone()` throws. Use `JSON.parse(JSON.stringify(...))` to deep-clone reactive state.
- **Memory thresholds: Go stores % free, UI works in % used** — `api.js` `fetchSettings()`/`saveSettings()` handles the inversion. Do NOT invert a second time in components; ConfigModal, presets, and validation all operate in % used space.

## Architecture
Root package = public API. `cmd/drainctl/` = CLI (cobra). `cmd/cshared/` = DLL (P/Invoke).
Service uses `RegNotifyChangeKeyValue` + `EvtSubscribe` + poll ticker + config file watcher.
CLI/DLL try named pipe to service first, fall back to direct registry read.
`MemAuditStore` = in-memory + JSONL flush. `AuditStore` = file-only (CLI fallback).
Config: JSON file with atomic writes (named mutex + MoveFileEx). Scoped updaters for dashboard API.
Notifications: multi-target (N webhook + M ntfy), granular triggers, per-target repeat intervals.
Sessions: `WTSEnumerateSessionsW` via wtsapi32.dll, utilization alerts at configurable threshold.
Dashboard chart: uPlot (inline ~50KB). Session gauges per server card.
Logging: slog-based, dual-sink — ETW manifest provider "LISS Technologies-DrainCtl" (Operational channel INF+, Debug channel DBG, disabled by default) + file log (`%ProgramData%\...\drainctl.log`, 10 MB rotate, 7 kept, local timestamps). Per-sink levels in `config.json` (`log_file_level`, `log_event_level`). CLI uses `--log-level debug|info|warn|error` flag (default `info`).
