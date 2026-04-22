<script>
    /**
     * SpikeSwimlane.svelte — per-host swimlane chart of confirmed evtspike
     * events backed by GET /api/evtspike/spikes?host=&from=&to=.
     *
     * Each distinct channel in the visible window is its own horizontal lane;
     * every confirmed spike lands as a single dot whose size scales with the
     * observed event count (sqrt so Security-scale spikes don't blot out
     * Application-scale) and whose color encodes severity from the
     * observed-over-expected ratio.
     *
     * Replaces the pre-009 RECENT SPIKES table — same data source, different
     * visual grammar: sparse point events are properly shown as sparse points,
     * not forced into a line-chart shape that implies continuity.
     *
     * Live SSE arrivals in appState.recentSpikes are merged in so a spike
     * confirmed while the user is watching the pane animates in without a
     * manual refresh.
     *
     * Props:
     *   host      — required hostname (must be registered)
     *   height    — chart height in pixels (default 180)
     */
    import { appState } from '../lib/state.svelte.js';
    import { fetchSpikeRange } from '../lib/api.js';
    import { formatTs } from '../lib/utils.js';

    /** @typedef {import('../lib/types.js').RecentSpike} RecentSpike */

    /** @type {{ host: string }} */
    let { host } = $props();

    // ── Window presets (matches 008 Overview shared-window semantics) ────
    const PRESETS = [
        { key: '15min', label: '15M', ms: 15 * 60 * 1000 },
        { key: '1hour', label: '1H', ms: 60 * 60 * 1000 },
        { key: '1day', label: '1D', ms: 24 * 60 * 60 * 1000 },
        { key: '3day', label: '3D', ms: 3 * 24 * 60 * 60 * 1000 },
        { key: '5day', label: '5D', ms: 5 * 24 * 60 * 60 * 1000 },
    ];
    const DEFAULT_PRESET = '1day';
    const STORAGE_KEY = 'drainctl.spike-swimlane.preset';

    function readStoredPreset() {
        try {
            const raw = localStorage.getItem(STORAGE_KEY);
            return PRESETS.some((p) => p.key === raw) ? raw : DEFAULT_PRESET;
        } catch {
            return DEFAULT_PRESET;
        }
    }
    function writeStoredPreset(key) {
        try {
            localStorage.setItem(STORAGE_KEY, key);
        } catch {}
    }

    let presetKey = $state(readStoredPreset());
    let presetMs = $derived(PRESETS.find((p) => p.key === presetKey)?.ms ?? PRESETS[2].ms);

    // ── Data fetch ─────────────────────────────────────────────────────────
    /** @type {RecentSpike[]} */
    let fetched = $state([]);
    let loading = $state(false);
    let error = $state('');
    /** @type {AbortController | null} */
    let inflight = null;

    let windowTo = $state(new Date());
    let windowFrom = $derived(new Date(windowTo.getTime() - presetMs));

    async function load() {
        if (!host) return;
        if (inflight) inflight.abort();
        inflight = new AbortController();
        loading = true;
        error = '';
        const to = new Date();
        windowTo = to;
        const from = new Date(to.getTime() - presetMs);
        try {
            const data = await fetchSpikeRange(host, from, to, inflight.signal);
            fetched = data;
        } catch (e) {
            if (e instanceof DOMException && e.name === 'AbortError') return;
            error = /** @type {Error} */ (e)?.message ?? String(e);
            fetched = [];
        } finally {
            loading = false;
        }
    }

    // Reload on host change or preset change.
    let lastKey = '';
    $effect(() => {
        const key = `${host}|${presetKey}`;
        if (key === lastKey) return;
        lastKey = key;
        load();
    });

    // Cleanup on unmount.
    $effect(() => {
        return () => {
            if (inflight) inflight.abort();
        };
    });

    function selectPreset(key) {
        presetKey = key;
        writeStoredPreset(key);
    }

    // ── Data shaping ───────────────────────────────────────────────────────
    // Merge REST fetch with SSE live appends. De-dupe on id so a spike arriving
    // via SSE after our range fetch doesn't appear twice.
    let mergedSpikes = $derived.by(() => {
        const live = appState.recentSpikes.get(host) ?? [];
        const byId = new Map();
        for (const s of fetched) byId.set(s.id, s);
        for (const s of live) byId.set(s.id, s); // SSE wins on tie — it's fresher
        const fromMs = windowFrom.getTime();
        const toMs = windowTo.getTime();
        const out = [];
        for (const s of byId.values()) {
            const t = Date.parse(s.window_end);
            if (!Number.isFinite(t)) continue;
            if (t < fromMs || t > toMs) continue;
            out.push({ ...s, _t: t });
        }
        out.sort((a, b) => a._t - b._t);
        return out;
    });

    // Derive channel lanes from the visible data. Newest spike's channel gets
    // the top lane so operators notice "what just fired" first.
    let channels = $derived.by(() => {
        const order = new Set();
        for (let i = mergedSpikes.length - 1; i >= 0; i--) {
            order.add(mergedSpikes[i].channel);
        }
        return Array.from(order);
    });

    // Per-channel latest spike for the lane label suffix. mergedSpikes is sorted
    // ascending by _t, so walking forward and overwriting leaves the newest
    // entry per channel.
    let lastByChannel = $derived.by(() => {
        const m = new Map();
        for (const s of mergedSpikes) m.set(s.channel, s);
        return m;
    });
    // Overall latest spike across every channel in the visible window — drives
    // the card-header "LAST SPIKE" summary.
    let lastOverall = $derived(mergedSpikes.length > 0 ? mergedSpikes[mergedSpikes.length - 1] : null);

    /** Compact timestamp suitable for inline label suffixes. */
    function fmtShortTs(ms) {
        const d = new Date(ms);
        const now = Date.now();
        const sameDay = new Date(now).toDateString() === d.toDateString();
        if (sameDay) {
            return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
        }
        const mmdd = d.toLocaleDateString(undefined, { month: '2-digit', day: '2-digit' });
        const hhmm = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
        return `${mmdd} ${hhmm}`;
    }
    function fmtVolume(n) {
        if (!Number.isFinite(n)) return '—';
        if (n >= 10_000) return (n / 1000).toFixed(n >= 100_000 ? 0 : 1) + 'k';
        return Math.round(n).toLocaleString();
    }

    // Channel labels render the raw channel name as supplied by the backend.
    // Overflow is handled by CSS ellipsis on the label strip; the full path
    // is always available via the label's `title` attribute on hover.

    // Detector status chip: displays the live evtspike DetectorStatus from the
    // shared SSE-seeded store so the operator can tell at a glance whether
    // the detector is healthy, still warming up, disabled, or errored.
    const EVT_LABEL = { healthy: 'Healthy', training: 'Training', disabled: 'Disabled', error: 'Error' };
    let detectorStatus = $derived(appState.detectorStatuses.get(host));
    function evtTitle(ds) {
        const lines = [`Evtspike: ${EVT_LABEL[ds.state] ?? ds.state}`];
        if (ds.state === 'error' && ds.error_reason) lines.push(ds.error_reason);
        if (ds.enabled_channels > 0) {
            lines.push(`${ds.mature_channels}/${ds.enabled_channels} channels mature`);
        }
        if (ds.last_spike_at) lines.push(`Last spike: ${new Date(ds.last_spike_at).toLocaleString()}`);
        return lines.join('\n');
    }

    // ── Layout math ────────────────────────────────────────────────────────
    /** @type {HTMLElement | undefined} */
    let canvasEl;
    let canvasWidth = $state(0);

    $effect(() => {
        if (!canvasEl) return;
        const ro = new ResizeObserver((entries) => {
            for (const e of entries) canvasWidth = e.contentRect.width;
        });
        ro.observe(canvasEl);
        return () => ro.disconnect();
    });

    // Left gutter is small — channel labels sit above each lane rather than in
    // a side gutter, so even long Windows channel paths have the full chart
    // width to breathe.
    const MARGIN = { top: 14, right: 16, bottom: 30, left: 16 };
    const LANE_TOP_PAD = 10; // px of empty space at the top of each lane, above its label
    const LABEL_STRIP = 18; // px reserved for the label strip itself
    const LABEL_GAP = 8; // px between the label strip and the dot strip
    const DOT_STRIP_PAD = 6; // px inside the dot strip that dots never enter, top or bottom
    // Fixed lane height — chrome stays comfortable regardless of channel count,
    // and the chart just grows taller + scrolls when there are many lanes.
    const LANE_HEIGHT = 64;
    // Hard ceiling on the scrollable viewport inside the tile. Beyond this the
    // canvas scrolls vertically so a noisy host doesn't explode the expanded
    // row to the point where the CPU chart is off-screen.
    const MAX_VIEWPORT_HEIGHT = 600;
    // Collapsed canvas height when the window has no spikes or the fetch
    // errored — just enough to seat the status line without wasting space.
    const EMPTY_CANVAS_HEIGHT = 72;

    // Chart sizes to exactly the content: one lane per channel at a fixed
    // LANE_HEIGHT. One channel yields a compact 108-px chart; many channels
    // grow tall and scroll inside the MAX_VIEWPORT_HEIGHT-clipped canvas.
    let chartHeight = $derived.by(() => {
        const n = Math.max(1, channels.length);
        return MARGIN.top + MARGIN.bottom + n * LANE_HEIGHT;
    });

    // Canvas viewport: collapses on empty/error, caps at MAX_VIEWPORT_HEIGHT
    // otherwise. The SVG itself is `chartHeight` tall — content exceeding the
    // viewport scrolls.
    let canvasHeight = $derived.by(() => {
        if (error || mergedSpikes.length === 0) return EMPTY_CANVAS_HEIGHT;
        return Math.min(chartHeight, MAX_VIEWPORT_HEIGHT);
    });

    let innerWidth = $derived(Math.max(0, canvasWidth - MARGIN.left - MARGIN.right));
    let innerHeight = $derived(Math.max(0, chartHeight - MARGIN.top - MARGIN.bottom));
    // Lane height mostly tracks the LANE_HEIGHT constant, but when the prop
    // height is larger than the channel count requires (e.g. 2 channels on a
    // 300-px min), the remaining slack is distributed evenly.
    let laneHeight = $derived(channels.length > 0 ? innerHeight / channels.length : 0);

    function timeToX(t) {
        if (innerWidth <= 0) return 0;
        const fromMs = windowFrom.getTime();
        const span = windowTo.getTime() - fromMs;
        if (span <= 0) return 0;
        return MARGIN.left + ((t - fromMs) / span) * innerWidth;
    }
    function laneTop(i) {
        return MARGIN.top + i * laneHeight;
    }
    // Each lane stacks: LANE_TOP_PAD (breathing room above the label) →
    // LABEL_STRIP → LABEL_GAP → DOT_STRIP.
    function labelTopY(i) {
        return laneTop(i) + LANE_TOP_PAD;
    }
    function dotStripTop(i) {
        return laneTop(i) + LANE_TOP_PAD + LABEL_STRIP + LABEL_GAP;
    }
    function dotStripHeight() {
        return Math.max(0, laneHeight - LANE_TOP_PAD - LABEL_STRIP - LABEL_GAP);
    }
    function laneDotY(i) {
        return dotStripTop(i) + dotStripHeight() / 2;
    }

    // Max dot radius caps at 8 but can expand when the dot strip is roomy
    // (few channels + extra prop height). DOT_STRIP_PAD on both sides keeps
    // the largest dot from grazing the strip edges.
    let maxDotRadius = $derived.by(() => {
        const stripH = dotStripHeight();
        return Math.max(2.5, Math.min(8, (stripH - 2 * DOT_STRIP_PAD) / 2));
    });

    // Dot size: sqrt scale so Application (observed ~50) and Security
    // (observed ~50k) both produce visible dots that aren't hidden or
    // overwhelming.
    function dotRadius(observed) {
        const r = Math.sqrt(Math.max(1, observed)) * 0.55;
        return Math.min(maxDotRadius, Math.max(2.5, r));
    }

    // Severity bucket from observed/expected ratio. Subtle for "just over
    // baseline", amber for "notable", red for "this is what you paged me for".
    function severityColor(observed, expected) {
        const exp = Number(expected);
        const ratio = exp > 0 ? observed / exp : Infinity;
        if (ratio >= 5) return 'var(--color-red)';
        if (ratio >= 2) return 'var(--color-amber)';
        return 'var(--color-accent)';
    }

    // ── X-axis ticks ────────────────────────────────────────────────────────
    // 4–6 tick marks across the window, rounded to human-friendly intervals.
    let xTicks = $derived.by(() => {
        if (innerWidth <= 0) return [];
        const span = windowTo.getTime() - windowFrom.getTime();
        if (span <= 0) return [];
        // Pick an interval that yields ~5 ticks.
        const targetCount = 5;
        const rawStep = span / targetCount;
        // Round to a human step: 1m, 5m, 15m, 1h, 3h, 6h, 12h, 1d.
        const steps = [
            60_000, 5 * 60_000, 15 * 60_000, 30 * 60_000,
            60 * 60_000, 3 * 60 * 60_000, 6 * 60 * 60_000, 12 * 60 * 60_000,
            24 * 60 * 60_000, 2 * 24 * 60 * 60_000,
        ];
        const step = steps.find((s) => s >= rawStep) ?? steps[steps.length - 1];
        const fromMs = windowFrom.getTime();
        const toMs = windowTo.getTime();
        const startMs = Math.ceil(fromMs / step) * step;
        /** @type {{t:number,label:string}[]} */
        const ticks = [];
        for (let t = startMs; t <= toMs; t += step) {
            ticks.push({ t, label: formatTick(t, span) });
        }
        return ticks;
    });

    function formatTick(ms, span) {
        const d = new Date(ms);
        if (span <= 60 * 60 * 1000) {
            // <= 1h: show HH:MM:SS
            return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' });
        }
        if (span <= 24 * 60 * 60 * 1000) {
            // <= 1d: HH:MM
            return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
        }
        // multi-day: MM-DD HH:MM
        const mmdd = d.toLocaleDateString(undefined, { month: '2-digit', day: '2-digit' });
        const hhmm = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
        return `${mmdd} ${hhmm}`;
    }

    // Tick text-anchor picks based on proximity to the chart edges so the
    // leftmost tick doesn't render half of its "01:15:00 PM" off-screen.
    function tickAnchor(tickX) {
        const leftEdge = MARGIN.left;
        const rightEdge = MARGIN.left + innerWidth;
        const edgePad = 44;
        if (tickX < leftEdge + edgePad) return 'start';
        if (tickX > rightEdge - edgePad) return 'end';
        return 'middle';
    }

    // ── Hover tooltip ──────────────────────────────────────────────────────
    /** @type {RecentSpike | null} */
    let hovered = $state(null);
    let hoverX = $state(0);
    let hoverY = $state(0);

    function onDotEnter(spike, evt) {
        hovered = spike;
        hoverX = timeToX(spike._t);
        hoverY = laneY(channels.indexOf(spike.channel));
    }
    function onDotLeave() {
        hovered = null;
    }

    function fmtNum(n) {
        if (n == null || !Number.isFinite(n)) return '—';
        return n < 10 ? n.toFixed(1) : Math.round(n).toLocaleString();
    }
    function fmtProb(p) {
        if (p == null || !Number.isFinite(p)) return '—';
        if (p === 0) return '0';
        if (p < 1e-6) return p.toExponential(1);
        return p.toFixed(6);
    }
