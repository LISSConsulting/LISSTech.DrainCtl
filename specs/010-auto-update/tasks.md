# Tasks: Agent Self-Poll Auto-Update (010)

Single PR. Internal commit order designed so each commit builds clean and is independently reviewable. Effort: **S** ≤ 30 min, **M** ≤ 2 h, **L** > 2 h.

## Commit 1 — `internal/lifecycle` package + Subsystem interface (S)

- New file `internal/lifecycle/lifecycle.go` with the interface from [docs/architecture/lifecycle.md](../../docs/architecture/lifecycle.md). Plus optional `Statusable`. ~30 LoC.
- New file `internal/lifecycle/lifecycle_test.go` with one no-op test type that satisfies the interface, just to confirm the package compiles. ~15 LoC.
- `//go:build windows` not strictly required (the package has no syscalls), but applied for consistency with the rest of `internal/`.
- Verify: `go build ./internal/lifecycle/...`. `just lint`.

## Commit 2 — Migrate `internal/evtspike` to LCI (S)

- Add one line to `internal/evtspike/subsystem.go` near the `Subsystem` type definition:
  ```go
  var _ lifecycle.Subsystem = (*Subsystem)(nil)
  ```
- Add the import.
- Zero behavior change.
- Verify: `go build ./...` clean. `go test ./internal/evtspike/...` unchanged.

## Commit 3 — Add `UpdateConfig` to root `Config` (S)

- Add the struct + field to `config.go` per [data-model.md §1](./data-model.md). Three fields: `Enabled bool`, `Channel string`, `PollInterval Duration`. Plus two exported constants: `ChannelStable = "stable"`, `ChannelPrerelease = "prerelease"`.
- `LoadConfig` defaults: empty `Channel` → `"stable"`; unrecognized `Channel` → `"stable"` with `slog.Warn` naming both the configured and clamped values; `PollInterval == 0` → `24h`; `PollInterval < 1h` → `1h` with `slog.Warn`. Bool zero-value (`false`) matches the desired default for `Enabled`, no special handling.
- Update `config_test.go`: round-trip a `Config` with and without an `update` object; assert all three default values; assert that `{"update": {"enabled": true}}` (without `channel`) loads with `Channel="stable"`; assert that `{"update": {"channel": "banana"}}` loads with `Channel="stable"` and emits a warn (capture via slog test handler); assert that `{"update": {"channel": "prerelease"}}` loads cleanly. Confirms the per-field opt-in + channel-clamp semantics.
- Verify: `go test ./... -run TestConfig`. `just lint`.

## Commit 4 — Version parser + comparator (S)

- New `internal/updater/version_windows.go` with:
  ```go
  type version struct{ year, doy, n int }
  func parseVersion(s string) (version, error)
  func (a version) less(b version) bool
  ```
- Strip leading `v` if present. Reject anything that isn't `\d+\.\d+\.\d+`. Numeric component compare.
- Test with table-driven cases including the FR-005 fallback (non-matching tag → "older than current").
- Verify: `go test ./internal/updater/...`.

## Commit 5 — Backoff state machine (S)

- New `internal/updater/backoff_windows.go`:
  ```go
  type backoff struct{ failures int }
  func (b *backoff) recordFailure() time.Duration  // returns delay to apply
  func (b *backoff) recordSuccess()
  ```
- Schedule: `5m, 15m, 45m, ..., capped at 24h`. (Doubling with a cap.)
- Pure-Go, no syscalls; `//go:build windows` applied for consistency.
- Test: confirm sequence; confirm cap; confirm reset on success.

## Commit 6 — GitHub releases client (M)

- New `internal/updater/github_windows.go`:
  ```go
  type release struct {
      tag, assetURL, etag string
      notModified         bool   // true on 304
      noReleases          bool   // true on 404 from a release-listing endpoint
  }
  func fetchLatestRelease(ctx context.Context, client *http.Client, channel, ifNoneMatch string) (release, error)
  ```
