# Feature Specification: Agent Self-Poll Auto-Update (010)

**Feature Branch**: `010-auto-update`
**Created**: 2026-04-26
**Status**: Draft
**Architectural dependencies**: [docs/architecture/lifecycle.md](../../docs/architecture/lifecycle.md), [docs/architecture/strangler-plan.md](../../docs/architecture/strangler-plan.md). This is the first new feature built against the LCI; per STP §"The ratchet rule" it ships in the same PR as the demonstration migration of `internal/evtspike` to formally implement `lifecycle.Subsystem`.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Hands-off update on standalone agents (Priority: P1)

A fleet operator manages a few dozen Windows hosts running drainctld. Today, every fix we ship — pipe goroutine leak, LSASS leak, telemetry pool bound — requires the operator to RDP to each host (or push via a config-mgmt tool they may not have set up) and manually re-run `irm install.ps1 | iex`. With auto-update, the agent itself periodically checks the project's GitHub releases page, downloads the signed MSI when there's a newer version, verifies the Authenticode signature, and runs `msiexec` silently. The next morning the host is up to date.

**Why this priority**: The core value proposition. We just shipped 17 version bumps in one session for the leak/perf sweep; the friction of getting any of those onto production hosts dominated the operator effort. Until this lands, every release is one more chore for every operator on every host.

**Independent Test**: Stand up a fresh host on a release (call it V0), wait one poll interval, observe that the agent's daily file log records the poll, the version comparison ("V0 vs Vlatest"), the download, the signature verification, the msiexec spawn, and that after the dust settles `drainctl --version` returns Vlatest.

**Acceptance Scenarios**:

1. **Given** an agent running version V0 with `update.enabled=true`, **When** GitHub publishes a release Vlatest > V0, **Then** the next poll downloads `LISSTech.DrainCtl.msi`, verifies the Authenticode signature against `CN=LISS Consulting, Corp.`, spawns msiexec detached, and the service ends up at Vlatest with an audit row for the version transition.
2. **Given** an agent running version V0 with `update.enabled=true`, **When** the GitHub `latest` release is V0 (already current), **Then** no download or install occurs; the file log records `update=up_to_date current=V0`.
3. **Given** an agent running version V0, **When** `update.enabled=false` in `config.json` (the default), **Then** the updater subsystem does not start its poll loop and no GitHub call is made.
4. **Given** an agent with `update.enabled=true, channel="stable"`, **When** the only releases on GitHub are prereleases, **Then** `/releases/latest` returns 404 and the updater logs `update=no_stable_release` and reschedules at the configured interval (no error spam, no backoff escalation — this is a steady state, not a failure).
5. **Given** an agent with `update.enabled=true, channel="prerelease"`, **When** GitHub publishes a new prerelease Vlatest > V0, **Then** the next poll downloads, verifies, and installs Vlatest exactly as in scenario 1.
6. **Given** an agent with `update.enabled=true, channel="banana"` (invalid), **When** the service loads config, **Then** the channel is clamped to `"stable"` with a `slog.Warn` and the updater proceeds as if `channel="stable"`.

---

### User Story 2 - Refuse to install anything we can't verify (Priority: P1)

A naïve auto-updater that pulls from a public URL and pipes it into `msiexec` is a remote-code-execution vector waiting to happen. This story makes the verification gate load-bearing: a downloaded artifact whose signature is missing, expired, or signed by anyone other than the project's code-signing certificate is rejected and the temp file is deleted. The service does NOT install, does NOT log a stack trace that could mask the refusal, and continues running the current version.

**Why this priority**: Without this, the feature in Story 1 is worse than not shipping. Any attacker who controls the agent's path to api.github.com (DNS hijack, transparent MITM on a non-HTTPS-pinned link, or compromise of a project distribution channel) gets every host in the fleet to run their MSI. The verification gate is what keeps the channel honest.

**Independent Test**: Construct (or grab from CI) an MSI that is unsigned, an MSI signed by a self-signed test cert, and an MSI signed by the real LISS cert. Feed each into the verifier in unit tests. Only the third passes; the other two return distinct errors and the temp file is removed.

**Acceptance Scenarios**:

1. **Given** a downloaded MSI with no Authenticode signature, **When** the verifier runs, **Then** it returns `errSignatureMissing`, the temp file is deleted, and a `slog.Warn` records `update=refused reason=signature_missing`.
2. **Given** a downloaded MSI signed by a certificate whose Subject CN is not `LISS Consulting, Corp.`, **When** the verifier runs, **Then** it returns `errSignatureSubjectMismatch`, the temp file is deleted, and a `slog.Warn` records `update=refused reason=subject_mismatch subject=…`.
3. **Given** a downloaded MSI signed by a revoked or expired certificate, **When** the verifier runs, **Then** `WinVerifyTrust` returns a non-success code, install is refused, and the temp file is deleted.

---

### User Story 3 - Don't hammer GitHub or wedge the service on outages (Priority: P2)

