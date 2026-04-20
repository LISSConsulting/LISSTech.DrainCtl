<!--
Sync Impact Report
Version change: unversioned -> 1.0.0
Modified principles:
- Placeholder template -> I. Windows-First Delivery
- Placeholder template -> II. Stable Operator Surfaces
- Placeholder template -> III. Tests and Zero-Noise Verification
- Placeholder template -> IV. Config and Release Discipline
- Placeholder template -> V. Operational Observability by Default
Added sections:
- Additional Constraints
- Delivery Workflow
Removed sections:
- None
Templates requiring updates:
- ✅ .specify/templates/plan-template.md
- ✅ .specify/templates/spec-template.md
- ✅ .specify/templates/tasks-template.md
- ✅ .specify/templates/agent-file-template.md
Follow-up TODOs:
- None
-->
# LISSTech DrainCtl Constitution

## Core Principles

### I. Windows-First Delivery
DrainCtl is a Windows Server product. Every new Go source file MUST carry
`//go:build windows`, and feature plans MUST state how the change behaves on the
service, CLI, PowerShell, installer, and dashboard surfaces it touches. Cross-platform
abstractions MAY exist only when they reduce complexity inside the Windows-first
product rather than chasing unsupported portability.

Rationale: the product depends on Windows APIs such as `RegNotifyChangeKeyValue`,
`EvtSubscribe`, WTS, ETW, and Windows Service control semantics.

### II. Stable Operator Surfaces
User-visible behavior exposed through the root package, `cmd/drainctl`,
`cmd/cshared`, PowerShell commands, the Windows service, and dashboard APIs MUST stay
coherent. A feature that changes one operator surface MUST explicitly assess the other
surfaces and document whether they are updated, intentionally unchanged, or deprecated.
Breaking changes to public commands, configuration shape, or automation-facing output
MUST include a migration path in the spec and plan before implementation starts.

Rationale: operators automate DrainCtl through multiple entry points, so drift between
surfaces becomes a support and reliability issue rather than a local code decision.

### III. Tests and Zero-Noise Verification
Behavior-changing work MUST ship with automated verification at the lowest useful layer
and at the affected boundary. Unit tests are mandatory for isolated logic; integration,
contract, or end-to-end tests are mandatory when a change crosses process, storage,
service, dashboard, or configuration boundaries. Before merge, `go test ./...`,
`just lint`, and the repository pre-commit checks MUST pass without bypasses or known
warnings.

Rationale: this repository mixes Windows APIs, SQLite, IPC, and UI work; quiet tooling
and boundary-focused tests are the only reliable way to catch regressions early.

### IV. Config and Release Discipline
Configuration MUST live in the existing JSON configuration flow and reuse the current
atomic-write and scoped-update patterns; new configuration systems are forbidden unless
the constitution is amended first. Release versioning MUST remain git-derived CalVer via
the documented build pipeline, and version strings MUST NOT be hand-maintained in code
or templates that are already build-generated. Feature plans MUST state whether a change
affects runtime config, stored data, upgrade behavior, or release artifacts.

Rationale: config sprawl and hand-managed versioning create silent operational drift,
especially across service, MSI, and PowerShell packaging.

### V. Operational Observability by Default
Every feature that affects host state, telemetry, or operator decisions MUST define how
it is observed, queried, and diagnosed. Changes MUST preserve or improve structured
logging, durable telemetry, error visibility, and operator-facing inspection paths such
as CLI output, dashboard views, Event Log entries, or persisted audit history. Silent
background behavior without a diagnostic surface is prohibited.

Rationale: DrainCtl exists to explain live operational state on production RDSH hosts;
unobservable behavior undermines the product's core promise.

## Additional Constraints

- The repository is a single Go module with multiple delivery surfaces; plans MUST name
  the concrete packages, commands, and docs they touch.
- Simplicity is the default. New packages, services, or persistence layers MUST be
  justified in the implementation plan's Complexity Tracking section when a smaller
  change would not work.
- Security-sensitive work MUST state required privileges, service-account assumptions,
  secrets handling, and any user-visible setup needed to operate safely.
- Durable data changes MUST document retention, migration, rollback expectations, and
  read/write ownership when concurrent readers or writers are involved.

## Delivery Workflow

1. Specification work MUST describe user value, independent testability, edge cases,
   public-surface impact, configuration impact, and observability impact.
2. The implementation plan MUST pass the Constitution Check before research or coding
   continues. Any exception MUST be recorded in Complexity Tracking with a rejected
   simpler alternative.
3. Tasks MUST be grouped by user story, include the verification work needed to prove
   the story, and include explicit tasks for docs, observability, and config or upgrade
   changes when those concerns are in scope.
4. Runtime guidance files such as `README.md`, `CLAUDE.md`, quickstarts, contracts, and
   operator docs MUST be updated in the same change when behavior or operating guidance
   changes.

## Governance

This constitution overrides ad hoc process notes when they conflict. Amendments require
an explicit document update, a rationale for the change, and corresponding template
updates for every affected Spec Kit artifact before the amendment is considered active.

Versioning policy follows semantic versioning for governance itself: MAJOR for removing
or redefining a principle in a backward-incompatible way, MINOR for adding a new
principle or materially expanding guidance, and PATCH for clarifications or wording-only
edits. This initial ratification establishes version `1.0.0`.

Compliance review is mandatory in every specification, plan, task list, and code review.
Reviewers MUST verify constitutional alignment or record a justified exception. Runtime
implementation details live in `CLAUDE.md` and active feature plans, but those files may
not weaken the non-negotiable rules defined here.

**Version**: 1.0.0 | **Ratified**: 2026-04-20 | **Last Amended**: 2026-04-20
