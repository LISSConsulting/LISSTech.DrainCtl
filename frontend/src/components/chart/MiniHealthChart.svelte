<script>
    /**
     * HealthIndicatorChart — full-size interactive chart for a single health metric.
     *
     * Supports normal thresholds (lower = good, e.g. latency) and inverted thresholds
     * (higher = good, e.g. FPS, quality %). Threshold zones and value colouring
     * automatically flip when `invertThresholds` is true.
     *
     * Session and RemoteFX history use `ts` as their timestamp field; pass
     * `timeKey="ts"` to override the default `"time"`.
     *
     * Pass a `transform` function to convert raw values before display
     * (e.g. bytes → MB for session memory).
     *
     * @typedef {{ [key: string]: number }} MetricPoint
     */

    import { appState } from '../../lib/state.svelte.js';

    /**
     * @type {{
     *   history: MetricPoint[],
     *   valueKey: string,
     *   p50Key?: string,
     *   label: string,
     *   unit: string,
     *   thresholds: {warn:number, crit:number},
     *   color: string,
     *   fmt: (v:number)=>string,
     *   icon?: import('svelte').Component,
     *   axisRight?: boolean,
     *   timeKey?: string,
     *   invertThresholds?: boolean,
     *   valueLabel?: string,
     *   p50Label?: string,
     *   transform?: (v:number)=>number,
     *   noThresholdZones?: boolean,
     *   autoScale?: boolean,
     * }}
     */
    let {
        history,
        valueKey,
        p50Key,
        label,
        unit,
        thresholds,
        color,
        fmt,
        icon,
        axisRight = false,
        timeKey = 'time',
        invertThresholds = false,
        valueLabel = 'P95',
        p50Label = 'P50',
        transform = (v) => v,
        noThresholdZones = false,
        autoScale = false,
        helpText = '',
        showHelp = false,
        fmtYTick = /** @type {((v:number)=>string)|null} */ (null),
    } = $props();

    // ── SVG geometry ──────────────────────────────────────────────────────────
    const CH = 180; // chart inner height (px)
    let PL = $derived(axisRight ? 8 : 44); // left padding
    let PR = $derived(axisRight ? 44 : 8); // right padding
    const PT = 8; // top padding
    const PB = 22; // bottom padding — X-axis labels
    const TOTAL_H = PT + CH + PB;

    let containerW = $state(400);
    let cw = $derived(Math.max(containerW - PL - PR, 10));

    // ── Dynamic Y-axis scale ───────────────────────────────────────────────────
    // For normal metrics: 125% of peak, floored at 110% of crit.
    // For inverted metrics: 125% of peak, floored at 150% of warn.
    // For autoScale (no natural threshold): 125% of peak only.
    let scaleMax = $derived(
        (() => {
            if (history.length === 0) {
                if (autoScale) return 1;
                if (invertThresholds) return Math.max(thresholds.warn * 2, 1);
                return Math.max(thresholds.crit * 2, 1);
            }
            const dataMax = Math.max(...history.map((h) => transform(/** @type {any} */ (h)[valueKey] ?? 0)), 0.01);
            if (autoScale) return Math.max(dataMax * 1.25, 1);
            if (invertThresholds) return Math.max(dataMax * 1.25, thresholds.warn * 1.5);
            return Math.max(dataMax * 1.25, thresholds.crit * 1.1);
        })(),
    );

    /** Map data index i → SVG X. */
    function xs(i, n) {
        return PL + (i / Math.max(n - 1, 1)) * cw;
    }

    /** Map value v → SVG Y (0 = top). */
    function ys(v, sMax) {
        return PT + (1 - Math.min(Math.max(v, 0), sMax) / sMax) * CH;
    }

    const yChartTop = PT;
    const yChartBot = PT + CH;
    let yCrit = $derived(ys(thresholds.crit, scaleMax));
    let yWarn = $derived(ys(thresholds.warn, scaleMax));

    // ── Y-axis ticks ──────────────────────────────────────────────────────────
    const TICK_FRACS = [0, 0.25, 0.5, 0.75, 1];

    function fmtTick(v) {
        if (v === 0) return '0';
        if (v >= 10000) return `${Math.round(v / 1000)}k`;
        if (v >= 1000) return `${Math.round(v / 100) / 10}k`;
        if (v >= 100) return Math.round(v).toString();
        if (v >= 10) return Math.round(v).toString();
        return (Math.round(v * 10) / 10).toString();
    }

    let yTicks = $derived(
        TICK_FRACS.map((f) => {
            const val = f * scaleMax;
            return {
                val,
                yPos: ys(val, scaleMax),
                label: fmtYTick ? fmtYTick(val) : fmtTick(val),
                isEdge: f === 0 || f === 1,
            };
        }),
    );

    // ── Area + line SVG paths (transform applied) ─────────────────────────────
    let paths = $derived(
        (() => {
            const n = history.length;
            if (n < 2) return { line: '', area: '' };
            const sMax = scaleMax;
            const pts = history.map((h, i) => ({
                x: xs(i, n),
                y: ys(transform(/** @type {any} */ (h)[valueKey] ?? 0), sMax),
            }));
            const bY = yChartBot.toFixed(1);
            const line = pts.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(' ');
            const area = `${line} L${pts[n - 1].x.toFixed(1)},${bY} L${pts[0].x.toFixed(1)},${bY} Z`;
            return { line, area };
        })(),
    );

    // ── P50 area + line paths (transform applied) ─────────────────────────────
    let p50Paths = $derived(
        (() => {
            if (!p50Key) return { line: '', area: '' };
            const n = history.length;
            if (n < 2) return { line: '', area: '' };
            const sMax = scaleMax;
            const pts = history.map((h, i) => ({
                x: xs(i, n),
                y: ys(transform(/** @type {any} */ (h)[p50Key] ?? 0), sMax),
            }));
            const bY = yChartBot.toFixed(1);
            const line = pts.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(' ');
            const area = `${line} L${pts[n - 1].x.toFixed(1)},${bY} L${pts[0].x.toFixed(1)},${bY} Z`;
            return { line, area };
        })(),
    );

    /** Short datetime format based on the visible span; matches the shared
     * formatter used by DualAxisChart and InteractiveTimeChart so every
     * Overview chart's x-axis reads the same. */
    function formatAxisTime(ms, spanMs) {
        const d = new Date(ms);
        if (spanMs < 2 * 60 * 60 * 1000) {
            return d.toLocaleTimeString('en', { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });
        }
        if (spanMs < 48 * 60 * 60 * 1000) {
            return d.toLocaleTimeString('en', { hour: '2-digit', minute: '2-digit', hour12: false });
        }
        if (spanMs < 14 * 24 * 60 * 60 * 1000) {
            return d.toLocaleDateString('en', { month: 'short', day: 'numeric' });
        }
        return d.toLocaleDateString('en', { month: 'short', day: 'numeric', year: '2-digit' });
    }

    // ── X-axis time labels (5 evenly-spaced ticks, using timeKey) ─────────────
    let xLabels = $derived(
        (() => {
            const n = history.length;
            if (n < 2) return /** @type {{x:number,label:string}[]} */ ([]);
            const indices = [
                0,
                Math.floor((n - 1) * 0.25),
                Math.floor((n - 1) * 0.5),
                Math.floor((n - 1) * 0.75),
                n - 1,
            ];
            const unique = [...new Set(indices)];
            const firstT = /** @type {any} */ (history[0])?.[timeKey];
            const lastT = /** @type {any} */ (history[n - 1])?.[timeKey];
            const spanMs = Number.isFinite(firstT) && Number.isFinite(lastT) ? lastT - firstT : 0;
            return unique.map((i) => {
                const t = /** @type {any} */ (history[i])?.[timeKey];
                const label = Number.isFinite(t) ? formatAxisTime(t, spanMs) : '';
                return { x: xs(i, n), label };
            });
        })(),
    );

    // ── Current value + status colour ─────────────────────────────────────────
    let currentValue = $derived(
        (() => {
            const idx = appState.pinnedChartIndex ?? appState.hoveredChartIndex;
            const pt = idx !== null ? history[idx] : history[history.length - 1];
            return transform(/** @type {any} */ (pt)?.[valueKey] ?? 0);
        })(),
    );

    let valueColor = $derived(
        noThresholdZones
            ? color
            : invertThresholds
              ? currentValue <= thresholds.crit
                  ? 'var(--color-red)'
                  : currentValue <= thresholds.warn
                    ? 'var(--color-amber)'
                    : 'var(--color-green)'
              : currentValue >= thresholds.crit
                ? 'var(--color-red)'
                : currentValue >= thresholds.warn
                  ? 'var(--color-amber)'
                  : 'var(--color-green)',
    );

    // ── Synchronized hover ─────────────────────────────────────────────────────
    let localHovering = $state(false);
    let displayIndex = $derived(appState.pinnedChartIndex ?? appState.hoveredChartIndex);

    /** @param {MouseEvent} e */
    function onMouseMove(e) {
        if (appState.pinnedChartIndex !== null) return;
        localHovering = true;
        const rect = /** @type {Element} */ (e.currentTarget).getBoundingClientRect();
        const mouseX = e.clientX - rect.left - PL;
        const n = history.length;
        if (n < 2) {
            appState.hoveredChartIndex = null;
            return;
        }
        const idx = Math.max(0, Math.min(n - 1, Math.round((mouseX / cw) * (n - 1))));
        appState.hoveredChartIndex = idx;
    }

    function onMouseLeave() {
        if (appState.pinnedChartIndex !== null) return;
        localHovering = false;
        appState.hoveredChartIndex = null;
    }

    /** @param {MouseEvent} e */
    function onClick(e) {
        if (appState.pinnedChartIndex !== null) {
            appState.pinnedChartIndex = null;
            appState.hoveredChartIndex = null;
            return;
        }
        const rect = /** @type {Element} */ (e.currentTarget).getBoundingClientRect();
        const mouseX = e.clientX - rect.left - PL;
        const n = history.length;
        if (n < 2) return;
        const idx = Math.max(0, Math.min(n - 1, Math.round((mouseX / cw) * (n - 1))));
        appState.pinnedChartIndex = idx;
        appState.hoveredChartIndex = idx;
    }

    // Crosshair X pixel position.
    let crosshairX = $derived(displayIndex !== null && history.length >= 2 ? xs(displayIndex, history.length) : null);

    // Value at crosshair for tooltip (transform applied).
    let hoverValue = $derived(
        (() => {
            if (displayIndex === null) return null;
            const raw = /** @type {any} */ (history[displayIndex])?.[valueKey];
            return raw != null ? transform(raw) : null;
        })(),
    );

    let hoverP50Value = $derived(
        (() => {
            if (displayIndex === null || !p50Key) return null;
            const raw = /** @type {any} */ (history[displayIndex])?.[p50Key];
            return raw != null ? transform(raw) : null;
        })(),
    );

    // Tooltip layout constants.
    const TIP_W = 170;
    const TIP_H = 66;
    const TIP_PAD = 8;
