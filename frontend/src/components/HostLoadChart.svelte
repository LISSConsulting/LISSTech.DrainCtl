<script>
    /**
     * HostLoadChart — per-host equivalent of MetricsChart's LOAD panel.
     *
     * Renders a single dual-axis chart carrying CPU, CPU P95, Memory, and
     * Sessions for one host, with window presets (15M/1H/1D/3D/5D),
     * drag-pan, wheel-zoom, series toggles, and a current-value readout.
     * Mirrors the composition of Overview's LOAD chart so operators see
     * the same visual grammar on the Server Detail drawer.
     *
     * Fetches from /api/v1/metrics/{host} — each instance owns its fetch,
     * window, pan, and toggle state. The window preset persists per-instance
     * under a host-qualified localStorage key so an operator's preferred
     * zoom survives navigation between server cards.
     */
    import { LayerCake, Svg } from 'layercake';
    import { OVERVIEW_WINDOW_PRESETS } from '../lib/state.svelte.js';
    import { fetchMetrics } from '../lib/api.js';
    import { resolveThresholds } from '../lib/thresholds.js';
    import { appState } from '../lib/state.svelte.js';
    import DualAxisChart from './chart/DualAxisChart.svelte';
    import { Cpu, MemoryStick, Users, Gauge } from 'lucide-svelte';

    /** @type {{ host: string }} */
    let { host } = $props();

    // ── Window preset (persisted per-host) ────────────────────────────────
    const LS_KEY = 'drainctl.hostLoadChart.window';
    function readStoredWindow() {
        try {
            const raw = localStorage.getItem(LS_KEY);
            if (!raw) return null;
            return OVERVIEW_WINDOW_PRESETS.some((p) => p.key === raw) ? raw : null;
        } catch {
            return null;
        }
    }
    let windowKey = $state(readStoredWindow() ?? '1hour');
    let windowMs = $derived(OVERVIEW_WINDOW_PRESETS.find((p) => p.key === windowKey)?.ms ?? 60 * 60 * 1000);
    $effect(() => {
        try {
            localStorage.setItem(LS_KEY, windowKey);
        } catch {
            /* localStorage disabled — not fatal */
        }
    });

    // ── Series toggles ────────────────────────────────────────────────────
    let showCpu = $state(true);
    let showCpuP95 = $state(false);
    let showMem = $state(true);
    let showSessions = $state(true);

    const LOAD_SERIES = [
        { key: 'cpu', label: 'CPU %', color: 'var(--color-accent)', axis: 'left', lineOnly: false,
          show: () => showCpu, toggle: () => { showCpu = !showCpu; } },
        { key: 'cpuP95', label: 'CPU P95', color: 'var(--color-amber)', axis: 'left', lineOnly: true,
          show: () => showCpuP95, toggle: () => { showCpuP95 = !showCpuP95; } },
        { key: 'mem', label: 'Memory %', color: 'var(--color-green)', axis: 'left', lineOnly: false,
          show: () => showMem, toggle: () => { showMem = !showMem; } },
        { key: 'sessions', label: 'Sessions', color: 'var(--color-red)', axis: 'right', lineOnly: true,
          show: () => showSessions, toggle: () => { showSessions = !showSessions; } },
    ];

    // ── Config-derived thresholds ─────────────────────────────────────────
    let perfCfg = $derived(appState.config?.performance ?? null);
    let cpuThresh = $derived(resolveThresholds('cpu', perfCfg));
    let memThresh = $derived(resolveThresholds('mem', perfCfg));

    // ── Fetch + pan state ─────────────────────────────────────────────────
    /** @type {import('../lib/api.js').MetricsResponse|null} */
    let response = $state(null);
    let loading = $state(false);
    let error = $state(false);
    let panOffsetMs = $state(0);
    let isDragging = $state(false);
    let dragStartX = 0;
    let dragStartOffset = 0;
    let dragPendingOffsetMs = 0;
    let containerW = $state(0);
    // didDrag distinguishes "click to pin/unpin" from "drag to pan". Set true
    // once the pointer moves past DRAG_THRESHOLD_PX during a mousedown cycle;
    // consumed by a capture-phase click listener on the chart body so the
    // post-drag click never reaches DualAxisChart's pin toggle. Threshold
    // intentionally generous — trackpad clicks routinely drift 5–8 px; anything
    // tighter makes "click to pin" unreliable because the post-click event
    // gets suppressed as a drag.
    //
    // $state so the class:dragging binding on .chart-body flips in lock-
    // step: dragging activates only once a real pan starts, which keeps
    // child SVG pointer-events alive during simple clicks. If dragging
    // engaged on mousedown the child's click target would sit behind
    // pointer-events:none by the time mouseup fires → click lands on
    // .chart-body, never on the rect, pin never triggers.
    let didDrag = $state(false);
    const DRAG_THRESHOLD_PX = 12;
    /** @type {HTMLDivElement|null} */
    let chartBodyEl = $state(null);

    // Counters needed: CPU (avg + P95), memory (derive from avail/total), sessions.
    const COUNTERS = ['cpu_pct', 'cpu_p95_pct', 'mem_avail_mb', 'mem_total_mb', 'sessions_total'];

    $effect(() => {
        // Reading these reactive values registers the effect's dependencies.
        const h = host;
        const ms = windowMs;
        const offset = panOffsetMs;
        if (!h) return;
        const ac = new AbortController();
        loading = true;
        error = false;
        const now = new Date();
        const to = new Date(now.getTime() - offset);
        const from = new Date(to.getTime() - ms);
        fetchMetrics(h, from, to, 'auto', COUNTERS, ac.signal)
            .then((data) => {
                if (!ac.signal.aborted) {
                    response = data;
                    loading = false;
                }
            })
            .catch(() => {
                if (!ac.signal.aborted) {
                    error = true;
                    loading = false;
                }
            });
        return () => {
            ac.abort();
        };
    });

    // ── Interactions ──────────────────────────────────────────────────────
    // Wheel zoom intentionally omitted — same policy as Overview LOAD and
    // lib/chart.svelte: operators dislike trackpad scrolls hijacking the
    // chart. Window preset pills are the only zoom path.

    /** @param {MouseEvent} e */
    function onMouseDown(e) {
        if (e.button !== 0) return;
        isDragging = true;
        didDrag = false;
        dragStartX = e.clientX;
        dragStartOffset = panOffsetMs;
        dragPendingOffsetMs = panOffsetMs;
        // Don't clear pinnedChartIndex here — that would defeat the
        // second-click-to-unpin path owned by DualAxisChart. Tooltip hover
        // gets cleared below only once the move passes the drag threshold.
    }

    /** @param {MouseEvent} e */
    function onMouseMove(e) {
        if (!isDragging) return;
        const dx = e.clientX - dragStartX;
        if (!didDrag && Math.abs(dx) > DRAG_THRESHOLD_PX) {
            didDrag = true;
            // Hide the hover tooltip the moment the user commits to a pan.
            appState.hoveredChartIndex = null;
        }
        if (didDrag) {
            const dMs = (dx / Math.max(containerW, 1)) * windowMs;
            dragPendingOffsetMs = Math.max(0, Math.round(dragStartOffset - dMs));
        }
    }

    function commitPan() {
        if (isDragging && didDrag && dragPendingOffsetMs !== panOffsetMs) {
            panOffsetMs = dragPendingOffsetMs;
        }
        isDragging = false;
        // didDrag stays true until the capture-phase click listener below
        // consumes it. Browsers dispatch `click` after mouseup on the same
        // logical gesture — without this gate the post-drag click would hit
        // DualAxisChart and toggle the pin state the user didn't ask for.
    }

    // Capture-phase click suppression: if the mouseup ended a real drag,
    // stop the synthetic click from reaching DualAxisChart's pin toggle.
    // Runs before target-phase handlers so stopPropagation actually wins.
    $effect(() => {
        const el = chartBodyEl;
        if (!el) return;
        const handler = (/** @type {MouseEvent} */ e) => {
            if (didDrag) {
                e.stopPropagation();
                e.preventDefault();
                didDrag = false;
            }
        };
        el.addEventListener('click', handler, { capture: true });
        return () => el.removeEventListener('click', handler, { capture: true });
    });

    // ── Adapt response.series → MetricsSample[] ───────────────────────────
    /** @typedef {{ time: number, cpu: number, cpuP95: number, mem: number, sessions: number }} Sample */
    //
    // Each counter is a parallel-arrays Series (t[], avg[]). Counters are
    // collected from the same PerfSnapshot but the storage path can drop
    // individual values (warmup, transient unavailability), so different
    // counters may return different lengths or skip timestamps. Indexing by
    // position would mis-pair (cpu_pct[5], cpu_p95_pct[5]) when cpu_p95_pct
    // dropped a sample earlier — that's how we hit "CPU=27.7% / P95=0%" on
    // the same point. Build a per-counter ts→value map and join on the
    // canonical cpu_pct timeline.
    function asTsMap(/** @type {{t:number[], avg:number[]}|undefined} */ s) {
        const m = new Map();
        if (!s) return m;
        const t = s.t || [];
        const a = s.avg || [];
        for (let i = 0; i < t.length; i++) m.set(t[i], a[i]);
        return m;
    }
    let history = $derived.by(() => {
        const series = response?.series ?? {};
        const cpu = series['cpu_pct'];
        if (!cpu || cpu.t.length === 0) return /** @type {Sample[]} */ ([]);
        const cpuMap = asTsMap(cpu);
        const p95Map = asTsMap(series['cpu_p95_pct']);
        const availMap = asTsMap(series['mem_avail_mb']);
        const totalMap = asTsMap(series['mem_total_mb']);
        const sessMap = asTsMap(series['sessions_total']);
        return cpu.t.map((ts) => {
            const cpuV = cpuMap.get(ts) ?? 0;
            const p95V = p95Map.get(ts);
            const a = availMap.get(ts) ?? 0;
            const m = totalMap.get(ts) ?? 0;
            const sV = sessMap.get(ts);
            return {
                time: ts,
                cpu: cpuV,
                // Fall back to current CPU when P95 is missing for this ts
                // (warmup or skipped storage). 0 is treated as missing too —
                // the agent emits 0 when its rolling window has no samples.
                cpuP95: p95V != null && p95V > 0 ? p95V : cpuV,
                mem: m > 0 ? (1 - a / m) * 100 : 0,
                sessions: sV != null ? Math.round(sV) : 0,
            };
        });
    });

    let sessionMax = $derived(Math.max(...history.map((h) => h.sessions ?? 0), 1));
    let hasRight = $derived(showSessions);

    let loadNormData = $derived(
        history.map((h, i) => ({
            i,
            time: h.time,
            cpu: Math.min(h.cpu ?? 0, 100),
            cpuP95: Math.min(h.cpuP95 ?? h.cpu ?? 0, 100),
            mem: Math.min(h.mem ?? 0, 100),
            sessions: ((h.sessions ?? 0) / sessionMax) * 100,
            raw: {
                cpu: +(h.cpu ?? 0).toFixed(1),
                cpuP95: +(h.cpuP95 ?? h.cpu ?? 0).toFixed(1),
                mem: +(h.mem ?? 0).toFixed(1),
                sessions: h.sessions ?? 0,
            },
        })),
    );

    let rightTicks = $derived(
        showSessions
            ? [0, 0.25, 0.5, 0.75, 1].map((f) => ({
                  pct: f * 100,
                  label: Math.round(f * sessionMax).toString(),
              }))
            : [],
    );

    let loadVisible = $derived({ cpu: showCpu, cpuP95: showCpuP95, mem: showMem, sessions: showSessions });

    let activeIdx = $derived(appState.pinnedChartIndex ?? appState.hoveredChartIndex);
    let displayPoint = $derived(
        activeIdx !== null ? (history[activeIdx] ?? history[history.length - 1]) : history[history.length - 1],
    );
    let loadCurrents = $derived([
        { label: 'CPU', value: displayPoint ? `${(+displayPoint.cpu).toFixed(1)}%` : '—',
          color: 'var(--color-accent)', icon: Cpu, show: () => showCpu },
        { label: 'CPU P95', value: displayPoint ? `${(+(displayPoint.cpuP95 ?? displayPoint.cpu)).toFixed(1)}%` : '—',
          color: 'var(--color-amber)', icon: Cpu, show: () => showCpuP95 },
        { label: 'MEM', value: displayPoint ? `${(+displayPoint.mem).toFixed(1)}%` : '—',
          color: 'var(--color-green)', icon: MemoryStick, show: () => showMem },
        { label: 'SESS', value: displayPoint ? `${displayPoint.sessions ?? 0}` : '—',
          color: 'var(--color-red)', icon: Users, show: () => showSessions },
    ]);

    const Y_DOMAIN = [0, 100];
    let lcData = $derived(history.map((_, i) => ({ x: i, y: 50 })));
