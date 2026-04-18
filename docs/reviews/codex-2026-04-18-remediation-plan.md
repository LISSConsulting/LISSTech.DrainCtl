# Remediation plan — WMI tracking of `Win32_TerminalServiceSetting.Logons`

**Scope:** every CONFIRMED issue C1–C13 plus brief notes on PARTIAL P1/P2 from `docs/reviews/codex-2026-04-18-synthesis.md`.
**Library choice:** `github.com/microsoft/wmi/pkg/base/event` (primary), `github.com/go-ole/go-ole` fallback only if query can't be expressed.

Ordering note: Steps 1–3 are skeleton + COM lifecycle (the blockers C1/C2/C3 are all embedded here because they must be correct from the first commit that imports a WMI call — shipping an "incorrect skeleton first, fix later" version would crash hosts). From step 4 onward each step is independently reviewable.

---

## Phase 1 — Library + skeleton

### Step 1. Add WMI dependency (pinned) and validate across OS matrix
- **Files touched:**
  - `go.mod`
  - `go.sum`
- **Change:** Run `go get github.com/microsoft/wmi/pkg/base/event@<pinned-version>` (pick a specific release tag — do NOT use `@latest`, which makes the plan non-reproducible). Record the chosen version in the commit message and a `// require` comment. Transitive `github.com/go-ole/go-ole` arrives as an indirect dep.

  **Scratch validation across OS matrix** (not a 5-minute drive-by — must cover the supported server versions): build a minimal scratch program in a branch-side directory that registers the query `SELECT * FROM __InstanceModificationEvent WITHIN 2 WHERE TargetInstance ISA 'Win32_TerminalServiceSetting'` and prints events. Run it on at least: Windows Server 2016 RDSH (domain-joined), Windows Server 2019 RDSH, Windows Server 2022 RDSH. Exercise `change logon /disable`, `/enable`, `/drain`, `/drainuntilrestart` on each. Also verify behavior with RDSH role absent (non-RDSH server) and with `winmgmt` stopped. If the query doesn't behave identically across all three OS versions, flag before proceeding.

  No code in the tree imports the new package yet.
- **Verification:** `just lint` passes. `go mod tidy` produces a clean diff pinning to the exact version. Manual matrix verification above passes.
- **Effort:** M (the OS matrix exercise adds real time — not S)
- **Covers:** C11 (library choice)
- **Round-1 addresses:** concerns #28 (`@latest` not reproducible), #29 (scratch validation too narrow)

### Step 2. Create `LogonsSnapshot` + `LogonsSource` in root package, then skeleton subscriber
- **Files touched:**
  - `logons.go` (new, root package — hosts the shared types)
  - `internal/watcher/logons_subscriber.go` (new)
- **Change:** Types in the root package (cannot live in `internal/watcher` because the root-package `tsstate_windows.go` aggregator in Step 11 will reference them and internal-package-to-root would be a cycle):

  ```go
  // LogonsSnapshot is the observed state of the LSM Logons flag.
  type LogonsSnapshot struct {
      Value         string    // WMI string value as read ("", "0", "1")
      Disabled      bool      // true iff Value == "1"
      ValuePresent  bool      // false iff Value == "" (non-RDSH host, or property absent)
      Health        LogonsHealth // independent of value — see below
      LastUpdated   time.Time // wall-clock of last successful read (subscription OR poll)
      LastSubEvent  time.Time // wall-clock of last successful subscription callback
      LastPollOK    time.Time // wall-clock of last successful reconcile poll
  }

  // LogonsHealth is derived from LastSubEvent and LastPollOK vs deathThreshold,
  // not from "did an event fire recently" — a quiet system with a working
  // subscription and a working poll is still Live even if Value never changed.
  type LogonsHealth int
  const (
      LogonsHealthUnavailable LogonsHealth = iota // never read successfully
      LogonsHealthLive                            // subscription OR poll proven OK in last deathThreshold
      LogonsHealthStale                           // both subscription and poll silent for > deathThreshold; last-known value returned
  )

  // LogonsSource is the interface the service-side aggregator consumes.
  // Concrete impls: *watcher.LogonsSubscriber in production, a fake in tests.
  // A nil LogonsSource is tolerated (treated as Unavailable) so CLI-direct
  // callers and the service's degraded path don't break.
  type LogonsSource interface {
      Snapshot() LogonsSnapshot
  }
  ```

  `internal/watcher/logons_subscriber.go` defines `LogonsSubscriber` implementing `drainctl.LogonsSource`: constructor `NewLogonsSubscriber(ctx context.Context, opts Options) (*LogonsSubscriber, error)`, method `Snapshot() drainctl.LogonsSnapshot`, method `Changes() <-chan struct{}` (buffer=1, non-blocking send — mirrors `internal/watcher/registry.go:49,103`). Constructor starts the worker goroutine but the worker idles — no WMI calls yet. Concurrency: `sync.RWMutex` guards the cached `LogonsSnapshot`.

  `LogonsSnapshot.LastUpdated` semantics are explicit: updated ONLY on successful read (callback OR poll). Unchanged on parse failure, RPC error, or empty response. `LastSubEvent` and `LastPollOK` track each path independently so Round-1 concern #10 (poll success shouldn't mask subscription death) is structurally impossible.
- **Verification:** `go build ./...` green. Unit test `TestLogonsSubscriber_Skeleton_StopsOnCtx` — construct, cancel ctx, assert channel closes within 2 s. `TestLogonsSnapshot_LastUpdatedOnlyAdvancesOnRead` — feed a sequence of (success, failure, success) calls into an exported test helper and assert `LastUpdated` only advances on successes.
- **Effort:** S
- **Covers:** C12 (code organization)
- **Round-1 addresses:** concerns #10 (split sub vs poll health), #17 (package cycle), #18 (interface for tests), dropped-items LastUpdated semantics

---

## Phase 2 — COM lifecycle correctness (BLOCKERS)

### Step 3. Lock OS thread + explicit CoInitializeEx / CoUninitialize (with correct refcount tracking)
- **Files touched:**
  - `internal/watcher/logons_subscriber.go`
- **Change:** The worker goroutine calls `runtime.LockOSThread()` at entry; deferred `runtime.UnlockOSThread()` at exit. Inside the locked thread:

  ```go
  hr := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED)
  switch {
  case hr == nil || hr == ole.S_FALSE:
      // S_OK or S_FALSE both increment the per-thread init count; we must
      // call CoUninitialize exactly once when we exit.
      comInitialized = true
  case errors.Is(hr, ole.RPC_E_CHANGED_MODE):
      // Another component on THIS thread is already in a different apartment.
      // Since we just locked this thread from a fresh goroutine, this should
      // not happen in practice — if it does, our threading assumption has
      // broken. Fail hard: return an error, run the degraded-mode path.
      return fmt.Errorf("wmi_logons: unexpected RPC_E_CHANGED_MODE on locked thread: %w", hr)
  default:
      return fmt.Errorf("wmi_logons: CoInitializeEx: %w", hr)
  }
  defer func() {
      if comInitialized {
          ole.CoUninitialize()
      }
      // If initialization failed, DO NOT call CoUninitialize — it would
      // unbalance any prior init on the thread (shouldn't be any, but the
      // rule keeps us honest under future refactors).
  }()
  ```

  Explicitly do NOT defer any COM-object `Release()` calls generically — they get released in reverse of acquisition order inside the run loop (see steps 5/6). The subscriber exit path is synchronous: context-cancel → release owned COM pointers → return from the worker → deferred CoUninitialize runs → deferred UnlockOSThread runs.

  On RPC_E_CHANGED_MODE: the subscriber's constructor returns an error. `NewLogonsSubscriber` surfaces this; the service falls through to the degraded path where `LogonsSource` is nil and `DrainSources.Logons.Health == Unavailable`. The service keeps running.
