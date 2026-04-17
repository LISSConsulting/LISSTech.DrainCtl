# Remediation Plan — spec 006-evtspike-detection

**Date**: 2026-04-16
**Target**: `specs/006-evtspike-detection/**`
**Source findings**: `docs/reviews/codex-2026-04-16-spec-006-synthesis.md`
**Scope**: pre-implementation spec edits only — no code changes.

---

## Phase 1 — Contract cleanups (shared fixes, no owner decisions)

### R-1 · Canonicalize 10s window in pipe and SSE examples
**Addresses**: B-1
**Files**: `contracts/pipe-spike-message.md` (§Example payload, lines ~33–39); `contracts/dashboard-sse-events.md` (§Example event, lines ~48–54)
**Change**: Rewrite both example JSON blobs to use `window_start: 14:22:42` → `window_end: 14:22:52` (10s window per `data-model.md:146`) and `first_seen_at: 14:22:32` (earliest sensible value in the 3-window confirmation range). Match the already-correct `contracts/event_spike-payload.md:29-35`.
**Verify**: grep all three contract files — no occurrence of `14:23:42` or any 60s window_end; `WindowEnd - WindowStart == 10s` in every example; `FirstSeenAt <= WindowStart` in every example.
**Effort**: S
**Owner decision needed?**: no

### R-2 · Fix three-vs-four off-by-one and clarify reload semantics
**Addresses**: B-5
**Files**: `data-model.md` §1 (lines ~28–32); `contracts/evtspike-config.md` §Hot-reload matrix (lines ~68–76)
**Change**: Replace the muddled sentence with: "Fields other than `Enabled`, `BaselinePath`, `DisabledChannels`, `AddedChannels` hot-apply to running detectors. `Enabled` transitions start or stop the subsystem. `BaselinePath`, `DisabledChannels`, `AddedChannels` trigger a Stop+Start cycle of a running subsystem." Update contract table to use the same three categories (hot-apply / Start-or-Stop / Stop+Start-cycle).
**Verify**: the word "three" does not appear followed by a list of four; contract table rows total = config field count; `enabled: false → true` row says "Start subsystem," not "Stop+Start."
**Effort**: S
**Owner decision needed?**: no
**Interaction with R-17**: R-17 may reclassify `prior_strength` and `mean_per_bucket_prior` from hot-apply into a different bucket. Editors MUST sequence: first apply R-17's decision, then run R-2 edits. If R-2 is applied first and R-17 later chooses Option A, the hot-reload table is rewritten twice. The per-row entries for `prior_strength` / `mean_per_bucket_prior` in R-2 should be left as placeholders ("see R-17") until R-17 resolves.

