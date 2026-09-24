# Implementation Plan: Chart Library Migration

**Branch**: `011-chart-library-migration` | **Date**: 2026-09-23
**Spec**: [spec.md](./spec.md)

## Summary

Replace layercake with LayerChart 2.x across all interactive chart components, and
uPlot for the Sparkline only. Centralize the chart data adapters and zoom-state math
into pure modules under `frontend/src/lib/chart/` so they can be unit-tested without
mounting Svelte. Deliver as a single PR with per-chart-type commits.

## Technical context

**Frontend**: Svelte 5.57.1 + Vite 8.3 + pnpm 12.6.0 (current after the dep refresh).
**Target libraries**:
- LayerChart 2.5.0 — primary interactive charting (`npm i layerchart`)
- uPlot ~1.6.x — Sparkline (`npm i uplot`)
**Build impact**: Bundle size currently ships ~5 kB layercake + custom chart code. Post-
migration expected ~30 kB LayerChart (tree-shaken) + 30 kB uPlot (Sparkline only) +
~3 kB new chart primitives we keep. Total budget: ≤ 65 kB gzipped for the entire
chart layer (currently estimated at ~22 kB gzipped).
**Testing**: Vitest 2.x + @testing-library/svelte 5.x for component-level sanity;
pure-function Vitest for adapters and zoom math.
**Files in scope** (8 files, 4,277 LOC):

```
frontend/src/lib/chart.svelte                                541 LOC  → rewrite
frontend/src/components/HostLoadChart.svelte                 675 LOC  → minimal edit
frontend/src/components/MetricsChart.svelte                 1565 LOC  → minimal edit
frontend/src/components/Sparkline.svelte                      33 LOC  → rewrite (uPlot)
frontend/src/components/chart/DualAxisChart.svelte           415 LOC  → delete (use LineChart)
frontend/src/components/chart/InteractiveTimeChart.svelte    290 LOC  → delete (use LineChart)
frontend/src/components/chart/LinePath.svelte                 35 LOC  → delete (use Spline)
frontend/src/components/chart/MiniHealthChart.svelte         723 LOC  → rewrite (use Area)
```

Plus new files:

```
frontend/src/lib/chart/adapters.js              ~250 LOC   pure data adapters
frontend/src/lib/chart/adapters.test.js          ~150 LOC   Vitest
frontend/src/lib/chart/zoom.js                   ~120 LOC   zoom-state + debounce
frontend/src/lib/chart/zoom.test.js               ~80 LOC   Vitest
frontend/src/lib/chart/theme.js                   ~60 LOC   design-token → d3-scale mapping
frontend/src/lib/chart/CpuMemLineChart.svelte    ~120 LOC   shared per-host time-series primitive
frontend/src/lib/chart/FleetLoadChart.svelte     ~200 LOC   shared dual-axis primitive
frontend/src/lib/chart/HealthChart.svelte        ~150 LOC   shared mini health primitive
```

Total new LOC: ~1,130. Total deleted LOC: ~3,950 (4,277 − small edits to HostLoadChart /
MetricsChart). Net reduction: ~2,820 LOC across the chart layer.

## Constitution check

*GATE: must pass before Phase 0 research. Re-check after Phase 1 design.*

- **Windows-first delivery**: PASS. No backend changes; embedded dashboard only.
- **Stable operator surfaces**: PASS. Visual diffs are limited to the "improve where it
  costs nothing" list in spec.md. All chart interactions preserved.
- **Tests and zero-noise verification**: PASS. Vitest unit tests for adapters and zoom
  math; CI runs them on every PR.
- **Config and release discipline**: PASS. No new config; release tagging flow unchanged.
- **Operational observability**: PASS. Charts already log fetch errors to console;
  no change.

**Gate result**: pass.

## Project structure

```
frontend/
├── src/
│   ├── App.svelte
│   ├── lib/
│   │   ├── chart.svelte                       ← rewrite (uses CpuMemLineChart)
│   │   ├── chart/
│   │   │   ├── adapters.js                    ← NEW (pure)
│   │   │   ├── adapters.test.js               ← NEW (Vitest)
│   │   │   ├── zoom.js                        ← NEW (pure)
│   │   │   ├── zoom.test.js                   ← NEW (Vitest)
│   │   │   ├── theme.js                       ← NEW (pure)
│   │   │   ├── CpuMemLineChart.svelte         ← NEW (per-host time-series primitive)
│   │   │   ├── FleetLoadChart.svelte          ← NEW (dual-axis primitive)
│   │   │   └── HealthChart.svelte             ← NEW (mini health primitive)
│   │   └── ...
│   └── components/
│       ├── HostLoadChart.svelte               ← edit: replace LayerCake with FleetLoadChart
│       ├── MetricsChart.svelte                ← edit: replace LayerCake with chart primitives
│       ├── Sparkline.svelte                   ← rewrite: uPlot
│       └── chart/
│           ├── DualAxisChart.svelte           ← DELETE
│           ├── InteractiveTimeChart.svelte    ← DELETE
│           ├── LinePath.svelte                ← DELETE
│           └── MiniHealthChart.svelte         ← DELETE
├── vitest.config.ts                           ← NEW
├── package.json                               ← add layerchart, uplot, vitest, @testing-library/svelte
```

