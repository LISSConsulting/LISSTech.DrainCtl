# Remediation Plan — Codex Review 2026-04-19

Source: `docs/reviews/codex-2026-04-19-synthesis.md` (9 CONFIRMED + 4 PARTIAL + 2 REJECTED).
Branch: `006-evtspike-detection` (~58 commits, ~8.7k LOC, tip `7aabd77`).
Scope: Go code under `internal/evtspike`, `internal/svc`, `notify.go`, `email.go`, `internal/dashboard`; tasks.md updates in `specs/006-evtspike-detection/`; `spec.md` only where spec drift is adjudicated.

## 1. Summary

Thirteen findings to adjudicate. The work clusters into three phases gated by prerequisites, not severity:

- **Phase A — Reload-path refactor (C1, C7, C9, C6 partial).** A single coherent rewrite of `Subsystem.Reload` + `restartWithConfig` + subscription lifetime + privilege lifecycle. Almost all HIGH/MEDIUM items touching `internal/evtspike/subsystem.go` or `subscriber_windows.go` belong together because they share the same control-flow surface.
- **Phase B — Subscription state machine (C2).** The `subscribed → retrying → failed` state machine that spec T098 asserts exists. Builds on Phase A's goroutine-ownership changes.
- **Phase C — Independent fixes (C3, C4, C5, C8).** Hot spots that don't share code with A or B: SMTP timeouts (email.go), cooldown rollback (notify.go), negative-binomial ceiling (detector.go), baseline hardening (baseline.go).

The PARTIAL findings (P1–P4) are adjudicated individually at the end: three DEFER/ACCEPT, one bundled into Phase A. No item is silently dropped.

Ten FIX steps, one spec-decision step, five DEFER/ACCEPT dispositions. Grand total effort ≈ **L** (two to three focused days for one author once the tasks.md entries are approved).

## 2. Ordering & dependencies

- **Phase A must land first.** Phase B's channel-level state transitions (subscribed/retrying/failed) rely on (a) the new `Reload` path actually propagating `Enabled`, (b) subscription goroutines being owned by a wait group so the retry goroutine can be shut down, and (c) privilege-restoration plumbing to cleanly Stop/Start on Security toggle.
- **Within Phase A**, Step A1 (wait-group ownership) is foundational. Steps A2–A4 layer on top of it. Do them in the order listed — A1 changes the subsystem struct and the subscribe goroutine signature; A2 consumes that infrastructure; A3 and A4 sit on the fully-plumbed lifecycle.
- **Phase B consumes all of Phase A.** Do not start B1 until A1–A4 are merged or on the same branch.
- **Phase C is independent** and can be picked up by a second author in parallel with A or B.
- **Spec alignment (C2) — human decision.** C2 has two remediation shapes: implement the state machine the spec claims exists, OR reduce spec FR / T098 to match reality (log-and-skip forever). This plan assumes the first choice; flag for human confirmation before executing B1.

## 3. Phase A — Reload-path refactor

Commit direction: `feat(evtspike): own subscription lifecycle end-to-end`.

### Step A1 — Subscription goroutines owned by `s.wg` (C9)

