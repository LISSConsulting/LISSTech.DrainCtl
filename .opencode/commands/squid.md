---
description: Push develop and merge it to trunk via PR
---

Push develop and merge to trunk via PR.

## Steps

1. Rebase develop on trunk (required after prior rebase merges):
   ```
   git fetch origin && git rebase origin/trunk
   ```
   If rebase conflicts: stop and tell the user.
2. `git push origin develop --force-with-lease`
3. Create a PR from `develop` to `trunk`:
   ```
   gh pr create --base trunk --head develop --title "<version or summary>" --body "$(git log origin/trunk..origin/develop --pretty=format:'- %s' --no-merges)"
   ```
   Use the most recent commit message or version as the PR title.
3. Wait for CI checks to pass: `gh pr checks <PR> --watch`
4. Merge with `--rebase` (preserves original commits, linear history):
   ```
   gh pr merge <PR> --rebase --delete-branch=false
   ```
5. Sync develop to trunk: `git fetch origin && git rebase origin/trunk`
6. If CI fails or merge is blocked: stop and tell the user.
