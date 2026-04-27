# Implementation Plan: Agent Self-Poll Auto-Update (010)

**Branch**: `010-auto-update` | **Date**: 2026-04-26 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/010-auto-update/spec.md`

## Summary

Add a small `internal/updater` subsystem that polls GitHub's releases API on a 24h-ish jittered cadence, downloads the signed MSI when there's a newer version, verifies its Authenticode signature against `LISS Consulting, Corp.`, spawns msiexec detached, and triggers the service's own clean shutdown so msiexec can replace files. The PR also lands the **first migration to the LCI**: a new `internal/lifecycle` package with the `Subsystem` interface, plus a compile-time conformance assertion for `internal/evtspike` (which already matches the shape) and for the new `updater.Subsystem`.

Technical approach in one sentence: ship a leak-proof leak-free goroutine + http.Client + WinVerifyTrust trio behind one public `Subsystem` type, wired into `Execute` via the same call-site pattern the LCI mandates, with the entire feature gated on `config.Update.Enabled` so operators who don't want it can disable in one config flip.

## Technical Context

**Language/Version**: Go 1.26.2; service-only feature (no frontend, no DLL changes).
**Primary Dependencies**: `net/http` (stdlib), `golang.org/x/sys/windows` (already a transitive dep), `wintrust.dll` + `crypt32.dll` (Win32, called via `windows.NewLazySystemDLL` like the existing evtspike subscriber pattern). No new third-party Go dependencies.
**Storage**: No persisted state — ETag held in memory only; no telemetry-DB schema changes. Durable persistence of version transitions to the audit store is **deferred to a follow-up spec** (see research.md Decision 10); v1 records each transition via `slog.Info` only. Config gains an `update` object (see data-model.md §1).
**Testing**: `go test ./internal/updater/...`, `go test ./internal/evtspike/...` (LCI conformance assertion), `go test ./internal/svc/...` (Execute call-site integration), `go test -race ./...`, `just lint`, `just vulncheck`.
**Target Platform**: Windows Server / Windows desktop running drainctld as an SCM service. CLI/DLL paths do not run the updater.
**Project Type**: Single Go module; touched packages limited to `internal/lifecycle/` (new), `internal/updater/` (new), `internal/evtspike/` (LCI assertion only — no behavior change), `internal/svc/handler.go` (subsystem registration), root `config.go` (UpdateConfig field).
**Performance Goals**: Steady-state cost is one HTTPS request per 24h returning 304 (~few KB). Backoff path bounded at 24h. Memory: one HTTP client, one goroutine, no buffers held between polls.
**Constraints**:
- Every new Go file MUST carry `//go:build windows`.
- LCI conformance: `internal/updater` and `internal/evtspike` MUST have compile-time `var _ lifecycle.Subsystem = (*X)(nil)` assertions.
- No new third-party Go modules — Win32 calls go through the existing LazyDLL pattern (see `internal/dashboard/sspi.go`, `internal/evtspike/subscriber_windows.go`).
- Updater MUST NOT block service shutdown beyond the existing 10s `waitTelemetryWorkers` budget. Stop must drain within ~5s in the worst case.
- No persisted state across service restarts (FR-015).
**Scale/Scope**: ~250-400 LoC of net-new Go (updater package + tests), plus ~20 LoC LCI declarations, plus ~10 LoC config field, plus ~20 LoC `Execute` wiring. Total feature is small.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

- **Windows-first delivery**: PASS. All new files are `//go:build windows`. Touched operator surfaces: service (new `update` config object surfaced in `config.json`), audit log (new event string). PowerShell, DLL, frontend, and CLI verbs unchanged.
- **Stable operator surfaces**: PASS with one documented addition.
  - **New**: `Update` object in `config.json` (`enabled` bool, `channel` string with values `"stable"` or `"prerelease"`, `poll_interval` Go duration string). Default-constructed on first load post-upgrade as `{enabled: false, channel: "stable", poll_interval: "24h"}` — opt-in by design (see [research.md Decision 8](./research.md)). Note that with `--prerelease`-only releases today, `enabled=true` AND `channel="prerelease"` are both required for any auto-update activity until the project ships a non-prerelease release.
  - **Unchanged**: CLI verbs, pipe verbs, dashboard routes, telemetry schema, MSI install behavior.