- Implements the contract in [contracts/github-releases-api.md](./contracts/github-releases-api.md). The `channel` parameter selects between `/releases/latest` (when `channel == dc.ChannelStable`) and `/releases?per_page=1` (when `channel == dc.ChannelPrerelease`). Caller (the Subsystem) is responsible for passing a recognized value; this function does NOT re-clamp — `LoadConfig` already did. When the prerelease channel returns a single-element array, `[0]` is the candidate.
- 304 → returns `release{notModified: true}` without parsing a body. 200 → parses into `tag` + `assetURL`. 404 from either release endpoint → returns `release{noReleases: true}` (NOT an error — the caller treats this as `update=no_stable_release` per FR + research Decision 9). 403 / 5xx / malformed-JSON / missing-asset → returns an error.
- `httptest`-based unit tests cover both channels × {200, 304, 403, 404, 5xx, missing-asset, malformed-JSON}, plus a test asserting `channel="prerelease"` actually hits `/releases?per_page=1` (verify via the request URL recorded by the test server).

## Commit 7 — Authenticode verifier (M)

- New `internal/updater/verify_windows.go`:
  ```go
  func verifyAuthenticode(msiPath string) error
  ```
- Calls `WinVerifyTrust` with `WINTRUST_ACTION_GENERIC_VERIFY_V2`. On success, calls `CryptQueryObject` to extract the signing cert, asserts Subject CN equals exactly `LISS Consulting, Corp.`. Returns sentinel errors (`errSignatureMissing`, `errSignatureSubjectMismatch`, `errSignatureUntrusted`) so callers and tests can branch precisely.
- Unit-test with three fixture MSIs in `internal/updater/testdata/`: `unsigned.msi`, `wrong-subject.msi`, `valid-liss.msi`. The valid one is whatever signed MSI is on disk from the most recent `just release` (committed once via `git add -f`, refreshed when the cert changes). The wrong-subject one is generated via signtool with a self-signed test cert; checked-in with the test fixture.
- Verify: `go test ./internal/updater/ -run TestVerify`. Unit test runs without network.

## Commit 8 — msiexec spawn (S)

- New `internal/updater/install_windows.go`:
  ```go
  func spawnInstall(msiPath string) error
  func triggerSelfShutdown(svcCtxCancel context.CancelFunc)
  ```
- `spawnInstall` uses `exec.Command("msiexec", "/i", msiPath, "/quiet", "/norestart")` with `SysProcAttr.CreationFlags = windows.CREATE_NEW_PROCESS_GROUP | 0x00000008 /* DETACHED_PROCESS */`. Calls `cmd.Start()`, then `cmd.Process.Release()`. Returns Start's error.
- `triggerSelfShutdown` invokes the cancel func passed in; documented contract is "the service-level ctx cancel; the running Execute loop responds by stopping all subsystems."
- Unit test for `spawnInstall` is light — assert the exec.Cmd is constructed with the expected flags via a test seam (`var execCommand = exec.Command`).

## Commit 9 — Updater Subsystem (M)

- New `internal/updater/updater_windows.go`:
  ```go
  type Subsystem struct {
      cfg             dc.UpdateConfig
      client          *http.Client
      shutdownService context.CancelFunc  // service-level ctx cancel
      etag            string
      backoff         backoff
      wg              sync.WaitGroup
      ctx             context.Context     // set in Start
      cancel          context.CancelFunc  // set in Start; Stop calls it then wg.Wait
  }
  func New(cfg dc.UpdateConfig, shutdownService context.CancelFunc) *Subsystem
  func (s *Subsystem) Start(ctx context.Context) error
  func (s *Subsystem) Stop()
  ```
- `Start` returns nil immediately if `cfg.Enabled == false` (no-op subsystem). Otherwise launches the poll loop in a goroutine tied to a derived ctx. The first tick fires after `rand(5m, 15m)`; subsequent ticks at `cfg.PollInterval ± jitter` plus any backoff.
- The poll loop on each tick:
  1. Re-read both `cfg.Enabled` and `cfg.Channel` (operator may have flipped either during the run); if `Enabled=false`, just reschedule.
  2. `fetchLatestRelease(ctx, client, cfg.Channel, etag)` → on 304: log not_modified, reset backoff, reschedule. On `noReleases=true`: log `update=no_stable_release`, reset backoff (this is steady-state idle, not a failure), reschedule.
  3. On 200: parse tag → version. Compare with `dc.Version`. If `<= current`: log up_to_date, reset backoff, reschedule.
  4. Newer: download to temp file. On any error: cleanup, record failure, backoff, reschedule.
  5. `verifyAuthenticode`. On error: cleanup, log refused, this is NOT a network failure so DON'T backoff (could be deliberate poisoning). Reschedule on the configured interval.
  6. Emit `slog.Info` with `update=installing old=<dc.Version> new=<tag>` — this is the v1 record of the version transition (durable audit-row deferred per FR-011 / research Decision 10).
  7. `spawnInstall(tempPath)`. On error: cleanup, log error, reschedule.
  8. `triggerSelfShutdown` — service drains, msiexec proceeds.
