# Research: Vite + Svelte Dashboard Migration

**Branch**: `002-vite-svelte-dashboard` | **Date**: 2026-04-09

## R1: Svelte 5 with Runes for Dashboard State Management

**Decision**: Svelte 5 with runes (`$state`, `$derived`, `$effect`)
**Rationale**: The dashboard has ~15 pieces of global mutable state (server list, event log, state history, chart data, config, theme, modal visibility, dirty flags). Svelte 5 runes provide explicit fine-grained reactivity that maps cleanly to these concerns without a separate store library. `$state` replaces the current global variables; `$derived` replaces computed values (counter totals, state bar percentages); `$effect` replaces the `setInterval` polling loop.
**Alternatives considered**:
- Svelte 4 stores: Implicit reactivity harder to trace in a monitoring dashboard with frequent data updates
- External state lib (Zustand, etc.): Unnecessary overhead when Svelte 5 runes cover all needs natively

## R2: Tailwind CSS v4 Theme Integration

**Decision**: Tailwind CSS v4 with CSS-based `@theme` directive
**Rationale**: The existing dashboard defines 17 CSS custom properties for its design system. Tailwind v4's `@theme` directive maps directly to these, eliminating the need for a JS config file. Dark mode overrides use `html[data-theme="dark"]` selectors, preserving the existing localStorage-based toggle. The Oxide engine produces smaller output with automatic purging.
**Alternatives considered**:
- Tailwind v3 with JS config: Requires additional `tailwind.config.js`, separate PostCSS setup, more boilerplate
- Plain CSS with custom properties: Loses utility-class ergonomics and consistent spacing/sizing system

### Theme Variable Mapping

| Existing CSS Var | Tailwind v4 @theme | Light | Dark |
|---|---|---|---|
| `--bg` | `--color-bg` | `#fdf8f6` | `#110a0a` |
| `--fg` | `--color-fg` | `#2d1a1a` | `#f5e8e8` |
| `--accent` | `--color-accent` | `#a3475b` | (same) |
| `--green` | `--color-green` | `#5d8a6e` | (same) |
| `--amber` | `--color-amber` | `#b87843` | (same) |
| `--red` | `--color-red` | `#9e2a3b` | (same) |
| `--border` | `--color-border` | `#2d1a1a` | `#4a3535` |
| `--shadow` | `--color-shadow` | `#2d1a1a` | `#000` |
| `--card` | `--color-card` | `#fffaf8` | `#1f1414` |
| `--muted` | `--color-muted` | `#7a5a5a` | `#c4a8a8` |
| `--subtle` | `--color-subtle` | `#a68e8e` | `#6b5050` |
| `--surface` | `--color-surface` | `#f5ebe8` | `#1f1414` |
| `--code-bg` | `--color-code-bg` | `#1a0e0e` | `#0d0808` |
| `--code-fg` | `--color-code-fg` | `#f0e0e0` | (same) |
| `--r` | `--radius-default` | `10px` | (same) |
| `--bw` | `--spacing-bw` | `2.5px` | (same) |
| `--so` | `--spacing-so` | `4px` | (same) |

## R3: Vite Build → Go Embed Strategy

**Decision**: `//go:embed all:dist` on a `dist/` directory within `internal/dashboard/`
**Rationale**: The current embed uses `//go:embed dashboard.html` for a single file. The migration replaces this with a directory embed of Vite's build output (index.html + hashed JS/CSS assets). The `handleUI` handler changes from serving raw bytes to serving `index.html` from the embedded filesystem, while static assets get served via `http.FileServer` with long cache headers (hashed filenames enable aggressive caching).
**Alternatives considered**:
- Symlink from `frontend/dist` to `internal/dashboard/dist`: Fragile on Windows, complicates CI
- Copy build output in justfile recipe: Simple, reliable, cross-platform — chosen approach

### Build Pipeline

```
frontend/npm run build → frontend/dist/ → copy → internal/dashboard/dist/ → go build (embeds)
```

The `justfile` gains a `frontend` recipe that runs before the existing `cli` recipe.

## R4: CSP Tightening

**Decision**: Remove `'unsafe-inline'` from `script-src` and `style-src`; use `'self'` instead
**Rationale**: With Vite, all JS and CSS are external hashed files (`app-[hash].js`, `app-[hash].css`). No inline `<script>` or `<style>` blocks needed. The one exception is the theme flash-prevention snippet, which will use a nonce-based approach: the Go handler generates a random nonce per request, injects it into the `<script nonce="...">` tag, and includes `'nonce-{value}'` in the CSP header.
**Alternatives considered**:
- Keep `'unsafe-inline'`: Works but misses the security improvement opportunity
- Hash-based CSP (`'sha256-...'`): Requires updating the hash whenever the snippet changes; nonce is more maintainable

### Updated CSP

```
default-src 'none';
script-src 'self' 'nonce-{random}';
style-src 'self' https://fonts.googleapis.com;
font-src https://fonts.gstatic.com;
img-src 'self' data:;
connect-src 'self';
frame-ancestors 'self';
base-uri 'self';
form-action 'self'
```

## R5: uPlot Integration as npm Dependency

**Decision**: Install `uplot` via npm, import in Svelte component
**Rationale**: uPlot v1.6.32 is currently inlined (~3,200 minified lines, ~50KB). As an npm dependency, Vite tree-shakes and bundles it properly. The chart wrapper becomes a Svelte component with `$effect` for reactive data binding and `onMount`/`onDestroy` for lifecycle management.
**Alternatives considered**:
- CDN import: Adds external dependency, breaks offline/air-gapped deployments
- Keep inline: Defeats the purpose of the migration to a module-based build system

## R6: Component Decomposition Strategy

**Decision**: Decompose the 6,732-line monolithic HTML into ~15 focused Svelte components
**Rationale**: The existing dashboard has clear visual/logical boundaries that map to components. Each component owns its state slice, API calls, and rendering. The component tree keeps the hierarchy flat (max 3 levels deep) to avoid prop-drilling complexity.

### Component Tree

```
App.svelte
├── Nav.svelte                    (brand, server summary, config toggle, theme toggle)
├── CounterGrid.svelte            (5 counter cards)
├── StateBar.svelte               (proportional state segments)
├── StateChart.svelte             (uPlot area chart + CPU/InputDelay overlays)
├── EventLog.svelte               (filterable, expandable log)
├── ServerTable.svelte            (sortable table with expandable rows)
│   └── ServerDetail.svelte       (accordion content: 3 tiles)
│       ├── RingGauge.svelte      (reusable SVG ring with threshold colors)
│       └── Sparkline.svelte      (mini uPlot chart)
├── ConfigModal.svelte            (settings, thresholds, perf toggles)
│   ├── NotificationTargets.svelte (targets table within config modal)
│   ├── TargetEditModal.svelte    (add/edit target, layered above config)
│   └── TargetDeleteModal.svelte  (delete confirmation)
├── HistoryModal.svelte           (server transition timeline)
└── Footer.svelte                 (connection status, last updated)
```

## R7: Frontend Directory Location

**Decision**: `frontend/` directory at repository root
**Rationale**: Keeps frontend tooling (node_modules, package.json, vite.config) separate from the Go module. The build output is copied to `internal/dashboard/dist/` for embedding. This avoids polluting the Go module with npm artifacts and keeps `go mod tidy` clean.
**Alternatives considered**:
- `internal/dashboard/frontend/`: Nests too deep, couples frontend source to Go package structure
- `web/` or `ui/`: Less descriptive than `frontend/`
