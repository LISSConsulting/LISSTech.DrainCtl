# Research: Tiered Logging

**Feature Branch**: `001-tiered-logging`
**Date**: 2026-04-09

## R-001: slog Migration Strategy

**Decision**: Replace custom `LogFunc func(l Level, fields ...string)` with Go's standard `log/slog` package using a multi-handler architecture.

**Rationale**: slog (stable since Go 1.21, project uses Go 1.26) provides built-in level semantics (`slog.LevelDebug=-4`, `LevelInfo=0`, `LevelWarn=4`, `LevelError=8`), structured attributes, and a `Handler` interface that naturally maps to multi-sink routing. The existing `LogFunc` signature already carries level + key-value pairs, making the migration mechanical.

**Alternatives considered**:
- **zerolog/zap**: Third-party; project has zero non-stdlib logging deps today. Adding one contradicts the project's minimal-dependency philosophy (only 3 direct deps).
- **Wrap LogFunc with level filtering**: Would require reimplementing level semantics, handler composition, and structured attribute propagation — all things slog provides out of the box.

**Migration approach**:
1. Create a `internal/logging` package with slog handler implementations (file handler, ETW handler)
2. Create a `MultiHandler` that fans out to configured sinks with per-sink level filtering
3. Replace `LogFunc` call sites with `slog.Debug/Info/Warn/Error` calls (~173 call sites across 32 files)
4. Remove `LogFunc`, `DefaultLogger`, `DiscardLogger`, `MultiLogger`, `FileLogger`, `EventLogLogger`, `LogMsg`
5. The `Level` type (`LvlDBG/INF/WRN/ERR/OK`) is removed; `LvlOK` becomes a `---` result line

**Key constraint**: The DLL (`cmd/cshared/exports.go`) currently uses `DiscardLogger()` in ~15 places. After migration, DLL exports will use `slog.SetDefault()` with a discard handler, or pass `slog.New(slog.DiscardHandler)` where a logger is needed.

---

## R-002: ETW Manifest-Based Provider Architecture

**Decision**: Create a proper ETW instrumentation manifest (`.man` XML) with Operational and Debug channels, compiled with `mc.exe`, registered via WiX installer.

**Rationale**: The project already uses `mc.exe` to compile `assets/drainctl.mc` into `drainctl-msg.dll` for the legacy event log. Moving to a manifest-based provider is a natural evolution that uses the same toolchain. Manifest-based providers enable proper channels, structured event schema, and integration with `wevtutil`, `tracelog`, and ETW consumers.

**Alternatives considered**:
- **TraceLogging via `go-winio/pkg/etw`**: Self-describing format, no manifest needed. But lacks proper channel support (Operational/Debug distinction) and doesn't integrate with Event Viewer's channel model.
- **Keep legacy event log + add TraceLogging**: Two separate mechanisms increases complexity. A single manifest-based provider is cleaner.

**Technical approach**:
1. **Manifest file** (`assets/drainctl.man`): Defines provider `LISS Technologies-DrainCtl` with GUID, two channels (Operational enabled by default, Debug disabled), event templates, and event definitions preserving existing IDs (1000-3099).
2. **Compilation**: `mc.exe -um drainctl.man` generates resource files. The existing `drainctl.mc` message compiler file is retired.
3. **Go ETW writer** (`internal/logging/etw.go`): Calls `EventRegister`, `EventEnabled`, `EventWrite`/`EventWriteString` via syscall from `advapi32.dll`. Implements `slog.Handler` interface.
4. **Channel routing**: The slog ETW handler checks the record's level to route: DEBUG → Debug channel, INFO/WARN/ERROR → Operational channel. Uses `EventEnabled` to skip Debug events when the channel is disabled (zero-cost when off).
5. **Installer registration**: WiX custom action runs `wevtutil im drainctl.man` on install and `wevtutil um drainctl.man` on uninstall. Replaces the current registry-based event source registration.
6. **Event IDs preserved**: All existing event IDs (1000-1004, 2000, 2099, 3000-3002, 3099) map to manifest event definitions with the same semantics.

**Key constraint**: `EventRegister`/`EventWrite` are in `advapi32.dll`. The Go code will use `golang.org/x/sys/windows` for `LazyDLL`/`LazyProc` wrappers — consistent with the project's existing syscall patterns for `wtsapi32.dll` etc.

---

## R-003: Per-Sink Level Configuration in config.json

**Decision**: Add `log_file_level` and `log_event_level` string fields to the top-level `Config` struct.

**Rationale**: The spec requires independent level tuning per sink. Two simple string fields in the existing config struct are the minimal change. The `Config` struct already has flat top-level fields (no nested "logging" section needed).

