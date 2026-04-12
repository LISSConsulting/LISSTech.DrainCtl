<script>
  /** @type {{ value?: number }} */
  let { value = 0 } = $props();

  // ── Layout constants ──
  const CX = 110, CY = 105;   // gauge centre (x, y)
  const R  = 78;              // track centre radius
  const TW = 20;              // track stroke width
  const MAX_MS   = 200;
  const GREEN_END = 50;
  const AMBER_END = 150;

  /**
   * SVG arc path for the upper semicircle between fractions f1 and f2.
   * f=0 → left endpoint (0 ms), f=1 → right endpoint (max ms).
   * Always uses sweep-flag=0 (counter-clockwise / upper arc).
   * @param {number} f1 @param {number} f2
   */
  function arc(f1, f2) {
    const t1 = Math.PI * (1 - f1);
    const t2 = Math.PI * (1 - f2);
    const x1 = (CX + R * Math.cos(t1)).toFixed(2);
    const y1 = (CY - R * Math.sin(t1)).toFixed(2);
    const x2 = (CX + R * Math.cos(t2)).toFixed(2);
    const y2 = (CY - R * Math.sin(t2)).toFixed(2);
    return `M ${x1} ${y1} A ${R} ${R} 0 0 0 ${x2} ${y2}`;
  }

  // Static zone paths (never change)
  const BG_ARC    = arc(0, 1);
  const GREEN_ARC = arc(0,                   GREEN_END / MAX_MS);  // 0–50 ms
  const AMBER_ARC = arc(GREEN_END / MAX_MS,  AMBER_END / MAX_MS);  // 50–150 ms
  const RED_ARC   = arc(AMBER_END / MAX_MS,  1);                   // 150–200 ms

  // ── Reactive state ──
  let frac      = $derived(Math.min(1, Math.max(0, value / MAX_MS)));
  let theta     = $derived(Math.PI * (1 - frac));

  // Needle: base extends 14 px past the pivot in the opposite direction
  let tipX  = $derived((CX + (R - TW / 2 - 4) * Math.cos(theta)).toFixed(2));
  let tipY  = $derived((CY - (R - TW / 2 - 4) * Math.sin(theta)).toFixed(2));
  let baseX = $derived((CX - 14 * Math.cos(theta)).toFixed(2));
  let baseY = $derived((CY + 14 * Math.sin(theta)).toFixed(2));

  // Active fill (0 → current value)
  let activeArc = $derived(frac > 0.005 ? arc(0, frac) : null);

  let zoneColor = $derived(
    value < GREEN_END ? 'var(--color-green)' :
    value < AMBER_END ? 'var(--color-amber)' :
                        'var(--color-red)'
  );

  let displayVal = $derived(`${Math.round(value)}ms`);

  // Precomputed label positions for 0 and 200ms arc endpoints
  const LPAD    = R + TW / 2 + 11;
  const L0_X    = (CX + LPAD * Math.cos(Math.PI)).toFixed(2);   // left  (0 ms)
  const L0_Y    = (CY - LPAD * Math.sin(Math.PI) + 4).toFixed(2);
  const L200_X  = (CX + LPAD * Math.cos(0)).toFixed(2);          // right (200 ms)
  const L200_Y  = (CY - LPAD * Math.sin(0) + 4).toFixed(2);
</script>

