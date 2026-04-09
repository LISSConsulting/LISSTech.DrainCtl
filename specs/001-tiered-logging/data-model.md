# Data Model: Tiered Logging

**Feature Branch**: `001-tiered-logging`
**Date**: 2026-04-09

## Entities

### LogLevel

Represents a log severity tier using slog's built-in integer values.

| Field | Type | Description |
|-------|------|-------------|
| Name | string | Human-readable name: `"debug"`, `"info"`, `"warn"`, `"error"` |
| Value | slog.Level | slog constant: `-4`, `0`, `4`, `8` |

**Validation**: Case-insensitive parsing. Invalid values rejected with error listing valid options.

**Mapping from current system**:

| Old Level | Old Tag | New slog.Level | New Tag |
|-----------|---------|----------------|---------|
| `LvlDBG` | `[DBG]` | `slog.LevelDebug` (-4) | `[DBG]` |
| `LvlINF` | `[INF]` | `slog.LevelInfo` (0) | `[INF]` |
| `LvlWRN` | `[WRN]` | `slog.LevelWarn` (4) | `[WRN]` |
| `LvlERR` | `[ERR]` | `slog.LevelError` (8) | `[ERR]` |
| `LvlOK`  | `[OK ]` | *(removed — becomes result line)* | `---` |

---

### LogSink

A destination for log output. Each sink has an independent minimum level.

| Field | Type | Description |
|-------|------|-------------|
| Name | string | `"file"`, `"etw_operational"`, `"etw_debug"`, `"cli"` |
| MinLevel | slog.Level | Minimum level to emit (records below are dropped) |
| Handler | slog.Handler | The slog handler implementation for this sink |

**Sink routing**:

| Sink | Config Key | Default Level | Context |
|------|-----------|---------------|---------|
| File log | `log_file_level` | `debug` | Service mode |
| ETW Operational | `log_event_level` | `info` | Service mode |
| ETW Debug | *(channel enable/disable)* | `debug` | Service mode, admin opt-in |
| CLI stderr | `--log-level` flag | `info` | CLI mode |

---

### ETWProvider

The manifest-based Windows event provider registration.

| Field | Type | Description |
|-------|------|-------------|
| Name | string | `"LISS Technologies-DrainCtl"` |
| GUID | GUID | Provider GUID (new, generated for manifest) |
| Channels | []Channel | Operational, Debug |
| Events | []EventDef | Event definitions with IDs |
| ResourceDLL | string | Path to compiled message/resource DLL |

---

### ETWChannel

A channel within the ETW provider.

| Field | Type | Description |
|-------|------|-------------|
| Name | string | `"LISS Technologies-DrainCtl/Operational"` or `".../Debug"` |
| Type | string | `"Operational"` or `"Debug"` |
| Enabled | bool | Operational=true (default), Debug=false (default) |
| Isolation | string | `"Application"` |

---

### EventDefinition

An event registered in the ETW manifest, preserving the existing event ID scheme.

| Event ID | Symbol | Level | Channel | Description |
|----------|--------|-------|---------|-------------|
| 1000 | ServiceStarted | Info | Operational | Service started |
| 1001 | ServiceStopped | Info | Operational | Service stopped |
| 1002 | CheckHealthy | Info | Operational | Health check passed |
| 1003 | ConfigReloaded | Info | Operational | Configuration reloaded |
| 1004 | TransitionDetected | Info | Operational | Drain mode transition |
| 1099 | GenericInfo | Info | Operational | Generic informational message |
| 2000 | CheckGrace | Warning | Operational | Grace period active |
| 2099 | GenericWarning | Warning | Operational | Generic warning message |
| 3000 | CheckAlert | Error | Operational | Grace period exceeded / alert |
| 3001 | RegistryReadFailed | Error | Operational | Registry read failure |
| 3002 | ServiceError | Error | Operational | Service error |
| 3099 | GenericError | Error | Operational | Generic error message |
| 4000 | GenericDebug | Debug | Debug | Generic debug message |

**Note**: Debug events (4000+) go exclusively to the Debug channel. All existing events (1000-3099) remain in the Operational channel.

---

### Config (modified fields)

New fields added to the existing `Config` struct in `config.go`:

| Field | JSON Key | Type | Default | Validation |
|-------|----------|------|---------|------------|
| LogFileLevel | `log_file_level` | string | `"debug"` | Must be valid level name |
| LogEventLevel | `log_event_level` | string | `"info"` | Must be valid level name |

**Precedence**: CLI `--log-level` flag > config.json values > hard-coded defaults.

---

## State Transitions

### Log Level Resolution (CLI mode)

```
Start
  ├─ --log-level flag provided? ──yes──→ Use flag value
  │                                        ├─ Valid? → Set as CLI handler level
  │                                        └─ Invalid? → Exit with error
  └─ no ──→ Check config.json log_file_level
               ├─ Present & valid? → Use config value
               └─ Absent or invalid? → Use default (INFO)
```

### Log Level Resolution (Service mode)

```
Start
  ├─ Read config.json log_file_level
  │    ├─ Valid? → Set file handler min level
  │    └─ Invalid/absent? → Warn + default (DEBUG)
  │
  └─ Read config.json log_event_level
       ├─ Valid? → Set ETW Operational handler min level
       └─ Invalid/absent? → Warn + default (INFO)

(Debug channel level is always DEBUG; gated by channel enabled state)
```

---

## Removed Entities

| Entity | Location | Replacement |
|--------|----------|-------------|
| `Level` type | `log.go:14` | `slog.Level` |
| `LvlDBG/INF/WRN/ERR/OK` | `log.go:16-21` | `slog.LevelDebug/Info/Warn/Error` + `PrintResult()` |
| `LogFunc` type | `log.go:24` | `*slog.Logger` |
| `DefaultLogger()` | `log.go:28-36` | `internal/logging.NewCLIHandler()` |
| `DiscardLogger()` | `log.go:39-41` | `slog.DiscardHandler` |
| `LogMsg()` | `log.go:45-51` | Direct `slog.Info/Warn/Error/Debug()` calls |
| `MultiLogger()` | `internal/svc/handler.go:102-109` | `internal/logging.NewMultiHandler()` |
| `EventLogLogger()` | `internal/svc/handler.go:77-89` | `internal/logging.NewETWHandler()` |
| `FileLogger()` | `internal/svc/handler.go:93-98` | `internal/logging.NewFileHandler()` |
| `--quiet` flag | `cmd/drainctl/main.go:31` | `--log-level error` |
