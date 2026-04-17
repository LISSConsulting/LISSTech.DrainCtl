# Codex Review Synthesis — 2026-04-17

**Subject**: Spec-level validation of proposed update to 006-evtspike-detection that assumes 007-sqlite-telemetry-store is implemented.

**Proposal under review**:

1. Confirmed spike events become a new record class in the unified SQLite telemetry store, with retention. Dashboard recent-spikes list (006 FR-027) reads from the store.
2. evtspike baseline state moves from an atomic-replace JSON file (006 FR-018..FR-021) into the store as a single BLOB row keyed by `(host, version)`, UPSERT every ~15 min. Baseline snapshotting becomes a named maintenance job reusing 007 FR-029..FR-032.

**Method**: Three parallel codex reviews (design coherence / operational risk / architectural coupling), verified against both spec files.

---

## CONFIRMED — blockers (must be resolved before accepting the proposal)

| # | Issue | Evidence | Severity |
|---|-------|----------|----------|
| B1 | **Spike-event retention class is undefined.** 007 FR-010/FR-011 define retention only as metrics (1–365 d) vs. audit (1–3650 d). Spike events are neither. Without an explicit policy, implementations could pick any bucket, accidentally prune all incident history, or grow unbounded. | 007 FR-010, FR-011; 006 Spike Event entity | BLOCKER |
| B2 | **Baseline row must be exempt from retention.** If the baseline BLOB is treated as a normal retained record, 007 FR-012 retention could delete the only durable snapshot, silently defeating 006 SC-005 warm-restart. | 007 FR-010..FR-012; 006 FR-018, SC-005 | BLOCKER |
| B3 | **006 FR-018/FR-020/FR-022/FR-033 literally require a "file on disk", "baseline file path" config, and atomic file replacement.** These become false under the proposal. The "Baseline State File" key entity also describes an on-disk JSON artifact. Spec text needs rewrite, not just reinterpretation. | 006 FR-018, FR-020, FR-022, FR-033; 006 Key Entity "Baseline State File" | BLOCKER |
| B4 | **Startup sequencing is unspecified.** Moving baseline into SQLite forces "store-open and migrations-complete" before evtspike Start. Today evtspike can start from a file (or start fresh on missing/corrupt). 007 FR-020 mandates legacy JSONL migration before new events; under the proposal, baseline warm-start queues behind that gate too. | 006 FR-018, FR-019; 007 FR-020 | BLOCKER |
| B5 | **Disk-full baseline behavior is unspecified.** 007's disk-full edge case only covers "new samples and audit events are dropped." Baseline UPSERT failure under disk pressure is undefined — does the subsystem drop the checkpoint, reset, or keep evolving in memory? | 006 FR-018..FR-020; 007 edge case "Disk full during write" | BLOCKER |

## CONFIRMED — high

| # | Issue | Evidence | Severity |
|---|-------|----------|----------|
| H1 | **Corruption recovery (`.corrupt-<ts>.bak`) doesn't map to rows.** 006 FR-019 + edge case specify file rename + start-fresh. For a corrupt BLOB row, "discard and start fresh" carries the intent; the named backup artifact does not. If the whole DB is corrupt, this overlaps with 007's database-corruption edge case, not 006's. | 006 FR-019; 006 Edge Case "baseline state file becomes corrupt" | HIGH |
| H2 | **007 Assumptions defers schema migrations beyond initial import.** Two new schema elements (`spike_events` table, baseline-state row/table) must be part of 007's *initial* schema — or 007's Assumption must be revised. The proposal depends on a capability 007 explicitly defers. | 007 Assumptions "Schema migrations beyond initial import are out of scope"; proposal parts (1) and (2) | HIGH |
| H3 | **Engine boundary must hide SQLite from the scoring engine.** The proposal is only architecturally safe if baseline persistence sits behind an engine-owned interface, and the scoring engine itself never opens SQLite, knows table names, or performs UPSERTs. Otherwise the "reusable scoring engine" architectural note is materially violated. | 006 Architectural Note ("Engine-owned cross-cutting concerns"); 006 "Structured observation interface" | HIGH |
| H4 | **Release-calendar coupling with 007.** Under the proposal, evtspike cannot ship without 007. Today evtspike can persist its baseline independently. This is a real tradeoff that should be stated as an explicit dependency, not implied. | 006 FR-018..FR-021; 007 FR-029..FR-032 | HIGH |

## CONFIRMED — medium

