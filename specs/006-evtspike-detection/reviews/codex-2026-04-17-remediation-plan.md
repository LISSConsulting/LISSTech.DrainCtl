# Spec-Update Plan: Codex Review 2026-04-17 Remediation

## Position statement

**Recommendation: proceed with Part (1) only. Drop Part (2).**

The synthesis itself concedes the decision point: *"If the blockers above can be cleanly addressed... the proposal is acceptable. Otherwise the safer path is to retain the JSON baseline file and only adopt part (1)."*

Weighing the evidence:

- **Part (1) still carries real spec-coherence work**: B1 (retention class), H2 (007 must commit the spike-event table in its initial schema — this is a live concern under Part (1), not only Part (2)), H3 (engine vs. persistence-sink boundary — also live under Part (1) because confirmed spike events are now persisted), H4 (007 becomes a hard dependency of 006 FR-027), M2 (spike-event immutability wording), M3 (spike-event persistence clause), plus a new disk-full gap in 007's edge cases for spike-event writes.
- **Part (2) adds on top**: 4 more blockers (B2–B5), 2 highs (H1, plus a narrower form of H3 for baseline), 2 mediums (M1, M5), and a rewrite of 006 FR-018/FR-020/FR-022/FR-033 + the Baseline State File entity.
- **What Part (2) buys**: ops-legibility loss (M5, L3), no operator-visible value, a modestly simpler persistence story inside evtspike. The synthesis rejects R2 (write-saturation) and R3 (atomicity) — so Part (2) is *feasible*, not *valuable*.
- **Asymmetric reversibility**: Part (1) is additive — persisting spike events to 007 doesn't preclude later moving the baseline too. Part (2) cannot easily be reversed later without another migration path.

Part (1)-only is still the right call, but **it is not free**. This plan commits 11 edits across both specs (7 in Phase 1 touching 007; 4 in Phase 2 touching 006), and several of them are structural rather than cosmetic.

Part (2)-specific concerns (B2, B3, B4, original-baseline B5, H1, M1, M5, L1, L2, L3) are deferred — see "Concerns not addressed" at the end.

---

## Phase 1 — Prerequisite: 007 schema commitment for spike events (unblocks B1, H2-partial)

### Edit 1A: 007 Assumptions — narrow the "schema migrations deferred" statement

- **Spec file**: `drainctl/specs/007-sqlite-telemetry-store/spec.md`
- **Section**: Assumptions, bullet *"Schema migrations beyond the initial import are out of scope..."*
- **Change gist**: Commit 007's **initial schema** to include a dedicated spike-event record class/table, alongside the existing audit, metric-sample, hourly-aggregate, migration-marker, and maintenance-job-run elements. The narrowed assumption reads: "Schema migrations beyond the initial import are out of scope; the initial schema explicitly includes audit records, metric samples, hourly aggregates, and confirmed spike events. Adding new counters or columns later is handled by a follow-up." Do NOT claim a three-table or N-table total — 007 already has multiple tables/entities, and stating a fixed count risks a regression against existing entities (Migration Marker, Maintenance Job Run, Retention Policy, etc.).
- **Verification**: Structural check — confirm 007's Key Entities list, Durable Storage FRs, Retention FRs, Query API FRs, and Edge Cases all treat spike events as a first-class initial-schema element. Not grep-only.
- **Effort**: S.

### Edit 1B: 007 FR-010 / FR-011 — add spike-event retention class (addresses B1)

- **Spec file**: 007 spec.md
- **FRs to modify**: FR-010, FR-011
- **Change gist**: Add a third retention class for **confirmed spike events**, range 1–365 days, default 90. New FR (FR-011c) stating that spike-event records are retained independently of metrics and audit and are deleted only by the bulk retention job on age.
- **Verification**: 007 FR-010/FR-011 name three classes explicitly. 006 FR-027 can be cross-referenced to this retention class. "Every record class has a retention bound" passes.
- **Effort**: S.

### Edit 1C: 007 Key Entities — add "Spike Event Record" entity

- **Spec file**: 007 spec.md
- **Section**: Key Entities
- **Change gist**: Add a **Spike Event Record** entity: attributes (timestamp, host, channel, observed count, expected count, confirmation window start/end), immutable after insertion, retained per its own policy. Explicitly state that it is **not** an Audit Record and FR-001b immutability for audit doesn't apply — immutability is repeated here as a separate commitment (addresses M2).
- **Verification**: 007's Key Entities list now contains Spike Event Record in addition to its existing entities (Audit Record, Metric Sample, Hourly Metric Aggregate, Retention Policy, Migration Marker, Maintenance Job Run). The new entity cross-references from 006's own Spike Event entity.
- **Effort**: S.

