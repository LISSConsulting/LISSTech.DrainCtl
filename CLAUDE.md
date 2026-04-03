# CLAUDE.md — Project Guide for Claude Code

## What This Is

LISSTech DrainCtl — a Windows Service + CLI + PowerShell module that monitors RDSH drain mode (`TSServerDrainMode`). Written in Go with a c-shared DLL for PowerShell P/Invoke, WiX 5 MSI installer, and named pipe IPC.

## Build

```bash
just all          # Unsigned build: CLI + DLL + PS module + MSI
just release      # Signed build (needs CODE_SIGNING_CERTIFICATE_THUMBPRINT in .env)
just lint         # go vet + gofmt + golangci-lint
just test         # PowerShell module smoke test
just resource     # Recompile .syso (after icon/version changes)
```

Requires: Go 1.22+, MinGW (`scoop install mingw`), WiX 5 (`dotnet tool install -g wix`), .NET SDK 8+.

## Architecture

- **Root package** (`package drainctl`): public API — registry, audit, check, history, format, config, service, pipe
- **`cmd/drainctl/`**: CLI entry point (cobra). Also the service binary (`service run`)
- **`cmd/cshared/`**: C-shared DLL exports for PowerShell P/Invoke
- **`powershell/`**: `LISSTech.DrainCtl` module (.psd1 + .psm1)
- **`installer/`**: WiX 5 MSI project
- **`assets/`**: icon, event log message file (.mc + compiled .dll)
- **`docs/`**: landing page (GitHub Pages)

## Conventions

- **Build tag**: every `.go` file in root and cmd packages has `//go:build windows`
- **No viper**: config is in registry `HKLM\...\Services\DrainCtl\Parameters`, read by `config.go`
- **Logging**: `LogFunc` callback type, never `log.Println`. Service uses `EventLogLogger`, CLI uses `DefaultLogger`
- **Version**: CalVer `YY.DOY.patch` (e.g., `26.92.0`). Set in `drainctl.go`, `drainctl.rc`, `.psd1`, `.wixproj`, `.wxs`
- **Company name**: "LISS Consulting, Corp." in legal contexts, "LISS Technologies" as d/b/a
- **Linting**: `go vet` + `gofmt` + `golangci-lint` must pass. Pre-commit hooks via `prek`
- **Branches**: `trunk` (protected, PRs required), `development` (working branch)
- **Signing**: EV code signing via `signtool` + `Set-AuthenticodeSignature`. Binaries signed before MSI build

## Key Design Decisions

- **JSONL over SQLite**: audit trail is ~26K records at 90 days. In-memory store is the query engine; file is persistence
- **cobra without viper**: viper's dependency tree added ~4MB. Config from registry instead
- **wevtutil fallback**: CLI mode shells out to `wevtutil` for attribution. Service mode uses `EvtSubscribe` (push)
- **Pipe-first fallback**: CLI/DLL try named pipe to service, fall back to direct registry + file if service not running
- **Retention cap**: 1-365 days. Enforced by `ClampRetention()` in config and CLI