- **Verification:** Unit test `TestLogonsSubscriber_CoUninitializeOnlyWhenInitialized` — inject a fake `CoInitializeEx` that returns `RPC_E_CHANGED_MODE` and a release-counter fake for `CoUninitialize`; assert the counter increments 0 times, not 1. Second unit test injects `S_OK` and asserts the counter increments exactly once. `go test ./internal/watcher/...` green. Manual: run service under WinDbg, confirm subscriber goroutine stays on one OS thread across ticks. Integration test from Step 6 covers the real thread-affinity path end-to-end.
- **Effort:** M
- **Covers:** C1
- **Round-1 addresses:** concerns #1 (unconditional CoUninitialize after RPC_E_CHANGED_MODE), #2 (fresh locked thread — hard-fail rather than tolerate), #26 (wrapper-hook testing concern — mitigated by injecting CoInitializeEx itself, not just LockOSThread)

### Step 4. Process-level CoInitializeSecurity (early) + per-proxy CoSetProxyBlanket
- **Files touched:**
  - `internal/watcher/com_init_windows.go` (new — small file, hosts `InitCOMSecurityOnce`)
  - `internal/svc/handler.go` (call `InitCOMSecurityOnce` before ANY other subsystem that could touch COM)
  - `internal/watcher/logons_subscriber.go`
