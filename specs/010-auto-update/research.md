# Research: Agent Self-Poll Auto-Update (010)

This file captures the design decisions whose alternatives the spec FRs already foreclose. Each decision lists the option chosen, why, and what was rejected and why.

## Decision 1: Self-poll vs. dashboard-pulled

- **Decision**: Self-poll first. Dashboard-pulled rollout is a separate future feature.
- **Rationale**: Self-poll has zero new attack surface — we already trust GitHub and our code-signing cert; the agent just polls `api.github.com` over HTTPS and verifies the same Authenticode signature operators verify when they install manually. Dashboard-pulled introduces a new RCE channel (dashboard tells agents "run this MSI") that requires careful design around operator authentication on the dispatch command, version pinning, rollback, and blast-radius containment. Self-poll delivers most of the operational value (operators stop manually re-running install.ps1 on every host) at a fraction of the design cost, and it does not block the dashboard-pulled feature from landing later.
- **Alternatives considered**:
  - **Dashboard-pulled first**: rejected — bigger spec, real RCE channel, blocks operator-friction relief on the longer design cycle.
  - **Both at once**: rejected — combining them in one PR makes review harder and the rollback story muddier.
  - **Manual updates only (status quo)**: rejected — the leak/perf sweep in 26.116.* shipped 17 version bumps in one session. Manual install on every host on every fix is the operator-hours problem this feature solves.

## Decision 2: Poll cadence default of 24h ± 2h jitter

- **Decision**: Default `update.poll_interval = 24h`. Jitter window is `±interval/12`. First poll is `rand(5m, 15m)` after Start, fixed regardless of interval. Minimum permitted interval is 1h.
- **Rationale**: Most fleet operators care about getting fixes "by tomorrow", not "within the hour". A 24h cadence:
  - Stays well under GitHub's 60/h unauthenticated rate limit (a 100-agent fleet averages ~4 calls/h).
  - Spreads load evenly with 2h of jitter — a release at noon UTC reaches all agents within 26h.
  - Survives transient outages within a single retry cycle (the 5m → 15m → 45m backoff handles short blips without burning the daily budget).
- **Alternatives considered**:
  - **1h cadence**: rejected — 24× the GitHub API cost per host, no real benefit; a 1h-fresh fix is rarely the difference between an incident and not.
  - **On-startup only**: rejected — a host that runs uninterrupted for weeks would never update. Service restarts are not a reliable update trigger.
  - **Configurable per-host with no default**: rejected — operators want sensible defaults. The default is the configuration most fleets will use.

## Decision 3: WinVerifyTrust + Subject CN check (not PowerShell, not signtool)

- **Decision**: Call `WinVerifyTrust` from `wintrust.dll` directly via `windows.NewLazySystemDLL`. After the trust check, extract the cert via `CryptQueryObject` from `crypt32.dll` and assert `Subject CN == "LISS Consulting, Corp."` exactly.
- **Rationale**: This is the same Win32 API the OS uses internally for SmartScreen / "do you want to run this installer" prompts. It validates the certificate chain against the system trust store, checks revocation, and respects timestamping. Adding the Subject CN equality check on top closes the "valid Authenticode signature by anyone" gap — without it, any code-signing cert issued by any trusted CA could pass the trust check.
- **Alternatives considered**:
  - **Spawn `powershell.exe -Command Get-AuthenticodeSignature`**: rejected — adds a ~200-500ms subprocess to every install path, depends on PowerShell version semantics (`Microsoft.PowerShell.Security` is shipped, but the JSON output schema is not formally a contract), and complicates the verifier's unit-testability. The Win32 API is direct and stable.
  - **Pinning the public key (not the Subject CN)**: rejected for v1 — Subject CN is what operators see in the cert dialog and what shows up in `signtool verify` output. Public-key pinning is more correct cryptographically (immune to CA reissue with the same Subject) but ties us to a single signing key and breaks if we ever rotate. A future hardening can move to pubkey pinning if a renewal incident motivates it.
  - **Skip the Subject CN check** (rely on Authenticode trust alone): rejected — see rationale above. Any public CA could issue a cert with `CN=LISS Consulting, Corp.` only via a fraudulent EV-validation event; relying on chain validity alone trusts every CA in the system store.

## Decision 4: Detached msiexec spawn + self-triggered shutdown (not msiexec-driven service stop)

