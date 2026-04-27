# Codex Review Synthesis — 2026-04-27

**Scope**: uncommitted work on `develop` vs `trunk`. SSPI orphan-on-Store leak fix
(`internal/dashboard/sspi.go`), Auto-update CN fix + Ed25519 manifest verification
(`internal/updater/*`, `cmd/release-sign/*`), Selfmetrics → LCI subsystem migration
(`internal/selfmetrics/*`), and supporting Justfile/test changes.

**Three lenses**: correctness, security, design/maintainability. All claims below
verified against the actual code. Severity = blast radius × likelihood.

## Confirmed — Blocker

### B1. SSPI reaper race — double-release + counter underflow
**File**: `internal/dashboard/sspi.go:143-148` (shutdown drain) and `:153-161`
(timer reaper). Both use `pending.Range(...)` followed by bare `pending.Delete(key)`.
This does NOT atomically claim ownership of the value. A concurrent leg-2 handler
running `pending.LoadAndDelete(connKey)` for the same key can win the entry first;
the reaper then unconditionally calls `releasePendingContext(value)` on a
`*pendingCtx` the handler is still using inside `sc.Update(token)`.

**Failure modes**:
- Use-after-release of the SSPI kernel handle inside `sc.Update`.
- `defer sc.Release()` in the leg-2 success path runs as a second release on the
  same context — the file's own contract says "Caller must not invoke twice on
  the same pc."
- `sspiLiveContexts` decrements twice for a single allocation; can drift below
  zero, masking the very leaks the counter exists to detect.

**Fix direction**: replace `pending.Delete(key) + release(value)` with
`pending.CompareAndDelete(key, value)` and only release on success.
`CompareAndDelete` deletes-iff-current-value-matches, defeating both the
"already-removed" and "swapped-to-different-pc" races. Same fix applies to both
reaper branches.

This bug was *introduced by this PR* — the original code had a different bug
(silent overwrite at `Store`) which the Swap path fixes, but the cleanup paths
were not adjusted to match.

## Confirmed — High

### H1. Layering inversion: `internal/selfmetrics` imports `internal/dashboard`
**Files**: `internal/selfmetrics/selfmetrics_windows.go:19`,
`internal/dashboard/sspi.go:58-74`. A generic observability package now reaches
into an HTTP/auth package's internals. The SSPI counters were originally
package-private in dashboard; we exported `SspiMetrics()` solely so selfmetrics
could read them. That's the wrong direction: selfmetrics should be a leaf or
mid-stack package, dashboard a higher-level one.

**Fix direction**: extract the two atomics + accessor into a tiny leaf package
(e.g. `internal/sspimetrics`). Both `dashboard` (writer) and `selfmetrics` (reader)
import it. Removes the inversion and shrinks the `dashboard` public surface.

### H2. `just release` silently skips manifest signing
**File**: `Justfile:300-318` (`sign-release-manifest`). When `RELEASE_SIGNING_KEY`
is unset, the recipe prints "Skipping manifest signing" and returns 0. With keys
embedded in `keys_windows.go` (which they now are), a release built without the
env var ships an MSI that every fielded binary will refuse — `update=refused
stage=manifest`.

**Fix direction**: make the recipe fail-loud when `keys_windows.go` contains any
non-empty entry but `RELEASE_SIGNING_KEY` is unset. The recipe can grep the keys
file (or the `release-sign` CLI gains a `--check-keys-file` mode that exits
non-zero if keys are configured but no env var). No silent path.

## Confirmed — Medium

### M1. Update replay/freeze — no highest-seen-version pin
**File**: `internal/updater/updater_windows.go:277`. The only version gate is
`current.less(remote)`. An attacker who can replay older but still-validly-signed
GitHub responses (compromised CDN, malicious release operator, sustained MITM)
can stall a fleet on a signed-but-stale newer release, blocking delivery of a
later security fix. Not a rollback (`<= current` is rejected), but a freeze.

**Fix direction**: persist the highest version ever seen (in
`%ProgramData%\...\update-state.json` or the existing config dir) and reject any
remote `<` highest-seen even if `> current`. Also fail loud if a binary that
previously saw v26.116.50 sees v26.116.20 — that's the diagnostic signal.

This was an accepted v1 risk in spec 010, but the manifest work changed the
threat model enough to revisit.

### M2. Integration-test coverage degraded
**File**: `internal/updater/updater_integration_windows_test.go:69`. `runUpdater`
unconditionally swaps `decodeKeys` to return `nil`, putting every existing
integration test into transition mode. With a real key now embedded in
`keys_windows.go`, no test exercises the production keys-on path through
orchestration end-to-end (the fetch-MSI → fetch-manifest → fetch-sig →
verifyManifest sequence). `manifest_remote_windows_test.go` covers the verifier
in isolation, so it's not a complete coverage hole — but a regression in
*ordering* (e.g. accidentally calling verifyMSI before verifyManifest) would
slip through.

