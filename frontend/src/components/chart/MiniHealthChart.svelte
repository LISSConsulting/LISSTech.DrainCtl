<script>
  /**
   * MiniHealthChart — self-contained mini chart for a single health metric.
   *
   * Visual style matches the Load chart: opaque area fill, same gridlines,
   * JetBrains Mono axis labels. No border or box-shadow — sits cleanly
   * inside the parent PERFORMANCE METRICS card.
   *
   * Y-axis scale is dynamic: 125% of the data peak seen in the current history,
   * with a floor of 110% of the crit threshold so zones are always visible.
   * This keeps the area fill at ~80% of chart height even during spikes.
   *
   * Threshold zones (green/amber/red) are background shading behind the fill:
   *   green  → normal operating range (below warn)
   *   amber  → warning zone (warn ↔ crit)
   *   red    → critical zone (above crit)
   *
   * @typedef {{ time: number, [key: string]: number }} MetricPoint
   */

  /** @type {{ history: MetricPoint[], valueKey: string, label: string, unit: string, yMax: number, thresholds: {warn:number,crit:number}, color: string, fmt: (v:number)=>string }} */
  let { history, valueKey, label, unit, yMax: _yMaxHint, thresholds, color, fmt } = $props();

  // ── SVG geometry ──────────────────────────────────────────────────────────
  const CH = 90;           // chart inner height (px)
  const PL = 36;           // left padding (Y-axis labels)
  const PR = 6;            // right padding
  const PT = 6;            // top padding
  const PB = 16;           // bottom padding (X-axis labels)
  const TOTAL_H = PT + CH + PB;  // 112

  let containerW = $state(220);

  // Chart inner width (reactive on container resize)
  let cw = $derived(Math.max(containerW - PL - PR, 10));

  // ── Dynamic Y-axis scale ───────────────────────────────────────────────────
  // 125% of data peak in current history, floored at 110% of crit threshold
  // so both the trend shape AND zone shading are always legible.
  let scaleMax = $derived((() => {
    if (history.length === 0) return Math.max(thresholds.crit * 2, 1);
    const dataMax = Math.max(
      ...history.map(h => (/** @type {any} */ (h))[valueKey] ?? 0),
      0.01,
    );
    return Math.max(dataMax * 1.25, thresholds.crit * 1.1);
  })());

  // ── Scale functions ────────────────────────────────────────────────────────
  /** Map data index i (0-based) to SVG X coordinate. */
  function xs(i, n) {
    return PL + (i / Math.max(n - 1, 1)) * cw;
  }

  /** Map value v to SVG Y coordinate (0=top). sMax is passed explicitly so
   *  Svelte's reactivity tracking sees the scaleMax dependency in callers. */
  function ys(v, sMax) {
    return PT + (1 - Math.min(Math.max(v, 0), sMax) / sMax) * CH;
  }

  // ── Fixed SVG Y positions ──────────────────────────────────────────────────
  // yChartTop/Bot are constants; yCrit/yWarn react to scaleMax.
  const yChartTop = PT;
  const yChartBot = PT + CH;
  let yCrit = $derived(ys(thresholds.crit, scaleMax));
  let yWarn = $derived(ys(thresholds.warn, scaleMax));

  // ── Y-axis ticks (5 ticks, same cadence as Load chart) ────────────────────
  const TICK_FRACS = [0, 0.25, 0.5, 0.75, 1];

  /** Compact axis label: avoid long decimals. */
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

  // ── X-axis time labels (3 ticks: oldest, midpoint, now) ───────────────────
  let xLabels = $derived((() => {
    const n = history.length;
    if (n < 2) return /** @type {{x:number,label:string}[]} */ ([]);
    const now = Date.now();
    return [0, Math.floor((n - 1) * 0.5), n - 1].map(i => {
      const diff = now - ((/** @type {any} */ (history[i]))?.time ?? now);
      const lbl = i === n - 1 ? 'now'
        : diff < 60_000 ? `${Math.round(diff / 1000)}s`
        : `${Math.round(diff / 60_000)}m`;
      return { x: xs(i, n), label: lbl };
    });
  })());

  // ── Current value + status colour ─────────────────────────────────────────
  let currentValue = $derived(
    (/** @type {any} */ (history[history.length - 1]))?.[valueKey] ?? 0
  );

  let valueColor = $derived(
    currentValue >= thresholds.crit ? 'var(--color-red)' :
    currentValue >= thresholds.warn ? 'var(--color-amber)' :
    'var(--color-green)'
  );
</script>