The poll has to be a good network citizen and a good neighbor on the host. This story sets the cadence (24h ± 2h jitter, default), the on-startup behavior (a delayed first poll 5–15 min after Start, jittered), and the failure-mode (exponential backoff on consecutive failures, capped at 24h, never a tighter retry than the configured interval). It also wires HTTP `If-None-Match` so a steady-state 304 response from GitHub is the dominant case.

**Why this priority**: A self-poll feature that hits GitHub every minute on every agent in a fleet of N hosts is a self-inflicted DoS against the public API. A self-poll feature that crash-loops the service on a transient DNS failure is operationally worse than no auto-update.

**Independent Test**: Inject a `httptest.Server` that returns 503 for the first three calls and 200 with a release JSON afterwards; observe that the updater backs off (5m → 15m → 45m, capped) and does not crash. Inject one that returns 304 with the same ETag; observe the updater doesn't re-download.

**Acceptance Scenarios**:

1. **Given** an agent that has never polled, **When** the service starts with `update.enabled=true`, **Then** the first poll fires between 5 and 15 minutes after Start (jittered).
2. **Given** an agent on the steady-state cadence, **When** the GitHub API returns 304 Not Modified, **Then** no download is attempted and the file log records `update=not_modified`.
3. **Given** an agent that has just had three consecutive poll failures, **When** the next poll is scheduled, **Then** the delay is at least the backoff value, never less than the configured `poll_interval`, never more than 24 h.

---

### Edge Cases

- **Same-version race after download.** Between the version-check decision and the install, another path (manual `msiexec`, dashboard-driven future feature) might already have installed the new version. msiexec running against an already-installed-and-current MSI is a no-op; we accept that.
- **Disk full on download.** Returns an error from the streamed-write step; the partial temp file is deleted; no install attempt.
- **Service stopping mid-poll.** The poll's `http.Client` uses a context derived from the subsystem ctx; a stop cancels in-flight requests within the HTTP client's response timeout. The updater's wg.Wait sees the goroutine exit cleanly.
- **Clock skew vs. release timestamp.** We compare versions, not timestamps; clock skew on the host is irrelevant.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The service MUST run a self-poll updater subsystem when `update.enabled=true` and skip it entirely when `update.enabled=false`. **Default is `false` (opt-in).** Operators turn auto-update on by editing `config.json`. The subsystem implements `lifecycle.Subsystem`.
- **FR-001a**: The service MUST honor `update.channel string`. Recognized values are `"stable"` and `"prerelease"`. Default is `"stable"`. Empty string (or absent field) is treated as `"stable"`. Any unrecognized value is clamped to `"stable"` with a `slog.Warn` naming the configured value and the clamp. When `"stable"`, the updater polls `/releases/latest` and ignores any release flagged as prerelease. When `"prerelease"`, the updater polls `/releases?per_page=1` and accepts the most recent release regardless of the prerelease flag. **Operational note**: every drainctl release through 010 ships with `--prerelease` (per `just publish`); on hosts where the operator wants automatic updates today, BOTH `enabled=true` AND `channel="prerelease"` are required. Once the project graduates to non-prerelease releases, `channel="stable"` becomes the operationally useful default.
- **FR-002**: The poll MUST query GitHub over HTTPS using Go's default system root store, with `User-Agent: drainctld/<version>` and `Accept: application/vnd.github+json`. Endpoint is selected by `channel` per FR-001a.
- **FR-003**: The poll cadence is configurable via `update.poll_interval` (Go duration string, default `24h`). Each poll's actual delay is `interval ± rand([0, jitter])` where `jitter = interval/12` (so 24h ± 2h). Minimum permitted interval is `1h`; values below are clamped with a warning.
- **FR-004**: First poll fires `rand(5m, 15m)` after Start, not immediately. This delay is fixed regardless of `poll_interval`.
- **FR-005**: The version comparison MUST parse `vYY.DOY.N` numerically, component by component. A remote tag that does not match this pattern is treated as "older than current" (skip).
- **FR-006**: The downloader MUST stream the MSI to a temp path (`%TEMP%\drainctl-update-<random>.msi`), not buffer in memory. On any error, the temp file MUST be deleted.
- **FR-007**: Before install, the verifier MUST call `WinVerifyTrust` on the temp MSI with `WINTRUST_ACTION_GENERIC_VERIFY_V2`. A non-zero return rejects the install.
- **FR-008**: After `WinVerifyTrust` succeeds, the verifier MUST extract the signing cert via `CryptQueryObject` and assert the Subject CN equals exactly `LISS Consulting, Corp.`. A mismatch rejects the install.
- **FR-009**: Install spawns `msiexec /i <path> /quiet /norestart` with `CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS` so msiexec survives the service's exit. The Go process MUST call `cmd.Process.Release()` (no Wait).
- **FR-010**: Immediately after the detached spawn, the updater triggers the service's own clean shutdown by cancelling the service-level ctx via the existing pipe-based stop mechanism (or a direct `s.shutdown()` call). The MSI is responsible for restarting the service after replacing files.
- **FR-011**: Every poll attempt MUST emit a `slog.Debug` line with fields: `update`, `current`, `remote`, `decision` (`install|up_to_date|not_modified|skipped|error`). Each successful version transition MUST emit a `slog.Info` and append an audit row (`telemetry.AuditStore`) with `event=auto_update_install old=V0 new=Vlatest`.
- **FR-012**: Consecutive poll errors MUST drive an exponential backoff: 5m, 15m, 45m, then capped at the larger of 24h or `poll_interval`. A successful poll resets the backoff. The backoff is in addition to (not instead of) the configured interval.
- **FR-013**: The updater MUST honor HTTP `If-None-Match` using the `ETag` returned by GitHub. The ETag is held in memory only — a service restart starts fresh.
- **FR-014**: If the configured `update.enabled=true` but a network probe (any DNS or TCP error reaching api.github.com) fails, the updater treats this the same as a poll failure and applies backoff. It MUST NOT crash, panic, or wedge the service.
- **FR-015**: The updater MUST persist no state across runs. (No "last successful poll" timestamp, no cached release JSON, no learned good ETag in the config or telemetry DB.) State persistence is explicitly out of scope; the simplest implementation has no failure mode tied to corrupt local state.

