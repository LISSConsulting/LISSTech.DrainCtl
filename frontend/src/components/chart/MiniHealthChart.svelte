<script>
  /**
   * HealthIndicatorChart — full-size interactive chart for a single P95 health metric.
   *
   * Replaces the old sparkline-style mini chart. Each instance renders its own card
   * with a prominent current-value display, a full area chart with threshold zones,
   * and a hover crosshair that synchronizes with all other charts via
   * appState.hoveredChartIndex.
   *
   * Threshold lines use a dark dashed style (var(--color-fg)) so they are always
   * visible regardless of the area fill colour beneath them. Zone backgrounds
   * (green / amber / red tints) provide the colour coding.
   *
   * @typedef {{ time: number, [key: string]: number }} MetricPoint
   */

  import { appState } from '../../lib/state.svelte.js';

  /** @type {{ history: MetricPoint[], valueKey: string, p50Key?: string, label: string, unit: string, thresholds: {warn:number,crit:number}, color: string, fmt: (v:number)=>string, icon?: import('svelte').Component, axisRight?: boolean }} */
  let { history, valueKey, p50Key, label, unit, thresholds, color, fmt, icon, axisRight = false } = $props();

  // ── SVG geometry ──────────────────────────────────────────────────────────
  const CH = 180;          // chart inner height (px)
  let PL = $derived(axisRight ? 8 : 44);   // left padding
  let PR = $derived(axisRight ? 44 : 8);   // right padding
  const PT = 8;            // top padding
  const PB = 22;           // bottom padding — X-axis labels
  const TOTAL_H = PT + CH + PB;

  let containerW = $state(400);
  let cw = $derived(Math.max(containerW - PL - PR, 10));

  // ── Dynamic Y-axis scale ───────────────────────────────────────────────────
  // 125 % of the data peak, floored at 110 % of crit so zones stay legible.
  let scaleMax = $derived((() => {
    if (history.length === 0) return Math.max(thresholds.crit * 2, 1);
    const dataMax = Math.max(...history.map(h => (/** @type {any} */ (h))[valueKey] ?? 0), 0.01);
    return Math.max(dataMax * 1.25, thresholds.crit * 1.1);
  })());

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
    if (v >= 1000)  return `${Math.round(v / 100) / 10}k`;
    if (v >= 100)   return Math.round(v).toString();
    if (v >= 10)    return Math.round(v).toString();
    return (Math.round(v * 10) / 10).toString();
  }

  let yTicks = $derived(TICK_FRACS.map(f => {
    const val = f * scaleMax;
    return { val, yPos: ys(val, scaleMax), label: fmtTick(val), isEdge: f === 0 || f === 1 };
  }));

  // ── Area + line SVG paths ──────────────────────────────────────────────────
  let paths = $derived((() => {
    const n = history.length;
    if (n < 2) return { line: '', area: '' };
    const sMax = scaleMax;
    const pts = history.map((h, i) => ({
      x: xs(i, n),
      y: ys((/** @type {any} */ (h))[valueKey] ?? 0, sMax),
    }));
    const bY = yChartBot.toFixed(1);
    const line = pts.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(' ');
    const area = `${line} L${pts[n - 1].x.toFixed(1)},${bY} L${pts[0].x.toFixed(1)},${bY} Z`;
    return { line, area };
  })());

  // ── P50 area + line paths ──────────────────────────────────────────────────
  let p50Paths = $derived((() => {
    if (!p50Key) return { line: '', area: '' };
    const n = history.length;
    if (n < 2) return { line: '', area: '' };
    const sMax = scaleMax;
    const pts = history.map((h, i) => ({
      x: xs(i, n),
      y: ys((/** @type {any} */ (h))[p50Key] ?? 0, sMax),
    }));
    const bY = yChartBot.toFixed(1);
    const line = pts.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(' ');
    const area = `${line} L${pts[n - 1].x.toFixed(1)},${bY} L${pts[0].x.toFixed(1)},${bY} Z`;
    return { line, area };
  })());

  // ── X-axis time labels (5 evenly-spaced ticks) ────────────────────────────
  let xLabels = $derived((() => {
    const n = history.length;
    if (n < 2) return /** @type {{x:number,label:string}[]} */ ([]);
    const now = Date.now();
    const indices = [0, Math.floor((n-1)*0.25), Math.floor((n-1)*0.5), Math.floor((n-1)*0.75), n-1];
    const unique = [...new Set(indices)];
    return unique.map(i => {
      const diff = now - ((/** @type {any} */ (history[i]))?.time ?? now);
      const lbl = i === n - 1 ? 'now'
        : diff < 60_000 ? `${Math.round(diff / 1000)}s`
        : `${Math.round(diff / 60_000)}m`;
      return { x: xs(i, n), label: lbl };
    });
  })());

  // ── Current value + status colour ─────────────────────────────────────────
  // Show hovered point when any chart is hovered, latest otherwise.
  let currentValue = $derived((() => {
    const idx = appState.pinnedChartIndex ?? appState.hoveredChartIndex;
    const pt = idx !== null ? history[idx] : history[history.length - 1];
    return (/** @type {any} */ (pt))?.[valueKey] ?? 0;
  })());

  let valueColor = $derived(
    currentValue >= thresholds.crit ? 'var(--color-red)' :
    currentValue >= thresholds.warn ? 'var(--color-amber)' :
    'var(--color-green)'
  );

  // ── Synchronized hover ─────────────────────────────────────────────────────
  // Pinned index takes priority → then hover.
  let localHovering = $state(false);
  let displayIndex = $derived(appState.pinnedChartIndex ?? appState.hoveredChartIndex);

  /** @param {MouseEvent} e */
  function onMouseMove(e) {
    if (appState.pinnedChartIndex !== null) return;
    localHovering = true;
    const rect = /** @type {Element} */ (e.currentTarget).getBoundingClientRect();
    const mouseX = e.clientX - rect.left - PL;
    const n = history.length;
    if (n < 2) { appState.hoveredChartIndex = null; return; }
    const idx = Math.max(0, Math.min(n - 1, Math.round(mouseX / cw * (n - 1))));
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
    const idx = Math.max(0, Math.min(n - 1, Math.round(mouseX / cw * (n - 1))));
    appState.pinnedChartIndex = idx;
    appState.hoveredChartIndex = idx;
  }

  // Crosshair X pixel position.
  let crosshairX = $derived(
    displayIndex !== null && history.length >= 2
      ? xs(displayIndex, history.length)
      : null
  );

  // Value at crosshair for the tooltip.
  let hoverValue = $derived(
    displayIndex !== null
      ? ((/** @type {any} */ (history[displayIndex]))?.[valueKey] ?? null)
      : null
  );

  let hoverP50Value = $derived(
    displayIndex !== null && p50Key
      ? ((/** @type {any} */ (history[displayIndex]))?.[p50Key] ?? null)
      : null
  );

  // Tooltip layout constants.
  const TIP_W   = 170;
  const TIP_H   = 66;
  const TIP_PAD = 8;
