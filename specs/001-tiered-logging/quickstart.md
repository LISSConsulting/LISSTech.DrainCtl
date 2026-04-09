# Quickstart: Tiered Logging

**Feature Branch**: `001-tiered-logging`

## Prerequisites

- Go 1.26+
- MinGW (windres for resource compilation)
- Windows SDK (`mc.exe` for message/manifest compilation)
- WiX 5 + .NET SDK 8+ (MSI build)

## Build & Test

```powershell
# Standard build (after implementation)
just all

# Run tests
just gotest

# Lint
just lint
```

## Verify Logging Tiers (CLI)

```powershell
# Default (INFO level)
drainctl check
# Output: [INF] and [WRN] and [ERR] on stderr, result line on stdout

# Debug level — see all messages
drainctl check --log-level debug

# Errors only
drainctl check --log-level error

# Invalid level — should error
drainctl check --log-level verbose
# Expected: "invalid log level "verbose"; valid levels: debug, info, warn, error"
```

## Verify Config-Based Levels (Service)

```powershell
# Edit config
notepad "$env:ProgramData\LISS Technologies\LISSTech DrainCtl\config.json"
# Add: "log_file_level": "warn", "log_event_level": "error"

# Restart service (picks up config on startup or via file watcher)
drainctl service restart

# Check file log — should only show WARN+ entries
Get-Content "$env:ProgramData\LISS Technologies\LISSTech DrainCtl\drainctl.log" -Tail 20
```

## Verify ETW Provider

```powershell
# Check provider registration (after MSI install)
wevtutil gp "LISS Technologies-DrainCtl"

# Query Operational channel
wevtutil qe "LISS Technologies-DrainCtl/Operational" /c:5 /f:text

# Enable Debug channel
wevtutil sl "LISS Technologies-DrainCtl/Debug" /e:true

# Query Debug channel
wevtutil qe "LISS Technologies-DrainCtl/Debug" /c:5 /f:text

# Disable Debug channel
wevtutil sl "LISS Technologies-DrainCtl/Debug" /e:false
```

## Verify Result Line

```powershell
# Result line should appear with --- prefix, no log level tag
drainctl check
# Last line: --- healthy host=RDSH01 ...

# Result line visible even with error-only logging
drainctl check --log-level error
# Only errors + result line visible

# No result line with structured format
drainctl check --format json
# JSON output only, no --- line
```

## Verify File Log Timestamps

```powershell
# Timestamps should be local time with offset
Get-Content "$env:ProgramData\LISS Technologies\LISSTech DrainCtl\drainctl.log" -Tail 5
# Expected: 2026-04-09T14:30:00.000-04:00 [INF] ...
# NOT:      2026-04-09T18:30:00.000Z [INF] ...
```

## Key Files Changed

| File | Change |
|------|--------|
| `log.go` | `LogFunc`, `Level`, `DefaultLogger`, `DiscardLogger`, `LogMsg` removed |
| `internal/logging/*.go` | NEW: slog handlers (File, ETW, CLI, Multi) |
| `internal/svc/handler.go` | `EventLogLogger`, `FileLogger`, `MultiLogger` removed; uses slog |
| `config.go` | `LogFileLevel`, `LogEventLevel` fields added to `Config` |
| `cmd/drainctl/main.go` | `--quiet` removed, `--log-level` added |
| `cmd/drainctl/*.go` | All commands migrated from `LogFunc` to `slog` |
| `assets/drainctl.man` | NEW: ETW instrumentation manifest |
| `assets/drainctl.mc` | Retired (replaced by manifest) |
| `installer/LISSTech.DrainCtl.wxs` | ETW provider registration replaces legacy event log source |
| `~32 .go files` | ~173 log call sites migrated to slog |
