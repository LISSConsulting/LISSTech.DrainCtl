# Branch protection verification — v26.10.48 release follow-up

The v26.10.48 release of 015-chart-data-export accidentally deleted
`origin/develop` because `gh pr merge --merge --delete-branch` was
used on a `develop -> trunk` PR. --delete-branch deletes the PR's
SOURCE branch; on a develop -> trunk PR that source is develop
itself.

This file documents what was added locally and what GitHub-side
configuration is still needed.

## Local guards (in this commit)

1. `scripts/protect-shared-branches.ps1` — hardcoded list of branches
   that must exist on origin (`develop`, `trunk`, `main`). Exits 1 if
   any is missing.

2. `justfile release-preflight` now calls
   `protect-shared-branches.ps1` before any release build.

3. `justfile publish` now calls `protect-shared-branches.ps1` before
   any tag push or `gh release create`. Belt-and-suspenders:
   `publish` can be invoked alone, so it carries the guard itself.

These guards catch the bad state ("branch is missing") but do not
catch the bad command ("gh pr merge --delete-branch" on a develop ->
trunk PR). For that, GitHub branch protection must refuse deletes.

## GitHub-side configuration still required

Verify the following on
https://github.com/LISSConsulting/LISSTech.DrainCtl/settings/branches
(requires repo admin):

| Branch   | Allow deletions | Required status checks      |
| -------- | --------------- | ---------------------------- |
| trunk    | NO              | Lint + Test + Vulncheck      |
| develop  | NO              | Lint + Test + Vulncheck (opt) |
| main     | NO              | n/a (no direct PRs)         |

If "Allow deletions" is enabled on `develop` or `trunk`, GitHub's
API will accept `gh pr merge --delete-branch` and silently remove
the source branch on a develop -> trunk PR. The local guards above
will then fail the *next* `just release` / `just publish` because
the protected branch is gone, but the damage is already done.

## Manual PR-merge rule for the future

If you ever invoke `gh pr merge` by hand (the justfile doesn't
currently do this; PR merges in this repo happen via the GitHub
UI's "Squash and merge" or "Rebase and merge" buttons), do not pass
`--delete-branch` for PRs whose source is `develop` or `trunk`.
For a feature-branch PR, `--delete-branch` is the correct flag.

## CHRONICLE entry to add

Append a "develop branch delete — 2026-10-02" entry to CHRONICLE.md
recording:
- The v26.10.48 release accidentally deleted origin/develop via
  `gh pr merge --delete-branch` on a develop -> trunk PR.
- Local guards were added in the same release.
- The GitHub-side protection check (this file) is the only thing
  standing between the next agent/operator and the same mistake.
