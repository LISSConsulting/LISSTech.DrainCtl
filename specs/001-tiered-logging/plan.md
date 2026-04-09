# Implementation Plan: Tiered Logging

**Branch**: `001-tiered-logging` | **Date**: 2026-04-09 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/001-tiered-logging/spec.md`

## Summary

Elevate DrainCtl's logging from a custom `LogFunc` abstraction with 5 string levels to Go's standard `log/slog` package with 4 severity tiers (DEBUG, INFO, WARN, ERROR), per-sink level configuration, and a modern ETW manifest-based provider with Operational and Debug channels. The former `[OK]` log level becomes a `---` result line outside the logging pipeline.

## Technical Context

**Language/Version**: Go 1.26.2
**Primary Dependencies**: cobra v1.10.2, golang.org/x/sys v0.42.0 (no new deps required — slog is stdlib)
**Storage**: File log (`%ProgramData%\LISS Technologies\LISSTech DrainCtl\drainctl.log`), Windows ETW channels
**Testing**: `go test` (table-driven tests, 28 test files across root + internal packages)
**Target Platform**: Windows Server 2016+ (x64), Windows 11+
**Project Type**: CLI + Windows service + DLL (P/Invoke)
**Performance Goals**: N/A — logging is not on the hot path (poll interval ≥10s)
**Constraints**: No new external dependencies; preserve existing event ID scheme (1000-3099); ETW manifest compiled with mc.exe (already in build chain)
**Scale/Scope**: ~173 log call sites across 32 .go files; ~35 `LvlOK` sites to convert to result lines

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

The project constitution is a template (not yet ratified for this project). No specific gates to enforce. Proceeding with standard engineering principles:

- **Simplicity**: slog is stdlib, no new deps. Handlers are thin wrappers.
- **Backwards compatibility**: `--quiet` removal is an intentional breaking change per spec FR-007.
- **Testing**: Existing test files cover logging (log_test.go). New handler tests required.

**Post-Phase 1 re-check**: Design uses 4 handler types in a new `internal/logging` package. This is proportional to the 3 sinks × 2 modes (service/CLI) the system serves. No over-engineering detected.

## Project Structure

### Documentation (this feature)

```text
specs/001-tiered-logging/
├── plan.md              # This file
├── research.md          # Phase 0 output (complete)
├── data-model.md        # Phase 1 output (complete)
├── quickstart.md        # Phase 1 output (complete)
├── contracts/
│   ├── cli-flags.md     # CLI flag contract
│   ├── config-schema.md # config.json schema changes
│   └── etw-provider.md  # ETW manifest provider contract
└── tasks.md             # Phase 2 output (pending /speckit.tasks)
```

### Source Code (repository root)

```text
# Root package (public API)
├── log.go                    # MODIFY: Remove LogFunc, Level, helpers
├── config.go                 # MODIFY: Add LogFileLevel, LogEventLevel fields
├── check.go                  # MODIFY: Migrate ~15 log call sites to slog
├── audit_setup.go            # MODIFY: Migrate ~12 call sites
├── notify.go                 # MODIFY: Migrate call sites
├── *.go                      # MODIFY: All files with log() calls

# New logging package
├── internal/logging/
│   ├── level.go              # NEW: ParseLevel(), level constants
│   ├── file.go               # NEW: FileHandler (slog.Handler for rotating file)
│   ├── etw.go                # NEW: ETWHandler (slog.Handler for ETW channels)
│   ├── cli.go                # NEW: CLIHandler (slog.Handler for stderr)
│   ├── multi.go              # NEW: MultiHandler (fan-out)
│   ├── result.go             # NEW: PrintResult() for --- lines
│   ├── level_test.go         # NEW: Level parsing tests
│   ├── file_test.go          # NEW: FileHandler tests
│   ├── cli_test.go           # NEW: CLIHandler tests
│   └── multi_test.go         # NEW: MultiHandler tests

# CLI
├── cmd/drainctl/
│   ├── main.go               # MODIFY: --quiet → --log-level, slog setup
│   ├── check_cmd.go          # MODIFY: Migrate ~20 call sites + result line
│   └── *.go                  # MODIFY: All commands

# Service
├── internal/svc/
│   └── handler.go            # MODIFY: Remove EventLogLogger/FileLogger/MultiLogger, use slog

# DLL
├── cmd/cshared/
│   └── exports.go            # MODIFY: DiscardLogger() → slog discard handler

# ETW manifest
├── assets/
│   ├── drainctl.man           # NEW: ETW instrumentation manifest
│   └── drainctl.mc            # RETIRE: Legacy message compiler file

# Installer
├── installer/
│   └── LISSTech.DrainCtl.wxs  # MODIFY: ETW provider registration
```

**Structure Decision**: No new top-level directories. The only new package is `internal/logging/` which houses the slog handler implementations. All other changes are modifications to existing files.

## Complexity Tracking

> No constitution violations to justify — constitution is not yet ratified.
