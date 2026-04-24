---
description: "Task list for security and correctness hardening (009)"
---

# Tasks: Security and Correctness Hardening (009)

**Input**: Design documents from `/specs/009-security-hardening/`
**Prerequisites**: plan.md (required), spec.md (required for user stories), research.md, data-model.md, contracts/, `docs/reviews/codex-2026-04-24-fullcodebase-remediation-plan.md` (authoritative file:line source)

**Tests**: Included. This feature changes credential-handling paths, pipe authorization, config concurrency, and event attribution. Unit and boundary tests are load-bearing for every functional requirement; quickstart.md contains the manual verification steps for each story.

**Organization**: Tasks are grouped by user story to enable independent PR landing. Each user story maps to a numbered step range from the remediation plan.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (e.g. `[US1]`, `[US2]`)
- Include exact file paths in descriptions
- Task IDs reference remediation plan steps in parentheses, e.g. `(Step 1)`

## Path Conventions

- Root package Go files live at repository root (e.g. `config.go`, `dpapi.go`, `email.go`, `notify.go`, `drainctl.go`, `history.go`)
- Service code lives under `internal/svc/`
- Pipe code lives under `internal/pipe/`
- Evtspike code lives under `internal/evtspike/`
- Registry watcher code lives under `internal/watcher/`
- Dashboard backend lives under `internal/dashboard/`
- CLI lives under `cmd/drainctl/`, DLL under `cmd/cshared/`
- Frontend lives under `frontend/src/`
- Feature docs live under `specs/009-security-hardening/`

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Confirm clean starting state before any behavior changes. This is a remediation batch on an existing, built codebase — no scaffolding or new dependencies required.

