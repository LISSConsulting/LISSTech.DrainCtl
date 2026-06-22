# Implementation Plan: Event Log Anomaly Detection (evtspike)

**Branch**: `006-evtspike-detection` | **Date**: 2026-04-16 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/006-evtspike-detection/spec.md`

## Summary

Detect anomalous Windows event-log activity on RDSH hosts by subscribing to ~54 channels, counting arrivals into 10-second scoring buckets (each bucket is scored directly; no further aggregation), and scoring each bucket against a per-channel, per-time-of-day Gamma-Poisson baseline with Negative-Binomial tail probability. Confirmed spikes (≥2 of 3 consecutive windows) are fanned out through the existing notification pipeline via a new `event_spike` trigger. The detector ships as an optional, config-gated subsystem inside the existing DrainCtl service — in-process direct function calls, no inter-process communication. Baseline state (~135 KB per host) persists to an atomically-written JSON file for warm restart. A small dashboard surface (status pill + recent-spikes list) is added to the existing Svelte dashboard; no new dashboard pages. The `Security` channel is excluded from defaults; admins opt in via the `evtspike.security_channel_enabled` config flag. At subsystem Start, if the flag is set, the service enables `SeSecurityPrivilege` on its own token via `AdjustTokenPrivileges` (LocalSystem already has this privilege present in its token; Disabled state by default). No MSI opt-in component and no LSA account-rights operations.

The POC on `feat/evtspike-poc` already validates the detection algorithm. The remaining work is productization: lifting the detector package into the service codebase, wiring it into Config / Notify / dashboard, and surfacing the Security opt-in config flag. A standalone CLI / separate MSI component are out of MVP scope (see spec.md Clarifications).

## Technical Context

**Language/Version**: Go 1.26+ (`//go:build windows` on every file; git-derived CalVer `YY.MM.BUILD` per CLAUDE.md).
**Primary Dependencies**:
- `golang.org/x/sys/windows` — `wevtapi.dll` (`EvtSubscribe`, `EvtNext`, `EvtClose`) already wired in POC; `AdjustTokenPrivileges` / `LookupPrivilegeValue` for enabling `SeSecurityPrivilege` on the service's own token when Security monitoring is opted in.
- Existing DrainCtl packages: `notify.go`, `config.go` (for new `EvtSpike` config block and `event_spike` trigger), `internal/dashboard` (for status + recent-spikes broker events), `internal/svc` (for service lifecycle).
- No new WiX / MSI components. The existing MSI project is unchanged by this feature.

**Storage**: JSON file at `%ProgramData%\LISS Technologies\LISSTech DrainCtl\evtspike-baseline.json` (atomic replace via temp file + `MoveFileEx`, matching how `config.go` writes `config.json`). Baseline is ~135 KB per host at the default channel list (54 channels × 96 slots × 2 floats × 8 bytes + metadata).

**Testing**: `go test ./...` with table-driven tests, following existing DrainCtl conventions (see `notify_test.go`, `config_test.go`, `cmd/evtspike/detector_test.go`). New tests:
- Unit: detector scoring, robust-cap update, 2-of-3 confirmation, slot maturity, slot fallback to global, baseline JSON round-trip, config merge (default + disable-list + add-list).
- Contract: `event_spike` webhook payload, email template rendering, ntfy priority mapping.
- Integration: end-to-end detector → notify path with a fake target; warm-restart smoke test; mid-run channel-loss + retry; Security channel opt-in flag + `AdjustTokenPrivileges` enable path.

**Target Platform**: Windows Server 2019+ running RDSH / Citrix workloads. 32-bit builds out of scope.

**Project Type**: Single Windows service, no new binaries. Fits the existing DrainCtl shape: root package exposes public API, `cmd/drainctl` is the user CLI, `cmd/cshared` is the DLL. The POC's `cmd/evtspike` binary is used as a dev-only smoke harness during transition and is not shipped in the MSI; the detector library lives in `internal/evtspike/`.

**Performance Goals** (measured per the `stress-performance-workload` defined in spec.md):
- SC-002: ≤30 seconds (three 10-second scoring buckets) from anomaly onset to notification fired.
- SC-007: steady-state CPU <5% of one core (1-hour average); memory <50 MB RSS.
- Baseline write volume: ~13 MB/day total (96 writes/day × ~135 KB per write at the default cadence), acceptable vs. typical SSD endurance. Net state-change per write is small; the atomic-replace model rewrites the full file each tick.
- Baseline write cadence: default once every 15 minutes (slot-rollover aligned at the default 900s interval; see `data-model.md` §PersistIntervalSeconds for non-default alignment semantics). A crash loses at most 15 minutes of learning.

