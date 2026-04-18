---
description: Run the full DrainCtl release protocol
---

Release the Kraken. Execute the full release protocol for LISSTech DrainCtl.

Run each step in order. If any step fails, diagnose and fix before continuing. Do not skip steps.

## 1. Pre-flight

- `git status` must show a clean working tree. If not, ask the user what to do.
- `git fetch origin` to refresh remote state.
- Determine the current branch.

## 2. Land the feature branch into develop

Skip this section if already on `develop`.

- If on a feature branch:
  - `git rebase origin/develop`
  - If rebase conflicts, stop and tell the user.
  - `git checkout develop`
  - `git merge --ff-only <feature-branch>`
  - `git branch -d <feature-branch>`

## 3. Quality gate

- Verify `develop` is checked out.
- Run `just lint` and fix any issues until it passes cleanly.
- Run `go build ./...` and ensure it compiles.
- Run `just gotest` and ensure all Go tests pass.
- Run `just vulncheck` and ensure no known vulnerabilities are reported.

## 4. Build and sign

- Version is git-derived. Do not edit or bump version strings by hand.
- Use `just version` to inspect the version that the release build will embed.
- Run `just release` to build and sign the CLI, DLL, PowerShell module, and MSI.
- If release build fails, diagnose, fix, and retry.

## 5. Commit and push develop

- If the release workflow modified tracked files, stage all intended changes, excluding transient build artifacts.
- Commit with a release-style message using the derived version, for example:
  - `release: v{VERSION}`
  - or `release: v{VERSION} <brief summary>`
- Push `develop`:
  ```
  git push origin develop
  ```

## 6. Merge develop to trunk via PR

- Create a PR:
  ```
  gh pr create --base trunk --head develop --title "v{VERSION}" --body "$(git log origin/trunk..origin/develop --pretty=format:'- %s' --no-merges)"
  ```
- Wait for CI:
  ```
  gh pr checks <PR_NUMBER> --watch
  ```
- If CI fails, diagnose, fix, push again, and wait again.
- Merge with rebase:
  ```
  gh pr merge <PR_NUMBER> --rebase --delete-branch=false
  ```
- `git fetch origin`

## 7. Publish

- Run `just publish` to tag, create the GitHub release, and publish to PSGallery.
- Verify the publish output indicates the release was created successfully.

## 8. Verify sync

- `git fetch origin`
- Confirm `trunk` and `develop` are identical, with zero commits in either direction.
- Report the release version and release URL to the user.
