# Implementation Plan: Security and Correctness Hardening (009)

**Branch**: `009-security-hardening` | **Date**: 2026-04-24 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/009-security-hardening/spec.md`

## Summary

Remediate the 16 active issues surfaced by the 2026-04-24 codex full-codebase review, scope-pruned per product decisions captured in `docs/reviews/codex-2026-04-24-fullcodebase-remediation-plan.md`. The batch ships as four themed PRs: **Phase 1 security** (refuse silently-changed dashboard fingerprints; require STARTTLS or `smtps://` when SMTP auth is set; drop `SERVICE` from `config.json` ACL; add fixed-entropy DPAPI scope), **Phase 2 correctness** (plumb evtspike loss callback; atomic config read-modify-write; shared named-pipe message reader), **Phase 3 medium** (reject cloud-metadata IP in notification URLs; pipe caller-SID check for privileged verbs; `html/template` for email; event `SystemTime` for registry attribution), **Phase 4 cleanup** (delete dead `Update*` helpers; move ETW IDs to `internal/etwids`; retire `DefaultAuditPath`; split `internal/svc/handler.go`; extract shared Svelte helpers; delete orphan `deriveP95`/`deriveP50`).

Technical approach in one sentence: land each phase as an independently revertible PR against `009-security-hardening`, driven by the existing remediation plan as the authoritative file:line source, with greenfield assumptions (no migration writebacks, no deployed-agent compatibility shims) to keep per-step effort at S everywhere except the `handler.go` split.

## Technical Context