- **Decision**: Spawn `msiexec /i <msi> /quiet /norestart` with `CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS`. Call `cmd.Process.Release()` immediately (no Wait). Then trigger our own clean shutdown via the service's existing ctx cancel path. The MSI custom action restarts the service after replacing files.
- **Rationale**: The race we want to avoid is "msiexec asks SCM to stop the service while the service is mid-write." By initiating our own shutdown right after spawning msiexec, we deterministically reach a quiesced state before msiexec's `ControlService(STOP)` arrives. msiexec's stop is then a no-op (service already stopped) and file replacement proceeds without a sharing violation. Detached process + Release ensures msiexec survives our process exit.
- **Alternatives considered**:
  - **Let msiexec drive the stop, don't trigger our own**: rejected — the in-flight `Execute` goroutine is mid-loop when msiexec asks SCM to stop us. We've added cleanup hooks (defer ms.Close(), defer etwH.Close(), evtSpikeSub.Stop, telemetryWG drain) that need our own shutdown path to run; relying on SCM hard-stop bypasses them.
  - **Don't detach; Wait on msiexec**: rejected — Wait blocks until msiexec finishes, but msiexec wants to stop us as part of finishing. Deadlock.
  - **Re-exec ourselves to a temp binary that runs the install**: rejected — clever, but adds a binary-on-disk shuffle and a second process to harden. The detached msiexec pattern is well-trodden on Windows.

## Decision 5: No persisted state across runs

- **Decision**: ETag, last-successful-poll timestamp, and consecutive-failure count are held in memory only. A service restart starts the updater fresh: 5–15 min initial delay, no ETag (so the first poll fetches the JSON in full ~few KB), zero failure count.
- **Rationale**: Persistence introduces failure modes (corrupt state file, stale state after a clock change, file-permissions issues if the data dir is wrong). The initial-delay + 24h cadence makes a fresh-start poll cheap. The savings from persisting an ETag are roughly "a few KB of HTTP body per service restart" — not worth a single bug class.
- **Alternatives considered**:
  - **Persist ETag in `config.json`**: rejected — pollutes the operator-edited config with machine-managed state.
  - **Persist in a sidecar file (`update_state.json`)**: rejected — net new on-disk artifact for marginal benefit.
  - **Persist in the telemetry SQLite DB**: rejected — ties the updater's lifecycle to the telemetry DB's. The updater should run independently of telemetry.

## Decision 6: Updater is the first new LCI subsystem; evtspike is the first migration

- **Decision**: This PR introduces `internal/lifecycle/lifecycle.go` with the `Subsystem` interface from [docs/architecture/lifecycle.md](../../docs/architecture/lifecycle.md) and adds compile-time conformance assertions for both the new `updater.Subsystem` and the existing `evtspike.Subsystem`. Per [docs/architecture/strangler-plan.md](../../docs/architecture/strangler-plan.md) §"The ratchet rule", every new-subsystem PR ships one existing-subsystem migration. evtspike is the natural first migration because it already has the right shape; the assertion is a one-line change.
- **Rationale**: Establishes the LCI as a thing the codebase actually depends on (compile-time, not just doc-time) starting from this PR. Makes the strangler ratchet enforceable in review (reviewers can grep for `var _ lifecycle.Subsystem` and see who's migrated).
- **Alternatives considered**:
  - **Land the LCI in a separate doc-only PR first**: rejected — a contract that nothing depends on isn't a contract. The PR that introduces a new contract MUST have at least two implementations on day one (otherwise we don't actually know whether the contract fits).
  - **Migrate every existing subsystem in this PR**: rejected — that's the big-bang refactor STP exists to avoid. evtspike + the new updater is the minimum to validate the contract; the rest land per the ratchet rule on subsequent PRs.

## Decision 7: HTTP client uses Go stdlib defaults; no custom retry logic at the http layer

