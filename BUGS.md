# BUGS & Follow-ups

Running list of defects and UX gaps surfaced during validation / manual testing. Not a roadmap — just things that need picking up, with enough context to act on each without re-investigating.

---

## Feature 007 — SQLite telemetry store

### 1. `drainctl register [--auto]` should proxy through the service pipe

**Surfaced:** 2026-04-18 during T067 validation on MDS-LDC1-RDS12.

**Symptom:** Running `drainctl register --auto` as an interactive user (e.g. `MDS\lissadmin`) returns 401.

**Root cause:** `cmd/drainctl/register_cmd.go:59` calls `dashboard.Register(dashURL)` directly from the CLI process, using the interactive user's Kerberos/NTLM token. The dashboard's register route is gated by `requireMachineAccount` (`internal/dashboard/server.go:263`), which only accepts `COMPUTERNAME$` principals.

**Fix:** The CLI should send a `register` command over the named pipe (`\\.\pipe\drainctl`) and let the service perform the HTTP POST with its own machine-account identity. Keep the direct-HTTP path as a fallback for the "service not yet running" bootstrap case.

**Scope:**
- New pipe command `register` in the pipe protocol; service handler invokes existing `dashboard.Register()` and returns `RegisterResult` over the pipe.
- CLI tries pipe first, falls back to direct HTTP if the pipe is unavailable.
- `DashboardURL` save and TLS fingerprint pinning (`register_cmd.go:47-72`) stay in the CLI — only the network call moves.

---

### 2. Migration log line is misleading when JSONL has no transitions

**Surfaced:** 2026-04-18 during T067 validation on CST-ISLAB-PC3.

**Symptom:** `INF audit JSONL migration lines=14761 imported=0 skipped=0` — numbers don't add up; looks like silent data loss.

**Root cause:** `internal/telemetry/migrate_jsonl.go:274-316` reads every line but only inserts records where `leg.Changed == true` (i.e. real drain-mode transitions). Non-transition observations are *neither* imported *nor* skipped — they're silently filtered. The log line reports all three counters but never accounts for the filtered-out middle bucket.

**Fix:** Either add an `observations=N` field to the log, or rename `lines` → `transition_candidates` and make the math explicit. Behaviour is correct; only the log wording is wrong.

---

### 3. Config reload doesn't re-run self-register when URL changes to local

**Surfaced:** 2026-04-18 during T067 validation on MDS-LDC1-RDS12.

**Symptom:** After fixing a stale discovered URL via config reload, the dashboard's in-memory registry never gets the local host — warnings pile up (`dashboard heartbeat (local): host not registered`) until the service restarts.

**Root cause:** `internal/svc/handler.go:616-631` only fires the hot-reload self-register path when the dashboard transitions `disabled → enabled`. A pure URL change (e.g. SRV discovery returned a stale URL at startup, then config.json is corrected) does not trigger the self-register, even though `isLocalDashboard(newDashCfg.URL)` is now true.

**Fix:** Expand the condition so self-register also fires when `isLocalDashboard(newDashCfg.URL) && !isLocalDashboard(dashCfg.URL)` — i.e. the URL just became local. Keep it idempotent (check `dashRegistered` first).

**Workaround today:** Restart the service after correcting the URL.

---

### 4. `/api/v1/register` 401 gives no hint about machine-account requirement

**Surfaced:** 2026-04-18 during T067 validation on MDS-LDC1-RDS12.

**Symptom:** Human users calling the register endpoint get a bare 401 with no body — confusing; looks like NTLM is broken.

**Root cause:** `requireMachineAccount` middleware rejects non-machine principals with a generic 401.

**Fix:** When the middleware rejects a human principal (authenticated but not a `$`-suffixed account), return 401 with a JSON body like `{"error": "register endpoint requires a machine account; run the CLI as SYSTEM or via the service"}`. Keep the 401 for unauthenticated requests unchanged so we don't leak information to unauthed callers.

**Note:** Superseded in practice by item 1 (pipe proxying); once the CLI routes through the service, humans won't hit this path. Still worth fixing for direct-HTTP callers and custom integrations.

---

