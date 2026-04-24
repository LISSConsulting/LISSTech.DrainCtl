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

### F5. Split `internal/svc/handler.go` into subsystem-named siblings

**Status:** `scoped` — deferred from 009 (T084-T086 / FR-016)

**Current state:** `internal/svc/handler.go` is 1138 lines mixing pipe RPC dispatch, dashboard registration + config pull, perf collector lifecycle, evtspike reload loop, and Windows service startup in one file. Codex 2026-04-24 flagged it as C-C3 (god-file). First attempt to split landed mid-009 produced tangled duplicate declarations (codex extracted partial content into siblings without removing originals from the keeper file); reverted cleanly, handler.go remains behavior-correct.

**Why:** the next bug fix in any one of those five subsystems currently touches three unrelated subsystems in the same file. Noise-to-signal ratio on every PR in this area is already bad; the 009 work surfaced it repeatedly.

**Scope sketch (already decided):**
- Keep `Service` struct + `RunService` + Windows service control-handler shim → rename `handler.go` → `service.go`.
- Move pipe RPC dispatch + per-verb handlers → `piperpc.go`.
- Move dashboard registration + periodic config pull → `dashsync.go` (contains the US1 `shouldRefuseFingerprintUpdate` helper).
- Move perf collector lifecycle supervisor → `perfsupervisor.go`.
- Move evtspike reload supervisor → `spikesupervisor.go`.
- All files stay `package svc` with `//go:build windows`. No subpackages. No signature changes. Each new file under 400 lines.

Naming rationale is captured in `specs/009-security-hardening/research.md` §Decision 8 — the `handler_<subsystem>.go` prefix was explicitly rejected (dead weight in-package), and `handler_lifecycle.go` was merged into `service.go` because the Windows service run loop *is* the service file.

**Trigger to promote:** cut `010-handler-split` off `develop` after 009 lands. Pure cut-and-paste refactor; verify via `git diff --stat` showing near-zero net line delta and each sibling file under 400 lines.

---

### F6. Promote `internal/svc/` subsystems to subpackages

**Status:** `proposed` — revisit after F5 lands

**Current behavior:** after F5, `internal/svc/` will hold five sibling files all in `package svc` with shared unexported helpers and package-level state. Subsystem coupling is still hidden as same-package function calls rather than explicit imports.

**Why revisit:** once the subsystems live in separate files, the natural next question is whether they want separate packages. Package boundaries would force explicit interfaces, make cross-subsystem coupling visible as imports, and prevent `handler_`-style naming drift from recurring.

**Scope sketch:** promote each subsystem to its own subpackage — `internal/svc/piperpc/`, `internal/svc/dashsync/`, `internal/svc/perfsup/`, `internal/svc/spikesup/`. `internal/svc/` itself keeps only `Service`, `RunService`, and wiring. Requires promoting now-unexported symbols to exported across the new package boundaries and potentially moving tests along with their subjects.

**Trigger to promote:** after F5 lands and operators observe two or more bug fixes where subsystem coupling across the five files produced merge churn. Don't do it preemptively — F5 may prove sufficient.

**Cross-ref:** 009 research `§Decision 8` explicitly rejected subpackage promotion for the 009 cycle as "too wide a refactor to blend with security + correctness work."

---

### F4. Production-shaped perf validation for 007 success criteria

**Status:** `proposed`

**Current state:** 007's T069 asked for a seeded perf harness measuring seven SC-defined latencies/sizes (chart render, zoom transition, audit query, JSONL migration, DB size, dashboard p95/p99, cadence simulator). Closed 2026-04-18 without a harness run — bounds were judged architectural and unit-level coverage via `TestRestart_LosesAtMostOneSamplingInterval` handles SC-004. The remaining SC numbers are *unobserved*, not *failing*.

**Why:** synthetic-harness numbers don't reflect actual RDSH fleet load; the first real production deployment is the more honest measurement. But if deployment surfaces a regression, we need a repeatable way to measure it.

**Scope sketch:** either a `cmd/drainctl-perf/` tool that seeds 50 hosts × 5 days + 1 year audit and runs each SC-assertion under `go test -bench` or a standalone binary, OR an `ops/` script that operates against a real installed build. Emit a CHRONICLE paragraph with the numbers. Promote to `specs/007a-perf-validation/` when a real measurement ask arrives.

**Trigger to promote:** production report of slow chart render, large DB, dashboard unresponsiveness under load, or a formal compliance ask for SC numbers.
