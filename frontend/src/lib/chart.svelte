<script>
    /**
     * chart.svelte — durable per-host metrics chart backed by GET /api/v1/metrics/{host}.
     *
     * Replaces the legacy history-ring data source; the server now holds the
     * authoritative series in SQLite so the chart survives a service restart
     * without losing pre-restart samples.
     *
     * Zoom/pan: wheel zooms around the cursor, drag pans horizontally,
     * double-click resets to the default window. Handlers are debounced to
     * 150 ms (research.md §9) and cancel in-flight fetches via AbortController
     * so a rapid drag doesn't pile up stale responses on the wire.
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

    /** @type {{
     *   host: string,
     *   counter?: string,
     *   windowMs?: number,
     *   resolution?: 'auto'|'raw'|'5min'|'hourly',
     *   color?: string,
     *   height?: number,
     *   refreshMs?: number,
     * }} */
    let {
        host,
        counter = 'cpu_pct',
        windowMs = 5 * 24 * 60 * 60 * 1000,
        resolution = 'auto',
        color = 'var(--color-accent)',
        height = 200,
        refreshMs = 0,
    } = $props();

    /** @type {import('./api.js').MetricsResponse | null} */
    let response = $state(null);
    let loading = $state(false);
    let error = $state('');

    // Authoritative visible window. Seeded by the prop-change $effect below
    // (which runs synchronously on mount); mutated by zoom/pan; reset by
    // double-click. Seed values here are never rendered.
    let viewFrom = $state(new Date(0));
    let viewTo = $state(new Date(0));

    const MIN_SPAN_MS = 60_000; // 1 minute
    const MAX_SPAN_MS = 365 * 24 * 60 * 60 * 1000; // 1 year
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
    async function load(from, to) {
        if (!host) return;
        const mySeq = ++fetchSeq;

        if (inflight) inflight.abort();
        inflight = new AbortController();
        const signal = inflight.signal;

        loading = true;
        error = '';
        try {
            const r = await fetchMetrics(host, from, to, resolution, [counter], signal);
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
        const to = new Date();
        const from = new Date(to.getTime() - windowMs);
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

    /** @param {WheelEvent} e */
    function onWheel(e) {
        e.preventDefault();
        const el = /** @type {HTMLElement} */ (e.currentTarget);
        const rect = el.getBoundingClientRect();
        if (rect.width <= 0) return;
        const relX = Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width));

        const fromT = viewFrom.getTime();
        const toT = viewTo.getTime();
        const span = toT - fromT;
        if (span <= 0) return;

        // deltaY > 0 (scroll down) → zoom out. 1.25 / 0.8 are reciprocals so
        // a scroll-up-then-down returns to the original span.
        const factor = e.deltaY > 0 ? 1.25 : 0.8;
        const newSpan = Math.max(MIN_SPAN_MS, Math.min(MAX_SPAN_MS, span * factor));
        if (newSpan === span) return;

        const cursorT = fromT + span * relX;
        viewFrom = new Date(cursorT - newSpan * relX);
        viewTo = new Date(cursorT + newSpan * (1 - relX));
        scheduleLoad();
    }

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
        { label: 'MIN', ms: 60 * 1000 },
        { label: 'HOUR', ms: 60 * 60 * 1000 },
        { label: 'DAY', ms: 24 * 60 * 60 * 1000 },
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

    let series = $derived(response?.series?.[counter] ?? null);
    let isEmpty = $derived.by(() => {
        if (!response) return true;
        const map = response.servers ?? response.series ?? {};
        if (Object.keys(map).length === 0) return true;
        return !series || !Array.isArray(series.t) || series.t.length === 0;
    });

    let points = $derived.by(() => {
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
            <span class="meta-counter">{counter}</span>
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
        onwheel={onWheel}
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
                yDomain={[0, null]}
                padding={{ top: 12, right: 16, bottom: 28, left: 52 }}
            >
                <Svg>
                    <InteractiveTimeChart {points} {counter} {color} hideHover={isDragging} />
                </Svg>
            </LayerCake>
            <div class="chart-hint" aria-hidden="true">scroll · drag · dbl-click</div>
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
