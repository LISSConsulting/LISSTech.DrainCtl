---
description: Push develop and merge it to trunk via PR
---

Push `develop` and merge it to `trunk` through a GitHub PR.

Execute these steps in order. If any step fails or hits conflicts, stop and tell the user.

## Steps

1. Confirm the current branch is `develop`. If it is not, stop and tell the user.
2. Rebase `develop` on `origin/trunk`:
   ```
   git fetch origin
   git rebase origin/trunk
   ```
3. Push `develop` with lease protection:
   ```
   git push origin develop --force-with-lease
   ```
4. Create a PR from `develop` to `trunk`:
   ```
   gh pr create --base trunk --head develop --title "<version or summary>" --body "$(git log origin/trunk..origin/develop --pretty=format:'- %s' --no-merges)"
   ```
   Use the most recent commit message or release version as the PR title.
5. Wait for CI to finish:
   ```
   gh pr checks <PR_NUMBER> --watch
   ```
6. If checks pass, merge with `--rebase` to preserve the original commits in a linear history:
   ```
   gh pr merge <PR_NUMBER> --rebase --delete-branch=false
   ```
7. Sync local `develop` back to `trunk`:
   ```
   git fetch origin
   git rebase origin/trunk
   ```
8. If CI fails or GitHub blocks the merge, stop and tell the user.