**Fix direction**: add one integration test that drives `runUpdater` with
non-empty keys + an `httptest.Server` that serves both the MSI and the signed
sidecar assets, asserting both get fetched and verifyManifest runs first.

### M3. Spec drift on feature 010
**Files**: `specs/010-auto-update/{plan,spec,tasks}.md`,
`specs/010-auto-update/contracts/github-releases-api.md:78-82`. The spec
explicitly rejected manifest signing for v1 ("we explicitly DO NOT depend on
assets[].digest"). Implementation now adds Ed25519 manifest verification with a
new asset contract (`release.json`, `release.json.sig`).

**Fix direction**: append a "v1.1 — signed manifest" section to spec.md
documenting the new flow, threat model, transition mode, key-rotation contract,
and updated GitHub asset contract.

### M4. Manifest schema has no version field
**File**: `internal/updater/manifest.go:22-29`. `ReleaseManifest` carries the
release version, not the schema version. Future shape changes have no clean
"older binaries reject this" path — they'll just silently drop unknown fields
(`json.Unmarshal` default).

**Fix direction**: add `"schema_version": 1` to the JSON. Verifier rejects
manifests with `schema_version` outside the supported set. Trivial.

### M5. `selfmetrics.Subsystem.Stop` doesn't actively quiesce
**File**: `internal/selfmetrics/selfmetrics_windows.go:55-65`. Stop only `Wait`s
on the wg; it relies on the caller cancelling ctx first. The canonical LCI
implementations (`internal/evtspike`, `internal/updater`) hold their own
derived `ctx + cancel` so Stop can call `cancel()` then `Wait` and be
self-contained.

**Fix direction**: Subsystem holds `ctx, cancel := context.WithCancel(parent)`
in Start; Stop calls `cancel()` first. Matches house pattern.

## Partial / Tracked, not blocking

### P1. AST surgery in `cmd/release-sign/main.go`
**File**: `cmd/release-sign/main.go:124-204`. Codex flagged the AST rewrite of
`keys_windows.go` as long-term fragile (depends on file shape, requires writable
checkout). Verified: the implementation uses `go/parser` + `go/format` correctly
with `strconv.Quote` so injection via key bytes is impossible. **PARTIAL**:
fragility is real but pragmatic — a generated-file approach can be considered
later if rotation friction shows up. Not a blocker.

### P2. Broadened `internal/updater` public-to-module surface
Codex flagged `ReleaseManifest`, `VerifyReleaseManifest`, `HashFileSHA256` as
internal leakage. Verified: they exist because `cmd/release-sign` consumes them.
A subpackage carve-out (`internal/updater/manifest`) would shrink the surface but
is churn for marginal benefit. **PARTIAL**: track if the package grows further.

### P3. TOCTOU on temp MSI between verify and msiexec
**File**: `internal/updater/updater_windows.go:295-305` (verify) →
`install_windows.go:39-55` (spawn). Verified: the gap exists. The MSI lives at
`%TEMP%\drainctl-update-*.msi`. On a service running as LocalSystem, `%TEMP%` is
`C:\Windows\Temp` (admin-only ACL). An attacker needs admin already, in which
case they can replace the binary directly. **PARTIAL**: low practical risk;
hardening could re-hash post-spawn-prep but adds complexity for a local-admin
threat model that's already lost.

## Rejected — codex claims that didn't hold

(None outright rejected. Codex was unusually disciplined this round — every
claim was either a real finding or self-flagged as "no finding, fail-closed as
designed".)

## Severity ladder

| ID | Severity | One-liner |
|----|----------|-----------|
| B1 | Blocker  | `pending.Range`+`Delete` race in SSPI cleanup; use `CompareAndDelete` |
| H1 | High     | `selfmetrics→dashboard` layering inversion; extract counters to leaf pkg |
| H2 | High     | `just release` silent skip when keys embedded but env var unset |
| M1 | Medium   | Replay/freeze gap; persist highest-seen version |
| M2 | Medium   | Integration tests bypass keys-on path; add one explicit test |
| M3 | Medium   | Spec 010 drift; document v1.1 signed-manifest flow |
| M4 | Medium   | Manifest has no schema_version field |
| M5 | Medium   | `selfmetrics.Stop` doesn't own its cancel; align with house pattern |
| P1 | Track    | AST rewrite fragility — pragmatic for now |
| P2 | Track    | `internal/updater` export surface — defer subpackage carve-out |
| P3 | Track    | Temp-MSI TOCTOU — local-admin threat model |