<div class="gauge-wrap">
  <div class="gauge-card">
    <svg viewBox="0 0 220 162" role="img" aria-label="Fleet average input delay: {displayVal}">

      <!-- ── Background track (full arc) ── -->
      <path d={BG_ARC}
        fill="none"
        stroke="var(--color-surface)"
        stroke-width={TW}
        stroke-linecap="butt" />

      <!-- ── Zone arcs (muted background) ── -->
      <path d={GREEN_ARC}
        fill="none" stroke="var(--color-green)"
        stroke-width={TW} stroke-linecap="butt" opacity="0.4" />
      <path d={AMBER_ARC}
        fill="none" stroke="var(--color-amber)"
        stroke-width={TW} stroke-linecap="butt" opacity="0.4" />
      <path d={RED_ARC}
        fill="none" stroke="var(--color-red)"
        stroke-width={TW} stroke-linecap="butt" opacity="0.4" />

      <!-- ── Active fill (0 → current value, fully opaque, thinner) ── -->
      {#if activeArc}
        <path d={activeArc}
          fill="none" stroke={zoneColor}
          stroke-width={TW - 6} stroke-linecap="butt" />
      {/if}

      <!-- ── Border ring (outer edge of track) ── -->
      <path d={BG_ARC}
        fill="none"
        stroke="var(--color-border)"
        stroke-width={TW + 5}
        stroke-linecap="butt"
        opacity="0.14" />

      <!-- ── Zone-boundary tick marks ── -->
      {#each [0, GREEN_END, (GREEN_END + AMBER_END) / 2, AMBER_END, MAX_MS] as ms}
        {@const tf  = ms / MAX_MS}
        {@const tt  = Math.PI * (1 - tf)}
        {@const ix  = (CX + (R - TW / 2 - 1) * Math.cos(tt)).toFixed(2)}
        {@const iy  = (CY - (R - TW / 2 - 1) * Math.sin(tt)).toFixed(2)}
        {@const ox  = (CX + (R + TW / 2 + 1) * Math.cos(tt)).toFixed(2)}
        {@const oy  = (CY - (R + TW / 2 + 1) * Math.sin(tt)).toFixed(2)}
        <line x1={ix} y1={iy} x2={ox} y2={oy}
          stroke="var(--color-border)" stroke-width="2.5" opacity="0.55" />
      {/each}

      <!-- ── Arc endpoint labels (0 and 200) ── -->
      <text x={L0_X} y={L0_Y} class="g-tick" text-anchor="end">0</text>
      <text x={L200_X} y={L200_Y} class="g-tick" text-anchor="start">200</text>

      <!-- ── Needle ── -->
      <line
        x1={baseX} y1={baseY}
        x2={tipX}  y2={tipY}
        stroke="var(--color-fg)" stroke-width="4" stroke-linecap="round" />
      <!-- Pivot ring -->
      <circle cx={CX} cy={CY} r="9"
        fill="var(--color-card)" stroke="var(--color-border)" stroke-width="2.5" />
      <circle cx={CX} cy={CY} r="4" fill="var(--color-fg)" />

      <!-- ── Big value readout (below arc, above label) ── -->
      <!-- Shadow -->
      <text x={CX + 3} y={133} class="g-val" text-anchor="middle"
        fill="var(--color-shadow)" opacity="0.22">{displayVal}</text>
      <!-- Value -->
      <text x={CX} y={130} class="g-val g-val-live" text-anchor="middle">{displayVal}</text>

      <!-- ── Fleet label ── -->
      <text x={CX} y={152} class="g-label" text-anchor="middle">FLEET AVG INPUT DELAY</text>

    </svg>
  </div>
</div>

<style>
  .gauge-wrap {
    width: 100%;
    height: 100%;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 4px;
  }

  .gauge-card {
    background: var(--color-card);
    border: var(--spacing-bw) solid var(--color-border);
    box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow);
    border-radius: var(--radius-default);
    padding: 14px 10px 10px;
    width: 100%;
  }

  svg {
    display: block;
    width: 100%;
    height: auto;
    overflow: visible;
  }

  .g-val {
    font-family: 'JetBrains Mono', monospace;
    font-size: 30px;
    font-weight: 800;
    fill: var(--color-fg);
    pointer-events: none;
  }

  .g-label {
    font-family: 'JetBrains Mono', monospace;
    font-size: 7px;
    font-weight: 700;
    letter-spacing: 0.11em;
    text-transform: uppercase;
    fill: var(--color-muted);
    pointer-events: none;
  }

  .g-tick {
    font-family: 'JetBrains Mono', monospace;
    font-size: 8px;
    font-weight: 700;
    fill: var(--color-muted);
    pointer-events: none;
  }
</style>
