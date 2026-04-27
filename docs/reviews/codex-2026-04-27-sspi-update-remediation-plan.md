# Codex Remediation Plan — 2026-04-27 (SSPI + Auto-Update)

## Ordering Rationale

B1 ships first because it is a live concurrency bug in `internal/dashboard/sspi.go`: any other test running in parallel (or just `go test ./...`) can hit the use-after-release path, masking unrelated failures with confusing crashes. H1 and M5 are clustered next because both touch the `selfmetrics` package and its boundary with `dashboard` — doing M5 alone would create churn that H1 immediately re-shapes. After the leaf-package extraction lands, the remaining items are sequenced by blast radius: H2 (release-pipeline footgun, single-file Justfile fix) → M4 (additive schema field, trivial) → M2 (test-only, depends on no other change) → M3 (docs only). M1 is intentionally last among the code changes because it introduces durable state under `%ProgramData%`, a new failure mode (corrupt state file), and design judgment (where to put it, what to do on tamper) — the riskiest non-blocker, best done with everything else green.

---

## Step 1 — B1: SSPI reaper race (Blocker)

- **Files**: `internal/dashboard/sspi.go:143-148` (shutdown drain) and `internal/dashboard/sspi.go:153-161` (timer reaper).
- **Change**: In both `pending.Range` callbacks, replace the bare `pending.Delete(key)` followed by `releasePendingContext(value.(*pendingCtx))` with `if pending.CompareAndDelete(key, value) { releasePendingContext(value.(*pendingCtx)); sspiPendingContexts.Add(-1) }`. This atomically claims the entry; if a concurrent `LoadAndDelete` in the leg-2 handler has already taken the entry, `CompareAndDelete` returns false and the reaper skips both the release and the counter decrement, leaving ownership cleanly with the handler.
- **Verify**: Two complementary tests in `internal/dashboard/sspi_test.go`. The reaper cleanup runs INSIDE the `Range` callback (`sspi.go:153-161`); the tests model that path directly.

  (1) `TestPendingReaper_NoRaceReleasesEntry` — call `resetSspiCounters()`, then `sspiPendingContexts.Add(1)` to mirror the "first-leg stored an entry" pre-state, then `sspiLiveContexts.Add(1)` to mirror the new-context-allocated pre-state. Insert an old `*pendingCtx` keyed by K. Run the reaper-cleanup snippet inline with the actual `value` from a `pending.Range` walk. Assert `fakeReleaser.released == 1`, the map is empty, and BOTH counters are back to 0. (Proves the fix didn't break the happy path.)

  (2) `TestPendingReaper_LosesCompareAndDeleteWhenHandlerWonFirst` — the regression test for B1, modeling the racy interleaving as it actually unfolds in production: a leg-2 handler wins `pending.LoadAndDelete(K)` AFTER `Range` has yielded `(K, value)` to the reaper but BEFORE the reaper's cleanup runs. To model that handler win, the test must mirror BOTH halves of the production handler path at `sspi.go:189-192`: `pending.LoadAndDelete(K)` AND `sspiPendingContexts.Add(-1)`. Sequence:
  - `resetSspiCounters()`. Then mirror the pre-state: `sspiPendingContexts.Add(1); sspiLiveContexts.Add(1)`. Insert `pc` keyed by K.
  - Save `value` (what `Range` would have yielded).
  - Test goroutine simulates the leg-2 handler winning: `pending.LoadAndDelete(K)` AND `sspiPendingContexts.Add(-1)`. (Simulating only the LoadAndDelete without the counter decrement would diverge from production and corrupt the counter assertion.)
  - Now invoke the reaper's fixed cleanup snippet: `if pending.CompareAndDelete(K, value) { releasePendingContext(pc); sspiPendingContexts.Add(-1) }`.
  - Assert: `fakeReleaser.released == 0` (CAS returned false → no double-release of the kernel handle); `sspiLiveContexts.Load() == 1` (handler hasn't called its own deferred `sc.Release()` in the test, so live should remain at 1 — the point is the reaper didn't drive it to 0 prematurely); `sspiPendingContexts.Load() == 0` (1 from setup, -1 from handler model, 0 net; reaper CAS-fail did NOT decrement again).
  - The buggy code (`pending.Delete(K) + releasePendingContext(pc)`) would fail this test: bare Delete-on-missing is a no-op, but `releasePendingContext` still calls Release and decrements Live; the bare `sspiPendingContexts.Add(-1)` after it would drive Pending to -1.
  - Also run `go test -race ./internal/dashboard/...`.
- **Effort**: S.
- **Risk**: If the reaper is refactored into a helper, keep the `value any` signature so the `CompareAndDelete` value comparison still uses the original `any` interface header that `Range` handed it (pointer equality on the interface — `*pendingCtx` is the dynamic type). Do NOT pre-cast to `*pendingCtx` and pass the cast value — `sync.Map` compares the `any`/`interface{}` values, so a roundtripped value is still equal, but it is cleaner to pass `value` straight through to avoid surprising future readers.

---

## Step 2 — H1: Extract leaf package `internal/sspimetrics`

- **Files**: NEW `internal/sspimetrics/sspimetrics_windows.go`; `internal/dashboard/sspi.go:58-74` (remove counters + `SspiMetrics`); `internal/selfmetrics/selfmetrics_windows.go:19,114` (swap import + caller).
- **Change**: Create `internal/sspimetrics/sspimetrics_windows.go` with `//go:build windows`, exporting `var Live atomic.Int64`, `var Pending atomic.Int64`, and `func Snapshot() (live, pending int64)`. In `internal/dashboard/sspi.go`: delete the package-level `sspiLiveContexts atomic.Int64`, `sspiPendingContexts atomic.Int64`, and `func SspiMetrics()`; rename every CALLER reference — i.e., change every `sspiLiveContexts.Add(...)` to `sspimetrics.Live.Add(...)` and every `sspiPendingContexts.Add(...)` to `sspimetrics.Pending.Add(...)` (this is the actual rename of identifiers, not just a textual substitution). Update test files in lockstep (`internal/dashboard/sspi_test.go` `resetSspiCounters` helper; the three `SspiMetrics()` assertions become `sspimetrics.Snapshot()` calls; the new B1 race tests added in Step 1 also need their `sspiLiveContexts`/`sspiPendingContexts` references rewritten — Step 1 lands FIRST so its tests use the old names; this Step 2 commit renames them to `sspimetrics.Live`/`sspimetrics.Pending`). In `selfmetrics_windows.go` swap the `dashboard` import for `sspimetrics` and call `sspimetrics.Snapshot()`.
- **Verify**: `go build ./...`, `go test ./internal/dashboard/... ./internal/selfmetrics/... ./internal/sspimetrics/...`, then `go vet ./...`. Confirm via Grep tool: search pattern `dashboard\.SspiMetrics` across `internal/` should return zero matches; search pattern `sspiLiveContexts|sspiPendingContexts` (the old names) across `internal/dashboard/` should also return zero. Pre-commit (gofmt, go vet, golangci-lint, gitleaks) must pass.
- **Effort**: M.
- **Risk**: The new package must keep `//go:build windows` at the top of every `.go` file (project convention) even though the code is platform-agnostic — `dashboard` and `selfmetrics` are windows-only and a non-tagged dependency would break their build matrix asymmetrically. Do NOT use `init()` to initialize counters; use plain `var`.

---

## Step 3 — M5: `selfmetrics.Subsystem` owns its cancel

- **Files**: `internal/selfmetrics/selfmetrics_windows.go:35-66`.
- **Change**: Add `ctx context.Context` and `cancel context.CancelFunc` fields to `Subsystem`. In `Start`, do `s.ctx, s.cancel = context.WithCancel(ctx)` and pass `s.ctx` to `s.run` (or close over it). In `Stop`, inside `stopOnce.Do`, call `if s.cancel != nil { s.cancel() }` BEFORE `s.wg.Wait()`. Mirrors the `internal/evtspike/subsystem.go` pattern at `:151,:214` and `internal/updater/updater_windows.go:111-114,168-194`.
- **Verify**: Existing `internal/selfmetrics/selfmetrics_test.go` tests must still pass. Add `TestSubsystem_StopWithoutCtxCancel` that calls `New(1*time.Millisecond)`, `Start(context.Background())` (a non-cancellable parent), waits for at least one `emit`, then calls `Stop` and asserts it returns within 100ms. Run `go test -race ./internal/selfmetrics/...`.
- **Effort**: S.
- **Risk**: Calling `Stop` before `Start` must remain safe — the `s.cancel != nil` guard handles that. Keep `stopOnce` so a double-Stop after Start is still idempotent.

---

## Step 4 — H2: Fail-loud manifest signing in `just release` AND publish sidecars

- **Files**:
  1. `Justfile:300-318` — `sign-release-manifest` recipe.
  2. `Justfile:354-398` — `publish` recipe (`$assets = @($msiPath)` is the gap codex caught: even a correctly signed local build produces a GitHub release missing the sidecars).
  3. Optionally extend `cmd/release-sign/main.go` with a `check-keys-file` subcommand.
- **Change** (two coupled edits, both required):
  1. **`sign-release-manifest`**: replace the silent `Skipping manifest signing` early-return with a check that inspects `internal/updater/keys_windows.go`. Recommended approach: `& go run "{{justfile_directory()}}/cmd/release-sign" check-keys-file --keys "{{justfile_directory()}}/internal/updater/keys_windows.go"` exits 0 if `releaseSigningKeysB64` is empty (transition mode allowed) and exits non-zero if any non-empty entry exists but `$env:RELEASE_SIGNING_KEY` is unset. The recipe wraps that check: exit 0 AND env unset → keep yellow "skip" message; check exits non-zero → propagate `$LASTEXITCODE` with a red error. Implement `check-keys-file` in `cmd/release-sign/main.go` by reusing the `go/parser` walk from `addKeyToReleaseSigningKeysB64` to count non-empty string literal entries.
  2. **`publish`**: change `$assets = @($msiPath)` to also append `release.json` and `release.json.sig` from `dist/` IF they exist on disk. Use Just template interpolation (NOT a PowerShell `$dist_dir` variable, which doesn't exist in scope): `$manifestPath = "{{dist_dir}}/release.json"; if (Test-Path $manifestPath) { $assets += $manifestPath }` and the same for `release.json.sig`. The `@assets` splatting into `gh release create $tag @assets ...` (already on line 396) handles space-quoting correctly under pwsh. Without this, every `gh release create` invocation silently omits the sidecars, breaking the entire manifest-verification chain on fielded binaries — H2's Justfile fix is incomplete without it.
- **Verify**:
  - `sign-release-manifest`: (a) env set + keys present → signs; (b) env unset + keys present → red error, exit non-zero; (c) env unset + keys file emptied → yellow skip, exit 0.
  - `publish`: drive the recipe with a `pwsh` invocation that captures the final `$assets` array contents and asserts it. Two scenarios: (a) only the MSI exists in `dist/` → `$assets` length = 1; (b) all three files exist → `$assets` length = 3 and contains both sidecar paths. Implement as either a small `_test.ps1` script run from `cmd/release-sign/main_test.go` via `os/exec` (gated by a `_windows` build tag), or as a Justfile recipe `verify-publish-assets` that runs the assertion. The codex iter-3 critique was right that "manually inspect the recipe" is too soft — execute it. Add `cmd/release-sign/main_test.go` cases for `check-keys-file` against fixture `.go` files (one non-empty entry; all-empty/commented).
- **Effort**: M.
- **Risk**: Don't shell out to a different `go` than what `PATH` provides — keep `& go run ...` form. Check `$LASTEXITCODE` immediately after each native invocation. For `publish`, gating sidecar inclusion on `Test-Path` rather than always-required preserves the transition-mode flow (no keys → no sidecars → no upload of nothing). Once keys are embedded, `sign-release-manifest` will refuse to skip, which means the sidecars will exist before `publish` runs — closing the loop.

---

## Step 5 — M4: Add `schema_version` to `ReleaseManifest`

- **Files**: `internal/updater/manifest.go:25-29` (struct), `internal/updater/manifest.go:75-89` (verifier), `cmd/release-sign/main.go` `sign` subcommand (signer must emit it), `internal/updater/manifest_test.go` and `internal/updater/manifest_remote_windows_test.go` (fixtures).
- **Change**: Add `SchemaVersion int \`json:"schema_version"\`` as the first field of `ReleaseManifest`. Define `const ManifestSchemaVersion = 1` exported. In the signer, always set `SchemaVersion: ManifestSchemaVersion`. In `VerifyReleaseManifest`, after `json.Unmarshal`:
  - **Transition acceptance window**: accept `m.SchemaVersion == 0` (the legacy/missing case — manifests produced by this PR before this step lands) AND `m.SchemaVersion == 1`. Reject any other value with `ErrManifestSchema`.
  - This is intentional: at the time this plan ships, manifests already generated by `cmd/release-sign sign` exist on developers' disks (and possibly one published prerelease). Hard-rejecting `0` would break the first release after this lands. The acceptance window ships in the same commit; a follow-up commit (after the first release with `schema_version: 1` ships and is verified live) tightens to `m.SchemaVersion == 1` only. Track that follow-up in `specs/010-auto-update/v1.1` (per Step 7).
  - **Safety scope of the transition window**: accepting `0` does NOT introduce a new bypass — the existing trust chain (Ed25519 signature verification + asset-name match + SHA-256 binding) is unchanged for `0` and `1` alike. It does, however, leave a legacy schema-0 manifest usable for as long as the window is open, so the threat model during the window is "an attacker who can replay or forge a schema-0 manifest signed by a key still in `releaseSigningKeysB64`" — which is identical to the threat model BEFORE this step lands. The window doesn't make security worse; it just doesn't make it stricter yet. (Note for future hardening: the updater currently does not bind `ReleaseManifest.Version` to GitHub `rel.tag` in `tick()`, so a schema-0 manifest signed under one tag could in principle be served against a different tag. That's a preexisting gap, not introduced by this step. Track separately in spec 010 v1.1 if desired.)
- **Verify**: `TestVerifyReleaseManifest_AcceptsLegacyZeroSchemaVersion` (legacy field-missing → unmarshals to 0 → accepted); `TestVerifyReleaseManifest_AcceptsCurrentSchemaVersion` (1 → accepted); `TestVerifyReleaseManifest_RejectsUnknownSchemaVersion` (2 → `ErrManifestSchema`). `cmd/release-sign sign` integration: produce a manifest, parse it back, assert `schema_version == 1`. Existing happy-path tests in `manifest_remote_windows_test.go` keep working without touching fixtures (the literal omits SchemaVersion → 0 → still accepted in the window).
- **Effort**: S.
- **Risk**: When the follow-up tightening commit drops the `0` acceptance, every release that currently exists must already be re-signed with `schema_version: 1` or the in-field updaters running the tightened binary will refuse them. Make the follow-up explicitly conditional: "land only after `>= 1` releases on every active channel carry `schema_version: 1`." Document this in spec 010 v1.1.

---

## Step 6 — M2: Keys-on integration test

- **Files**: `internal/updater/updater_integration_windows_test.go:32-87` (extend `fakes` struct + `runUpdater`), NEW test in same file.
- **Change** (three coupled edits in the same file, all required for the verification to actually observe ordering):
  1. **`decodeKeys` seam shape**: do NOT add a `signingKeys []ed25519.PublicKey` field — a nil slice can't distinguish "field omitted, default to transition-mode" from "explicit transition mode" unambiguously. Instead add `decodeKeys func() ([]ed25519.PublicKey, error)` to the `fakes` struct, mirroring the existing function-typed seam pattern (`fetchRelease`, `verifyMSI`, `spawnMSI`, `downloadMSI`). In `runUpdater`, the swap rule is: if `f.decodeKeys != nil` → swap to `f.decodeKeys`; else → swap to `func() ([]ed25519.PublicKey, error) { return nil, nil }` (transition-mode default, preserving every existing test). Tests opting into keys-on set `f.decodeKeys = func() ([]ed25519.PublicKey, error) { return []ed25519.PublicKey{pub}, nil }`. This solves the nil-slice ambiguity and matches the rest of the seam pattern in the file.
  2. **`verifyManifest` seam**: add `verifyManifest func(ctx context.Context, client *http.Client, msiPath, manifestURL, sigURL string, keys []ed25519.PublicKey) error` to the `fakes` struct. Signature verified against `internal/updater/updater_windows.go:335`. Swap pattern mirrors the existing `verifyMSI` swap at `runUpdater:45-49`. The plan's earlier draft glossed over this — without it, tests can't observe the `verifyManifest` call ordering through `tick()`.
  3. **`TestRunUpdater_KeysOn_VerifiesManifestBeforeMSI`**: generate a real keypair via `mustGenKey`. Stand up an `httptest.Server` serving the MSI body (and, in the variant exercising the REAL `verifyManifestRemote`, also serving the signed manifest + sig at separate endpoints). Wire `fetchRelease` to return a `release` record whose `assetURL`/`manifestURL`/`sigURL` point at the server. Use a shared `var order atomic.Int32` and two captured ints `manifestOrder, msiOrder`; each fake records `order.Add(1)` into its own captured int on first call. Set `f.decodeKeys` to return the real `[]ed25519.PublicKey{pub}`. Assert both `manifestOrder > 0` and `msiOrder > 0` AND `manifestOrder < msiOrder`. Provide TWO sub-test variants: (a) `verifyManifest` is a fake that just records order and returns nil — proves orchestration ordering; (b) NO `verifyManifest` fake (uses production `verifyManifestRemote`) plus a real signed manifest+sig served by `httptest` — proves end-to-end wiring. Both must pass.
- **Verify**: `go test ./internal/updater/ -run KeysOn -race`. Existing integration tests must remain green (they continue to opt into `signingKeys=nil`, `verifyManifest=nil`).
- **Effort**: M.
- **Risk**: The `verifyManifest` seam currently has the exact signature `func(ctx, client, msiPath, manifestURL, sigURL string, keys []ed25519.PublicKey) error` (six args including the keys parameter from the LCI fix). The fake field type must match exactly or the swap won't compile. Use `httptest.NewServer` not `NewTLSServer`. Don't substitute `decodeKeys` AND `verifyManifest` simultaneously to a nop — at least one variant of the test must drive both real seams to prove the wiring.

---

## Step 7 — M3: Spec drift — append v1.1 signed-manifest section

- **Files**: `specs/010-auto-update/spec.md`, `specs/010-auto-update/plan.md`, `specs/010-auto-update/tasks.md`, `specs/010-auto-update/contracts/github-releases-api.md:78-82`.
- **Change**: Append a `## v1.1 — Signed Manifest Verification` section to `spec.md` covering: (1) threat model addition (forged-CN + valid Authenticode), (2) new asset contract (`release.json`, `release.json.sig` MUST accompany every release once any key is embedded), (3) transition mode (empty `releaseSigningKeysB64` → verifier is a no-op), (4) key-rotation flow (ship N+1 with both old and new key, then rotate, then ship N+2 dropping old). Update the GitHub Releases API contract section to list both sidecar assets. Add corresponding entries to `plan.md` and a `tasks.md` checkbox section marking the implementation complete.
- **Verify**: Manual review by the orchestrator; ensure the v1 "we explicitly DO NOT depend on assets[].digest" line at `contracts/github-releases-api.md:78-82` is either struck through or moved into a "v1.0 → v1.1 evolution" note. No code or test impact.
- **Effort**: S.
- **Risk**: Documentation drift between spec.md, plan.md, and tasks.md — make the v1.1 section the single source of truth and have the other two files cross-link to it.

---

## Step 8 — M1: Highest-seen-version pin (replay/freeze defense)

- **Files**: NEW `internal/updater/state_windows.go`; NEW `internal/updater/state_windows_test.go`; `internal/updater/updater_windows.go:277-281` (version gate, plus install-success path).
- **Change**: Add `type updateState struct { HighestSeenVersion string \`json:"highest_seen_version"\` }` persisted as JSON via `encoding/json`. Path: `%ProgramData%\LISS Technologies\LISSTech DrainCtl\update-state.json`, resolved via the same helper as evtspike (mirror `internal/evtspike/subsystem.go` baseline-path code).

  **Atomic write — match the repo convention, not `os.Rename`.** Use the named-mutex + temp + `windows.MoveFileEx(src, dst, MOVEFILE_REPLACE_EXISTING|MOVEFILE_WRITE_THROUGH)` pattern from `internal/evtspike/baseline.go:WriteBaseline` (lines 64-110 are the canonical implementation).

  **Mutex name**: use `Global\DrainCtlUpdaterState` — distinct from evtspike's `Global\DrainCtlEvtSpikeBaseline` so unrelated files don't contend. Per-target naming is the right choice; sharing one mutex across unrelated state files would serialize updates needlessly.

  **Inline vs shared helper**: inline the pattern into `state_windows.go` for this PR. The inline copy is acceptable given evtspike already inlines config.go's pattern. A future commit can factor an `internal/atomicfile` package once a third caller appears (track in spec 010 v1.1 backlog). Picking the helper extraction now would be premature generalization across two callers.

  **Test seams (three layers)**:
  - `updateStatePath func() string` — resolves the on-disk path. Default implementation reads `os.Getenv("ProgramData")` and joins the LISS Technologies subdir + filename. Tests override to point at `t.TempDir()`. Required to drive the production load/save against an isolated filesystem.
  - `loadUpdateState func() (updateState, error)` — default implementation reads from `updateStatePath()` and parses JSON. Tests testing the gate logic in isolation override this to return a canned state.
  - `saveUpdateState func(updateState) error` — default implementation writes to `updateStatePath()` via the named-mutex+`MoveFileEx` path. Tests testing the gate logic override this to capture writes; tests testing the production atomic-write path leave it default and override only `updateStatePath`.
  - All three are package vars assigned at init time, swapped via the standard `prev := X; t.Cleanup(func(){ X = prev }); X = fake` pattern.

  **Version comparison must be parsed, not stringly**: `parseVersion("v26.116.17")` and `parseVersion("26.116.17")` are equal, but their `String()` forms may not be byte-identical depending on the formatter. Always compare via `parseVersion(state.HighestSeenVersion)` against `remote` (a parsed version), never via raw string compare. Add an `equals(other version) bool` helper alongside `less` if one doesn't exist, defined as `!a.less(b) && !b.less(a)`.

  **When to enforce the gate** (in `tick()`, after parsing `remote` and `current`, BEFORE the `current.less(remote)` check):
  - If `state.HighestSeenVersion == ""` → no gate (first-run case; the seed step below still runs).
  - Otherwise parse `highSeen := parseVersion(state.HighestSeenVersion)`. If `remote.less(highSeen)` (strict `remote < highSeen`) → log `update=refused stage=replay highest_seen=… remote=…` and return. The `remote.equals(highSeen)` case passes through (legitimate re-poll of the current pinned release).

  **When to PERSIST highest-seen — explicit rule** (codex flagged earlier wordings as either too eager or logically inconsistent):
  - **At Subsystem.Start**: load state; compute `seed := max(highSeen, parseVersion(dc.Version))`; if `seed > highSeen` → save. This protects fresh installs running v50 from being rolled back to a signed v40 before any successful poll has run. It also covers the edge case where a manual MSI install advanced the binary past `highSeen`.
  - **In `tick()` install-success path**: only after `verifyManifest(MSI) → verifyMSI(MSI) → spawnMSI(MSI)` ALL returned nil AND `triggerSelfShutdown` has fired → save `remote` as new highest (since `remote > highSeen` is implied by the gate having let it through and `current.less(remote)` having been true).
  - Do NOT save on the up-to-date branch (`!current.less(remote)`). Codex caught a regression in an earlier draft: when `current > remote` AND `highSeen` is empty, saving `remote` would record an OLDER version than `current`, weakening rather than strengthening the pin. The Start-time seed already records `dc.Version` so up-to-date polls have nothing to add.
  - Do NOT save at fetch time, do NOT save before manifest+authenticode verification, do NOT save if any verifier rejected, do NOT save if `spawnMSI` returned an error.
- **Verify**: New `internal/updater/state_windows_test.go`. Tests that exercise gate LOGIC fake `loadUpdateState`/`saveUpdateState`. Tests that exercise PRODUCTION load/save fake only `updateStatePath` (pointing at `t.TempDir()`) so the real filesystem code runs against an isolated dir.
  - `TestStartSeedsHighestSeenFromVersion` — empty state file, dc.Version=26.116.50 → after Subsystem.Start, persisted state has HighestSeenVersion=26.116.50.
  - `TestUpdateState_RejectsReplayBelowHighestSeen` (highest=26.116.50, remote=26.116.20 → `remote.less(highSeen)` true → refused; install seam never called).
  - `TestUpdateState_PassesEqualToHighestSeen` (highest=26.116.50, remote=26.116.50 → `remote.equals(highSeen)` → pass through to up-to-date or install branch).
  - `TestUpdateState_AcceptsForwardProgress` (highest=26.116.20, remote=26.116.50, current=26.116.20 → install seams called, state saved with HighestSeenVersion=26.116.50 only after spawn success).
  - `TestUpdateState_FirstRunNoStateFile` (no state file on disk → load returns zero-value, no refusal; Start seeds the file).
  - `TestUpdateState_DoesNotPersistOnVerifyFailure` (verifyManifest fake returns error → state file unchanged from pre-tick value).
  - `TestUpdateState_DoesNotPersistOnSpawnFailure` (spawnMSI fake returns error → state file unchanged from pre-tick value).
  - `TestUpdateState_DoesNotPersistOnUpToDateBranch` (highest=26.116.20, remote=26.116.20, current=26.116.20 → up-to-date branch hit, state file unchanged because Start already seeded it).
  - `TestUpdateState_VersionComparisonHandlesPrefixForms` (persisted "v26.116.50" must equal observed "26.116.50" and not be flagged as replay; uses parsed comparison, not String compare).
  - `TestUpdateState_CorruptFileFailsOpen` — write garbage to the path returned by `updateStatePath()`, call `loadUpdateState` → assert it returns zero-value + a warning is logged; next `saveUpdateState` writes valid JSON over the garbage. State-file integrity must NOT brick the updater. Uses real load/save with `updateStatePath` overridden to `t.TempDir()`.
  - `TestUpdateState_ConcurrentSaves_NoTornWrite` — override `updateStatePath` to `t.TempDir()`. Spawn N=10 goroutines, each calling production `saveUpdateState` with a different `HighestSeenVersion` value. Wait, then load and assert the resulting JSON parses cleanly and matches one of the N inputs (no torn/partial bytes). This actually exercises the named-mutex+`MoveFileEx`+`WRITE_THROUGH` path; merely asserting the temp file is gone (as an earlier draft proposed) does not prove the mutex was acquired or that the write was atomic.
  - Run `go test -race ./internal/updater/...`.
- **Effort**: L.
- **Risk**:
  1. **Persistence rule wording matters.** The plan's earlier draft was internally inconsistent (codex caught this); the rewrite above is the precise rule. Don't deviate during implementation.
  2. **Atomic write must match repo convention.** `os.Rename` alone is weaker than `MoveFileEx + WRITE_THROUGH + named mutex` — the latter survives concurrent writers and forces flush-to-disk. Picking the wrong primitive risks lost state under power loss.
  3. **Corrupt file MUST fail open** (treat as "no highest seen" + log warning + overwrite on next successful save). A corrupt state file blocking all future updates is exactly the bricking failure mode this defense is supposed to prevent.
  4. **State-file ACL** under `%ProgramData%` is inherited from the parent dir; LocalSystem-write is fine but a non-LocalSystem service install needs the same path resolution as evtspike. Reuse the same path-resolution helper.
  5. **`//go:build windows`** at top of every new `.go` file.
  6. **Faking at `loadUpdateState`/`saveUpdateState` is sufficient for the gate logic but does not validate the real atomic-write/parsing path** — that's what `TestUpdateState_AtomicWritePattern` and `TestUpdateState_CorruptFileFailsOpen` cover by going through the production save/load functions against `t.TempDir()` paths.

---

## Items intentionally not fixed in this plan

### P1 — AST surgery in `cmd/release-sign/main.go`
**Not fixed.** The synthesis confirmed the implementation is correct (uses `go/parser`+`go/format`+`strconv.Quote`, no injection vector). Fragility is real but pragmatic; replacing it with a `go:embed`-driven generated file or a separate `keys.json` would be churn for marginal benefit at the current rotation cadence (every 1-3 years per spec 010). Track in spec 010 v1.1 backlog as "consider when rotation friction shows up."

### P2 — `internal/updater` export surface (`ReleaseManifest`, `VerifyReleaseManifest`, `HashFileSHA256`)
**Not fixed.** A subpackage carve-out (`internal/updater/manifest`) would shrink the surface but require `cmd/release-sign` import-path churn and one more package boundary on what is currently a tight bundle. Revisit when `internal/updater` grows another consumer beyond `cmd/release-sign`.

### P3 — TOCTOU on temp MSI between verify and msiexec spawn
**Not fixed.** `%TEMP%` for LocalSystem is `C:\Windows\Temp` (admin-only ACL); an attacker who can write there already has the keys to the kingdom and can subvert the binary directly. Hardening (re-hash post-spawn-prep, or fd-based `msiexec` invocation if Windows even allows it) adds non-trivial complexity for a threat model already lost. Document the assumption explicitly in the v1.1 spec section (Step 7) under "Out of scope: local-admin attacker."