- **Decision**: One `http.Client` per updater Subsystem with a 30s `Timeout`. No retry, no circuit breaker, no rate limiter at the HTTP level. The poll-loop's backoff state machine (FR-012) is the only retry layer.
- **Rationale**: One layer of retry, in one place, with one set of state. Pushing retries down into the HTTP client (e.g., via a custom `RoundTripper`) creates two retry mechanisms whose interaction is hard to reason about during incidents. The 30s timeout is generous (GitHub's `latest` endpoint typically responds in <500ms) but bounded.
- **Alternatives considered**:
  - **Use `cleanhttp` or `retryablehttp`**: rejected — net new dependency for ~30 LoC of retry logic that's already simpler in the poll loop.
  - **Per-request timeout via `context.WithTimeout`**: chosen as a complement, not a replacement. Each poll's `http.NewRequestWithContext` uses a timeout-bounded ctx so the client's `Timeout` is the upper bound, not the load-bearing one.

## Decision 8: Opt-in default (Enabled=false)

- **Decision**: `update.enabled` defaults to `false`. Operators must flip it to `true` deliberately. A fresh install with the 010 release contacts GitHub zero times until the operator edits `config.json`.
- **Rationale**: Auto-installing software on a host the operator administers — without their explicit consent on this specific host — violates the principle of least surprise even when the artifact is signed. Enterprises in particular often have change-control regimes (CAB approval, maintenance windows, tested-on-staging-first) that prohibit unsupervised software installation. Opt-in respects those regimes by default; opt-in-elsewhere ("you opted in by upgrading to 010") doesn't, because the operator who upgraded to 010 isn't necessarily the same person responsible for change control on every host they manage. The cost of opt-in is one extra editorial step per fleet (a config-mgmt template can flip the flag for hosts that should be auto-managed); the cost of opt-out-default is an angry CAB email.
- **Alternatives considered**:
  - **Opt-out default (`enabled=true`)**: rejected — see rationale. Also documented in spec.md Assumption 1.
  - **First-run prompt** (e.g., installer dialog asking the operator): rejected — drainctld installs silently in most fleet workflows; the prompt would be invisible.
  - **Different defaults per install path** (silent install opts-in, interactive install prompts): rejected — adds an MSI-property mechanism for a tiny gain; the config flip is simpler.

## Decision 9: Channel string field with `"stable"` and `"prerelease"`; resolves the prerelease-only release situation

- **Decision**: Add `update.channel string` defaulting to `"stable"`. Recognized values are `"stable"` (polls `/releases/latest`, GitHub filters out prereleases) and `"prerelease"` (polls `/releases?per_page=1`, accepts the most recent regardless of prerelease flag). Empty string or absent field is treated as `"stable"`. Unknown values are clamped to `"stable"` in `LoadConfig` with a `slog.Warn`. A 404 from `/releases/latest` is logged as `update=no_stable_release` and treated as a steady-state idle, not a poll failure (no backoff escalation). Two exported constants (`ChannelStable`, `ChannelPrerelease`) keep callers off string literals.
- **Rationale**: Through release 010, every drainctl release ships with `--prerelease` per the `just publish` recipe (the project owner's deliberate choice — "CalVer means there is no 1.0 to graduate to; stable is owner-declared rather than a version threshold", per the publish recipe comment). With `enabled=true, channel="stable"`, the updater would 404 forever and never install anything. Three resolutions are possible: (a) drop `--prerelease` from publish; (b) hard-code the updater to ignore the prerelease flag; (c) make the channel choice an operator decision. (c) is correct because it lets the project owner keep the prerelease signal as a product communication while letting operators opt into the early channel knowingly. A string field (over a bool) gives forward-extensibility — adding a third channel later (e.g., `"nightly"` if a daily-build pipeline is set up, or `"lts"` if support windows formalize) is one parser case, not a config-format migration. The cost of the string is one validation step in `LoadConfig`; the bool would have saved nothing meaningful (the operator action to opt in is identical either way).
- **Alternatives considered**:
  - **`pre_release bool` field**: rejected — locks us into two channels with a name that doesn't generalize. If we ever add a third channel, the bool has to be migrated to a string anyway, and pre-010 deployments (none today, many tomorrow) would carry the legacy field forward in their `config.json`.
  - **Hard-code `channel="prerelease"` always**: rejected — silently makes the prerelease flag useless for the project. If we ever ship a stable release alongside a prerelease, every agent jumps to the prerelease.
  - **Drop `--prerelease` from `just publish`**: rejected — that's a project-meta decision, not the updater's call. The owner has explicitly framed every release as beta until they say otherwise.
  - **Enum-typed Go constants exported from the package** (`Channel = ChannelStable | ChannelPrerelease`): rejected — Config is JSON-marshaled; a string is the simplest representation operators read in their config files. The exported constants `ChannelStable`/`ChannelPrerelease` give Go callers the same compile-time checking without forcing JSON to be anything other than a string.

## Decision 10: Audit log records version transitions; no separate "update event log"

- **Decision**: Append one row to `telemetry.AuditStore` per successful version transition: `event=auto_update_install old=V0 new=Vlatest`. No new table, no new event-stream, no new dashboard view in this feature.
- **Rationale**: Operators already use audit log to investigate "what changed on this host." Auto-update transitions are exactly that. Reusing the existing audit infrastructure means no schema migration and no new view to maintain. The dashboard-pulled feature can layer richer telemetry on top.
- **Alternatives considered**:
  - **New `update_history` table in the telemetry DB**: rejected — premature normalization for one row per ~weeks-to-months interval.
  - **Just slog.Info, no audit row**: rejected — slog rotates daily, kept 7 days. A version transition that happened 8 days ago is invisible. Audit is the durable record.
