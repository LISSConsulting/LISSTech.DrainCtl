# Codex Review Synthesis — spec 006-evtspike-detection

**Date**: 2026-04-16
**Branch**: 006-evtspike-detection
**Scope**: Pre-implementation review of `specs/006-evtspike-detection/**` (12 files, 2099 lines). POC at `cmd/evtspike/` not under review.
**Reviewers**: 3 parallel codex sessions under lenses A (rigor/testability), B (feasibility/risk), C (completeness/consistency).
**Verification**: Every CONFIRMED claim below was checked against the actual spec files. Claims that codex raised but could not be verified are classified REJECTED.

---

## Legend

- **BLOCKER** — hard contradiction or silently-dropped requirement; must be fixed before impl starts or implementers will produce incompatible code.
- **HIGH** — real architectural/design concern; defer at your own risk.
- **MEDIUM** — genuine gap but not likely to block ship; worth closing.
- **REJECTED** — claim was hallucinated, out of scope, or already handled elsewhere.

---

## BLOCKERS (6)

### B-1 · Window length contradiction in two contracts
**Files**: `contracts/pipe-spike-message.md:33-39`, `contracts/dashboard-sse-events.md:48-54`
Both examples use `window_start: 14:22:42` → `window_end: 14:23:42` (60-second window) and `first_seen_at: 14:20:42` (2 min before window_start). This violates the hard invariants in `data-model.md:146-147`: `WindowEnd == WindowStart + 10s` and `FirstSeenAt <= WindowStart`. The third contract (`event_spike-payload.md:29-35`) is correct (10s window, first_seen_at 10s before). Implementers copying the pipe/dashboard examples will produce payloads that fail their own validation.
**Severity**: blocker — corrupts two of the three JSON contracts.
**Fix direction**: rewrite both examples with 10s windows (e.g., `14:22:42 → 14:22:52`) and first_seen_at within the 3-window confirmation range (`14:22:32` is the earliest sensible value).

### B-2 · Spec/contract contradiction: FR-017 "without losing spikes" vs "drop this spike"
**Files**: `spec.md:111` (Edge Case), `contracts/pipe-spike-message.md:72`
Spec Edge Case says the standalone CLI "reconnects on the next spike **without losing in-flight spikes already emitted**." The pipe contract's failure-modes table says on pipe write timeout: "**Drop this spike**; continue detection. Next spike will attempt reconnect (FR-017)." Directly incompatible observable behaviors. FR-017's own text also doesn't specify any durable-retry guarantee.
**Severity**: blocker — implementers don't know whether to add a retry queue or drop.
**Fix direction**: pick one. Recommend: update spec Edge Case to "reconnects on the next spike; spikes that failed to forward during disconnect are written to the local log (FR-016)." The contract is already self-consistent; the spec's prose is the looser end.

### B-3 · tasks.md Independent Test for US1 contradicts the detection model
**Files**: `tasks.md:79`
Phase 3 Independent Test says "No notification fires on a single transient burst that **lasts <60 s**." The detector uses 10s buckets with 2-of-3 confirmation, so any burst covering 3 consecutive anomalous buckets (~20–30s) should alert. A test written literally to this spec would reject the detector's correct behavior on 20–50s sustained bursts.
**Severity**: blocker — the MVP acceptance test would fail on correct implementations.
**Fix direction**: reword to "No notification fires on a single transient burst lasting **one 10-second scoring bucket or less** (2-of-3 confirmation suppresses one-shot transients)." Matches spec.md US1 Acceptance Scenario 2 and SC-003.

### B-4 · Baseline-write I/O budget off by 130×
**Files**: `plan.md:36`, `research.md:14`
`plan.md` Performance Goals: "a day of writes is **<0.1 MB/day churn**." `research.md` R1 rationale: "96 writes/day is **~13 MB/day of churn**." (135 KB × 96 = 12.96 MB.) Pick one; the perf budget for baseline I/O currently has two contradictory targets two orders of magnitude apart.
**Severity**: blocker — SC-007 validation has no pass/fail target on I/O.
**Fix direction**: update `plan.md` to match `research.md`'s arithmetic (~13 MB/day total write volume, acceptable vs. SSD endurance), OR clarify that plan.md's "<0.1 MB/day" refers to **net state change** (file size delta), which is a different metric. Either way, make it unambiguous.