</script>

<div class="h-load">
    <div class="sub-label">
        <Gauge size={12} strokeWidth={2.4} /> HOST LOAD
    </div>

    <div class="window-pills">
        {#each OVERVIEW_WINDOW_PRESETS as preset}
            <button
                class="window-pill"
                class:active={windowKey === preset.key && panOffsetMs === 0}
                onclick={() => {
                    panOffsetMs = 0;
                    windowKey = preset.key;
                }}
                aria-pressed={windowKey === preset.key && panOffsetMs === 0}
            >
                {preset.label}
            </button>
        {/each}
    </div>

    <div class="chart-toggles-stacked">
        {#each LOAD_SERIES as s}
            <button
                class="chart-toggle"
                class:active={s.show()}
                style="--sc: {s.color}"
                aria-pressed={s.show()}
                onclick={s.toggle}
            >
                {#if s.lineOnly}
                    <span class="t-dash" aria-hidden="true"></span>
                {:else}
                    <span class="t-dot" aria-hidden="true"></span>
                {/if}
                {s.label}
                {#if s.axis === 'right'}<span class="t-axis">R</span>{/if}
            </button>
        {/each}
    </div>

    <div class="chart-panel">
        <div class="load-chart-header">
            <div></div>
            <div class="load-currents" style="padding-right: {hasRight ? 64 : 16}px">
                {#each loadCurrents as lc}
                    {#if lc.show()}
                        {@const Icon = lc.icon}
                        <span class="load-val">
                            <Icon size={11} strokeWidth={2.2} />
                            <span class="load-val-label">{lc.label}</span>
                            <span class="load-val-num" style="color: {lc.color}">{lc.value}</span>
                        </span>
                    {/if}
                {/each}
            </div>
        </div>
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div
            class="chart-body"
            class:dragging={isDragging && didDrag}
            bind:clientWidth={containerW}
            bind:this={chartBodyEl}
            onmousedown={onMouseDown}
            onmousemove={onMouseMove}
            onmouseup={commitPan}
            onmouseleave={commitPan}
        >
            {#if containerW > 0}
                <LayerCake
                    data={lcData}
                    x="x"
                    y="y"
                    yDomain={Y_DOMAIN}
                    padding={{ top: 16, right: hasRight ? 64 : 16, bottom: 24, left: 48 }}
                >
                    <Svg>
                        <DualAxisChart
                            normData={loadNormData}
                            SERIES={LOAD_SERIES}
                            {rightTicks}
                            thresholds={[
                                { pct: cpuThresh.warn, opacity: 0.25, label: 'CPU WARN', show: () => showCpu },
                                { pct: cpuThresh.crit, opacity: 0.35, label: 'CPU CRIT', show: () => showCpu },
                                { pct: memThresh.warn, opacity: 0.25, label: 'MEM WARN', show: () => showMem },
                                { pct: memThresh.crit, opacity: 0.35, label: 'MEM CRIT', show: () => showMem },
                            ]}
                            {history}
                            visible={loadVisible}
                            showXAxis={true}
                        />
                    </Svg>
                </LayerCake>
            {/if}
            <!-- Status messages overlay the chart frame instead of replacing
                 it — the operator keeps axes, thresholds, and cursor context
                 while panning back into covered history. pointer-events:none
                 on .chart-overlay lets the underlying chart-body still
                 receive pan mouse events; .overlay-live opts the button back
                 in so the LIVE click still registers. -->
            {#if loading}
                <div class="chart-overlay">Loading retained history…</div>
            {:else if error}
                <div class="chart-overlay chart-error">Unable to reach the metrics endpoint</div>
            {:else if history.length < 2}
                <div class="chart-overlay">
                    {#if panOffsetMs > 0}
                        <button class="back-to-live overlay-live" onclick={() => { panOffsetMs = 0; }}>↺ LIVE</button>
                    {/if}
                    <span>No retained history for this window</span>
                </div>
            {/if}
        </div>
    </div>
</div>

<style>
    .h-load {
        display: flex;
        flex-direction: column;
    }

    .sub-label {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.6rem;
        font-weight: 700;
        letter-spacing: 0.14em;
        text-transform: uppercase;
        color: var(--color-muted);
        margin-bottom: 10px;
        opacity: 0.65;
        display: flex;
        align-items: center;
        gap: 5px;
    }

    .window-pills {
        display: flex;
        gap: 4px;
        margin-bottom: 10px;
    }

    .window-pill {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.58rem;
        font-weight: 700;
        letter-spacing: 0.12em;
        text-transform: uppercase;
        padding: 4px 9px;
        border: var(--spacing-bw) solid var(--color-border);
        border-radius: var(--radius-default);
        box-shadow: 2px 2px 0 var(--color-shadow);
        background: var(--color-surface);
        color: var(--color-muted);
        cursor: pointer;
        transition:
            transform 0.08s linear,
            box-shadow 0.08s linear,
            color 0.08s linear,
            background 0.08s linear;
    }

    .window-pill:hover {
        color: var(--color-fg);
        transform: translate(-1px, -1px);
        box-shadow: 3px 3px 0 var(--color-shadow);
    }

    .window-pill:active {
        transform: translate(2px, 2px);
        box-shadow: none;
    }

    .window-pill.active {
        background: var(--color-accent);
        color: #fff;
        border-color: var(--color-accent);
    }

    .chart-toggles-stacked {
        display: flex;
        gap: 6px;
        flex-wrap: wrap;
        margin-bottom: 10px;
    }

    .chart-toggle {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.6rem;
        font-weight: 700;
        letter-spacing: 0.1em;
        text-transform: uppercase;
        padding: 4px 9px;
        border-radius: var(--radius-default);
        border: var(--spacing-bw) solid var(--color-border);
        box-shadow: 2px 2px 0 var(--color-shadow);
        background: var(--color-surface);
        color: var(--color-fg);
        cursor: pointer;
        display: flex;
        align-items: center;
        gap: 6px;
        transition:
            transform 0.08s linear,
            box-shadow 0.08s linear;
        white-space: nowrap;
    }

    .chart-toggle:hover {
        transform: translate(-1px, -1px);
        box-shadow: 3px 3px 0 var(--color-shadow);
    }

    .chart-toggle:active {
        transform: translate(2px, 2px);
        box-shadow: none;
    }

    .chart-toggle.active {
        background: var(--sc);
        color: #fff;
    }

    .t-dot {
        width: 8px;
        height: 8px;
        border-radius: 1px;
        background: var(--sc);
        border: 1.5px solid color-mix(in srgb, var(--color-border) 60%, transparent);
        flex-shrink: 0;
    }

    .chart-toggle.active .t-dot {
        background: rgba(255, 255, 255, 0.85);
        border-color: rgba(255, 255, 255, 0.4);
    }

    .t-dash {
        width: 18px;
        height: 3px;
        background: repeating-linear-gradient(
            to right,
            var(--sc) 0px,
            var(--sc) 7px,
            transparent 7px,
            transparent 11px
        );
        flex-shrink: 0;
    }

    .chart-toggle.active .t-dash {
        background: repeating-linear-gradient(
            to right,
            rgba(255, 255, 255, 0.9) 0px,
            rgba(255, 255, 255, 0.9) 7px,
            transparent 7px,
            transparent 11px
        );
    }

    .t-axis {
        font-size: 0.52rem;
        opacity: 0.55;
        margin-left: -2px;
    }

    .chart-panel {
        display: flex;
        flex-direction: column;
    }

    .load-chart-header {
        display: flex;
        align-items: flex-end;
        justify-content: space-between;
        margin-bottom: 4px;
    }

    .load-currents {
        display: flex;
        align-items: center;
        gap: 12px;
    }

    .load-val {
        display: flex;
        align-items: center;
        gap: 4px;
        font-family: 'JetBrains Mono', monospace;
        color: var(--color-fg);
    }

    .load-val-label {
        font-size: 0.55rem;
        font-weight: 700;
        letter-spacing: 0.1em;
        text-transform: uppercase;
        color: var(--color-muted);
    }

    .load-val-num {
        font-size: 1.0rem;
        font-weight: 700;
        letter-spacing: -0.02em;
        line-height: 1;
    }

    .chart-body {
        position: relative;
        height: 220px;
        cursor: grab;
    }

    .chart-body.dragging {
        cursor: grabbing;
        user-select: none;
    }

    /* While actively panning, stop the inner SVG from receiving mouse
       events — DualAxisChart's own onmousemove would otherwise keep
       updating the tooltip crosshair, making it look like the tooltip is
       fighting the pan. Events still reach the parent div for drag
       tracking. */
    .chart-body.dragging :global(svg) {
        pointer-events: none;
    }

    /* Overlay sits on top of the chart frame without replacing it. The
       chart axes, thresholds, and grab cursor stay visible — and
       pointer-events:none lets the drag-pan gesture still reach the
       chart body underneath so the operator can drag back into
       covered history without hunting for a pan target. The LIVE
       button inside (.overlay-live) opts pointer-events back in so it
       remains clickable. */
    .chart-overlay {
        position: absolute;
        inset: 0;
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        gap: 10px;
        padding: 8px 16px;
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.8rem;
        color: var(--color-muted);
        background: color-mix(in srgb, var(--color-bg) 72%, transparent);
        backdrop-filter: blur(1px);
        pointer-events: none;
        z-index: 2;
    }

    .chart-overlay.chart-error {
        color: var(--color-red);
    }

    .overlay-live {
        pointer-events: auto;
    }

    .back-to-live {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.56rem;
        font-weight: 700;
        letter-spacing: 0.12em;
        text-transform: uppercase;
        padding: 3px 8px;
        border: var(--spacing-bw) solid var(--color-amber);
        border-radius: var(--radius-default);
        box-shadow: 1px 1px 0 var(--color-shadow);
        background: var(--color-surface);
        color: var(--color-amber);
        cursor: pointer;
    }

    .back-to-live:hover {
        transform: translate(-1px, -1px);
        box-shadow: 2px 2px 0 var(--color-shadow);
    }

    .back-to-live:active {
        transform: translate(1px, 1px);
        box-shadow: none;
    }
</style>