**Language/Version**: Go 1.26.2 for the service/CLI/DLL; Svelte 5 + Vite 8 for the embedded dashboard frontend.
**Primary Dependencies**: `modernc.org/sqlite`, `cobra`, `golang.org/x/sys/windows`, `layercake`, `lucide-svelte`, `html/template` (net-new consumer replacing `text/template` for email).
**Storage**: Existing `config.json` (`%ProgramData%\LISS Technologies\LISSTech DrainCtl\`) and telemetry SQLite store. No schema changes. DPAPI ciphertext format changes (entropy arg added); greenfield so no on-disk migration.
**Testing**: `go test ./...`, focused package tests (`internal/evtspike`, `internal/pipe`, `internal/svc`, root `config_test.go`, `dpapi_test.go`, `email_test.go`, `notify_test.go`), frontend build verification, `just lint`, `just vulncheck`, and pre-commit (`prek`).
**Target Platform**: Windows Server service plus browser-based dashboard clients on the same authenticated operator network.
**Project Type**: Single Go module with embedded Svelte dashboard served from `internal/dashboard` and `frontend/`.
**Performance Goals**: No regression on existing latency budgets. New entropy pass to DPAPI is O(1); SID check adds a single `GetNamedPipeClientProcessId` + `OpenProcessToken` call per privileged pipe verb (sub-millisecond). Atomic RMW holds the config mutex across one file read + one file write (already the existing save pattern's I/O cost).
**Constraints**:
- Every new or moved Go file MUST carry `//go:build windows`.
- Config remains in `config.json`; no new config system, no new top-level fields (pruned plan).
- Release version remains git-derived CalVer; no hand-maintained version strings.
- Phase 1 must ship before any external release because the DPAPI entropy change has no migration path.
- Sequencing: Phase 2 Step 6 (atomic RMW) must land before Phase 4 Step 12 (`Update*` deletion); Phases 2 and 3 are otherwise disjoint and may interleave; Phase 4 trails.
**Scale/Scope**: Touches root package (`config.go`, `dpapi.go`, `email.go`, `notify.go`, `drainctl.go`, `history.go`, `email_template.html`), CLI (`cmd/drainctl/register_cmd.go`), service (`internal/svc/handler.go` split, new pipe SID check), dashboard (`internal/dashboard/server.go` notify validation), internal (`internal/etwids/` new, `internal/evtspike/subscriber_windows.go`, `internal/pipe/pipe.go`, `internal/watcher/evtsubscribe.go`), frontend (`frontend/src/App.svelte`, `frontend/src/lib/state.svelte.js`). No installer, DLL, or schema migration expected.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

- **Windows-first delivery**: PASS. All touched Go files are already `//go:build windows` or reside in `frontend/` (non-Go). The new `internal/etwids/` package will carry the build tag. Affected operator surfaces: root package (API shrinks), CLI (`register` error path), service (pipe privilege check + handler split), dashboard (notify target validation), installer (`restrictConfigACL` tightened in `config.go`). PowerShell and DLL: unchanged.
- **Stable operator surfaces**: PASS with documented migrations.
  - **Updated**: `drainctl register` (new error on fingerprint mismatch); SMTP send path (refuses AUTH without TLS); privileged pipe verbs (deny non-admin); notify target validation (rejects cloud-metadata IP); email rendering (HTML-escape dynamic fields); registry attribution timestamp (event `SystemTime`).
  - **Deprecated**: root-package `DefaultAuditPath` (becomes alias for `DefaultDBPath`; hard removal deferred one release cycle).
  - **Removed from public root surface**: `UpdateNotifications`, `UpdateSessionThreshold`, `UpdateGracePeriod`, `UpdatePerformanceConfig`; ETW event-ID constants (moved to `internal/etwids`).
  - **Unchanged**: CLI verb list; config shape; PowerShell module; DLL exports; dashboard routes; telemetry schema.
  - Migration notes captured in release notes (spec §Compatibility / migration).
- **Tests and zero-noise verification**: PASS. Unit tests required at every FR boundary (see spec §Required tests). Integration tests for pipe caller-SID (admin vs non-admin), register mismatch flow (CLI + service helper), concurrent config RMW (20-goroutine stress), pipe `ERROR_MORE_DATA` loop via `scriptedReader`. Verification commands for completion: `go test ./...`, `just lint`, `just vulncheck`, `pnpm -C frontend build`, plus the quickstart manual walkthrough. Pre-commit (`prek`) runs `gofmt`, `go vet`, `golangci-lint`, `gitleaks`.
- **Config and release discipline**: PASS. No new config system. No new top-level `Config` fields (pruned — `NotifyAllowlist` was rejected alongside the RFC1918 guard). `config.json` ACL tightens on fresh install via `restrictConfigACL` (existing function). DPAPI ciphertext format change has no migration path (greenfield). Release versioning remains git-derived CalVer. Installer unchanged.
- **Operational observability**: PASS. New structured log signals:
  - `slog.Warn "smtp: no STARTTLS available, sending without encryption" host=…` (FR-003).
  - `slog.Error "dashboard=fingerprint-mismatch" saved=… offered=…` (FR-001, both CLI and service paths).
  - `slog.Warn "pipe=access_denied" cmd=… sid=…` plus existing `EvtAccessDenied` ETW audit event (FR-010).
  - `slog.Warn "evtspike: unparseable SystemTime" raw=…` (FR-012 fallback).
  - Existing evtspike supervisor re-subscription events are made to actually fire (FR-006).
  - No silent background behavior introduced; every refusal path emits a structured log or a returned error visible to the caller.

**Gate result**: Pass. No constitutional violations require justification at planning time.

## Project Structure

### Documentation (this feature)

```text
specs/009-security-hardening/
├── plan.md                                   # This file
├── research.md                               # Phase 0: scope decisions + alternatives
├── data-model.md                             # Phase 1: entity contracts (TLS fingerprint, DPAPI entropy, etc.)
├── quickstart.md                             # Phase 1: per-story manual validation
├── contracts/
│   ├── pipe-access-control.md                # Privileged-vs-readonly verb classification + deny shape
│   ├── register-fingerprint-refusal.md       # CLI+service mismatch-refusal behavior
│   └── notify-url-validation.md              # Cloud-metadata IP rejection rule
├── checklists/
│   └── requirements.md                       # /speckit.specify checklist
└── tasks.md                                  # Phase 2 output (/speckit.tasks — NOT created here)
```

### Source Code (repository root)

```text
# Phase 1 — Security hardening
cmd/drainctl/
├── register_cmd.go                           # Add fingerprint-mismatch refusal branch
└── register_cmd_test.go                      # Mismatch subtest

internal/svc/
└── handler.go                                # (Phase 1) Mirror fingerprint-mismatch refusal at registerWithDashboard;
                                              # (Phase 4) rename to service.go (Service struct + RunService + control-handler);
                                              #           split sibling files: piperpc.go / dashsync.go / perfsupervisor.go /
                                              #           spikesupervisor.go. Subpackage promotion deferred to a later PR.

email.go                                      # Require STARTTLS for authenticated SMTP; warn on cleartext unauth
email_test.go                                 # STARTTLS refusal subtest + HTML-escape subtest
email_template.html                           # No edits (rendering swap is code-side)

config.go                                     # Drop SERVICE from ACL; rework Update* helpers onto readModifyWrite;
                                              # retire four dead Update* helpers (Phase 4)
config_test.go                                # ACL absence verification; concurrent-RMW stress; Update* deletion follow-up

dpapi.go                                      # Add dpapiEntropy constant; pass to CryptProtectData/CryptUnprotectData
dpapi_test.go                                 # Entropy round-trip + wrong-entropy failure

# Phase 2 — Correctness
internal/evtspike/
├── subscriber_windows.go                     # Invoke loss(e) on terminal EvtNext errors; return to supervisor
└── subscriber_test.go                        # Closed-handle loss-callback subtest

internal/pipe/
├── pipe.go                                   # Share readPipeMessage helper across request + response paths
└── pipe_test.go                              # scriptedReader ERROR_MORE_DATA subtest + 1 MiB cap subtest

# Phase 3 — Medium hardening
notify.go                                     # rejectCloudMetadata helper at sendWebhook/sendNtfy entry
notify_test.go                                # 169.254.169.254 reject subtest; LAN IP allow subtest

internal/dashboard/server.go                  # Call rejectCloudMetadata at handlePutSettings + handleNotifyTest

internal/pipe/
├── sid_windows.go                            # NEW: GetNamedPipeClientProcessId + token SID check
└── sid_test.go                               # SID-verdict table via stub token-reader

email.go                                      # Swap text/template → html/template (Step 10)

internal/watcher/
├── evtsubscribe.go                           # Parse SystemTime in processEvent; fallback to time.Now() on parse error
└── evtsubscribe_test.go                      # TestProcessEvent_UsesSystemTime + TestWaitAttribution_IgnoresOlderEvents

# Phase 4 — Cleanup
internal/etwids/
└── etwids.go                                 # NEW: EvtDashboardAccess, EvtDashboardConfigChange, EvtServerRegistered,
                                              #      EvtServerRemoved, EvtAccessDenied, EvtGenericAudit

drainctl.go                                   # Delete ETW consts (moved); rename DefaultAuditPath → DefaultDBPath alias
history.go                                    # Widen HistoryOptions.DBPath semantics: accept file or dir

cmd/drainctl/main.go                          # Switch default from DefaultAuditPath to DefaultDBPath
cmd/cshared/exports.go                        # Same default switch

frontend/src/
├── App.svelte                                # Replace four duplicated blocks with shared helper calls
└── lib/state.svelte.js                       # Export logStatusTransition + appendPerfToRingBuffer;
                                              # DELETE deriveP95, deriveP50
```

**Structure Decision**: Keep the existing single-module + embedded-dashboard layout. The one net-new package is `internal/etwids/` — justified because the root `drainctl` package was accidentally becoming a shared-helper dumping ground (codex C-C1). No new public packages, no new config surfaces, no new persistence layers.

## Phase Sequencing

- **Phase 1 (Security)** — ships first, independently. Four S-effort steps; can bundle into one PR or split into four. Pre-external-release blocker.
- **Phase 2 (Correctness)** — ships after Phase 1 or in parallel. Step 6 (atomic RMW) must land before Phase 4 Step 12 (`Update*` deletion).
- **Phase 3 (Medium)** — disjoint file set from Phase 2; can interleave. No hard sequencing constraints.
- **Phase 4 (Cleanup)** — trails. All steps touch already-stabilized surfaces and benefit from Phase 2's groundwork.

## Complexity Tracking

> Constitution check passed; no violations require justification at planning time.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| (none) | | |