### B-5 · Enabled live-reload: three-vs-four off-by-one + semantic drift
**Files**: `data-model.md:30`, `contracts/evtspike-config.md:70-74`
Data-model §1 says: "All fields except `Enabled`, `BaselinePath`, `DisabledChannels`, `AddedChannels` can reload without restart. The **three** excepted fields require Stop+Start of the subsystem." Lists four fields, says "three." The contract correctly defines `enabled: false → true` as "Start subsystem" (not Stop+Start of a running one). Terminology is muddled between "Stop+Start of subsystem" (what reload of channel list means) and "Start or Stop the subsystem" (what enable/disable means).
**Severity**: blocker — the spec's own count is wrong; reviewers and implementers will disagree on semantics.
**Fix direction**: rewrite as "Fields other than `Enabled`, `BaselinePath`, `DisabledChannels`, `AddedChannels` hot-apply to running detectors. `Enabled` transitions start or stop the subsystem. `BaselinePath`, `DisabledChannels`, `AddedChannels` trigger a Stop+Start cycle of a running subsystem." Four fields, correctly counted, with distinct semantics.

### B-6 · tasks.md T015 contradicts data-model.md §3 on unreadable-file handling
**Files**: `tasks.md:64`, `data-model.md:98-102`
Data-model load protocol: (1) missing → fresh; (2) **unreadable → same as missing (no rename)**; (3) JSON unmarshal error → rename to `.corrupt-<ts>.bak` + fresh; (4) schema-incompat → rename to `.incompat-<ts>.bak` + fresh. T015 says "handles missing / unreadable / JSON-corrupt / incompatible-SchemaVersion cases by **renaming to timestamped `.bak`**" — treats unreadable the same as corrupt. A file that's temporarily unreadable (AV lock, transient permission error) would get renamed and discarded, losing learned state.
**Severity**: blocker — behavior mismatch between task and data model means startup error-path tests will disagree.
**Fix direction**: update T015 to distinguish: unreadable = no rename, fresh in-memory; corrupt/incompat = rename + fresh. Match data-model exactly.

---

## HIGH (5)

### H-1 · Privilege grant/revoke on LocalSystem is load-bearing but underspecified
**Files**: `contracts/msi-security-opt-in.md:67`, `research.md:66-71`, `research.md:89`
The MSI custom action grants `SeSecurityPrivilege` to whatever account runs the DrainCtl service — default LocalSystem. `LsaRemoveAccountRights` on a shared account (LocalSystem) removes the privilege globally for that account, potentially affecting other services running as LocalSystem on the same host that rely on that privilege. The "symmetric revoke" model only works cleanly for a dedicated service account.
**Severity**: high — real operational blast radius on hosts with other LocalSystem services that depend on SeSecurityPrivilege.
**Fix direction**: add an explicit note to `msi-security-opt-in.md` that (a) grant is a per-account right, (b) revocation on LocalSystem is host-global and could affect unrelated services. Consider a `Before revoke` check that counts other LSA entries referencing SeSecurityPrivilege; alternatively, document that admins who run other LocalSystem services with SeSecurityPrivilege must re-grant after DrainCtl opt-out.

### H-2 · MSI deferred custom action ordering vs service registration
**Files**: `contracts/msi-security-opt-in.md:49-60`
`GrantSeSecurityPrivilege` is scheduled `After="InstallFiles"`. The DrainCtl service is typically registered by `InstallServices`, which runs **after** `InstallFiles`. The CA calls `QueryServiceConfig` to look up the service account — at `After='InstallFiles'` time the service may not yet be registered on a fresh install. Additionally, deferred CAs cannot read MSI properties without `CustomActionData`; the contract doesn't define the handoff.
**Severity**: high — fresh-install opt-in could fail with "service not found."
**Fix direction**: schedule `GrantSeSecurityPrivilege` `After="InstallServices"` (or after `StartServices`). Define `CustomActionData` explicitly if the CA needs the service name. Add a contract test for fresh-install-with-opt-in ordering.

