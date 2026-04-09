# Feature Specification: Tiered Logging

**Feature Branch**: `001-tiered-logging`
**Created**: 2026-04-09
**Status**: Draft
**Input**: User description: "Elevate current logging implementation to support tiered logging: INFO, WARNING, ERROR, VERBOSE, DEBUG. Support CLI flags and configuration values to configure logging interface."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Configure Log Verbosity via CLI (Priority: P1)

An operator runs a drainctl CLI command and wants to control how much detail appears in output. They pass `--log-level debug` to see full diagnostic output while troubleshooting, or `--log-level error` to suppress everything except errors (replacing the old `--quiet` flag).

**Why this priority**: CLI is the primary operator interface. Controlling verbosity at the command line is the most immediate, visible change and unblocks all other logging tiers.

**Independent Test**: Run any drainctl CLI command with `--log-level debug`, `--log-level info`, `--log-level warn`, and `--log-level error` — verify that only messages at or above the specified level appear in output.

**Acceptance Scenarios**:

1. **Given** a drainctl CLI command, **When** the operator passes `--log-level debug`, **Then** all log messages (DEBUG, INFO, WARN, ERROR) appear in output.
2. **Given** a drainctl CLI command, **When** the operator passes `--log-level warn`, **Then** only WARN and ERROR messages appear in output.
3. **Given** a drainctl CLI command, **When** no `--log-level` flag is provided, **Then** the default level is INFO (DEBUG suppressed).
4. **Given** a drainctl CLI command, **When** the operator passes an invalid level value, **Then** the command exits with a clear error message listing valid levels.

---

### User Story 2 - Configure Log Levels in config.json (Priority: P2)

An administrator configures persistent log level settings in the DrainCtl config file so that the service and CLI respect those levels without requiring CLI flags on every invocation. Each sink (file log, Event Log) can be tuned independently.

**Why this priority**: Persistent configuration is essential for the service (which has no CLI flags at runtime) and avoids requiring operators to remember flags on every CLI invocation.

**Independent Test**: Set per-sink log levels in config.json, restart the service, and verify that the file log and Event Log each respect their configured minimum level.

**Acceptance Scenarios**:

1. **Given** config.json contains `"log_file_level": "debug"` and `"log_event_level": "info"`, **When** the service starts, **Then** the file log captures DEBUG+ messages and the Event Log operational channel captures INFO+ messages.
2. **Given** config.json contains `"log_file_level": "warn"`, **When** the service emits an INFO message, **Then** the file log does not record it.
3. **Given** config.json has no log level settings, **When** the service starts, **Then** defaults apply: file log at DEBUG, Event Log operational channel at INFO.
4. **Given** config.json contains an invalid log level value, **When** the service starts, **Then** it logs a warning about the invalid value and falls back to defaults.
5. **Given** a CLI command is run with `--log-level` and config.json also sets a level, **When** both are present, **Then** the CLI flag takes precedence over config.json for that invocation.

---

### User Story 3 - Migrate to slog Structured Logging (Priority: P1)

The development team replaces the custom `LogFunc` implementation with Go's standard `log/slog` package. All internal logging call sites emit structured log records through slog, enabling consistent structured output across all sinks.

**Why this priority**: slog is the foundation on which all other logging features (level filtering, per-sink routing, ETW integration) are built. Without this migration, tiered logging would require reimplementing level semantics in the custom logger.

**Independent Test**: After migration, all existing log output continues to appear in both sinks with the same information content. Structured fields (key-value pairs) are preserved. No existing test breaks.

**Acceptance Scenarios**:

1. **Given** the codebase has been migrated to slog, **When** any component emits a log message, **Then** the message is a structured slog record with level, message, and key-value attributes.
2. **Given** the migration is complete, **When** the service starts and runs through a health check cycle, **Then** file log and Event Log both receive the same messages they did before migration (content parity).
3. **Given** the custom `LogFunc` type existed before, **When** migration is complete, **Then** `LogFunc` and its factory functions (`DefaultLogger`, `DiscardLogger`, `MultiLogger`) are removed with no remaining references.

---