**Constraints**:
- Windows-only: every new `.go` file needs `//go:build windows`.
- Signing order: sign binaries → build MSI → sign MSI (`just release`). No new binaries or MSI components are added by this feature.
- No viper: config lives in `%ProgramData%\LISS Technologies\LISSTech DrainCtl\config.json` (encoding/json). `EvtSpikeConfig` goes inside the existing `Config` struct.
- Retention `ClampRetention()` pattern: any new numeric configurable must clamp (slot-maturity threshold in [1, 100]; cooldown in [1s, 24h]; persistence cadence in [1 min, 24h]).
- `prek` pre-commit hook runs gofmt, go vet, golangci-lint, gitleaks — all new code must pass cleanly per `feedback_clean_output.md` (zero warnings).

**Scale/Scope**:
- 54 default channels × 96 time-of-day slots × 2 floats (α, β) per slot + global summary + metadata ≈ 135 KB per host baseline file.
- Per host: ~54 event subscriptions, one counter bucket per channel at 10 s resolution.
- Single-host detector only; no cross-host aggregation.

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

**Post-Phase 1 re-check**: The Phase 1 artifacts introduce one new file type (baseline JSON), two new dashboard endpoints, and two new SSE event types. Each hits the existing atomic-write path, existing broker / REST patterns. No new build-system additions, no new binaries, no new MSI components. **Re-check result: PASS.**

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
│   ├── evtspike-config.md        # Config schema for the EvtSpike block
│   └── dashboard-sse-events.md   # New SSE event types: detector_status, recent_spike
├── checklists/
│   └── requirements.md           # (already present)
├── fixtures/                     # Test fixtures for SC validation workloads
│   ├── normal-day.json           # normal-day-false-positive-workload trace (SC-001, SC-003)
│   └── stress.json               # stress-performance-workload trace (SC-007)
└── tasks.md                      # Phase 2 output — produced by /speckit.tasks, not /speckit.plan
```

### Source Code (repository root)

Additive changes to the existing repo layout.

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
└── evtspike/                     # POC, kept as dev-only smoke harness during transition.
                                  # Detector code moves to internal/evtspike. Not shipped in MSI.
                                  # Can be deleted after internal/evtspike is fully tested.

internal/
├── evtspike/                     # NEW: the detector library used by the in-service subsystem
│   ├── detector.go               # Gamma-Poisson / NegBin / 2-of-3 / robust cap (lifted from POC)
│   ├── detector_test.go          # Unit tests (lifted + expanded)
│   ├── baseline.go               # NEW: JSON round-trip, atomic write, version tag
│   ├── baseline_test.go          # NEW: round-trip, corrupt-file, version-mismatch tests
│   ├── channels.go               # NEW: default 54-channel list + ResolveChannels(cfg) honoring disabled/added + security_channel_enabled
│   ├── channels_test.go          # NEW: merge behavior, case-insensitive dedup
│   ├── subscriber_windows.go     # NEW: EvtSubscribe + EvtNext drain loop (lifted from POC main)
│   ├── privilege_windows.go      # NEW: AdjustTokenPrivileges helper for SeSecurityPrivilege enable
│   ├── subsystem.go              # NEW: the lifecycle object the service embeds (Start/Stop, live reload, persistence ticker, notify dispatch, subscription retry)
│   └── subsystem_test.go         # NEW: fake subscriber + fake notifier integration tests
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

msi/                              # WiX 5 project — UNCHANGED by this feature.

docs/
└── index.html                    # EDIT: CalVer bump; brief feature callout
```

**Structure Decision**: Single-project (Windows-only) layout, additive only. The detector library lives in `internal/evtspike/`. The existing dashboard's SSE broker gains two new event types rather than a new page. No new binaries, no new MSI components, no new IPC. Security opt-in is a config flag plus an `AdjustTokenPrivileges` call on the service's own token at subsystem Start.

## Complexity Tracking

*No constitution violations to justify.* The feature reuses the existing service shell, config file, notification pipeline, and dashboard broker. No new infrastructure — Security opt-in is a config-level decision handled inside the Go service.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| (none) | — | — |
