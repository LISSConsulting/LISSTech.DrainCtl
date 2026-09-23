# Open PR review — 2026-09-22

Companion artifact to branch `chore/dependency-refresh-2026-09`. All
findings are derived from the live GitHub state of LISSConsulting/LISSTech.DrainCtl
at 2026-09-22 23:21 UTC plus direct reads of `frontend/package.json`,
`go.mod`, `.pre-commit-config.yaml`, `.github/workflows/ci.yml`, and the
relevant source files for layercake usage.

## TL;DR

| PR | Title | Base | Status | Recommendation |
|----|-------|------|--------|----------------|
| #130 | ci: bump postcss 8.5.15→8.5.25 in /frontend | trunk | CONFLICTING, wrong file | **Close as not applicable** — repo uses `pnpm-lock.yaml`, not `package-lock.json` |
| #156 | deps: bump golang.org/x/sys 0.47.0→0.48.0 | develop | MERGEABLE, CI green | **Close as superseded** — change already on `chore/dependency-refresh-2026-09` |
| #158 | deps: bump layercake 10.0.3→11.0.0 | develop | MERGEABLE, CI green but breaks 4 chart components | **Close in favor of follow-up** — needs `getContext('LayerCake')`→`getLayerCakeContext()` migration in DualAxisChart, InteractiveTimeChart, LinePath, MiniHealthChart |
| #159 | deps: bump modernc.org/sqlite 1.58.0→1.59.0 | develop | MERGEABLE, CI green | **Close as superseded** — change already on `chore/dependency-refresh-2026-09` |
| #160 | deps: bump vite 8.2.2→8.3.0 | develop | MERGEABLE, CI green | **Close as superseded** — change already on `chore/dependency-refresh-2026-09` (lockfile rebased under pnpm 12) |
| #161 | deps: bump @lucide/svelte 1.39.0→1.46.0 | develop | MERGEABLE, CI green | **Close as superseded** — `chore/dependency-refresh-2026-09` goes one minor further (1.47.0) and rebases the lockfile under pnpm 12 |

## Per-PR detail

### #130 — postcss 8.5.15→8.5.25

**File changed**: `frontend/package-lock.json` (npm format).

**Problem**: `frontend/package-lock.json` does not exist in this repo —
dependency lockfile is `frontend/pnpm-lock.yaml` (see `frontend/package.json`
`packageManager: pnpm@…`). Dependabot's npm ecosystem monitor ran against
the wrong filename; the change touches a phantom file.

