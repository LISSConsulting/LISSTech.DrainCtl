<script>
    /**
     * chart.svelte — durable per-host metrics chart backed by GET /api/v1/metrics/{host}.
     *
     * Replaces the legacy history-ring data source; the server now holds the
     * authoritative series in SQLite so the chart survives a service restart
     * without losing pre-restart samples.
     *
     * Pan: drag pans horizontally, double-click resets to the default window,
     * and the preset pills jump to named windows. Wheel zoom is deliberately
     * disabled because operators found accidental trackpad scrolls kept
     * hijacking the chart. Handlers are debounced to 150 ms (research.md §9)
     * and cancel in-flight fetches via AbortController so a rapid drag doesn't
     * pile up stale responses on the wire.
     *
     * Props:
     *   host        — required hostname (must be registered)
     *   counter     — counter name from the PerfSnapshot/SessionSummary set;
     *                 defaults to 'cpu_pct'
     *   windowMs    — default lookback window in ms (also the reset target)
     *   resolution  — 'auto' | 'raw' | '5min' | 'hourly' (default 'auto')
     *   color       — line/fill color (CSS var or hex)
     *   height      — chart height in pixels
     *   refreshMs   — optional auto-refetch interval; 0 disables polling
     *
     * When the server returns an empty series map the chart renders the
     * "Collecting data…" status per FR-019a.
     */
    import { LayerCake, Svg } from 'layercake';
    import InteractiveTimeChart from '../components/chart/InteractiveTimeChart.svelte';
    import { fetchMetrics } from './api.js';
    import { counterLabel, isPercentCounter } from './utils.js';

    /** @type {{
     *   host: string,
     *   counter?: string,
     *   windowMs?: number,
     *   resolution?: 'auto'|'raw'|'1min'|'5min'|'hourly',
     *   color?: string,
     *   height?: number,
     *   refreshMs?: number,
     * }} */
    let {
        host,
        counter = 'cpu_pct',
        windowMs = 24 * 60 * 60 * 1000,
        resolution = 'auto',
        color = 'var(--color-accent)',
        height = 200,
        refreshMs = 0,
    } = $props();

    // Persisted zoom-pill choice. The last pill the operator clicked sticks
    // across page reloads and server-card switches so they don't have to re-pick
    // their preferred zoom every time. Manual wheel/drag zooms are intentionally
    // not persisted — those are transient inspection gestures.
    const ZOOM_PRESET_STORAGE_KEY = 'drainctl.chart.zoomPillMs';
    function readStoredPresetMs() {
        try {
            const raw = localStorage.getItem(ZOOM_PRESET_STORAGE_KEY);
            if (!raw) return null;
            const n = Number(raw);
            return Number.isFinite(n) && n > 0 ? n : null;
        } catch {
            return null;
        }
    }
    function writeStoredPresetMs(ms) {
        try {
            localStorage.setItem(ZOOM_PRESET_STORAGE_KEY, String(ms));
        } catch {
            /* localStorage disabled / full — preset just won't persist */
        }
    }

    /** @type {import('./api.js').MetricsResponse | null} */
    let response = $state(null);
    let loading = $state(false);
    let error = $state('');

    // Authoritative visible window. Seeded by the prop-change $effect below
    // (which runs synchronously on mount); mutated by zoom/pan; reset by
    // double-click. Seed values here are never rendered.
    let viewFrom = $state(new Date(0));
    let viewTo = $state(new Date(0));

    const DEBOUNCE_MS = 150;

    // Monotonic sequence so a slow-returning request can't overwrite a newer
    // one even if the abort signal hasn't propagated to fetch's reject yet.
    let fetchSeq = 0;
    /** @type {AbortController | null} */
    let inflight = null;
    /** @type {ReturnType<typeof setTimeout> | null} */
    let debounceTimer = null;

    /**
     * @param {Date} from
     * @param {Date} to
     */
    // mem_used_pct is a virtual counter — the service stores mem_avail_mb and
    // mem_total_mb separately, so the chart fetches both and derives the %
    // used client-side. Matches the derivation in MetricsChart's fleet adapter.
    const VIRTUAL_MEM_USED_PCT = 'mem_used_pct';
    let requestedCounters = $derived(
        counter === VIRTUAL_MEM_USED_PCT ? ['mem_avail_mb', 'mem_total_mb'] : [counter],
    );

    async function load(from, to) {
        if (!host) return;
        const mySeq = ++fetchSeq;

        if (inflight) inflight.abort();
        inflight = new AbortController();
        const signal = inflight.signal;

        loading = true;
        error = '';
        try {
            const r = await fetchMetrics(host, from, to, resolution, requestedCounters, signal);
            if (mySeq !== fetchSeq) return;
            response = r;
        } catch (e) {
            if (signal.aborted || (e instanceof DOMException && e.name === 'AbortError')) return;
            if (mySeq !== fetchSeq) return;
            response = null;
            error = /** @type {Error} */ (e)?.message ?? String(e);
        } finally {
            if (mySeq === fetchSeq) loading = false;
        }
    }

    function scheduleLoad() {
        if (debounceTimer) clearTimeout(debounceTimer);
        debounceTimer = setTimeout(() => {
            debounceTimer = null;
            load(viewFrom, viewTo);
        }, DEBOUNCE_MS);
    }

    // Prop-driven reload: reset the window and fetch immediately when the
    // identifying props actually change. Svelte can re-run an $effect on any
    // parent re-render even when prop values are unchanged — without this
    // guard, the parent's 10 s clock tick would silently wipe the user's
    // zoom/pan back to the default window.
    let lastPropKey = '';
    $effect(() => {
        const key = `${host}|${counter}|${windowMs}|${resolution}`;
        if (key === lastPropKey) return;
        lastPropKey = key;
        // Prefer the operator's most recent pill choice (validated against the
        // current PRESETS so a stale/renamed value falls back cleanly) over the
        // prop default. Without this, flipping between server cards would reset
        // the zoom every time.
        const stored = readStoredPresetMs();
        const span = stored != null && PRESETS.some((p) => p.ms === stored) ? stored : windowMs;
        const to = new Date();
        const from = new Date(to.getTime() - span);
        viewFrom = from;
        viewTo = to;
        if (debounceTimer) {
            clearTimeout(debounceTimer);
            debounceTimer = null;
        }
        load(from, to);
    });

    // Optional polling so the chart keeps pace while the user stays on the page.
    // The setInterval callback reads viewFrom/viewTo outside a reactive context,
    // so zoom/pan does not reset the interval. If the visible window's right
    // edge is still near real-time we slide the window forward by the elapsed
    // time so newly collected samples come into view; once the user pans well
    // into the past we leave the fixed window alone.
    $effect(() => {
        if (!refreshMs || refreshMs <= 0) return;
        const id = setInterval(() => {
            const nowMs = Date.now();
            const fromMs = viewFrom.getTime();
            const toMs = viewTo.getTime();
            const span = toMs - fromMs;
            if (span > 0 && toMs >= nowMs - 2 * refreshMs) {
                const newTo = new Date(nowMs);
                const newFrom = new Date(nowMs - span);
                viewFrom = newFrom;
                viewTo = newTo;
                load(newFrom, newTo);
            } else {
                load(viewFrom, viewTo);
            }
        }, refreshMs);
        return () => clearInterval(id);
    });

    $effect(() => {
        return () => {
            if (debounceTimer) clearTimeout(debounceTimer);
            if (inflight) inflight.abort();
        };
    });

    let isDragging = $state(false);
    let dragPointerId = -1;
    let dragStartX = 0;
    let dragStartFromMs = 0;
    let dragStartToMs = 0;
    let dragWidth = 0;

    /** @param {PointerEvent} e */
    function onPointerDown(e) {
        if (e.button !== 0) return;
        const el = /** @type {HTMLElement} */ (e.currentTarget);
        const rect = el.getBoundingClientRect();
        if (rect.width <= 0) return;
        isDragging = true;
        dragPointerId = e.pointerId;
        dragStartX = e.clientX;
        dragStartFromMs = viewFrom.getTime();
        dragStartToMs = viewTo.getTime();
        dragWidth = rect.width;
        el.setPointerCapture?.(e.pointerId);
    }

    /** @param {PointerEvent} e */
    function onPointerMove(e) {
        if (!isDragging || e.pointerId !== dragPointerId) return;
        const dx = e.clientX - dragStartX;
        const span = dragStartToMs - dragStartFromMs;
        // Drag right → pan backwards in time (content follows the pointer).
        const deltaT = -(dx / dragWidth) * span;
        viewFrom = new Date(dragStartFromMs + deltaT);
        viewTo = new Date(dragStartToMs + deltaT);
        scheduleLoad();
    }

    /** @param {PointerEvent} e */
    function onPointerUp(e) {
        if (!isDragging || e.pointerId !== dragPointerId) return;
        const el = /** @type {HTMLElement} */ (e.currentTarget);
        el.releasePointerCapture?.(e.pointerId);
        isDragging = false;
        dragPointerId = -1;
    }

    function onDoubleClick() {
        const to = new Date();
        const from = new Date(to.getTime() - windowMs);
        viewFrom = from;
        viewTo = to;
        if (debounceTimer) {
            clearTimeout(debounceTimer);
            debounceTimer = null;
        }
        load(from, to);
    }

    // ── Zoom preset pills ────────────────────────────────────────────────
    // Operators rarely know to scroll-wheel a chart — surface the common
    // windows as clickable affordances. Tolerance when matching current span
    // to a preset is 1 % to account for float drift from wheel zoom.
    const PRESETS = [
        { label: '15M', ms: 15 * 60 * 1000 },
        { label: '1H', ms: 60 * 60 * 1000 },
        { label: '1D', ms: 24 * 60 * 60 * 1000 },
        { label: '3D', ms: 3 * 24 * 60 * 60 * 1000 },
        { label: '5D', ms: 5 * 24 * 60 * 60 * 1000 },
    ];

    /** @param {number} spanMs */
    function applyPreset(spanMs) {
        const to = new Date();
        const from = new Date(to.getTime() - spanMs);
        viewFrom = from;
        viewTo = to;
        if (debounceTimer) {
            clearTimeout(debounceTimer);
            debounceTimer = null;
        }
        writeStoredPresetMs(spanMs);
        load(from, to);
    }

    // Compute which preset matches the current viewFrom→viewTo span. Nothing
    // matches when the user has zoomed to a custom range (returns null).
    let activePresetMs = $derived.by(() => {
        const span = viewTo.getTime() - viewFrom.getTime();
        if (span <= 0) return null;
        for (const p of PRESETS) {
            if (Math.abs(span - p.ms) / p.ms < 0.01) return p.ms;
        }
        return null;
    });

    // For mem_used_pct the returned series map has no `mem_used_pct` entry; we
    // compute points below by joining mem_avail_mb and mem_total_mb. The
    // regular isEmpty / series accessors still fire for real counters.
    let series = $derived(
        counter === VIRTUAL_MEM_USED_PCT ? (response?.series?.['mem_total_mb'] ?? null) : (response?.series?.[counter] ?? null),
    );
    let isEmpty = $derived.by(() => {
        if (!response) return true;
        const map = response.series ?? {};
        if (Object.keys(map).length === 0) return true;
        return !series || !Array.isArray(series.t) || series.t.length === 0;
    });

    let points = $derived.by(() => {
        if (counter === VIRTUAL_MEM_USED_PCT) {
            const avail = response?.series?.['mem_avail_mb'];
            const total = response?.series?.['mem_total_mb'];
            if (!avail || !total) return [];
            const ts = total.t ?? [];
            const availAvg = avail.avg ?? [];
            const totalAvg = total.avg ?? [];
            /** @type {{t:number,v:number}[]} */
            const out = [];
            for (let i = 0; i < ts.length; i++) {
                const a = availAvg[i];
                const m = totalAvg[i];
                if (a == null || m == null || !Number.isFinite(a) || !Number.isFinite(m) || m <= 0) continue;
                out.push({ t: ts[i], v: (1 - a / m) * 100 });
            }
            return out;
        }
        if (!series) return [];
        const ts = series.t ?? [];
        const avg = series.avg ?? [];
        /** @type {{t:number,v:number}[]} */
        const out = [];
        for (let i = 0; i < ts.length; i++) {
            const v = avg[i];
            if (v == null || !Number.isFinite(v)) continue;
            out.push({ t: ts[i], v });
        }
        return out;
    });

    let tier = $derived(response?.tier ?? resolution);

    // Percent counters (CPU %, Memory %, …) pin the Y-axis to 0–100 so ticks
    // like 75 % / 100 % don't scale to a pixel Y above the chart area and spill
    // up into the card above. Non-percent counters keep the auto-upper-bound
    // behaviour (LayerCake treats null as "use dataMax").
    let yDomain = $derived(isPercentCounter(counter) ? [0, 100] : [0, null]);

    // FR-019: flag when the requested window reaches further into the past
    // than the chosen tier still retains. `oldest_available` is the oldest
    // row in the tier for this host (not just within the request window);
    // if it's strictly newer than the left edge of the view, the chart is
    // missing data the user asked for because retention has already purged it.
    let retentionTruncated = $derived.by(() => {
        const oa = response?.oldest_available;
        if (!oa) return false;
        const oldestMs = Date.parse(oa);
        if (!Number.isFinite(oldestMs)) return false;
        return oldestMs > viewFrom.getTime();
    });
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="chart-wrap" style="height:{height}px" role="img" aria-label="{counter} history for {host}">
    <div class="chart-controls">
        <span class="chart-meta-inline">
            <span class="meta-counter" title={counter}>{counterLabel(counter)}</span>
            <span class="meta-tier">tier={tier}</span>
        </span>
        <div class="zoom-pills" role="group" aria-label="Zoom preset">
            {#each PRESETS as p}
                <button
                    type="button"
                    class="btn-brutal zoom-pill"
                    class:active={activePresetMs === p.ms}
                    onclick={() => applyPreset(p.ms)}
                    aria-pressed={activePresetMs === p.ms}
                >
                    {p.label}
                </button>
            {/each}
        </div>
    </div>

    <div
        class="chart-canvas"
        class:dragging={isDragging}
        onpointerdown={onPointerDown}
        onpointermove={onPointerMove}
        onpointerup={onPointerUp}
        onpointercancel={onPointerUp}
        ondblclick={onDoubleClick}
    >
        {#if error}
            <div class="chart-status err">Error: {error}</div>
        {:else if loading && !response}
            <div class="chart-status">Loading…</div>
        {:else if isEmpty}
            <!-- When the whole visible window is before oldest_available the server
                 returns an empty series even though data exists — surface retention
                 as the reason instead of the misleading "Collecting data…" state. -->
            {#if retentionTruncated}
                <div class="chart-status retention" title="oldest_available={response?.oldest_available}">
                    Data beyond this range is not retained
                </div>
            {:else}
                <div class="chart-status">Collecting data…</div>
            {/if}
        {:else}
            <LayerCake
                data={points}
                x="t"
                y="v"
                xDomain={[viewFrom.getTime(), viewTo.getTime()]}
                {yDomain}
                padding={{ top: 12, right: 16, bottom: 28, left: 52 }}
            >
                <Svg>
                    <InteractiveTimeChart {points} {counter} {color} hideHover={isDragging} />
                </Svg>
            </LayerCake>
            <div class="chart-hint" aria-hidden="true">drag · dbl-click</div>
            {#if retentionTruncated}
                <div class="retention-badge" title="oldest_available={response?.oldest_available}">
                    Data beyond this range is not retained
                </div>
            {/if}
        {/if}
    </div>
</div>

<style>
    .chart-wrap {
        display: flex;
        flex-direction: column;
        width: 100%;
        background: var(--color-surface);
        border-radius: var(--radius-default);
        user-select: none;
    }

    /* ── Controls bar (counter/tier meta + zoom preset pills) ── */
    .chart-controls {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 12px;
        padding: 6px 8px 4px;
        flex-wrap: wrap;
    }
    .chart-meta-inline {
        display: flex;
        gap: 8px;
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.55rem;
        text-transform: uppercase;
        letter-spacing: 0.08em;
        color: var(--color-muted);
        opacity: 0.7;
    }
    .chart-meta-inline .meta-tier {
        color: var(--color-subtle);
    }

    .zoom-pills {
        display: flex;
        gap: 4px;
    }
    .zoom-pill {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.6rem;
        font-weight: 700;
        padding: 3px 8px;
        color: var(--color-muted);
        letter-spacing: 0.06em;
    }
    .zoom-pill.active {
        background: var(--color-accent);
        color: #fff;
        border-color: var(--color-accent);
    }

    /* ── Canvas area (the SVG + overlays) ── */
    .chart-canvas {
        position: relative;
        flex: 1;
        min-height: 0;
        cursor: grab;
        touch-action: none;
    }
    .chart-canvas.dragging {
        cursor: grabbing;
    }

    .chart-status {
        position: absolute;
        inset: 0;
        display: flex;
        align-items: center;
        justify-content: center;
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.72rem;
        color: var(--color-muted);
        letter-spacing: 0.04em;
    }

    .chart-status.err {
        color: var(--color-red);
    }

    .chart-status.retention {
        color: var(--color-amber);
    }

    .retention-badge {
        position: absolute;
        top: 6px;
        left: 8px;
        padding: 2px 6px;
        background: var(--color-surface);
        border: 1px solid var(--color-amber);
        border-radius: var(--radius-default);
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.55rem;
        text-transform: uppercase;
        letter-spacing: 0.08em;
        color: var(--color-amber);
        pointer-events: none;
    }

    /* Subtle hint so operators discover the wheel/drag/dblclk interactions
       without needing to stumble into them by accident. */
    .chart-hint {
        position: absolute;
        bottom: 4px;
        right: 8px;
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.55rem;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        color: var(--color-subtle);
        opacity: 0.55;
        pointer-events: none;
    }
</style>