### User Story 4 - Modern ETW with Operational and Debug Channels (Priority: P2)

The service registers a modern ETW manifest-based provider with two channels: Operational (INFO, WARN, ERROR) and Debug (DEBUG). Administrators use standard Windows tooling (Event Viewer, `wevtutil`, `tracelog`) to enable the Debug channel on demand for diagnostics.

**Why this priority**: ETW channels are the Windows-native way to expose diagnostic data. This replaces the legacy event log approach with a proper manifest-based provider, enabling integration with Windows monitoring tools and ETW consumers.

**Independent Test**: With only the Operational channel enabled (default), run the service — verify INFO/WRN/ERR events appear. Then enable the Debug channel via `wevtutil sl` — verify DEBUG events appear.

**Acceptance Scenarios**:

1. **Given** the DrainCtl ETW provider is registered, **When** the service emits an INFO message, **Then** it appears in the Operational channel.
2. **Given** the Debug channel is disabled (default), **When** the service emits a DEBUG message, **Then** no event appears in Event Viewer for that message.
3. **Given** an administrator enables the Debug channel, **When** the service emits a DEBUG message, **Then** it appears in the Debug channel.
4. **Given** the ETW provider manifest is installed, **When** an administrator runs `wevtutil gp "LISS Technologies-DrainCtl"`, **Then** the provider metadata shows Operational and Debug channels.

---

### User Story 5 - File Log Timestamps in Local Time (Priority: P3)

Operators reading the file log see timestamps in local time instead of UTC, making it easier to correlate log entries with local events and user reports.

**Why this priority**: Quality-of-life improvement. UTC timestamps cause friction when operators cross-reference logs with local incident times.

**Independent Test**: Generate a log entry and verify the file log timestamp matches the system's local time (with timezone offset).

**Acceptance Scenarios**:

1. **Given** the service is running, **When** a log message is written to the file log, **Then** the timestamp reflects the system's local time with timezone offset (e.g., `2026-04-09T14:30:00.000-04:00`).
2. **Given** the system timezone changes while the service is running, **When** subsequent log messages are written, **Then** timestamps reflect the new local time.

---

### User Story 6 - CLI Result Line Separator (Priority: P3)

CLI commands that produce a final status result display it with a `---` separator, visually distinguishing the result from log output. This replaces the former `[OK]` log level.

**Why this priority**: UX refinement. The result line is data output, not a log message. The separator makes this distinction clear to operators. Scripting uses `--format json`, `--format csv`, or exit codes — not the result line.

**Independent Test**: Run `drainctl check` and verify the final status line is prefixed with `---` and is not tagged with a log level.

**Acceptance Scenarios**:

1. **Given** a CLI command that produces a status result, **When** the command completes, **Then** the result line appears after a `---` separator with no timestamp or level tag.
2. **Given** `--log-level error` is set, **When** the command completes, **Then** log messages are suppressed but the `---` result line still appears.
3. **Given** `--format json` is set, **When** the command completes, **Then** structured output goes to stdout and the `---` result line is not emitted (structured format replaces it).

---

### Edge Cases