### Edit 1E (NEW): 007 Durable Storage — add FR that system MUST persist confirmed spike events, with commit semantics

- **Spec file**: 007 spec.md
- **Section**: Functional Requirements → Durable Storage (currently FR-001..FR-004)
- **Change gist**: Add a new FR (FR-002a or equivalent) that parallels FR-001 (audit) and FR-002 (metric samples): *"The system MUST persist every confirmed spike event produced by the evtspike subsystem as a discrete record containing at minimum: timestamp, host, channel, observed count, expected count, confirmation window start/end. Each spike event MUST be committed durably (fsync-equivalent, respecting WAL semantics) before the sink acknowledges the handoff to the detector. Batching is permitted only within a single transaction; async-fire-and-forget that can lose confirmed events on crash is NOT permitted."* This explicitly closes the async-batch-loss-on-crash gap that a bare "MUST persist" FR would leave open, and brings spike-event durability in line with FR-003's "committed records MUST survive restart."
- **Verification**: 007's Durable Storage section has storage requirements for all three record classes with explicit commit-timing semantics. 006 FR-027 traces through a real 007 write FR with defined durability.
- **Effort**: S.

### Edit 1F (NEW): 007 Edge Cases — cover disk-full for spike-event writes

- **Spec file**: 007 spec.md
- **Section**: Edge Cases, bullet "Disk full during write"
- **Change gist**: Update the bullet to name spike events alongside samples and audit events: *"new samples, audit events, and spike events should be dropped with a logged warning; the service must continue running..."* Without this, adding a third record class leaves its disk-full behavior unspecified.
- **Verification**: Edge case enumerates all three record classes. Cross-check: no record class has undefined disk-full behavior.
- **Effort**: S.

### Edit 1G (NEW, iteration-3 finding): 007 — define transient DB-unavailable behavior for spike-event writes

- **Spec file**: 007 spec.md
- **Sections**: Edge Cases, plus a cross-reference clause on the new FR-002a (Edit 1E).
- **Change gist**: Add a new Edge Case bullet *"Database temporarily unavailable during spike-event write (AV lock, SQLITE_BUSY, transient EACCES): the spike-event write is retried with a bounded exponential backoff (max 3 attempts over ≤2 seconds). If all attempts fail, the event is dropped with a logged warning and the FR-027 recent-spikes list reflects the gap. Detection, scoring, confirmation, cooldown, and notification delivery MUST NOT block on spike-event persistence — in-process notification delivery proceeds regardless of persistence outcome."* Also add a clause to FR-002a (1E) clarifying that "commit before handoff" applies to the successful-commit path; the bounded-retry-then-drop fallback above is the failure path. This prevents commit-before-ack from stalling the detector under transient DB pressure (addressing the iteration-3 FR-006/SC-007 soft concern).
- **Verification**: Explicit failure path for every write FR. Detection loop never blocks on the DB. Cross-reference: 007 FR-006 (maintenance must not block ingest) now also holds for spike-event writes symmetrically.
- **Effort**: S.

### Edit 1D: 007 FR-016 / Query API — add spike-event query surface

- **Spec file**: 007 spec.md
- **FRs to modify**: Add sibling FR-016a in Query API subsection.
- **Change gist**: Requirement for querying spike events by host and time range, newest-first, parallel to FR-016 for audit. This is what 006 FR-027's recent-spikes list will call.
- **Verification**: 006 FR-027 reads trace to an explicit 007 query FR. Cross-spec grep: 006 "recent spikes" → 007 FR-016a citation.
- **Effort**: S.

---

## Phase 2 — 006 wording updates for spike-event persistence

### Edit 2A: 006 Spike Event key entity — add persistence clause (addresses M3)

- **Spec file**: `evtspike-poc/specs/006-evtspike-detection/spec.md`
- **Section**: Key Entities, the Spike Event bullet.
- **Change gist**: Append one sentence stating that confirmed Spike Events are **both** delivered to the in-process notification pipeline **and** persisted to the unified telemetry store (007) as a Spike Event Record, retained per the store's spike-event retention policy. Note immutability (records are not edited or annotated after insertion; retention job is the only deletion path) — folds M2 in.
- **Verification**: Grep 006 for "persisted" near "Spike Event" → matches. Cross-spec: 006's entity description references 007's Spike Event Record entity.
- **Effort**: S.

### Edit 2B: 006 FR-027 — name the source of the recent-spikes list