## Migration phases (commits within the single PR)

Each commit leaves the dashboard in a working state. Reviewers can stop after any
commit and the build is green.

| # | Commit | Files touched | What it does |
|---|--------|---------------|--------------|
| 1 | `chore(frontend): add layerchart 2.5.0 and uPlot dependencies` | package.json, pnpm-lock.yaml | Add deps; layercake stays. No source change. |
| 2 | `refactor(frontend): extract chart adapters into pure module` | lib/state.svelte.js, lib/chart/adapters.js (new), MetricsChart.svelte, lib/chart/adapters.test.js (new) | Extract `adaptFleetToRfxSamples`, `tsMap`, lcData builders into `lib/chart/adapters.js`. Cover with Vitest. |
| 3 | `refactor(frontend): extract chart zoom math into pure module` | lib/chart.svelte, lib/chart/zoom.js (new), lib/chart/zoom.test.js (new) | Extract debounced re-fetch sequencing, in-flight cancellation, preset→window math. Cover with Vitest. |
| 4 | `refactor(frontend): extract chart theme tokens` | lib/chart/theme.js (new), lib/chart.svelte, MetricsChart.svelte, HostLoadChart.svelte | Centralize `--color-accent`, threshold opacity, font sizes as design-token → CSS-var exports. |
| 5 | `feat(frontend): add CpuMemLineChart primitive` | lib/chart/CpuMemLineChart.svelte (new), lib/chart.svelte | Replace `<LayerCake>` + `<Svg>` + `<InteractiveTimeChart>` in `lib/chart.svelte` with a LayerChart-based primitive. Verify visually. |
| 6 | `feat(frontend): add FleetLoadChart primitive` | lib/chart/FleetLoadChart.svelte (new), HostLoadChart.svelte | Replace DualAxisChart + LayerCake in `HostLoadChart.svelte`. |
| 7 | `feat(frontend): add HealthChart primitive` | lib/chart/HealthChart.svelte (new), MetricsChart.svelte | Replace MiniHealthChart + LayerCake in `MetricsChart.svelte`. |
| 8 | `refactor(frontend): replace Sparkline with uPlot` | components/Sparkline.svelte | Drop LayerCake dependency from the ServerTable sparkline cell. |
| 9 | `chore(frontend): remove layercake and dead chart subcomponents` | package.json, pnpm-lock.yaml, components/chart/{DualAxisChart,InteractiveTimeChart,LinePath,MiniHealthChart}.svelte (delete) | Remove layercake dep; delete dead subcomponents. |
| 10 | `ci(frontend): run vitest on every PR` | .github/workflows/ci.yml | Add `pnpm vitest run` to the CI check job. |

Each commit is reviewed as a unit. The reviewer can request changes at any phase
without blocking the rest.

## Risk register

| Risk | Likelihood | Mitigation |
|------|-----------|------------|
| LayerChart API surface doesn't cover one of our chart interactions | Medium | LayerChart 2.5.0 ships Highlight (crosshair), AnnotationLine (thresholds), Brush (zoom), Pan/Zoom action components. Spikes in PR #1 will confirm coverage before committing. |
| Sparkline uPlot integration breaks ServerTable rendering | Low | uPlot has ~10 years of stable API; ServerTable cells are simple — 1 hour of swap-and-verify work. |
| Vitest + Svelte 5 + runes setup has rough edges | Medium | Pin Vitest 2.x and `@testing-library/svelte` 5.x with runes support landed in 5.2. Verify with a spike before commit 2. |
| Bundle size grows beyond budget | Low | Tree-shake LayerChart; import only Chart, Axis, Svg, Spline, Area, Highlight, AnnotationLine per the LayerChart docs. Measure at commit 1 + after each chart commit. |
| Visual diff exceeds the "improve where it costs nothing" budget | Medium | Manual screenshot comparison against the v26.9.12 baseline screenshots in `docs/`. If any single chart exceeds the diff budget, that commit is held back and revisited. |
| Developer misses a LayerCake import | Low | `pnpm exec grep -r "layercake" frontend/src` is part of the verification checklist before commit 9. |

## Verification

Each phase commit:

1. `pnpm build` succeeds.
2. The dashboard renders in `pnpm dev` without console errors.
3. Manual smoke test of the chart family touched by the commit.

Final PR verification:

1. `pnpm vitest run` — all tests pass.
2. `pnpm build` — production bundle built.
3. Visual diff vs `docs/dashboard-screenshot*.png` baseline — within the documented
   "improve where it costs nothing" budget.
4. `pnpm exec grep -r "layercake" frontend/src` returns nothing.
5. `pnpm exec grep -r "LayerCake" frontend/src` returns nothing.
6. `go build ./cmd/drainctl` succeeds (the embedded dashboard is what the binary
   serves, so this verifies the embed copy).
7. CI green on `lint + test + vulncheck + vitest`.

## Open issues

None. The full plan is committed here; implementation begins on branch creation.