- **Tests and zero-noise verification**: PASS. Unit tests at every FR boundary (version parse, signature verify three cases, poll-loop 200/304/503, backoff state machine). Integration test against a fake GitHub server asserting the full Start/poll/decide/Stop cycle. LCI conformance enforced at compile time via `var _ lifecycle.Subsystem = ...` lines that fail `go build ./...` if either subsystem regresses out of shape. Verification commands: `go test ./...`, `go test -race ./internal/updater/ ./internal/svc/ ./internal/evtspike/`, `just lint`, `just vulncheck`.
- **Config and release discipline**: PASS. New top-level `Update` object is the minimum surface (no top-level scalars proliferating). Field defaults are inline in `LoadConfig`; missing-field handling uses Go zero-value + explicit defaulting. Release versioning remains git-derived CalVer.
- **Operational observability**: PASS. New structured log signals:
  - `slog.Debug "update=poll_start url=… current=…"` (every poll).
  - `slog.Debug "update=not_modified etag=…"` (304 path).
  - `slog.Info  "update=up_to_date current=…"` (200, no newer).
  - `slog.Info  "update=installing remote=…"` (decision to install).
  - `slog.Warn  "update=refused reason=…"` (signature failures, network failures).
  - `slog.Info  "update=installed_pending_restart remote=…"` (msiexec spawned, service shutting down).
  - The `slog.Info "update=installing old=… new=…"` line IS the v1 record of the version transition (file log keeps 7 days). Durable audit-row persistence is deferred — see research.md Decision 10. No silent-failure paths; every refusal emits a structured log naming the reason.

**Gate result**: Pass.

## Package layout

```
internal/lifecycle/
    lifecycle.go              // Subsystem interface + Statusable extension. ~30 LoC.

internal/updater/
    updater_windows.go        // Subsystem struct, Start/Stop, poll loop. ~150 LoC.
    github_windows.go         // GitHub releases API client (single function). ~60 LoC.
    verify_windows.go         // WinVerifyTrust + Subject CN check. ~80 LoC.
    install_windows.go        // Detached msiexec spawn + service shutdown trigger. ~40 LoC.
    version_windows.go        // Parse + compare CalVer tags. ~30 LoC.
    backoff_windows.go        // Exponential backoff state machine. ~30 LoC.
    *_test.go                 // Unit tests; integration test in updater_integration_windows_test.go.
```

`internal/lifecycle` is intentionally tiny — one file, one interface, one optional capability. It is NOT a place for shared utilities; it MUST stay a contract-only package.

## Sequencing

The whole feature ships as one PR. Internal commit order on the branch:

1. **Introduce LCI** — `internal/lifecycle/lifecycle.go` (interface + `Statusable`). Empty package. ~5 LoC of test (a no-op compile assertion against an in-test type).
2. **Migrate evtspike to LCI** — add `var _ lifecycle.Subsystem = (*Subsystem)(nil)` to `internal/evtspike/subsystem.go`. Zero behavior change; this is the demonstration migration per STP §"Migration order #1". Build must stay green.
3. **Add UpdateConfig to root config** — new struct, default-construction in `LoadConfig`, JSON round-trip test in `config_test.go`.
4. **Build the updater** — small commits in the order: version → backoff → github (HTTP client) → verify (WinVerifyTrust) → install (msiexec spawn) → updater (Start/Stop wiring it all together).
5. **Wire updater into Execute** — register the subsystem in `drainService.subsystems`. The `Execute` shape stays mostly unchanged for now; the STP §"End state" refactor is a future PR. We just add `updaterSub.Start(ctx)` next to the existing inline goroutines and `updaterSub.Stop()` next to `waitTelemetryWorkers`.
6. **Release notes / docs/guide.html update** — describe the new `update` config and the opt-out path.