- **Spec file**: 006 spec.md
- **FR to modify**: FR-027.
- **Change gist**: Add a clarifying clause: the recent-spikes list is backed by the unified telemetry store (007), so it survives service restart and is not limited to in-memory retention.
- **Verification**: FR-027 cites 007 as its data source. SC-005 (warm restart) implicitly benefits.
- **Effort**: S.

### Edit 2C: 006 Assumptions + FR-027 + User Story 5 — declare 007 as a hard dependency for spike persistence

- **Spec file**: 006 spec.md
- **Sections**: Assumptions; FR-027; **all User Story 5 locations that reference the recent-spikes list**; **Clarifications session entry at ~line 13**.
- **Change gist**: Commit to **007 as a hard dependency** for spike-event persistence and the recent-spikes list. All five coordinated changes must land together, or the spec contradicts itself on 007-absent behavior:
  1. Add an Assumption bullet: *"006 depends on the unified telemetry store (007) being present for spike-event persistence and the dashboard recent-spikes list. Running 006 on an installation that has not yet been upgraded to include 007 is out of scope for this feature; such installations lose the recent-spikes list surface, but core detection and notification delivery continue to operate."*
  2. Add one sentence to FR-027 clarifying that the list surface is only available when 007 is present; absence is logged once at startup and the server detail view omits the list rather than showing empty or erroring.
  3. Update **User Story 5 acceptance scenario 5** (~line 93) with the same conditional.
  4. Update **User Story 5 story prose** (~line 81: *"clicking into a server reveals a short list of recent spikes for that host"*) with the same conditional.
  5. Update **User Story 5 Independent Test** (~line 85: *"Inject a confirmed spike — confirm it appears in the recent-spikes list"*) to note the test requires 007 to be present.
  6. Update the **Clarifications session entry at ~line 13** that currently states recent-spikes unconditionally (*"plus a recent-spikes list for the selected server"*) — add the same 007-present qualifier so the clarification record matches the FR.
- **Verification**: FR-027, US5 story prose, US5 Independent Test, US5 acceptance scenario 5, and the Clarifications entry all agree on 007-absent degraded behavior. 006 Assumptions references 007 once, in a scoped way. Baseline persistence FRs (FR-018..FR-021) remain untouched.
- **Effort**: S (five small coordinated text edits in one file).

### Edit 2D (NEW): 006 — promote Spike Event Sink to a first-class FR + Key Entity (addresses H3, M4)

- **Spec file**: 006 spec.md
- **Sections**: Functional Requirements (new subsection or under "Notification integration"); Key Entities; Architectural Note.
- **Change gist**: Three coordinated additions, not just an Architectural Note sentence:
  1. **New Key Entity**: *"Spike Event Sink — an internal in-process subscriber to the engine's confirmed-spike output. Responsible for durable persistence (via 007), dashboard delivery, and notification dispatch. Multiple sinks MAY subscribe; the scoring engine is indifferent to sink identity. The telemetry-store writer is one such sink."*
  2. **New FR**: *"Confirmed spike events MUST be emitted through an internal sink interface. The scoring engine MUST NOT write to the telemetry store directly and MUST NOT import or reference the telemetry-store persistence layer. Engine unit tests MUST be runnable with an in-memory sink — no SQLite fixture required."*
  3. **Architectural Note clarification**: update the "Engine-owned cross-cutting concerns" bullet and "Source-agnostic spike output" bullet to reference the Spike Event Sink entity explicitly, and state that future adapters (perf-counter, custom metrics) inherit the same sink contract.
- **Verification**: FR-level commitment, not just prose. Engine can be unit-tested without SQLite fixtures (preserves M4). Future perf-counter adapter inherits the sink contract automatically.
- **Effort**: S (still <1 hr; three small coordinated insertions).

---

## Phase 3 — Cross-spec consistency check (verification only)

### Verification 3A: confirm Part (2) is not implied anywhere

- **Action**: After Edits 1A–2D, search both specs for any wording that could imply baseline-in-SQLite, not just the specific implementation terms. Check for: "baseline BLOB", "baseline row", "baseline_snapshot job", "UPSERT baseline", "baseline stored in telemetry store", "detector state persisted in database", "baseline in the store". FR-018 through FR-022, FR-033, and the Baseline State File key entity in 006 must remain unchanged (byte-identical or minor prose only).
- **Effort**: S.

### Verification 3B: retention class completeness (structural)

- **Action**: Confirm 007 has three retention classes with explicit range and default, and each record class 006 produces (spike events) traces end-to-end: write FR (1E) → entity (1C) → retention FR (1B) → query FR (1D) → disk-full edge case (1F). Verify 006 FR-027 cites the corresponding 007 FRs, not just the concept.
- **Effort**: S.