| # | Issue | Evidence | Severity |
|---|-------|----------|----------|
| M1 | **007 FR-031 2× threshold = 30-min warning for a 15-min baseline_snapshot cadence.** That may cry wolf for a best-effort checkpoint where the in-memory detector is still healthy. Either raise the snapshot cadence, or exempt baseline_snapshot from the generic 2× rule. | 007 FR-031; 006 FR-018 | MEDIUM |
| M2 | **Spike Event mutation semantics unstated.** Once Spike Events become durable records used by FR-027's dashboard, the spec should say whether they are immutable (only pruned by retention) or editable/annotatable. | 007 FR-001b (audit immutability); 006 FR-011, Key Entity "Spike Event" | MEDIUM |
| M3 | **006 Spike Event entity says "Delivered to the notification pipeline in-process"** — silent on persistence. Needs an explicit clause that confirmed spikes are also persisted to the telemetry store. | 006 Key Entity "Spike Event"; 006 FR-027 | MEDIUM |
| M4 | **Engine test surface may require SQLite fixtures.** If baseline persistence is intrinsically SQL-backed, testing scoring math (slot maturity, fallback, confirmation) will bring in DB fixtures. Reinforces H3: persistence must be pluggable so engine tests use an in-memory fake. | 006 Architectural Note; 006 SC-001..SC-007 | MEDIUM |
| M5 | **FR-033 "single baseline state file" intent beyond wording.** "Single file" also encoded *inspectability, copyability, operator-visible artifact*. Moving to SQLite loses those properties. The rewording should preserve the intent ("one detector state per host") and acknowledge the ops-legibility tradeoff. | 006 FR-033; 006 Key Entity "Baseline State File"; 006 FR-028 | MEDIUM |

## CONFIRMED — low (state as assumption / tradeoff)

| # | Issue | Note |
|---|-------|------|
| L1 | **AV-lock / transient EACCES blast radius broadens from file to whole DB** (B5 from Lens B). Inherited from the choice to use SQLite at all; 007 already accepts this tradeoff. Worth stating explicitly that baseline no longer degrades independently. |
| L2 | **Future perf-counter adapter sharing 007 compounds coupling** (Lens C concern 2). Symmetric with evtspike's use of 007, but makes 007 the durable model-state backend, not just telemetry history. Not a blocker — but the architectural note should acknowledge it. |
| L3 | **Ops-legibility loss** (Lens C concern 6). Moderate: 006 FR-028 already excludes baseline inspection tools from MVP, so losing the "copy the JSON file" workflow is tolerable. |

## REJECTED — no action needed

| # | Claim | Reason |
|---|-------|--------|
| R1 | BLOB-in-a-row is incoherent / should normalize to ~5000 rows. | Codex itself rejected this. Baseline is engine-owned snapshot state, not dashboard-queryable history. A BLOB preserves whole-snapshot semantics closer to today's JSON file than normalization would. Normalization would invent a goal neither spec states. |
| R2 | Single-writer lock saturation at the proposed cadences. | Codex Lens B explicitly ran the write mix and said no — 15–30 s metric writes, 10 s evtspike writes, 135 KB BLOB UPSERT every 15 min is not close to SQLite's write ceiling. Real contention risk sits in long-running aggregation/retention transactions, already covered by 007 FR-006. |
| R3 | Crash-during-UPSERT leaves inconsistent baseline. | Codex confirmed that a single-statement UPSERT under SQLite WAL is atomic in the same sense as today's rename: either previous row or next row, never half. |
| R4 | Reusing 007 maintenance-job widget for baseline_snapshot stretches the surface. | Codex confirmed this is exactly what 007 FR-029..FR-032 is for. Only caveat is M1 (the 2× warning threshold). |
| R5 | Spike-event persistence as a new record class violates 007's immutability. | Codex confirmed 007 FR-001b immutability is scoped to audit records only. Spike events as a new class don't touch that guarantee. |

---

## Bottom line

The **spike-event persistence** half of the proposal (part 1) is architecturally sound — the only gaps are B1 (retention class) and M2/M3 (mutation semantics wording).

The **baseline-in-SQLite** half of the proposal (part 2) is defensible but carries more weight:

- Five blockers (B1–B5), four of which are part-2-specific (B2–B5).
- The strongest structural constraint is **H3**: the scoring engine must not know SQLite. If this is honored via a baseline-store interface, the reusable-scoring-engine architectural note survives; if not, the proposal actively damages 006's future extensibility.
- **H2** (schema-migrations-deferred in 007) is a cross-spec concern: 007 needs to include both new schemas in its initial design or relax its Assumption.

**If the blockers above can be cleanly addressed in the spec updates, the proposal is acceptable.** Otherwise the safer path is to retain the JSON baseline file and only adopt part (1).
