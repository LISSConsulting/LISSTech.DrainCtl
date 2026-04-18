# FEATURES — Proposed and In-Progress

Forward-looking list of features we're thinking about. Lives here until it either gets scaffolded into `specs/NNN-<feature-name>/` (promoted) or gets killed. Not a roadmap or a commitment — just a shared memory so ideas don't rot in chat.

**Lifecycle:**
- `proposed` — raised in conversation, not yet sized
- `scoped` — we've talked through the shape and it's captured below
- `in-progress → specs/NNN-<name>/` — a spec folder now exists; work is underway
- **shipped** → entry removed from this file, a one-paragraph retrospective lands in `CHRONICLE.md`
- **killed** → entry removed from this file with a one-line note in `CHRONICLE.md` if the reasoning is worth remembering

**Discipline**: entries must either advance through the lifecycle or be pruned. Don't leave stale ideas to accumulate — if it's been `proposed` for more than a release cycle without anyone touching it, kill it or promote it.

---

## Proposed

### F1. Fleet-wide persistence for Overview charts (LOAD + Health Indicators)

**Status:** `proposed`

**Current behavior:** Overview page's LOAD and Health Indicators charts read from `appState.metricsHistory`, an in-memory Svelte ring buffer persisted only to `localStorage`. Data is client-local, lost on browser switch, doesn't survive cold-start across machines. Per-host detail chart is SQL-backed (feature 007); fleet-wide is not.

**Why:** operators asking "what did fleet load look like at 3am last Tuesday" currently have no answer unless a browser was left open. Retention of fleet aggregates matches the same operational need 007 solved for per-host.

**Scope sketch:** new endpoint `/api/v1/metrics/_fleet?counters=...&from=...&to=...&resolution=auto` that server-side `GROUP BY bucket_ts` across registered hosts and returns averages/P95. Frontend `App.svelte` replaces the synthesized `fleetHistory` cold-start with a `fetchMetrics('_fleet', …)` call; `MetricsChart` consumes the same shape it gets today. ~100–150 lines Go + OpenAPI entry + frontend rewiring. Promote to a `specs/008-fleet-metrics/` when ready.

**Deferred from 007:** feature 007 spec was explicitly per-host (FR-017, SC-001 all say "each host / every host"). Fleet-wide was not in scope.

---

### F2. `drainctl register` via service pipe (resolves BUGS #1)

**Status:** `scoped`

**Current behavior:** `cmd/drainctl/register_cmd.go:59` calls `dashboard.Register(dashURL)` directly from the CLI process using the interactive user's Kerberos/NTLM token. Dashboard's `/api/v1/register` is gated by `requireMachineAccount`, so running as a human returns 401.

**Why:** operators typing `drainctl register --auto` expect it to work. Requiring them to elevate to SYSTEM or run PSExec is poor UX, and the existing pipe to the service already has the right identity.

**Scope sketch:** new `register` command in the pipe protocol. Service handler invokes the existing `dashboard.Register()` with its machine-account identity and returns `RegisterResult` over the pipe. CLI tries pipe first, falls back to direct HTTP when the pipe isn't reachable (bootstrap case). `DashboardURL` persistence + TLS fingerprint pinning stay in the CLI — only the network call moves.

**Cross-ref:** BUGS.md item #1 captures the user-facing symptom. The structural fix belongs here (it's additive feature work, not a bug fix). Memory `project_register_via_service.md` has the full design.

---

### F3. `WinStationQueryInformationW` escape hatch for `change logon /disable` tracking

**Status:** `proposed` — escape hatch only, revisit on demand

**Current behavior:** `change logon /disable` flips an in-memory LSM flag that DrainCtl does not track. Declared out-of-scope in BUGS #6a after a three-round review concluded WMI subscription was too costly for the benefit.

**Why revisit:** if `/disable` tracking surfaces as an operational gap (security audit, incident where an operator ran `/disable` invisibly), we still have an option that doesn't need COM.

**Scope sketch:** direct syscall to `winsta.dll!WinStationQueryInformationW` with a suitable info class. Matches the existing `internal/perfmon/` PDH and `internal/watcher/` Reg/Evt syscall patterns. Undocumented API — validate across Windows Server 2016/2019/2022/2025 before committing. No registry SACL path, so attribution is best-effort via Security 4688 (process creation + command-line logging GPO).

**Trigger to promote:** production telemetry shows operators running `change logon /disable` and expecting it to be audited. Until then, stay with the documented `Set-RDSessionHost -NewConnectionAllowed No` workflow.

---

## In-progress

*(none)*

---

### F4. Production-shaped perf validation for 007 success criteria

**Status:** `proposed`

**Current state:** 007's T069 asked for a seeded perf harness measuring seven SC-defined latencies/sizes (chart render, zoom transition, audit query, JSONL migration, DB size, dashboard p95/p99, cadence simulator). Closed 2026-04-18 without a harness run — bounds were judged architectural and unit-level coverage via `TestRestart_LosesAtMostOneSamplingInterval` handles SC-004. The remaining SC numbers are *unobserved*, not *failing*.

**Why:** synthetic-harness numbers don't reflect actual RDSH fleet load; the first real production deployment is the more honest measurement. But if deployment surfaces a regression, we need a repeatable way to measure it.

**Scope sketch:** either a `cmd/drainctl-perf/` tool that seeds 50 hosts × 5 days + 1 year audit and runs each SC-assertion under `go test -bench` or a standalone binary, OR an `ops/` script that operates against a real installed build. Emit a CHRONICLE paragraph with the numbers. Promote to `specs/007a-perf-validation/` when a real measurement ask arrives.

**Trigger to promote:** production report of slow chart render, large DB, dashboard unresponsiveness under load, or a formal compliance ask for SC numbers.