**Alternatives considered**:
- **Nested `logging` struct**: `{"logging": {"file_level": "debug", "event_level": "info"}}`. Adds an extra level of nesting for just two fields. Rejected for over-engineering.
- **Single `log_level` field**: Doesn't satisfy the per-sink requirement.

**Defaults**: `log_file_level: "debug"`, `log_event_level: "info"` — matches current hard-coded behavior (file gets all levels, event log gets INF+).

**Validation**: `ParseLevel()` function validates input; invalid values log a warning and fall back to defaults. Same pattern as existing `ClampRetention()`.

---

## R-004: CLI `--log-level` Flag and `--quiet` Removal

**Decision**: Add `--log-level <level>` persistent flag on root command, remove `--quiet` flag. No backward-compatibility shim.

**Rationale**: The spec explicitly states "`--quiet` MUST be removed. This is a breaking change with no backward-compatibility shim." The new `--log-level` flag subsumes `--quiet` (use `--log-level error` for equivalent behavior).

**Implementation**:
1. Replace `cfg.Quiet bool` with `cfg.LogLevel string` in `cmd/drainctl/main.go`
2. Root command persistent flag: `--log-level` with default `"info"`
3. In `PersistentPreRunE`: parse level, configure slog default logger with CLI handler at that level
4. Invalid level → exit with error listing valid values

**Log routing in CLI mode**:
- Log messages → stderr (via slog handler writing to `os.Stderr`)
- Structured data output (`--format json/csv/table`) → stdout
- `---` result line → stdout (always, unless `--format` is set)

---

## R-005: Result Line (`---` Separator) Replacing `[OK]`

**Decision**: Replace all `LvlOK` log calls with a dedicated `PrintResult()` function that writes a `---` prefixed line to stdout, outside the slog pipeline.

**Rationale**: The result line is command output, not a log record. It should bypass the logging system entirely. The `---` separator visually distinguishes it from log output.

**Implementation**:
- New function: `PrintResult(w io.Writer, msg string)` → writes `--- <msg>\n` to `w`
- All ~35 `LvlOK` call sites are converted to `PrintResult(os.Stdout, ...)`
- `PrintResult` is suppressed when `--format` is `json`, `csv`, or `table` (structured output replaces it)
- `PrintResult` is NOT suppressed by `--log-level` (it's not a log message)

---

## R-006: File Log Timestamp Format

**Decision**: Switch file log timestamps from UTC to local time with timezone offset.

**Rationale**: Spec FR-011. Operators correlate logs with local incident times.

**Implementation**: In the file log slog handler, use `time.Now().Format("2006-01-02T15:04:05.000-07:00")` instead of the current UTC format `"2006-01-02T15:04:05.000Z"`. This is a one-line change in the handler's format method.

---

## R-007: slog Handler Architecture

**Decision**: Three custom `slog.Handler` implementations composed via a `MultiHandler`.

**Handlers**:
1. **`FileHandler`** (`internal/logging/file.go`): Writes to the existing `filelog.Writer`. Text format with local timestamps. Level filter from config.
2. **`ETWHandler`** (`internal/logging/etw.go`): Writes to ETW channels via syscall. Routes DEBUG to Debug channel, INFO+ to Operational. Level filter from config.
3. **`CLIHandler`** (`internal/logging/cli.go`): Writes to `io.Writer` (stderr in CLI). Text format with local timestamps and `[DBG]/[INF]/[WRN]/[ERR]` tags. Level filter from `--log-level` flag.
4. **`MultiHandler`** (`internal/logging/multi.go`): Fans out to multiple handlers. Replaces the current `MultiLogger` function.

**Service mode**: `slog.SetDefault(slog.New(MultiHandler(FileHandler, ETWHandler)))`
**CLI mode**: `slog.SetDefault(slog.New(CLIHandler))` — CLI doesn't write to file or ETW.
**DLL mode**: `slog.SetDefault(slog.New(slog.DiscardHandler))` — or per-export logger.

---

## R-008: Attribute Mapping (LogFunc → slog)

**Decision**: Map existing `fields ...string` key-value pairs to `slog.Attr` values.

**Current pattern**: `log(LvlINF, "msg=\"health check\"", "host=RDSH01", "state=healthy")`
**New pattern**: `slog.Info("health check", "host", "RDSH01", "state", "healthy")`

The existing call sites use `key=value` or `key="quoted value"` string formatting via `LogMsg()`. After migration, these become proper slog attributes: `slog.String("key", "value")`. This is the bulk of the migration work (~173 call sites).

**`LogMsg` helper removal**: `LogMsg(log, LvlINF, "msg", fields...)` → `slog.Info("msg", fields...)`. Direct replacement.
