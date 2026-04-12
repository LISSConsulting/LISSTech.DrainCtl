<script>
  import { getContext } from 'svelte';

  /**
   * Health indicator chart — dual Y-axes with area fills.
   * Left axis: Input Delay (ms) + Pages/sec (tens-to-hundreds range).
   * Right axis: TCP Retrans/sec + Avg Disk Queue (low single-digit range).
   * All series pre-normalised to 0–100 by MetricsChart; tick labels show real values.
   *
   * @typedef {{ i: number, time: number, raw: Record<string,number>, [key: string]: any }} HealthPoint
   * @typedef {{ key: string, label: string, color: string, axis: string }} HealthSeriesDef
   * @typedef {{ pct: number, label: string }} AxisTick
   */

  /** @type {{ normData: HealthPoint[], SERIES: HealthSeriesDef[], leftTicks: AxisTick[], rightTicks: AxisTick[], history: any[], visible: Record<string,boolean> }} */
  let { normData, SERIES, leftTicks, rightTicks, history, visible } = $props();

  const { xScale, yScale, width, height } = getContext('LayerCake');

  const LINE_WIDTH = 3.5;

  // Whether any right-axis series are currently visible
  let hasRight = $derived(SERIES.some(s => s.axis === 'right' && visible[s.key]));

  // X-axis labels — up to 5 evenly spaced ticks
  let xLabels = $derived((() => {
    const n = history.length;
    if (n < 2) return [];
    const indices = [0, Math.floor((n-1)*0.25), Math.floor((n-1)*0.5), Math.floor((n-1)*0.75), n-1];
    const unique = [...new Set(indices)];
    const now = Date.now();
    return unique.map(i => {
      const diffMs = now - (history[i]?.time ?? now);
      const label  = i === n-1      ? 'now'
                   : diffMs < 60_000 ? `${Math.round(diffMs/1000)}s`
                   :                   `${Math.round(diffMs/60_000)}m`;
      return { x: $xScale(i), label };
    });
  })());

  // Pre-compute SVG line + area paths for all series
  let allPaths = $derived((() => {
    const n = normData.length;
    if (n < 2) return /** @type {Record<string,{line:string,area:string}>} */ ({});
    /** @type {Record<string,{line:string,area:string}>} */
    const out = {};
    const bottomY = $yScale(0).toFixed(1);
    for (const s of SERIES) {
      const pts = normData.map(d => ({
        x: $xScale(d.i),
        y: $yScale(/** @type {any} */ (d)[s.key] ?? 0),
      }));
      const lineParts = pts.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(' ');
      const area = `${lineParts} L${pts[pts.length-1].x.toFixed(1)},${bottomY} L${pts[0].x.toFixed(1)},${bottomY} Z`;
      out[s.key] = { line: lineParts, area };
    }
    return out;
  })());

  // Area series sorted highest-value-first so large areas render behind small ones
  let areaRenderOrder = $derived((() => {
    const last = normData[normData.length - 1];
    if (!last) return SERIES;
    return [...SERIES].sort((a, b) =>
      (/** @type {any} */ (last)[b.key] ?? 0) - (/** @type {any} */ (last)[a.key] ?? 0)
    );
  })());

  // Hover state
  let hoverIndex = $state(/** @type {number|null} */ (null));

  /** @param {MouseEvent} e */
  function onMouseMove(e) {
    const rect = /** @type {Element} */ (e.currentTarget).getBoundingClientRect();
    const mouseX = e.clientX - rect.left;
    const n = normData.length;
    if (n < 2) { hoverIndex = null; return; }
    hoverIndex = Math.max(0, Math.min(n - 1, Math.round(mouseX / rect.width * (n - 1))));
  }

  function onMouseLeave() { hoverIndex = null; }

  // Tooltip layout constants
  const TIP_W    = 185;
  const TIP_PAD  = 8;
  const TIP_LNSP = 17;
  const TIP_HDR  = 20;

  /** @param {HealthSeriesDef[]} vis */
  function tipHeight(vis) {
    return TIP_HDR + vis.length * TIP_LNSP + TIP_PAD;
  }

  /** Format raw value with proper units. @param {HealthPoint} d @param {HealthSeriesDef} s */
  function fmtRaw(d, s) {
    const raw = d.raw?.[s.key];
    if (raw == null) return '—';
    if (s.key === 'inputDelay')  return `${Math.round(raw)}ms`;
    if (s.key === 'pagesPerSec') return `${Math.round(raw)}/s`;
    if (s.key === 'tcpRetrans')  return `${raw.toFixed(1)}/s`;
    if (s.key === 'diskQueue')   return raw.toFixed(2);
    return String(raw);
  }
</script>