- **Files**: `internal/evtspike/subscriber_windows.go`, `internal/evtspike/subsystem.go`.
- **Change**: `Subscribe` grows a `*sync.WaitGroup` parameter. **The caller (Subscribe, synchronously on the caller's goroutine) executes `wg.Add(1)` BEFORE `go func() { defer wg.Done(); ... }()` — never inside the goroutine.** This avoids the classic `Add`-after-`Wait` race. `Subsystem.initChannels` passes `&s.wg` to every `Subscribe` call. `Subsystem.Stop` continues to cancel context, and `s.wg.Wait()` now also blocks on every subscription goroutine — not just the two internal loops.
- **Verification**: unit test `TestSubscribe_GoroutineOwnedByWaitGroup` in a new `internal/evtspike/subscriber_windows_test.go`. Use a stub `SubscribeFunc` that blocks on a channel the test controls; assert `Stop` returns only after the stub returns. Companion test `TestStop_WaitsForSubscriberGoroutines` at the `Subsystem` level: inject a `SubscribeFunc` that holds a goroutine in a 2-second sleep after ctx cancel; assert `Stop` blocks until that goroutine exits (≤3 s deadline).
- **Risk**: a stuck subscription (provider hang inside `EvtNext`) now blocks `Stop` for up to the `WaitForSingleObject` timeout. Already the 1-s worst case; explicitly document it.
- **Rollback**: revert the `*sync.WaitGroup` parameter and the two-line add/done. Goroutine leak returns but functional behavior is unchanged.
- **Effort**: S.

### Step A2 — Honor `Enabled` in Reload, both directions (C1)

- **Files**: `internal/evtspike/subsystem.go`, `internal/svc/handler.go`.
- **Change**: four parts. The disabled-at-startup path is the subtle one — the current `Start` captures `s.startCtx` and the current `Reload/restartWithConfig` reads it, so "never call Start when disabled" breaks the subsequent `false→true` reload. Resolve by making Start idempotent w.r.t. `Enabled`.
  1. In `handler.go` around line 580, always construct `evtSpikeSub = evtspike.New(fullCfg.EvtSpike, host)` **unconditionally**. Also allocate `spikeCh = make(chan dc.SpikePayload, 16)` **unconditionally** — the current code creates `spikeCh` only inside `if Enabled`, which means `OnSpike` would close over a nil channel after a `false→true` reload and every spike would hit the `default:` branch of the select and be dropped silently. Then wire `OnSpike`, `OnStatusChange`, and `RegisterEvtSpikeStatusFunc` **immediately after New regardless of `Enabled` state** (so a later `false→true` Reload does not trip the existing `OnSpike == nil` check at subsystem.go:108). Finally call `evtSpikeSub.Start(ctx)` **unconditionally**. `applyEvtSpikeConfigReload` around line 837 drops the `sub == nil` early-return.
  2. In `subsystem.go` `Start`, if `s.cfg.Enabled == false`, capture `s.startCtx = ctx`, set `s.cancel = nil`, and return nil **without** launching subscriptions, scoring, or persistence goroutines. This preserves the parent-context capture required by Reload and keeps `OnStatusChange(Status{State: "disabled"})` correct if non-nil.
  3. In `subsystem.go` `Reload`, add an `Enabled` diff as the first check: old=true→new=false invokes the quiescing portion of `Stop` (cancel scoring/persistence, wait on `s.wg`, optionally final `writeBaseline`) and updates `s.cfg = newCfg`; old=false→new=true reuses `s.startCtx` to launch subscriptions + scoring + persistence. Factor the start/stop bodies so Reload does not re-enter `Start`/`Stop` through their public signatures (which would double-cancel the parent context).
  4. `channelSetChanged` and the rest of Reload remain gated on `s.cfg.Enabled == newCfg.Enabled == true`.
- **Verification**: three tests in `subsystem_test.go`.
  - `TestReload_EnabledFalseStopsScoringLoop`: start subsystem, observe one scoring tick, call `Reload(Enabled=false)`, assert within 1 s (a) `Status().State == "disabled"`, (b) a counter increment produces no `OnSpike`, (c) `OnStatusChange` fires with state `disabled`.
  - `TestReload_EnabledTrueStartsFromDisabled`: construct with `Enabled=false`, call Start once (which should be a no-op in that mode), then call `Reload(Enabled=true)` and assert Start-equivalent behavior: a spike-inducing counter pattern fires `OnSpike`.
  - `TestReload_EnabledToggleFromSvcWiring`: drive via `applyEvtSpikeConfigReload` to cover the handler.go path.
- **Risk**: storing `parent` context at `New` changes the shutdown contract — the service's top-level ctx must outlive any `Reload` window. Already the case in practice; document it.
- **Rollback**: revert the three changes; enabled-toggle-requires-restart is the pre-patch behavior.
- **Effort**: M.

### Step A3 — Restore `SeSecurityPrivilege` on Security disable (C7) and close `Security` bypass (C6)

- **Files**: `internal/evtspike/privilege_windows.go`, `internal/evtspike/subsystem.go`, `internal/evtspike/channels.go`.
- **Change**: three parts.
  1. Add `DisableSecurityPrivilege()` to `privilege_windows.go`, symmetric with `EnableSecurityPrivilege` but passing `0` (not `SE_PRIVILEGE_ENABLED`) in `AdjustTokenPrivileges`. Returns same error types.
  2. In `subsystem.go` `restartWithConfig`, on the Stop side, if the old config had `SecurityChannelEnabled == true` and the new config has `SecurityChannelEnabled == false`, call `DisableSecurityPrivilege()` after Stop and log the outcome. Do the same in the `Enabled=true→false` path added in A2 (so a full disable also drops the privilege).
  3. **No change to `ResolveChannels`.** An earlier draft proposed filtering `Security` out of `AddedChannels` when the opt-in flag is false, but that would contradict the current `channels.go:107-109` comment AND the documented behavior in `spec.md`, `data-model.md`, and `contracts/evtspike-config.md` ("`AddedChannels=Security` preserved when flag off; subscription fails at subscribe time per FR-009"). The bypass is closed entirely by part 2 — once `DisableSecurityPrivilege` runs on opt-out, a subsequent `AddedChannels=Security` subscription attempt fails with `ErrPrivilegeNotAssigned` and is logged as skipped. No spec/docs changes required.
- **Verification**:
  - `TestResolveChannels_AddedSecurityPreservedWithFlagOff`: `SecurityChannelEnabled=false` + `AddedChannels=["Security"]` → result still contains `Security` (behavior preserved per spec contract); the runtime protection is the privilege, not the channel list.
  - `TestReload_SecurityDisableRestoresPrivilege`: swap a mock `PrivilegeFunc` that records enable/disable calls; `Reload(SecurityChannelEnabled=false)` after `Start(SecurityChannelEnabled=true)` must record exactly one disable call after restart.
  - `TestReload_AddedChannelsSecurityAfterDisableFails`: start with `SecurityChannelEnabled=true`, reload to `false`, then reload with `AddedChannels=["Security"]`. Assert the Subscribe call for `Security` fails with `ErrPrivilegeNotAssigned` via the mock; assert the channel is logged as skipped and not counted in `Status.EnabledChannels`.
  - **No direct test of `DisableSecurityPrivilege` against the real process token** — that test would mutate the running process's privilege state and behave differently on LocalSystem / admin dev / standard-user CI runners. Unit coverage flows through the `PrivilegeFunc` injection point; if a real-token sanity check is desired, gate it behind a `//go:build integration` tag and run it only in the signed-build pipeline.
- **Risk**: `DisableSecurityPrivilege` failure leaves the privilege enabled. Log at WARN, do not fail Reload. Standalone C7 is LOW-MEDIUM; the real protection comes from (3) — the `AddedChannels` filter.
- **Rollback**: revert all three parts. Bypass returns but privilege-leak risk is only defense-in-depth.
- **Effort**: M.

### Step A4 — Same-file cleanup: Reload docstring and baseline dead comments (P4)

- **Files**: `internal/evtspike/subsystem.go` (lines 150–163), `internal/evtspike/baseline.go` (lines 99–112).
- **Change**: trim both doc comments to the WHY-only portion. Keep the live-reload matrix pointer in subsystem.go; drop the line-by-line field restatement. Keep the four-case data-model.md pointer in baseline.go; drop the restatement of WHAT each case does (already visible in code).
- **Verification**: `golangci-lint` clean; `go vet` clean; no test needed.
- **Risk**: minimal — comments only.
- **Rollback**: git revert.
- **Effort**: S.

## 4. Phase B — Subscription state machine

Commit direction: `feat(evtspike): add per-channel retry/state machine per T098`.

### Step B1 — Implement `subscribed → retrying → failed` per channel (C2)

- **Files**: `internal/evtspike/subscriber_windows.go`, `internal/evtspike/subsystem.go`, `internal/evtspike/status.go`, `internal/dashboard/server.go` (status response shape only).
- **Change**: introduce a per-channel state carried in a new struct on the `Subsystem` (`map[string]*channelSubscription` under `s.mu`). States per spec.md Edge Cases: `subscribed`, `retrying`, `failed`. Change the `SubscribeFunc` signature so it reports **asynchronous** subscription loss: the drain goroutine must call back into the subsystem on exit-due-to-error, not just on successful-context-cancel. Concretely, `Subscribe` grows a `loss func(err error)` callback OR returns an `<-chan error` the subsystem can select on from the supervisor loop. When the drain goroutine's `EvtNext`/`WaitForSingleObject` returns an error indicating the subscription is gone (handle invalidated, provider unregistered), it closes the handle, calls `loss(err)`, and exits. The subsystem's supervisor goroutine (one per subsystem, not per channel) then transitions the channel to `retrying` and retries `Subscribe` every 5 minutes for up to 12 attempts (1 h) before moving to `failed`. Returned to `subscribed` on successful retry; baseline state is preserved across transitions because detectors and baselines live on `Subsystem`, not on the subscription struct. **Status accounting**: `Status.EnabledChannels` and `Status.MatureChannels` must BOTH reflect only channels currently in state `subscribed`. Specifically: replace the existing `len(s.channels)` read at `subsystem.go:477` with a count of `channelSubscription` entries whose state is `subscribed`; replace the mature-count loop at `subsystem.go:461-466` with the same subscribed-only filter. `DeriveState` at `subsystem.go:476` consumes both counts, so the dashboard pill will correctly transition to degraded when channels drop out. All retry-loop and drain goroutines participate in `s.wg` (built on A1). Emit `detector_status` SSE on transitions.
- **Verification**:
  - `TestSubscription_TransitionsToRetryingOnAsyncLoss` in `subsystem_test.go`: inject a stub `SubscribeFunc` that completes the initial subscribe, then after a test-triggered signal invokes its `loss` callback with a simulated error. Assert the subsystem transitions that channel to `retrying` within one supervisor tick without requiring a second Subscribe call first. This exercises the primary failure mode (a live subscription dying mid-run) that T098 is built to test.
  - `TestSubscription_TransitionsToRetryingOnInitialFailure`: retained from prior draft — `SubscribeFunc` returns an error on first call; assert state=`retrying` immediately after `initChannels`.
  - `TestSubscription_TransitionsToFailedAfterTwelveAttempts`: as above, failing on every call; after 12 × 5-min ticks, state=`failed` and no further retries are attempted.
  - `TestSubscription_RecoversToSubscribed`: `retrying` state; next retry succeeds; assert state=`subscribed`, baseline state unchanged, `OnStatusChange` called exactly once per transition.
  - `TestStatus_MatureChannelsExcludesRetrying`: two channels, one mature but in `retrying`, one mature in `subscribed`; `Status().MatureChannels == 1`.
  - `TestStatus_EnabledChannelsExcludesNonSubscribed`: three channels — one `subscribed`, one `retrying`, one `failed`; `Status().EnabledChannels == 1`. Without this, `DeriveState` keeps reporting `healthy` while channels are silently dropping out.
- **Risk**: retry cadence needs a fake clock to keep tests fast. Use the existing `PersistTickSource` pattern: inject a `RetryTickSource`. Risk of retry storms if many channels fail at once — mitigate by spacing retries with a small jitter (stdlib `math/rand`, no new dep).
- **Rollback**: remove the supervisor goroutine and revert `Status`; subscription failure reverts to "log and skip forever." Leaves C2 unfixed, other phase-A work intact.
- **Effort**: L.

### Step B2 — Decide: implement-spec or relax-spec (human gate)

- **Files**: `specs/006-evtspike-detection/spec.md` (Edge Cases paragraph), `specs/006-evtspike-detection/tasks.md` (T098 wording).
- **Spec drift**: spec asserts behavior (Edge Cases line beginning "An event channel becomes unavailable mid-run") that the implementation lacks. **Recommendation: implement as in B1 — the behavior is cheap, matches operator expectation, and T098 already depends on it.** Alternative: reduce the Edge-Cases paragraph and T098 to "log-and-skip; operator must restart the service to rejoin a lost channel." User must pick before B1 starts.
- **If implement (default)**: no spec edit needed beyond confirming T098's wording matches the supervisor cadence (5-min × 12 attempts is already in the spec; this plan inherits it verbatim).
- **If relax**: drop B1 entirely; replace with a one-paragraph spec edit in `spec.md` and a T098 rewrite in `tasks.md` stating that mid-run channel loss requires service restart.
- **Verification**: N/A (a decision, not a code step). If relax, run `go vet` + `golangci-lint` clean and add a new SC-validation skip note in quickstart.md.
- **Risk**: going with relax ships a spec that already said "retry every 5 min" to operators who read it on 2026-04-17. Flag this to the branch author before choosing.
- **Rollback**: trivial — pick the other option.
- **Effort**: S (decision only; cost of each branch captured above).

## 5. Phase C — Independent fixes

Commit direction: `fix(notify,evtspike): timeout SMTP, protect cooldown, harden bounds`.

### Step C1 — SMTP dial/read/write deadlines (C3)

- **Files**: `email.go`.
- **Change**: both `sendSMTPStartTLS` (line 288) and `sendSMTPS` (line 315) use a `net.Dialer{Timeout: 10*time.Second}` to dial. **Critical ordering**: call `conn.SetDeadline(time.Now().Add(30*time.Second))` **immediately after the successful `Dial`/`tls.Dial` and BEFORE `smtp.NewClient`** — `smtp.NewClient` internally reads the server greeting, and a silent-accept server blocks there unless a read deadline is already armed. For STARTTLS: `conn, err := d.Dial("tcp", addr)`, `conn.SetDeadline(...)`, then `smtp.NewClient(conn, host)`. For implicit TLS: `conn, err := tls.DialWithDialer(&d, "tcp", addr, cfg)`, `conn.SetDeadline(...)`, then `smtp.NewClient(conn, host)`. Pull the two timeouts into package-level `const`s so a future config knob is one line away (out of scope now; don't add the config).
- **Verification**:
  - `TestSMTPStartTLS_DialTimeout` in `email_test.go`: `net.Listen` socket that accepts but never sends a greeting (blackhole); assert `sendSMTPStartTLS` returns a `net.Error` matched via `var ne net.Error; errors.As(err, &ne) && ne.Timeout()` within 40 s (10 s dial + 30 s read deadline budget; do NOT assert the concrete type `*net.OpError` — the stdlib `smtp` package wraps it via `textproto`).
  - `TestSMTPStartTLS_WriteDeadline`: mock server that completes greeting + STARTTLS, then stops reading; assert send returns timeout within 35 s.
  - Repeat both for `sendSMTPS`.
  - **Test-speed knob**: introduce test-only hooks (`var smtpDialTimeout = 10*time.Second; var smtpOverallDeadline = 30*time.Second`) that tests can replace with 100 ms / 500 ms using `t.Cleanup` to restore. Without this the suite grows by ~3 minutes.
- **Risk**: slow mail relays (genuine 15-s delivery) will now fail. 30 s is generous; defensible.
- **Rollback**: revert. Poll-loop stall risk returns.
- **Effort**: S.

### Step C2 — Roll back `LastSpikeNotify` on send failure (C4)

- **Files**: `notify.go`.
- **Change**: in the `event_spike` branch around lines 129–142, don't mutate `state.LastSpikeNotify[target.URL][spikeKey] = now` before dispatch. Instead: capture `prev, hadPrev := m[spikeKey]`, set `now` optimistically, then in the dispatch goroutine for each of webhook/ntfy/email, if the send fails, acquire the NotifyState mutex and either restore `prev` (if `hadPrev`) or `delete(m, spikeKey)` (if not).
  - **Mutex scope**: add a `sync.Mutex` to `NotifyState` that protects `LastSpikeNotify` **for every read AND write** during `SendNotification`, not just the failure path. Rationale: with goroutines dispatched in the loop, a dispatch goroutine for target T1 can restore a key while the main `SendNotification` loop is concurrently writing T2's key — same map, no sync → data race. Protect the initial `m[spikeKey]` read, the optimistic write, the rollback write, and the `state.LastSpikeNotify[target.URL] == nil` initialization. Loop-body logic stays single-goroutine; the mutex only matters when dispatch goroutines write back.
  - Narrower alternative considered and rejected: "don't set cooldown until after send." That introduces a race where two concurrent confirmations double-fire. The rollback approach preserves the fast-path dedup.
- **Verification**:
  - `TestSendNotification_SpikeCooldownRolledBackOnWebhookFailure` in `notify_test.go`: mock webhook returns 500; assert `state.LastSpikeNotify[url][key]` equals the pre-call value (or is unset if it was unset) after `wg.Wait()`.
  - `TestSendNotification_SpikeCooldownConsumedOnSuccess`: mock webhook returns 200; assert the timestamp is set.
  - `TestSendNotification_MultiTargetOneFailsOneSucceeds`: cooldown for the failing target rolls back; the succeeding target's cooldown is consumed.
- **Risk**: the retry-after-failure now fires through at the next confirmation inside the repeat window, creating more traffic than before if a target is persistently broken. Acceptable — a broken target was already re-attempted per confirmation pre-A2; the `state.LastSpikeNotify` rollback restores that behavior for event_spike specifically.
- **Rollback**: revert the mutex + rollback logic; pre-patch cooldown-eats-failure behavior returns.
- **Effort**: S.

### Step C3 — Remove NegBin soft cap by raising iteration limit + robust-cap preservation (C5)

- **Files**: `internal/evtspike/detector.go`, and the call site that consumes `negBinQuantile` (grep for it).
- **Change**: two paths. **The original plan's "geometric-tail upper bound `pmf / (1-q)`" is unsafe** — the PMF ratio `((k+α)/(k+1)) · q` is strictly greater than `q` for `α > 1` at any finite `k`, so that bound under-estimates residual mass and yields an over-small tail. Replace with the simpler-and-safer "raise the cap."
  1. `negBinUpperTail` (lines 173–184): raise `maxNBinIter` from 50_000 to 10_000_000 (or compute an explicit upper-bound survival using `math/big.Float` / log-sum-exp if the perf hit matters — it won't for normal-use counts). Even 10 M iterations at ~50 ns each is ≤0.5 s on the hot path, and we only reach it on extreme floods. Add an early-exit when successive `pmf` values fall below `1e-300` so underflow short-circuits cleanly. The existing `logPMF0 < -700` guard at line 167 already handles the degenerate-prior case.
  2. `negBinQuantile` (lines 204–211): do NOT return `math.MaxInt32` on loop overflow — that value is consumed by the robust-cap / flood-suppression path, and treating the bucket as "unbounded" disables the very protection against baseline poisoning that the robust cap exists for. Instead: if the loop exits at the (new, raised) cap, return the last `k` evaluated AND set a second return value or error-sentinel the caller can detect. The call site MUST, on detecting the sentinel, treat the observation as "extreme flood — cap at the robust threshold" and refuse to update the baseline with the full count for that bucket (FR-009's flood-poisoning protection). Log a WARN at most once per (host, channel) per hour.
- **Verification**:
  - `TestNegBinTail_ExtremeCountDoesNotUnderflow` in `detector_test.go`: construct `(α, β)` whose 99th percentile is > 50000; observe `y = 100_000`; assert returned tail < `1e-10` and is a valid float (not NaN, not Inf).
  - `TestNegBinQuantile_ExtremeCapSignalsOverflow`: same parameters with an α > 1 choice; assert the new sentinel/error is set rather than a silent `math.MaxInt32`.
  - `TestDetector_FloodingAboveSoftCapStillFlags`: end-to-end observe 200_000 on a mature slot; assert `ObserveBucket` returns `Alert == true`.
  - `TestDetector_FloodDoesNotPoisonBaseline`: observe 1_000_000 on a previously stable mature slot; after alert, assert the GammaState's Alpha/Beta values haven't been shifted by more than the robust cap would allow. This locks the FR-009 protection the original plan's `math.MaxInt32` would have broken.
- **Risk**: raised cap slows the rare pathological bucket; bounded above by ~0.5 s before the early-exit underflow kicks in. No correctness regression.
- **Rollback**: revert; silent miss returns.
- **Effort**: S.

### Step C4 — Baseline size cap and strict schema check (C8)

- **Files**: `internal/evtspike/baseline.go`.
- **Change**: at `LoadBaseline` (line 118), enforce the 16 MB cap in a way that can distinguish "at-cap" from "truncated" — a plain `io.LimitReader(f, 16<<20)` cannot. Either `os.Stat` the file first and reject if `Size() > 16<<20`, **or** read via `io.LimitReader(f, (16<<20)+1)` + `io.ReadAll` and reject when `len(data) == (16<<20)+1`. Use the stat approach unless atomic-rename races are a concern; stat is simpler and the file is owned by the service. On oversize: rename to `.oversize-<ts>.bak` and return fresh. Tighten the schema guard at line 135 to `bf.SchemaVersion != SchemaVersion` (reject zero, negative, and future alike; all three get `.incompat-*.bak` treatment). Clamp post-load per-channel `Alpha`/`Beta` to `[0.001, 1e9]` and `N` to `[0, 1e9]` with a WARN if any value is clamped; this prevents state poisoning via file tamper.
- **Verification**:
  - `TestLoadBaseline_OversizeFileIsRenamed`: write a 17-MB file, call LoadBaseline, assert fresh returned + `.oversize-*.bak` exists.
  - `TestLoadBaseline_ZeroSchemaVersionIsRenamed` and `TestLoadBaseline_NegativeSchemaVersion…`: both should now be rejected.
  - `TestLoadBaseline_ClampsOutOfBandFloats`: write a baseline with `Alpha=-5` and `Beta=1e20`; assert loaded values are clamped and a WARN is logged.
- **Risk**: tightening schema rejection breaks a pre-v1 baseline if one ever shipped. Version is currently 1 and this is the first schema; no upgrade path exists yet.
- **Rollback**: revert the three checks; attacker-lite exposure returns.
- **Effort**: S.

## 6. PARTIAL dispositions

- **P1 (URLs logged in full)** → **DEFER**. Pre-existing, touches `notify.go` + `internal/dashboard/server.go`, orthogonal to 006. Treat as a dedicated hardening follow-up; do not hold branch merge on it.
- **P2 (dashboard types coupling to internal/evtspike)** → **ACCEPT**. Intentional: the dashboard is the canonical consumer of evtspike status types. Refactoring behind an interface now is premature abstraction. Revisit if a second evtspike-like subsystem ships.
- **P3 (SC fixture placeholders)** → **ACCEPT**. Phase 9 SC validation (tasks.md T094b/T094c) explicitly gates on real hardware. Placeholders are correct; pair fixtures land with those tasks.
- **P4 (dead-weight comments)** → **FIX** in Step A4 (bundled).

## 7. Rejected claims

- **R1 (SpikePayload in public drainctl)** — structural constraint, not a leak. No action.
- **R2 (`cmd/evtspike` orphan)** — documented dev-only smoke harness. No action.

## 8. Tasks.md integration

New task IDs to insert in `specs/006-evtspike-detection/tasks.md`. Existing Phase 9 reserves T099+; use that range. Do not re-use the T047–T058 or T072–T082 gaps (documented as DROPPED).

| New ID | Phase | Step | One-line description |
|---|---|---|---|
| T099 | 9 | A1 | Audit `Subscribe` to accept `*sync.WaitGroup`; subscription goroutines join `s.wg`; add `TestStop_WaitsForSubscriberGoroutines`. |
| T100 | 9 | A2 | `Subsystem.Reload` honors `Enabled` in both directions; `handler.go` always constructs `evtSpikeSub`; drop `sub == nil` early return. |
| T101 | 9 | A2 | `TestReload_EnabledFalseStopsScoringLoop` + `TestReload_EnabledTrueStartsFromDisabled` + `TestReload_EnabledToggleFromSvcWiring`. |
| T102 | 9 | A3 | Add `DisableSecurityPrivilege`; call on Security-opt-out and on full disable; filter `Security` from `AddedChannels` when flag is off. |
| T103 | 9 | A3 | `TestResolveChannels_SecurityInAddedChannelsRequiresOptIn` + `TestReload_SecurityDisableRestoresPrivilege` + `TestPrivilege_DisableHandlesNotAssigned`. |
| T104 | 9 | A4 | Trim `Subsystem.Reload` docstring and `LoadBaseline` WHAT-restatement comments (WHY only). |
| T105 | 9 | B2 | **Human decision, gates T106+T107**: keep T098 as-is (implement state machine) OR reduce to log-and-skip. Capture outcome in a short `spec.md` edit if the latter. |
| T106 | 9 | B1 | Per-channel `subscribed/retrying/failed` state machine with 5-min × 12-attempt supervisor; wire into `Status` and SSE emission. Requires T105 = "implement." |
| T107 | 9 | B1 | Four-test suite for B1 transitions: `TransitionsToRetryingOnAsyncLoss`, `TransitionsToRetryingOnInitialFailure`, `TransitionsToFailedAfterTwelveAttempts`, `RecoversToSubscribed`, plus `MatureChannelsExcludesRetrying`. |
| T108 | 9 | C1 | `email.go` STARTTLS + SMTPS: `net.Dialer{Timeout: 10s}` + `SetDeadline(30s)`; plus blackhole and slow-server tests. |
| T109 | 9 | C2 | `notify.go` roll back `LastSpikeNotify` on send failure; add `sync.Mutex` to `NotifyState`; add three rollback tests. |
| T110 | 9 | C3 | Cap `negBinTail` via log-domain bound OR raise `maxNBinIter` to 1e6; `negBinQuantile` returns sentinel at cap; rate-limited WARN; three tests. |
| T111 | 9 | C4 | `LoadBaseline` 16 MB cap, strict `SchemaVersion == SchemaVersion` check, post-load float clamp; three tests. |

Parallelization: T099, T100 must be sequential (same files). T102, T104 [P] with T100. T105 blocks on T099–T103. T108, T109, T110, T111 are all [P] with each other and with any of A1–A4 once file ownership is known. Every new task is still under `[Story]: US1` or `[Story]: US5` (reload path) — pick by which user-story line the behavior sits on; suggested defaults: T099–T106 = `[US5]` (reload/live-reconfigure), T108–T111 = `[US1]`.

## 9. Risks & interactions

- **A2 + B1 share `Subsystem` state and the Reload code path.** Merge them into one PR if possible; separate PRs require rebasing the second on the first. If split, do A2 first.
- **A3's `DisableSecurityPrivilege` must land with the `AddedChannels` filter.** Landing the filter alone would make valid admin configs (that previously relied on AddedChannels=Security with SecurityChannelEnabled=false as a deliberate override) stop working — but that config is exactly the bypass the spec disallows, so the change is correct. Call this out in release notes.
- **C2 (cooldown rollback) assumes `NotifyState` is only mutated from one goroutine.** The current code confirms this (see handler.go:575 comment). Adding the narrow mutex preserves correctness even if a future caller breaks that invariant. If a future caller DOES break that invariant, the mutex is the guard.
- **B1 retry storm.** Add a small per-channel jitter (±30 s) so 54 channels losing their provider simultaneously don't hammer at t+5, t+10, … minutes in lockstep.
- **Pre-commit hygiene.** Every step above must pass `just lint` (gofmt + go vet + golangci-lint) and `gitleaks`. No `--no-verify`.

## 10. Effort total

- Phase A: S + M + M + S → **M+**
- Phase B: L + S → **L-**
- Phase C: 4 × S → **M**
- **Grand total ≈ L** (two to three focused days for one author; one day if A and C parallelize across two authors and B is scheduled for the following day).