(Step "Audit-log event" was removed mid-implementation — durable persistence deferred per research.md Decision 10. The install path emits `slog.Info "update=installing old=… new=…"` which lands in the daily file log instead.)

Phase 4 of the STP migration order (registry watcher, config-file watcher, named-pipe server, etc.) is OUT of this PR. The next non-hot PR migrates one of them per the ratchet rule.

## Risks and mitigations

| Risk | Likelihood | Mitigation |
|---|---|---|
| WinVerifyTrust integration is fiddly; signature checks pass when they shouldn't or fail when they should | Medium | Three-case unit test in `verify_windows_test.go` using fixture MSIs (real-signed, unsigned, self-signed) checked into the repo or generated in `TestMain`. CI runs them on every build. |
| Detached msiexec spawn doesn't actually survive service exit; the upgrade hangs | Medium | Integration test in a Windows VM via the existing manual smoke test (quickstart.md). Single-host validation before any release that ships the feature. |
| The 5-15 min initial-poll delay accidentally races with config reload (operator flips `enabled=false` between Start and first poll) | Low | The poll loop reads `enabled` fresh each tick, not at Start. If it's been flipped off, the tick is a no-op. Documented in updater_windows.go. |
| GitHub API rate-limit (60/h unauthenticated) trips on a fleet of 100+ agents polling the same repo | Low | Per-host 24h ± 2h jitter spreads the load. 100 agents × 1 call/day = ~4 calls/h average against the unauth limit. Anything more is a fleet size we'd want a proper distribution channel for. |
| MSI install fails (disk full on system drive, AV interference) and the host is bricked | Low | Install runs `msiexec /norestart`; failures leave the OLD service intact. SCM keeps the old service running because we only triggered our own ctx cancel — if the new install fails, the old binary is still on disk and can restart. |
| Auto-update interacts badly with config.json migration on a future release | Medium | Treat `update.enabled=true` as the default in every future release. Operators who explicitly set `false` keep that setting through upgrades. Standard JSON-merge semantics. |

## Out-of-scope items (NOT in this plan)

Cross-references to spec.md §"Out of scope":

- Manual `drainctl update check` CLI verb — deferred. Adds a new pipe verb + a new CLI command; not load-bearing for the core value.
- Pinning / channel separation / staged rollout — deferred to dashboard-pulled feature.
- Pre-flight health check on the new version with automatic rollback — deferred. Out-of-the-box Windows SCM behavior is acceptable for v1.
- Persisted ETag / last-poll-timestamp — deferred (FR-015). Simpler is safer for the initial version.

## What lands on day 1

Day 1 of this PR being merged:
- Operators on the new release get the `update` object on next `LoadConfig` write (which happens any time the dashboard or CLI mutates config). Defaults are `enabled=false, channel="stable"` — **no host auto-deploys without explicit operator action**.
- The release notes for 010 explain the `update` object and the operationally useful combo today (`enabled=true, channel="prerelease"` while everything is still flagged prerelease).
- Operators who want auto-update opt in by editing `config.json`. The poll loop comes online within the watcher's reload latency (a few seconds) without a service restart.
- The first auto-deploy that actually fires is whatever release follows 010, and only on the subset of hosts that opted in. There is no fleet-wide rollout event tied to the 010 release itself.

## Acceptance gate for merge

- All unit tests pass under `go test -race`.
- LCI conformance assertions compile.
- `just lint` clean. `just vulncheck` clean.
- Integration test against the fake GitHub server passes (no real network calls).
- Manual smoke test on one Windows host: install pre-010 version, install 010 version, observe a synthetic newer version (e.g., flipping the version constant or pointing at a private staging repo) trigger a real msiexec install end-to-end.
- Codex review of the merged PR (single-shot, no validation loop) approves the verifier and the install-spawn paths specifically — those are the highest-risk surfaces.