### 5. `drainctl history` drops CPU% and INPUT DLY columns (always `-`) — FIXED 2026-04-18

**Status:** Fixed. Chose option (C) below — join at read time.

**Change:** `internal/telemetry/metrics.go` gained `MetricsStore.NearestCounters(ctx, host, target, toleranceMs, counters)` which returns the metrics_raw sample closest to each transition's timestamp per requested counter, within a ±2-minute window. `serviceHandler` in `internal/svc/handler.go` now carries a `*telemetry.MetricsStore` and `HandleHistory` calls `NearestCounters` per audit row to populate CPU / input-delay / session fields before returning. Tests: `TestNearestCounters_*` (telemetry), `TestHandleHistory_EnrichesFromMetrics` + `TestHandleHistory_NilMetricsStillReturnsAudit` (svc). Lint clean.

Original report below for historical context.

**Surfaced:** 2026-04-18 during T067 validation on MDS-LDC1-RDS12.

**Symptom:** Every row of `drainctl history` shows `-` in the `CPU%` and `INPUT DLY` columns, even for transitions that happened after performance counters were up.

**Root cause:** The new audit table schema (`internal/telemetry/migrate_jsonl.go:222-226`) is `(ts, host, prev_state, new_state, principal, changed_by, reason, key_modified_ts, reconciliation, before_ts)` — it does not carry `cpu_pct`, `input_delay_max_ms`, or the other perf fields the legacy JSONL stored inline on each `AuditRecord`. The CLI history formatter still prints those columns, but the data is never populated, so it always renders as `-`.

**This is an FR-026 ("no operator-visible regression") violation** — the column shape is unchanged, but column data that used to populate is now always empty.

**Fix options:**
- **(A) Drop the columns.** Simplest, but operators lose a diagnostic they had before.
- **(B) Add the fields to the audit table.** Requires a schema migration and duplicates data already stored in `metrics_raw`.
- **(C) Join at read time.** For each transition row, look up the nearest `metrics_raw` sample (same host, closest `ts`) and inline the perf fields into the CLI formatter's rows. No schema change; data stays normalized. **Preferred.**

---

### 6. `fDenyTSConnections` transitions not tracked — FIXED 2026-04-18

**Status:** Fixed for registry-side deny via `fDenyTSConnections`.

The `fDenyTSConnections` registry value is now tracked. Operators can still flip drain state via GPO, "Allow users to connect remotely" in System Properties, or direct regedit — all of which write `fDenyTSConnections` — and DrainCtl now emits a `DENY_ALL_CONNECTIONS` transition for each.

**Change:**
- `registry.go`: added `DenyValueName = "fDenyTSConnections"` + synthetic `DenyAll DrainMode = 3` + `"DENY_ALL_CONNECTIONS"` label. `ReadDrainMode` now reads `fDenyTSConnections` and returns `DenyAll` when it is 1, regardless of the underlying `TSServerDrainMode` value. The drain enum still takes values 0/1/2 when fDeny is 0 or absent; the synthetic 3 does not collide.
- `frontend/src/lib/utils.js`: new `modeLabel` mapping `DENY_ALL_CONNECTIONS → "Denied"`.
- `audit_setup.go`: success log updated to mention both values (the existing SACL already covers any value-write under the Terminal Server key, so Event 4657 attribution works for fDenyTSConnections with no SACL change).
- Transition detection, `ClassifyState`, the registry key watcher, and notification triggers compose on `DrainMode` equality or `!= AllowAll` — no code changes needed for those paths.

Tests: `TestDrainMode_String` in `registry_test.go`. `go test ./...` green, `just lint` clean.

---

### 6a. `change logon /disable` explicitly out of scope

**Status:** Won't fix — scoped out 2026-04-18 after a design review (scrapped).

**Why it's not covered by #6's fix:** empirical testing on Windows Server 2022 showed that `change logon /disable` does NOT modify `fDenyTSConnections` or any other registry value we could watch. It flips an in-memory flag in the Terminal Services LSM (lsm.exe), readable only via:
- The undocumented `winsta.dll!WinStationQueryInformationW` API, or
- `Win32_TerminalServiceSetting.Logons` via WMI (`root\cimv2\TerminalServices`).

