<script>
  /** @type {{ value?: number }} */
  let { value = 0 } = $props();

  // ── Geometry constants ──────────────────────────────────────────────────────
  const CX  = 130;   // arc centre x
  const CY  = 120;   // arc centre y (= arc endpoint baseline)
  const R   = 88;    // arc centre radius
  const TW  = 22;    // track stroke width
  const MAX_MS    = 200;
  const GREEN_END =  50;
  const AMBER_END = 150;

  /**
   * SVG arc path along the UPPER semicircle from fraction f1 to f2.
   * f=0 → left endpoint (0 ms), f=1 → right endpoint (MAX_MS).
   * Angles: θ = π·(1−f), sweep-flag=0 draws the upper (counterclockwise) arc.
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

  // Static zone arcs (computed once)
  const BG_ARC    = arc(0,                   1);
  const GREEN_ARC = arc(0,                   GREEN_END / MAX_MS);
  const AMBER_ARC = arc(GREEN_END / MAX_MS,  AMBER_END / MAX_MS);
  const RED_ARC   = arc(AMBER_END / MAX_MS,  1);

  // ── Reactive state ──────────────────────────────────────────────────────────
  let frac      = $derived(Math.min(1, Math.max(0, value / MAX_MS)));
  let theta     = $derived(Math.PI * (1 - frac));

  // Needle tip sits just inside the inner edge of the track
  let tipX  = $derived((CX + (R - TW / 2 - 2) * Math.cos(theta)).toFixed(2));
  let tipY  = $derived((CY - (R - TW / 2 - 2) * Math.sin(theta)).toFixed(2));
  // Needle tail extends 12 px past the pivot in the opposite direction
  let baseX = $derived((CX - 12 * Math.cos(theta)).toFixed(2));
  let baseY = $derived((CY + 12 * Math.sin(theta)).toFixed(2));

  // Progress arc (0 → current value)
  let activeArc = $derived(frac > 0.005 ? arc(0, frac) : null);

  let zoneColor = $derived(
    value < GREEN_END ? 'var(--color-green)' :
    value < AMBER_END ? 'var(--color-amber)' :
                        'var(--color-red)'
  );

  let displayVal = $derived(`${Math.round(value)}ms`);

  // Endpoint labels: placed just outside and below each arc tip
  const L0_X   = CX - R - 8;    // left  of left  endpoint
  const L200_X = CX + R + 8;    // right of right endpoint
  const L_Y    = CY + 16;       // below endpoint baseline
</script>

<div class="gauge-wrap">
  <div class="gauge-card">
    <!--
      viewBox "0 0 260 178"
      Content bounds:
        x: L0_X "0" label (text-anchor end) ≈ 28 … L200_X "200" (text-anchor start) ≈ 248 — within 0–260
        y: top of track outer = CY − (R + TW/2) = 120 − 99 = 21 … label baseline 170 — within 0–178
    -->
    <svg viewBox="0 0 260 178"
         role="img"
         aria-label="Fleet average input delay: {displayVal}">

      <!-- ── Background track (full gray semicircle) ── -->
      <path d={BG_ARC}
        fill="none"
        stroke="var(--color-surface)"
        stroke-width={TW}
        stroke-linecap="butt" />

      <!-- ── Muted zone arcs ── -->
      <path d={GREEN_ARC}
        fill="none" stroke="var(--color-green)"
        stroke-width={TW} stroke-linecap="butt" opacity="0.35" />
      <path d={AMBER_ARC}
        fill="none" stroke="var(--color-amber)"
        stroke-width={TW} stroke-linecap="butt" opacity="0.35" />
      <path d={RED_ARC}
        fill="none" stroke="var(--color-red)"
        stroke-width={TW} stroke-linecap="butt" opacity="0.35" />

      <!-- ── Active fill (0 → current value, thinner, full opacity) ── -->
      {#if activeArc}
        <path d={activeArc}
          fill="none" stroke={zoneColor}
          stroke-width={TW - 6} stroke-linecap="butt" />
      {/if}

      <!-- ── Zone-boundary tick marks ── -->
      {#each [0, GREEN_END, AMBER_END, MAX_MS] as ms}
        {@const tf = ms / MAX_MS}
        {@const tt = Math.PI * (1 - tf)}
        {@const ix = (CX + (R - TW / 2) * Math.cos(tt)).toFixed(2)}
        {@const iy = (CY - (R - TW / 2) * Math.sin(tt)).toFixed(2)}
        {@const ox = (CX + (R + TW / 2) * Math.cos(tt)).toFixed(2)}
        {@const oy = (CY - (R + TW / 2) * Math.sin(tt)).toFixed(2)}
        <line x1={ix} y1={iy} x2={ox} y2={oy}
          stroke="var(--color-border)" stroke-width="2" opacity="0.5" />
      {/each}

      <!-- ── Arc endpoint labels ── -->
      <text x={L0_X}   y={L_Y} class="g-tick" text-anchor="end">0</text>
      <text x={L200_X} y={L_Y} class="g-tick" text-anchor="start">200</text>

      <!-- ── Needle ── -->
      <line
        x1={baseX} y1={baseY}
        x2={tipX}  y2={tipY}
        stroke="var(--color-fg)" stroke-width="3.5" stroke-linecap="round" />
      <!-- Pivot ring -->
      <circle cx={CX} cy={CY} r="8"
        fill="var(--color-card)" stroke="var(--color-border)" stroke-width="2.5" />
      <circle cx={CX} cy={CY} r="4" fill="var(--color-fg)" />

      <!-- ── Value readout ── -->
      <text x={CX} y={CY + 34} class="g-val" text-anchor="middle">{displayVal}</text>

      <!-- ── Fleet label ── -->
      <text x={CX} y={CY + 52} class="g-label" text-anchor="middle">FLEET AVG INPUT DELAY</text>

    </svg>
  </div>
</div>

<style>
  .gauge-wrap {
    width: 100%;
    display: flex;
    align-items: center;
    justify-content: center;
  }

  .gauge-card {
    background: var(--color-card);
    border: var(--spacing-bw) solid var(--color-border);
    box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow);
    border-radius: var(--radius-default);
    padding: 12px 10px 10px;
    width: 100%;
    box-sizing: border-box;
    /* Prevent the box-shadow from leaking outside gauge-panel */
    overflow: visible;
  }

  svg {
    display: block;
    width: 100%;
    height: auto;
    /* All content lives inside the viewBox — no overflow needed */
    overflow: hidden;
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
    font-size: 9px;
    font-weight: 700;
    fill: var(--color-muted);
    pointer-events: none;
  }
</style>
