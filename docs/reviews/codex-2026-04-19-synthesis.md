# Codex review synthesis — 006-evtspike-detection vs trunk

**Date**: 2026-04-19
**Scope**: 58 commits, ~8.7k LOC, 63 files on branch `006-evtspike-detection`
**Reviewers**: three parallel codex-0.121.0 agents under correctness / security / design lenses
**Synthesizer**: Claude Opus (Claude Code), verified each claim against the code

## Summary

15 codex claims → 9 CONFIRMED, 4 PARTIAL, 2 REJECTED. Two HIGH-severity blockers for ship-readiness: (1) `evtspike.enabled` hot-reload is broken in both directions, and (2) `EvtSubscribe` has no retry/state machine despite spec T098 asserting one. A third HIGH issue (SMTP without timeout stalls the service poll loop) pre-exists on trunk but is now more exposed because `event_spike` dispatches inline from the same select.

## CONFIRMED issues

### C1 — `evtspike.enabled` hot-reload broken both directions [HIGH]

- `internal/svc/handler.go:580` only creates `evtSpikeSub` if `EvtSpike.Enabled == true` at service start.
- `internal/svc/handler.go:837` `applyEvtSpikeConfigReload` returns early when `sub == nil`, so a runtime toggle `enabled: false → true` cannot construct a subsystem — restart required.
- `internal/evtspike/subsystem.go:164` `Reload` never inspects `newCfg.Enabled` and the field is not copied into `s.cfg` at line 191-198. `channelSetChanged` (line 209) compares `ResolveChannels` which doesn't depend on `Enabled`, so a toggle `true → false` keeps the scoring loop, persistence loop, subscriptions, and `OnSpike` callback alive. Only `Status()` at line 476 reads `s.cfg.Enabled` — and the stored value is never updated after Start.

**Failure mode**: dashboard says disabled while detectors keep scoring and notifications keep firing; hosts that started disabled need a full service restart to turn the feature on.
**Evidence**: read subsystem.go end-to-end; `s.cfg.Enabled` appears only at line 476 (status).

### C2 — No EvtSubscribe retry/state machine [HIGH]

- `internal/evtspike/subscriber_windows.go:30` `Subscribe` is called exactly once per channel from `initChannels` (subsystem.go:267). On failure, `initChannels` at line 268 logs `skipped` and moves on forever.
- The drain goroutine at `subscriber_windows.go:52` waits on `WaitForSingleObject(sigEvent, 1000)` and breaks out of the inner `EvtNext` loop on `r == 0 || returned == 0` (line 77). There is no handle re-creation, no retry cadence, no state transition reported back.
- spec.md T098 explicitly claims a test for "subscribed → retrying → failed" with 5-minute retry and 12-attempt cap. That infrastructure does not exist.

**Failure mode**: a provider that's briefly missing (service install/upgrade, manifest flush) permanently removes that channel from the watch list until the service restarts. Dashboard `mature_channels` never reflects the loss.

### C3 — SMTP without dial/read/write deadlines blocks service poll [HIGH, partially pre-existing]

- `email.go:288` uses `smtp.Dial(addr)` with no timeout.
- `email.go:315` uses `tls.Dial("tcp", addr, ...)` with no `net.Dialer` wrapping and no deadlines.
- `notify.go:231` `wg.Wait()` waits for every target's goroutine to complete.
- `internal/svc/handler.go:678` calls `dc.SendNotification(..., dc.TriggerEventSpike, ...)` synchronously from the same `select` that drives `regCh`, `configCh`, `pollTicker.C`.

**Failure mode**: one blackholed SMTP target stalls the service poll loop indefinitely. This class of bug pre-exists on trunk (alert/warning/perf triggers have the same path), but `event_spike` is now the third trigger dispatched inline, so the exposure grows.
**Narrower truth**: webhook/ntfy use a 5 s `httpClient` timeout — only SMTP is unbounded.

### C4 — `event_spike` cooldown recorded before delivery [MEDIUM]

- `notify.go:142` sets `state.LastSpikeNotify[target.URL][spikeKey] = now` before the dispatch goroutine runs.
- Send errors at `notify.go:189,213,224` are logged only; no rollback.

