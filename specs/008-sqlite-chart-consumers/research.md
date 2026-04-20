# Research: Remaining Persistent Telemetry Consumers

## Decision 1: Use a fleet sentinel on the existing metrics route

- **Decision**: Extend `GET /api/v1/metrics/{host}` to accept `_fleet` as a special host
  value, returning the same `MetricsResponse` shape already used by the per-host durable
  chart.
- **Rationale**: The codebase already has a durable metrics route, tier-selection logic,
  and frontend `fetchMetrics()` client with `MetricsResponse` typing. Reusing that
  contract keeps the backend and frontend aligned and lets Overview charts adopt retained
  history with minimal new concepts.
- **Alternatives considered**:
  - Create a dedicated `/api/v1/metrics/_fleet` endpoint: rejected because the existing
    path parameter already models the query and a `_fleet` sentinel matches the feature
    note in `FEATURES.md`.
  - Keep client-side synthesis from polled host snapshots: rejected because it leaves the
    browser as the source of truth and fails the feature goal.

## Decision 2: Preserve the current Overview aggregation semantics exactly

- **Decision**: Keep today's fleet rollup meaning unchanged: LOAD remains fleet averages
  plus CPU P95, HIC remains percentile-based fleet indicators, SESSIONS remains the
  current fleet session rollup, and REMOTE FX remains the current fleet percentile view.
- **Rationale**: The clarification phase locked this in, and operator familiarity depends
  on preserving the semantic meaning of each chart rather than merely keeping the colors
  and layout.
- **Alternatives considered**:
  - Convert all Overview charts to averages only: rejected because it would silently
    change operator meaning for HIC and REMOTE FX.
  - Convert all Overview charts to percentile-only: rejected because it would change the
    LOAD chart's established behavior.

## Decision 3: One shared Overview time window controls all chart families

- **Decision**: Centralize Overview time-window state so 5M/1H/1D/3D/5D selection,
  wheel zoom, drag pan, and reset apply to LOAD, HIC, SESSIONS, and REMOTE FX together.
- **Rationale**: Shared comparison across chart families is the primary operator value.
  Independent windows would make cross-signal diagnosis harder and add avoidable UI and
  state complexity.
- **Alternatives considered**:
  - Independent per-chart windows: rejected because it weakens side-by-side diagnosis.
  - Shared by default with per-chart detach: rejected because it broadens scope and task
    count without a matching spec requirement.

## Decision 4: Empty and error states remain visible, never hidden or replaced by local fallback

- **Decision**: A chart with no retained data for the shared window stays visible with an
  explicit empty or unavailable state. A query failure stays visible with an explicit
  error state. In neither case may the dashboard fall back to browser-local history.
- **Rationale**: The feature's core promise is that retained telemetry is authoritative.
  Hiding charts or silently substituting browser-local data would mislead operators.
- **Alternatives considered**:
  - Hide chart families with no data: rejected because it makes cross-chart comparison
    inconsistent and hides useful operational context.
  - Fallback to localStorage on errors: rejected because it can show stale, machine-local
    data as if it were authoritative history.

## Decision 5: Migrate additional in-scope historical consumers by formalizing a production sparkline seed route

- **Decision**: Treat `serverMetrics` consumers as in-scope and formalize a production
  `GET /api/v1/metrics` seed endpoint that returns a bounded recent raw-window history per
  registered host for dashboard sparklines and `ServerDetail` fallback history.
- **Rationale**: `ServerTable.svelte` and `ServerDetail.svelte` still depend on
  `appState.serverMetrics`, which today is accumulated from polling and localStorage and is
  only seeded by a dev-only `/metrics` route. They qualify as dashboard historical
  surfaces that can already be backed by existing retained telemetry without inventing a
  new telemetry class.
- **Alternatives considered**:
  - Leave `serverMetrics` browser-local: rejected because it fails FR-012 once the review
    finds an existing retained source is available.
  - Issue one `GET /api/v1/metrics/{host}` request per visible host on cold start:
    rejected because it multiplies network requests and complicates table/server-detail
    bootstrapping.

## Decision 6: Avoid telemetry schema changes unless implementation proves an existing counter gap

- **Decision**: Plan assuming all requested Overview families can be derived from the
  existing retained telemetry counters and tiers. If implementation proves that a needed
  fleet rollup cannot be computed from currently persisted counters, record the gap and
  add the smallest schema change required in implementation or a follow-up.
- **Rationale**: The current telemetry store already retains per-host metrics, 5-minute
  aggregates, and hourly aggregates. No evidence from the codebase review requires a new
  table or retention class up front.
- **Alternatives considered**:
  - Preemptively add new fleet aggregate tables: rejected because it adds storage and
    maintenance complexity before proving a need.
  - Prohibit any schema change regardless of findings: rejected because correctness is
    more important than a blanket no-change rule.
