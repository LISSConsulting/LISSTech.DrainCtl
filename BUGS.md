# BUGS & Follow-ups

Running list of defects and UX gaps surfaced during validation / manual testing. Not a roadmap — just things that need picking up, with enough context to act on each without re-investigating.

---

## Feature 007 — SQLite telemetry store

### 1. `drainctl register [--auto]` should proxy through the service pipe — FIXED 2026-04-19

**Status:** Fixed.

**Change:**
- `internal/pipe/pipe.go`: added `register` command to the pipe protocol (extends `PipeRequest.URL`, `PipeHandler.HandleRegister`), a 30 s per-connection deadline so the outbound SSPI+HTTPS round-trip has room to complete, and `RegisterViaPipe()` for CLI callers. Introduced a new `ErrPipeUnavailable` sentinel so dial failures (bootstrap case) are distinguishable from errors returned by a working service.
- `internal/svc/handler.go`: `serviceHandler.HandleRegister()` invokes existing `dashboard.Register()` under the service's machine-account identity and marshals `RegisterResult` back over the pipe.
- `cmd/drainctl/register_cmd.go`: `drainctl register` now calls `pipe.RegisterViaPipe()` first and falls back to `dashboard.Register()` **only** when the error is `ErrPipeUnavailable` (service not installed/started). Service-side failures surface as-is so a 401 / TLS mismatch / timeout isn't silently retried under the operator's user token.
- Tests: `TestHandlePipeConn_Register{OK,MissingURL,HandlerError}`, `TestRegisterViaPipe_WrapsDialFailureWithErrPipeUnavailable`, `TestHandleRegister_EmptyURLReturnsError`; compile-time `var _ pipe.PipeHandler = (*serviceHandler)(nil)` guards interface drift.
- `DashboardURL` persistence and auto-pin both stay in the CLI — only the network call moved.

**Verification:** `go test ./...` green; `just lint` 0 issues. Codex review caught the over-broad fallback (falling back on *any* pipe error would double-call under the human token) — narrowed before commit.

Original report below for historical context.

**Surfaced:** 2026-04-18 during T067 validation on MDS-LDC1-RDS12.

**Symptom:** Running `drainctl register --auto` as an interactive user (e.g. `MDS\lissadmin`) returns 401.

**Root cause:** `cmd/drainctl/register_cmd.go:59` calls `dashboard.Register(dashURL)` directly from the CLI process, using the interactive user's Kerberos/NTLM token. The dashboard's register route is gated by `requireMachineAccount` (`internal/dashboard/server.go:263`), which only accepts `COMPUTERNAME$` principals.

**Fix:** The CLI should send a `register` command over the named pipe (`\\.\pipe\drainctl`) and let the service perform the HTTP POST with its own machine-account identity. Keep the direct-HTTP path as a fallback for the "service not yet running" bootstrap case.

**Scope:**
- New pipe command `register` in the pipe protocol; service handler invokes existing `dashboard.Register()` and returns `RegisterResult` over the pipe.
- CLI tries pipe first, falls back to direct HTTP if the pipe is unavailable.
- `DashboardURL` save and TLS fingerprint pinning (`register_cmd.go:47-72`) stay in the CLI — only the network call moves.

---

### 2. Migration log line is misleading when JSONL has no transitions — FIXED 2026-04-18

**Status:** Fixed.

**Symptom:** `INF audit JSONL migration lines=14761 imported=0 skipped=0` — numbers don't add up; looked like silent data loss.

**Root cause:** `internal/telemetry/migrate_jsonl.go:274-316` read every line but only inserted records where `leg.Changed == true` (real drain-mode transitions). Successfully parsed non-transition observations were *neither* imported *nor* skipped — they're the "dropped by design" bucket the new schema no longer stores. The log line reported three counters with no home for this middle bucket.

**Fix:** added `Observations int` to `telemetry.MigrationResult`, incremented on every successfully parsed line where `leg.Changed == false`. Service boot log now emits `observations` alongside `imported` and `skipped`. Invariant: `imported + observations + skipped ≤ lines` (equal in the no-empty-lines common case). Unit test `TestMigrate_ValidJSONLAllImported` gained an `Observations` assertion plus an invariant sum-check.