**Failure mode**: a transient webhook 500 / DNS blip consumes the per-target repeat interval for the (host, channel) pair. Next confirmed spike within `repeat_minutes` is silently dropped for that target. Same pattern exists for the non-event_spike branch at `notify.go:152` — mark as a regression on the `event_spike` path specifically because the code is newly added.

### C5 — Negative-binomial tail/quantile silently truncate at maxNBinIter=50000 [MEDIUM]

- `internal/evtspike/detector.go:173` loop condition `k < y-1 && k < maxNBinIter`: for `y > 50001`, cdf accumulates only up to `k = maxNBinIter - 1`. The returned `tail = 1 - cdf` over-estimates `P(Y ≥ y)`.
- `internal/evtspike/detector.go:204` quantile returns `maxNBinIter` when the 99th percentile is higher.

**Failure mode**: a genuine extreme flood (>~50 k events / 10 s bucket) reports a LARGER tail than reality → alert threshold (`tail < ε`) is not crossed → silent miss. For typical drain-mode channels this is unreachable; for Security floods (e.g. audit storm, brute force) it is plausible.

### C6 — `Security` channel bypass via `added_channels` combined with un-restored privilege [MEDIUM]

- `internal/evtspike/channels.go:135` appends `cfg.AddedChannels` AFTER the `Security` filter, with no opt-in check. The existing comment at line 107-109 acknowledges: "An explicit `Security` entry here is preserved even when the flag is off; subscription then fails at startup."
- But `internal/evtspike/privilege_windows.go:32` enables `SeSecurityPrivilege` and never disables it. Once the service has ever run with `security_channel_enabled: true`, the privilege stays enabled for the process lifetime.

**Bypass path**: admin enables the opt-in once → privilege enabled on token. Admin later sets `security_channel_enabled: false` AND puts `Security` in `added_channels`. Privilege is still on token → subscription succeeds despite the opt-in flag being false.
**Trust boundary**: config writer → Security log access. Honor-system, not cryptographic, but the opt-in language in the spec (FR-031) is stronger than the implementation enforces.

### C7 — `SeSecurityPrivilege` never restored after Security subscription disabled [LOW-MEDIUM]

Foundation of C6. Standalone it's defense-in-depth only (LocalSystem holds the privilege by default anyway); together with C6 it enables the bypass.

### C8 — Baseline file has no size cap, weak schema guard [LOW-MEDIUM]

- `internal/evtspike/baseline.go:118` `os.ReadFile` — unbounded.
- `internal/evtspike/baseline.go:129` `json.Unmarshal` — unbounded channels map.
- `internal/evtspike/baseline.go:135` only rejects `bf.SchemaVersion > SchemaVersion`; missing / zero / negative are accepted as valid.

**Attack model**: local-admin tamper with the baseline file. Exploit: oversized file → memory pressure; bogus Alpha/Beta values → detector state poisoning (though Go JSON rejects NaN/Inf, so exotic float injection is out). Mitigation: cap at e.g. 16 MB, require `SchemaVersion == SchemaVersion`, clamp per-field values post-load.

### C9 — Subscription goroutines not owned by `s.wg` [MEDIUM]

- `internal/evtspike/subsystem.go:127` `s.wg.Add(2)` for scoring + persistence only.
- `internal/evtspike/subscriber_windows.go:52` spawns its own goroutine on every `Subscribe` call. Not added to any wait group.
- `Stop` at `subsystem.go:145` returns after waiting for the two known goroutines and writing the baseline — subscription goroutines may still be waiting on `WaitForSingleObject(sigEvent, 1000)` for up to ~1 s.

**Failure mode**: during `restartWithConfig` (Stop → clear maps → Start), old subscription goroutines can still hold EvtSubscribe handles when the new Subscribe runs. Per-channel counters are new instances (different `atomic.Int64` pointers), so double-counting isn't possible, but handles overlap for ~1 s and a slow provider could push that higher. Not catastrophic.

## PARTIAL issues

### P1 — Notification URLs logged in full [LOW, pre-existing]