</script>

<div class="hic-card">
  <!-- ── Card header: metric name + prominent current P95 value ── -->
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

  <!-- ── Chart area ── -->
  <div class="hic-chart" bind:clientWidth={containerW}>
    {#if history.length < 2}
      <div class="hic-placeholder">Collecting data… {history.length}/2</div>
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
        <!-- Red zone: values above crit -->
        <rect
          x={PL} y={yChartTop}
          width={cw} height={Math.max(0, yCrit - yChartTop)}
          fill="var(--color-red)" fill-opacity="0.09"
        />
        <!-- Amber zone: between warn and crit -->
        <rect
          x={PL} y={yCrit}
          width={cw} height={Math.max(0, yWarn - yCrit)}
          fill="var(--color-amber)" fill-opacity="0.09"
        />
        <!-- Green zone: below warn -->
        <rect
          x={PL} y={yWarn}
          width={cw} height={Math.max(0, yChartBot - yWarn)}
          fill="var(--color-green)" fill-opacity="0.09"
        />

        <!-- ── Y-axis labels (no grid lines) ── -->
        {#each yTicks as tick}
          {#if axisRight}
            <text
              x={PL + cw + 4} y={(tick.yPos + 3.5).toFixed(1)}
              class="ax" text-anchor="start"
            >{tick.label}</text>
          {:else}
            <text
              x={PL - 4} y={(tick.yPos + 3.5).toFixed(1)}
              class="ax" text-anchor="end"
            >{tick.label}</text>
          {/if}
        {/each}

        <!-- ── Axis borders ── -->
        {#if axisRight}
          <line x1={PL + cw} y1={yChartTop} x2={PL + cw} y2={yChartBot}
            stroke="var(--color-border)" stroke-width="2" opacity="0.7" />
        {:else}
          <line x1={PL} y1={yChartTop} x2={PL} y2={yChartBot}
            stroke="var(--color-border)" stroke-width="2" opacity="0.7" />
        {/if}
        <line x1={PL} y1={yChartBot} x2={PL + cw} y2={yChartBot}
          stroke="var(--color-border)" stroke-width="2" opacity="0.7" />

        <!-- ── P95 area fill (lighter when P50 is present) ── -->
        {#if paths.area}
          <path d={paths.area} fill={color} fill-opacity={p50Paths.area ? 0.4 : 1} />
        {/if}

        <!-- ── P50 area fill (solid, sits inside P95 envelope) ── -->
        {#if p50Paths.area}
          <path d={p50Paths.area} fill={color} fill-opacity="1" />
        {/if}

        <!-- ── P95 stroke line ── -->
        {#if paths.line}
          <path
            d={paths.line}
            fill="none"
            stroke-linejoin="round"
            stroke-linecap="round"
            style="stroke: color-mix(in srgb, {color} 95%, black); stroke-width: 3.5"
          />
        {/if}

        <!-- ── P50 stroke line ── -->
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
        <line
          x1={PL} y1={yCrit.toFixed(1)} x2={PL + cw} y2={yCrit.toFixed(1)}
          stroke="var(--color-fg)" stroke-width="1.5"
          stroke-dasharray="6,4" opacity="0.35"
        />
        <text x={PL + cw / 2} y={(yCrit - 3).toFixed(1)} class="thresh-label" opacity="0.3">CRIT</text>
        <line
          x1={PL} y1={yWarn.toFixed(1)} x2={PL + cw} y2={yWarn.toFixed(1)}
          stroke="var(--color-fg)" stroke-width="1.5"
          stroke-dasharray="6,4" opacity="0.25"
        />
        <text x={PL + cw / 2} y={(yWarn - 3).toFixed(1)} class="thresh-label" opacity="0.2">WARN</text>

        <!-- ── Synchronized crosshair ── -->
        {#if crosshairX !== null}
          <!-- Vertical line — visible on all synced charts when any one is hovered -->
          <line
            x1={crosshairX.toFixed(1)} y1={yChartTop}
            x2={crosshairX.toFixed(1)} y2={yChartBot}
            stroke="var(--color-fg)" stroke-width="1.5"
            stroke-dasharray="3,3" opacity="0.55"
          />

          <!-- Data dot on P95 line -->
          {#if hoverValue !== null}
            {@const dotY = ys(hoverValue, scaleMax)}
            <circle
              cx={crosshairX.toFixed(1)} cy={dotY.toFixed(1)} r="5"
              fill={color} stroke="var(--color-bg)" stroke-width="2.5"
            />
          {/if}

          <!-- Data dot on P50 line -->
          {#if hoverP50Value !== null}
            {@const dotY = ys(hoverP50Value, scaleMax)}
            <circle
              cx={crosshairX.toFixed(1)} cy={dotY.toFixed(1)} r="4"
              fill={color} stroke="var(--color-bg)" stroke-width="2"
              opacity="0.7"
            />
          {/if}

          <!-- Tooltip — visible on all synced charts when any one is hovered -->
          {#if hoverValue !== null && displayIndex !== null}
            {@const tipX = crosshairX + 14 + TIP_W + 4 > PL + cw
              ? crosshairX - TIP_W - 10
              : crosshairX + 14}
            {@const tipY = Math.max(
              yChartTop + 2,
              Math.min(yChartBot - TIP_H - 8, ys(scaleMax / 2, scaleMax) - TIP_H / 2)
            )}
            {@const diffMs = Date.now() - ((/** @type {any} */ (history[displayIndex]))?.time ?? Date.now())}
            {@const timeStr = diffMs < 1200 ? 'now'
              : diffMs < 60_000 ? `${Math.round(diffMs / 1000)}s ago`
              : `${Math.round(diffMs / 60_000)}m ago`}

            <!-- Shadow (neobrutalist offset) -->
            <rect x={tipX + 4} y={tipY + 4} width={TIP_W} height={TIP_H} rx="6"
              fill="var(--color-shadow)" />
            <!-- Card -->
            <rect x={tipX} y={tipY} width={TIP_W} height={TIP_H} rx="6"
              fill="var(--color-card)" stroke="var(--color-border)" stroke-width="2.5" />
            <!-- Divider below time header -->
            <line
              x1={tipX + TIP_PAD} y1={tipY + 18}
              x2={tipX + TIP_W - TIP_PAD} y2={tipY + 18}
              stroke="var(--color-border)" stroke-width="1" opacity="0.25"
            />
            <!-- Time stamp -->
            <text x={tipX + TIP_PAD} y={tipY + 13} class="tip-time">{timeStr}</text>
            <!-- P95 value -->
            <text x={tipX + TIP_PAD} y={tipY + 35} class="tip-val">
              P95: <tspan font-weight="700" fill={color}>{fmt(hoverValue)}</tspan>
            </text>
            <!-- P50 value -->
            {#if hoverP50Value !== null}
              <text x={tipX + TIP_PAD} y={tipY + 51} class="tip-val">
                P50: <tspan font-weight="700" fill={color} opacity="0.6">{fmt(hoverP50Value)}</tspan>
              </text>
            {/if}
          {/if}
        {/if}

        <!-- ── X-axis time labels ── -->
        {#each xLabels as xl}
          <text
            x={xl.x.toFixed(1)} y={(yChartBot + 15).toFixed(1)}
            class="ax x-ax" text-anchor="middle"
          >{xl.label}</text>
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

  /* Prominent current P95 — coloured by threshold status */
  .hic-current {
    font-family: 'JetBrains Mono', monospace;
    font-size: 1.05rem;
    font-weight: 700;
    letter-spacing: -0.02em;
    line-height: 1;
    transition: color 0.3s ease;
    white-space: nowrap;
  }

  .hic-chart {
    flex: 1;
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
