# Codex review synthesis — WMI subscription design for `change logon /disable` tracking

**Date:** 2026-04-18
**Scope:** Design proposal (no code written yet) for tracking `Win32_TerminalServiceSetting.Logons` in DrainCtl via WMI event subscription.
**Lenses:** (A) correctness & lifecycle, (B) Windows/WMI specifics + attribution, (C) design & Go idioms.
**Verification:** each codex claim cross-checked against the actual codebase and public docs. Claims marked REJECTED / PARTIAL / CONFIRMED below.

---

## CONFIRMED issues — must be addressed

### C1. COM initialization is per-thread, not per-process — **BLOCKER**
**Sources:** Lens A #1, #3. Microsoft COM docs.

The proposal's "CoInitializeEx once per process" is wrong. COM init state is tracked per OS thread. Go runtime migrates goroutines across OS threads. A subscriber goroutine making WMI calls can land on an uninitialized thread → `CO_E_NOTINITIALIZED`.

**Correct shape:** subscriber goroutine calls `runtime.LockOSThread()` at entry, then `ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED)` on that locked thread, performs all WMI work there, explicitly `Release()`s every COM interface, then `ole.CoUninitialize()` on the same thread before `runtime.UnlockOSThread()` / goroutine exit.

### C2. `ctx.Done()` is insufficient COM teardown — **BLOCKER**
**Sources:** Lens A #3.

The proposal relies on ctx cancellation to unwind COM. It will not: context cancellation does not unblock a waiting `IEnumWbemClassObject::Next` or release COM interfaces. Subscriber can hang on stop, leak handles on exit.

**Correct shape:** use finite-timeout waits around event retrieval (≤1 s), check `ctx.Done()` between waits, and on exit path explicitly `Release()` every stored COM pointer (sink, enumerator, services, locator) on the same locked OS thread that initialized COM.

### C3. Missing `CoInitializeSecurity` and `CoSetProxyBlanket` — **BLOCKER**
**Sources:** Lens A #5, Lens B #2, #8. MSDN `Win32_TerminalServiceSetting` page.

The proposal omits COM security initialization. For `root/cimv2/terminalservices` specifically, Microsoft documentation mandates `RPC_C_AUTHN_LEVEL_PKT_PRIVACY` on the WMI service proxy. Without it, queries will fail with opaque access-denied on hardened hosts.

**Correct shape:**
1. `CoInitializeSecurity` once per process (after CoInitializeEx on a thread, before any WMI call). Check for `RPC_E_TOO_LATE` — not fatal, fall through.
2. After obtaining the `IWbemServices` proxy via `ConnectServer`, call `CoSetProxyBlanket` setting `RPC_C_AUTHN_LEVEL_PKT_PRIVACY`, `RPC_C_IMP_LEVEL_IMPERSONATE`.

### C4. `Logons` is a WMI **string** property, not uint32 — **HIGH**
**Sources:** Lens B #3 (codex verified empirically via PowerShell on the review machine).

`Win32_TerminalServiceSetting.Logons` is declared as `CIM_STRING` in the MOF. Values are `"0"` (enabled) or `"1"` (disabled). The proposal implicitly assumed integer comparison.

**Correct shape:** read as string, compare `== "1"` for disabled. Treat empty string (observed on client Windows) as "property not applicable — host is not RDSH".

### C5. `__InstanceModificationEvent WITHIN 2` is **polling**, not push delivery — **HIGH**
**Sources:** Lens A #6, #9, Lens B #5.

`WITHIN 2` instructs WMI to poll the target class every ~2 seconds. Rapid flip/flops (`/disable` then `/enable` in <2 s) can deliver zero, one, or two events depending on when WMI sampled. There is no extrinsic (push) event class for `Win32_TerminalServiceSetting`.

**Correctness criterion to accept:** eventual convergence to the current effective state. Emit transitions only when recomputed effective mode differs from last emitted. Do not promise historical edge capture.

