# Feature Specification: Security and Correctness Hardening (009)

**Feature Branch**: `009-security-hardening`
**Created**: 2026-04-24
**Status**: Draft
**Input**: Remediation plan at `docs/reviews/codex-2026-04-24-fullcodebase-remediation-plan.md` (authoritative for scope, files, and verification). Synthesis at `docs/reviews/codex-2026-04-24-fullcodebase-synthesis.md` (issue evidence). 16 active steps across 4 phases, scope-pruned for on-prem greenfield Windows context.

## User Scenarios & Testing *(mandatory)*

<!--
  This feature is a remediation batch rather than a net-new user-facing capability.
  User stories are grouped by operational outcome, prioritized by fleet-protection value.
  Each story is a standalone PR that can ship, be tested, and be reverted independently.
-->

### User Story 1 - Fail-closed on dashboard cert tampering and cleartext SMTP auth (Priority: P1)

A fleet operator has drainctl agents pinned to a dashboard over HTTPS and is using an authenticated SMTP relay for email notifications. Today, if someone silently replaces the dashboard's TLS certificate between re-registrations, the agent quietly overwrites its pinned fingerprint and keeps going. Separately, if a MITM strips the STARTTLS offer from the SMTP server's EHLO response, drainctl will happily send `AUTH PLAIN` credentials in the clear. This story closes both doors.

**Why this priority**: These are the two paths that leak or overwrite long-lived credentials in production — a compromised dashboard cert unlocks the entire fleet's audit and config plane, and leaked SMTP credentials are reusable against anything that relay touches. Both are pre-release blockers for external installs.

**Independent Test**: Stand up a dashboard, register an agent, rotate the dashboard cert without clearing the saved fingerprint on the agent, re-run register — the operation fails with a clear mismatch error and the saved fingerprint is unchanged. Separately, run a test SMTP server that advertises no STARTTLS while drainctl has `target.Secret != ""`; the send fails with a refusal error naming the host.

**Acceptance Scenarios**:

1. **Given** an agent with a pinned dashboard fingerprint, **When** the dashboard's TLS cert changes and `drainctl register` runs again, **Then** register fails with an error naming both fingerprints and `config.json` is not modified.
2. **Given** an SMTP target with non-empty `Secret`, **When** the server does not advertise STARTTLS or STARTTLS fails, **Then** drainctl refuses to send and returns an error that names the host and tells the operator to use `smtps://` or a STARTTLS-capable server.
3. **Given** an SMTP target with empty `Secret` (internal unauthenticated relay), **When** the server does not advertise STARTTLS, **Then** drainctl sends unencrypted and emits a `slog.Warn` naming the host.

---

### User Story 2 - Narrow credential blast radius on the agent host (Priority: P1)

Today, `config.json` grants `Modify` to the `SERVICE` group, and drainctl encrypts its SMTP/webhook secrets with DPAPI at machine scope with **zero** entropy. Any other service on the host — including third-party services that have no business reading drainctl's config — can read the file and decrypt the secrets. This story flips both surfaces to fail-closed defaults: drop `SERVICE` from the ACL, and pass a fixed entropy blob into `CryptProtectData`/`CryptUnprotectData` so only drainctl itself can round-trip its own ciphertexts.

**Why this priority**: Lateral credential theft from a compromised third-party service on the same box is a realistic threat on Windows fleets (agents often share hosts with monitoring, backup, or vendor services). Greenfield — no deployed agents hold old ciphertext — so the DPAPI change is a flat flip with no migration complexity.

**Independent Test**:
- After a fresh install, `icacls "%ProgramData%\LISS Technologies\LISSTech DrainCtl\config.json"` shows only SYSTEM and Administrators. `S-1-5-6` is absent.
- A plaintext secret round-tripped through `DPAPIEncrypt`/`DPAPIDecrypt` matches; a ciphertext produced with a different entropy constant fails to decrypt.

**Acceptance Scenarios**:

1. **Given** a fresh install on an elevated dev box, **When** the service creates `config.json`, **Then** the ACL grants SYSTEM and Administrators only; `SERVICE` has no grant.
2. **Given** a stored webhook secret, **When** the service re-reads it after restart, **Then** the secret decrypts correctly with the new entropy scheme and notifications fire as expected.
3. **Given** a ciphertext crafted with a different entropy constant, **When** drainctl attempts to decrypt, **Then** the decrypt returns an error and the in-memory secret is blanked (existing behavior preserved).