</script>

<div class="hic-card">
    <!-- ── Card header: metric name + prominent current value ── -->
    <div class="hic-header">
        <div class="hic-title-row">
            {#if icon}{@const Icon = icon}<span class="hic-icon"><Icon size={12} strokeWidth={2.4} /></span>{/if}
            <span class="hic-label">{label}</span>
            {#if unit}<span class="hic-unit">{unit}</span>{/if}
        </div>
        <div class="hic-current" style="color: {valueColor}; padding-right: {PR}px">
            {fmt(currentValue)}
        </div>
    </div>

    <!-- ── Per-chart help text ── -->
    {#if helpText && showHelp}
        <p class="hic-help">{helpText}</p>
    {/if}

    <!-- ── Chart area ── -->
    <div class="hic-chart" bind:clientWidth={containerW}>
        {#if history.length < 2}
            <div class="hic-placeholder">No retained history for this window</div>
        {:else}
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            <svg
                width={containerW}
                height={TOTAL_H}
                class="hic-svg"
                aria-hidden="true"
                onmousemove={onMouseMove}
                onmouseleave={onMouseLeave}
                onclick={onClick}
                style="cursor: crosshair"
            >
                <!-- ── Threshold zone backgrounds ── -->
                {#if !noThresholdZones}
                    {#if invertThresholds}
                        <!-- Inverted: green on top (high = good), red on bottom (low = bad) -->
                        <rect
                            x={PL}
                            y={yChartTop}
                            width={cw}
                            height={Math.max(0, yWarn - yChartTop)}
                            fill="var(--color-green)"
                            fill-opacity="0.09"
                        />
                        <rect
                            x={PL}
                            y={yWarn}
                            width={cw}
                            height={Math.max(0, yCrit - yWarn)}
                            fill="var(--color-amber)"
                            fill-opacity="0.09"
                        />
                        <rect
                            x={PL}
                            y={yCrit}
                            width={cw}
                            height={Math.max(0, yChartBot - yCrit)}
                            fill="var(--color-red)"
                            fill-opacity="0.09"
                        />
                    {:else}
                        <!-- Normal: red on top (high = bad), green on bottom (low = good) -->
                        <rect
                            x={PL}
                            y={yChartTop}
                            width={cw}
                            height={Math.max(0, yCrit - yChartTop)}
                            fill="var(--color-red)"
                            fill-opacity="0.09"
                        />
                        <rect
                            x={PL}
                            y={yCrit}
                            width={cw}
                            height={Math.max(0, yWarn - yCrit)}
                            fill="var(--color-amber)"
                            fill-opacity="0.09"
                        />
                        <rect
                            x={PL}
                            y={yWarn}
                            width={cw}
                            height={Math.max(0, yChartBot - yWarn)}
                            fill="var(--color-green)"
                            fill-opacity="0.09"
                        />
                    {/if}
                {/if}

                <!-- ── Y-axis labels (no grid lines) ── -->
                {#each yTicks as tick}
                    {#if axisRight}
                        <text x={PL + cw + 4} y={(tick.yPos + 3.5).toFixed(1)} class="ax" text-anchor="start"
                            >{tick.label}</text
                        >
                    {:else}
                        <text x={PL - 4} y={(tick.yPos + 3.5).toFixed(1)} class="ax" text-anchor="end"
                            >{tick.label}</text
                        >
                    {/if}
                {/each}

                <!-- ── Axis borders ── -->
                {#if axisRight}
                    <line
                        x1={PL + cw}
                        y1={yChartTop}
                        x2={PL + cw}
                        y2={yChartBot}
                        stroke="var(--color-border)"
                        stroke-width="2"
                        opacity="0.7"
                    />
                {:else}
                    <line
                        x1={PL}
                        y1={yChartTop}
                        x2={PL}
                        y2={yChartBot}
                        stroke="var(--color-border)"
                        stroke-width="2"
                        opacity="0.7"
                    />
                {/if}
                <line
                    x1={PL}
                    y1={yChartBot}
                    x2={PL + cw}
                    y2={yChartBot}
                    stroke="var(--color-border)"
                    stroke-width="2"
                    opacity="0.7"
                />

                <!-- ── P95 area fill (lighter when secondary series present) ── -->
                {#if paths.area}
                    <path d={paths.area} fill={color} fill-opacity={p50Paths.area ? 0.4 : 1} />
                {/if}

                <!-- ── Secondary area fill (solid, sits inside primary envelope) ── -->
                {#if p50Paths.area}
                    <path d={p50Paths.area} fill={color} fill-opacity="1" />
                {/if}

                <!-- ── Primary stroke line ── -->
                {#if paths.line}
                    <path
                        d={paths.line}
                        fill="none"
                        stroke-linejoin="round"
                        stroke-linecap="round"
                        style="stroke: color-mix(in srgb, {color} 95%, black); stroke-width: 3.5"
                    />
                {/if}

                <!-- ── Secondary stroke line ── -->
                {#if p50Paths.line}
                    <path
                        d={p50Paths.line}
                        fill="none"
                        stroke-linejoin="round"
                        stroke-linecap="round"
                        style="stroke: color-mix(in srgb, {color} 85%, black); stroke-width: 3.5"
                    />
                {/if}

                <!-- ── Threshold marker lines with inline labels ── -->
                {#if !noThresholdZones}
                    <line
                        x1={PL}
                        y1={yCrit.toFixed(1)}
                        x2={PL + cw}
                        y2={yCrit.toFixed(1)}
                        stroke="var(--color-fg)"
                        stroke-width="1.5"
                        stroke-dasharray="6,4"
                        opacity="0.35"
                    />
                    <text x={PL + cw / 2} y={(yCrit - 3).toFixed(1)} class="thresh-label" opacity="0.3">CRIT</text>
                    <line
                        x1={PL}
                        y1={yWarn.toFixed(1)}
                        x2={PL + cw}
                        y2={yWarn.toFixed(1)}
                        stroke="var(--color-fg)"
                        stroke-width="1.5"
                        stroke-dasharray="6,4"
                        opacity="0.25"
                    />
                    <text x={PL + cw / 2} y={(yWarn - 3).toFixed(1)} class="thresh-label" opacity="0.2">WARN</text>
                {/if}

                <!-- ── Synchronized crosshair ── -->
                {#if crosshairX !== null}
                    <line
                        x1={crosshairX.toFixed(1)}
                        y1={yChartTop}
                        x2={crosshairX.toFixed(1)}
                        y2={yChartBot}
                        stroke="var(--color-fg)"
                        stroke-width="1.5"
                        stroke-dasharray="3,3"
                        opacity="0.55"
                    />

                    {#if hoverValue !== null}
                        {@const dotY = ys(hoverValue, scaleMax)}
                        <circle
                            cx={crosshairX.toFixed(1)}
                            cy={dotY.toFixed(1)}
                            r="5"
                            fill={color}
                            stroke="var(--color-bg)"
                            stroke-width="2.5"
                        />
                    {/if}

                    {#if hoverP50Value !== null}
                        {@const dotY = ys(hoverP50Value, scaleMax)}
                        <circle
                            cx={crosshairX.toFixed(1)}
                            cy={dotY.toFixed(1)}
                            r="4"
                            fill={color}
                            stroke="var(--color-bg)"
                            stroke-width="2"
                            opacity="0.7"
                        />
                    {/if}

                    {#if hoverValue !== null && displayIndex !== null}
                        {@const tipX =
                            crosshairX + 14 + TIP_W + 4 > PL + cw ? crosshairX - TIP_W - 10 : crosshairX + 14}
                        {@const tipY = Math.max(
                            yChartTop + 2,
                            Math.min(yChartBot - TIP_H - 8, ys(scaleMax / 2, scaleMax) - TIP_H / 2),
                        )}
                        {@const hoverT = /** @type {any} */ ((history[displayIndex])?.[timeKey])}
                        {@const timeStr = Number.isFinite(hoverT)
                            ? new Date(hoverT).toLocaleString('en', {
                                  month: 'short',
                                  day: 'numeric',
                                  hour: '2-digit',
                                  minute: '2-digit',
                                  second: '2-digit',
                                  hour12: false,
                              })
                            : '—'}

                        <rect
                            x={tipX + 4}
                            y={tipY + 4}
                            width={TIP_W}
                            height={TIP_H}
                            rx="6"
                            fill="var(--color-shadow)"
                        />
                        <rect
                            x={tipX}
                            y={tipY}
                            width={TIP_W}
                            height={TIP_H}
                            rx="6"
                            fill="var(--color-card)"
                            stroke="var(--color-border)"
                            stroke-width="2.5"
                        />
                        <line
                            x1={tipX + TIP_PAD}
                            y1={tipY + 18}
                            x2={tipX + TIP_W - TIP_PAD}
                            y2={tipY + 18}
                            stroke="var(--color-border)"
                            stroke-width="1"
                            opacity="0.25"
                        />
                        <text x={tipX + TIP_PAD} y={tipY + 13} class="tip-time">{timeStr}</text>
                        <text x={tipX + TIP_PAD} y={tipY + 35} class="tip-val">
                            {valueLabel}: <tspan font-weight="700" fill={color}>{fmt(hoverValue)}</tspan>
                        </text>
                        {#if hoverP50Value !== null}
                            <text x={tipX + TIP_PAD} y={tipY + 51} class="tip-val">
                                {p50Label}:
                                <tspan font-weight="700" fill={color} opacity="0.6">{fmt(hoverP50Value)}</tspan>
                            </text>
                        {/if}
                    {/if}
                {/if}

                <!-- ── X-axis time labels ── -->
                {#each xLabels as xl}
                    <text x={xl.x.toFixed(1)} y={(yChartBot + 15).toFixed(1)} class="ax x-ax" text-anchor="middle"
                        >{xl.label}</text
                    >
                {/each}
            </svg>
        {/if}
    </div>
</div>

<style>
    /* ── Card wrapper — neobrutalist ── */
    .hic-card {
        background: var(--color-card);
        padding: 14px 16px 10px;
        display: flex;
        flex-direction: column;
        min-width: 0;
    }

    /* ── Header: metric label left, current value right ── */
    .hic-header {
        display: flex;
        align-items: flex-start;
        justify-content: space-between;
        gap: 8px;
        margin-bottom: 10px;
    }

    .hic-title-row {
        display: flex;
        align-items: baseline;
        gap: 5px;
        padding-top: 3px;
    }

    .hic-icon {
        display: flex;
        align-items: center;
        color: var(--color-muted);
        opacity: 0.6;
    }

    .hic-label {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.6rem;
        font-weight: 700;
        letter-spacing: 0.14em;
        text-transform: uppercase;
        color: var(--color-muted);
    }

    .hic-unit {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.5rem;
        font-weight: 400;
        color: var(--color-muted);
        opacity: 0.6;
    }

    .hic-current {
        font-family: 'JetBrains Mono', monospace;
        font-size: 1.05rem;
        font-weight: 700;
        letter-spacing: -0.02em;
        line-height: 1;
        transition: color 0.12s linear;
        white-space: nowrap;
    }

    .hic-help {
        font-family: 'Work Sans', sans-serif;
        font-size: 0.7rem;
        line-height: 1.55;
        color: var(--color-muted);
        margin: 0 0 10px 0;
        max-width: 640px;
    }

    .hic-chart {
        margin-top: auto;
    }

    .hic-svg {
        display: block;
        overflow: visible;
    }

    .hic-placeholder {
        display: flex;
        align-items: center;
        justify-content: center;
        height: 210px;
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.75rem;
        color: var(--color-muted);
        opacity: 0.5;
    }

    /* ── Axis labels ── */
    .ax {
        font-family: 'JetBrains Mono', monospace;
        font-size: 10px;
        font-weight: 700;
        fill: var(--color-muted);
        user-select: none;
        pointer-events: none;
    }

    .x-ax {
        font-size: 9px;
        letter-spacing: 0.04em;
        text-transform: uppercase;
    }

    /* ── Threshold labels ── */
    .thresh-label {
        font-family: 'JetBrains Mono', monospace;
        font-size: 8px;
        font-weight: 700;
        letter-spacing: 0.14em;
        text-transform: uppercase;
        fill: var(--color-fg);
        text-anchor: middle;
        user-select: none;
        pointer-events: none;
    }

    /* ── Tooltip text ── */
    .tip-time {
        font-family: 'JetBrains Mono', monospace;
        font-size: 10px;
        font-weight: 700;
        letter-spacing: 0.05em;
        text-transform: uppercase;
        fill: var(--color-muted);
        pointer-events: none;
    }

    .tip-val {
        font-family: 'JetBrains Mono', monospace;
        font-size: 11px;
        fill: var(--color-muted);
        pointer-events: none;
    }
</style>