### C6. Winmgmt can be disabled by policy — "graceful degradation" silently assuming allowed is too weak — **HIGH**
**Sources:** Lens B #1, Lens C #8.

If winmgmt is stopped/disabled/broken, the subscription never fires. The proposal's "skip Logons tracking on init failure" silently composes effective mode from the other two sources, which can report `AllowAll` while LSM is actually denying logons.

**Correct shape:**
- Track WMI source health as a first-class field (`LogonsSource: "live" | "stale:<reason>" | "unavailable"`).
- Surface this in the CheckResult / dashboard so operators see "Logons state unknown" rather than a confident-but-wrong mode.
- If winmgmt is down at startup and no registry signal forces DenyAll, effective mode must NOT silently claim AllowAll.

### C7. Subscription death not handled — **HIGH**
**Sources:** Lens A #10, Lens B #6, Lens C (fallback strategy).

The proposal considers fallback A/B/C but does not specify subscription-loss detection. WMI provider unload, winmgmt restart, RPC failure — all can silently stop event delivery.

**Correct shape:** option B+C combined.
- On event-delivery error or N-seconds-without-any-event heartbeat: log, `Release` current COM objects, reconnect with exponential backoff (1 s → 60 s max), on reconnect do a fresh read.
- **In addition**, periodic reconciliation poll (suggested 60–300 s) that calls `IWbemServices::ExecQuery` for `SELECT Logons FROM Win32_TerminalServiceSetting` and compares to last-known. Catches missed edges from `WITHIN 2` polling gaps AND dead subscriptions.

### C8. Race between WMI and registry watchers — **MEDIUM → actionable**
**Sources:** Lens A #7. Verified: `internal/watcher/registry.go:49,103` uses `chan struct{}` buffer=1 + non-blocking send. Existing pattern coalesces only the *signal*, not the downstream `ReadDrainMode` work.

If an operator flips `fDenyTSConnections=1` and runs `change logon /disable` in quick succession, both watchers nudge, service loop recomputes twice. Depending on ordering, the service may emit two transitions when the effective state changed once, or coalesce two genuinely distinct transitions (`Allow → DrainUntilBoot → DenyAll`) into one (`Allow → DenyAll`).

**Correct shape:** emission decision based on recomputed effective state, not watcher event count. Only append an audit row when `newEffectiveMode != lastEmittedEffectiveMode`. Already how `svcRunCheck` works in `check.go` — just need to make sure it stays that way after the new channel joins the select.

### C9. Don't collapse source state into one `DrainMode` — **HIGH**
**Sources:** Lens A #8, Lens C #7.

The proposal's "fDenyTSConnections=1 OR Logons=1 → DenyAll, same display label" loses source identity. If an operator sees "DenyAll" and wants to know *why* (which mechanism) or *what to flip to clear it*, the audit trail can't tell them.

**Correct shape:** internal model carries three sources. `DrainMode` stays as the display-layer enum (already used by audit/dashboard schema). Add a `reason` / `source` string field to the audit row and CheckResult: `"registry: fDenyTSConnections=1"`, `"wmi: Logons=1"`, `"registry: TSServerDrainMode=2"`. Frontend doesn't need to change label — just gains the extra detail in the details pane when operators drill in.

### C10. Attribution heuristic via Security 4688 — **LOW (opt-in)**
**Sources:** Lens B #4.

No Event 4657 equivalent for the Logons flip (not a registry write, no SACL applies). Best heuristic: Security 4688 "Process Creation" with command-line auditing enabled — watch for `change.exe` invocations with `logon /disable` / `/enable` in the command line.

**Correct shape:** optional correlation hook in `EventSubscriber`. Document the required GPO (Audit Process Creation: Success + Administrative Templates > Include command line in process creation events). If unavailable, `changed_by=""` on Logons transitions — same pattern as reconciliation rows today.

