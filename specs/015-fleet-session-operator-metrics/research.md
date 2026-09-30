# Research: Fleet Session Operator Metrics

## Decision 1: Derive aggregates during accepted snapshot ingestion

**Decision**: Calculate anonymous workload aggregates from the original validated snapshot inside `SessionSnapshotStore.Apply`, before privacy projection, and persist them in the same SQLite transaction as current-session replacement.

**Rationale**: Identity masking must not alter workload eligibility, and one transaction prevents a current snapshot from advancing without its anonymous coverage row. The aggregate contains no identity and survives independent current-snapshot privacy/retention purges.

**Alternatives rejected**:
- Recompute from `session_latest`: privacy projection can remove fields and only retains current state.
- Append generic heartbeat counters: host P95/averages cannot be merged into true fleet P95.
- A second asynchronous ingest pipeline: creates consistency and retry ambiguity.

## Decision 2: Use server receipt time and replaceable one-minute host buckets

**Decision**: Base buckets are UTC one-minute buckets based on dashboard receipt time. `(host, bucket_ts)` is unique and accepted attempts use `ON CONFLICT ... DO UPDATE`.

**Rationale**: This aligns hosts without trusting agent clocks, prevents high-frequency reporters from receiving more weight, and lets the newest accepted attempt remove an earlier successful contribution when collection subsequently fails.

**Alternatives rejected**:
- Agent observation time: exposes fleet points to clock skew.
- Append-only snapshots: overweights hosts and cannot satisfy newest-wins.
- Configurable bucket size: unnecessary operator surface and historical ambiguity.

## Decision 3: Deterministic bounded histograms

**Decision**: CPU uses 201 half-percentage-point bins across `0..100`; memory uses a zero bucket plus logarithmic positive-byte bins whose representative spacing is ≤1.02. Both serialize counts only with versioned binary codecs.

**Rationale**: Histograms merge associatively across hosts and tiers, contain no raw session values, and have explicit deterministic error bounds. CPU nearest-bin error is ≤0.25 point. A memory value and its bin representative differ by <2%.

**Alternatives rejected**:
- Exact sorted values: violates the prohibition on raw per-session historical values and grows poorly.
- t-digest/DDSketch dependency: broader dependency surface and less transparent persisted compatibility.
- Host percentile rows: mathematically cannot reconstruct pooled fleet percentiles.

## Decision 4: Preserve sums/counts separately from histograms

**Decision**: AVG uses exact accumulated floating-point sum divided by integer observation count; P95 uses the merged histogram only.

**Rationale**: Quantization appropriate for percentiles must not bias averages. SQLite REAL provides sufficient precision for expected CPU and byte totals at the documented fleet scale.

## Decision 5: Roll up per host through existing telemetry lifecycle

**Decision**: Add raw, five-minute, and hourly workload tables and invoke workload rollups from the existing `Aggregator`. Retention applies the same 25h raw, 6d five-minute, and configured hourly windows as scalar metrics.

**Rationale**: Per-host tier rows preserve selected-host recomputation. Reusing scheduling, watermarks, and retention avoids a second service loop.

**Alternatives rejected**:
- Query raw forever: violates retention/performance goals.
- Fleet-wide preaggregation: breaks host filtering.
- Browser-side merging: leaks storage details and transfers unnecessary blobs.

## Decision 6: Add a typed fleet workload response

**Decision**: `GET /api/v1/metrics/_fleet` adds optional `session_workload`; the existing generic `series` object remains unchanged.

**Rationale**: Workload points require coverage, denominators, rates, and average/max concurrency semantics that do not fit `CounterSeries.avg/min/max/p50`. A typed object prevents accidental reuse of legacy host-summary counters.

## Decision 7: Coverage is explicit and gaps are preserved

**Decision**: Every returned point reports expected and contributing hosts, observed CPU/memory sessions, successful-empty hosts, and omitted-host categories. No successful contributor means no point. Missing pre-feature history remains absent.

**Rationale**: Correct statistics over unknown coverage are operationally unsafe. The chart can stay usable under partial coverage while making incompleteness inspectable.

## Decision 8: Existing chart interaction and threshold grammar remains

**Decision**: Reuse the Overview fetch cancellation, selected-host query, shared window, pan/zoom, and chart component. Threshold severity follows Fleet Session P95 only; AVG remains contextual. CPU-active tooltips add rates and average/max counts.

**Rationale**: This changes metric meaning, not navigation or chart behavior, and minimizes UI divergence.
