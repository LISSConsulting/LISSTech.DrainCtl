# Data Model: Remaining Persistent Telemetry Consumers

## 1. OverviewWindowSelection

- **Purpose**: The authoritative dashboard state for the selected Overview time span.
- **Fields**:
  - `preset`: one of `5m`, `1h`, `1d`, `3d`, `5d`
  - `from`: UTC timestamp, inclusive
  - `to`: UTC timestamp, exclusive
  - `source`: `preset` | `wheel` | `drag` | `reset`
- **Validation rules**:
  - `to` MUST be greater than `from`
  - Window MUST stay within supported bounds for the dashboard interaction model
  - On the Overview page this window is shared across all chart families
- **State transitions**:
  - `preset` selection resets `from/to` to the chosen duration ending at "now"
  - `wheel` and `drag` mutate the current window while preserving the shared model
  - `reset` returns to the last chosen preset window

## 2. FleetMetricsQuery

- **Purpose**: Request contract for fleet-retained chart data.
- **Fields**:
  - `host`: always `_fleet` for fleet-wide retained queries
  - `from`: UTC timestamp, inclusive
  - `to`: UTC timestamp, exclusive
  - `resolution`: `auto` | `raw` | `5min` | `hourly`
  - `counters`: zero or more counter identifiers
- **Validation rules**:
  - `host` MUST be `_fleet`
  - `resolution` MUST be one of the supported tier names
  - `from/to` MUST describe a valid bounded time range
  - Unknown counters are rejected or ignored per the final API contract; behavior must be
    consistent across calls

## 3. FleetHistorySeries

- **Purpose**: Durable time-series payload rendered by Overview charts.
- **Fields**:
  - `tier`: concrete served tier, one of `raw`, `5min`, `hourly`
  - `from`, `to`: echo of request bounds
  - `oldest_available`, `newest_available`: retained coverage bounds for the served tier
  - `series`: map of counter name to `CounterSeries`
- **CounterSeries fields**:
  - `t`: parallel array of Unix-ms timestamps
  - `avg`: numeric values used by average-style fleet series
  - `min`: numeric lower envelope
  - `max`: numeric upper envelope or percentile carrier, depending on counter semantics
- **Validation rules**:
  - Arrays in one series MUST be parallel and equal length
  - `series` MAY be empty to represent no retained data for the requested window
  - `tier` MAY differ from requested `resolution` when `auto` or tier degradation applies

## 4. MetricsSeedWindow

- **Purpose**: Small recent retained-history slice used to seed `serverMetrics` consumers
  such as sparklines and `ServerDetail` fallback history on a fresh browser load.
- **Fields**:
  - `from`, `to`: bounded recent raw window
  - `max_points_per_host`: upper bound on returned samples per host
  - `hosts`: registered hostnames included in the response
- **Validation rules**:
  - Window must stay small enough for dashboard cold-start seeding
  - Returned host set is the currently registered fleet snapshot at query time

## 5. HistoricalConsumerReview

- **Purpose**: Planning artifact that records whether a dashboard history surface is
  migrated in this feature.
- **Fields**:
  - `surface_name`: canonical dashboard surface name
  - `current_source`: browser-local, polled snapshot accumulation, or retained telemetry
  - `retained_source_available`: boolean
  - `decision`: `migrate` | `defer`
  - `reason`: short justification
- **Validation rules**:
  - Any dashboard historical surface with `retained_source_available=true` should resolve
    to `migrate` unless a stronger documented constraint blocks it

## 6. ChartAvailabilityState

- **Purpose**: Operator-visible state for one chart family within the shared window.
- **Fields**:
  - `state`: `ready` | `empty` | `unavailable` | `error`
  - `message`: operator-facing explanation
  - `window_from`, `window_to`: current shared selection
- **Validation rules**:
  - `empty` and `error` remain visible states; they do not hide the chart family
  - `error` MUST not trigger browser-local fallback history

## Relationships

- One `OverviewWindowSelection` drives many `ChartAvailabilityState` instances
  simultaneously.
- One `FleetMetricsQuery` returns one `FleetHistorySeries` payload.
- One `MetricsSeedWindow` returns many per-host recent history slices.
- One `HistoricalConsumerReview` row exists for each reviewed dashboard history surface.