### Verification 3C: engine-vs-sink boundary holds (structural contract, not re-read)

- **Action**: With Edit 2D's FR and Key Entity in place, define and record three concrete implementation-phase acceptance criteria that future code must satisfy (these do not land as code yet — they become part of the plan handed to implementers):
  1. The scoring-engine package/module MUST NOT import, reference, or link against the telemetry-store package or any SQLite driver. Enforceable via a lint rule or package-boundary test in the eventual implementation.
  2. Engine unit tests MUST run with an in-memory sink (fake) and MUST NOT require a SQLite fixture, filesystem access, or the 007 DB file.
  3. The telemetry-store writer MUST be implementable as a sink subscriber, and swapping it for a no-op sink MUST leave the engine functionally unchanged (confirmation/cooldown/scoring all proceed; only persistence/recent-spikes surface goes dark).

  These are structural contracts on the eventual code, not just spec prose. They make H3/M4 concretely verifiable at implementation time.
- **Effort**: S.

---

## Concerns explicitly NOT addressed (and why)

| # | Concern | Reason for deferring |
|---|---------|----------------------|
| B2 | Baseline row retention exemption | N/A — baseline stays in JSON file under Part (1)-only. |
| B3 | FR-018/020/022/033 file→row rewrite | N/A — no rewrite needed. |
| B4 | Startup sequencing for baseline | N/A — baseline load path unchanged. |
| B5 | Disk-full baseline behavior | N/A — covered by existing 006 FR-019 since baseline remains a file. |
| H1 | `.corrupt-<ts>.bak` rename semantics | N/A — baseline file corruption path preserved. |
| H3 | Engine boundary hiding SQLite | **Now addressed by Edit 2D** (promoted from "optional" to mandatory). Without 2D, an implementation could legally have the engine write SQLite directly. 2D makes the sink/engine split explicit spec text. |
| H4 | Release-calendar coupling | **Now addressed by Edit 2C** (strengthened from "scoped dependency" to "hard dependency with defined degraded behavior"). Installations without 007 lose the recent-spikes list; core detection/notification continue. |
| M1 | 007 FR-031 2× warning for baseline_snapshot | N/A — no baseline_snapshot job under Part (1)-only. |
| M4 | Engine test surface / SQLite fixtures | N/A — baseline stays file-based. Spike-event emission is in-memory handoff; persistence sink is test-substitutable. |
| M5 | "Single baseline state file" intent | N/A — FR-033 wording preserved. |
| L1 | AV-lock blast radius to whole DB | Baseline-specific L1 is N/A (baseline stays on disk). **Spike-event persistence inherits 007 DB availability**: a transient DB-wide access failure (AV lock, SQLITE_BUSY) affects spike writes and the FR-027 list surface. Disk-full behavior for spike events is covered by Edit 1F; transient AV-lock is covered by 007's existing availability model — no new 006 edit needed beyond Edit 2C's degraded-behavior statement. |
| L2 | Future perf-counter adapter sharing 007 | Out of scope — future-work concern. |
| L3 | Ops legibility loss | N/A — no legibility loss under Part (1)-only. |

---

## Summary

**Total: 11 small edits across 2 spec files, plus 3 verification passes. Estimated effort: ~4–6 hours.**

- Phase 1 (007 edits, prerequisite): 1A, 1B, 1C, 1D, 1E, 1F, 1G — seven edits.
- Phase 2 (006 edits, dependent on Phase 1): 2A, 2B, 2C, 2D — four edits.
- Phase 3 (verification): 3A, 3B, 3C — three structural checks.

**Phase ordering is sequential, not parallel.** Edits 2A, 2B, 2C reference 007 concepts (Spike Event Record entity, retention class, write FR, query FR) that do not exist until 1A–1F land.

**Within-phase ordering for Phase 1**: 1C (Key Entity) first, then 1A/1E/1B/1D/1F/1G (all reference the entity). Specifically:
- 1C defines Spike Event Record as a 007 Key Entity.
- 1A narrows the schema-migrations assumption to include the entity.
- 1E adds the Durable Storage FR that writes the entity.
- 1B adds the retention class for the entity.
- 1D adds the query FR over the entity.
- 1F updates the disk-full edge case to name the entity.
- 1G adds transient-DB-unavailable behavior for the entity's writes.

Drafts can be written in parallel, but merge/acceptance must be: 1C → 1A/1E/1B/1D/1F/1G → (Phase 2: 2A → 2B/2C/2D) → (Phase 3 verification).

If anyone proposes re-introducing Part (2), blockers B2–B5 and highs H1 (plus the stronger form of H3 and H4) become live again — defer to follow-up spec.
