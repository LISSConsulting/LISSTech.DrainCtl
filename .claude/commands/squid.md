Push development and merge to trunk via PR.

## Steps

1. `git push origin development`
2. Create a PR from `development` to `trunk`:
   ```
   gh pr create --base trunk --head development --title "<version or summary>" --body "$(git log origin/trunk..origin/development --pretty=format:'- %s' --no-merges)"
   ```
   Use the most recent commit message or version as the PR title.
3. Wait for CI checks to pass: `gh pr checks <PR> --watch`
4. Merge with `--rebase` (preserves original commits, linear history):
   ```
   gh pr merge <PR> --rebase --delete-branch=false
   ```
5. Pull trunk locally: `git fetch origin && git checkout development`
6. If CI fails or merge is blocked: stop and tell the user.