**Why we're not tracking it:** the implementation path was costed via a three-round adversarial design review. The WMI subscription approach requires raw COM in-process (DrainCtl has no COM today), per-thread `CoInitializeEx` lifecycle, `CoInitializeSecurity` + `CoSetProxyBlanket` for the `root\cimv2\terminalservices` namespace, a custom `IWbemObjectSink` implementation, graceful degradation when `winmgmt` is unreachable, and a 4688-process-creation heuristic for attribution (with spoofability concerns). The cost-benefit ratio wasn't justified for what is a transient, in-memory, non-persistent toggle.

**What operators should do instead:** use `Set-RDSessionHost -NewConnectionAllowed {Yes|No|NotUntilReboot}` (verified 2026-04-18 to write `TSServerDrainMode` and thus to be captured by DrainCtl). Or flip `fDenyTSConnections` via GPO / System Properties for a persistent deny. Both produce audit rows with attribution via Event 4657.

**Revisit if:** `change logon /disable` usage shows up as an operational gap in production (e.g. security audit asks "why isn't this tracked?"). At that point the path is a well-scoped follow-up: direct `WinStationQueryInformationW` syscall (matches the existing perfmon/watcher pattern; no COM; undocumented but stable enough for `change.exe` itself to use).

---

### 7. `change logon /enable` transition lacks attribution (user-reported)

**Surfaced:** 2026-04-18 during T067 validation on MDS-LDC1-RDS12 — user-reported; not directly visible in the paste (later `/enable` rows in the same session *did* attribute `MDS\lissadmin`, so the failure may be intermittent or tied to a specific earlier run).

**Symptom:** An `/enable` transition appears in `drainctl history` with `changed_by = -` instead of the principal who invoked the command.

**Candidate root causes:**
- Event 4657 (registry SACL) records the setter for the registry value write, but `change logon /enable` may perform two sequential writes (clear `fDenyTSConnections` + possibly reset `TSServerDrainMode`), and if the audit correlator picks the wrong event the attribution can be lost.
- Audit SACL was set on the key, not each value; if the 4657 event arrives late, the transition row may already be written with empty `changed_by`.

**Next step:** reproduce with Security log open (Event ID 4657) and match timestamps against the audit row. Worth tracing the end-to-end path in `internal/telemetry/reconcile.go` or wherever transitions are correlated with 4657 events.

---

### 8. Attribution reverts from user to `-` on later refresh — FIXED 2026-04-18

**Status:** Fixed. Reproduced on the dashboard (two screenshots taken seconds apart — the same "Grace" row for MDS-LDC1-RDS12 showed `Changed By = MDS\lissadmin` in the first and `-` in the second, with State Since unchanged). The CLI report was the same bug surfacing through a different reader.

**Root cause:** `internal/svc/check.go` only populated `changedBy` *inside* the transition branch (line 34 initializes to `""`, line 38-60 overwrites it only when `prev.DrainMode != state.Mode`). On every subsequent non-transition tick, `changedBy` stayed `""` and flowed into `CheckResult.ChangedBy` at line 208, which the agent sent to the dashboard via `/api/v1/report`. `toServerView` in `internal/dashboard/server.go:108` unconditionally copied that into `ServerView.ChangedBy`, so the frontend saw the principal get reset on every poll tick. The SQLite audit row was never mutated — the bug was purely in the agent's in-tick view, not in persisted state.

**Change:**
- `internal/svc/handler.go`: `observation` struct gained a `ChangedBy` field. Startup seed from `LatestByHost` now populates it.
- `internal/svc/check.go`: after the transition block, non-transition ticks inherit `changedBy = prev.ChangedBy`. The new observation stored each tick carries `ChangedBy` forward.

Semantic: "ChangedBy" now means "the principal who set the current state", not "the principal who acted on this tick". Lint clean, `go test ./...` green.

Original report below for historical context.

---

### 9. `TSServerDrainMode` labels for values 1 and 2 were swapped — FIXED 2026-04-18