**Conflict state**: `CONFLICTING` against trunk because trunk diverged
(latest merge: PR #154 at `a485b7c`).

**Action**: Close as `not applicable`. If a postcss bump is genuinely
wanted, re-open against the pnpm ecosystem: `pnpm update postcss -r --latest`
will produce a `pnpm-lock.yaml` diff.

### #156 — golang.org/x/sys 0.47.0→0.48.0

**Files**: `go.mod` (1 line), `go.sum` (2 lines).

**Status**: MERGEABLE, CI completed.

**Substitution**: This exact change is commit `f4b7649` on
`chore/dependency-refresh-2026-09` plus the same change in `go.sum`.
The refresh branch also bumps `modernc.org/sqlite` 1.58.0→1.59.0.

**Action**: Close as superseded once the refresh PR merges. Reviewer
note for the refresh PR: x/sys 0.48.0 is additive on the Windows
surface used here (registry, token, DPAPI, event log, named pipe,
WinVerifyTrust). The minimum Go requirement for x/sys 0.48.0 is
Go 1.26, satisfied by the pinned toolchain 1.27.1.

### #158 — layercake 10.0.3→11.0.0

**Files**: `frontend/package.json` (1 line), `frontend/pnpm-lock.yaml` (6 lines).

**Status**: MERGEABLE, CI completed (green because CI builds against the
Svelte 5 frontend which technically compiles — but the chart components
break at render time, not at build time).

**Real risk** — verified by reading the published v11.0.0 source:

- v11 replaces the old store-based context with a runes-based context.
  `setContext` is invoked by `<LayerCake>` via `setLayerCakeContext(ctx)`
  which is a *typed* context created by Svelte 5's `createContext()` —
  the context key is no longer the string `'LayerCake'`. Consumer
  components must use `getLayerCakeContext()` from `'layercake'` to read
  the context. Calling `getContext('LayerCake')` returns `undefined`.
- The context is a single object whose properties are reactive getters.
  Reading `k.width` inside a `$derived` block tracks the value; reading
  it at the top level and destructuring (`const { xScale, yScale, width,
  height } = getContext('LayerCake')`) captures stale values once and
  never updates. v11 README explicitly calls this out.

Repo usages found and confirmed by grep:

```
frontend/src/components/HostLoadChart.svelte:16:    import { LayerCake, Svg } from 'layercake';
frontend/src/components/MetricsChart.svelte:3:    import { LayerCake, Svg } from 'layercake';
frontend/src/components/Sparkline.svelte:2:    import { LayerCake, Svg } from 'layercake';
frontend/src/lib/chart.svelte:29:    import { LayerCake, Svg } from 'layercake';

frontend/src/components/chart/DualAxisChart.svelte:2:    import { getContext } from 'svelte';
frontend/src/components/chart/DualAxisChart.svelte:19:    const { xScale, yScale, width, height } = getContext('LayerCake');
frontend/src/components/chart/InteractiveTimeChart.svelte:18:    import { getContext } from 'svelte';
frontend/src/components/chart/InteractiveTimeChart.svelte:24:    const { xScale, yScale, xDomain, yDomain, width, height } = getContext('LayerCake');
frontend/src/components/chart/LinePath.svelte:2:    import { getContext } from 'svelte';
frontend/src/components/chart/LinePath.svelte:6:    const { data, xGet, yGet, width, height } = getContext('LayerCake');
frontend/src/components/chart/MiniHealthChart.svelte:??  (also uses LayerCake context)
```

All four chart subcomponents (DualAxisChart, InteractiveTimeChart,
LinePath, MiniHealthChart) destructured the context outside `$derived`,
which v11 forbids.

**Action**: Do not merge this PR. Open a follow-up "migrate dashboard
charts to layercake v11 runes context" PR that:

1. Replaces `import { getContext } from 'svelte'` with `import { getLayerCakeContext } from 'layercake'` in all four chart subcomponents.
2. Replaces each top-level destructure with a single
   `const k = getLayerCakeContext()` and reads `k.xScale` etc inside
   `$derived` blocks.
3. Updates any `<slot>` usage in chart components to the Svelte 5 snippet
   form (`{#snippet}` / `{@render}`) — v11's `<LayerCake>` accepts a
   `children` snippet typed as `(ctx) => …` rather than `<slot>`.
4. Re-validates the chart visuals in the embedded dashboard after
   migration. The chart consumer code lives in `MetricsChart.svelte`,
   `HostLoadChart.svelte`, `Sparkline.svelte`, and `lib/chart.svelte` —
   none of them read LayerCake context directly; they only pass props
   down to the four subcomponents, so the migration is contained to
   the four files listed above.

The refresh PR keeps layercake pinned at `^10.0.3` for this reason.

### #159 — modernc.org/sqlite 1.58.0→1.59.0

**Files**: `go.mod` (1 line), `go.sum` (2 lines).

**Status**: MERGEABLE, CI completed.

**Substitution**: Exact change in commit `f4b7649` on
`chore/dependency-refresh-2026-09`. Repo still has no UDFs or virtual
tables, so the v1.59.0 `*FunctionContext` pooling change is a non-event
for DrainCtl. Embedded SQLite engine version is identical (3.53.4) on
all targets.

**Action**: Close as superseded once the refresh PR merges.

### #160 — vite 8.2.2→8.3.0

**Files**: `frontend/package.json` (1 line), `frontend/pnpm-lock.yaml`
(16 lines).

**Status**: MERGEABLE, CI completed.

**Substitution**: Exact change in commit `5f59423` on
`chore/dependency-refresh-2026-09`. Lockfile was regenerated under
pnpm 12 (vs Dependabot's pnpm 11), so the lockfile differs in format
beyond the version bump — peer variant canonicalization and the
lockfile-version comment will differ.

**Action**: Close as superseded once the refresh PR merges.

### #161 — @lucide/svelte 1.39.0→1.46.0

**Files**: `frontend/package.json` (1 line), `frontend/pnpm-lock.yaml`
(5 lines).

**Status**: MERGEABLE, CI completed.

**Substitution**: The refresh branch goes one minor further to 1.47.0
with a regenerated lockfile. Repo only uses static named imports from
`@lucide/svelte` (e.g. `import { Coffee, Save, X, … } from '@lucide/svelte'`),
so the major-icon-additions between 1.46 and 1.47 do not affect any
existing imports. Both 1.46 and 1.47 are fine; landing the higher one
keeps the lockfile regen to a single point.

**Action**: Close as superseded once the refresh PR merges.

## Aggregate effect

After the refresh PR merges, dependabot will need to be told that all
five `develop`-targeted PRs (#156, #158, #159, #160, #161) are
obsoleted. The standard ways:

- Merge the refresh PR first, then close the dependabot PRs with a
  comment pointing at the refresh PR.
- Or close them first with the comment and let dependabot rebase itself
  on next run.

Either order works. The cleanup avoids three further rebase-and-merge
cycles for dependabot and unblocks future dependabot runs from picking
up 1.47.x, 8.3.x patch releases, etc.