**Verification:** `go test ./internal/telemetry/ -run TestMigrate` all green. The next JSONL migration that runs will emit `lines=N imported=X observations=Y skipped=Z` where `X+Y+Z=N`; the previously-confusing `imported=0 skipped=0` from CST-ISLAB-PC3 would now read `imported=0 observations=14761 skipped=0` — instantly legible.

---

### 3. Config reload doesn't re-run self-register when URL changes to local — FIXED 2026-04-19

**Status:** Fixed.

**Change:**
- `internal/svc/handler.go`: added an `else if` branch after the `disabled → enabled` self-register block. The new branch fires when `dashState != nil && !dashRegistered && !cfg.DashboardOnly && isLocalDashboard(newDashCfg.URL) && !isLocalDashboard(dashCfg.URL)` — i.e. the URL just changed to name this machine. Calls `dashState.Register(h)` immediately (in-memory, no HTTP round-trip) and sets `dashRegistered = true`. Idempotency is guaranteed by the `!dashRegistered` guard which the URL-change path at line 796 already resets when the URL changes.
- `internal/svc/handler_test.go`: `TestIsLocalDashboard_MatchesHostname` — unit covers case-insensitive hostname match and explicit rejection of "localhost". `TestIsLocalDashboard_URLBecameLocal` — verifies the transition predicate `isLocalDashboard(new) && !isLocalDashboard(old)` holds when old is stale and new is local, confirming the reload branch will fire.

**Verification:** `go test ./internal/svc/ -run TestIsLocalDashboard` green; `go test ./...` all green; `just lint` 0 issues. Codex review: NO ISSUES.

Original report below for historical context.

**Surfaced:** 2026-04-18 during T067 validation on MDS-LDC1-RDS12.

**Symptom:** After fixing a stale discovered URL via config reload, the dashboard's in-memory registry never gets the local host — warnings pile up (`dashboard heartbeat (local): host not registered`) until the service restarts.

**Root cause:** `internal/svc/handler.go:616-631` only fires the hot-reload self-register path when the dashboard transitions `disabled → enabled`. A pure URL change (e.g. SRV discovery returned a stale URL at startup, then config.json is corrected) does not trigger the self-register, even though `isLocalDashboard(newDashCfg.URL)` is now true.

**Fix:** Expand the condition so self-register also fires when `isLocalDashboard(newDashCfg.URL) && !isLocalDashboard(dashCfg.URL)` — i.e. the URL just became local. Keep it idempotent (check `dashRegistered` first).

**Workaround today:** Restart the service after correcting the URL.

---

### 4. `/api/v1/register` 401 gives no hint about machine-account requirement — FIXED 2026-04-19

**Status:** Fixed.

**Change:**
- `internal/dashboard/auth_prod.go`: `requireMachineAccount` now returns 403 with `Content-Type: application/json` and a body of `{"error":"machine account required: this endpoint accepts only COMPUTERNAME$ principals; <username> is a human user account"}` when an authenticated human principal hits the route. The unauthenticated path (auth == nil → 401) is unchanged, preserving the no-info-leak contract. Username is serialised via `json.Marshal` so domain backslashes (e.g. `DOMAIN\alice`) are correctly escaped.
- Note: the original report cited a bare 401; by the time this was fixed the code already returned 403 (correct HTTP semantics for authenticated-but-unauthorised). The body was still plain text.
- Tests: `TestRequireMachineAccount_HumanPrincipalReturnsJSONError` (status 403, Content-Type application/json, body names the principal), `TestRequireMachineAccount_UnauthenticatedReturns401` (unauthenticated path still returns 401 with no JSON body).

**Verification:** `go test ./internal/dashboard/ -run TestRequireMachineAccount` green; `go test ./...` all green; `just lint` 0 issues. Codex caught unescaped backslash in username interpolation (confirmed high-confidence); fixed by switching to `json.Marshal`.

Original report below for historical context.

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

### 7. `change logon /enable` transition lacks attribution — DUPLICATE of #8, FIXED 2026-04-18

