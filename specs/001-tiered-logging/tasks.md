# Tasks: Tiered Logging

**Input**: Design documents from `/specs/001-tiered-logging/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/

**Tests**: Not explicitly requested in the feature specification. Test tasks included only for the new `internal/logging` package (new code with testable contracts).

**Organization**: Tasks are grouped by user story. US3 (slog migration) is foundational — all other stories depend on it.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (e.g., US1, US2, US3)

---

## Phase 1: Setup (Package Infrastructure)

**Purpose**: Create the `internal/logging` package skeleton and shared utilities that all stories depend on.

- [x] T001 Create `internal/logging/` package directory and `level.go` with `ParseLevel()` function mapping `"debug"/"info"/"warn"/"error"` strings to `slog.Level` constants (case-insensitive), returning error for invalid input
- [x] T002 [P] Create `internal/logging/result.go` with `PrintResult(w io.Writer, msg string, fields ...string)` function that writes `--- <msg> [key=value ...]\n` to `w`
- [x] T003 [P] Create `internal/logging/multi.go` with `MultiHandler` implementing `slog.Handler` that fans out to a slice of handlers, each with its own `Enabled()` check
- [x] T004 [P] Create `internal/logging/level_test.go` with table-driven tests for `ParseLevel()`: valid inputs (all cases), invalid inputs, empty string

**Checkpoint**: Package skeleton exists with shared utilities. No functional changes to the codebase yet.

---

## Phase 2: US3 — Migrate to slog Structured Logging (Priority: P1) — FOUNDATIONAL

**Goal**: Replace the custom `LogFunc` implementation with Go's standard `log/slog` package. All internal logging call sites emit structured slog records. `LvlOK` is replaced with `PrintResult()`.

**Independent Test**: After migration, `just gotest` passes. All existing log output continues to appear with the same information content. No references to `LogFunc`, `Level`, `DefaultLogger`, `DiscardLogger`, `LogMsg`, `MultiLogger`, `EventLogLogger`, or `FileLogger` remain.

**CRITICAL**: This phase BLOCKS all subsequent user stories. The codebase transitions from `LogFunc` to `slog` here.

### slog Handlers

- [x] T005 [US3] Create `internal/logging/cli.go` with `CLIHandler` implementing `slog.Handler` — writes to `io.Writer` (stderr) with format `<timestamp> [<LVL>] <msg> <key=value ...>\n`, timestamps in local time with offset, configurable min level via `slog.LevelVar`
- [x] T006 [P] [US3] Create `internal/logging/file.go` with `FileHandler` implementing `slog.Handler` — writes to `io.Writer` (filelog.Writer) with format `<timestamp> <LVL> <msg> <key=value ...>\n`, timestamps in local time with offset (FR-011), configurable min level
- [x] T007 [P] [US3] Create `internal/logging/cli_test.go` with tests for `CLIHandler`: level filtering (messages below min suppressed), output format matches `[DBG]/[INF]/[WRN]/[ERR]` tags, structured attributes preserved
- [x] T008 [P] [US3] Create `internal/logging/file_test.go` with tests for `FileHandler`: level filtering, local timestamp format with offset, structured attributes preserved
- [x] T009 [P] [US3] Create `internal/logging/multi_test.go` with tests for `MultiHandler`: fan-out to multiple handlers, per-handler level filtering

### Migrate Root Package (public API)

- [x] T010 [US3] Refactor `log.go` — remove `Level` type, `LvlDBG/INF/WRN/ERR/OK` constants, `LogFunc` type, `DefaultLogger()`, `DiscardLogger()`, `LogMsg()`. File may be deleted entirely or retained with just the `PrintResult` re-export if needed
- [x] T011 [US3] Update all function signatures in root package that accept `LogFunc` parameter — change to accept `*slog.Logger` or use `slog.Default()`. Key files: `check.go` (RunCheck), `audit_setup.go` (RunAuditSetup), `notify.go`, and any other exported functions with `log LogFunc` parameter
- [x] T012 [US3] Migrate ~15 log call sites in `check.go` — replace `log(LvlINF, ...)` with `slog.Info(...)`, `log(LvlERR, ...)` with `slog.Error(...)`, etc. Convert `log(LvlOK, ...)` calls to `PrintResult()`. Convert `LogMsg()` calls to direct `slog` calls. Preserve all structured fields as slog attributes
- [x] T013 [P] [US3] Migrate ~12 log call sites in `audit_setup.go` — same pattern as T012. Convert `LvlOK` calls to `PrintResult()`
- [x] T014 [P] [US3] Migrate log call sites in `notify.go` — replace LogFunc usage with slog calls
- [x] T015 [P] [US3] Migrate log call sites in any remaining root package files (`sessions.go`, `registry.go`, `history.go`, `perf.go`, etc.) that reference `LogFunc` or `Level`

### Migrate CLI Commands

- [x] T016 [US3] Update `cmd/drainctl/main.go` — replace `cfg.Quiet bool` with `cfg.LogLevel string`, change persistent flag from `--quiet` to `--log-level` with default `"info"`. In `PersistentPreRunE` (or add one): parse level with `ParseLevel()`, create `CLIHandler` writing to `os.Stderr`, set as `slog.SetDefault()`. On invalid level, exit with error listing valid values
- [x] T017 [US3] Migrate ~20 log call sites in `cmd/drainctl/check_cmd.go` — replace `log(LvlINF, ...)` with `slog.Info(...)`, convert `log(LvlOK, ...)` to `PrintResult(os.Stdout, ...)`. Remove `log := dc.DefaultLogger(os.Stdout, cfg.Quiet)` initialization. Ensure result line is suppressed when `cfg.Format` is json/csv/table
- [x] T018 [P] [US3] Migrate log call sites in `cmd/drainctl/audit_cmd.go` — remove `DefaultLogger` call, use slog
- [x] T019 [P] [US3] Migrate log call sites in `cmd/drainctl/configure_cmd.go` — remove `DefaultLogger` calls, use slog
- [x] T020 [P] [US3] Migrate log call sites in `cmd/drainctl/dashboard_cmd.go` — convert `LvlOK` to `PrintResult()`, use slog for others
- [x] T021 [P] [US3] Migrate log call sites in `cmd/drainctl/notify_cmd.go` — convert `LvlOK` to `PrintResult()`, use slog
- [x] T022 [P] [US3] Migrate log call sites in `cmd/drainctl/register_cmd.go` — convert `LvlOK` to `PrintResult()`, use slog
- [x] T023 [P] [US3] Migrate log call sites in `cmd/drainctl/service_cmd.go` — convert `LvlOK` to `PrintResult()`, replace `DefaultLogger` call, use slog
- [x] T024 [P] [US3] Migrate log call sites in `cmd/drainctl/sessions_cmd.go` — use slog

### Migrate Service Handler

- [x] T025 [US3] Refactor `internal/svc/handler.go` — remove `EventLogLogger()`, `FileLogger()`, `MultiLogger()` functions. Replace `log dc.LogFunc` field in `drainService` struct with `logger *slog.Logger`. In service startup (`Execute` method), create `MultiHandler(FileHandler, legacyEventLogHandler)` and set as logger. Migrate all ~30+ log call sites to `slog` calls. Preserve specific event ID writes (1000-1004, 2000, 3000-3002) as direct `elog.Info/Warning/Error` calls alongside slog — these become ETW events in US4
- [x] T026 [P] [US3] Migrate log call sites in `internal/svc/check.go` — use slog via logger from handler
- [x] T027 [P] [US3] Migrate log call sites in `internal/watcher/` package — registry.go, evtsubscribe.go, params.go
- [x] T028 [P] [US3] Migrate log call sites in `internal/dashboard/client.go` — SSPI negotiation logging
- [x] T029 [P] [US3] Migrate log call sites in `internal/pipe/` package — pipe handler logging
- [x] T030 [P] [US3] Migrate log call sites in `internal/perfmon/` package — performance collection logging

### Migrate DLL Exports

- [x] T031 [US3] Update `cmd/cshared/exports.go` — replace all ~15 `dc.DiscardLogger()` calls. Either set `slog.SetDefault(slog.New(slog.DiscardHandler))` once in `init()` and remove per-function logger setup, or pass `slog.New(slog.DiscardHandler)` where individual loggers are needed

### Update Tests

- [x] T032 [US3] Update `log_test.go` — rewrite tests for new logging behavior. Remove tests for `LogFunc`, `DefaultLogger`, `DiscardLogger`, `LogMsg`. Add tests validating slog integration works (or delete file if no root-package logging code remains)
- [x] T033 [P] [US3] Update `cmd/drainctl/main_test.go` — update for `--log-level` flag instead of `--quiet`
- [x] T034 [P] [US3] Update `internal/svc/handler_test.go` — remove references to `EventLogLogger`, `FileLogger`, `MultiLogger`, update to use slog-based service handler
- [x] T035 [P] [US3] Scan for and fix any remaining test files that reference `LogFunc`, `Level`, `DefaultLogger`, `DiscardLogger`, or `LogMsg` across `config_test.go`, `audit_test.go`, `notify_test.go`, and other test files
- [x] T036 [US3] Run `just gotest` and `just lint` — verify all tests pass and no lint errors. Fix any compilation issues from the migration

**Checkpoint**: Entire codebase uses slog. Zero references to `LogFunc` or old `Level` type. `--quiet` flag removed. `[OK]` log level replaced with `---` result lines. `just gotest` passes. `just lint` passes.

---

## Phase 3: US1 — Configure Log Verbosity via CLI (Priority: P1) 🎯 MVP

**Goal**: Operators can control log verbosity from the CLI with `--log-level debug|info|warn|error`.

**Independent Test**: Run `drainctl check --log-level debug` and see all messages. Run `drainctl check --log-level error` and see only errors plus the `---` result line. Run `drainctl check --log-level invalid` and see an error message with valid options.

> Note: The `--log-level` flag and `CLIHandler` were already created in Phase 2 (T005, T016). This phase validates and polishes the behavior.

- [x] T037 [US1] Verify `--log-level` flag behavior in `cmd/drainctl/main.go` — confirm `PersistentPreRunE` correctly parses level, sets `CLIHandler` min level on `slog.Default()`, and exits with clear error on invalid input per contract (`invalid log level "<value>"; valid levels: debug, info, warn, error`)
- [x] T038 [US1] Verify log output routing — confirm log messages go to stderr (not stdout), structured data (`--format json/csv/table`) goes to stdout, and `---` result line goes to stdout. Ensure `--log-level` only affects log messages, not data output or result line
- [x] T039 [US1] Verify default level behavior — confirm that when no `--log-level` flag is provided, CLI defaults to INFO (DEBUG messages suppressed, INFO+ visible)

**Checkpoint**: US1 acceptance scenarios 1-4 all pass. CLI flag contract fully satisfied.

---

## Phase 4: US2 — Configure Log Levels in config.json (Priority: P2)

**Goal**: Administrators can set persistent per-sink log levels in config.json without CLI flags.

**Independent Test**: Set `"log_file_level": "warn"` in config.json, restart the service, emit an INFO message — verify the file log does NOT contain it. Set `"log_event_level": "error"` — verify only errors reach the event log.

- [x] T040 [US2] Add `LogFileLevel string` and `LogEventLevel string` fields to `Config` struct in `config.go` with JSON tags `"log_file_level"` and `"log_event_level"`
- [x] T041 [US2] Update config validation in `config.go` — validate `LogFileLevel` and `LogEventLevel` with `ParseLevel()`. Missing/empty → use defaults (`"debug"` for file, `"info"` for event). Invalid → log warning, fall back to defaults. Same pattern as `ClampRetention()`
- [x] T042 [US2] Update `internal/svc/handler.go` service startup — read `LogFileLevel` and `LogEventLevel` from config, pass parsed `slog.Level` values to `FileHandler` and legacy event log handler (or ETW handler if US4 is complete). Apply config values to handler `LevelVar` on startup
- [x] T043 [US2] Update config file watcher in `internal/svc/handler.go` — when config is reloaded at runtime, update handler level vars dynamically (use `slog.LevelVar.Set()` so levels change without service restart)
- [x] T044 [US2] Update `config_test.go` — add test cases for `LogFileLevel`/`LogEventLevel` parsing: valid values, invalid values (warning + default), missing values (defaults), empty strings (defaults)
- [x] T045 [US2] Update installer default config template `installer/config.json` — add `"log_file_level": "debug"` and `"log_event_level": "info"` to the default configuration

**Checkpoint**: US2 acceptance scenarios 1-5 all pass. Per-sink levels configurable. CLI flag takes precedence.

---

## Phase 5: US4 — Modern ETW with Operational and Debug Channels (Priority: P2)

**Goal**: Register a modern ETW manifest-based provider with Operational and Debug channels, replacing the legacy event log.

**Independent Test**: After MSI install, run `wevtutil gp "LISS Technologies-DrainCtl"` and see provider metadata with two channels. Start service, verify INFO events in Operational channel. Enable Debug channel with `wevtutil sl`, verify DEBUG events appear.

### ETW Manifest & Build

- [x] T046 [US4] Create `assets/drainctl.man` — ETW instrumentation manifest XML with provider name `LISS Technologies-DrainCtl`, generated GUID, Operational channel (enabled, type=Operational), Debug channel (disabled, type=Debug), event definitions for IDs 1000-1004/1099/2000/2099/3000-3002/3099/4000 per contracts/etw-provider.md, string template for message data
- [x] T047 [US4] Update build system (`justfile`) — replace `mc` compilation of `drainctl.mc` with `mc -um drainctl.man` to compile the manifest. Update `resource` recipe if needed. Ensure compiled resource DLL output goes to `assets/drainctl-msg.dll`
- [x] T048 [US4] Retire `assets/drainctl.mc` — remove or rename the legacy message compiler file (keep in git history). The manifest replaces it

### ETW slog Handler

- [x] T049 [US4] Create `internal/logging/etw.go` with `ETWHandler` implementing `slog.Handler` — use `advapi32.dll` syscalls (`EventRegister`, `EventEnabled`, `EventWriteString`) via `golang.org/x/sys/windows` `LazyDLL`/`LazyProc`. Route DEBUG records to Debug channel descriptor, INFO/WARN/ERROR to Operational channel descriptor. Use `EventEnabled` to skip writes to disabled channels (zero-cost when Debug channel is off). Support specific event IDs for known events (1000-3002) via slog attribute, fall back to generic IDs (1099/2099/3099/4000) for untagged messages. Implement `Close()` method calling `EventUnregister`
- [x] T050 [US4] Update `internal/svc/handler.go` — replace legacy `eventlog.Open()` + direct `elog.Info/Warning/Error` calls with `ETWHandler`. In service startup: create `ETWHandler`, compose with `FileHandler` via `MultiHandler`, set as `slog.Default()`. Remove `elog *eventlog.Log` field from `drainService` struct. Update all specific event ID writes (EvtServiceStarted, etc.) to use slog with an `"event_id"` attribute that the ETWHandler maps to the manifest event ID

### Installer Updates

- [x] T051 [US4] Update `installer/LISSTech.DrainCtl.wxs` — replace `EventLogSource` component (registry-based event log source) with ETW provider registration. Add `drainctl.man` as an installed file. Add custom actions: `wevtutil im` on install, `wevtutil um` on uninstall. Remove legacy `EventLog\DrainCtl\DrainCtl` registry key creation. Keep the legacy Application log cleanup (`RemoveRegistryKey`)
- [x] T052 [US4] Add `drainctl.man` to the MSI file list in `installer/LISSTech.DrainCtl.wxs` — ensure the manifest is installed alongside the resource DLL in `[BinFolder]`

**Checkpoint**: US4 acceptance scenarios 1-4 all pass. `wevtutil gp` shows provider. Events route to correct channels. Debug channel toggleable.

---

## Phase 6: US5 — File Log Timestamps in Local Time (Priority: P3)

**Goal**: File log timestamps show local time with timezone offset instead of UTC.

**Independent Test**: Generate a log entry, read the file log, verify timestamp matches system local time with offset (e.g., `2026-04-09T14:30:00.000-04:00`).

> Note: If the `FileHandler` (T006) was already implemented with local timestamps in Phase 2, this phase is just validation.

- [x] T053 [US5] Verify `FileHandler` in `internal/logging/file.go` uses `time.Now().Format("2006-01-02T15:04:05.000-07:00")` for timestamps (local time with offset), NOT UTC format `"2006-01-02T15:04:05.000Z"`. Fix if needed
- [x] T054 [US5] Verify `file_test.go` includes a test case validating that the timestamp format contains a timezone offset (not `Z` suffix)

**Checkpoint**: US5 acceptance scenarios 1-2 pass. File log shows local time.

---

## Phase 7: US6 — CLI Result Line Separator (Priority: P3)

**Goal**: CLI commands display final status with `---` separator, distinct from log output.

**Independent Test**: Run `drainctl check` — last line has `---` prefix, no timestamp, no level tag. Run with `--log-level error` — result line still appears. Run with `--format json` — no `---` line.

> Note: `PrintResult()` was created in T002 and call sites were migrated in Phase 2. This phase validates behavior.

- [x] T055 [US6] Verify `PrintResult()` output format in `internal/logging/result.go` — confirm format is `--- <msg> [key=value ...]\n` with no timestamp and no level tag
- [x] T056 [US6] Verify result line suppression — confirm `PrintResult()` is NOT called when `cfg.Format` is `json`, `csv`, or `table` in all CLI commands that emit result lines (`check_cmd.go`, `dashboard_cmd.go`, `notify_cmd.go`, `register_cmd.go`, `service_cmd.go`, `audit_cmd.go`)
- [x] T057 [US6] Verify result line is NOT affected by `--log-level` — confirm `---` line appears even with `--log-level error`

**Checkpoint**: US6 acceptance scenarios 1-3 pass. Result line is visually distinct from log output.

---

## Phase 8: Polish & Cross-Cutting Concerns

**Purpose**: Final validation, cleanup, and build verification.

- [x] T058 Run `just lint` — verify no lint errors across entire codebase after all migrations
- [x] T059 Run `just gotest` — verify all tests pass (existing + new)
- [x] T060 [P] Run `just all` — verify full unsigned build succeeds (CLI + DLL + PS module + MSI)
- [x] T061 [P] Verify no remaining references to removed types — grep for `LogFunc`, `LvlDBG`, `LvlINF`, `LvlWRN`, `LvlERR`, `LvlOK`, `DefaultLogger`, `DiscardLogger`, `MultiLogger`, `EventLogLogger`, `FileLogger`, `LogMsg`, `--quiet` across all `.go` files. Zero matches expected (excluding test assertions about removal and comments)
- [x] T062 Update `CLAUDE.md` Architecture section — change "Logging: dual-sink — Windows Event Log (custom 'DrainCtl' log, INF+) + file log" to reflect new architecture: "Logging: slog-based, dual-sink — ETW manifest provider (Operational channel INF+, Debug channel DBG) + file log (`%ProgramData%\...\drainctl.log`, 10 MB rotate, 7 kept, local timestamps). Per-sink levels in config.json. CLI uses `--log-level` flag."
- [x] T063 Run quickstart.md validation — execute the verification steps from `specs/001-tiered-logging/quickstart.md` to confirm all features work end-to-end

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: No dependencies — can start immediately
- **Phase 2 (US3 — slog Migration)**: Depends on Phase 1 — **BLOCKS all subsequent phases**
- **Phase 3 (US1 — CLI --log-level)**: Depends on Phase 2 — validation of work done in Phase 2
- **Phase 4 (US2 — Config levels)**: Depends on Phase 2 — adds config fields + service integration
- **Phase 5 (US4 — ETW)**: Depends on Phase 2 — adds ETW manifest + handler, modifies service
- **Phase 6 (US5 — Timestamps)**: Depends on Phase 2 — validates FileHandler format
- **Phase 7 (US6 — Result line)**: Depends on Phase 2 — validates PrintResult behavior
- **Phase 8 (Polish)**: Depends on all desired user stories being complete

### User Story Dependencies

- **US3 (P1)**: Foundational — no dependencies on other stories. Must complete first.
- **US1 (P1)**: Depends on US3 (CLIHandler + --log-level created during migration). Can start after Phase 2.
- **US6 (P3)**: Depends on US3 (PrintResult + LvlOK removal during migration). Can start after Phase 2.
- **US2 (P2)**: Depends on US3 (slog handlers with LevelVar). Can start after Phase 2.
- **US4 (P2)**: Depends on US3 (slog Handler interface). Can start after Phase 2. Independent of US1/US2.
- **US5 (P3)**: Depends on US3 (FileHandler timestamp format). Can start after Phase 2.

### Post-Phase 2 Parallel Opportunities

After Phase 2 completes, **all remaining user story phases (3-7) can run in parallel** since they modify different files:
- US1 (Phase 3): Only validates `cmd/drainctl/main.go` behavior
- US2 (Phase 4): Modifies `config.go`, `config_test.go`, `internal/svc/handler.go`, `installer/config.json`
- US4 (Phase 5): Creates `assets/drainctl.man`, `internal/logging/etw.go`, modifies `installer/*.wxs`, `justfile`
- US5 (Phase 6): Validates `internal/logging/file.go`
- US6 (Phase 7): Validates result line behavior across CLI commands

### Within Phase 2 (US3 — largest phase)

Sequential execution order:
1. **T005-T009**: Create handlers + tests (T006-T009 parallel with each other, after T005)
2. **T010-T011**: Refactor root package API (must complete before call site migration)
3. **T012-T015**: Migrate root package call sites (T013-T015 parallel, after T012)
4. **T016**: Update CLI main.go (before CLI command migration)
5. **T017-T024**: Migrate CLI commands (T018-T024 parallel, after T017)
6. **T025**: Refactor service handler (before internal package migration)
7. **T026-T030**: Migrate internal packages (all parallel, after T025)
8. **T031**: Migrate DLL exports (independent, can run anytime after T010)
9. **T032-T035**: Update tests (all parallel, after all migrations)
10. **T036**: Final verification

---

## Parallel Example: Phase 2 Internal Migration

```
# After T025 (service handler refactored), launch all internal migrations:
Task T026: "Migrate log call sites in internal/svc/check.go"
Task T027: "Migrate log call sites in internal/watcher/ package"
Task T028: "Migrate log call sites in internal/dashboard/client.go"
Task T029: "Migrate log call sites in internal/pipe/ package"
Task T030: "Migrate log call sites in internal/perfmon/ package"
```

## Parallel Example: Post-Phase 2 Story Validation

```
# After Phase 2 completes, all story phases can start simultaneously:
Phase 3 (US1): Validate --log-level flag
Phase 4 (US2): Add config.json per-sink levels
Phase 5 (US4): ETW manifest + handler
Phase 6 (US5): Validate local timestamps
Phase 7 (US6): Validate result line behavior
```

---

## Implementation Strategy

### MVP First (US3 + US1 Only)

1. Complete Phase 1: Setup (T001-T004)
2. Complete Phase 2: US3 slog migration (T005-T036)
3. Complete Phase 3: US1 CLI --log-level validation (T037-T039)
4. **STOP and VALIDATE**: Run `just gotest`, `just lint`, manual CLI verification
5. At this point: CLI log verbosity works, slog is fully integrated, old logging removed

### Incremental Delivery

1. Setup + US3 Migration → Core logging works (MVP!)
2. Add US1 validation → CLI flag polished
3. Add US2 → Config-based levels for service
4. Add US4 → ETW manifest (biggest post-MVP feature)
5. Add US5 + US6 → Polish (timestamps, result line validation)
6. Polish phase → Build verification, docs update

### Parallel Team Strategy

With multiple developers after Phase 2:
- Developer A: US2 (config levels) + US4 (ETW manifest)
- Developer B: US1 + US5 + US6 (CLI validation + polish)

---

## Notes

- [P] tasks = different files, no dependencies on incomplete tasks
- [Story] label maps task to specific user story for traceability
- Phase 2 (US3) is the critical path — ~65% of all tasks. Plan accordingly.
- The migration is mechanical but large (~173 call sites). Batch by file, verify compilation after each file.
- `LvlOK` → `PrintResult()` conversion happens during US3 migration, validated in US6.
- ETW handler (US4) requires Windows SDK `mc.exe` for manifest compilation.
- Commit after each task or logical group within Phase 2.