- What happens when the config file specifies a log level for a sink that doesn't exist (e.g., event log level on a system where the ETW provider isn't registered)? The system should log a warning to the file sink and continue with the available sinks.
- What happens when the file log disk is full? Existing behavior (rotating file writer) should be preserved — the writer handles rotation and old file pruning.
- What happens when the ETW Debug channel fills its buffer? Standard ETW circular buffer behavior applies — oldest events are overwritten.
- What happens when `--log-level` is passed to a subcommand that produces structured output (e.g., `--format json`)? Log messages go to stderr; structured output goes to stdout. Log level applies only to log messages.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: System MUST support four log severity levels using slog conventions, in ascending order: DEBUG, INFO, WARN, ERROR.
- **FR-002**: The former OK level MUST be removed from the log level hierarchy. CLI commands that previously emitted an `[OK]` log line MUST instead emit a `---` prefixed result line that is not a log record.
- **FR-003**: The `---` result line MUST always be emitted regardless of `--log-level` setting, except when `--format` is set to a structured format (json, csv, table).
- **FR-004**: System MUST replace the custom `LogFunc` implementation with Go's standard `log/slog` package for all internal logging.
- **FR-005**: System MUST map log levels directly to slog's built-in levels: `slog.LevelDebug`, `slog.LevelInfo`, `slog.LevelWarn`, `slog.LevelError`. No custom levels.
- **FR-006**: System MUST accept a `--log-level <level>` CLI flag on the root command, where `<level>` is one of: `debug`, `info`, `warn`, `error` (case-insensitive).
- **FR-007**: The `--quiet` CLI flag MUST be removed. This is a breaking change with no backward-compatibility shim.
- **FR-008**: When no `--log-level` flag is provided, the CLI MUST default to INFO level.
- **FR-009**: System MUST support per-sink log level configuration in config.json with keys `log_file_level` and `log_event_level`.
- **FR-010**: When both CLI flag and config.json specify a log level, the CLI flag MUST take precedence for that invocation.
- **FR-011**: System MUST write file log timestamps in local time with timezone offset (ISO 8601 format with offset).
- **FR-012**: System MUST register a modern ETW manifest-based provider with the name "LISS Technologies-DrainCtl".
- **FR-013**: The ETW provider MUST define two channels: Operational (INFO, WARN, ERROR events) and Debug (DEBUG events).
- **FR-014**: The Operational channel MUST be enabled by default. The Debug channel MUST be disabled by default, requiring administrator opt-in via standard Windows tooling.
- **FR-015**: System MUST preserve the existing rotating file log behavior (10 MB max size, 7 retained files).
- **FR-016**: System MUST use slog handlers to route log records to the appropriate sinks based on level and per-sink configuration.
- **FR-017**: The slog migration MUST preserve structured key-value attributes in log output across all sinks.
- **FR-018**: The ETW provider MUST define specific event IDs for operational events (service lifecycle, health checks, config changes, transitions, errors) consistent with the current event ID scheme.

### Key Entities

- **Log Level**: A severity tier (DEBUG, INFO, WARN, ERROR) using slog's built-in level values that determines which messages are emitted to each sink.
- **Log Sink**: A destination for log output — file log, ETW Operational channel, or ETW Debug channel.
- **ETW Provider**: A manifest-based Windows event provider that registers channels, event IDs, and metadata with the operating system.
- **Log Configuration**: Per-sink minimum level settings stored in config.json, overridable by CLI flags.
- **Result Line**: A `---` prefixed CLI output line carrying the command's final status. Not a log record; always emitted unless a structured format is active.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Operators can control log verbosity from the command line with a single flag, seeing only messages at or above their chosen level.
- **SC-002**: Administrators can configure persistent per-sink log levels without modifying CLI invocations or service startup parameters.
- **SC-003**: All logging call sites use Go's standard slog package with no remaining references to the custom LogFunc implementation.
- **SC-004**: Windows administrators can enable the Debug ETW channel on demand using standard Windows tooling, without service restart.
- **SC-005**: File log timestamps display in local time, reducing time-to-correlation when operators compare logs with local incident reports.
- **SC-006**: Existing log content (messages, structured fields, event IDs) is preserved after migration — no information loss.
- **SC-007**: CLI result output is visually distinct from log output via the `---` separator, with no ambiguity about which lines are log records vs. command results.

## Assumptions

- The project uses Go 1.26+, which includes the `log/slog` package (available since Go 1.21).
- The ETW manifest will be compiled and registered as part of the MSI installer (WiX handles provider registration/unregistration).
- The existing event ID scheme (1000–3099) will be preserved and mapped to the new ETW manifest.
- Log levels map directly to slog built-in values: DEBUG (-4), INFO (0), WARN (4), ERROR (8).
- The DLL (cshared) build uses the same slog-based logging infrastructure as the service and CLI.
- Log level names in config.json and CLI flags use lowercase slog convention: `"debug"`, `"info"`, `"warn"`, `"error"`.
- The `--format` flag (json, csv, table) controls data output formatting, not log formatting. Logs always go to stderr in the CLI; structured data goes to stdout.