**Status:** Duplicate. The "missing attribution" symptom was the same bug as item 8 (attribution reverts from user to `-` on later refresh) surfacing through the CLI instead of the dashboard. Both were caused by `changedBy` being re-initialized to `""` on every non-transition tick in `internal/svc/check.go` and flowing into `CheckResult.ChangedBy`. See item 8 for the root cause and fix.

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

**Verification:** `pnpm run build` in `frontend/` succeeds clean. Visual verification pending operator restart of dashboard with rebuilt bundle.

---

### 11. 5-day per-host chart had no axes, tooltips, or visible zoom affordance — FIXED 2026-04-18

**Status:** Fixed.

**Symptom:** after the black-rectangle bug (item 10) was cleared, the chart correctly rendered the CPU time-series in the accent color but offered no other affordances — no Y-axis labels, no X-axis dates, no hover tooltip, no visible indication that scroll/drag/dbl-click worked. Operators couldn't read exact values at a point or learn how to zoom.

**Root cause:** `frontend/src/lib/chart.svelte` wrapped `<LayerCake>` with only a `<LinePath>` — no `AxisX` / `AxisY` components, no hover layer, no affordance text. The Overview page's LOAD and Health Indicators charts (`DualAxisChart.svelte`) already had the full pattern (Y grid labels, X time labels, crosshair, data dot, tooltip card with shadow) but it had never been ported to the per-host chart.

**Change:**
- New component `frontend/src/components/chart/InteractiveTimeChart.svelte` — single-series analogue of `DualAxisChart`. Renders Y-axis ticks (pinned to 0/25/50/75/100 for percent counters, auto-scaled for the rest), X-axis time ticks (format adapts to window span: H:M:S for <2 h, H:M for <48 h, month+day for longer), faint horizontal gridlines, left+bottom axis borders, hover crosshair + data dot + tooltip card with neobrutalist shadow.
- `frontend/src/lib/chart.svelte` — swapped `<LinePath>` for `<InteractiveTimeChart>`. Passes explicit `xDomain={[viewFrom, viewTo]}` so the chart still shows the visible window even when data is sparse. Increased LayerCake padding to make room for axis labels. Added a small `.chart-hint` at bottom-right: `SCROLL · DRAG · DBL-CLICK`. The subscriber's existing wheel/drag/dblclick handlers on the outer div remain — unchanged. New `hideHover={isDragging}` prop so the tooltip doesn't fight the drag-pan.
- Counter-aware Y-axis formatting: `_pct` → `N%`, `_ms` → `Nms`, `_mb` → `N MB`, `_bytes` → `N MB` (auto-converted), `_sec` → `N/s`, else numeric.

**Verification:** `pnpm run build` clean. Playwright visual check: Y-axis ticks 0-100% rendered, X-axis date ticks APR 13 → APR 18 rendered, hover at 70% of chart width produced a crosshair + dot + tooltip with `"Apr 18, 15:27:51 / cpu_pct: 87%"`, hint text visible.

**Not done (follow-up):** visible +/- zoom buttons. The text hint is sufficient for now; buttons can come when operators ask for them.

---

### 12. Drift reconciliation never ran — T025 was unwired despite `[x]` status — FIXED 2026-04-18

**Status:** Fixed.

**Symptom:** T067 B5 walk — stop service, run `change logon /drain`, start service, grep `drainctl.log` for `reconcil|drift|maintenance_job` → zero matches. A drain-state change made while the service was down was completely invisible: no reconciliation audit row, no `maintenance_jobs` row, no operator-facing log line.

**Root cause:** `specs/007-sqlite-telemetry-store/tasks.md:95` T025 was marked `[x]` (done), and `internal/telemetry/reconcile.go` did contain a fully implemented `Reconcile` function plus unit tests. But `grep -r telemetry.Reconcile` in production code returned zero hits — the wiring from `internal/svc/handler.go` boot → `telemetry.Reconcile(...)` was never added. The reconciler existed on paper but was never called at runtime. A checkbox-level false positive in the task list.