<!-- ── Grid lines + left-axis labels ── -->
{#each leftTicks as tick}
  {@const y = $yScale(tick.pct)}
  <line
    x1={0} y1={y.toFixed(1)}
    x2={$width} y2={y.toFixed(1)}
    stroke="var(--color-border)"
    stroke-width="1"
    stroke-dasharray={tick.pct === 0 || tick.pct === 100 ? '' : '5,4'}
    opacity={tick.pct === 0 || tick.pct === 100 ? '0.6' : '0.38'}
  />
  <text x={-6} y={(y + 3.5).toFixed(1)} class="ax" text-anchor="end">{tick.label}</text>
{/each}

<!-- ── Right-axis ── -->
{#if hasRight && rightTicks.length > 0}
  <line
    x1={$width} y1={0} x2={$width} y2={$height}
    stroke="var(--color-border)" stroke-width="1" opacity="0.35"
  />
  {#each rightTicks as tick}
    {@const y = $yScale(tick.pct)}
    <text x={$width + 6} y={(y + 3.5).toFixed(1)} class="ax ax-r" text-anchor="start">{tick.label}</text>
  {/each}
{/if}

<!-- ── Axis borders ── -->
<line x1={0} y1={0} x2={0} y2={$height}
  stroke="var(--color-border)" stroke-width="2" opacity="0.7" />
<line x1={0} y1={$height} x2={$width} y2={$height}
  stroke="var(--color-border)" stroke-width="2" opacity="0.7" />

<!-- ── Area fills: largest first ── -->
{#each areaRenderOrder as s}
  {#if visible[s.key] && allPaths[s.key]?.line}
    <path d={allPaths[s.key].area} fill={s.color} fill-opacity="1" />
  {/if}
{/each}

<!-- ── Stroke lines: all visible series ── -->
{#each SERIES as s}
  {#if visible[s.key] && allPaths[s.key]?.line}
    <path
      d={allPaths[s.key].line}
      stroke={s.color}
      stroke-width={LINE_WIDTH}
      fill="none"
      stroke-linejoin="round"
      stroke-linecap="round"
    />
  {/if}
{/each}

<!-- ── X-axis labels ── -->
{#each xLabels as xl}
  <text x={xl.x.toFixed(1)} y={($height + 17).toFixed(1)} class="ax x-ax" text-anchor="middle">{xl.label}</text>
{/each}

<!-- ── Hover crosshair + tooltip ── -->
{#if hoverIndex !== null && normData[hoverIndex]}
  {@const d   = normData[hoverIndex]}
  {@const cx  = $xScale(hoverIndex)}
  {@const vis = SERIES.filter(s => visible[s.key])}
  {@const th  = tipHeight(vis)}
  {@const tx  = cx + 14 + TIP_W > $width ? cx - TIP_W - 10 : cx + 14}
  {@const ty  = Math.max(2, Math.min($height - th - 2, $yScale(50) - th / 2))}
  {@const diffMs  = Date.now() - (d.time ?? Date.now())}
  {@const timeStr = diffMs < 1200 ? 'now' : diffMs < 60_000 ? `${Math.round(diffMs/1000)}s ago` : `${Math.round(diffMs/60_000)}m ago`}

  <!-- Vertical crosshair -->
  <line
    x1={cx.toFixed(1)} y1={0} x2={cx.toFixed(1)} y2={$height}
    stroke="var(--color-fg)" stroke-width="1.5" stroke-dasharray="3,3" opacity="0.5"
  />

  <!-- Data-point dots -->
  {#each vis as s}
    {@const dotY = $yScale(/** @type {any} */ (d)[s.key] ?? 0)}
    <circle cx={cx.toFixed(1)} cy={dotY.toFixed(1)} r="4"
      fill={s.color} stroke="var(--color-border)" stroke-width="2" />
  {/each}

  <!-- Tooltip shadow (neobrutalist offset) -->
  <rect x={tx + 5} y={ty + 5} width={TIP_W} height={th} fill="var(--color-shadow)" />

  <!-- Tooltip card -->
  <rect
    x={tx} y={ty} width={TIP_W} height={th}
    fill="var(--color-card)"
    stroke="var(--color-border)"
    stroke-width="3"
  />

  <!-- Divider under time header -->
  <line
    x1={tx + TIP_PAD} y1={ty + TIP_HDR - 1}
    x2={tx + TIP_W - TIP_PAD} y2={ty + TIP_HDR - 1}
    stroke="var(--color-border)" stroke-width="1" opacity="0.3"
  />

  <!-- Time label + P95 badge -->
  <text x={tx + TIP_PAD} y={ty + 13} class="tip-time">{timeStr}</text>
  <text x={tx + TIP_W - TIP_PAD} y={ty + 13} class="tip-time tip-p95" text-anchor="end">P95 fleet</text>

  <!-- Series value rows -->
  {#each vis as s, si}
    <!-- Area color swatch -->
    <rect
      x={tx + TIP_PAD} y={ty + TIP_HDR + si * TIP_LNSP + 2}
      width={6} height={6}
      fill={s.color}
      stroke="var(--color-border)" stroke-width="1"
    />
    <text
      x={tx + TIP_PAD + 16}
      y={ty + TIP_HDR + si * TIP_LNSP + 10}
      class="tip-val"
    >{s.label}: <tspan font-weight="700" fill={s.color}>{fmtRaw(d, s)}</tspan></text>
  {/each}
{/if}

<!-- ── Transparent overlay — captures mouse events, rendered last (topmost) ── -->
<!-- svelte-ignore a11y_no_static_element_interactions -->
<rect
  x={0} y={0} width={$width} height={$height}
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
  .ax-r {
    /* right-axis labels — same style, positioned via x attribute */
  }
  .x-ax {
    font-size: 9px;
    letter-spacing: 0.04em;
    text-transform: uppercase;
  }
  .tip-time {
    font-family: 'JetBrains Mono', monospace;
    font-size: 10px;
    font-weight: 700;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    fill: var(--color-muted);
    pointer-events: none;
  }

  .tip-p95 {
    font-weight: 400;
    font-size: 9px;
    opacity: 0.6;
  }
  .tip-val {
    font-family: 'JetBrains Mono', monospace;
    font-size: 10px;
    fill: var(--color-fg);
    pointer-events: none;
  }
</style>