**Status:** Fixed. Pre-existing bug, surfaced during T067 when the user tested `Set-RDSessionHost -NewConnectionAllowed No` / `NotUntilReboot` and the registry values disagreed with drainctl's labels.

**Symptom:** Every `change logon /drain` (persistent — writes value 2) displayed as `ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS_UNTIL_RESTART` ("Drain (Restart)" in the dashboard). `change logon /drainuntilrestart` (temporary — writes value 1) displayed as `ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS` ("Drain"). Semantic meanings were flipped for every drainctl render since day one.

**Root cause:** `registry.go` mapped the constants inside-out relative to Microsoft's documented `TSServerDrainMode` semantics:
- Registry value 1 = prevent new logons *until reboot* (temporary). drainctl called it `PreventNewLogon` with label `ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS` (no "_UNTIL_RESTART" suffix) — suggesting persistence.
- Registry value 2 = prevent new logons *until the drain is manually cleared* (persistent). drainctl called it `PreventUntilRST` with label `ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS_UNTIL_RESTART` — suggesting temporary.

**Functional impact:** Zero. Every downstream consumer (`ClassifyState`, notification triggers, transition detection, drain-active checks) composes on `state.Mode != AllowAll`, not on the specific label. The audit table stored the correct integer, the registry was read correctly; only the *rendered string* was wrong.

**Change:**
- `registry.go`: renamed constants to match Windows semantics — `DrainUntilBoot DrainMode = 1` and `DrainPersistent DrainMode = 2`. Label strings swapped: value 1 now renders with `_UNTIL_RESTART`, value 2 without. Kept `PreventNewLogon` and `PreventUntilRST` as `Deprecated:` aliases (same integer values, compile clean for external DLL / PowerShell-module callers; emits `SA1019` in new internal code).
- `registry_test.go`: test cases updated to the corrected pairing, plus an explicit alias-compat block.
- `internal/svc/handler_test.go`: migrated internal test fixtures to `dc.DrainPersistent` (what `change logon /drain` actually writes).

**Data migration:** None needed. Existing audit rows with `new_state = 2` will retroactively render with the correct (persistent) label on the next `drainctl history` invocation — the change is label-only; the stored int was always right.

**Deploy note:** operators who relied on the old label text in log-scraping scripts or downstream dashboards will see the strings swap. The short frontend labels in `frontend/src/lib/utils.js` continue to map the same strings; they now correctly associate "Drain (Restart)" with the temporary (value 1) state instead of the persistent one.

Tests: `TestDrainMode_String` (including deprecated-alias compat cases), `go test ./...` green, `just lint` clean.

---

### 10. 5-day chart renders as solid black rectangle — FIXED 2026-04-18

**Status:** Fixed.

**Symptom:** the per-server `5-DAY CPU HISTORY` chart on the dashboard renders as a solid black rectangle with no visible line, no axis labels, no zoom/pan interactivity. The `DATA BEYOND THIS RANGE IS NOT RETAINED` badge and `CPU_PCT TIER=HOURLY` meta label show correctly on top of the black area — so the chart component *did* run and *did* receive data, but the drawn content was invisible.

**Root cause:** `frontend/src/components/chart/LinePath.svelte:18` built the fill color by string-concatenating a hex alpha suffix onto the caller-supplied color: `fill="{color}33"`. This works when the caller passes a hex literal (e.g. `#fd6cbb` becomes `#fd6cbb33` — valid 8-digit hex with ~20% alpha), but `chart.svelte:45` passes the default `color = 'var(--color-accent)'`. Concatenation produced the attribute string `var(--color-accent)33`, which is NOT a valid CSS color — `var()` invocations cannot have a suffix. Browsers fall back to **black** for unparseable `fill` attributes. At the 5-day zoom, the filled polygon extends under every sample point and effectively covers the chart area.

**Change:** decouple alpha from color using the SVG `fill-opacity` attribute. `fill={color} fill-opacity="0.2"` is equivalent visually to the old `#NNN33` (≈0.2 alpha) and works for both hex literals AND `var()` references. One-line fix in `LinePath.svelte`.

**Verification:** `npm run build` in `frontend/` succeeds clean. Visual verification pending operator restart of dashboard with rebuilt bundle.