### H-3 · Synchronous pipe handler + 2s write timeout creates double-path notifications under degraded targets
**Files**: `contracts/pipe-spike-message.md:74-82`, `tasks.md:169` (T054)
Service handler runs `SendNotification` on the caller goroutine with "no background queue." Contract claims "extra 100ms under normal conditions." But SMTP to a down server, or an unresponsive webhook, can block fan-out for 10–30s. T054 sets client write timeout at 2s and "no retry." Under degraded conditions: client times out → falls back to local log (FR-016) → considers spike "dropped"; service eventually completes notification. Result: operator gets BOTH a webhook and a local-log entry for the same spike. Also contradicts B-2 guarantees.
**Severity**: high — double notification path is a real observable bug during SMTP/webhook outages.
**Fix direction**: either (a) service handler acks the pipe immediately and runs SendNotification in a background goroutine (adds a queue but bounds pipe latency), or (b) document explicitly that standalone considers pipe timeouts as "delivered" and does NOT log locally in that case. Pick one.

### H-4 · T058 doesn't enforce the "coordinate or refuse to double-run" invariant
**Files**: `spec.md:109` (Edge Case), `tasks.md:176` (T058)
Spec Edge Case: "Two processes try to own the baseline file simultaneously... the detector **must either coordinate or refuse to double-run**; it must not silently overwrite each other's state." T058 only gives standalone a **different default path** (`evtspike-standalone-baseline.json` vs `evtspike-baseline.json`). If an admin sets both `baseline_path` fields to the same file, nothing refuses or coordinates — two detectors will corrupt the same baseline.
**Severity**: high — silent failure mode exactly as spec warns against.
**Fix direction**: add a task to acquire an exclusive per-path file lock on `LoadBaseline`, fail Start with a clear error if locked. OR (cheaper) on Start, write a `<path>.lock` file with the current PID; refuse to start if a live PID owns it. Document the chosen approach in `data-model.md` §3.