- [ ] T001 Confirm branch `009-security-hardening` is checked out and working tree is clean (`git status` shows no uncommitted changes outside this feature's `specs/` directory)
- [ ] T002 Run baseline verification: `go build ./...`, `go test ./...`, `just lint`, `just vulncheck`, `pnpm -C frontend build` — all must pass with zero warnings, capturing the pre-change baseline
- [ ] T003 Record baseline `internal/svc/handler.go` line count and DPAPI ciphertext format sample in `specs/009-security-hardening/plan.md` §Phase Sequencing (for the Phase 4 split verification)

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: No foundational prerequisites. Every user story operates on an independent surface of the existing codebase.

**Note**: The only cross-story dependency is US3 (readModifyWrite primitive) landing before US5 (Update* deletion) — documented in Dependencies section below and reflected in priority ordering.

---

## Phase 3: User Story 1 — Fail-closed on dashboard cert tampering and cleartext SMTP auth (Priority: P1) 🎯 MVP

**Goal**: Close the two pre-release credential-leak paths: silent dashboard fingerprint re-pinning (FR-001) and cleartext SMTP AUTH after STARTTLS strip (FR-002/FR-003).

**Independent Test**: See `specs/009-security-hardening/quickstart.md` §User Story 1 — rotate dashboard cert + attempt re-register → refused; configure authenticated SMTP target against no-STARTTLS server → send refused with named host.

**Remediation plan steps**: Step 1 (fingerprint refusal), Step 2 (STARTTLS requirement).

### Implementation for User Story 1

- [x] T010 [US1] (Step 1) Factor testable helper `shouldRefuseFingerprintUpdate(saved, offered string) (bool, error)` in `internal/svc/handler.go` that returns `true` + error when both non-empty and differ
- [x] T011 [P] [US1] (Step 1) Add fingerprint-mismatch refusal branch in `cmd/drainctl/register_cmd.go:68-82` — compare `fileCfg.Dashboard.TLSFingerprint` vs `regResult.TLSFingerprint`, return error matching shape in `specs/009-security-hardening/contracts/register-fingerprint-refusal.md` §Error shape
- [x] T012 [P] [US1] (Step 1) Mirror refusal in `internal/svc/handler.go:1031-1042` `registerWithDashboard`: use helper from T010, emit `slog.Error("dashboard=fingerprint-mismatch", "saved", …, "offered", …)`, do NOT overwrite `dashCfg.TLSFingerprint`, do NOT re-init dashboard client, return `false`
- [x] T013 [P] [US1] (Step 1) Add mismatch subtest in `cmd/drainctl/register_cmd_test.go`: stub Register returning `TLSFingerprint: "B"` with stored `"A"`; assert error contains `fingerprint mismatch`, on-disk unchanged
- [x] T014 [P] [US1] (Step 1) Add unit test for `shouldRefuseFingerprintUpdate` in `internal/svc/handler_test.go` covering all six rows of the decision matrix in `specs/009-security-hardening/contracts/register-fingerprint-refusal.md`
- [x] T015 [US1] (Step 2) Update `sendSMTPStartTLS` in `email.go` (around line 326-332): after `c.Hello`, call `c.Extension("STARTTLS")`. When `target.Secret != ""` AND STARTTLS is not advertised OR `c.StartTLS` fails, return `fmt.Errorf("smtp: refusing cleartext AUTH on %s; use smtps:// or a server that supports STARTTLS", host)`
- [x] T016 [US1] (Step 2) Preserve opportunistic-cleartext path for empty `Secret` in `sendSMTPStartTLS`: when STARTTLS is unavailable, emit `slog.Warn("smtp: no STARTTLS available, sending without encryption", "host", host)` and continue
- [x] T017 [P] [US1] (Step 2) Add `TestSMTPStartTLS_RefusesCleartextAuth` in `email_test.go` using `net.Pipe`-driven in-process SMTP server that omits STARTTLS with `Secret: "x"`; assert error contains `refusing cleartext AUTH`
- [x] T018 [US1] Document cert-rotation operator procedure in `CHRONICLE.md`: "Operators must manually clear `Dashboard.TLSFingerprint` in `config.json` before re-registering after cert rotation."
- [x] T019 [US1] Document SMTP transport requirement in `CHRONICLE.md`: "Authenticated SMTP relays now require STARTTLS (port 587 flow) or `smtps://` (port 465); plain-port-25 AUTH is rejected."

**Checkpoint**: Run `go test ./... && just lint`. Execute quickstart.md §User Story 1 manual walkthrough (cert rotation + SMTP refusal). Ship as one bundled PR or split by step; either way, both steps must land together to close the credential-leak blockers before external release.

---

## Phase 4: User Story 2 — Narrow credential blast radius on the agent host (Priority: P1)

**Goal**: Remove the two paths that let other processes on the host read or decrypt drainctl secrets: SERVICE group ACL on config.json (FR-004) and zero-entropy DPAPI (FR-005).

**Independent Test**: See `specs/009-security-hardening/quickstart.md` §User Story 2 — `icacls` output shows no SERVICE ACE; DPAPI round-trip works under new entropy; ciphertext crafted with different entropy fails to decrypt.

**Remediation plan steps**: Step 3 (ACL), Step 4 (DPAPI entropy, greenfield).

### Implementation for User Story 2

- [x] T020 [P] [US2] (Step 3) Update `restrictConfigACL` in `config.go:768-772`: remove `icacls /grant *S-1-5-6:(M)` line; add explicit `icacls /remove *S-1-5-6` before grants to clean up older ACLs on repeat installs
- [x] T021 [P] [US2] (Step 3) Add manual-verification checklist item to the release checklist (`docs/install.ps1` or `CHRONICLE.md`): "Post-install `icacls` check — confirm no `NT AUTHORITY\SERVICE` ACE on config.json"
- [x] T022 [P] [US2] (Step 4) Add unexported `var dpapiEntropy = []byte("LISSTech.DrainCtl/v1/notify-secret")` in `dpapi.go`
- [x] T023 [US2] (Step 4) Update `DPAPIEncrypt` in `dpapi.go` to pass `dpapiEntropy` as the `pOptionalEntropy` arg to `CryptProtectData` (replacing the current `0`/`nil`)
- [x] T024 [US2] (Step 4) Update `DPAPIDecrypt` in `dpapi.go` to pass `dpapiEntropy` as the same arg to `CryptUnprotectData`
- [x] T025 [P] [US2] (Step 4) Add positive round-trip test in `dpapi_test.go`: encrypt a plaintext, decrypt the result, assert equality
- [x] T026 [P] [US2] (Step 4) Add negative test in `dpapi_test.go`: craft ciphertext with a different entropy constant, attempt `DPAPIDecrypt`, assert error
- [ ] T027 [US2] Manual verification (documented, not automated): reinstall on a fresh dev VM, run `icacls "%ProgramData%\LISS Technologies\LISSTech DrainCtl\config.json"`, confirm only SYSTEM and Administrators are granted

**Checkpoint**: Run `go test ./dpapi_test.go` (or package-scoped), `just lint`. Execute quickstart.md §User Story 2 manual icacls check. Ship as one PR.

---

## Phase 5: User Story 3 — Long-running RPC and event pipelines stop silently breaking (Priority: P1)

**Goal**: Fix three correctness bugs in pipelines operators can't see: evtspike loss-callback never invoked (FR-006), concurrent config-update races (FR-007), and named-pipe server truncating requests >4 KB (FR-008).

**Independent Test**: See `specs/009-security-hardening/quickstart.md` §User Story 3 — closed-handle `loss` fires within 2 s; 20-goroutine stress on `UpdateSessionThreshold` produces valid JSON matching exactly one write; scripted reader with `ERROR_MORE_DATA` is reassembled correctly.

**Remediation plan steps**: Step 5 (evtspike loss), Step 6 (atomic RMW), Step 7 (shared pipe reader).

### Implementation for User Story 3

- [ ] T030 [P] [US3] (Step 5) In `internal/evtspike/subscriber_windows.go:80`, capture the `e` return from `procEvtNext.Call(...)` into a `windows.Errno`. When `r == 0` AND errno is non-zero AND errno is not `ERROR_NO_MORE_ITEMS`/`ERROR_TIMEOUT`, call `loss(fmt.Errorf("EvtNext %s: %w", channel, e))` then `return` from the goroutine
- [ ] T031 [P] [US3] (Step 5) Add comment in `internal/evtspike/subscriber_windows.go` documenting the terminal-error contract between `Subscribe` and its caller
- [ ] T032 [P] [US3] (Step 5) Add `TestSubscribe_FiresLossOnTerminalError` in `internal/evtspike/subscriber_test.go` (or appropriate test file): invoke `Subscribe` against a deliberately closed subscription handle; assert `loss` fires within 2 s with non-nil error
- [ ] T033 [US3] (Step 6) Split `LoadConfig` in `config.go` into exported `LoadConfig()` (takes the mutex) and unexported `loadConfigLocked()` (assumes mutex held)
- [ ] T034 [US3] (Step 6) Split `saveConfigToFile` similarly into locked + unlocked variants, hoisting the named-mutex acquisition up to the new helper
- [ ] T035 [US3] (Step 6) Introduce unexported `readModifyWrite(f func(*Config) error) error` in `config.go` that acquires the mutex, calls `loadConfigLocked()`, invokes `f(cfg)`, and calls `saveConfigFileLocked(cfg)`
- [ ] T036 [US3] (Step 6) Rewrite `UpdateNotifySettings` in `config.go:858` to use `readModifyWrite`
- [ ] T037 [US3] (Step 6) Rewrite `UpdateEvtSpikeEnabled` to use `readModifyWrite`
- [ ] T038 [US3] (Step 6) Rewrite `InstallCertificate` to use `readModifyWrite`
- [ ] T039 [US3] (Step 6) Rewrite `UpdateNotifications`, `UpdateSessionThreshold`, `UpdateGracePeriod`, `UpdatePerformanceConfig` in `config.go:806-844` onto `readModifyWrite` — these are deleted in US5 but must still compile and participate in RMW until then
- [ ] T040 [P] [US3] (Step 6) Add `TestConfigRMW_NoInterleave` in `config_test.go`: spawn 20 goroutines hammering `UpdateSessionThreshold` with distinct values; assert final on-disk JSON parses and matches exactly one written value
- [ ] T041 [P] [US3] (Step 6) Add regression test in `config_test.go` verifying two concurrent updaters (notifications + grace) both land in the final file
- [ ] T042 [P] [US3] (Step 7) Extract `readPipeMessage(r io.Reader, initialBufSize int) ([]byte, error)` helper in `internal/pipe/pipe.go` that loops on `errors.Is(err, windows.ERROR_MORE_DATA)`, growing the buffer up to a 1 MiB cap; return concatenated bytes
- [ ] T043 [US3] (Step 7) Replace the single 4 KB `conn.Read(buf)` in `handlePipeConn` at `internal/pipe/pipe.go:100-103` with a call to `readPipeMessage`
- [ ] T044 [US3] (Step 7) Refactor `readPipeResponse` to delegate its existing `ERROR_MORE_DATA` loop to `readPipeMessage`; remove the now-duplicated code
- [ ] T045 [P] [US3] (Step 7) Add `TestReadPipeMessage_HandlesServerSideMoreData` in `internal/pipe/pipe_test.go` using the existing `scriptedReader` pattern from `TestReadPipeResponse_ContinuesOnMoreData`; exercise multi-chunk `ERROR_MORE_DATA` concatenation
- [ ] T046 [P] [US3] (Step 7) Add cap test: scripted reader returning `ERROR_MORE_DATA` past 1 MiB; assert specific cap error

**Checkpoint**: Run `go test ./...`, `just lint`. Execute quickstart.md §User Story 3 (closed-handle loss, 20-goroutine RMW stress, pipe large-message test). Ship as one PR — internal RMW refactor spans many callers; splitting would create merge churn.

**⚠️ Blocks**: US5 T070 depends on T039 landing (the four `Update*` helpers must be rewritten on `readModifyWrite` before US5 can safely delete them).

---

## Phase 6: User Story 4 — Close medium-severity security paper cuts (Priority: P2)

**Goal**: Four defense-in-depth fixes that individually don't block release but cheaply remove easy pivots: cloud-metadata URL rejection (FR-009), pipe caller-SID check (FR-010), HTML template (FR-011), event `SystemTime` attribution (FR-012).

**Independent Test**: See `specs/009-security-hardening/quickstart.md` §User Story 4 — metadata IP save fails, LAN URL saves; non-admin pipe privileged verb denied, read-only verb still works; email escapes `<script>` to `&lt;script&gt;`; synthetic event with `SystemTime` is attributed correctly.

**Remediation plan steps**: Step 8 (metadata IP), Step 9 (SID check), Step 10 (html/template), Step 11 (SystemTime).

### Implementation for User Story 4

- [ ] T050 [P] [US4] (Step 8) Add `rejectCloudMetadata(rawURL string) error` helper in `notify.go` per `specs/009-security-hardening/contracts/notify-url-validation.md` §Helper function contract
- [ ] T051 [P] [US4] (Step 8) Call `rejectCloudMetadata` at the top of `sendWebhook` (`notify.go:390`) and `sendNtfy` (`notify.go:580`); return error before any network activity
- [ ] T052 [US4] (Step 8) Call `rejectCloudMetadata` per-target in `internal/dashboard/server.go:748` `handlePutSettings` (iterate webhook/ntfy targets; fail whole PUT with 400 on any rejection)
- [ ] T053 [US4] (Step 8) Call `rejectCloudMetadata` in `internal/dashboard/server.go:930` `handleNotifyTest` before dispatching; return 400 on rejection
- [ ] T054 [P] [US4] (Step 8) Add rejection subtests in `notify_test.go` covering all cases in `specs/009-security-hardening/contracts/notify-url-validation.md` §Testing contract (metadata IP variants, LAN allowed, loopback allowed, parse failure delegated)
- [ ] T055 [P] [US4] (Step 9) Create `internal/pipe/sid_windows.go` implementing `callerIsPrivileged(conn net.Conn) (bool, string, error)` per `specs/009-security-hardening/contracts/pipe-access-control.md` §Authorization algorithm; respect handle-lifetime invariants in §Handle-lifetime invariants
- [ ] T056 [US4] (Step 9) Integrate `callerIsPrivileged` into `handlePipeConn` in `internal/pipe/pipe.go`: for verbs `register`, `remove-server`, `baseline-reset`, check before dispatch; on deny return `PipeResponse{OK: false, Error: "access denied"}` and emit `slog.Warn("pipe=access_denied", "cmd", req.Cmd, "sid", sidStr)` plus `EvtAccessDenied` ETW audit event
- [ ] T057 [P] [US4] (Step 9) Create `internal/pipe/sid_test.go` with stub-injectable token reader; table-test SID classes: SYSTEM, admin, non-admin, filtered-admin (UAC), unknown SID, token-open error, process-open error
- [ ] T058 [US4] (Step 9) Add integration test in `internal/pipe/pipe_caller_test.go`: open pipe, dial as current process (admin on dev), call `register` against stub `HandleRegister`, assert success; separately simulate deny branch via scripted reader, assert response shape
- [ ] T059 [P] [US4] (Step 10) Replace `import "text/template"` with `"html/template"` in `email.go:14`; update `emailTmpl` initializer at `email.go:21` accordingly
- [ ] T060 [P] [US4] (Step 10) Add `TestEmailTemplate_EscapesHTML` in `email_test.go`: render with `Message: "<script>alert(1)</script>"`; assert body contains `&lt;script&gt;`, not raw tag
- [ ] T061 [US4] (Step 10) Audit `email_template.html` for any intentional raw-HTML insertions; if any field genuinely needs raw HTML, wrap as `template.HTML` only after confirming source is trusted (none of `.Subject/.Message/.Host/.ChangedBy/.SpikeChannel` qualify)
- [ ] T062 [US4] (Step 11) Factor `processEvent` in `internal/watcher/evtsubscribe.go:230-272` so the XML string is a parameter (separate from `renderEventXML`) for testability
- [ ] T063 [US4] (Step 11) In `processEvent`, after `xml.Unmarshal`, parse `evt.System.TimeCreated.SystemTime` via `time.Parse(time.RFC3339Nano, …)`; on success use parsed time for `RegistryChangeAttribution.Timestamp` at line 263; on parse error fallback to `time.Now()` and `slog.Warn("evtspike: unparseable SystemTime", "raw", raw)`
- [ ] T064 [P] [US4] (Step 11) Add `TestProcessEvent_UsesSystemTime` in `internal/watcher/evtsubscribe_test.go`: feed synthetic 4657 XML with `TimeCreated SystemTime="2026-04-24T10:00:00Z"`; assert `LatestAttribution().Timestamp.Equal(parsed)`
- [ ] T065 [P] [US4] (Step 11) Add `TestWaitAttribution_IgnoresOlderEvents` regression: push attribution with SystemTime=T0, call `WaitAttribution(after=T0+5s, timeout=100ms)`, assert return is `""`
- [ ] T066 [P] [US4] (Step 11) Add `TestProcessEvent_FallbackOnUnparseableSystemTime`: feed malformed SystemTime, assert `Timestamp` equals `time.Now()` (within tolerance) and warn log captured

**Checkpoint**: Run `go test ./... && just lint`. Execute quickstart.md §User Story 4 manual walkthroughs. Ship as four small PRs (one per step) or one bundled PR — all four are disjoint file sets.

---

## Phase 7: User Story 5 — Remove drift in the public API and internal layout (Priority: P3)

**Goal**: Pure hygiene. Delete four dead `Update*` exports (FR-013), move ETW IDs out of root (FR-014), retire `DefaultAuditPath` surface (FR-015), split `internal/svc/handler.go` (FR-016), extract shared Svelte helpers (FR-017), delete orphan `deriveP95`/`deriveP50` (FR-018).

**Independent Test**: See `specs/009-security-hardening/quickstart.md` §User Story 5 — grep for removed symbols returns empty; `go build ./... && go test ./...` passes; `handler*.go` files all under 400 lines; manual dashboard transition-list deduplication check.

**Remediation plan steps**: Step 12 (delete Update*), Step 13 (etwids), Step 14 (DefaultDBPath), Step 15 (handler split), Step 16 (Svelte helpers), Step 17 (delete orphans).

**⚠️ Dependency**: T070-T072 MUST NOT run until US3 T039 has landed (Update* helpers rewritten on readModifyWrite).

### Implementation for User Story 5

- [ ] T070 [US5] (Step 12) Delete `UpdateNotifications` (`config.go:806`), `UpdateSessionThreshold` (`config.go:818`), `UpdateGracePeriod` (`config.go:831`), `UpdatePerformanceConfig` (`config.go:844`)
- [ ] T071 [P] [US5] (Step 12) Delete the companion tests for the four helpers from `config_test.go`
- [ ] T072 [P] [US5] (Step 12) Grep to confirm no external consumer: `grep -r "UpdateNotifications\|UpdateSessionThreshold\|UpdateGracePeriod\|UpdatePerformanceConfig"` returns no hits outside release notes
- [ ] T073 [P] [US5] (Step 13) Create `internal/etwids/etwids.go` with `//go:build windows`, package `etwids`, exporting `EvtDashboardAccess`, `EvtDashboardConfigChange`, `EvtServerRegistered`, `EvtServerRemoved`, `EvtAccessDenied`, `EvtGenericAudit` with the same numeric values they have in `drainctl.go`
- [ ] T074 [US5] (Step 13) Delete the six ETW event-ID constants from `drainctl.go:17`
- [ ] T075 [US5] (Step 13) Update imports in `internal/svc/` and `internal/dashboard/` files that referenced the old root-package constants to use `internal/etwids/` instead
- [ ] T076 [P] [US5] (Step 13) Add doc comment to `MigrateFromRegistry` and `WriteDefaultParameters` in `config.go`: `// Installer-only; do not call from runtime code.`
- [ ] T077 [P] [US5] (Step 14) Add `DefaultDBPath() string` in `drainctl.go` returning `DefaultDataDir() + "\\drainctl.db"`; add `DefaultDBDir() string` returning `DefaultDataDir()`
- [ ] T078 [US5] (Step 14) Reimplement `DefaultAuditPath()` as a thin wrapper calling `DefaultDBPath()`; add `// Deprecated: use DefaultDBPath.` doc comment
- [ ] T079 [US5] (Step 14) Widen `GetHistory` in `history.go:35-39`: call `os.Stat(opts.DBPath)`; if dir, use as-is; if file, use `filepath.Dir`; if empty, use `DefaultDBDir()`
- [ ] T080 [P] [US5] (Step 14) Switch default in `cmd/drainctl/main.go:45` from `dc.DefaultAuditPath()` to `dc.DefaultDBPath()`; update flag help text to "SQLite audit DB"
- [ ] T081 [P] [US5] (Step 14) Switch default in `cmd/cshared/exports.go:81` similarly
- [ ] T082 [P] [US5] (Step 14) Switch default-setting call sites in `config.go:283, 389, 1073` to `DefaultDBPath()`
- [ ] T083 [P] [US5] (Step 14) Add `TestGetHistory_AcceptsFilePath` and `TestGetHistory_AcceptsDirPath` in `history_test.go` (or equivalent) covering both widened-input shapes
- [ ] T084 [US5] (Step 15) Split `internal/svc/handler.go` (1138 lines) via pure cut-and-paste: pipe RPC handlers → `handler_pipe.go`, dashboard sync → `handler_dashboard.go`, perf collector lifecycle → `handler_perf.go`, evtspike reloads → `handler_evtspike.go`, service startup → `handler_lifecycle.go`. Core Run loop stays in `handler.go`. Every new file carries `//go:build windows` and `package svc`
- [ ] T085 [US5] (Step 15) Run `go build ./... && go test ./internal/svc/...` after each file split; fix any cross-file reference that needs an import update
- [ ] T086 [US5] (Step 15) Verify `git diff --stat` on the split commit shows near-zero net line delta (pure moves, no behavior changes); each new file under 400 lines
- [ ] T087 [P] [US5] (Step 16) Export `logStatusTransition(prev, next, host, timestamp)` in `frontend/src/lib/state.svelte.js` — pushes to the shared transitions ring buffer
- [ ] T088 [P] [US5] (Step 16) Export `appendPerfToRingBuffer(sample)` in `frontend/src/lib/state.svelte.js` — pushes to shared sparkline ring with existing cap
- [ ] T089 [US5] (Step 16) Replace duplicated blocks at `frontend/src/App.svelte:192-216, 284-302, 386-411, 414-431` with calls to the new helpers — keep all `$state` subscriptions in `App.svelte`, keep helpers side-effect-only on shared buffers
- [ ] T090 [P] [US5] (Step 17) Delete `deriveP95` and `deriveP50` exports from `frontend/src/lib/state.svelte.js:425, 432`
- [ ] T091 [P] [US5] (Step 17) Run `pnpm -C frontend build` to confirm no consumer; grep the rest of `frontend/src` for the names
- [ ] T092 [US5] Manual dashboard validation per quickstart.md §User Story 5: force a drain-mode transition, confirm the transition list updates once (not twice) and sparkline ring length caps correctly

**Checkpoint**: Run `go test ./... && just lint && pnpm -C frontend build`. Execute quickstart.md §User Story 5 checks. Ship as multiple PRs grouped by step, or one bundled "cleanup" PR — each step is independently testable.

---

## Phase 8: Polish & Cross-Cutting Concerns

**Purpose**: Cross-story verification, docs refresh, release readiness.

- [ ] T100 Run the full quickstart.md validation across all 5 user stories on a fresh Windows VM with an installed MSI
- [ ] T101 [P] Update `README.md` notifications section if any operator-visible text changes (SMTP transport requirement, cloud-metadata rejection)
- [ ] T102 [P] Update `docs/index.html` release notes with the 009 summary: "Security and correctness hardening — fail-closed credential paths, narrowed ACLs, greenfield DPAPI entropy, pipeline correctness fixes, module layout cleanup"
- [ ] T103 [P] Update `CHRONICLE.md` with the 009 entry covering: per-story scope decisions, cert-rotation operator procedure, SMTP transport requirement, greenfield DPAPI reason
- [ ] T104 Run `go test ./...`, `just lint`, `just vulncheck`, `pnpm -C frontend build` — all exit 0, zero warnings
- [ ] T105 Run `prek run --all-files` — all pre-commit hooks pass
- [ ] T106 Verify observability paths: service log contains the new `slog.Error/Warn` lines for each fail-closed path exercised in testing; ETW audit channel shows `EvtAccessDenied` events for pipe denies
- [ ] T107 Sync the `specs/009-security-hardening/` directory via `specify sync` (or the project's equivalent) so downstream automation sees the completed state
- [ ] T108 Mark feature 009 complete in `AGENTS.md` speckit markers

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: No dependencies; runs immediately.
- **Phase 2 (Foundational)**: Empty — no prerequisites for user stories.
- **Phase 3 (US1)**: Can start after Phase 1. Blocks external release.
- **Phase 4 (US2)**: Can start after Phase 1. Blocks external release.
- **Phase 5 (US3)**: Can start after Phase 1. T039 blocks Phase 7 T070–T072.
- **Phase 6 (US4)**: Can start after Phase 1. No cross-story dependencies.
- **Phase 7 (US5)**: Must start after Phase 5 T039 lands. Trailing cleanup.
- **Phase 8 (Polish)**: Depends on all user stories being complete for full quickstart validation.

### User Story Dependencies

- **US1 (P1)**: Independent. Ship first for pre-release blocker coverage.
- **US2 (P1)**: Independent of US1. Can ship in parallel or bundled with US1.
- **US3 (P1)**: Independent of US1, US2. Must land T039 (Update\* onto readModifyWrite) before US5.
- **US4 (P2)**: Independent of all other stories. No sequencing constraint.
- **US5 (P3)**: Depends on US3 T039. Trailing PR.

### Within Each User Story

- Helpers and primitives before callers (e.g., US1 T010 `shouldRefuseFingerprintUpdate` before T011/T012 using it; US3 T035 `readModifyWrite` before T036–T039)
- Production code before tests for structural work; tests alongside for behavioral changes (e.g., US1 mismatch tests alongside the mismatch branch)
- Documentation tasks (CHRONICLE, release notes) at end of story for accuracy

### Parallel Opportunities

- Within US1: T011 (CLI) and T012 (service) in parallel after T010 lands; tests T013, T014, T017 in parallel
- Within US2: all four tasks effectively parallel (T020/T022/T025/T026 touch different files)
- Within US3: Step 5 (T030–T032) and Step 7 (T042–T046) in parallel with each other; Step 6 (T033–T041) is sequential within itself
- Within US4: the four steps (T050–T054, T055–T058, T059–T061, T062–T066) are mutually disjoint file sets — fully parallelizable across developers
- Within US5: Step 12 (T070–T072), Step 13 (T073–T076), Step 14 (T077–T083), Step 16 (T087–T089), Step 17 (T090–T091) all run in parallel; Step 15 (T084–T086) runs sequentially within itself due to file-split ordering

---

## Parallel Example: User Story 4

```bash
# Four developers (or four codex-writer instances) can take one step each:

# Developer A — Step 8: cloud metadata rejection
Task: T050 rejectCloudMetadata helper in notify.go
Task: T051 call helper at sendWebhook/sendNtfy
Task: T054 rejection subtests in notify_test.go

# Developer B — Step 9: pipe SID check
Task: T055 sid_windows.go with callerIsPrivileged
Task: T057 sid_test.go table tests

# Developer C — Step 10: html/template
Task: T059 swap import in email.go
Task: T060 TestEmailTemplate_EscapesHTML

# Developer D — Step 11: event SystemTime
Task: T062 factor processEvent
Task: T063 parse SystemTime with fallback
Task: T064+T065 SystemTime tests
```

---

## Implementation Strategy

### MVP First (US1 + US2 + US3 bundled)

All three P1 stories are pre-external-release blockers:

1. Complete Phase 1: Setup (minutes)
2. Complete Phase 3: US1 — credential paths (S-effort per step)
3. Complete Phase 4: US2 — blast radius (S-effort per step)
4. Complete Phase 5: US3 — pipeline correctness (M-effort for RMW, S-effort for loss and pipe reader)
5. **STOP and VALIDATE**: run full Phase 8 T100 quickstart before first external release
6. Optionally ship as three separate PRs for reviewability, or one bundled "Phase 1+2" PR

### Incremental Delivery

1. **PR 1** — US1 (Phase 3): credential fail-closed paths. Ship. Validate.
2. **PR 2** — US2 (Phase 4): ACL + DPAPI entropy. Ship. Validate.
3. **PR 3** — US3 (Phase 5): evtspike loss + atomic RMW + pipe reader. Ship. Validate. ⚠️ Unblocks US5.
4. **PR 4** — US4 (Phase 6): medium paper cuts (can ship as one bundle or split by step). Ship. Validate.
5. **PR 5** — US5 (Phase 7): cleanup. Ship. Validate.
6. **PR 6** — Polish (Phase 8): release notes, docs, final verification.

### Parallel Team Strategy

With multiple developers (or codex-writer teams):

1. **Wave 1** (after Phase 1): US1, US2, US3 in parallel. One developer per story.
2. **Wave 2** (after Wave 1): US4 and the non-US5-blocking parts of Phase 7 in parallel.
3. **Wave 3** (after Wave 2): US5 remaining tasks (the ones depending on US3 T039).
4. **Final**: Phase 8 polish, single sequential pass.

Within US4, the four steps are ideal for `/codex-writer teams` mode — mutually disjoint files, all S-effort.

---

## Notes

- [P] tasks = different files, no dependencies on incomplete tasks
- [Story] label maps task to specific user story for traceability
- Every task references a remediation plan step in parentheses for drill-down into the authoritative file:line source at `docs/reviews/codex-2026-04-24-fullcodebase-remediation-plan.md`
- Verify required tests fail before implementing (TDD not strictly required but recommended for US1 refusal paths and US3 RMW stress)
- Commit after each task or logical group; each story's checkpoint is a natural PR boundary
- Stop at any user-story checkpoint to validate independently
- Avoid: editing `config.go` or `internal/svc/handler.go` in parallel across stories — these files are touched by US1, US3, and US5; sequence within a story then hand off