`notify.go:189,191,213,215,224` and `internal/dashboard/server.go:922` log `url=<full URL>`. ntfy tokens are commonly embedded in the URL path; leaking them to the file log (log_file_level=debug or warn+ on warnings) is a secret-exfil risk. Pattern pre-dates this branch — not introduced by evtspike work. Worth a hardening pass but outside branch scope.

### P2 — Dashboard `ServerState` couples to `internal/evtspike` types [LOW]

`internal/dashboard/store.go:39,43,48` adds three evtspike-typed callbacks (`OnEvtSpikeIngest`, `OnEvtSpikeStatus`, `RegisterEvtSpikeStatusFunc`). The existing `OnMetrics` uses `dc.CheckResult` (root package, not internal/). The new fields cross an additional internal-to-internal package import that a cleaner design would hide behind an interface in `internal/dashboard`. Style smell, not a bug.

### P3 — SC validation fixtures still placeholders [LOW, expected]

`specs/006-evtspike-detection/fixtures/normal-day.json` and `stress.json` are placeholder stubs. Tasks T094b / T094c depend on them. This is by design — Phase 9 tasks are human-gated on real hardware and pending. Not a ship blocker if Phase 9 is honored.

### P4 — Dead-weight / spec-referencing comments [LOW]

`internal/evtspike/subsystem.go:150-163` (14-line Reload docstring), `internal/evtspike/baseline.go:99-112` (4-case restatement), `frontend/src/lib/api.js:491`. Some explain WHY (live-reload matrix, data-model.md pointer) — keep. Others restate WHAT the code does — drop. Low severity.

## REJECTED claims

### R1 — "`SpikePayload` leaks into public `drainctl` package" [REJECTED]

`spike.go:7-16` explicitly documents: `SpikePayload` lives in root drainctl so `notify.go` can embed it without creating an import cycle with `internal/evtspike` (which imports drainctl for `EvtSpikeConfig`). The alternative — move `EvtSpikeConfig` into internal/evtspike — is impossible because root `Config` embeds it. This is a structural constraint of the existing package layout, not a leak.

### R2 — "`cmd/evtspike` is orphan POC code" [REJECTED]

`cmd/evtspike/main.go` is intentionally retained as a dev-only smoke harness. `specs/006-evtspike-detection/plan.md:29` documents this decision; `tasks.md:224` notes it's not packaged by the MSI; `justfile` correctly does not include it in `all`/`release`. Known, intentional, documented — not unreviewed dead code.

## Priority for remediation

| # | Severity | Finding | Why |
|---|----------|---------|-----|
| 1 | HIGH     | C1 — Enabled hot-reload broken | User-visible feature bug; both toggle directions wrong |
| 2 | HIGH     | C2 — No subscription retry/state machine | Spec T098 asserts it exists; shipping this without breaks the SC claim |
| 3 | HIGH     | C3 — SMTP no timeout, blocks poll | Pre-existing but event_spike adds third exposure point |
| 4 | MEDIUM   | C4 — Cooldown consumed on failure | Notification reliability regression |
| 5 | MEDIUM   | C6 — Security bypass via added_channels | Spec language stronger than enforcement |
| 6 | MEDIUM   | C9 — Subscription goroutines not in wg | Reload handle overlap, 1-s window |
| 7 | MEDIUM   | C5 — NegBin tail truncation | Silent miss on extreme floods |
| 8 | LOW-MED  | C7 — Privilege never restored | Foundation of C6; standalone is defense in depth |
| 9 | LOW-MED  | C8 — Baseline no size cap / weak schema | Hardening for local-admin tamper |
| 10 | LOW     | P1/P2/P3/P4 | Style & pre-existing hardening — defer |

## Verification notes

All line numbers spot-checked against `evtspike-poc/` worktree at commit `7aabd77` (branch tip). Codex line citations were accurate for every CONFIRMED finding except C5 (where codex said "less anomalous than they are" — correct for the alert-fires-when-tail-is-small semantic; I re-read the alert predicate path to verify). Codex hallucination rate on this review: 2/15 (R1 and R2; both rejected on structural/documentary grounds, not incorrect citations).