### Out of scope (explicitly deferred)

- **Pinning to a specific version.** Operators who want a specific version disable auto-update and install manually, or wait for the dashboard-pulled rollout feature.
- **Manual update trigger via CLI or dashboard.** No `drainctl update check` command in this version. Operators restart the service to trigger an early first-poll.
- **Maintenance windows / business-hours skip.** The 24h ± 2h jitter is the only scheduling primitive.
- **Pre-flight reachability/health check on the new version.** If the new MSI installs but the new service fails to start, SCM Recovery handles the crash-loop; the operator gets the existing alert. No automatic rollback in v1.
- **Channel separation (stable / beta / nightly).** Only `latest` GitHub releases are polled.
- **Delta updates.** Full MSI replacement only.

## Key Entities

- **`UpdateConfig`** — new struct in `config.go`: `Enabled bool` (default `false`), `Channel string` (default `"stable"`), `PollInterval Duration` (Go duration string in JSON form, default `24h`). Default-constructed on first `LoadConfig` post-upgrade. See [data-model.md §1](./data-model.md).
- **`updater.Subsystem`** — new package `internal/updater/`. Owns the poll-tick goroutine, the http.Client, the in-memory ETag, and the backoff state. Implements `lifecycle.Subsystem`. See [plan.md §"Package layout"](./plan.md).
- **GitHub releases-API contract** — the subset of `/repos/.../releases/latest` JSON we depend on. See [contracts/github-releases-api.md](./contracts/github-releases-api.md).

## Assumptions

1. **Opt-in by default.** Operators upgrading from a pre-010 version get `update.enabled=false` by default. They opt in deliberately by editing `config.json`. No release auto-deploys to existing fleets without operator action; the value of this feature is operator convenience, not silent rollout.
2. **Public GitHub releases stay public.** This feature targets repos whose releases are downloadable without authentication. Private-repo support is a future consideration.
3. **Authenticode trust chain is intact.** WinVerifyTrust depends on the host's trust store containing the LISS Consulting code-signing CA's chain; this has been true since the cert was issued. If the chain breaks, every host's auto-update fails closed (correct behavior).
4. **MSI custom action restarts the service.** The existing WiX MSI already stops/starts the service on install; we don't need to restart it ourselves after spawning msiexec.
5. **Today, prereleases are the only releases.** Until the project ships a non-prerelease release, `channel="prerelease"` is required for any auto-update activity. The release notes for 010 call this out; the default `"stable"` for `channel` reflects what we expect operators to want once stable releases exist, not what works today.

## Compatibility / migration

- **Config**: `update` is a new top-level object. Missing in old `config.json` → defaults to `{enabled: true, poll_interval: "24h"}`. No migration step needed; the JSON unmarshal handles missing fields via Go's zero-value behavior plus an explicit default in `LoadConfig`.
- **Service surface**: No new CLI verbs, no new pipe verbs, no new dashboard routes in this feature.
- **Telemetry**: One new audit event type (`auto_update_install`); no schema change (the audit table already takes free-form event strings).

## Required tests

- Unit: version-comparison parser; signature verifier (3 cases — unsigned, wrong-subject, valid); HTTP poll loop with httptest backend exercising 200/304/503 paths; backoff state machine.
- Integration: full updater subsystem against a fake GitHub server, asserting the LCI contract (Start returns nil, Stop drains within bounded time, ctx cancel halts in-flight HTTP).
- LCI conformance: a compile-time `var _ lifecycle.Subsystem = (*updater.Subsystem)(nil)` assertion in `internal/updater/updater_windows.go`.
- Strangler ratchet: same PR adds the same compile-time assertion to `internal/evtspike` and confirms `go build ./...` is clean — see plan.md §"Sequencing".