### H-5 · Hot-apply of `prior_strength` / `mean_per_bucket_prior` is a silent no-op for existing channels
**Files**: `contracts/evtspike-config.md:72`, `data-model.md:19-21`
The contract lists `prior_strength`, `mean_per_bucket_prior`, and `half_life_buckets` as "Hot-apply to running detectors (no baseline reset)." But `prior_strength` and `mean_per_bucket_prior` only define the PRIOR that seeds a `GammaState` at first observation. Once `Alpha`/`Beta` have integrated observations, changing these values affects only newly-added channels. An admin who bumps `prior_strength` expecting tighter scoring won't see any effect on existing channels — silent config drift.
**Severity**: high — violates principle of least surprise for a config knob the spec says admins can tune.
**Fix direction**: either (a) move `prior_strength` and `mean_per_bucket_prior` to the Stop+Start list (so they actually re-initialize with the new prior), or (b) document in the contract that these affect only new channels; existing channels keep their integrated posteriors. `half_life_buckets` is fine to hot-apply (it's used on every update).

---

## MEDIUM (11)

### M-1 · Spec Key Entities says Spike Event "carries a severity"; data model doesn't
**Files**: `spec.md:186` vs `data-model.md:132-147`
`Key Entities` prose describes Spike Event as carrying "a severity" alongside observed/expected/etc. The `SpikePayload` struct has no severity field; severity is on the notification envelope (`event_spike-payload.md:19` `status`), echoed from the target's configured severity per FR-011a. This is the right design — severity is a notification-wiring property, not a spike property — but the spec prose is stale.
**Fix**: rewrite `spec.md:186` to "Carries server, channel, observed count, expected count, and window timestamps; severity is assigned by the notification-target wiring (FR-011a), not the detector."

### M-2 · Clock-skew edge case incomplete
**Files**: `spec.md:107` (Edge Case)
Edge case names clock changes but only says "uses the new wall-clock time going forward." Missing: DST fall-back (same slot visited twice in one day), DST spring-forward (slot skipped), NTP backward correction (10s bucket boundary could move backward mid-window), and monotonic-clock vs wall-clock semantics for `WindowStart/WindowEnd`.
**Fix**: add explicit sub-bullets: (i) backward clock jump within a bucket → drop the bucket counter and restart on the next 10s boundary; (ii) DST fall-back → second visit to the same 15-min slot updates the same `GammaState.N` (acceptable); (iii) spring-forward skipped slot → no harm (just one less observation).

### M-3 · Mid-run channel unavailability is underspecified
**Files**: `spec.md:106` (Edge Case)
"Detector logs and continues; does not bring down the whole subsystem." Doesn't say: does it retry the subscription, mark the channel as error state, change the dashboard `DetectorStatus` pill, preserve the baseline for later, or silently stop monitoring forever?
**Fix**: specify: on mid-run subscription loss, the channel is marked `disabled=true` in the in-memory state; baseline is preserved; the detector retries subscription every N minutes (configurable default 5); dashboard `mature_channels` count reflects actual-subscribed-and-mature.

### M-4 · SC-001 has no reproducible workload definition
**Files**: `spec.md:194` (SC-001)
"Representative RDSH host" and "routine daily operation" are undefined. Independent testers can't reproduce the false-positive budget without a spec for normal-day traces/rates.
**Fix**: define "representative" via a minimum-spec trace (e.g., 100–500 events/hour across watched channels, at least one morning logon storm and one overnight idle period, a named test workload). Or downgrade SC-001 to "SHOULD" with "measured on a representative host" caveat.

### M-5 · SC-006 disabled-mode resource target is unmeasurable AND untested
**Files**: `spec.md:199` (SC-006), `tasks.md` (Phase 9 T094)
"Indistinguishable from a build without the feature" has no tolerance, workload, or method; T094 only measures enabled-mode CPU/memory.
**Fix**: add a numeric tolerance (e.g., "baseline delta ≤1 MB RSS and ≤0.1% CPU under 1-hour idle"). Add a task to validate disabled-mode resource usage.

### M-6 · SC-007 enabled-mode perf budget lacks workload definition
**Files**: `spec.md:200` (SC-007), `plan.md:33-34`
"Small single-digit percentage of one core" and "well under 50 MB" — no workload spec (events/second, channel count, CPU baseline).
**Fix**: define the measurement workload alongside SC-007 (e.g., "under 200 events/sec aggregate across the 54-channel default list on a 4-core VM, averaged over 1 hour").

### M-7 · DetectorStatus "healthy" = 1 mature channel out of 54 is too permissive
**Files**: `data-model.md:160-170`
Rule: `MatureChannels > 0` → `healthy`. One channel with one mature 15-min slot (out of 54 × 96 = 5184 slot-cells) flips the pill from `training` to `healthy`. Operator intuition of "healthy" is probably closer to "most channels mature."
**Fix**: define a threshold, e.g., "`healthy` when ≥ 50% of enabled channels have any mature slot; `training` otherwise." Or add a fourth state `partial` for the intermediate case.

### M-8 · Multiple Success Criteria have no validation task
**Files**: `tasks.md` Phase 9, `spec.md:194-203`
SC-001 (weekly zero-FP) → no task. SC-003 (99% steady-state transient suppression) → no statistical test task; T022 is a single-case deterministic test. SC-006 → see M-5. FR-013 disabled startup (no subs, no baseline file) → no test task. FR-025 target-failure isolation → only manual T096.
**Fix**: add tasks for (a) a week-long synthetic-workload SC-001 test (can run in CI with compressed-time simulator), (b) statistical test for SC-003 (inject N transient bursts, verify ≤1% notify rate), (c) disabled-mode startup test (assert no files written, zero subscriptions), (d) automated target-isolation test.

### M-9 · Email/ntfy contract tests listed but no task implements them
**Files**: `contracts/event_spike-payload.md:84-85`, `tasks.md` Phase 3
Contract lists `TestEventSpikePayload_EmailTemplate` and `TestEventSpikePayload_NtfyPriority`. T018 only covers webhook shape + HMAC; T032 (email) and T033 (ntfy) are implementation tasks without paired test tasks.
**Fix**: add T018a (email template render test: MJML → HTML, assert observed/expected + emoji), T018b (ntfy priority mapping test: warning→3, alert→4).

### M-10 · MSI warning-copy test gap
**Files**: `tasks.md:240` (T075), `contracts/msi-security-opt-in.md:133` (`TestMSI_UIText_ContainsWarningCopy`), `spec.md:130` (FR-031)
Contract lists `TestMSI_UIText_ContainsWarningCopy` — scans the compiled `.msi` UI tables for the exact FR-031 string. No task implements it; T075 is actually modify-remove. Also, FR-031 lists four capabilities ("read, clear, alter audit policy") but `research.md:94` and the Feature description add "set SACLs" — the canonical copy itself is inconsistent across the spec package.
**Fix**: (a) canonicalize the warning string in FR-031 to include all four capabilities consistently; (b) add a test task that asserts the exact string is present in the built `.msi`.

### M-11 · Persistence cadence: slot-rollover vs wall-clock ticker drift
**Files**: `plan.md:36`, `research.md:11`, `tasks.md:95` (T024), `data-model.md:18` (`PersistIntervalSeconds`)
Plan and research say "once per slot rollover boundary" (every 15 min on :00/:15/:30/:45). Tasks and data-model use a generic `PersistIntervalSeconds=900` ticker that is not aligned to slot boundaries. The stated rationale in R1 for slot-aligned writes was "the written state is coherent — a slot is either fully updated or not, simplifying the on-disk schema." An un-aligned 15-min ticker can write mid-slot, losing the coherence property.
**Fix**: either (a) have T024 align the ticker to the next slot boundary after start (a one-time offset calc), or (b) drop the "slot-aligned" rationale from R1 and accept mid-slot writes as harmless given the atomic-replace + same-slot-overwrite-next-tick dynamic.

---

## REJECTED (5)

- **C4 (research mentions `source_host`)**: research.md R3 mentions a `source_host` field that final docs don't carry. It's stale research prose, not a contract defect. Low-priority doc hygiene fix if any; no behavior impact.
- **B8 (capped updates don't prove bounded drift)**: the POC validates this property; spec treats the algorithm as committed (Assumption: "POC has validated the statistical approach"). Not a pre-impl review issue.
- **B12 (EvtSubscribe backpressure under floods)**: implementation concern against the POC, which is out of this review's scope. If the POC already handles this (atomic counter, drain loop), it carries over; if not, it's a code-review finding on the POC itself.
- **B13 (multi-server dashboard state)**: matches existing DrainCtl deployment model — each host runs DrainCtl service, each host reports its own detector status to its own dashboard. The spec is consistent with how the rest of the product works.
- **B14 (admin can manually add `Security` after opt-out)**: explicitly handled in `contracts/evtspike-config.md:53` — subscription fails, channel is logged as skipped per FR-009. Defined behavior, not a bug.

---

## Summary

| Severity | Count |
|----------|-------|
| Blocker  | 6     |
| High     | 5     |
| Medium   | 11    |
| Rejected | 5     |

**Net**: the spec package is largely coherent but has a cluster of hard contradictions (6) that must be fixed before implementation starts, five architectural concerns (MSI privilege scope, pipe sync handler, double-run invariant, config hot-apply semantics) that need owner decisions, and a pile of testability gaps that weaken SC validation. Total engineering lift to address: 1–2 days of focused spec editing and task splitting. Recommend not starting implementation until blockers are closed.
