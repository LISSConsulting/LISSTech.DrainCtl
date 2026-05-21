---
description: Run the full DrainCtl release protocol
---

Release the Kraken! Execute the full release protocol for LISSTech DrainCtl.

## Protocol

Run each step in order. If any step fails, diagnose and fix before continuing. Do not skip steps.

### 1. Pre-flight
- `git status` — must be clean working tree. If not, ask what to do.
- `git fetch origin` — ensure we have latest remote state.
- Determine the current branch.

### 2. Land feature branch (skip if already on `develop`)
- If on a feature branch (anything other than `develop` or `trunk`):
  - `git rebase origin/develop` — rebase feature work onto latest develop.
  - If rebase conflicts: stop and tell the user.
  - `git checkout develop && git merge --ff-only <feature-branch>` — fast-forward develop to include the feature.
  - `git branch -d <feature-branch>` — delete the feature branch locally.

### 3. Quality gate
- Verify `develop` branch is checked out.
- `just lint` — must pass with zero issues.
- If lint fails: fix the issues, re-run lint, repeat until clean.
- `go build ./...` — must compile.
- `just gotest` — all Go tests must pass.
- `just vulncheck` — no known vulnerabilities.

### 4. Version bump
- `just bump` — bumps CalVer across all version-bearing files + recompiles .syso.

### 5. Build & sign
- `just release` — builds CLI, DLL, PS module, MSI. Signs everything (binaries, MSI).
- If build fails: diagnose, fix, retry.

### 6. Commit & push
- Stage all changed files (exclude obj/, dist/, build artifacts).
- Commit with message: `v{VERSION}: {brief description of what changed since last release}`
- `git push origin develop`

### 7. Merge to trunk via PR
- `git push origin develop`
- Create PR: `gh pr create --base trunk --head develop --title "v{VERSION}" --body "$(git log origin/trunk..origin/develop --pretty=format:'- %s' --no-merges)"`
- Wait for CI: `gh pr checks <PR_NUMBER> --watch`
- If CI fails: diagnose, fix, push again, wait for CI.
- Merge: `gh pr merge <PR_NUMBER> --rebase --delete-branch=false`
- `git fetch origin`

### 8. Publish
- `just publish` — tags, creates GH release with MSI, publishes to PSGallery.
- Verify: "Release created with 1 asset(s)" in output.

### 9. Verify sync
- `git fetch origin`
- Confirm trunk and develop are identical (zero commits in either direction).
- Report the release URL and version.