---

### User Story 3 - Long-running RPC and event pipelines stop silently breaking (Priority: P1)

Three latent correctness bugs hide in pipelines the operator can't see directly. First, the evtspike subsystem promises (in its own doc comment) to fire a `loss` callback when its event-log subscription dies so the supervisor can re-subscribe; the implementation forgets to call it, so a dead subscription stops counting forever. Second, multiple concurrent handlers can read-modify-write `config.json` in overlapping windows; a named mutex guards the save step but not the whole read-then-save cycle, so two updates racing will have one silently overwrite the other. Third, the service-side of the named pipe uses a single 4 KB read and treats any overflow as fatal; the client side already handles the chunked-read path. This story fixes all three so the supervisory assumptions hold under load.

**Why this priority**: These are high-impact correctness failures with low visibility — symptoms look like "detection got quiet" or "that config change didn't stick." They undercut trust in every other fix in this batch. All three are small, testable, and independent.

**Independent Test**:
- Close an evtspike subscription handle out from under `Subscribe`; the `loss` callback fires within 2 s with a non-nil error.
- Run 20 goroutines concurrently updating the session threshold to distinct values; the final on-disk JSON is valid and equals exactly one of the written values.
- Send a >4 KB request across the pipe using a scripted reader that yields `ERROR_MORE_DATA`; the server reassembles the full message.

**Acceptance Scenarios**:

1. **Given** an evtspike subscription that fails with a terminal EvtNext error, **When** the error occurs, **Then** the `loss` callback fires with the wrapped error and the supervisor re-subscribes.
2. **Given** two concurrent config updaters touching different fields, **When** both commit, **Then** both changes are present in the final `config.json`.
3. **Given** a pipe request larger than 4 KB, **When** the service reads it, **Then** the full message is delivered to the handler intact.

---

### User Story 4 - Close medium-severity security paper cuts (Priority: P2)

Three smaller hardening items that don't individually protect against a named incident but collectively remove easy pivots: (a) verify the caller SID on privileged pipe verbs (`register`, `remove-server`, `baseline-reset`) so a future DACL regression doesn't silently lower the bar; (b) swap `text/template` for `html/template` in email rendering so crafted event-log fields can't inject HTML into an admin's inbox; (c) parse the event's `SystemTime` for registry-change attribution instead of wall-clock `time.Now()` so a buffered delayed event doesn't get stamped to the wrong transition.

