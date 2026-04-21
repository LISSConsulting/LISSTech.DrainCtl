<script>
    /**
     * InteractiveTimeChart — single-series time chart with axes, crosshair,
     * tooltip, and zoom/pan-friendly rendering. Designed for the per-host
     * detail chart that swings across 1-minute to 365-day windows.
     *
     * The parent owns the visible window (viewFrom/viewTo) and passes it via
     * xDomain. Zoom and pan are wheel/drag handlers on the parent's wrapper
     * div, not here — this component only renders.
     *
     * Props:
     *   points   — [{ t: number (UnixMilli), v: number }] sorted by t ASC
     *   counter  — counter name (drives Y-axis formatting)
     *   color    — line / fill color (CSS var or hex)
     *   hideHover — when true (e.g. while parent is dragging), suppress the
     *               crosshair + dot + tooltip
     */
    import { getContext } from 'svelte';
    import { counterLabel, isPercentCounter } from '../../lib/utils.js';

    /** @type {{ points: {t:number,v:number}[], counter: string, color: string, hideHover?: boolean }} */
    let { points, counter, color, hideHover = false } = $props();

    const { xScale, yScale, xDomain, yDomain, width, height } = getContext('LayerCake');

    // ── Y-axis tick generation ───────────────────────────────────────────
    // Percent counters pin to 0/25/50/75/100. Everything else auto-scales to
    // the data domain LayerCake computed (yDomain is reactive).
    let isPct = $derived(isPercentCounter(counter));

    let yTicks = $derived.by(() => {
        if (isPct) return [0, 25, 50, 75, 100];
        const [y0, y1] = $yDomain;
        if (y1 == null || !Number.isFinite(y1)) return [];
        if (y1 === y0) return [y0];
        const step = (y1 - y0) / 4;
        return [0, 1, 2, 3, 4].map((i) => y0 + step * i);
    });

    // ── X-axis tick generation ───────────────────────────────────────────
    // Up to 5 evenly spaced timestamps across the visible window.
    let xTicks = $derived.by(() => {
        const [x0, x1] = $xDomain;
        if (x1 == null || x1 === x0) return [];
        const span = x1 - x0;
        const step = span / 4;
        return [0, 1, 2, 3, 4].map((i) => x0 + step * i);
    });

    /** @param {number} v */
    function formatY(v) {
        if (isPct) return `${Math.round(v)}%`;
        if (counter.endsWith('_ms')) return `${Math.round(v)}ms`;
        if (counter.endsWith('_mb')) return `${Math.round(v)} MB`;
        if (counter.endsWith('_bytes')) return `${(v / (1024 * 1024)).toFixed(1)} MB`;
        if (counter.endsWith('_sec')) return `${v.toFixed(1)}/s`;
        if (Math.abs(v) >= 100) return String(Math.round(v));
        return v.toFixed(1);
    }

    /** @param {number} ms */
    function formatXTick(ms) {
        const [x0, x1] = $xDomain;
        const span = x1 - x0;
        const d = new Date(ms);
        if (span < 2 * 60 * 60 * 1000) {
            return d.toLocaleTimeString('en', { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });
        }
        if (span < 48 * 60 * 60 * 1000) {
            return d.toLocaleTimeString('en', { hour: '2-digit', minute: '2-digit', hour12: false });
        }
        if (span < 14 * 24 * 60 * 60 * 1000) {
            return d.toLocaleDateString('en', { month: 'short', day: 'numeric' });
        }
        return d.toLocaleDateString('en', { month: 'short', day: 'numeric', year: '2-digit' });
    }

    /** @param {number} ms */
    function formatTooltipTime(ms) {
        const d = new Date(ms);
        return d.toLocaleString('en', {
            month: 'short',
            day: 'numeric',
            hour: '2-digit',
            minute: '2-digit',
            second: '2-digit',
            hour12: false,
        });
    }

    // ── Path geometry ────────────────────────────────────────────────────
    let paths = $derived.by(() => {
        if (!points || points.length < 2) return { line: '', area: '' };
        const px = points.map((p) => ({ x: $xScale(p.t), y: $yScale(p.v) }));
        const line = px
            .map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x.toFixed(1)},${p.y.toFixed(1)}`)
            .join(' ');
        const bottom = $height.toFixed(1);
        const first = px[0];
        const last = px[px.length - 1];
        const area = `${line} L${last.x.toFixed(1)},${bottom} L${first.x.toFixed(1)},${bottom} Z`;
        return { line, area };
    });

    // ── Hover state ──────────────────────────────────────────────────────
    // Nearest-point lookup by pixel-X on the capture overlay. Cleared when
    // the pointer leaves the chart or when the parent tells us to hide
    // (e.g. during a drag-pan — showing a tooltip mid-drag is disorienting).
    let hoverIdx = $state(/** @type {number|null} */ (null));

    /** @param {MouseEvent} e */
    function onMouseMove(e) {
        if (hideHover) return;
        const rect = /** @type {Element} */ (e.currentTarget).getBoundingClientRect();
        if (rect.width <= 0 || !points || points.length === 0) {
            hoverIdx = null;
            return;
        }
        const mouseX = e.clientX - rect.left;
        // Binary search would be faster, but sweeping through O(N) keeps the
        // code small and the typical sample count is < 1000 per visible window.
        let best = 0;
        let bestDist = Infinity;
        for (let i = 0; i < points.length; i++) {
            const px = $xScale(points[i].t);
            const d = Math.abs(px - mouseX);
            if (d < bestDist) {
                bestDist = d;
                best = i;
            }
        }
        hoverIdx = best;
    }

    function onMouseLeave() {
        hoverIdx = null;
    }

    // Hide crosshair while the parent is dragging (pan).
    $effect(() => {
        if (hideHover) hoverIdx = null;
    });

    // ── Tooltip layout ───────────────────────────────────────────────────
    const TIP_W = 180;
    const TIP_H = 48;
    const TIP_PAD = 8;
</script>

<!-- ── Y-axis labels + faint gridlines ── -->
{#each yTicks as tick}
    {@const y = $yScale(tick)}
    <line
        x1={0}
        y1={y.toFixed(1)}
        x2={$width}
        y2={y.toFixed(1)}
        stroke="var(--color-border)"
        stroke-width="0.5"
        opacity="0.18"
    />
    <text x={-6} y={(y + 3.5).toFixed(1)} class="ax" text-anchor="end">{formatY(tick)}</text>
{/each}

<!-- ── Axis borders (left + bottom) ── -->
<line x1={0} y1={0} x2={0} y2={$height} stroke="var(--color-border)" stroke-width="1.5" opacity="0.7" />
<line
    x1={0}
    y1={$height}
    x2={$width}
    y2={$height}
    stroke="var(--color-border)"
    stroke-width="1.5"
    opacity="0.7"
/>

<!-- ── Area fill + line stroke ── -->
{#if paths.line}
    <path d={paths.area} fill={color} fill-opacity="0.2" stroke="none" />
    <path
        d={paths.line}
        fill="none"
        stroke={color}
        stroke-width="1.5"
        stroke-linejoin="round"
        stroke-linecap="round"
    />
{/if}

<!-- ── X-axis labels ── -->
{#each xTicks as tick}
    <text x={$xScale(tick).toFixed(1)} y={($height + 16).toFixed(1)} class="ax x-ax" text-anchor="middle"
        >{formatXTick(tick)}</text
    >
{/each}

<!-- ── Hover crosshair + data point + tooltip ── -->
{#if !hideHover && hoverIdx !== null && points[hoverIdx]}
    {@const p = points[hoverIdx]}
    {@const cx = $xScale(p.t)}
    {@const cy = $yScale(p.v)}

    <line
        x1={cx.toFixed(1)}
        y1={0}
        x2={cx.toFixed(1)}
        y2={$height}
        stroke="var(--color-fg)"
        stroke-width="1.5"
        stroke-dasharray="3,3"
        opacity="0.5"
    />

    <circle
        cx={cx.toFixed(1)}
        cy={cy.toFixed(1)}
        r="4.5"
        fill={color}
        stroke="var(--color-border)"
        stroke-width="2"
    />

    {@const tx = cx + 12 + TIP_W > $width ? cx - TIP_W - 10 : cx + 12}
    {@const ty = Math.max(2, Math.min($height - TIP_H - 2, cy - TIP_H / 2))}

    <!-- Shadow offset (neobrutalist) -->
    <rect x={tx + 4} y={ty + 4} width={TIP_W} height={TIP_H} rx="4" fill="var(--color-shadow)" />

    <!-- Card -->
    <rect
        x={tx}
        y={ty}
        width={TIP_W}
        height={TIP_H}
        rx="4"
        fill="var(--color-card)"
        stroke="var(--color-border)"
        stroke-width="2"
    />

    <text x={tx + TIP_PAD} y={ty + 16} class="tip-time">{formatTooltipTime(p.t)}</text>
    <text x={tx + TIP_PAD} y={ty + 36} class="tip-val"
        >{counterLabel(counter)}: <tspan font-weight="700" fill={color}>{formatY(p.v)}</tspan></text
    >
{/if}

<!-- ── Mouse capture overlay — MUST be last so it sits on top ── -->
<!-- svelte-ignore a11y_no_static_element_interactions -->
<rect
    x={0}
    y={0}
    width={Math.max(0, $width)}
    height={Math.max(0, $height)}
    fill="transparent"
    style="cursor: crosshair"
    onmousemove={onMouseMove}
    onmouseleave={onMouseLeave}
/>

<style>
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
    .tip-time {
        font-family: 'JetBrains Mono', monospace;
        font-size: 9px;
        font-weight: 700;
        letter-spacing: 0.06em;
        text-transform: uppercase;
        fill: var(--color-muted);
        pointer-events: none;
    }
    .tip-val {
        font-family: 'JetBrains Mono', monospace;
        font-size: 11px;
        fill: var(--color-fg);
        pointer-events: none;
    }
</style>