### R-3 · Resolve FR-017 retry-vs-drop contradiction — split fix
**Addresses**: B-2
**Files**: `spec.md` §Edge Cases (line ~111); optional cross-ref in FR-017 text
**Change**: Split this fix into two commits so it respects the R-15 dependency:
- **R-3a (safe to do now)**: remove the false durable-retry claim. Delete the phrase "without losing in-flight spikes already emitted" from the Edge Cases bullet. Do not add a replacement guarantee yet.
- **R-3b (after R-15 resolves)**: replace the edge-case bullet with language that matches the chosen pipe-handler semantics from R-15. If R-15 Option A wins: "Standalone CLI reconnects on the next spike; spikes emitted while the pipe was down are written to the local log per FR-016." If R-15 Option B wins: "Standalone CLI treats a pipe-write timeout as 'service accepted'; spikes for which the pipe connection is refused or reset are written to the local log per FR-016." (Note Option B is itself flagged unsafe under codex iter-1 #7; R-15 should not pick Option B as currently written.)
**Verify**: after R-3a, no file mentions "without losing in-flight spikes"; after R-3b, spec edge-case prose and pipe contract §Failure modes describe identical observable behavior under each failure row.
**Effort**: S
**Owner decision needed?**: depends on R-15 for R-3b; R-3a is unblocked

### R-4 · Reconcile baseline-write I/O budget
**Addresses**: B-4
**Files**: `plan.md` §Performance Goals (line ~36); `research.md` §R1 rationale (line ~14); `spec.md` §Success Criteria SC-007 (line ~200); `tasks.md` T094 (line ~279)
**Change**: (a) Update `plan.md` to read: "Baseline persistence: ~13 MB/day total write volume (96 writes × ~135 KB), acceptable vs. typical SSD endurance. Net state-change per write is small (incremental slot updates), but the atomic-replace model rewrites the full file each tick." Keep `research.md` arithmetic authoritative. (b) Extend SC-007 with a third clause: "baseline write volume ≤13 MB/day under the default 15-minute cadence on the default channel list." (c) Extend T094 acceptance: "also measure or compute baseline-file write volume over the 1-hour steady-state window and extrapolate to daily; assert ≤13 MB/day."
**Verify**: the string "13 MB/day" appears in `plan.md`, `research.md`, `spec.md` SC-007, and `tasks.md` T094; no file cites "<0.1 MB/day" in a non-explanatory context.
**Effort**: S
**Owner decision needed?**: no

### R-5 · Align T015 with data-model unreadable-vs-corrupt split
**Addresses**: B-6
**Files**: `tasks.md` T015 (line ~64)
**Change**: Rewrite T015 acceptance to: "handles four startup cases per `data-model.md` §3 load protocol: (1) missing → fresh, no rename; (2) unreadable (AV lock / transient EACCES) → fresh in-memory, no rename; (3) JSON unmarshal error → rename to `.corrupt-<ts>.bak` + fresh; (4) `SchemaVersion` mismatch → rename to `.incompat-<ts>.bak` + fresh."
**Verify**: T015 text matches `data-model.md:98-102` verbatim on unreadable path (no rename); test task enumerates all four cases.
**Effort**: S
**Owner decision needed?**: no

### R-6 · Fix MSI deferred-CA ordering (grant AND revoke)
**Addresses**: H-2
**Files**: `contracts/msi-security-opt-in.md` §Custom action sequencing (lines ~49–88)
**Change**: (a) `GrantSeSecurityPrivilege` — change to `After="InstallServices"` (so the DrainCtl service is already registered and `QueryServiceConfig` resolves its account). (b) `RevokeSeSecurityPrivilege` — change to `Before="DeleteServices"` (so the service registration still exists and the account SID is still resolvable). (c) For BOTH CAs, add an explicit `CustomActionData` block specifying the properties passed to the deferred CA (service name, action flag = grant|revoke). Deferred CAs cannot read MSI properties directly; the `<SetProperty>` / `CustomActionData` handoff must be spelled out, including a preceding immediate CA that populates the service account lookup while the service registration still exists.
**Verify**: grant CA is scheduled after `InstallServices`; revoke CA is scheduled before `DeleteServices`; both CAs have a paired immediate CA that populates `CustomActionData` before the service registration is changed; the contract includes a test case for the fresh-install, modify-add, modify-remove, and full-uninstall sequences each resolving the service account correctly.
**Effort**: M
**Owner decision needed?**: no (pure sequencing fix)

---

## Phase 2 — Testability and coverage

### R-7 · Correct US1 transient-suppression test window
**Addresses**: B-3
**Files**: `tasks.md` Phase 3 Independent Test (line ~79)
**Change**: Replace "lasts <60 s" with "lasting **one 10-second scoring bucket or less** (2-of-3 confirmation suppresses one-shot transients)." Cross-reference spec.md US1 Acceptance Scenario 2 and SC-003.
**Verify**: no remaining "<60 s" in tasks.md; test wording cannot reject a 20–50s sustained burst as a correct alert.
**Effort**: S
**Owner decision needed?**: no

### R-8 · Canonicalize FR-031 warning copy across the whole spec package
**Addresses**: M-10 (prerequisite for R-9)
**Files**: `spec.md` FR-031 (line ~130) AND spec.md §Clarifications (Security channel clarification, line ~16); `research.md` (line ~94); `contracts/msi-security-opt-in.md` §Feature definition (line ~12) AND §Installer UI (line ~112); `quickstart.md` Path C (if the warning is quoted); any future installer-UI screenshot captions
**Change**: Pick the four-capability canonical string ("read Security log, clear Security log, alter audit policy, set SACLs") and make it byte-identical wherever it appears in the spec package. Put the authoritative copy in FR-031 and have every other mention quote it by reference or inline with a source-of-truth note. Run a grep for partial substrings ("alter audit policy", "clear Security log", "SeSecurityPrivilege") and reconcile every occurrence.
**Verify**: exact-string grep for the canonical capability sentence returns identical matches in ≥4 files (spec.md, research.md, msi-security-opt-in.md, quickstart.md); no file lists only three capabilities; no file omits "set SACLs"; Clarifications Q5 wording matches FR-031.
**Effort**: S–M (depends on how many incidental mentions the grep finds)
**Owner decision needed?**: no (pick four-capability superset)

### R-9 · Add missing test tasks
**Addresses**: M-8, M-9, M-10
**Files**: `tasks.md` Phase 3 (add T018a, T018b) and Phase 9 (add SC-001/SC-003/SC-006/FR-013/FR-025/MSI-warning tasks)
**Change**: Add:
- T018a: email template render test (MJML → HTML, assert observed/expected + emoji) — from `event_spike-payload.md:84`.
- T018b: ntfy priority mapping test (warning→3, alert→4) — from `event_spike-payload.md:85`.
- T094a: disabled-mode resource test (no subs, no baseline file, RSS delta ≤1 MB, CPU ≤0.1% over 1h idle).
- T094b: statistical SC-003 test (N transient injections, assert ≤1% notify rate).
- T094c: week-long synthetic-workload SC-001 test (compressed-time simulator, zero-FP assertion).
- T094d: automated target-isolation test for FR-025 (replace manual T096 where possible).
- T075a: MSI warning-copy test that scans compiled `.msi` UI tables for the canonical FR-031 string (depends on R-8).
**Verify**: each of **SC-001 through SC-009** is referenced by at least one T-xxx in tasks.md (explicit: SC-001→T094c, SC-002→T095, SC-003→T094b, SC-004→T097, SC-005→T059–T062, SC-006→T094a, SC-007→T094, SC-008→T092, SC-009→T094d or T096); every `TestXxx` identifier named in any `contracts/*.md` is cited in the description of at least one T-xxx task (not renamed to TestXxx); FR-013 and FR-025 each have at least one automated task.
**Effort**: M
**Owner decision needed?**: no

### R-10 · Make SC-001, SC-006, SC-007 measurable + define simulator fidelity
**Addresses**: M-4, M-5, M-6
**Files**: `spec.md` §Success Criteria (lines ~194–203); `plan.md` §Performance Goals (lines ~33–34); `tasks.md` Phase 9 description for the new simulator-based tasks
**Change**: Add a single "Measurement workload" sub-section to spec.md defining: representative host (4-core VM, 54-channel default list), normal-day trace (100–500 events/hour with one morning logon storm + overnight idle), enabled-mode load (200 events/sec aggregate, 1h average), disabled-mode tolerance (baseline delta ≤1 MB RSS and ≤0.1% CPU over 1h idle). Reference this section from SC-001, SC-006, SC-007. Add a "Simulator fidelity" sub-section for the compressed-time SC-001 test (T094c): the simulator MUST exercise the production bucket/slot/maturity/fallback/confirmation/cooldown logic end-to-end (no mocking of detector internals); it may compress wall-clock time but must preserve slot boundaries, observation counts, and per-slot maturity progression; trace fixtures must be deterministic and checked into `specs/006-evtspike-detection/fixtures/` (new directory).
**Verify**: each of the three SC entries cites the workload section; no SC uses the words "representative" or "small single-digit" without a numeric backing; T094c's acceptance mentions the simulator-fidelity constraints and a deterministic fixture path.
**Effort**: M
**Owner decision needed?**: no

### R-11 · Tighten DetectorStatus health rule (threshold only; four-state enum preserved)
**Addresses**: M-7
**Files**: `data-model.md` §DetectorStatus (lines ~160–170)
**Change**: Keep the four-state public enum (`healthy|training|disabled|error`) — the synthesis M-7 fix direction offered "threshold OR new state" and the codex plan-review (iter 1, concern #3/#17) noted that adding a fifth `partial` state cascades into FR-026, US5 scenarios, SSE contract, TypeScript types, UI color map (T040), and test tasks. Take the threshold-only path: replace `MatureChannels > 0 → healthy` with `MatureChannels >= 0.5 × EnabledChannels → healthy`; `MatureChannels < 0.5 × EnabledChannels → training`. No new enum value, no cascading contract updates.
**Verify**: `grep -n "MatureChannels > 0" data-model.md` returns nothing; `MatureChannels >= 0.5` or equivalent appears; no new enum value anywhere in the spec package; FR-026 still lists four states.
**Effort**: S
**Owner decision needed?**: no (50% is editorial)

### R-12 · Flesh out edge cases: clock skew and mid-run channel loss
**Addresses**: M-2, M-3
**Files**: `spec.md` §Edge Cases (lines ~106–107); `data-model.md` §Baseline (per-channel in-memory state) for the new runtime-state field
**Change**: (a) Clock skew — expand with sub-bullets: (i) backward wall-clock jump within a bucket → drop the in-progress bucket counter, resume on next 10s boundary; (ii) DST fall-back → second visit to the same 15-min slot updates the same `GammaState.N` (acceptable); (iii) spring-forward skipped slot → no harm. (b) Mid-run channel loss — on subscription loss: do NOT reuse the config-level "disabled" vocabulary (avoids conflating admin-disabled via `DisabledChannels` with fault-disabled), instead add a runtime `SubscriptionStatus` field on the in-memory `Baseline` struct with values `{subscribed, retrying, failed}`; preserve baseline across retries; retry every **5 minutes** (fixed constant `SubscriptionRetryInterval = 5 * time.Minute`, not a new config knob — keeps the config surface small per FR-028 scope discipline; can be promoted to a config field in a future release if operators ask); the dashboard `mature_channels` count must only include channels currently in `subscribed` state with at least one mature slot.
**Verify**: edge-case section names all three clock scenarios; mid-run loss bullet answers "does it retry (yes, every 5 min), does it preserve baseline (yes), does the dashboard reflect it (yes, excludes `retrying`/`failed` from mature count)"; no new config field `SubscriptionRetryMinutes` appears anywhere; the word "disabled" is NOT reused for runtime subscription state.
**Effort**: M
**Owner decision needed?**: no (fixed constant avoids config surface creep)

### R-13 · Fix Spike Event severity prose
**Addresses**: M-1
**Files**: `spec.md` §Key Entities (line ~186)
**Change**: Rewrite to: "Spike Event carries server, channel, observed count, expected count, and window timestamps. Severity is assigned by notification-target wiring (FR-011a), not the detector."
**Verify**: no "carries a severity" on the Spike Event entity; FR-011a is the only source of severity.
**Effort**: S
**Owner decision needed?**: no

### R-14 · Align persistence cadence with slot boundaries (and constrain non-default values)
**Addresses**: M-11
**Files**: `tasks.md` T024 (line ~95); `data-model.md` §PersistIntervalSeconds (line ~18); `contracts/evtspike-config.md` §Field contract and §Clamp (for `persist_interval_seconds`)
**Change**: (a) Update T024 to compute a one-time offset at Start so the default 900s ticker fires at the next `:00/:15/:30/:45` wall-clock boundary. (b) Define the behavior for non-900s values: if `PersistIntervalSeconds` is a divisor of 900 (60, 180, 300, 450, 900) OR a multiple of 900 (900, 1800, 2700, ..., 86400), the ticker is slot-aligned (fires at boundaries that also align with slot rollovers). For other values (e.g., 120, 500, 1200 when not a multiple of 900), the ticker fires on a fixed interval from start with NO slot alignment — R1 coherence is a best-effort property at the default cadence; clamp documentation notes this explicitly. (c) Add the clamp entry: `persist_interval_seconds` in `[60, 86400]` (unchanged) and a warning line in `ClampEvtSpike()` when the value is not a divisor or multiple of 900.
**Verify**: T024 mentions "next slot boundary" at the default; data-model / contract explicitly state the alignment rule for non-900 values; `ClampEvtSpike` emits a warning on non-aligned values; the word "coherence" in R1 rationale is preserved for the default case only.
**Effort**: S–M
**Owner decision needed?**: no (chose "align at default, best-effort at non-default" because R1 coherence is only promised at the default)

---

## Phase 3 — Design decisions required (owner calls before editing)

### R-15 · Pipe handler: ack-early-with-queue vs. ack-on-delivery
**Addresses**: H-3, cross-checks B-2
**Files**: `contracts/pipe-spike-message.md` §Failure modes (lines ~74–82); `tasks.md` T054 (line ~169); potentially new task for background queue
**Change**: Owner must choose one of three revised options (Option B from iter-1 was unsafe — it lost spikes on pre-processing timeouts):
- **Option A (recommended)**: Service handler parses and validates the payload, then acks the pipe **and** enqueues the notification on a bounded goroutine-serviced queue. Queue overflow behavior MUST be defined — recommended: on queue near-full (>90% of capacity), the handler returns `{"ok": false, "error": "spike: service overloaded, falling back"}` BEFORE acking, so the standalone logs locally per FR-016. This avoids silent loss (codex iter-1 #6). New task T054a defines the queue + overflow behavior + observability metric for queue depth.
- **Option B (revised — only acceptable with this guardrail)**: Keep synchronous handler; standalone treats pipe-write timeout as "service accepted" **ONLY IF** it has already received a bytes-received ack at the transport layer before timeout (i.e., the framing layer confirmed the service read the request). Pre-ack timeouts (refused connection, reset, write fails before server reads) fall back to local log per FR-016. This requires pipe-framing changes to expose read-ack state to the client — non-trivial; not recommended.
- **Option C (fallback)**: Keep synchronous handler, standalone ALWAYS logs locally on pipe-write timeout (accept potential double-notification during SMTP outages as the lesser harm). Document the double-notification window explicitly as a known limitation.
Recommend **Option A**. Option C is the safe fallback if queue engineering budget is too tight.
**Verify**: pipe contract defines a single observable behavior under degraded targets; for every failure row, exactly one durable visibility path is specified (service notification fires OR standalone local log is written); no pipe outcome can leave a spike untracked.
**Effort**: L (Option A), L (Option B — framing changes), M (Option C — doc-only + accept double-notify limitation)
**Owner decision needed?**: yes — pick A, B, or C before editing; this blocks R-3b's final wording

### R-16 · Double-run coordination mechanism
**Addresses**: H-4
**Files**: `data-model.md` §3 (load/lock protocol); `tasks.md` add task T058a
**Change**: Owner must choose:
- **Option A**: OS-level exclusive file lock (`LockFileEx` on Windows) acquired during `LoadBaseline`; `Start` fails with a clear error if already held.
- **Option B**: Sidecar `<path>.lock` file containing owner PID + start time; `Start` checks if PID is live and refuses if so; stale-lock cleanup on mismatch.
Option A is more robust (OS-enforced); Option B is easier to debug (human-readable, no handle leaks). Pick one and spec it in `data-model.md` §3; add T058a to enforce.
**Verify**: setting both `baseline_path` fields to the same path fails Start on the second process with a named error; `spec.md:109` invariant ("coordinate or refuse to double-run") has a concrete mechanism behind it.
**Effort**: M
**Owner decision needed?**: yes — pick A or B

### R-17 · Hot-apply scope for prior_strength / mean_per_bucket_prior
**Addresses**: H-5
**Files**: `contracts/evtspike-config.md` §Hot-reload matrix (line ~72); `data-model.md` §GammaState (lines ~19–21)
**Change**: Owner must choose:
- **Option A**: Move `prior_strength` and `mean_per_bucket_prior` to the Stop+Start list (re-initializes posteriors with new prior; loses learned state for all channels — operationally costly but matches operator intuition).
- **Option B**: Keep hot-apply; add an explicit note that these affect only channels observed after the change; existing `GammaState.Alpha/Beta` are not rewritten.
- **Option C**: Hot-apply with a one-time re-weighting (blend new prior into existing posteriors) — most complex, not recommended without POC support.
Recommend Option B — preserves learned state, surfaces the surprise as documented behavior rather than a silent no-op.
**Verify**: contract row for these two fields has explicit scope ("new channels only" or "stop+start"); no admin-facing knob has unwritten effect semantics.
**Effort**: S (Option B, doc-only) / M (Option A, requires Stop+Start wiring in data-model)
**Owner decision needed?**: yes — pick A, B, or C

### R-18 · LocalSystem privilege blast-radius policy
**Addresses**: H-1
**Files**: `contracts/msi-security-opt-in.md` §Security considerations (line ~67); `research.md` (lines ~66–71, ~89); `spec.md` FR-032 (line ~131)
**Change**: The iter-1 "enumerate other LSA entries holding SeSecurityPrivilege" mitigation is technically invalid — LSA stores one entry per account, not per service; enumerating holders finds LocalSystem once whether one service or ten depend on it (codex iter-1 #9). Owner must ratify ONE of the following closure policies (narrow the decision explicitly):
- **Policy P-1 (document-only)**: Add a warning block in `contracts/msi-security-opt-in.md` stating revocation of `SeSecurityPrivilege` from LocalSystem is a host-global operation that cannot be scoped to DrainCtl; admins accept this risk at opt-in time. FR-031 warning copy is updated to say "opting out will revoke this privilege from the LocalSystem account on this host, which may affect other LocalSystem services." Simplest; ships today.
- **Policy P-2 (confirm-at-opt-out)**: On opt-out (`REMOVE=SecurityEventLog` or full uninstall), the installer shows a confirmation dialog with the same warning as P-1 + "Proceed with revoke?" Admin must click through. Silent uninstall (`/qn`) defaults to proceed (must document); scripted rollouts aren't blocked.
- **Policy P-3 (abort on LocalSystem)**: If the detected service account is `LocalSystem`, the revoke CA logs a warning and does NOT call `LsaRemoveAccountRights`. Minimum-privilege posture is not restored on opt-out for LocalSystem deployments; admins who want revocation must first reconfigure the service to run under a dedicated account. Safest; least surprising.
- **Policy P-4 (dedicated-account redesign)**: Out of scope for this remediation round — deferred to a future release (sync with R-6's MSI work).
Recommend **P-3**. P-1 ships risk unmitigated. P-2 is a UX half-measure that silent-install automation overrides. P-3 is the only option that makes revoke safe at the cost of a visible opt-out caveat, which is accurate to the architecture.
**Verify**: `contracts/msi-security-opt-in.md` explicitly names the host-global-revoke risk and specifies the chosen policy; the CA's revoke logic matches the policy (not the invalid LSA-enumeration approach); FR-031 warning copy reflects the chosen policy; no task tests "LSA enumeration detects other services."
**Effort**: S (P-1), M (P-2 dialog + silent-install caveat), M (P-3 CA branch), L (P-4 deferred)
**Owner decision needed?**: yes — pick P-1, P-2, P-3, or P-4

---

## Phase 4 — Documentation polish

(Covered inline above under R-8, R-12, R-13 — no standalone polish-only items remain after Phases 1–3. Any stale prose not already captured is out of synthesis scope.)

---

## Phase 5 — Deferred

- **REJECTED items (C4, B8, B12, B13, B14)**: not fixed; synthesis classifies these as hallucinated, out of scope, or already handled. C4 (`source_host` stale prose in research.md) is doc hygiene only — defer to a future sweep.
- **H-1 redesign to dedicated service account**: explicitly deferred. Closure is "document and move on" (R-18). Rationale: dedicated service account is a cross-cutting product change that affects MSI installer UX, upgrade paths, and existing deployments; not justified by the blast-radius risk alone when a pre-revoke check + warning suffices.
- **M-11 option (b) "accept mid-slot writes as harmless"**: not taken (R-14 chose option (a)) — preserves the R1 coherence rationale that was already published.

---

## Post-remediation verification

After all edits, the following properties must hold across the spec package:

1. **Window invariants**: parse every JSON example in the spec package containing `window_start`/`window_end`; assert the timestamp delta equals exactly 10 seconds and `first_seen_at <= window_start` in every example. (String-match of "60s" or "14:23:42" is insufficient — the check must be semantic.)
2. **FR-017 single voice**: spec prose and pipe contract describe identical observable behavior for EVERY failure row; no "without losing" claims remain. After R-15 resolves, R-3b wording matches the selected option.
3. **I/O budget single value**: `plan.md`, `research.md`, `spec.md` SC-007, and `tasks.md` T094 all cite ~13 MB/day baseline write volume; T094 acceptance explicitly measures it.
4. **Hot-reload matrix coherent**: `data-model.md` §1 and `contracts/evtspike-config.md` agree on four special fields, correctly counted, with three distinct semantics (hot-apply / Start-or-Stop / Stop+Start-cycle). `prior_strength` and `mean_per_bucket_prior` placement matches whichever R-17 option was chosen, documented in both files with identical language.
5. **Startup error paths**: `tasks.md` T015 and `data-model.md` §3 enumerate the same four cases with the same rename-vs-no-rename decisions.
6. **FR-031 canonical string**: byte-identical across `spec.md` FR-031, `spec.md` §Clarifications Q5, `research.md`, `contracts/msi-security-opt-in.md` feature + UI sections, and `quickstart.md` Path C; T075a asserts presence in the built MSI.
7. **Every SC has a task**: each of SC-001 through SC-009 is cited by at least one T-xxx in `tasks.md` Phase 9 (explicit map: SC-001→T094c, SC-002→T095, SC-003→T094b, SC-004→T097, SC-005→T059–T062, SC-006→T094a, SC-007→T094, SC-008→T092, SC-009→T094d or T096).
8. **Contract-test coverage**: every `TestXxx` identifier named in any `contracts/*.md` is cited in the description of at least one T-xxx task. (The task IDs are `Txxx`; this check verifies that the test function name appears in a task description, not that a task is literally renamed to `TestXxx`.)
9. **Pipe delivery single durable path**: for every pipe-handler failure row, exactly one durable visibility path is specified — service notification fires, OR standalone local log is written per FR-016, OR explicit invalid-payload drop with error log. No row leaves a spike untracked. No row produces both a notification AND a local-log entry for the same spike (unless R-15 Option C is chosen, in which case the double-notify scenario is explicitly documented as a known limitation under SMTP outages).
10. **Double-run invariant enforced**: a concrete mechanism (file lock or PID sidecar) is specified in `data-model.md` §3 and exercised by T058a; `spec.md:109` invariant has a named enforcement.
11. **MSI CA ordering correct (both grant and revoke)**: no deferred CA reads MSI properties without explicit `CustomActionData`; `GrantSeSecurityPrivilege` is scheduled `After="InstallServices"`; `RevokeSeSecurityPrivilege` is scheduled `Before="DeleteServices"` OR receives the service account via `CustomActionData` populated before service deletion; all four MSI sequences (fresh install opt-in, modify-add, modify-remove, full uninstall) can resolve the service account.
12. **DetectorStatus threshold preserved four-state enum**: the public enum remains `healthy|training|disabled|error`; threshold rule uses `MatureChannels >= 0.5 × EnabledChannels`; no new enum value (`partial` or similar) appears anywhere; FR-026 still lists four states.
13. **Edge-case completeness**: clock-skew bullet enumerates backward jump, DST fall-back, and spring-forward; mid-run channel loss bullet answers retry-cadence (5 min fixed), baseline-preservation (yes), and dashboard-reflection (`mature_channels` excludes non-subscribed); no new config field introduced.
14. **Persistence cadence**: T024 aligns the ticker to wall-clock slot boundaries at the default 900s; `data-model.md` and `contracts/evtspike-config.md` specify alignment semantics for non-default values (aligned if divisor/multiple of 900, best-effort otherwise); R1 rationale in `research.md` is preserved with a note scoping coherence to the default cadence.
15. **LocalSystem revoke policy ratified**: `contracts/msi-security-opt-in.md` names the chosen R-18 policy (P-1, P-2, P-3, or P-4); the CA revoke logic matches the policy; no code or contract references the invalid "enumerate LSA holders" mitigation.
16. **Simulator fidelity constraints**: T094c acceptance names the simulator-fidelity requirements (exercises bucket/slot/maturity/fallback/confirmation/cooldown logic end-to-end, deterministic fixtures under `specs/006-evtspike-detection/fixtures/`).

---

**Total effort**: ~14 steps, of which 7 are S, 5 are M, 2 are L (R-15 option A, R-9). Owner decisions required before Phase 3 edits can land: R-15, R-16, R-17, R-18. Phases 1 and 2 are unblocked and can proceed immediately.

---

## Iter-2 amendments (supersede earlier text where they conflict)

After the initial plan was written, codex iter-2 adversarial review identified 12 residual defects. The following amendments supersede any conflicting earlier text.

### A-1 · R-14 multiples-vs-divisors slot-coherence distinction
Earlier text said both divisors and multiples of 900 are "slot-aligned." That is incorrect: a 60s ticker writes at `:00, :01, :02…` — wall-clock aligned but writes inside slots. Only multiples of 900 (900, 1800, 2700, …, 86400) align with slot rollover boundaries. Revised rule:
- `PersistIntervalSeconds` a **multiple** of 900 → ticker fires at slot-rollover boundaries; R1 coherence holds.
- `PersistIntervalSeconds` a **divisor** of 900 (60, 180, 300, 450) → ticker fires at wall-clock sub-boundaries; writes may land mid-slot; R1 coherence is not guaranteed; `ClampEvtSpike` logs a warning.
- Other values → ticker fires at fixed offset from Start; no alignment; R1 coherence not guaranteed; warning logged.
Verification property #14 is updated to match.

### A-2 · R-15 Option C removed from remediation menu
Option C ("always log locally, accept double-notification") does not close H-3 — it accepts the bug. Strike Option C from the remediation menu. If the owner wants to defer H-3, that is a separate "defer, H-3 remains open" decision, not a remediation choice. R-15 menu becomes: **Option A (recommended)** or **Option B (requires a new two-phase pipe protocol — explicitly price as L-sized protocol work or strike)**. If neither A nor B is chosen, H-3 is flagged unresolved in the final report.

### A-3 · R-15 Option B reclassified as "requires new pipe protocol"
As originally written, Option B relied on a transport-layer "bytes-received ack" that the existing named-pipe framing does not expose. Properly implementing it requires introducing a two-phase protocol (`received` ack before notification delivery + final status). Option B's effort jumps from M to L. Recommend striking Option B unless the owner wants to invest in a protocol change; Option A remains the default recommendation.

### A-4 · R-18 owner menu restructured: P-3 is the only remediation; P-1/P-2 are accepted-risk
P-4 ("deferred") is not a closure — strike it. The remaining options fall into two distinct categories:

**Remediation menu (closes H-1)**:
- **P-3 (only safe remediation)**: Revoke CA detects service account == LocalSystem and does NOT call `LsaRemoveAccountRights`; logs a warning; FR-031 policy sentence documents the no-revoke behavior (A-8) and directs admins who want revocation to run under a dedicated account.

**Accepted-risk escape hatch (does NOT close H-1; requires explicit product-owner signoff)**:
- **P-1 (document-only)**: Warning in `contracts/msi-security-opt-in.md`; revoke still runs on LocalSystem, potentially affecting other services. Final report MUST flag H-1 as "accepted, not remediated."
- **P-2 (confirm-at-opt-out dialog)**: Interactive confirmation gate; silent installs (`/qn`) still revoke host-globally. Final report MUST flag H-1 as "accepted, not remediated."

**If the owner declines P-3**, H-1 is flagged "accepted, not remediated" in the final review report and a separate product-owner signoff note is added to `contracts/msi-security-opt-in.md` quoting the accepted risk language.

### A-5 · R-17 Option C removed
Option C ("hot-apply with one-time re-weighting") is not POC-validated and has no concrete formula. Strike it. R-17 menu becomes: **Option A (Stop+Start)** or **Option B (hot-apply with "new channels only" documentation — recommended)**.

### A-6 · R-16 Option B strengthened
PID-reuse on Windows makes bare-PID sidecar locking unsafe. If Option B is chosen, the sidecar must contain PID + process creation time + executable path; Start refuses if ALL three match a live process; stale-lock cleanup only runs if all three mismatch. Option A (OS-level `LockFileEx`) remains simpler and is now the explicit recommendation.

### A-7 · R-6 CA sequencing — per-install-mode explicit pairs
R-6's original "paired immediate CA populates `CustomActionData`" was ambiguous across the four install modes. Revised: specify FOUR explicit sequences, each with its own immediate/deferred CA pair:
- **Fresh install + opt-in**: immediate `SetGrantData` scheduled `After="InstallServices"` (service now exists); deferred `GrantSeSecurityPrivilege` runs after it.
- **Modify-add** (adding feature to existing install): immediate `SetGrantData` scheduled `After="InstallServices"` (idempotent); deferred `GrantSeSecurityPrivilege` runs after it.
- **Modify-remove** (removing feature, product stays): immediate `SetRevokeData` scheduled `Before="DeleteServices"` BUT service isn't deleted in this mode — scheduled `Before="UnpublishComponents"`; deferred `RevokeSeSecurityPrivilege` runs after, subject to R-18 policy check.
- **Full uninstall**: immediate `SetRevokeData` scheduled `Before="DeleteServices"` (service still registered); deferred `RevokeSeSecurityPrivilege` runs after, subject to R-18 policy check.
The contract tests in R-9/T072–T075 must cover all four sequences, not just happy-path grant.

### A-8 · R-8 FR-031 copy split into capability + policy sentences
A single byte-identical canonical string cannot both canonicalize the capability list AND carry the R-18-chosen revoke policy (which changes wording across P-1/P-2/P-3). Split FR-031 into two canonical strings:
- **Capability sentence** (owned by R-8, constant): "This grants the DrainCtl service the privilege to read the Security log, clear the Security log, manage audit policy, and set SACLs."
- **Policy sentence** (owned by R-18, policy-dependent):
  - P-1: "Opting out will revoke this privilege from the LocalSystem account on this host, which may affect other Windows services that run as LocalSystem."
  - P-2: "Opting out will prompt for confirmation before revoking this privilege, which would affect other Windows services that run as LocalSystem."
  - P-3: "Opting out removes DrainCtl's Security monitoring but does NOT revoke the privilege from LocalSystem (to avoid affecting other services). Revoke manually via `secedit` or reconfigure DrainCtl to run under a dedicated account."
R-8 canonicalizes only the capability sentence. R-18 owns the policy sentence. Both appear wherever FR-031 is quoted.

### A-9 · R-8 capability sentence vocabulary locked
The spec package currently has drift: spec.md says "clear Security log, alter audit policy"; research.md adds "set SACLs"; msi-security-opt-in.md uses different prose. Lock the canonical capability sentence to the exact wording in A-8 above ("read the Security log, clear the Security log, manage audit policy, and set SACLs") and reject any other ordering/vocabulary in the verification step.

### A-10 · R-10 two named workloads
Split into two named workloads with distinct pass/fail criteria:
- **`normal-day-false-positive-workload`** (validates SC-001, SC-003): 100–500 events/hour aggregate across the 54-channel default list, one morning logon storm (sustained 60s elevated rate), overnight idle, weekly cycle. Pass: zero notifications after baseline maturity; ≤1% of single-bucket transients produce alerts.
- **`stress-performance-workload`** (validates SC-007): 200 events/sec aggregate across the default channel list, sustained for 1 hour after baseline maturity. Pass: CPU <5% of one core, RSS <50 MB, baseline write volume ≤13 MB/day extrapolated.
Reference each workload by name from the applicable SC entries. Add both as deterministic fixtures under `specs/006-evtspike-detection/fixtures/`.

### A-11 · R-11 edge cases defined
The threshold rule must handle:
- `EnabledChannels == 0` → `error` state (precedence over the threshold rule).
- integer rounding: use `MatureChannels * 2 >= EnabledChannels` instead of `MatureChannels >= 0.5 × EnabledChannels` to avoid floating-point edge cases.
Revised state derivation (in priority order):
1. `cfg.EvtSpike.Enabled == false` → `disabled`
2. `EnabledChannels == 0` (no subscriptions succeeded) → `error`
3. `MatureChannels * 2 >= EnabledChannels` → `healthy`
4. else → `training`

### A-12 · R-9 adds subscription-state test tasks (addresses R-12)
R-12 introduced runtime `SubscriptionStatus` with `subscribed/retrying/failed` values. R-9's test-task list must include:
- **T021a**: subsystem-level integration test for mid-run subscription loss: inject a simulated `EvtSubscribe` failure mid-run; assert state transitions `subscribed → retrying`; after 5 min, retry fires; baseline is preserved; dashboard `mature_channels` excludes the retrying channel; successful retry returns to `subscribed`; **repeated failure transitions to `failed` after 12 consecutive retry failures (1 hour at the 5-minute retry cadence) — this is normative, not a recommendation**. The constant `SubscriptionMaxRetries = 12` lives alongside `SubscriptionRetryInterval = 5 * time.Minute` in `internal/evtspike/subsystem.go`.
- Dashboard broker test: `SubscriptionStatus` transitions emit `detector_status` SSE events with updated `mature_channels` count.

### A-13 · R-17 task-list update target
R-17's file list must include `tasks.md` T069 (the reload implementation task). Under Option B, T069 needs a note: "`prior_strength` / `mean_per_bucket_prior` hot-apply affects only channels observed after the change; existing `GammaState` values are not rewritten." Under Option A, T069 needs to trigger Stop+Start on change to those fields.

### A-14 · R-3b covers all surviving R-15 options
After A-2/A-3, R-15 has only Options A and B. R-3b wording maps 1:1:
- Under Option A: "Standalone CLI reconnects on the next spike; spikes emitted while the pipe was down are written to the local log per FR-016. The service-side notification queue may hold up to N spikes; queue overload returns `{ok: false}` and the standalone logs locally."
- Under Option B: "Standalone CLI reconnects on the next spike; spikes for which the pipe connection is refused, reset, or fails before the service-side `received` ack are written to the local log per FR-016. Timeouts after `received` ack are treated as 'service accepted.'"

### Amended verification properties

- **#6 (FR-031 canonical strings)**: capability sentence (A-8) is byte-identical across all files that mention it; policy sentence matches the R-18 chosen policy; both sentences appear together wherever FR-031 is quoted.
- **#7 (SC coverage)**: each of SC-001..SC-009 is referenced by at least one T-xxx AND each referenced task specifies workload (by name from A-10 when applicable), metric, numeric threshold, and automation classification (automated/manual).
- **#11 (MSI CA ordering)**: for each of the four install sequences (fresh+opt-in, modify-add, modify-remove, full-uninstall), the contract specifies the immediate CA name, the deferred CA name, the scheduling anchor, and relative ordering (A-7). Verification tests assert per-sequence behavior.
- **#14 (persistence cadence)**: default 900 and multiples of 900 are slot-rollover-aligned (coherent); divisors of 900 are wall-clock-sub-aligned but NOT slot-coherent; other values are not aligned at all; `ClampEvtSpike` logs a warning for non-multiples.
- **#15 (R-18 policy)**: contracts/msi-security-opt-in.md names the chosen policy (P-1, P-2, or P-3 — P-4 is not allowed); the CA revoke logic matches; if P-1 or P-2 is chosen, a product-owner signoff note appears in `contracts/msi-security-opt-in.md` and the final review report flags H-1 as "accepted, not remediated."

### Owner decisions after iter-2

Before any Phase 3 edits land, the owner must ratify:
1. **R-15 pick**: A (recommended) or B (requires protocol work). Option C is removed.
2. **R-16 pick**: A (recommended) or B (strengthened). Both are now safe.
3. **R-17 pick**: A (Stop+Start) or B (recommended, "new channels only" doc note). Option C is removed.
4. **R-18 pick**: P-3 is the only remediation option that closes H-1. P-1/P-2 are accepted-risk non-remediations requiring separate product-owner signoff and flagging H-1 as "accepted, not remediated" in the final report. P-4 is removed.

If any owner declines to pick, the corresponding finding is flagged as "unresolved after remediation round 2" in the final report.

---

## Iter-3 owner decisions (records actual owner ratifications)

### A-15 · SCOPE REDUCTION: drop standalone CLI (supersedes R-15, R-16, and parts of R-3, R-6, multiple findings)

**Decision**: Drop the standalone CLI deployment mode entirely from MVP. The spec's US3 justifications (not-yet-onboarded hosts, minimal footprint, isolation) do not justify the cost of maintaining dual deployment modes. If a thin CLI for RMM deployment is needed later, it will be a separate project.

**Reasoning** (owner, 2026-04-16): "I was thinking like a thin CLI to use in RMM, but the cost of duality is unjust. Perhaps it's a dedicated project."

**What this changes**:

- **spec.md**: mark FR-014, FR-015, FR-016, FR-017 and User Story 3 as OUT OF SCOPE for this feature. Add a one-paragraph "Deferred" note explaining the rationale. Remove Spike Event's "or over the named pipe to DrainCtl" clause from Key Entities.
- **plan.md**: remove cmd/evtspike as a shippable binary. Detector code still lives in `internal/evtspike/` (library used by the in-service subsystem). The existing `cmd/evtspike/` POC binary can be kept as a dev-only smoke tool or deleted; either way it does NOT ship in the MSI and is NOT installed as a Windows service.
- **research.md**: strike R3 (pipe message schema) and R4 (standalone service install) entirely. Update R6 to reflect in-service-only lifecycle. Update R9 to remove pipe contract tests.
- **tasks.md**: delete Phase 5 (T047–T058); delete T052–T054 pipe changes; delete T057–T058 standalone-binary work; remove all `[US3]` task tags. Keep T055/T056 only if cmd/evtspike is retained as a dev smoke tool (recommend delete).
- **contracts/**: delete `pipe-spike-message.md` entirely. Update `dashboard-sse-events.md` and `evtspike-config.md` to remove any "standalone" references.
- **data-model.md**: §8 `PipeRequest` extension — delete. §5 `SpikePayload` uses drop to two usages (notification + dashboard SSE; pipe usage removed).
- **checklists/requirements.md**: remove any checkbox referencing standalone CLI.
- **quickstart.md**: remove Path B (standalone deployment).

**Findings resolved by scope reduction**:
- **B-2** (FR-017 "without losing" vs "drop this spike") — FR-017 itself is dropped, contradiction dissolves.
- **H-3** (pipe sync handler double-notification) — no pipe, no problem.
- **H-4** (double-run baseline corruption) — single process, single baseline owner.
- **M-11** persistence cadence concern is simplified (one ticker, one owner).
- Half of **H-2** (MSI deferred CA account lookup) is simplified — no standalone service to install/uninstall means `QueryServiceConfig` is only ever looking up the DrainCtl main service. R-6's per-sequence CA handoff still applies for DrainCtl service creation/deletion.

**Remediation steps now moot**:
- R-3 (R-3a and R-3b) — FR-017 deleted outright; nothing left to reconcile.
- R-15 (pipe handler option A/B) — no pipe handler exists.
- R-16 (double-run coordination) — single baseline owner makes this invariant trivially true.
- Portions of R-9 that added pipe contract tests (T021-T023 range, keep only DrainCtl-internal subsystem tests).

**Remediation steps still required (post-scope-reduction list)**:
- **Blockers**: R-1 (window examples in `dashboard-sse-events.md` only — pipe contract is deleted), R-2 (hot-reload matrix), R-4 (I/O budget), R-5 (T015 error paths), R-6 (MSI CA ordering for the main DrainCtl service only), R-7 (US1 test wording).
- **High**: R-18 (LocalSystem privilege — decision still required).
- **High-effectively**: R-17 (prior-param hot-apply — decision still required).
- **Medium**: R-8 (FR-031 copy), R-9 (test-coverage gaps, reduced to non-pipe scope), R-10 (workload definitions), R-11 (DetectorStatus threshold), R-12 (edge cases — clock skew and mid-run channel loss), R-13 (Spike Event severity prose), R-14 (persistence cadence).

**New task**: a Phase-0 scope-reduction sweep through the spec package must happen FIRST, before any of the remaining R-steps. Scope reduction edits are atomic: either all files are updated together or the spec package is internally inconsistent.

**Remaining owner decisions (reduced from 4 to 2)**:
1. **R-17**: A (Stop+Start) or B (hot-apply + "new channels only" doc note — recommended).
2. **R-18**: P-3 (only remediation), or P-1/P-2 (accepted-risk, does not close H-1).

---

### A-16 · R-17 owner ratification: Option B

**Decision**: Hot-apply `prior_strength` and `mean_per_bucket_prior` with documented "new channels only" scope.

**Reasoning** (owner, 2026-04-16): After discussion of the Gamma-Poisson math — the prior fades as observations accumulate; changing it on a mature detector is mathematically near-zero effect; Option A's cost (one typo wipes a week of learned state on all 54 channels) is unacceptable for a knob whose operational value is mainly at install time. Day-to-day sensitivity tuning is already hot-apply via `threshold` and `min_count`.

**Plan edits required under A-16**:

- **`contracts/evtspike-config.md`** §Live reload semantics (table at lines ~68–76): `prior_strength` and `mean_per_bucket_prior` rows → "Hot-apply; takes effect for channels observed after the change. Existing `GammaState.Alpha/Beta` are not rewritten — the prior's influence fades over time as observations accumulate, so late changes have diminishing effect. Admins wanting to re-prior a mature detector should delete the baseline file and restart instead."
- **`data-model.md`** §1 `EvtSpikeConfig` field notes: add the same caveat to the `PriorStrength` and `MeanPerBucketPrior` rows (short form).
- **`tasks.md`** T069: add "When `prior_strength` or `mean_per_bucket_prior` change, the new values are stored in the subsystem config but existing `GammaState` values are NOT reset. New channels that start observation after the change use the new prior. Add a log line: `evtspike: prior params updated; affects only channels subscribed after this point`."
- **`tasks.md`** Phase 7 tests (T066 area): add a test that asserts — change `prior_strength` at runtime, then observe an existing channel → its `Alpha/Beta` deltas are the same as before the change; then add a new channel → its initial `Alpha/Beta` reflect the new prior.

**Verify**: the word "new channels only" (or equivalent) appears alongside both fields in `contracts/evtspike-config.md`, `data-model.md`, and `tasks.md` T069; no test assumes mid-run re-init of existing `GammaState` on prior-param change.

---

### A-17 · SCOPE REDUCTION: drop MSI privilege component; config.json property replaces marker file (supersedes R-18, most of R-6, and multiple findings)

**Premise verified (owner, 2026-04-17)**: Running `whoami /priv` as `NT AUTHORITY\SYSTEM` on the target Windows platform shows `SeSecurityPrivilege` **present in the token** (State: Disabled). LocalSystem can enable it at runtime via `AdjustTokenPrivileges` without any `LsaAddAccountRights` grant. The original research.md R5 premise — that the MSI must grant `SeSecurityPrivilege` to LocalSystem — is incorrect for the default service-account configuration.

**Decision**: Eliminate the MSI privilege component entirely. Replace the marker file with a config.json property. Move the decision to enable Security monitoring from installer-time to config-time, consistent with every other evtspike knob.

**Reasoning** (owner, 2026-04-17): "Mark file is stupid as fuck, use a property in config.json." Combined with the verified premise: the MSI's CA DLL, grant, revoke, and marker-file install/remove are all solving a non-problem. An opt-in boolean in config.json, read at subsystem start, is sufficient.

**New design**:

1. Add field `SecurityChannelEnabled bool` (JSON: `security_channel_enabled`, default `false`, no clamp) to `EvtSpikeConfig` in `data-model.md` §1 and `contracts/evtspike-config.md`.
2. `ResolveChannels(cfg EvtSpikeConfig)` adds `"Security"` to the watched list iff `cfg.SecurityChannelEnabled == true` (or equivalently if an admin has put `"Security"` in `added_channels`).
3. Subsystem `Start` enables `SeSecurityPrivilege` on its own token via `AdjustTokenPrivileges(SE_SECURITY_NAME, ENABLE)` iff the Security channel will be watched. Log a warning if `AdjustTokenPrivileges` returns `ERROR_NOT_ALL_ASSIGNED` (happens when running under a dedicated service account without the right — admin must grant it manually via `secedit` or group policy; out of MVP scope).
4. Live reload: `SecurityChannelEnabled` is Stop+Start (same bucket as `DisabledChannels` / `AddedChannels` — channel-list change).
5. No MSI custom actions. No CA DLL project. No `msi/ca/`. No marker file. No `SecurityEventLog` MSI feature. The MSI is back to its current shape.

**Findings resolved by A-17**:
- **H-1** (LocalSystem blast radius) — dissolved. No `LsaRemoveAccountRights` call is ever made.
- **H-2** (MSI deferred CA ordering) — dissolved for the privilege CAs. The main DrainCtl service CAs still need correct sequencing; that's unchanged.
- **M-10** (FR-031 warning copy canonicalization) — mostly dissolved. The capability warning no longer needs to live in the MSI UI; it can live in README / docs / a CLI-level warning when the admin sets the flag. The four-capability canonical string from A-8/A-9 still applies wherever it's quoted (doc, CLI output), but the byte-identical MSI UI requirement is gone.

**Remediation steps now moot**:
- **R-18** (LocalSystem privilege policy) — no policy needed; no revoke ever runs.
- **R-6 privilege-CA portions** (grant/revoke scheduling, CustomActionData handoff) — deleted. R-6 survives only as "confirm the main DrainCtl service CA sequencing is already correct in the existing MSI" (S effort, possibly no-op).
- **Portions of A-7** (four-sequence CA pairs for grant/revoke) — deleted.
- **Portions of A-8** (MSI UI policy sentence from R-18) — deleted; capability sentence survives as doc/CLI text only.

**Spec-package edits under A-17**:

- **`spec.md`**: delete FR-029, FR-030, FR-031, FR-032. Keep FR-001d (Security excluded from defaults) and FR-033 (single process, single config). Rewrite §Clarifications Q5 to match the new model: "Security is excluded by default. Admins opt in by setting `evtspike.security_channel_enabled: true` in `config.json`. No installer opt-in. The service enables `SeSecurityPrivilege` on its own token at startup if the flag is set; this works under the default LocalSystem account, which has the privilege available but disabled."
- **`plan.md`**: remove the "WiX 5 new optional MSI component" dependency from Technical Context. Remove the CA DLL mention. Post-Phase-1 re-check loses the MSI Feature addition.
- **`research.md`**: rewrite R5 entirely. Correct premise: LocalSystem has SeSecurityPrivilege present but disabled; service enables it via `AdjustTokenPrivileges`. Opt-in mechanism is a config field, not an MSI feature. Document the dedicated-service-account case as "out of MVP — admin must grant the right manually."
- **`tasks.md`**: delete Phase 8 in full (T072–T082). Delete the T028 marker-file read; replace with "at subsystem Start, if the Security channel will be watched, call `AdjustTokenPrivileges` to enable `SeSecurityPrivilege` on the service's token; log warning if it returns `ERROR_NOT_ALL_ASSIGNED`." Add one test task: enable the flag, run subsystem, verify it subscribes to Security without error (integration, LocalSystem only).
- **`contracts/msi-security-opt-in.md`**: **delete entirely**.
- **`contracts/evtspike-config.md`**: add the new field `security_channel_enabled: false` to the schema example and the field-contract cross-reference; update §Live reload semantics; remove §Interaction with MSI Security opt-in (replace with a short note saying Security is a config-level decision, identical in shape to other channel toggles, with the caveat that it requires `SeSecurityPrivilege` which LocalSystem has by default).
- **`data-model.md`**: add `SecurityChannelEnabled` field to §1 `EvtSpikeConfig`. Update §4 `ChannelList` resolver signature: `ResolveChannels(cfg EvtSpikeConfig) []string` (drop the `securityOptIn bool` parameter). Delete §4's reference to the marker file.
- **`quickstart.md`**: rewrite Path C (Security opt-in) to use the config flag: edit `config.json`, set `security_channel_enabled: true`, restart DrainCtl. Verify `Security` appears in the subscribed channels log line.
- **`checklists/requirements.md`**: remove any item referencing MSI installer component for Security.

**Owner decisions remaining after A-17**: **zero**. All previously-pending decisions (R-15, R-16, R-17, R-18) are either ratified or dissolved by scope reduction.
