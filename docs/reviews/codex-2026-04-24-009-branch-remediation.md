# Remediation Plan — 009 Branch Codex Review (2026-04-24)

Target branch: `009-security-hardening` (off `develop`). All `.go` files keep `//go:build windows`. Pre-commit (`prek`) runs gofmt, go vet, golangci-lint, gitleaks — every step assumes a clean prek pass before commit.

## PR Bundle 1 — Security & correctness fixes (Go)

### Step 1 — B-1: Drop `rejectCloudMetadata` entirely (full spec rollback)
- **Files**:
  - Code: `notify.go` (remove `rejectCloudMetadata` func + call sites), `internal/dashboard/server.go:830-835` (remove the `t.Type == "webhook" || t.Type == "ntfy"` gate that calls it), `notify_test.go` (delete the metadata-IP test cases).
  - Spec rollback (full scope per validation round 2 — every current-policy reference must change; historical review docs under `docs/reviews/` are preserved as-is to keep the decision trail intact):
    - `specs/009-security-hardening/spec.md` — remove FR-009 and every US4 acceptance scenario referencing the metadata IP (lines 71, 76, 83, 136, 154, 167, 215).
    - `specs/009-security-hardening/plan.md` — drop references at lines 8, 35, 65, 107.
    - `specs/009-security-hardening/tasks.md` — mark T050-T054 at lines 137-141 as withdrawn rather than completed; cross-reference this remediation plan.
    - `specs/009-security-hardening/data-model.md:40-45` — remove the NotifyTarget.URL validation entry for cloud metadata.
    - `specs/009-security-hardening/quickstart.md:123` — remove the manual validation step.
    - `specs/009-security-hardening/checklists/requirements.md:36` — remove the requirement line.
    - `specs/009-security-hardening/contracts/notify-url-validation.md` — mark whole contract superseded with a link to this remediation plan.
    - `specs/009-security-hardening/research.md` — **rewrite Decision 3** (not a mere addendum). Replace the body with the updated reasoning: "The 169.254.169.254 literal-string check was bypass-prone (IPv6-mapped addresses, decimal/hex variants, redirect chains via the default Go HTTP client). Dropped in favor of the scheme allowlist (`http`/`https`) alone. On-prem product charter remains — RFC1918 LAN webhooks are legitimate, and cloud deployment is out of scope." Leave the alternatives-considered list intact but annotate each with its rejection reason updated.
    - `README.md:680` — remove the webhook/ntfy hostname-rejection line.
    - `CHRONICLE.md` — one-line reversal entry with reason (IPv6/decimal/hex/redirect bypasses).
- **Change**: Remove the metadata-IP literal-string check; keep the `http`/`https` scheme allowlist (the load-bearing guard). Reason: IPv6/decimal/hex/redirect bypasses make the hedge zero-value while the spec implies a guarantee we don't deliver. Every spec artifact promising the behavior rolls back in the same commit to avoid drift.
- **Test/verify**: Delete the now-dead test cases; add a single test asserting non-http(s) schemes are still rejected. `go test ./...` green. Manual: register a webhook with `http://169.254.169.254/...` and confirm it saves without error (the gate is gone); the on-prem threat model documents this. Verification grep, scoped to exclude `docs/reviews/` (historical review docs intentionally preserve the old decision): `grep -rn "169\.254\.169\.254" specs/ README.md CHRONICLE.md docs/guide.html` should return zero hits outside the CHRONICLE rollback entry and the `docs/reviews/` dir.
- **Effort**: M
- **Sequencing**: Resolves B-2 implicitly (no metadata gate exists for any target type to skip). Land first; C-5 doc work cross-references this.

### Step 2 — B-3: ACL the tmp file before rename + surface `icacls` errors + clean up on failure
- **Files**: `config.go` (`SaveConfig` body around L727-742, and `restrictConfigACL` at L807+).
- **Change**:
  1. Call `restrictConfigACL(tmpPath)` **before** `MoveFileEx`. NTFS same-volume rename preserves the source file's explicit security descriptor, so a tight ACL on the tmp file carries into the final path.
  2. Modify `restrictConfigACL` to return `error`, log via `slog.Error` with the `icacls` exit code/stderr, retry once.
  3. On final ACL failure, `os.Remove(tmpPath)` **before** returning the error so a world-readable tmp file never persists in `%ProgramData%`. Return the error up through `SaveConfig`.
  4. **(round 2)** Also `os.Remove(tmpPath)` on the rename-failure branch: if both `MoveFileEx` and the `os.Rename` fallback fail, the tmp file currently leaks. Extend the existing `if err := windows.MoveFileEx(...); err != nil { ... if renameErr := os.Rename(...); renameErr != nil { ... } }` block so the outer error return includes `os.Remove(tmpPath)`. Use `errors.Join` to preserve both the rename error and any remove error.
  5. With the named mutex held, failing the save loudly is safer than silently leaving secrets world-readable OR leaving a leaking tmp file.