### C11. Library choice — prefer `github.com/microsoft/wmi/pkg/base/event` over raw `go-ole` — **MEDIUM**
**Sources:** Lens C #1. Verified via pkg.go.dev: `RegisterWmiCallback(context *CallbackContext, wmiNamespace, hostName, queryString string) (*wmi.WmiEventSink, error)` exists and accepts arbitrary WMI event queries.

The proposal chose `go-ole` as a compromise between "zero-COM raw syscalls" (400 lines, ref-counting hazards) and "new deps". Codex recommends a higher-level Microsoft-maintained alternative. `microsoft/wmi/pkg/base/event` wraps the subscription mechanics (CoInit, proxy blanket, sink lifecycle) behind a callback API, cutting our glue to ~30–50 lines.

**Correct shape:** use `microsoft/wmi/pkg/base/event.RegisterWmiCallback` as the primary path. Fall back to raw go-ole only if the Microsoft package can't express this specific query (unlikely — it accepts any WQL string).

### C12. Code organization — don't put WMI in `registry.go` — **MEDIUM**
**Sources:** Lens C #5.

Mixing WMI into `registry.go` makes the file name lie.

**Correct shape:**
- `registry.go` stays registry-only. `ReadDrainMode` becomes a thin wrapper that delegates.
- New `tsstate_windows.go` at root — owns the aggregator type (`DrainSources`) and `EffectiveMode()`. `ReadDrainMode()` becomes a compatibility shim that calls the aggregator and returns the legacy `RegistryState` for external DLL/PS callers.
- New `internal/watcher/logons_subscriber.go` — WMI subscription only. Exports channel/callback just like the other watchers.

---

## CONFIRMED pre-existing bug (found during verification, not in proposal)

### C13. `EventSubscriber` at `internal/watcher/evtsubscribe.go:246` filters 4657 events by `ObjectValueName == "TSServerDrainMode"` only
Earlier in this conversation `registry.go` gained `fDenyTSConnections` tracking. Attribution for `fDenyTSConnections` transitions is broken today because the 4657 filter excludes that value name. Easy fix: accept either name.

Worth bundling into this work since we're already touching the attribution path.

---

## PARTIAL issues — note and acknowledge, don't necessarily fix now

### P1. `WinStationQueryInformationW` alternative to WMI entirely
**Lens C #3.** Would match existing syscall style (perfmon, watcher). Undocumented; stability across Windows Server 2016 → 2025 not guaranteed. The user already agreed on the WMI path; keep WinStation as a follow-up option in BUGS.md if WMI proves too fragile in production.

### P2. Startup-failure ordering
**Lens A #4.** If subscriber starts but later service init fails, parent ctx must cancel before returning. Existing code (perfmon init, evtSub init) already follows this pattern via the service's parent ctx — the new subscriber must wire into the same ctx.

---

## REJECTED claims

None. Every codex concern either landed or was reclassified as PARTIAL for follow-up. Codex was disciplined this round — no obvious hallucinations on library APIs, event IDs, or Windows facts.

---

## Confirmed from verification, worth calling out

- Current `chan struct{}` nudge pattern (`internal/watcher/registry.go:49,103`) is idiomatic — new WMI subscriber should match exactly: buffer=1 channel, non-blocking send, one `<-ch` case added to service-loop `select`.
- Existing `EventSubscriber.run()` pattern (`internal/watcher/evtsubscribe.go:183-228`) bridges `ctx.Done()` to a Windows event via a goroutine + `WaitForMultipleObjects`. The WMI subscriber can re-use this shape on its locked OS thread for the "wait-for-event-or-cancel" loop.
- `microsoft/wmi/pkg/base/event.RegisterWmiCallback` confirmed via pkg.go.dev.

---

## Severity summary

- **Blockers** (must fix before implementation): C1, C2, C3.
- **High** (visible failures without fix): C4, C5, C6, C7, C9.
- **Medium**: C8, C11, C12, C13.
- **Low** (opt-in / follow-up): C10, P1, P2.

Nothing rejected outright. Ready to hand to Plan.
