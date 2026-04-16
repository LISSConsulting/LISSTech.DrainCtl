# Implementation Plan: Event Log Anomaly Detection (evtspike)

**Branch**: `006-evtspike-detection` | **Date**: 2026-04-16 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/006-evtspike-detection/spec.md`

## Summary

Detect anomalous Windows event-log activity on RDSH hosts by subscribing to ~54 channels, counting arrivals into 10-second buckets aggregated into 60-second rolling scoring windows, and scoring each window against a per-channel, per-time-of-day Gamma-Poisson baseline with Negative-Binomial tail probability. Confirmed spikes (≥2 of 3 consecutive windows) are fanned out through the existing notification pipeline via a new `event_spike` trigger. The detector ships two ways: (a) as an optional, config-gated subsystem inside the existing DrainCtl service, and (b) as a standalone CLI that can also install as a Windows service and forward spikes over the existing `\\.\pipe\drainctl` named pipe. Baseline state (~135 KB per host) persists to an atomically-written JSON file for warm restart. A small dashboard surface (status pill + recent-spikes list) is added to the existing Svelte dashboard; no new dashboard pages. The `Security` channel is excluded from defaults; admins opt in at install time via an MSI component that grants `SeSecurityPrivilege` to the DrainCtl service account and revokes it on opt-out.

The POC on `feat/evtspike-poc` already validates the detection algorithm. The remaining work is productization: lifting the detector package into the service codebase, wiring it into Config / Notify / dashboard / named pipe, making the standalone a Windows service, and adding the MSI component for Security opt-in.

## Technical Context

**Language/Version**: Go 1.26+ (`//go:build windows` on every file; CalVer `YY.DOY.patch` versioning updated in seven places per CLAUDE.md).
**Primary Dependencies**:
- `golang.org/x/sys/windows` — `wevtapi.dll` (`EvtSubscribe`, `EvtNext`, `EvtClose`) already wired in POC.
- `golang.org/x/sys/windows/svc` + `svc/mgr` — for installing the standalone CLI as a Windows service.
- Existing DrainCtl packages: `notify.go`, `config.go` (for new `EvtSpike` config block and `event_spike` trigger), `internal/pipe` (for forwarding), `internal/dashboard` (for status + recent-spikes broker events), `internal/svc` (for service lifecycle).
- WiX 5 — new optional MSI component "Enable Security event log monitoring" with a C# / WiX `CAQuietExec` custom action that calls `ntrights.exe` (or an equivalent P/Invoke to `LsaAddAccountRights` / `LsaRemoveAccountRights`) to grant/revoke `SeSecurityPrivilege` to the service account.

**Storage**: JSON file at `%ProgramData%\LISS Technologies\LISSTech DrainCtl\evtspike-baseline.json` (atomic replace via temp file + `MoveFileEx`, matching how `config.go` writes `config.json`). Baseline is ~135 KB per host at the default channel list (54 channels × 96 slots × 2 floats × 8 bytes + metadata).

**Testing**: `go test ./...` with table-driven tests, following existing DrainCtl conventions (see `notify_test.go`, `config_test.go`, `cmd/evtspike/detector_test.go`). New tests:
- Unit: detector scoring, robust-cap update, 2-of-3 confirmation, slot maturity, slot fallback to global, baseline JSON round-trip, config merge (default + disable-list + add-list).
- Contract: `event_spike` webhook payload, pipe message schema, MSI custom-action privilege grant idempotence.
- Integration: end-to-end detector → notify path with a fake target; pipe forwarder from standalone → service; warm-restart smoke test.

**Target Platform**: Windows Server 2019+ running RDSH / Citrix workloads. 32-bit builds out of scope.

**Project Type**: Single Windows service (with a secondary CLI/service binary). Fits the existing DrainCtl shape: root package exposes public API, `cmd/drainctl` is the user CLI, `cmd/cshared` is the DLL, `cmd/evtspike` is the standalone binary. No new top-level project layout.

**Performance Goals**:
- SC-002: ≤3 minutes (three 60-second scoring windows) from anomaly onset to notification fired.
- SC-007: steady-state CPU a small single-digit percentage of one core; memory well under 50 MB.
- SC-008: standalone CLI first scoring pass within 15 s of launch.
- Baseline write: default once every 15 minutes (one per slot rollover) — sized so a day of writes is <0.1 MB/day churn, and a crash loses at most 15 minutes of learning.