**Fix:** `internal/svc/handler.go` now calls `telemetry.Reconcile` between `NewAuditStore` and the aggregator/retention goroutines. Reads current drain state via `dc.ReadDrainMode()`, builds a `DrainProbe`, invokes `Reconcile`. Registry-read or reconcile errors log `WARN` and the service continues (degraded-mode pattern matching `MigrateJSONL` failure handling). Success emits `drift_reconciliation=complete` at `INFO` including host + observed mode + duration, so operators can verify the job ran regardless of whether a drift row was actually written.

**Verification:** `go test ./internal/svc/ ./internal/telemetry/` green (existing reconcile_test.go already covered the function itself; the gap was the call site). Operator verification: repeat T067 B5 — the log should now show `drift_reconciliation=complete` and the audit table should gain a row with `reconciliation=1` when an offline drain toggle happened.

**Related:** T025's text in tasks.md was accurate about WHERE the call should happen ("from service boot after `telemetry.Open()` AND after `MigrateJSONL` has completed ... and before live ingest starts"); only the implementation was missing.

---

## Feature 009 — Security hardening (post-codex)

### 13. `seedServerMetrics` overwrites fresher SSE samples on cold start

**Status:** Open. Pre-009 behavior; surfaced by 2026-04-24 codex review on the 009 branch.

**Symptom:** on a cold dashboard load, if an SSE `server_update` event arrives between the `fetchAllServerMetrics` request leaving the browser and the seed promise resolving, the live perf sample gets clobbered by the older retained-history seed. One sample lost per host per cold start. Largely invisible to operators — sparkline gets a single missing point that gets backfilled on the next 30 s poll.

**Root cause:** `frontend/src/lib/state.svelte.js:823` (`seedServerMetrics`) does `next.set(host, samples.slice(-MAX_METRICS))` unconditionally. The function name says "seed" but the implementation says "replace." `frontend/src/App.svelte:160` kicks the seed in parallel with `frontend/src/App.svelte:369` opening the SSE EventSource, and SSE's `onmessage` calls `appendPerfToRingBuffer` which mutates the same Map.

**Fix sketch:** in `seedServerMetrics`, only set the host's ring buffer if the existing entry is empty OR the seed sample's max timestamp is newer than the buffer's max timestamp. Pre-008 the helper was conditional ("only if not yet seeded"); the unconditional overwrite was introduced when the seed path moved from one-shot bootstrap to "always overwrites stale browser-local."

**Why deferred from 009:** semantically pre-existing (predates US5's helper extraction). Codex found it because the prompt pointed at state.svelte.js, not because 009 introduced it. Out-of-scope for the 009 cleanup batch.

---

### 14. Stale `detectorStatuses` / `recentSpikes` after ServerTable delete + re-register

**Status:** Open. Pre-009; surfaced by 2026-04-24 codex review.

**Symptom:** delete a server in the dashboard, re-register the same hostname later, and the Server Detail panel shows the previous host's evtspike detector state until the next SSE `detector_status` transition arrives. `recentSpikes` map carries forward similarly. UI looks broken until something triggers an update.

**Root cause:** `frontend/src/components/ServerTable.svelte:185` (`doRemoveServer`) calls `removeServerMetrics(host)` to clear the perf ring buffer but does not clear `detectorStatuses` or `recentSpikes`. The cold-start seeding effect at `:56-65` skips the REST seed (`fetchEvtSpikeStatus`) when `appState.detectorStatuses.has(host)` is true — so a re-registered hostname sees the old map entry, declines to fetch fresh state, and shows stale until SSE happens to update.

**Fix sketch:** add `removeEvtSpikeState(host)` and `removeRecentSpikes(host)` calls alongside `removeServerMetrics(host)` in `doRemoveServer`. The `removeEvtSpikeState` helper already exists in `state.svelte.js:801` (used by `App.svelte:426` on SSE `server_deleted`); just call it from the local-delete path too.

**Why deferred from 009:** pre-009; codex flagged because the prompt pointed at ServerTable.svelte but the issue isn't a 009 regression. Out-of-scope for the 009 cleanup batch.
