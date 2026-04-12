<script>
  /** @type {{ value?: number }} */
  let { value = 0 } = $props();

  const MAX_MS    = 200;
  const GREEN_END = 50;
  const AMBER_END = 150;

  let frac = $derived(Math.min(1, Math.max(0, value / MAX_MS)));

  let zoneColor = $derived(
    value < GREEN_END ? 'var(--color-green)' :
    value < AMBER_END ? 'var(--color-amber)' :
                        'var(--color-red)'
  );

  let displayVal = $derived(`${Math.round(value)}ms`);
</script>

<!--
  Renders as a single bar-chart bar.
  Parent (gauge-panel in MetricsChart) must supply an explicit height
  and padding-top matching LayerCake's padding.top so the plot area aligns.
-->
<div class="vu">
  <div class="vu-track">
    <!--
      Horizontal grid lines matching DualAxisChart GRID_PCTS [0, 25, 50, 75, 100].
      0% = bottom border (handled by border-bottom on .vu-track).
      100% = top solid line, opacity 0.6.
      25/50/75% = dashed (5px dash, 4px gap), opacity 0.38.
    -->
    <div class="gl gl-solid" style:bottom="100%"></div>
    <div class="gl gl-dash"  style:bottom="75%"></div>
    <div class="gl gl-dash"  style:bottom="50%"></div>
    <div class="gl gl-dash"  style:bottom="25%"></div>

    <!-- Single bar — grows from bottom -->
    <div class="vu-fill" style:height="{frac * 100}%" style:background-color={zoneColor}></div>
  </div>

  <div class="vu-num" style:color={zoneColor}>{displayVal}</div>
  <div class="vu-lbl">INPUT DELAY</div>
</div>

<style>
  .vu {
    height: 100%;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 4px;
    overflow: visible;   /* number/label may extend below padding boundary */
  }

  /* Plot-area track — fills the space between padding-top and bottom of container */
  .vu-track {
    flex: 1 1 0;
    width: 100%;
    position: relative;
    /* Axis borders — matches DualAxisChart: 2px solid, opacity ~0.7 */
    border-left:   2px solid var(--color-border);
    border-bottom: 2px solid var(--color-border);
    box-sizing: border-box;
    overflow: visible;
  }

  /* Shared grid-line base */
  .gl {
    position: absolute;
    left: 0;
    right: 0;
    height: 1px;
    pointer-events: none;
  }

  /* Solid line (top / 100% mark) — opacity 0.6 matching DualAxisChart */
  .gl-solid {
    background: var(--color-border);
    opacity: 0.6;
  }

  /* Dashed lines (25/50/75%) — 5px dash, 4px gap, opacity 0.38 */
  .gl-dash {
    background-image: repeating-linear-gradient(
      to right,
      var(--color-border) 0,
      var(--color-border) 5px,
      transparent 5px,
      transparent 9px
    );
    opacity: 0.38;
  }

  /* The bar fill — anchored to bottom, grows upward */
  .vu-fill {
    position: absolute;
    bottom: 0;
    left: 0;
    right: 0;
    transition: height 0.35s ease, background-color 0.2s ease;
  }

  /* Value readout — small, clean, proportional to the narrow column */
  .vu-num {
    font-family: 'JetBrains Mono', monospace;
    font-size: 11px;
    font-weight: 800;
    line-height: 1;
    letter-spacing: -0.02em;
    white-space: nowrap;
    transition: color 0.2s ease;
  }

  /* Label */
  .vu-lbl {
    font-family: 'JetBrains Mono', monospace;
    font-size: 6px;
    font-weight: 700;
    letter-spacing: 0.18em;
    text-transform: uppercase;
    color: var(--color-muted);
    white-space: nowrap;
    margin-top: -1px;
  }
</style>