**Constraints**:
- Windows-only: every new `.go` file needs `//go:build windows`.
- Signing order: sign binaries → build MSI → sign MSI (`just release`). The evtspike service binary and its MSI component must be added to both stages.
- MSI/CLI flag sync: any new flag exposed on `evtspike` CLI must be mirrored in the corresponding MSI install behavior; per `feedback_msi_cli_sync.md`, a drift here broke v26.100.0.
- No viper: config lives in `%ProgramData%\LISS Technologies\LISSTech DrainCtl\config.json` (encoding/json). `EvtSpikeConfig` goes inside the existing `Config` struct.
- Retention `ClampRetention()` pattern: any new numeric configurable must clamp (slot-maturity threshold in [1, 100]; cooldown in [1s, 24h]; persistence cadence in [1 min, 24h]).
- `prek` pre-commit hook runs gofmt, go vet, golangci-lint, gitleaks — all new code must pass cleanly per `feedback_clean_output.md` (zero warnings).
- Named-pipe cross-process use: the standalone CLI opens `\\.\pipe\drainctl` as a client; a new `cmd` value ("spike") needs to be added to `PipeRequest` with graceful handling for older services.

**Scale/Scope**:
- 54 default channels × 96 time-of-day slots × 2 floats (α, β) per slot + global summary + metadata ≈ 135 KB per host baseline file.
- Per host: ~54 event subscriptions, one counter bucket per channel at 10 s resolution.
- Single-host detector only; no cross-host aggregation (already excluded by spec's "Standalone pipe scope").

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

The project constitution at `.specify/memory/constitution.md` is a placeholder template (unfilled). No enumerated principles are in force. The de facto constitution for this repo is the `CLAUDE.md` rule-set:

| Gate | Rule | Compliance |
|------|------|------------|
| Windows build tags | Every `.go` file carries `//go:build windows` | PLAN: all new files tagged |
| CalVer versioning | Update version in seven places on any shippable change | PLAN: included in task list once tasks are generated |
| No viper | Config lives in `config.json` via `encoding/json` | PLAN: `EvtSpikeConfig` added as nested struct in existing `Config`, reused atomic-write path |
| Retention clamping | Any tunable must clamp | PLAN: `ClampRetention`-style helpers for new tunables |
| Signing order | Binaries → MSI → sign MSI | PLAN: evtspike binary + MSI component integrated into `just release` |
| MSI/CLI flag sync | WiX custom actions must match CLI flags | PLAN: explicit contract in `contracts/msi-security-opt-in.md` |
| Pre-commit cleanliness | gofmt, vet, golangci-lint, gitleaks pass | PLAN: tests gate, no suppressions |
| Structured logging | slog, dual-sink (ETW + file) | PLAN: new subsystem logs through existing slog setup |

**Gate result: PASS** (vacuous principles + CLAUDE.md rules all satisfiable by design).

**Post-Phase 1 re-check**: The Phase 1 artifacts introduce one new file type (baseline JSON), one new named-pipe command (`"spike"`), two new dashboard endpoints, two new SSE event types, and one new MSI feature. Each hits the existing atomic-write path, existing pipe framing, existing broker / REST patterns, and the existing WiX project. No new build-system additions. No new CLI flag drift (the `evtspike` CLI gains `install-service` / `uninstall-service` / `run-service` subcommands but no flags that need mirroring in the MSI — the MSI-resident decisions are feature selection and the Security opt-in component, both captured in `contracts/msi-security-opt-in.md`). **Re-check result: PASS.**

## Project Structure

### Documentation (this feature)

```text
specs/006-evtspike-detection/
├── plan.md                       # This file
├── research.md                   # Phase 0 output
├── data-model.md                 # Phase 1 output
├── quickstart.md                 # Phase 1 output
├── contracts/
│   ├── event_spike-payload.md    # Webhook/ntfy/email payload schema for the new trigger
│   ├── pipe-spike-message.md     # Standalone → service named-pipe message
│   ├── evtspike-config.md        # Config schema for the EvtSpike block
│   ├── dashboard-sse-events.md   # New SSE event types: detector_status, recent_spike
│   └── msi-security-opt-in.md    # MSI component behavior + privilege grant/revoke contract
├── checklists/
│   └── requirements.md           # (already present)
└── tasks.md                      # Phase 2 output — produced by /speckit.tasks, not /speckit.plan
```

### Source Code (repository root)

Additive changes to the existing repo layout. Nothing is moved for its own sake; the POC's `cmd/evtspike/` stays and gets extended.

```text
# Existing (unchanged layout)
/ (root package drainctl)
├── config.go                     # EDIT: add EvtSpikeConfig struct; add TriggerEventSpike
├── notify.go                     # EDIT: add event_spike payload branch; update NotifyState
├── drainctl.go                   # EDIT: CalVer bump; wire EvtSpike subsystem into Run()
├── drainctl.rc                   # EDIT: CalVer bump; recompile .syso via `just resource`
└── ...

cmd/
├── drainctl/                     # CLI (cobra) — unchanged by this feature
├── cshared/                      # DLL — unchanged
└── evtspike/                     # EDIT: productize POC
    ├── main.go                   # Becomes a thin wrapper: foreground + `install-service` / `uninstall-service` / `run-service`
    ├── service.go                # NEW: svc.Run handler (Windows service glue)
    ├── detector.go               # MOVE to internal/evtspike (kept for Go test import convenience during transition)
    └── detector_test.go          # MOVE to internal/evtspike

internal/
├── evtspike/                     # NEW: the detector library reused by both deployment modes
│   ├── detector.go               # Gamma-Poisson / NegBin / 2-of-3 / robust cap (lifted from POC)
│   ├── detector_test.go          # Unit tests (lifted + expanded)
│   ├── baseline.go               # NEW: JSON round-trip, atomic write, version tag
│   ├── baseline_test.go          # NEW: round-trip, corrupt-file, version-mismatch tests
│   ├── channels.go               # NEW: default 54-channel list + merge(default, disable, add)
│   ├── channels_test.go          # NEW: merge behavior, case-insensitive dedup
│   ├── subscriber_windows.go     # NEW: EvtSubscribe + EvtNext drain loop (lifted from POC main)
│   ├── subsystem.go              # NEW: the lifecycle object the service embeds (Start/Stop, live reload, persistence ticker, notify dispatch, pipe fan-in)
│   └── subsystem_test.go         # NEW: fake subscriber + fake notifier integration tests
├── pipe/
│   ├── pipe.go                   # EDIT: add "spike" PipeRequest.Cmd; add SpikePayload type
│   ├── conn_windows.go           # (unchanged)
│   └── spike_forward_windows.go  # NEW: standalone-side client that dials the pipe and forwards spikes
├── dashboard/
│   ├── broker.go                 # EDIT: add detector_status + recent_spike event types
│   ├── server.go                 # EDIT: new read-only endpoints GET /api/evtspike/status, GET /api/evtspike/spikes?host=
│   └── ...                       # existing tests get additive coverage
├── svc/
│   └── svc.go                    # EDIT: construct the EvtSpike subsystem when EvtSpikeConfig.Enabled is true; pass it into Run loop
└── ...

ui/                               # existing Svelte 5 dashboard (feature-complete per memory)
└── src/
    ├── lib/
    │   ├── api.js                # EDIT: fetchEvtSpikeStatus, fetchRecentSpikes; SSE event handlers for new event types
    │   └── types.ts (or .d.ts)   # EDIT: add DetectorStatus, RecentSpike types
    └── components/
        ├── ServerCard.svelte     # EDIT: render detector status pill next to existing server pills
        └── ServerDetail.svelte   # EDIT: add "Recent spikes" list section reusing existing list styling

msi/                              # WiX 5 project (under existing .wixproj)
├── product.wxs                   # EDIT: add <Feature Id="SecurityEventLog"> with <ComponentRef> for the opt-in
├── SecurityOptIn.wxs             # NEW: component + custom actions (grant/revoke)
└── ca/                           # NEW (if needed): native custom-action DLL wrapping LsaAddAccountRights

docs/
└── index.html                    # EDIT: CalVer bump; brief feature callout
```

**Structure Decision**: Single-project (Windows-only) layout, additive only. The detector library lives in `internal/evtspike/` so it is shared by both the in-service subsystem and the standalone CLI. The existing `cmd/evtspike/` binary becomes the standalone entry point (foreground + service install). The existing `internal/pipe/` gains a `spike` command so standalone forwarding reuses the same endpoint the dashboard and CLI already use. The existing dashboard's SSE broker gains two new event types rather than a new page. The MSI gets one new optional Feature for Security opt-in.

## Complexity Tracking

*No constitution violations to justify.* The feature reuses the existing service shell, config file, notification pipeline, named pipe, dashboard broker, and MSI project. The only genuinely new infrastructure is the MSI Security-opt-in component (unavoidable — it's the product decision).

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| (none) | — | — |
