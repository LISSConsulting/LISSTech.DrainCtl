Release the Kraken! Execute the full release protocol for LISSTech DrainCtl.

## Protocol

Run each step in order. If any step fails, diagnose and fix before continuing. Do not skip steps.

### 1. Pre-flight
- `git status` — must be clean working tree. If not, ask what to do.
- `git fetch origin` — ensure we have latest remote state.
- Verify `development` branch is checked out.

### 2. Quality gate
- `just lint` — must pass with zero issues.
- If lint fails: fix the issues, re-run lint, repeat until clean.
- `go build ./...` — must compile.

### 3. Version bump
- `just bump` — bumps CalVer across all version-bearing files + recompiles .syso.

### 4. Build & sign
- `just release` — builds CLI, DLL, PS module, MSI, Bundle. Signs everything (binaries, MSI, Bundle with detach/reattach).
- If build fails: diagnose, fix, retry.

### 5. Commit & push
- Stage all changed files (exclude obj/, dist/, build artifacts).
- Commit with message: `v{VERSION}: {brief description of what changed since last release}`
- `git push origin development`

### 6. Fast-forward trunk
- `git checkout trunk && git merge --ff-only origin/development && git push origin trunk && git checkout development`
- If FF fails: ask the user — trunk has diverged.

### 7. Publish
- `just publish` — tags, creates GH release with MSI + Bundle EXE, publishes to PSGallery.
- Verify: "Release created with 2 asset(s)" in output.

### 8. Verify sync
- `git fetch origin`
- Confirm trunk and development are identical (zero commits in either direction).
- Report the release URL and version.