- LCI conformance: `var _ lifecycle.Subsystem = (*Subsystem)(nil)` at the top of the file.
- **No `AuditAppender` interface and no audit-store dependency** — durable persistence of version transitions is deferred to a follow-up spec; v1's `slog.Info` line is sufficient for operator visibility (7-day file-log rotation).

## Commit 10 — Integration test against fake GitHub (M)

- New `internal/updater/updater_integration_windows_test.go`.
- Spins up an `httptest.Server` mocking GitHub `/releases/latest`. Provides a tiny valid MSI byte stream as the asset (or a fixture MSI from testdata).
- Test scenarios:
  - **Steady state, stable channel**: `channel="stable"`, server returns 200 with current version → no install, no spawn. (Use a test seam to replace `spawnInstall` with a counting fake.)
  - **Newer available, stable channel**: `channel="stable"`, server returns 200 with bumped version → spawn fake fires once, slog.Info line with `update=installing` captured (via slog test handler), ctx cancel called. Asserts the request URL was `/releases/latest`.
  - **Newer available, prerelease channel**: `channel="prerelease"`, server returns 200 with bumped version → same outcome as above. Asserts the request URL was `/releases?per_page=1`.
  - **No stable release**: `channel="stable"`, server returns 404 → no spawn, no error log, `consecutiveFailures` stays at 0 (steady-state idle, not a failure). Repeats across multiple poll cycles without escalating.
  - **Disabled**: `enabled=false` from Start → Start returns nil, no goroutine, no HTTP call. Confirmed by recording requests in the test server.
  - **Mid-run channel flip**: start with `channel="stable"`, drive one poll, flip the cfg to `channel="prerelease"`, drive another poll → the second poll hits the prerelease endpoint. Tests the re-read-on-tick semantics.
  - **304 path**: server returns 304 → no spawn, backoff stays at 0.
  - **5xx path**: server returns 503 three times then 200 → backoff increments then resets.
  - **LCI conformance under load**: launch Subsystem, immediately Stop; assert Stop returns within 200ms (no in-flight HTTP holds it).
- Run under `-race`.

## Commit 11 — Wire updater into Execute (S)

- In `internal/svc/handler.go`'s `Execute`, after the existing telemetry + evtspike setup, construct an updater Subsystem with the service-level cancel func. Add to the (newly minted, but not yet generalized) subsystems-to-Stop list.
- Stop ordering: updater stops alongside telemetry workers, before telDB.Close.
- This commit does NOT introduce the LCI-driven `[]Subsystem` registry — that's deferred to a future STP migration. We just call `updaterSub.Start(ctx)` and `defer updaterSub.Stop()` inline alongside the existing pattern.
- Verify: `go test -race ./internal/svc/...`.

## Commit 12 — Release notes + docs/guide.html update (S)

- Add an "Auto-update" section to `docs/guide.html` describing the `update` config object, the default cadence, and the opt-out path.
- Mention in the release notes that the next release will be the first to auto-deploy to hosts running this version.
- Verify: `pnpm -C frontend build` if the guide is templated; otherwise just visual review.

## Commit 13 — Manual smoke test (no code change; checklist) (M)

Not a commit; a pre-merge gate. Run on one Windows VM:
- Install the pre-merge release (e.g., `26.116.17` if not yet superseded).
- Replace the binary with the 010-branch build via manual MSI install.
- Verify the updater starts (file log shows `update=poll_start ...` between 5–15 min after service start).
- Push a synthetic newer release (private staging repo or a forked build with bumped version) and observe the updater downloads, verifies, spawns msiexec, the service stops, and a new version is running 30–60s later.
- Note any rough edges in the PR description.

## What is NOT in tasks (deferred)

- `drainctl update check` CLI verb.
- Dashboard UI surfacing version-distribution / update-status.
- Pinning, channel separation, staged rollout.
- Pre-flight health check + automatic rollback on a failed install.
