<!-- SPECKIT START -->
For additional context about technologies to be used, project structure,
shell commands, and other important information, read `specs/015-fleet-session-operator-metrics/plan.md`
<!-- SPECKIT END -->

## Release workflow (summary; full detail in CLAUDE.md)

- Feature/fix branches: push branch, open PR into `develop`, let CI run, merge with a merge commit (never rebase or squash).
- `develop` → `trunk`: release PR with the merge-commit rule. After trunk advances, immediately merge any direct `trunk` hotfix back into `develop` so the two don't diverge.
- Release ceremony runs from `trunk` only: `just release` builds and signs in order — binaries + PS module → MSI → sign MSI → sign release manifest. Version is git-derived CalVer `YY.MM.BUILD`; committing bumps it automatically. The MSI `ProductVersion` `(100+YY).MM.BUILD` is generated separately by `scripts/msi-version.ps1`.
- PRs are the review surface, not the merge vehicle for trunk releases. Releases are cut from `trunk` after the release PR lands.
- Forgejo is a read-only mirror; do not try to push there. GitHub `origin` is the only writable source of truth.
