# CLAUDE.md

## Build
```
just all        # unsigned: CLI + DLL + PS module + MSI
just release    # signed (needs CODE_SIGNING_CERTIFICATE_THUMBPRINT in .env)
just lint       # go vet + gofmt + golangci-lint
just resource   # re-render drainctl.rc from tmpl + recompile .syso
just version    # print the version the next build will embed
```
Requires: Go 1.26+, MinGW, WiX 5, .NET SDK 8+.

## Key Rules
- Every `.go` file needs `//go:build windows`
- Version is git-derived CalVer `YY.DOY.N` via `scripts/version.ps1` — injected into Go at build time (ldflags), into `drainctl.rc`/`drainctl.syso` via `just resource`, into `.wixproj` via `-p:ProductVersion=`, into the PS module via `.psd1.tmpl` rendering. Nothing to bump by hand.
- The only version strings still stored in git are `docs/index.html` release-notes content, which updates manually on release refreshes (not per commit).
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
Telemetry: single SQLite DB (`drainctl.db`, WAL, via `modernc.org/sqlite` — no cgo) backs both metrics and audit. Metrics flow `metrics_raw` → `metrics_5min` → `metrics_hourly` via a watermarked aggregator; retention is configurable per tier. Audit is append-only (`telemetry.AuditStore`, `PRAGMA synchronous=FULL` on a dedicated `*sql.Conn`) with one row per drain-mode transition; CLI/DLL read the same DB `?mode=ro`. Startup path: `Open → MigrateJSONL → drift reconciliation → live ingest`. `MemAuditStore` and the file-only root-package `AuditStore` were retired in feature 007.
Config: JSON file with atomic writes (named mutex + MoveFileEx). Scoped updaters for dashboard API.
Notifications: multi-target (N webhook + M ntfy), granular triggers, per-target repeat intervals.
Sessions: `WTSEnumerateSessionsW` via wtsapi32.dll, utilization alerts at configurable threshold.
Dashboard chart: LayerCake (Svelte-idiomatic composition). Session gauges per server card.
Logging: slog-based, dual-sink — ETW manifest provider "LISS Technologies-DrainCtl" (Operational channel INF+, Debug channel DBG, disabled by default) + file log (`%ProgramData%\...\drainctl.log`, rotates at local midnight to `drainctl-YYYY-MM-DD.log`, 7 days kept, local timestamps). Per-sink levels in `config.json` (`log_file_level`, `log_event_level`). CLI uses `--log-level debug|info|warn|error` flag (default `info`).
Event-log anomaly detection: `internal/evtspike` subsystem (opt-in via `evtspike.enabled`) subscribes to configured channels, maintains a robust-cap Bayesian baseline persisted to JSON, and fires `event_spike` notifications through the existing notify pipeline on confirmed spikes.