- **Change:** **`CoInitializeSecurity` must be called exactly once per process, BEFORE any COM activity (including the first `CoInitializeEx`).** Calling it after some other library/component has already initialized COM security returns `RPC_E_TOO_LATE`, at which point the process is stuck with whatever security the first caller picked — potentially wrong for `root\cimv2\TerminalServices`. The plan originally treated `RPC_E_TOO_LATE` as benign; it is not. Fix:

  Create `internal/watcher/com_init_windows.go` with:

  ```go
  var initCOMSecurityOnce sync.Once
  var initCOMSecurityErr error

  // InitCOMSecurityOnce initializes process-wide COM security for WMI with
  // RPC_C_AUTHN_LEVEL_PKT_PRIVACY / RPC_C_IMP_LEVEL_IDENTIFY. MUST be called
  // BEFORE the first CoInitializeEx in this process — Windows blocks a second
  // CoInitializeSecurity with RPC_E_TOO_LATE, and WMI against
  // root/cimv2/TerminalServices REQUIRES packet privacy.
  func InitCOMSecurityOnce() error {
      initCOMSecurityOnce.Do(func() {
          initCOMSecurityErr = callCoInitializeSecurity(
              RPC_C_AUTHN_LEVEL_PKT_PRIVACY,
              RPC_C_IMP_LEVEL_IDENTIFY,
          )
      })
      return initCOMSecurityErr
  }
  ```

  `internal/svc/handler.go` calls `watcher.InitCOMSecurityOnce()` in `Execute` **before** the first subsystem that could touch COM (today that's nothing, but future-proofs against a perfmon COM path or anything else). If the call returns `RPC_E_TOO_LATE`, that means the Go runtime or some dependency beat us to `CoInitializeSecurity` — log a `WARN` including the error, but continue. The subscriber will still run and may still succeed (defaults might be adequate), but this log is the signal to investigate.

  **Per-proxy:** after `IWbemLocator::ConnectServer` returns `IWbemServices` (inside the subscriber), call `CoSetProxyBlanket(services, RPC_C_AUTHN_LEVEL_PKT_PRIVACY, RPC_C_IMP_LEVEL_IMPERSONATE, ...)` directly on the proxy pointer. This is mandatory for the `root\cimv2\TerminalServices` namespace per MSDN.

  **Library-hides-proxy risk (Round-1 concern #4):** if `microsoft/wmi/pkg/base/event.RegisterWmiCallback` does not expose the underlying `IWbemServices` pointer, we CANNOT call `CoSetProxyBlanket` on it, and the subscription will fail on hardened hosts. Step 6 addresses this directly by adding a feasibility gate: if microsoft/wmi hides the proxy, we fall back to setting up `IWbemLocator` / `IWbemServices` ourselves via `go-ole` raw interfaces, call `CoSetProxyBlanket`, and pass the already-connected `IWbemServices` into microsoft/wmi's helper via its session-reuse API (or escalate to owning the full subscription path).

  **Hot-reload:** `CoInitializeSecurity` is process-wide and cannot be reapplied. Step 18's config reload path does NOT re-run `InitCOMSecurityOnce` — the `sync.Once` guard makes repeat calls no-ops.
- **Verification:**
  - Unit test `TestInitCOMSecurityOnce_CalledOnce` injects a fake to verify `sync.Once` semantics (counts invocations across concurrent calls).
  - Unit test `TestInitCOMSecurityOnce_TooLateNotFatal` — fake returns `RPC_E_TOO_LATE`, assert `err != nil` is returned but subscriber proceeds.
  - Integration test: manual on a hardened host (strict UAC, no `LocalAccountTokenFilterPolicy`): subscriber receives events without `E_ACCESSDENIED`. Log line `wmi_logons=proxy_blanket_set authn=pkt_privacy impl=impersonate` emitted on startup.
- **Effort:** M
- **Covers:** C3
- **Round-1 addresses:** concerns #3 (CoInitializeSecurity timing), #24 (hot-reload doesn't re-run security), partially #4 (proxy blanket access — feasibility gate moved to Step 6)

### Step 5. Explicit Release of COM pointers WE own on exit (library-owned objects excluded)
- **Files touched:**
  - `internal/watcher/logons_subscriber.go`
- **Change:** The subscriber owns an ordered slice of COM pointers **that it acquired directly** — specifically `IWbemLocator` (from `CoCreateInstance`) and `IWbemServices` (from `IWbemLocator::ConnectServer`, with `CoSetProxyBlanket` applied). It does NOT own the sink and async enumerator — those are internal to `microsoft/wmi/pkg/base/event.RegisterWmiCallback`. Calling `Release()` on library-owned objects can double-free; NOT calling `Release()` on our own locator/services leaks handles and WMI subscriptions.

  Explicit ownership rule in code:
  ```go
  // Owned by subscriber — MUST Release in reverse acquisition order on every exit path.
  ownedCOMPointers []comReleaser // locator, services, (optional future: direct sink)

  // NOT owned: sink and enumerator created by event.RegisterWmiCallback — library handles their lifecycle.
  ```

  On every exit path (ctx cancelled, subscription dead, reconnect backoff) the worker:
  1. Calls `unregister` / cancel API on the microsoft/wmi callback (step 6 specifies this) — this disconnects the sink and tells the library to release its refs. Wait for that to complete synchronously.
  2. Walks `ownedCOMPointers` in reverse, calls `Release()`, zeros the field.
  3. Returns from the worker → deferred `CoUninitialize` runs → deferred `UnlockOSThread` runs.

  Crucially, `ctx.Done()` alone does NOT trigger Release — the run loop uses finite timeouts (≤1 s waits between event retrievals), checks `ctx.Done()` between waits, and drops into the cleanup path synchronously.
- **Verification:**
  - Unit test `TestLogonsSubscriber_ReleasesOwnedPointersOnCtx` uses mock COM pointers that increment per-pointer release counters; assert counter == acquire counter after ctx cancel for locator and services only (sink and enumerator left alone).
  - Unit test `TestLogonsSubscriber_DoesNotDoubleReleaseLibraryObjects` — injects a microsoft/wmi callback-helper fake that records whether we called Release on the sink/enumerator it owns; assert we did not.
  - Manual: run `Get-Process drainctl | Select Handles, WorkingSet, CommandLine` in a loop across 50 service start/stop cycles, confirm handle count stable (drift < 10 per cycle). Also run `Get-WmiObject -Namespace root -List __TemporaryEventConsumer` to spot-check the WMI server side isn't accumulating orphan subscriptions (stale subscriptions would show up as lingering consumer instances).
- **Effort:** M
- **Covers:** C2
- **Round-1 addresses:** concerns #5 (ownership model unclear), #27 (WMI server-side leak detection — added to verification)

---

## Phase 3 — Subscription mechanics

### Step 6. Feasibility gate + register WMI callback (microsoft/wmi primary, raw go-ole fallback)
- **Files touched:**
  - `internal/watcher/logons_subscriber.go`
- **Change:**

  **Feasibility gate (do this FIRST, before writing much subscription code):** audit the `microsoft/wmi/pkg/base/event` package source to confirm:
  1. `RegisterWmiCallback` either accepts an already-connected `IWbemServices` proxy (so we can apply `CoSetProxyBlanket` ourselves before handing it over), OR exposes the internal proxy to callers for blanket application.
  2. The callback runs with deterministic lifecycle (caller can cancel/unregister and be guaranteed no more callbacks fire).
  3. The callback's apartment/thread model is documented OR observable — we need to confirm the callback does NOT require our locked worker thread (it will fire on WMI-managed threads, and we accept that — the callback does minimum work).

  **If microsoft/wmi fails any of these three:** fall back to raw `go-ole` for the full subscription path: `CoCreateInstance(CLSID_WbemLocator)` → `ConnectServer("root\\cimv2\\TerminalServices")` → `CoSetProxyBlanket` → `ExecNotificationQueryAsync(sink)` where `sink` implements `IWbemObjectSink` via go-ole. This is ~200 lines of our own COM code but keeps the proxy-blanket contract intact. Budget: if the feasibility gate fails, Step 6 grows from M to L.

  **If microsoft/wmi passes:** use `event.RegisterWmiCallback(callbackCtx, "root\\cimv2\\TerminalServices", "", queryString)`. Callback runs on a WMI thread — it does the minimum work (extract `TargetInstance.Logons` as STRING via `ole.VT_BSTR`, update the subscriber's cached `LogonsSnapshot` under `sync.RWMutex`, set `LastSubEvent = time.Now()`, non-blocking nudge on the channel) and returns. Callbacks doing more risk blocking WMI's thread pool.

  **Cancellation contract (Round-1 concern #7):** on exit path, call the library's unregister API (or raw `IUnsubscribe` equivalent), then block for up to 2s on a drain channel that the callback signals when the library confirms no more callbacks will fire. If the library doesn't offer this contract, fall back to raw go-ole — we cannot Release `IWbemServices` while a callback might still be in flight.

  **Callback apartment (Round-1 concern #6):** explicitly documented: the callback fires on an arbitrary thread inside WMI's provider pool, NOT the subscriber's locked worker thread. This is by design — the worker thread only owns the setup/teardown path. The callback body uses atomic-ish operations (mutex-guarded snapshot update + channel nudge) and does not make any COM calls itself. Any COM work the callback needs to do (reading `TargetInstance.Logons`) happens through the already-constructed `ole.IDispatch` handle supplied by the library, which is safe to read from multiple threads.
- **Verification:**
  - **Feasibility audit artifact:** a short `docs/reviews/wmi-feasibility.md` (1–2 pages) documenting the three contracts above with links to microsoft/wmi source. Committed alongside this step.
  - Integration test (build-tagged `//go:build windows && integration`) `TestLogonsSubscriber_Live` runs on CI/dev box with TS role: start subscriber, shell out to `change logon /disable`, assert `Snapshot().Disabled == true` within 5 s, then `/enable` and re-assert.
  - Integration test `TestLogonsSubscriber_ClosesOnCancelCleanly` — start subscriber, cancel ctx, assert `NewLogonsSubscriber` can be called immediately afterward (no orphaned __TemporaryEventConsumer blocking recreation).
- **Effort:** M (or L if feasibility gate fails)
- **Covers:** C11, C12
- **Round-1 addresses:** concerns #4 (proxy blanket access verified), #6 (callback apartment explicitly documented), #7 (cancellation contract)

### Step 7. Parse `Logons` as string per MOF; distinguish "not applicable" from "could not read"
- **Files touched:**
  - `internal/watcher/logons_subscriber.go`
- **Change:** Read `TargetInstance.Logons` as `VARIANT BSTR`, not uint32 (`Win32_TerminalServiceSetting.Logons` is `CIM_STRING` per MOF). Tri-state parse outcomes:

  | Input | Resulting snapshot |
  | --- | --- |
  | `"0"` | `Value="0", Disabled=false, ValuePresent=true` — logons allowed |
  | `"1"` | `Value="1", Disabled=true, ValuePresent=true` — logons disabled |
  | `""` (property present but empty) | `Value="", Disabled=false, ValuePresent=false` — treat as "not applicable to this host" (non-RDSH, client Windows) |
  | Property absent from instance (MOF mismatch) | Return PARSE_ERROR — subscriber logs a single warning at Debug and sets Health to Stale until next successful read. Does NOT update ValuePresent. |
  | `VARIANT` conversion fails (unexpected VT type) | Same as property absent — PARSE_ERROR path. |
  | Query-time RPC error (no VARIANT to parse) | Not a parse error — handled in Step 9's subscription-death detection. |

  **Distinction matters (Round-1 concern #12):** a property-absent / parse-fail state is NOT the same as "legitimate empty value". The former means we don't know; the latter means the host isn't RDSH and never will have a meaningful Logons value. The aggregator (Step 11) treats them differently: `ValuePresent=false && Health=Live` → permanently ignore Logons dimension; `ValuePresent=false && Health=Stale` → Logons unknown, factor into Degraded.
- **Verification:** Unit test `TestLogonsSubscriber_ParseValues` feeds synthetic VARIANTs (`VT_BSTR "0"`, `VT_BSTR "1"`, `VT_BSTR ""`, `VT_NULL`, `VT_I4 1`) into the parse helper and asserts the resulting `LogonsSnapshot` AND `Health` transitions. No live WMI needed. A separate test confirms property-absent doesn't flip `ValuePresent` from a prior-known-true to false (sticky on last successful read).
- **Effort:** S
- **Covers:** C4
- **Round-1 addresses:** concern #12 ("not applicable" vs "could not read")

---

## Phase 4 — Robustness

### Step 8. Health tracking split across subscription + poll (never "quiet = stale")
- **Files touched:**
  - `internal/watcher/logons_subscriber.go`
- **Change:** `LogonsSnapshot.Health` is derived from two independent timestamps: `LastSubEvent` (last successful subscription callback) and `LastPollOK` (last successful reconcile-poll read). A quiet system with a working subscription is Live even if no event has fired in hours — health is about "subscription is healthy", not "activity recently".

  Derivation rule:
  ```go
  func (s LogonsSnapshot) deriveHealth(now time.Time, deathThreshold time.Duration) LogonsHealth {
      if s.LastSubEvent.IsZero() && s.LastPollOK.IsZero() {
          return LogonsHealthUnavailable
      }
      subHealthy := !s.LastSubEvent.IsZero() && now.Sub(s.LastSubEvent) < deathThreshold
      pollHealthy := !s.LastPollOK.IsZero() && now.Sub(s.LastPollOK) < deathThreshold
      if subHealthy || pollHealthy {
          return LogonsHealthLive
      }
      return LogonsHealthStale
  }
  ```

  The reconcile poll (Step 10) is what keeps `LastPollOK` current on quiet systems — every 120 s the poll runs even if no events fire, refreshing health. The subscription callback (Step 6) refreshes `LastSubEvent` whenever an event does fire. Neither path "expires" the other.

  **`deathThreshold` tuning:** default = `3 × reconcileInterval` (360 s with default 120s reconcile). Rationale: a single missed poll is noise; three consecutive misses means the poll path is genuinely broken. Configurable via Step 18.

  **Failure modes that flip Health to Stale:**
  - `winmgmt` stopped (RPC calls fail, reconcile fails, no new subscription events)
  - WMI provider unload (`WmiPrvSE.exe` crashes; subscription silently dies)
  - RPC `E_ACCESSDENIED` (transient cert / token rotation)
  - Subscription confirmed dead via Step 9's detection but before reconnect succeeds

  **Health transitions are surfaced via a distinct log key** (`wmi_logons=health_transition from=live to=stale reason=poll_failed`) so operator scraping / alerting can notice.

  **Independence from the cached `Value`:** Health is *about the source*, not the value. The last-known `Disabled` and `ValuePresent` fields keep their previous values across a Live→Stale transition — operators see "last-known-value, but we're not sure it's current" rather than losing the state entirely.
- **Verification:**
  - Unit test `TestLogonsSnapshot_HealthTransitions` — table-driven over `(LastSubEvent, LastPollOK, now, deathThreshold)` tuples asserting the derived Health. 12+ cells.
  - Unit test `TestLogonsSnapshot_QuietSystemStaysLive` — subscribe at t=0, never deliver an event, run reconcile poll every 120 s, assert Health stays Live at t=10min.
  - Unit test `TestLogonsSnapshot_BothPathsFailedGoesStale` — suppress both callbacks and poll success, advance clock past `deathThreshold`, assert Health flips to Stale. Drive recovery, assert flip back to Live.
- **Effort:** M
- **Covers:** C6
- **Round-1 addresses:** concerns #8 (quiet system should not go stale), #9 (death detection based on health of both paths, not event volume), #10 (successful poll doesn't mask subscription death — separate timestamps)

### Step 9. Subscription-death detection + retry with backoff (trigger on source health, not event volume)
- **Files touched:**
  - `internal/watcher/logons_subscriber.go`
- **Change:** Detection triggers from three independent signals, any of which causes the worker to enter the reconnect path:
  1. **Reconcile poll failure** — a `SELECT Logons FROM Win32_TerminalServiceSetting` returns an RPC error (not a zero-row result, that's handled separately in Step 10). One failure is noise; two consecutive is actionable.
  2. **Subscription confirmed dead** — the microsoft/wmi callback helper surfaces a channel/callback that notifies on subscription death (or errors out of `Next()` on the raw fallback path). Single signal triggers immediately.
  3. **`LastSubEvent` AND `LastPollOK` both older than `deathThreshold`** — fallback safety net. This is *not* the primary trigger (because a quiet system can legitimately have no events for a long time — Step 8 explicitly calls this out), but if *both* paths have been silent past threshold, something is wrong.

  **Reconnect path:**
  1. Unregister microsoft/wmi callback (or equivalent for raw path). Wait for the library's "no-more-callbacks" confirmation (per Step 6's cancellation contract).
  2. Release owned COM pointers in reverse order (per Step 5).
  3. Sleep for backoff duration (exponential: 1 s → 2 → 4 → 8 → 16 → 32 → 60, capped at 60 s). Reset backoff to 1 s on successful reconnect.
  4. Reacquire: `CoCreateInstance(WbemLocator)` → `ConnectServer` → `CoSetProxyBlanket` → `event.RegisterWmiCallback`.
  5. **Immediately run a reconcile poll** on success to seed `LastPollOK` and flip Health back to Live.
  6. Loop back to normal operation.

  **Ctx cancellation during reconnect:** the backoff sleep is cancellable — use `time.NewTimer` + `select` on `ctx.Done()`, not bare `time.Sleep`. Otherwise the service stops block on up to 60 s of backoff.

  **Throttling of reconnect logs:** log `WARN` only on transition into "reconnecting" and transition out ("reconnected"), not on each retry — avoids spamming the log if winmgmt is flapping.
- **Verification:**
  - Unit test `TestLogonsSubscriber_ReconnectBackoff` — inject a fake connect that fails N times then succeeds; assert backoff sequence `1s, 2s, 4s, …, 60s` (capped), assert ctx cancellation during backoff returns within 50 ms.
  - Unit test `TestLogonsSubscriber_PollFailureTriggersReconnect` — inject a poll fault, assert subscriber enters reconnect path after two consecutive failures.
  - Manual: `net stop winmgmt && Start-Sleep 5 && net start winmgmt`, confirm subscriber recovers within ~90 s (one backoff cycle). Log shows single WARN on entry + single INFO on recovery, not per-retry spam.
- **Effort:** M
- **Covers:** C7
- **Round-1 addresses:** concerns #9 (death detection based on failed health, not event absence), #10 (explicit trigger #2 — subscription death signal independent of poll)

### Step 10. Periodic reconciliation poll — robust to singleton cardinality edge cases
- **Files touched:**
  - `internal/watcher/logons_subscriber.go`
- **Change:** Independent `time.Ticker` (default 120 s, configurable per Step 18) runs on the locked OS thread (drained alongside the cancel channel in the main select) and executes `SELECT Logons FROM Win32_TerminalServiceSetting` synchronously via our owned `IWbemServices` proxy (blanket already applied). `Win32_TerminalServiceSetting` is a singleton class per host — exactly one instance expected. Result handling:

  | Query result | Action |
  | --- | --- |
  | Exactly 1 row, `Logons` property present | Parse per Step 7 rules, update cache, update `LastPollOK = now`, Health derived to Live |
  | Exactly 1 row, `Logons` absent from returned properties | Treat as parse error per Step 7; `LastPollOK` NOT updated; single WARN log |
  | Zero rows | Treat as provider misconfiguration — log WARN at the first occurrence, every 15 minutes thereafter (not every poll); `LastPollOK` NOT updated; trigger Step 9's reconnect path after 2 consecutive zero-row results |
  | More than 1 row | Unexpected (singleton class). Log ERROR with row count; consume first row; `LastPollOK` updated |
  | RPC error | Per Step 9: one failure is noise, two consecutive trigger reconnect |
  | Provider unload / namespace unavailable | RPC error path above |

  If the parsed value differs from cached `Snapshot.Value`, nudge the channel — catches both `WITHIN 2` polling gaps AND silent subscription death. If the parsed value matches, just refresh `LastPollOK` (no nudge — nothing to report).

  **Interaction with subscription callback:** parallel mutex-guarded writes. The `sync.RWMutex` on the cached snapshot lets the poll and callback both update safely; the last-writer-wins is fine because they're reading the same underlying state.
- **Verification:**
  - Unit test `TestLogonsSubscriber_ReconcileCatchesDrift` — suppress the callback path (simulate dead subscription), flip the mock query result, assert nudge fires within 2× reconcile interval.
  - Unit test `TestLogonsSubscriber_ReconcileZeroRows` — mock returns zero rows, assert `LastPollOK` unchanged, WARN log emitted, Step 9 reconnect triggers after 2 consecutive empty results.
  - Unit test `TestLogonsSubscriber_ReconcileMultipleRows` — mock returns 2 rows, assert ERROR log, first-row parsed.
  - Integration test on non-RDSH Server (feature not installed) — confirm the `Logons` property is empty-string (handled as `ValuePresent=false` per Step 7) rather than zero-row or missing-property.
- **Effort:** S
- **Covers:** C5, C7
- **Round-1 addresses:** concern #11 (cardinality / namespace edge cases)

---

## Phase 5 — Aggregator refactor (`DrainSources` / `EffectiveMode` / shim)

### Step 11. Add `DrainSources` aggregator with explicit `Degraded` signal (fail-safe, not fail-open)
- **Files touched:**
  - `tsstate_windows.go` (new, root package)
- **Change:** New file (keeps `registry.go` registry-only per C12).

  ```go
  type DrainSources struct {
      DenyTSConnections       bool           // fDenyTSConnections registry value
      TSServerDrainMode       DrainMode      // 0/1/2 raw value; meaningful iff TSServerDrainModePresent
      TSServerDrainModePresent bool
      Logons                  LogonsSnapshot // copied, not pointer — isolates aggregator from subscriber lifecycle
      KeyModified             time.Time
  }

  type EffectiveState struct {
      Mode     DrainMode // display-layer enum (AllowAll, DrainUntilBoot, DrainPersistent, DenyAll)
      Reason   string    // human-readable source attribution
      Degraded bool      // true iff at least one source is Stale or Unavailable in a way that affects confidence
  }

  func (s DrainSources) EffectiveMode() EffectiveState { ... }
  ```

  **Precedence table (explicit, covers every observable state):**

  | `DenyTS` | `TSServerDrainMode`present | `TSServerDrainMode` | `Logons.Health` | `Logons.ValuePresent` | `Logons.Disabled` | Result `Mode` | Result `Reason` | Result `Degraded` |
  | --- | --- | --- | --- | --- | --- | --- | --- | --- |
  | true | * | * | * | * | * | `DenyAll` | `"registry: fDenyTSConnections=1"` | false |
  | false | * | * | `Live` | true | true | `DenyAll` | `"wmi: Logons=1"` | false |
  | false | * | * | `Stale` | true | true | `DenyAll` | `"wmi: Logons=1 (stale; last confirmed <Xs ago)"` | true |
  | false | * | * | `Stale` | * | * (last-known-false) | *(fall through to registry drain)* | *(registry reason)* + `"; logons: stale"` | true |
  | false | * | * | `Unavailable` | * | * | *(fall through to registry drain)* | *(registry reason)* + `"; logons: unavailable"` | true |
  | false | true | 2 | any | * | * | `DrainPersistent` | `"registry: TSServerDrainMode=2"` | *(from logons)* |
  | false | true | 1 | any | * | * | `DrainUntilBoot` | `"registry: TSServerDrainMode=1"` | *(from logons)* |
  | false | true | 0 | any | * | * | `AllowAll` | `""` | *(from logons)* |
  | false | false | — | any | * | * | `AllowAll` | `""` | *(from logons)* |

  **Key semantic decision (Round-1 concern #13):** when Logons is `Stale` AND last-known `Disabled=true`, we continue to report `DenyAll` — last-known state is better than silently dropping it, and the `Degraded=true` flag tells the operator we're not sure it's still current. When Logons is `Stale` or `Unavailable` AND there's no other deny signal, we fall through to the registry drain but set `Degraded=true`. **We never silently claim `AllowAll` while Logons is unknown** — the `Degraded` flag is the explicit signal to the operator and to automation.

  **Who reads `Degraded`:**
  - Dashboard UI: shows an orange warning pip alongside the mode (details in Step 14's frontend note).
  - Notifications: a new `TriggerDegraded` fires once on Live→Degraded transition (`config.Notifications` can opt in/out just like existing triggers).
  - Automation (PowerShell module / DLL): `Get-DrainStatus` output gains `Degraded: $true` / `$false`. Orchestration scripts that gate on "connections allowed" can check both `Mode == AllowAll` AND `Degraded == $false` if they want fail-safe behavior.

  `ReadDrainSources(src LogonsSource) (*DrainSources, error)` — registry read + (`src != nil ? src.Snapshot() : LogonsSnapshot{Health: Unavailable}`). Takes the interface (Step 2) not a concrete type, so tests can inject fakes.
- **Verification:** Table-driven unit test `TestDrainSources_EffectiveMode` covering every cell in the table above — **all** dimensions: `{DenyTS, TSDrainModePresent, TSDrainMode(0/1/2), Logons.Health(3), Logons.ValuePresent(2), Logons.Disabled(2)}` = 2×2×3×3×2×2 = 144 cells, but many collapse because DenyTS=true short-circuits. Effective coverage: ~60 meaningful rows. Assert `Mode`, `Reason`, AND `Degraded` for every row. Pure Go, no COM.
- **Effort:** M
- **Covers:** C6, C9
- **Round-1 addresses:** concerns #13 (AllowAll-while-unknown is unsafe — explicit `Degraded` flag), #14 (ValuePresent in precedence), #15 (full coverage table covers all observable combinations), #17 (LogonsSnapshot in root package resolves cycle), #18 (interface `LogonsSource` for test injection), dropped-items "stale Disabled=true policy" (documented explicitly as "last-known wins + Degraded=true")

### Step 12. Shim `ReadDrainMode` through `DrainSources` (byte-exact legacy behavior preserved)
- **Files touched:**
  - `registry.go`
- **Change:** `ReadDrainMode()` becomes a thin wrapper that:
  1. Calls `ReadDrainSources(nil)` — passing nil means `DrainSources.Logons.Health = Unavailable` and any Logons signal is ignored (falls through to registry-only behavior in Step 11's precedence table).
  2. Calls `sources.EffectiveMode()` to derive `Mode`. In the nil-subscriber case, this can only return `AllowAll / DrainUntilBoot / DrainPersistent / DenyAll` — the same four values the pre-refactor `ReadDrainMode` could return, so enum-compatibility is preserved.
  3. Constructs the legacy `*RegistryState{Host, Mode, KeyModified, ValuePresent}` — preserving the EXACT field set and JSON shape. `ValuePresent` reflects `TSServerDrainModePresent` (original behavior: true iff `TSServerDrainMode` was readable from the registry). No new fields on the returned struct.

  **Absent-`TSServerDrainMode` behavior — explicitly preserved (Round-1 concern #16):** pre-refactor `ReadDrainMode` handled `ErrNotExist` by returning `Mode=AllowAll, ValuePresent=false`. This path is exercised today when the Terminal Server key exists but the `TSServerDrainMode` value has never been written (fresh Windows install, no `change logon` ever run). Step 12 preserves this exactly: when `TSServerDrainModePresent=false` AND `DenyTSConnections=false` AND Logons is ignored (nil subscriber), `EffectiveMode()` returns `AllowAll` and the shim reports `ValuePresent=false`.

  `Reason` and `Degraded` from `EffectiveState` are NOT carried into `RegistryState` — the legacy struct doesn't have those fields. External DLL / PowerShell callers see unchanged JSON. The new fields are consumed only on the service path via the new `CheckResult.Reason` and (if added) `CheckResult.Degraded` — see Step 14.

  **External API compatibility is non-negotiable** — the DLL is shipped and in production.
- **Verification:**
  - Existing `TestDrainMode_String` still passes.
  - Add `TestReadDrainMode_ShimMatchesLegacy_AllCases` — snapshot the returned `RegistryState` AND its marshalled JSON for every combination of:
    - `fDenyTSConnections` absent / 0 / 1
    - `TSServerDrainMode` absent / 0 / 1 / 2
    - Subscriber nil (legacy path — this is the only one the shim exercises)

    Assert exact equality with hand-computed golden values that match the pre-refactor behavior.
- **Effort:** S
- **Covers:** C9, C12
- **Round-1 addresses:** concern #16 (absent `TSServerDrainMode` handling spelled out)

### Step 13. Service-path read via the `LogonsSource` interface (no concrete type leak)
- **Files touched:**
  - `internal/svc/check.go`
  - `internal/svc/handler.go` (to pass the source in)
- **Change:** `svcRunCheck` gains a `logonsSrc dc.LogonsSource` parameter (the interface from Step 2, NOT the concrete `*watcher.LogonsSubscriber` type). This lets tests inject fakes without spinning up COM, and keeps `internal/svc` from importing `internal/watcher` just for the concrete type.

  Replaces `dc.ReadDrainMode()` with `dc.ReadDrainSources(logonsSrc)` and calls `sources.EffectiveMode()` to derive `state.Mode`, `state.Reason`, and `state.Degraded`. Transition detection stays based on recomputed effective mode (already how `check.go` works — don't break it per C8, and explicitly document this invariant in the function doc comment so future refactors preserve it).

  The `Reason` string and `Degraded` bool flow onto `dc.AuditRecord.Reason` / (new) `dc.AuditRecord.Degraded` and `dc.CheckResult.Reason` / `dc.CheckResult.Degraded` — see Step 14 for plumbing. Transitions emit only when `newEffective.Mode != lastEmitted.Mode` OR `newEffective.Degraded != lastEmitted.Degraded` — a flip into/out of Degraded is itself a material state change operators want to see.

  **Race preservation (Round-1 concern — C8 / silently-dropped item):** the existing effective-mode-based transition detection coalesces simultaneous watcher nudges naturally — both registry and Logons channel events result in the same recomputed `EffectiveState`. The second nudge in a pair sees the same state as the first (no new row emitted) UNLESS state actually changed between ticks. Document this invariant in a comment at the top of `svcRunCheck`. Add a test (`TestSvcRunCheck_DualNudgeDoesNotDoubleEmit`) that fires both channels in quick succession and asserts exactly one audit row is written.

  **4657 attribution correlation:** when `EffectiveMode.Reason` starts with `"registry: "`, the existing `evtSub.WaitAttribution` correlation applies (per `internal/svc/check.go:46-55`). When `Reason` starts with `"wmi: "`, skip 4657 correlation — there's no registry write to attribute. If the optional 4688 heuristic (Step 16) is enabled, that path fires for `"wmi: "` reasons. Otherwise `changed_by=""`.
- **Verification:**
  - Existing `check_test.go` cases updated to pass a `nil` `LogonsSource`, verify behaviour unchanged from today.
  - New test `TestSvcRunCheck_LogonsOverridesEffectiveMode` injects a fake `LogonsSource` reporting `Disabled=true, Health=Live`, asserts transition emitted with reason `"wmi: Logons=1"`.
  - New test `TestSvcRunCheck_DegradedTransitionEmitsRow` — start Live, flip Logons source to Stale mid-run, assert a new audit row is written with `Degraded=true` and matching reason suffix.
  - New test `TestSvcRunCheck_DualNudgeDoesNotDoubleEmit` — fire both registry and Logons channels, assert exactly one audit row for the same effective state.
- **Effort:** M
- **Covers:** C6, C8, C9
- **Round-1 addresses:** concern #18 (interface not concrete type), C8 dropped-item (explicit race discussion + test)

### Step 14. Wire `Reason` + `Degraded` into CheckResult / audit / CLI
- **Files touched:**
  - `format.go` (CheckResult struct, CSV writer)
  - `internal/svc/check.go`
  - `internal/svc/handler.go` (HandleStatus)
  - `internal/telemetry/audit.go` already has `Reason` (no schema change); adds `Degraded bool` — this IS a schema change, pair with a migration
  - `internal/telemetry/schema.go` / migration file — add `degraded INTEGER NOT NULL DEFAULT 0` column to `audit` table (SQLite supports `ALTER TABLE ADD COLUMN` with DEFAULT — safe migration)
- **Change:** Add to `dc.CheckResult`:
  ```go
  Reason   string `json:"reason,omitempty"`
  Degraded bool   `json:"degraded,omitempty"`
  ```

  `svcRunCheck` populates both from `EffectiveMode()`. `serviceHandler.HandleStatus` computes the same via `ReadDrainSources(h.logonsSrc)` and populates both.

  **DrainMode label display does NOT change** — `Mode` is still the display-layer enum that the dashboard's `modeLabel()` maps to strings. `Reason` is a detail-pane field. `Degraded` is a boolean that the dashboard renders as an orange warning pip adjacent to the mode label (see deferred item — frontend implementation is out of scope for this pass, but the JSON contract is in place so it's a one-PR add).

  **CLI `drainctl history` display (Round-1 dropped-item — external consumer visibility of hedge):** `format.go`'s table writer gains a new `REASON` column when `--reason` flag is passed (opt-in to preserve default column layout). JSON output includes `reason` and `degraded` unconditionally. CSV writer includes both as new columns (non-breaking: appended to the end). `drainctl check --json` includes both.

  **PowerShell module (`Get-DrainStatus`):** out of scope for this PR but the JSON contract is in place; a follow-up PR adds `Reason` and `Degraded` to the output object.

  **Audit table schema migration:** `internal/telemetry/schema.go` gets a new version bump. Migration SQL: `ALTER TABLE audit ADD COLUMN degraded INTEGER NOT NULL DEFAULT 0`. Old rows default to `0` (not degraded) which is sound — they predate the Degraded concept. `AuditRecord` struct gains `Degraded bool`.
- **Verification:**
  - Unit test `TestHandleStatus_IncludesReasonAndDegraded`.
  - Frontend `api.js` JSON contract: no breakage (both fields are `omitempty` so absence keeps old clients happy).
  - `drainctl check --json` shows both fields.
  - Table writer test with `--reason` flag.
  - CSV writer test confirms both new columns appended at end (existing positional consumers unaffected unless they parse by column index past the old tail).
  - Schema migration test `TestAuditSchema_AddDegradedColumn` — open a pre-migration DB, apply migration, write a new row, read it back, assert `Degraded=false` for old rows and the written value for new ones.
- **Effort:** M (upgraded from S because of the schema migration + CLI/CSV plumbing)
- **Covers:** C9
- **Round-1 addresses:** concern #9 (dropped-item: CLI/DLL display of Degraded hedge — now plumbed through JSON / table --reason flag / CSV)

---

## Phase 6 — Attribution

### Step 15. Widen 4657 filter to accept `fDenyTSConnections` (C13 pre-existing bug)
- **Files touched:**
  - `internal/watcher/evtsubscribe.go` (line 246)
- **Change:** Replace `strings.EqualFold(valueName, "TSServerDrainMode")` with a set check: accepts `TSServerDrainMode` OR `fDenyTSConnections`. Path suffix filter (`\control\terminal server`) unchanged — that still scopes us to the correct key.
- **Verification:** Unit test `TestEvtSubscriber_FilterAcceptsBothValueNames` feeds synthetic 4657 XML with each value name, asserts both produce an attribution entry. Manual: `reg add "HKLM\SYSTEM\CurrentControlSet\Control\Terminal Server" /v fDenyTSConnections /t REG_DWORD /d 1 /f` with SACL configured, confirm attribution appears in audit row.
- **Effort:** S
- **Covers:** C13

### Step 16. Opt-in 4688 (Process Creation) heuristic for Logons attribution — best-effort with documented limitations
- **Files touched:**
  - `internal/watcher/evtsubscribe.go` (extend existing subscriber to also filter 4688 when enabled)
  - `config.go` (one new field — see naming note)
- **Change:** Config naming (Round-1 concern #19): settled name is `Audit.AttributionViaProcessCreation bool` with default `false`. One name, used everywhere — no `ProcessCreationEnabled` variant. The field is part of an `Audit` sub-struct on `Config` so future audit tuning can cluster there.

  When true, the existing `EventSubscriber` query widens to `*[System[EventID=4657 or EventID=4688]]`. A new 4688-handler filters the resulting XML.

  **Command-line matching (Round-1 concern #20 — make this robust):** the match function handles:
  - Case-insensitive path comparison on `NewProcessName` — accepts `C:\Windows\System32\change.exe`, `C:\Windows\SysWOW64\change.exe`, any drive letter, UNC paths.
  - `CommandLine` tokenization using a real shell-quote parser (we reuse `encoding/csv` with `'` `"` handling, or a small hand-rolled tokenizer matching Windows CommandLineToArgv semantics).
  - Matches argument 1 against case-insensitive `logon` (accepts `/LOGON`, `-logon`, `logon`).
  - Matches argument 2 against the set `{disable, enable, drain, drainuntilrestart}` (case-insensitive), with or without a leading `/` or `-`.
  - Localized `change.exe` variants — the executable name is localized on non-English Windows (e.g. `change.exe` stays but argument words may be localized via some versions). For safety, the match also considers `chglogon.exe` (the Server Core alias). Unmatched localizations surface as "unknown operator" — `changed_by=""` with a DEBUG log `wmi_logons=4688_unmatched_cmdline` to help operators tune if they see untagged transitions.

  **Process-creation ≠ operation-succeeded (Round-1 concern #20):** a 4688 event proves the operator LAUNCHED `change.exe`, not that it succeeded. If the process was blocked by UAC or returned non-zero, Logons may not have flipped. We accept this — the attribution is best-effort by design. Document in README + BUGS.md.

  Matching events populate a new attribution buffer (NOT the same `latest *RegistryChangeAttribution` the 4657 path uses, per Round-1 concern #21 — that struct carries registry-specific fields that don't apply to process creation). Introduce a small discriminator:

  ```go
  type AttributionSource int
  const (
      AttrSourceRegistry4657 AttributionSource = iota // 4657 — registry write, carries key/value
      AttrSourceProcess4688                           // 4688 — process creation, carries command line only
  )

  type AttributionEntry struct {
      Timestamp time.Time
      User      string
      Source    AttributionSource
      // Registry-specific fields (Source == Registry4657 only)
      ObjectName, ObjectValueName string
      // Process-specific fields (Source == Process4688 only)
      CommandLine string
  }
  ```

  `svcRunCheck` already consults `evtSub.WaitAttribution` on transitions. Update `WaitAttribution` to accept a filter: `(after time.Time, wantSources []AttributionSource, timeout time.Duration) (user string)`. Registry-caused transitions ask for `Registry4657` only (don't pick up process-creation noise). WMI-caused transitions ask for `Process4688` only.

  When disabled (default) or required GPOs aren't present, Logons transitions get `changed_by=""` — same pattern as reconciliation rows per C10.
- **Verification:**
  - Unit test `TestEvtSubscriber_4688CmdLineMatch` feeds synthetic 4688 XML with variants: canonical `change.exe logon /disable`, quoted paths `"C:\Program Files\change.exe"` (won't match but shouldn't crash), mixed-case `CHANGE.EXE LOGON /DISABLE`, Server Core `chglogon.exe logon /drain`, non-matching `cmd.exe /c dir`. Asserts only matching invocations populate attribution.
  - Unit test `TestAttributionEntry_SourceFilter` — mixed buffer of 4657 and 4688 entries, `WaitAttribution` with `Registry4657` filter returns only the 4657 ones.
  - Manual: GPO enables "Audit Process Creation: Success" + "Include command line in process creation events", run `change logon /disable`, verify `changed_by` populates on the resulting audit row.
- **Effort:** M
- **Covers:** C10
- **Round-1 addresses:** concerns #19 (one canonical config name), #20 (robust cmdline match + docs that 4688 ≠ success), #21 (discriminated AttributionEntry)

---

## Phase 7 — Service wiring

### Step 17. Start the subscriber in `Execute`; thread ctx + failure path; nil-safe select
- **Files touched:**
  - `internal/svc/handler.go`
- **Change:** Call `watcher.InitCOMSecurityOnce()` FIRST (before any subsystem that could touch COM — per Step 4). Then, after `evtSub` init (around line 440), start `watcher.NewLogonsSubscriber(ctx, logonsOpts)`.

  Pattern matches existing `evtSub` init: on error, log `WARN` + continue with `logonsSrc = nil`. The `nil` sentinel flows through `DrainSources` (per Step 11) as `LogonsHealthUnavailable`, so the service degrades gracefully.

  **Nil-safe select (Round-1 concern #23):** the new `case <-logonsSub.Changes():` is only added to the main select when `logonsSub != nil`. The canonical Go idiom uses a `<-chan struct{}` variable initialized to `nil` that is only assigned when the subscriber starts:

  ```go
  var logonsChanges <-chan struct{} // nil — select ignores nil channels
  if logonsSub != nil {
      logonsChanges = logonsSub.Changes()
  }
  // ... later in select:
  case <-logonsChanges: // when nil, this case is never selected
      svcRunCheck(ctx, handler, &cfg, ..., logonsSub)
  ```

  Pass `logonsSub` (as the `LogonsSource` interface — typed as `dc.LogonsSource`) into every `svcRunCheck` call. Ctx cancellation is already parent-scoped via `defer cancel()` at line 332; the subscriber inherits that correctly (addresses P2).

  **Stop ordering:** the subscriber's exit path is synchronous (Step 5). On service stop, wait up to 10 s for the subscriber to drain + Release + CoUninitialize. If it exceeds 10 s, log ERROR and proceed with service stop anyway (SCM's wait hint) — but this indicates a subscriber bug.
- **Verification:**
  - Manual service start, look for log line `logons_subscriber=started`. Trigger a Logons flip and confirm `trigger=logons_change` log appears. Service stop is clean (`service=stopped` log line emits within 10 s).
  - `handler_test.go` adds `TestHandler_NilLogonsSubscriber_DoesNotPanic` — select-case does not fire, service runs normally.
  - `TestHandler_LogonsInitFailure_ContinuesWithDegraded` — inject a constructor fault, assert service runs with `logonsSrc = nil` and subsequent `HandleStatus` / audit rows carry `Degraded=true` (since `Logons.Health=Unavailable`).
- **Effort:** M
- **Covers:** C2 (ctx ordering), C5, P2
- **Round-1 addresses:** concerns #22 (hot-reload race — see Step 18 for restart semantics), #23 (nil-safe select case), #24 (InitCOMSecurityOnce placement — BEFORE evtSub, not on every subscriber restart)

### Step 18. Config surface for Logons tuning — safe hot-reload with generation fencing
- **Files touched:**
  - `config.go`
  - `internal/svc/handler.go` (pass config into subscriber, restart path)
- **Change:** Add `LogonsWMI` sub-struct on `Config`: `Enabled bool` (default true), `ReconcileIntervalSeconds int` (default 120, clamped 30–600), `DeathThresholdSeconds int` (default 360 = 3× default reconcile, clamped 60–1800). Subscriber consumes these via an `Options` struct passed to `NewLogonsSubscriber`.

  **Hot-reload path — safe restart (Round-1 concern #22):** when config reload detects `LogonsWMI` changes, the service loop performs the restart explicitly, NOT from inside a select-case handler. Sequence:
  1. Swap the service-loop's `logonsChanges` variable back to `nil` — the select case stops firing.
  2. Cancel the subscriber's parent ctx (a child-ctx created specifically for this subscriber instance from the service parent ctx).
  3. Wait up to 10 s for the subscriber's Done channel to close (it performs its Release + CoUninitialize synchronously per Step 5).
  4. Construct `NewLogonsSubscriber(newChildCtx, newOpts)`.
  5. On success, reassign `logonsChanges` to the new subscriber's `Changes()` channel AND swap `logonsSrc` for `svcRunCheck`.
  6. If construction fails, log WARN, leave `logonsChanges = nil` and `logonsSrc = nil` — service runs degraded.

  Critical invariants:
  - `svcRunCheck` never holds a reference to a subscriber that's been stopped (step 1 + atomic swap in step 5 guarantees this).
  - `InitCOMSecurityOnce` is NOT re-run — the `sync.Once` ensures process-wide COM security init happens exactly once regardless of how many times the subscriber is restarted (addresses concern #24).
  - The parent service ctx is never cancelled by this flow — only the per-subscriber child ctx.

  Shape mirrors `syncPerfCollector` in `handler.go` which already handles a similar swap for the perfmon collector on config reload.
- **Verification:**
  - `config_test.go` adds cases for clamp + defaults. Both `ReconcileIntervalSeconds` and `DeathThresholdSeconds` must clamp to their ranges.
  - Manual: edit `config.json`, bump reconcile interval, confirm:
    - Log shows `logons_subscriber=stopping reason=config_reload`, then `logons_subscriber=started`, within 12 s of file save.
    - Subsequent reconcile logs show the new interval.
    - No WMI handle leak after 10 reload cycles (`Get-Process drainctl | Select Handles` stable).
  - Manual: flip `Enabled=false` via config reload, confirm subscriber stops cleanly, Logons health goes `Unavailable`, effective mode falls through to registry-only (Degraded=true if no registry deny, Degraded=false if registry has authoritative signal).
- **Effort:** M (upgraded from S — the hot-reload safety is not trivial)
- **Covers:** C5, C7
- **Round-1 addresses:** concerns #22 (hot-reload race — atomic swap + nil channel before cancel), #24 (InitCOMSecurityOnce runs exactly once, restart path does not re-invoke)

---

## Phase 8 — Tests

### Step 19. Aggregator + shim test coverage
- **Files touched:**
  - `tsstate_windows_test.go` (new)
  - `registry_test.go` (extend)
- **Change:** Tests already sketched in steps 11, 12, 14. Target coverage for the new code paths ≥ 85 % — this code is pure (no COM) so high coverage is cheap and load-bearing.
- **Verification:** `go test -coverpkg=github.com/LISSConsulting/LISSTech.DrainCtl -run 'TestDrainSources|TestReadDrainMode' ./... -cover`.
- **Effort:** S
- **Covers:** C6, C8, C9

### Step 20. Subscriber adapter tests (no live COM) + integration tests on committed-pipeline matrix
- **Files touched:**
  - `internal/watcher/logons_subscriber_test.go` (new)
  - `internal/watcher/logons_subscriber_integration_test.go` (new, `//go:build windows && integration`)
  - `justfile` (add `just test-integration` recipe)
  - `CI documentation` (manual — `docs/CI.md` or README if applicable)
- **Change:** Split subscriber into "adapter" (COM-talking, minimal) and "core" (state machine, channel plumbing, backoff, source transitions, parse logic) so unit tests exercise core without COM. The unit tests live in `logons_subscriber_test.go`; they cover:
  - Step 3: `TestLogonsSubscriber_CoUninitializeOnlyWhenInitialized`
  - Step 5: `TestLogonsSubscriber_ReleasesOwnedPointersOnCtx`, `TestLogonsSubscriber_DoesNotDoubleReleaseLibraryObjects`
  - Step 7: `TestLogonsSubscriber_ParseValues`
  - Step 8: `TestLogonsSnapshot_HealthTransitions`, `TestLogonsSnapshot_QuietSystemStaysLive`, `TestLogonsSnapshot_BothPathsFailedGoesStale`
  - Step 9: `TestLogonsSubscriber_ReconnectBackoff`, `TestLogonsSubscriber_PollFailureTriggersReconnect`
  - Step 10: `TestLogonsSubscriber_ReconcileCatchesDrift`, `TestLogonsSubscriber_ReconcileZeroRows`, `TestLogonsSubscriber_ReconcileMultipleRows`

  COM itself is out-of-scope for unit tests — verified only via the build-tagged integration tests.

  **Integration tests — committed pipeline, not "excluded by default" (Round-1 concern #25):** the integration tests are the ONLY verification of C1/C2/C3 (COM lifecycle blockers) end-to-end. Leaving them off CI means those concerns revert to "manually verified before merge" which is fragile.

  - `just test-integration` recipe runs `go test -tags integration ./...` — separate from the default `just test`.
  - Document the required dev-machine setup in `docs/CI.md` (or README): Windows Server 2016+ RDSH role installed, LocalSystem service context OR `Run as administrator` PowerShell, `winmgmt` running. Any dev contributor running the integration tests locally must meet this.
  - **Commitment:** the Codex-writer workflow / release protocol (the user's `/kraken` release skill) must run `just test-integration` before tagging a release. This is a process commitment, not a code gate — but it closes the loop on C1/C2/C3 verification.
  - Integration tests from Step 6: `TestLogonsSubscriber_Live`, `TestLogonsSubscriber_ClosesOnCancelCleanly`.
- **Verification:**
  - `go test ./internal/watcher/... -run TestLogonsSubscriber` green on any dev machine (no RDSH required).
  - `just test-integration` green on a dev machine with RDSH role.
  - The release protocol document explicitly lists `just test-integration` as a pre-release gate.
- **Effort:** M
- **Covers:** C1, C2, C3, C5, C6, C7
- **Round-1 addresses:** concern #25 (integration tests not left off CI — elevated to release-gate commitment)

---

## Phase 9 — Docs

### Step 21. Update `BUGS.md` and `README.md` (commit-hash note moved to post-merge)
- **Files touched:**
  - `BUGS.md`
  - `README.md`
- **Change:**
  - `BUGS.md`: add entry for P1 (WinStationQueryInformationW follow-up) with context "evaluate if WMI path proves fragile in production (track via `wmi_logons=reconnect` log count)". **Do NOT reference a commit hash for C13 at plan-authoring time** — the commit doesn't exist yet. Post-merge, an operator with write access updates `BUGS.md` with the actual C13-fixing commit hash.
  - `README.md`: add "Logons tracking" subsection under `## 🏗️ Architecture` describing the three-source aggregator (`fDenyTSConnections`, `TSServerDrainMode`, `Logons`), the `reason` field in CheckResult, the `Degraded` bool semantics, and the Health-state signal (`Live / Stale / Unavailable`).

  Add a "Change attribution" subsection documenting:
  - Default: 4657 correlation for registry-side changes (`fDenyTSConnections` and `TSServerDrainMode` both covered after Step 15).
  - Optional: 4688 process-creation heuristic for `change logon` → Logons flips. Requires GPOs: "Audit Process Creation: Success" + "Include command line in process creation events". Best-effort — a 4688 event proves the operator LAUNCHED `change.exe`, not that the operation succeeded. Enable via `Audit.AttributionViaProcessCreation=true` in `config.json`.
  - When attribution is unavailable: `changed_by=""` on the audit row — same pattern as reconciliation rows.
- **Verification:** Markdown linter (if any runs in `just lint`). Manual read-through. Final `BUGS.md` update with commit hash happens as a small follow-up commit AFTER merge.
- **Effort:** S
- **Covers:** C10, C13, P1
- **Round-1 addresses:** concern #30 (commit hash moved to post-merge action)

---

## Out of scope / deferred

- **P1 — `WinStationQueryInformationW` alternative.** Documented in `BUGS.md` (step 21) as a follow-up. Would sidestep WMI entirely but is undocumented and Microsoft could drop it any Windows Server release. Keep as escape hatch; revisit only if WMI path shows instability in production telemetry (track via the `wmi_logons=reconnect` log count).
- **P2 — Startup-failure ordering.** Already handled by existing parent-ctx plumbing; step 17 explicitly follows the existing pattern (log warn + continue, ctx cancel unwinds everything). No separate step needed — called out in the step 17 verification.
- **CLI/DLL consumer changes for the new `reason` field.** The field is additive (JSON `omitempty`) — existing consumers keep compiling. A future pass can surface `reason` in the PowerShell module's `Get-DrainStatus` output and in `drainctl check` human-readable output; not blocking for correctness.
- **Dashboard UI column for `reason`.** The backend emits it (step 14), but adding it to the frontend table is out of scope — belongs on the Dashboard Table Redesign spec track.
- **Metrics row for Logons state.** Intentionally not writing Logons to `metrics_raw`: state is already captured via `DrainMode` transitions in the audit table, and `reason` tells you which source flipped. Adding a counter would duplicate information.

---

## Summary of synthesis-item coverage

| Item | Severity | Step(s) |
| --- | --- | --- |
| C1 (thread-locked CoInit) | Blocker | 3 |
| C2 (explicit teardown, not ctx) | Blocker | 3, 5, 17, 20 |
| C3 (security + proxy blanket) | Blocker | 4 |
| C4 (Logons is string) | High | 7 |
| C5 (WITHIN 2 is polling) | High | 10, 17, 18 |
| C6 (graceful degradation) | High | 8, 11, 13 |
| C7 (subscription death) | High | 9, 10, 18 |
| C8 (watcher race) | Medium | 13 (preserves existing invariant) |
| C9 (don't collapse sources) | High | 11, 12, 13, 14 |
| C10 (4688 attribution) | Low | 16, 21 |
| C11 (library choice) | Medium | 1, 6 |
| C12 (code organization) | Medium | 2, 11, 12 |
| C13 (4657 filter pre-existing bug) | Medium | 15 |
| P1 (WinStation alt) | Deferred | Out of scope |
| P2 (startup ordering) | Deferred | 17 (handled by existing pattern) |

**Total: 21 steps, estimated ~4–5 days of focused work.** Blocker steps (3, 4, 5) must land in a single PR — shipping any COM init code without full lifecycle correctness risks handle leaks and crashes on hardened hosts.