<div class="mini-wrap">
  <!-- ── Header: metric label + unit ── -->
  <div class="mini-header">
    <span class="mini-label">{label}</span>
    {#if unit}<span class="mini-unit">{unit}</span>{/if}
  </div>

  <!-- ── Prominent current P95 value ── -->
  <div class="mini-current" style="color: {valueColor}">
    {fmt(currentValue)}
  </div>

  <!-- ── Chart SVG ── -->
  <div class="mini-chart" bind:clientWidth={containerW}>
    {#if history.length < 2}
      <div class="mini-placeholder">Collecting data… {history.length}/2</div>
    {:else}
      <svg width={containerW} height={TOTAL_H} class="mini-svg" aria-hidden="true">
        <!-- ── Threshold zone backgrounds (behind everything) ── -->
        <!-- Red: values above crit (top strip, visible above area fill) -->
        <rect
          x={PL} y={yChartTop}
          width={cw} height={yCrit - yChartTop}
          fill="var(--color-red)" fill-opacity="0.12"
        />
        <!-- Amber: values between warn and crit -->
        <rect
          x={PL} y={yCrit}
          width={cw} height={yWarn - yCrit}
          fill="var(--color-amber)" fill-opacity="0.12"
        />
        <!-- Green: values below warn (visible when data is calm) -->
        <rect
          x={PL} y={yWarn}
          width={cw} height={yChartBot - yWarn}
          fill="var(--color-green)" fill-opacity="0.12"
        />

        <!-- ── Grid lines + Y-axis labels ── -->
        {#each yTicks as tick}
          <line
            x1={PL} y1={tick.yPos.toFixed(1)}
            x2={PL + cw} y2={tick.yPos.toFixed(1)}
            stroke="var(--color-border)"
            stroke-width="1"
            stroke-dasharray={tick.isEdge ? '' : '5,4'}
            opacity={tick.isEdge ? '0.6' : '0.38'}
          />
          <text
            x={PL - 4} y={(tick.yPos + 3.5).toFixed(1)}
            class="ax" text-anchor="end"
          >{tick.label}</text>
        {/each}

        <!-- ── Threshold marker lines ── -->
        <line
          x1={PL} y1={yCrit.toFixed(1)} x2={PL + cw} y2={yCrit.toFixed(1)}
          stroke="var(--color-red)" stroke-width="1" stroke-dasharray="3,2" opacity="0.55"
        />
        <line
          x1={PL} y1={yWarn.toFixed(1)} x2={PL + cw} y2={yWarn.toFixed(1)}
          stroke="var(--color-amber)" stroke-width="1" stroke-dasharray="3,2" opacity="0.55"
        />

        <!-- ── Axis borders (left + bottom) ── -->
        <line x1={PL} y1={yChartTop} x2={PL} y2={yChartBot}
          stroke="var(--color-border)" stroke-width="2" opacity="0.7" />
        <line x1={PL} y1={yChartBot} x2={PL + cw} y2={yChartBot}
          stroke="var(--color-border)" stroke-width="2" opacity="0.7" />

        <!-- ── Area fill (opaque, same as Load chart) ── -->
        {#if paths.area}
          <path d={paths.area} fill={color} fill-opacity="1" />
        {/if}

        <!-- ── Stroke line ── -->
        {#if paths.line}
          <path
            d={paths.line}
            stroke={color}
            stroke-width="3.5"
            fill="none"
            stroke-linejoin="round"
            stroke-linecap="round"
          />
        {/if}

        <!-- ── X-axis time labels ── -->
        {#each xLabels as xl}
          <text
            x={xl.x.toFixed(1)} y={(yChartBot + 12).toFixed(1)}
            class="ax x-ax" text-anchor="middle"
          >{xl.label}</text>
        {/each}
      </svg>
    {/if}
  </div>
</div>

<style>
  /* No border or box-shadow — these charts breathe inside the parent card */
  .mini-wrap {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    padding: 0 4px;
  }

  .mini-header {
    display: flex;
    align-items: baseline;
    gap: 4px;
    margin-bottom: 3px;
  }

  .mini-label {
    font-family: 'JetBrains Mono', monospace;
    font-size: 0.58rem;
    font-weight: 700;
    letter-spacing: 0.12em;
    text-transform: uppercase;
    color: var(--color-muted);
    opacity: 0.8;
  }

  .mini-unit {
    font-family: 'JetBrains Mono', monospace;
    font-size: 0.5rem;
    font-weight: 400;
    letter-spacing: 0.06em;
    color: var(--color-muted);
    opacity: 0.5;
  }

  /* Prominent current P95 value */
  .mini-current {
    font-family: 'JetBrains Mono', monospace;
    font-size: 1.35rem;
    font-weight: 700;
    letter-spacing: -0.02em;
    line-height: 1;
    margin-bottom: 6px;
    transition: color 0.3s ease;
  }

  .mini-chart {
    flex: 1;
    min-height: 0;
  }

  .mini-svg {
    display: block;
    overflow: visible;
  }

  .mini-placeholder {
    display: flex;
    align-items: center;
    justify-content: center;
    height: 112px;
    font-family: 'JetBrains Mono', monospace;
    font-size: 0.7rem;
    color: var(--color-muted);
    opacity: 0.5;
  }

  /* Y-axis + X-axis labels — matches Load chart style */
  .ax {
    font-family: 'JetBrains Mono', monospace;
    font-size: 9px;
    font-weight: 700;
    fill: var(--color-muted);
    user-select: none;
    pointer-events: none;
  }

  .x-ax {
    font-size: 8px;
    letter-spacing: 0.04em;
    text-transform: uppercase;
  }
</style>