- **Test/verify**:
  - New unit test `TestSaveConfig_TmpFileACLAppliedBeforeRename` — fault-inject a pause between WriteFile and MoveFileEx; read the tmp file's DACL via `windows.GetNamedSecurityInfo`, assert `Users` group has no read.
  - New `TestSaveConfig_FinalPathACLPostRename` — after a successful save, read the DACL of the FINAL path and assert `Users` has no read. This is the load-bearing verification (rename preservation).
  - New `TestRestrictConfigACL_PropagatesIcaclsFailure` using a fault-injected exec seam (similar pattern to evtspike's function-pointer globals, accepted in synthesis §C-8).
  - New `TestSaveConfig_CleansTmpOnACLFailure` — fault-inject icacls failure, assert `SaveConfig` returns an error AND `os.Stat(tmpPath)` returns `IsNotExist`.
  - New `TestSaveConfig_CleansTmpOnRenameFailure` — fault-inject both `MoveFileEx` and `os.Rename` failures (e.g., hold an exclusive handle on the final path in the test), assert `SaveConfig` returns an error AND `os.Stat(tmpPath)` returns `IsNotExist`.
  - Manual: on a VM, run `icacls config.json` after save and confirm only SYSTEM + Administrators.
- **Effort**: M
- **Sequencing**: Independent; can land in parallel with Step 1.

### Step 3 — C-1: AuditPath normalizer preserves directory
- **Files**: `config.go:388-397`.
- **Change**: Replace `c.AuditPath = DefaultDBPath()` in the legacy-suffix branch with `c.AuditPath = filepath.Join(filepath.Dir(c.AuditPath), "drainctl.db")`. Empty-string branch is unchanged.
- **Test/verify**: New table test `TestValidate_AuditPathLegacyJSONLPreservesDirectory` covering: empty → DefaultDBPath; `D:\Custom\audit.jsonl` → `D:\Custom\drainctl.db`; `%ProgramData%\...\audit.jsonl` → DefaultDBPath equivalent; non-legacy path passes through.
- **Effort**: S

### Step 4 — C-4: Pick 90 for `slot_maturity_observations` default (no auto-migration)
- **Files**: `config.go:70` (change `DefaultEvtSpikeSlotMaturityObservations = 7` to `90`). Confirm `internal/evtspike/detector.go:37` `defaultSlotMaturityObservations = 90` stays — this aligns with the detector comment and the new config default.
- **Change**:
  1. Update the constant to `90`.
  2. **(round 2 — revised)** Do NOT auto-rewrite persisted `7` values. Round-2 review surfaced that `7` was the documented supported default in feature 006's data-model (`specs/006-evtspike-detection/data-model.md:17`, `research.md:131`), so a persisted `7` might be the legacy default *or* an intentional operator tuning choice; config files cannot distinguish the two. Silent rewrite would mutate deliberate tuning. Instead: fresh installs get `90`; existing configs keep their persisted value; operators wanting the stricter default explicitly set `slot_maturity_observations: 90` (or delete the key to pick up the new default on the next clamp).
- **Operator-visible behavior change**: Fresh installs now spend longer in TRAINING on each time-of-day slot — HEALTHY state and slot-specific scoring arrive later on the first day of observation. Existing installs are unchanged unless the operator opts in. Document in CHRONICLE.md alongside Step 4 with explicit guidance on how to opt in.
- **Test/verify**:
  - `TestClampEvtSpike_PromotesZeroSlotMaturityToDefault` — already covers zero → default; update the expectation to 90.
  - New `TestClampEvtSpike_PreservesPersistedLegacy7` — config-level test with explicit `SlotMaturityObservations: 7`; assert clamp leaves it at 7. This locks in the "no auto-migration" decision so a future contributor doesn't re-introduce the rewrite.
  - New `TestClampEvtSpike_PreservesNonLegacySlotMaturity` — values of 1, 30, 90, 100 pass through unchanged.
  - `go test ./...` green.
- **Effort**: S
- **Sequencing**: Must land before C-7 README fix (Step 6) so the doc value matches code.

## PR Bundle 2 — Documentation drift (single commit)

### Step 5 — C-6: README `--db` default
- **Files**: `README.md:213`.
- **Change**: Replace `audit.jsonl` with `drainctl.db`; the parent-directory wording stays (it's still accurate as the data dir).
- **Test/verify**: `git diff` review; `just lint` (no Go impact). Manual: run `drainctl check --help` and confirm the help text matches.
- **Effort**: S

### Step 6 — C-7: Evtspike config defaults across all operator-facing docs
- **Files**:
  - `README.md:557-568` — the config table.
  - **(round 2 added)** `docs/guide.html` — the evtspike configuration knobs section I landed in commit `fcdbc9f`. Currently states `slot_maturity_observations` default of `7` (lines ~2054-2067 area). Update to `90`.
  - Grep: `grep -rn "slot_maturity_observations\|cooldown_minutes\|half_life_buckets\|prior_strength\|mean_per_bucket_prior" README.md docs/guide.html specs/` to catch any stragglers before commit.
- **Change**:
  - README: Fix the six wrong defaults to match `config.go` constants — `cooldown_minutes` 15→10; `slot_maturity_observations` row to 90 (post-Step 4); `half_life_buckets` 8640→360; `prior_strength` 1.0→60.0; `mean_per_bucket_prior` 0.5→0.1. Also rewrite the `evtspike.enabled=false` description: subsystem **is** constructed but quiescent (subscriber starts disabled; runtime cost ~zero, but not "never constructed"). Re-derive any narrative numbers from the corrected values.
  - guide.html: Update the `slot_maturity_observations` default citation to `90`. Review the surrounding prose in the "Strict" tuning profile (which already said "set to 90") — that paragraph is now just the default, no longer a tuning step. Simplify accordingly.
- **Test/verify**: Grep each `evtspike.*` key against `config.go` `Default*` constants. Render README and guide.html locally to catch table-pipe / HTML damage. `grep -n "default.*7\b" docs/guide.html` should return no hits referencing the slot-maturity default.
- **Effort**: M
- **Sequencing**: Depends on Step 4 (so 90 is correct in code, not just in docs).

### Step 7 — C-5: spec.md SC-011 update
- **Files**: `specs/009-security-hardening/spec.md:208`.
- **Change**: Rewrite SC-011 to reflect actual state — `handler.go` split (T084-T086) deferred to follow-up F5; current line count 1149 (was 1138). Phrase as "deferred — tracked in F5" rather than reframing the criterion as met.
- **Test/verify**: `wc -l internal/svc/handler.go` to confirm the cited number. Cross-check tasks.md for the F5/T084-T086 reference and link it.
- **Effort**: S

## Explicitly NOT fixing in this plan

- **C-2** (`seedServerMetrics` overwrites fresher SSE samples) — pre-009; logging to `BUGS.md` instead. Not in 009 scope.
- **C-3** (stale `detectorStatuses`/`recentSpikes` after server delete) — pre-009; logging to `BUGS.md`. Not in 009 scope.
- **C-8** (function-pointer test seams) — synthesis labels accept; common Go test idiom, no security impact.
- **B-2** falls away with Step 1 (B-1 dropped) — no separate work item.

## Validation history

**Round 1 (2026-04-24)** — codex raised 4 concerns, all addressed:
1. Step 1 spec drift — plan now rolls back FR-009 / US4 scenarios / quickstart / plan / tasks / README / research/contract spec artifacts in the same commit.
2. Step 2 tmp-file leak on ACL failure — plan now `os.Remove`s the tmp before returning the error.
3. Step 2 verification only covered tmp path — plan now includes `TestSaveConfig_FinalPathACLPostRename` asserting ACL preservation through the rename.
4. Step 4 didn't fix persisted 7 values — plan originally added auto-migration; reversed in round 2.

**Round 3 (2026-04-24)** — codex raised 1 residual concern, addressed:
1. Step 1 `research.md` rollback was "addendum" only; Decision 3's body still asserted the rejected behavior, leaving the spec package internally contradictory. Plan now **rewrites** Decision 3 body rather than annotating.

**Round 2 (2026-04-24)** — codex raised 4 more concerns, all addressed:
1. Step 1 spec-rollback list still missed `data-model.md`, `tasks.md:137-141`, `plan.md:35/65/107`, `checklists/requirements.md:36`; verification grep also had impossible scope (historical review docs retain old decision). Plan now enumerates those files and narrows the grep scope.
2. Step 2 tmp-file cleanup covered only the ACL-failure branch; rename-failure branch also leaks. Plan now extends cleanup to the `MoveFileEx`+`os.Rename` both-fail path with `errors.Join`, plus a dedicated regression test.
3. Step 4 auto-migration (round 1 fix) silently overwrote an operator-chosen `7`; `7` was the documented supported default in feature 006. Reversed: plan now changes only the fresh-install default; existing installs keep their persisted value; guidance in CHRONICLE.
4. Step 6 missed `docs/guide.html` which I just landed with `7` as the cited default. Plan now extends Step 6's file list and verification grep to include guide.html and any spec evtspike-default citations.

## Sequencing summary

1. PR Bundle 1 (Steps 1-4) — Go fixes; Steps 1, 2, 3 parallel; Step 4 must precede Step 6.
2. PR Bundle 2 (Steps 5-7) — single doc commit; depends on Step 4 landing.
3. Separate ticket: `BUGS.md` entries for C-2/C-3.

Total estimated effort: ~1 day (3×S + 3×M).