> **Note (2026-04-24)**: an earlier revision of this story included a fourth item — reject `http://169.254.169.254` as a notification target. **Withdrawn** during 009 codex post-review: the literal-string check was bypassable via IPv6-mapped, decimal/hex IPv4, trailing-dot, and 302 redirect (Go's default HTTP client follows redirects). Hedge value was zero while the spec implied a guarantee we couldn't deliver. See `docs/reviews/codex-2026-04-24-009-branch-remediation.md` Step 1.

**Why this priority**: Each item is defense-in-depth or attribution accuracy — not a currently-exploitable hole, but cheap to fix and each removes one assumption an attacker or a bug could lean on later.

**Independent Test**:
- From a non-admin shell, `drainctl register https://dash/` is denied over the pipe; from the same shell, `drainctl status` still works.
- An email rendered with `Message: "<script>alert(1)</script>"` produces output containing `&lt;script&gt;` and not the raw tag.
- Feeding a synthetic event with `SystemTime="2026-04-24T10:00:00Z"` makes `LatestAttribution().Timestamp` equal the parsed time.

**Acceptance Scenarios**:

1. **Given** a non-admin caller, **When** they invoke a privileged pipe verb, **Then** the server returns `access denied` and logs `pipe=access_denied` with the caller's SID; read-only verbs still succeed.
2. **Given** an event-log payload that could contain HTML, **When** the notification email renders, **Then** angle brackets and entities are escaped.
3. **Given** a registry-change event that arrives 10 s late, **When** drainctl records the attribution, **Then** the stored timestamp matches the event's own `SystemTime` and `WaitAttribution(after=eventTime+5s, …)` correctly ignores it.

---

### User Story 5 - Remove drift in the public API and internal layout (Priority: P3)

Six cleanup items that remove dead code, stale naming, and growing god-files without changing behavior: (a) delete four orphan `Update*` config helpers that only their own tests call; (b) move ETW event IDs out of the root `drainctl` package into `internal/etwids/`; (c) retire the "JSONL audit trail" API surface — rename `DefaultAuditPath` to `DefaultDBPath`, widen `GetHistory` to accept either a file or dir path; (d) split `internal/svc/handler.go` (1138 lines mixing pipe RPC, dashboard sync, perf, evtspike, lifecycle) into topical siblings; (e) extract two duplicated blocks (status-transition logging, sparkline append) from `App.svelte` into shared helpers in `state.svelte.js`; (f) delete orphan `deriveP95`/`deriveP50` exports in `state.svelte.js`.

**Why this priority**: Pure hygiene — none of these change behavior, fail closed, or protect the fleet. They exist because the codebase is big enough now that dead exports and god-files are starting to tax every adjacent fix. Lowest priority, trailing PR. Step (a) must land after Story 3's atomic RMW refactor — deleting the helpers before they're rewritten creates merge churn.

**Independent Test**: `go build ./... && go test ./...` passes before and after each step; `git diff --stat` on the handler split shows near-zero net line delta (pure moves).

**Acceptance Scenarios**:

1. **Given** the post-refactor `config.go`, **When** a caller looks up `UpdateSessionThreshold` / `UpdateGracePeriod` / `UpdateNotifications` / `UpdatePerformanceConfig`, **Then** the symbol is absent; `UpdateNotifySettings`, `UpdateEvtSpikeEnabled`, and `InstallCertificate` remain.
2. **Given** the split `internal/svc/handler_*.go` files, **When** `go build ./...` and `go test ./internal/svc/...` run, **Then** both pass and no public signatures change.
3. **Given** `DefaultDBPath()` returning `drainctl.db`, **When** `drainctl history --limit 1` runs against an existing telemetry database, **Then** history is returned correctly.
4. **Given** a forced drain-mode transition, **When** both the poll loop and the SSE handler observe it, **Then** the transition is logged exactly once via the shared helper.

---

### Edge Cases

- **Dashboard cert rotation with no operator intervention**: Once a fingerprint is pinned, `drainctl register` refuses to re-pin automatically. Operators must manually clear `Dashboard.TLSFingerprint` in `config.json` (or use a to-be-added future flag) before re-registering against a rotated cert. This is the intended behavior but needs release-notes coverage.
- **Internal SMTP relay on port 25 that accepts AUTH PLAIN without TLS**: Breaks after Story 1. No workaround in this release — migration path is to move the relay to STARTTLS (587) or smtps:// (465). Operators running such a relay must be warned.
- **Existing agents with DPAPI ciphertexts from the old scheme**: None exist (greenfield). If an agent somehow shipped before this story lands, its notification secrets would blank on first load and the operator would need to re-enter them — acceptable, but motivates the pre-external-release requirement.
- **Pipe caller on a machine with group-membership quirks**: `Token.IsMember(adminSID)` handles filtered tokens (UAC) correctly; manual test from a non-admin-of-admin-group user verifies the path.
- **Event-log SystemTime in a locale or TZ format we can't parse**: Fallback to `time.Now()` with a `slog.Warn` preserves behavior rather than dropping attribution; no data loss.
- **Legacy `DefaultAuditPath()` callers after rename**: The old symbol is kept as a `// Deprecated:` alias pointing at `DefaultDBPath()` in this release; actual removal is deferred one release cycle per the plan.

## Requirements *(mandatory)*

### Functional Requirements

**Phase 1 — Security hardening**

- **FR-001**: On `register`, the CLI and service MUST refuse to overwrite a non-empty `Dashboard.TLSFingerprint` when the server offers a different non-empty fingerprint; the refusal MUST name both values and MUST NOT modify `config.json`.
- **FR-002**: When an SMTP notification target has a non-empty `Secret` and the server does not successfully complete `STARTTLS`, drainctl MUST refuse to transmit `AUTH` and MUST return an error identifying the host.
- **FR-003**: When an SMTP notification target has an empty `Secret` and the server does not advertise STARTTLS, drainctl MAY send in the clear but MUST emit a `slog.Warn` naming the host.
- **FR-004**: `restrictConfigACL` MUST grant `config.json` only to `SYSTEM` and `Administrators`. The `SERVICE` (`S-1-5-6`) grant MUST be removed and explicitly cleared on repeat installations.
- **FR-005**: `DPAPIEncrypt` and `DPAPIDecrypt` MUST pass a fixed non-empty entropy constant (`LISSTech.DrainCtl/v1/notify-secret`) to `CryptProtectData`/`CryptUnprotectData`.

**Phase 2 — Correctness**

- **FR-006**: When `EvtNext` returns a terminal error inside `internal/evtspike/subscriber_windows.go:Subscribe`, the function MUST invoke the `loss` callback with a non-nil wrapped error and MUST return, enabling the supervisor's re-subscription path.
- **FR-007**: The config package MUST provide an atomic read-modify-write primitive used by every scoped updater (`UpdateNotifySettings`, `UpdateEvtSpikeEnabled`, `InstallCertificate`, and — until Story 5 — `UpdateNotifications`, `UpdateSessionThreshold`, `UpdateGracePeriod`, `UpdatePerformanceConfig`). The named mutex MUST bracket the full Load-then-Save cycle.
- **FR-008**: The named-pipe server MUST handle request messages larger than 4 KB using a shared reader that loops on `ERROR_MORE_DATA` up to a capped maximum (1 MiB). The same reader MUST be used on both request and response paths.

**Phase 3 — Medium hardening**

- **FR-009**: ~~Webhook and ntfy cloud-metadata IP rejection~~. **Withdrawn 2026-04-24**: literal-string check was bypassable via IPv6-mapped, decimal/hex IPv4, trailing-dot, and 302 redirect. See `docs/reviews/codex-2026-04-24-009-branch-remediation.md` Step 1.
- **FR-010**: The pipe server MUST verify via `GetNamedPipeClientProcessId` + token SID check that callers of `register`, `remove-server`, and `baseline-reset` are `SYSTEM` or members of the local Administrators group. Denied calls MUST return `access denied` and MUST emit `slog.Warn("pipe=access_denied", "cmd", …, "sid", …)` plus the existing `EvtAccessDenied` audit event. Read-only verbs (`status`, `history`, `servers`) MUST remain accessible.
- **FR-011**: The email notification template MUST be rendered via `html/template` (not `text/template`). All dynamic fields (`Subject`, `Message`, `Host`, `ChangedBy`, `SpikeChannel`, and any others) MUST be contextually auto-escaped.
- **FR-012**: `RegistryChangeAttribution.Timestamp` MUST be populated from the event's `<TimeCreated SystemTime="…">` parsed via `time.Parse(time.RFC3339Nano, …)`. On parse error, fallback to `time.Now()` with a `slog.Warn` carrying the raw value.

**Phase 4 — Cleanup**

- **FR-013**: `UpdateNotifications`, `UpdateSessionThreshold`, `UpdateGracePeriod`, and `UpdatePerformanceConfig` and their dedicated tests MUST be deleted from `config.go` / `config_test.go`. The primary write path is `UpdateNotifySettings`.
- **FR-014**: ETW event-ID constants (`EvtDashboardAccess`, `EvtDashboardConfigChange`, `EvtServerRegistered`, `EvtServerRemoved`, `EvtAccessDenied`, `EvtGenericAudit`) MUST move from the root `drainctl` package to a new `internal/etwids/` package.
- **FR-015**: `DefaultAuditPath` MUST be renamed to `DefaultDBPath` returning `drainctl.db`; the old symbol MUST remain as a `// Deprecated:` alias for one release cycle. `HistoryOptions.DBPath` semantics MUST widen so `GetHistory` accepts either a file path or a directory path.
- **FR-016**: `internal/svc/handler.go` MUST split into topically-coherent, subsystem-named files (`service.go` Service struct + RunService + control-handler shim; `piperpc.go` pipe RPC dispatch; `dashsync.go` dashboard registration + config pull; `perfsupervisor.go` perf collector lifecycle; `spikesupervisor.go` evtspike reload loop) with no public signature changes. Subpackage promotion is explicitly out of scope for this feature — revisit on a later branch.
- **FR-017**: `frontend/src/App.svelte` duplicated status-transition and sparkline-append blocks MUST call shared helpers exported from `frontend/src/lib/state.svelte.js`.
- **FR-018**: Orphan exports `deriveP95` and `deriveP50` in `state.svelte.js` MUST be deleted.

### Key Entities

- **Dashboard TLS Fingerprint**: A SHA-256 hex string stored in `DashboardConfig.TLSFingerprint` representing the pinned dashboard certificate. After this feature, a stored non-empty value is immutable via automatic register; any change requires explicit operator action.
- **DPAPI Entropy**: A fixed process-constant 35-byte byte slice (`LISSTech.DrainCtl/v1/notify-secret`). Not a secret; its purpose is to prevent cross-service `CryptUnprotectData` by other processes running as SYSTEM or SERVICE on the same host.
- **Notify Target**: Webhook / ntfy / SMTP / email entry persisted in `Config.Notifications`. For SMTP+auth, gains a transport requirement (STARTTLS or `smtps://`).
- **Pipe Verb Privilege Class**: Read-only (`status`, `history`, `servers`) vs. privileged (`register`, `remove-server`, `baseline-reset`). The latter gains caller-SID verification.
- **Registry Change Attribution**: Existing struct in `internal/watcher`. Its `Timestamp` field changes provenance from wall-clock to event-embedded `SystemTime`.

## Constitution Alignment *(mandatory)*

### Operator Surface Impact

- **Affected surfaces**: root package (exports renamed/deleted), CLI (`register` refusal path), service (pipe caller-SID check; config RMW; evtspike loss wiring), installer (`restrictConfigACL` tightened), internal packages (new `internal/etwids/`; `internal/svc/handler.go` split; `internal/pipe` reader shared).
- **Public behavior changes**:
  - `drainctl register` against a dashboard with a changed cert now errors instead of silently re-pinning.
  - SMTP notifications with a `Secret` set now require STARTTLS or `smtps://`; plain-text-capable relays that previously worked will fail.
  - Privileged pipe verbs from non-admin callers now return `access denied`.
  - `DefaultAuditPath` remains callable but is deprecated; `DefaultDBPath` is the new canonical name.
  - Four `Update*` helpers removed from the public root-package surface.
- **Compatibility / migration**:
  - Dashboard cert rotation now requires manually clearing `Dashboard.TLSFingerprint` in `config.json` before re-registering. Document in release notes.
  - Operators running SMTP auth over plaintext relays must switch to STARTTLS or smtps://. Document in release notes.
  - Greenfield DPAPI entropy change has no migration path; must ship before any external release.
  - `DefaultAuditPath` callers continue to work but should move to `DefaultDBPath`; hard removal one release cycle later (separate feature branch).

### Quality and Observability Impact

- **Required tests**:
  - Unit: fingerprint mismatch refusal (CLI + service), STARTTLS refusal, ACL absence of SERVICE ACE (manual, documented), DPAPI entropy round-trip + wrong-entropy failure, evtspike loss callback, concurrent config RMW (20-goroutine stress), pipe `ERROR_MORE_DATA` loop via `scriptedReader` (not `net.Pipe`), pipe caller-SID verdict table, HTML template escaping, event `SystemTime` parse + fallback.
  - Integration: pipe admin/non-admin integration test, register → rotate cert → register mismatch manual walkthrough.
  - Regression: `TestWaitAttribution_IgnoresOlderEvents`, `go build ./... && go test ./...` after the handler split and rename.
- **Operational signals**:
  - New `slog.Warn`: `"smtp: no STARTTLS available, sending without encryption"`.
  - New `slog.Error`: `"dashboard=fingerprint-mismatch" saved=… offered=…`.
  - New `slog.Warn`: `"pipe=access_denied" cmd=… sid=…` (also surfaced as `EvtAccessDenied` ETW audit event, already defined).
  - New `slog.Warn`: `"evtspike: unparseable SystemTime" raw=…`.
  - Evtspike supervisor re-subscription events already exist; this feature ensures they actually fire.
- **Configuration / data impact**:
  - No new `Config` fields added in the pruned plan.
  - `Config.AuditPath` field retained for now; renamed internally to `DBPath` in a future release.
  - DPAPI ciphertext format changes (entropy arg); greenfield so no on-disk migration.
  - `config.json` ACL changes on install; documented.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Zero agents in the fleet silently accept a changed dashboard TLS fingerprint on re-registration; 100% of such attempts fail loudly with both fingerprints logged.
- **SC-002**: Zero SMTP AUTH credentials transmitted over a cleartext session in production. Verified by code path: every authenticated send path is gated by a successful STARTTLS or native TLS handshake.
- **SC-003**: Zero non-`SYSTEM`/non-Administrators groups have `Modify` access to `config.json` on any fresh install; verified by `icacls` output on a post-install fresh VM.
- **SC-004**: Zero cross-process DPAPI decrypts of drainctl secrets succeed without the documented entropy constant; verified by negative unit test.
- **SC-005**: 100% of evtspike subscription terminal errors trigger supervisor re-subscription within 5 seconds (previously: indefinite hang).
- **SC-006**: Zero lost concurrent config updates under a 20-writer stress test; final file parses as valid JSON and contains a value written by exactly one of the updaters.
- **SC-007**: Pipe messages up to 1 MiB round-trip without truncation; messages above 1 MiB fail with a specific cap error.
- **SC-008**: Zero non-admin callers succeed at `register` / `remove-server` / `baseline-reset`; read-only verbs (`status`, `history`, `servers`) remain available to the same callers.
- **SC-009**: Registry-change attribution timestamp error (wall-clock vs event `SystemTime`) reduced to zero on delayed-delivery test payloads.
- **SC-010**: Root `drainctl` package public surface shrinks by at least four symbols (`UpdateNotifications`, `UpdateSessionThreshold`, `UpdateGracePeriod`, `UpdatePerformanceConfig`); ETW event-ID constants no longer exported from root.
- **SC-011**: ~~`internal/svc/handler.go` drops from 1138 lines to no single new file exceeding 400 lines~~. **Deferred** — first-pass split during US5 produced tangled duplicate declarations and was reverted; tracked as `FEATURES.md` F5 for a follow-up branch (`010-handler-split`). Current state: file remains at 1149 lines, behavior-correct.
- **SC-012**: Svelte duplicated blocks reduced from 4 copies to 2 call sites; `deriveP95`/`deriveP50` removed from `state.svelte.js` exports.

## Assumptions

- The product is greenfield: there are **no** external deployments holding secrets encrypted under the old zero-entropy DPAPI scheme. This assumption drives the decision to drop all migration-writeback complexity from the DPAPI change.
- TOFU (trust-on-first-use) for dashboard certificate pinning remains an acceptable product posture. This feature strengthens *after*-pin tamper detection; it does not harden first-contact.
- Drainctl runs on on-prem Windows Server hosts, not cloud VMs. This assumption drives the decision to drop the general RFC1918/link-local SSRF guard. An earlier 009 revision kept a single rejection of `169.254.169.254` as a cheap cloud-metadata hedge; that hedge was withdrawn during 009 codex post-review (bypass-prone, see Step 1 of the 009-branch remediation plan).
- LAN webhooks (internal Slack bridges, internal ntfy, internal Mattermost) are legitimate and common deployment patterns. No RFC1918 block.
- Operators who run authenticated SMTP relays on plain port 25 without STARTTLS are rare enough that refusing AUTH on such connections is the right default, with a clear error message as the migration aid.
- Named-pipe DACL on a SYSTEM-owned pipe is sufficiently restrictive by default; the caller-SID check is defense-in-depth against a future regression, not a current hole.
- The `Dashboard.AutoPin` config flag is retained for TOFU opt-in on first register; only the silent-change path gains a refusal.
- Dashboard cert rotation is a rare operator event; requiring a manual `config.json` edit to clear the stored fingerprint before re-register is acceptable friction.
- The codex review in `docs/reviews/codex-2026-04-24-fullcodebase-remediation-plan.md` at commit `69309ff` is the authoritative scope for this feature. Any issue not in that plan is out of scope for 009 and belongs on a follow-up branch.
- Step sequencing within 009: Phase 1 ships first (four small PRs or one bundled PR, all S-effort). Phase 2 Step 6 (atomic RMW) must land before Phase 4 Step 12 (`Update*` deletion) — they touch the same helpers. Phases 2 and 3 are otherwise disjoint and may interleave. Phase 4 trails.