</script>

<div class="sw-wrap" style="height:{canvasHeight + 52}px">
    <div class="sw-controls">
        <div class="sw-left">
            {#if detectorStatus}
                <span
                    class="pill evt-chip evt-{detectorStatus.state}"
                    title={evtTitle(detectorStatus)}
                    aria-label="Event-log detector: {EVT_LABEL[detectorStatus.state] ?? detectorStatus.state}"
                >EVT {EVT_LABEL[detectorStatus.state] ?? detectorStatus.state}</span>
            {/if}
            {#if lastOverall}
                <span
                    class="sw-last-spike"
                    title="Last spike in this window: {formatTs(lastOverall.window_end)} · {lastOverall.observed.toLocaleString()} events on {lastOverall.channel}"
                >
                    <span class="sw-last-label">LAST SPIKE</span>
                    <span class="sw-last-time mono">{fmtShortTs(lastOverall._t)}</span>
                    <span class="sw-last-vol mono">{fmtVolume(lastOverall.observed)}</span>
                </span>
            {/if}
            {#if loading}
                <span class="sw-loading" aria-label="Loading">…</span>
            {/if}
        </div>
        <div class="sw-pills" role="group" aria-label="Time window">
            {#each PRESETS as p}
                <button
                    type="button"
                    class="btn-brutal sw-pill"
                    class:active={presetKey === p.key}
                    onclick={() => selectPreset(p.key)}
                    aria-pressed={presetKey === p.key}
                >
                    {p.label}
                </button>
            {/each}
        </div>
    </div>

    <div class="sw-canvas" bind:this={canvasEl} style="height:{canvasHeight}px">
        {#if error}
            <div class="sw-status err">Error: {error}</div>
        {:else if mergedSpikes.length === 0}
            <div class="sw-status">No spikes in this window.</div>
        {:else if canvasWidth > 0}
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            <svg width={canvasWidth} height={chartHeight} role="img" aria-label="Event spike swimlane for {host}">
                <!-- Alternating dot-strip backgrounds so rows are visually
                     distinct. Label strips stay on the card surface; only the
                     area where dots plot is tinted. -->
                {#each channels as _, i}
                    <rect
                        class="sw-lane-bg"
                        class:alt={i % 2 === 1}
                        x={MARGIN.left}
                        y={dotStripTop(i)}
                        width={innerWidth}
                        height={dotStripHeight()}
                    />
                {/each}

                <!-- X-axis gridlines + labels -->
                {#each xTicks as tk}
                    <line
                        class="sw-gridline"
                        x1={timeToX(tk.t)}
                        x2={timeToX(tk.t)}
                        y1={MARGIN.top}
                        y2={MARGIN.top + innerHeight}
                    />
                    <text
                        class="sw-tick-label"
                        x={timeToX(tk.t)}
                        y={MARGIN.top + innerHeight + 18}
                        text-anchor={tickAnchor(timeToX(tk.t))}
                    >
                        {tk.label}
                    </text>
                {/each}

                <!-- Spike dots — plotted in the dot strip below each lane's label -->
                {#each mergedSpikes as spike (spike.id)}
                    <circle
                        class="sw-dot"
                        cx={timeToX(spike._t)}
                        cy={laneDotY(channels.indexOf(spike.channel))}
                        r={dotRadius(spike.observed)}
                        fill={severityColor(spike.observed, spike.expected)}
                        onpointerenter={(e) => onDotEnter(spike, e)}
                        onpointerleave={onDotLeave}
                        onfocus={(e) => onDotEnter(spike, e)}
                        onblur={onDotLeave}
                        tabindex="0"
                        role="button"
                        aria-label="Spike on {spike.channel} at {formatTs(spike.window_end)}: {spike.observed} events vs expected {fmtNum(spike.expected)}"
                    />
                {/each}

                <!-- Hover marker line -->
                {#if hovered}
                    <line
                        class="sw-hover-line"
                        x1={hoverX}
                        x2={hoverX}
                        y1={MARGIN.top}
                        y2={MARGIN.top + innerHeight}
                    />
                {/if}
            </svg>

            <!-- Lane labels overlay — HTML so CSS ellipsis + native
                 title-attribute tooltip handle long channel paths. Labels sit
                 at the top of each lane so they get the full chart width. The
                 right side carries the lane's last-spike timestamp + volume so
                 operators can read "when did this channel last fire" without
                 hovering a dot. -->
            {#each channels as ch, i}
                {@const last = lastByChannel.get(ch)}
                <div
                    class="sw-lane-label"
                    style="top:{labelTopY(i)}px; left:{MARGIN.left}px; width:{innerWidth}px; height:{LABEL_STRIP}px"
                    title={last
                        ? `${ch}\nLast spike: ${formatTs(last.window_end)} · ${last.observed.toLocaleString()} events`
                        : ch}
                >
                    <span class="sw-lane-name">{ch}</span>
                    {#if last}
                        <span class="sw-lane-meta mono">
                            <span class="sw-lane-time">{fmtShortTs(last._t)}</span>
                            <span class="sw-lane-vol">{fmtVolume(last.observed)}</span>
                        </span>
                    {/if}
                </div>
            {/each}

            {#if hovered}
                <div
                    class="sw-tooltip"
                    style="left:{Math.min(hoverX + 12, canvasWidth - 220)}px; top:{Math.max(4, hoverY - 70)}px"
                    role="tooltip"
                >
                    <div class="sw-tt-row">
                        <span class="sw-tt-label">time</span>
                        <span class="mono">{formatTs(hovered.window_end)}</span>
                    </div>
                    <div class="sw-tt-row">
                        <span class="sw-tt-label">observed</span>
                        <span class="mono">{hovered.observed.toLocaleString()}</span>
                    </div>
                    <div class="sw-tt-row">
                        <span class="sw-tt-label">expected</span>
                        <span class="mono">{fmtNum(hovered.expected)}</span>
                    </div>
                    <div class="sw-tt-row">
                        <span class="sw-tt-label">tail p</span>
                        <span class="mono">{fmtProb(hovered.tail_probability)}</span>
                    </div>
                </div>
            {/if}
        {/if}
    </div>
</div>

<style>
    .sw-wrap {
        display: flex;
        flex-direction: column;
        width: 100%;
        background: var(--color-surface);
        border-radius: var(--radius-default);
        user-select: none;
    }

    .sw-controls {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 16px;
        padding: 10px 14px 8px;
        flex-wrap: wrap;
    }
    .sw-left {
        display: flex;
        align-items: center;
        gap: 8px;
    }
    .sw-loading {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.6rem;
        color: var(--color-accent);
    }
    /* LAST SPIKE summary — sits next to the detector chip so operators get a
       glanceable "when did anything last fire, and how big" without having to
       pan the chart or read a tooltip. */
    .sw-last-spike {
        display: inline-flex;
        align-items: baseline;
        gap: 6px;
        padding-left: 4px;
    }
    .sw-last-label {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.55rem;
        font-weight: 700;
        letter-spacing: 0.1em;
        text-transform: uppercase;
        color: var(--color-muted);
    }
    .sw-last-time {
        font-size: 0.7rem;
        font-weight: 700;
        color: var(--color-fg);
    }
    .sw-last-vol {
        font-size: 0.65rem;
        font-weight: 700;
        color: var(--color-accent);
    }
    /* Detector-state chip — moved out of the status column in feature 009 so
       the main table's status cell isn't fighting two pills for space. */
    .evt-chip {
        background: transparent;
        border: 1.5px solid currentColor;
        padding: 1px 6px;
        font-size: 0.55rem;
        letter-spacing: 0.08em;
    }
    .evt-chip.evt-healthy {
        color: var(--color-green);
    }
    .evt-chip.evt-training {
        color: var(--color-amber);
    }
    .evt-chip.evt-disabled {
        color: var(--color-subtle);
    }
    .evt-chip.evt-error {
        color: var(--color-red);
    }
    .sw-pills {
        display: flex;
        gap: 6px;
    }
    .sw-pill {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.6rem;
        font-weight: 700;
        padding: 3px 8px;
        color: var(--color-muted);
        letter-spacing: 0.06em;
    }
    .sw-pill.active {
        background: var(--color-accent);
        color: #fff;
        border-color: var(--color-accent);
    }

    .sw-canvas {
        position: relative;
        flex: 1;
        min-height: 0;
        overflow-y: auto;
    }

    .sw-status {
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
    .sw-status.err {
        color: var(--color-red);
    }


    /* Lane label sits at the top of its lane, above the dot strip. Left-
       aligned so long channel paths flow naturally rather than fighting a
       fixed-width right-aligned gutter. Clipped with ellipsis on extreme
       hostnames; full path surfaces via the title attribute on hover. */
    /* Alternating dot-strip backgrounds. Both rows use the border token for
       fill so they adapt to light and dark themes; the two opacity steps keep
       them clearly distinguishable from each other and from the card surface
       they sit on. */
    .sw-lane-bg {
        fill: var(--color-border);
        opacity: 0.08;
    }
    .sw-lane-bg.alt {
        opacity: 0.2;
    }

    .sw-lane-label {
        position: absolute;
        display: flex;
        align-items: center;
        gap: 10px;
        padding: 0 10px;
        font-family: 'JetBrains Mono', monospace;
        font-size: 11px;
        color: var(--color-muted);
        text-transform: uppercase;
        letter-spacing: 0.06em;
        pointer-events: auto;
        box-sizing: border-box;
    }
    .sw-lane-name {
        flex: 1 1 auto;
        min-width: 0;
        overflow: hidden;
        white-space: nowrap;
        text-overflow: ellipsis;
    }
    .sw-lane-meta {
        flex: 0 0 auto;
        display: inline-flex;
        gap: 8px;
        align-items: baseline;
        font-size: 10.5px;
        color: var(--color-subtle);
        text-transform: none;
        letter-spacing: 0;
    }
    .sw-lane-time {
        color: var(--color-fg);
        font-weight: 700;
    }
    .sw-lane-vol {
        color: var(--color-accent);
        font-weight: 700;
    }

    .sw-gridline {
        stroke: var(--color-border);
        stroke-width: 1;
        stroke-dasharray: 1 5;
        opacity: 0.18;
    }

    .sw-tick-label {
        font-family: 'JetBrains Mono', monospace;
        font-size: 11px;
        fill: var(--color-subtle);
    }

    .sw-dot {
        stroke: var(--color-border);
        stroke-width: 1.5;
        cursor: pointer;
        transition: filter 0.15s ease;
    }
    .sw-dot:hover,
    .sw-dot:focus {
        filter: brightness(1.15) drop-shadow(0 0 3px currentColor);
        outline: none;
    }

    .sw-hover-line {
        stroke: var(--color-border);
        stroke-width: 1;
        pointer-events: none;
    }

    .sw-tooltip {
        position: absolute;
        min-width: 200px;
        padding: 8px 10px;
        background: var(--color-card);
        border: 2px solid var(--color-border);
        border-radius: var(--radius-default);
        box-shadow: 3px 3px 0 var(--color-shadow);
        font-family: 'JetBrains Mono', monospace;
        font-size: 11px;
        pointer-events: none;
        z-index: 10;
    }
    .sw-tt-row {
        display: flex;
        justify-content: space-between;
        gap: 12px;
        line-height: 1.5;
    }
    .sw-tt-label {
        color: var(--color-subtle);
        text-transform: uppercase;
        font-size: 10px;
        letter-spacing: 0.04em;
    }

    .mono {
        font-family: 'JetBrains Mono', monospace;
    }
</style>
