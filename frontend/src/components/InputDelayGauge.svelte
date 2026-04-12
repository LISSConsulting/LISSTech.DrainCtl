<script>
  import { onDestroy, untrack } from 'svelte';

  /** @type {{ value?: number }} */
  let { value = 0 } = $props();

  // ── Constants ──────────────────────────────────────────────────────────────
  const N         = 20;          // number of LED segments
  const MAX_MS    = 200;
  const GREEN_END = 50;
  const AMBER_END = 150;
  const STEP      = MAX_MS / N;  // 10 ms per segment

  const SEG_H  = 12;   // segment height (SVG px)
  const GAP    = 3;    // gap between segments
  const BAR_W  = 36;   // bar width
  const PAD_X  = 7;    // horizontal padding inside panel
  const PAD_Y  = 8;    // vertical padding inside panel

  const BAR_H  = N * SEG_H + (N - 1) * GAP;   // 297 px
  const SVG_W  = BAR_W + PAD_X * 2;            // 50 px
  const SVG_H  = BAR_H + PAD_Y * 2;            // 313 px

  // ── Segment metadata (static — position & zone never change) ──────────────
  /** @param {number} i  0 = top (red), N-1 = bottom (green) */
  function segZone(i) {
    const mid = (N - i - 0.5) * STEP;
    if (mid >= AMBER_END) return /** @type {'red'}   */ ('red');
    if (mid >= GREEN_END) return /** @type {'amber'} */ ('amber');
    return                       /** @type {'green'} */ ('green');
  }

  const COLOR_VAR = /** @type {Record<string,string>} */ ({
    red:   'var(--color-red)',
    amber: 'var(--color-amber)',
    green: 'var(--color-green)',
  });

  const GRAD_ID = /** @type {Record<string,string>} */ ({
    red:   'vug-r',
    amber: 'vug-a',
    green: 'vug-g',
  });

  const SEGS = Array.from({ length: N }, (_, i) => ({
    i,
    y: PAD_Y + i * (SEG_H + GAP),
    zone: segZone(i),
  }));

  // ── Reactive derived values ────────────────────────────────────────────────
  let activeCount = $derived(Math.round(Math.min(N, Math.max(0, value / STEP))));

  let zoneColor = $derived(
    value < GREEN_END ? 'var(--color-green)' :
    value < AMBER_END ? 'var(--color-amber)' :
                        'var(--color-red)'
  );

  let displayVal = $derived(`${Math.round(value)}ms`);

  // ── Peak hold ──────────────────────────────────────────────────────────────
  let peak   = $state(0);
  let _timer = /** @type {ReturnType<typeof setTimeout>|null} */ (null);
  let _raf   = /** @type {number|null} */ (null);

  function _cancelDecay() {
    if (_timer !== null) { clearTimeout(_timer);        _timer = null; }
    if (_raf   !== null) { cancelAnimationFrame(_raf);  _raf   = null; }
  }

  function _startDecay(fromPeak) {
    const HOLD_MS  = 2000;
    const DECAY_MS = 2500;

    _timer = setTimeout(() => {
      _timer = null;
      const t0 = performance.now();

      const tick = (/** @type {number} */ now) => {
        const t      = Math.min(1, (now - t0) / DECAY_MS);
        const eased  = 1 - (1 - t) * (1 - t);          // ease-in-quad for natural fall
        const next   = Math.max(value, fromPeak * (1 - eased));
        peak = next;

        if (t < 1 && next > value) {
          _raf = requestAnimationFrame(tick);
        } else {
          _raf = null;
          peak = value;
        }
      };

      _raf = requestAnimationFrame(tick);
    }, HOLD_MS);
  }

  $effect(() => {
    const v = value;
    if (v >= untrack(() => peak)) {
      _cancelDecay();
      peak = v;
      _startDecay(v);
    }
  });

  onDestroy(_cancelDecay);

  // Peak segment: topmost lit segment at peak value. -1 = no peak shown.
  let peakActive = $derived(Math.round(Math.min(N, Math.max(0, peak / STEP))));
  let peakIdx    = $derived(peakActive > 0 ? N - peakActive : -1);
</script>

<div class="vu">
  <!-- LED panel -->
  <svg
    class="vu-bar"
    viewBox="0 0 {SVG_W} {SVG_H}"
    width={SVG_W}
    height={SVG_H}
    role="img"
    aria-label="Fleet average input delay: {displayVal}"
    aria-valuenow={Math.round(value)}
    aria-valuemin="0"
    aria-valuemax={MAX_MS}
  >
    <defs>
      <!--
        Per-segment gradients use objectBoundingBox by default, so each rect
        gets its own top-to-bottom gradient regardless of absolute position.
        The lighter-top / darker-bottom gives the "lit LED" depth effect.
      -->
      <linearGradient id="vug-g" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0%"   stop-color="var(--color-green)" stop-opacity="1"    />
        <stop offset="100%" stop-color="var(--color-green)" stop-opacity="0.68" />
      </linearGradient>
      <linearGradient id="vug-a" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0%"   stop-color="var(--color-amber)" stop-opacity="1"    />
        <stop offset="100%" stop-color="var(--color-amber)" stop-opacity="0.68" />
      </linearGradient>
      <linearGradient id="vug-r" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0%"   stop-color="var(--color-red)"   stop-opacity="1"    />
        <stop offset="100%" stop-color="var(--color-red)"   stop-opacity="0.68" />
      </linearGradient>
    </defs>

    <!-- Dark panel background -->
    <rect x="0" y="0" width={SVG_W} height={SVG_H} fill="#0c0c0e" rx="4" />

    <!-- Segment rows -->
    {#each SEGS as { i, y, zone }}
      {@const active = i >= N - activeCount}
      {@const isPeak = i === peakIdx && !active}
      {@const cvar   = COLOR_VAR[zone]}
      {@const gid    = GRAD_ID[zone]}

      <!-- Unlit background: very faint zone colour so structure is always visible -->
      <rect
        x={PAD_X} y={y}
        width={BAR_W} height={SEG_H}
        fill={cvar} opacity="0.10"
        rx="1.5"
      />

      {#if active || isPeak}
        <!-- Lit segment with gradient depth -->
        <rect
          x={PAD_X} y={y}
          width={BAR_W} height={SEG_H}
          fill="url(#{gid})"
          rx="1.5"
        />
        <!-- Subtle surface highlight: thin bright stripe at very top of segment -->
        <rect
          x={PAD_X + 1} y={y + 1}
          width={BAR_W - 2} height="2"
          fill="white" opacity="0.18"
          rx="0.5"
        />
      {/if}

      {#if isPeak}
        <!--
          Peak hold marker: full-brightness segment with a crisp white accent
          stripe at its top edge — the "floating dot" above the main bar.
        -->
        <rect
          x={PAD_X + 1} y={y + 0.5}
          width={BAR_W - 2} height="2.5"
          fill="white" opacity="0.70"
          rx="0.5"
        />
        <rect
          x={PAD_X + 1} y={y + 0.5}
          width={BAR_W - 2} height="2.5"
          fill={cvar} opacity="0.60"
          rx="0.5"
        />
      {/if}
    {/each}
  </svg>

  <!-- Digital readout -->
  <div class="vu-num" style:color={zoneColor}>{displayVal}</div>
  <div class="vu-lbl">INPUT DELAY</div>
</div>

<style>
  .vu {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 10px;
    padding: 4px 0 2px;
  }

  .vu-bar {
    display: block;
    /* Fixed intrinsic size; scales down if container is narrower */
    max-width: 100%;
    height: auto;
  }

  .vu-num {
    font-family: 'JetBrains Mono', monospace;
    font-size: 1.85rem;
    font-weight: 800;
    line-height: 1;
    letter-spacing: -0.03em;
    /* Glow in zone colour — gives the "lit display" instrument feel */
    text-shadow: 0 0 14px currentColor;
    transition: color 0.15s ease, text-shadow 0.15s ease;
  }

  .vu-lbl {
    font-family: 'JetBrains Mono', monospace;
    font-size: 0.44rem;
    font-weight: 700;
    letter-spacing: 0.24em;
    text-transform: uppercase;
    color: var(--color-muted);
    margin-top: -2px;
  }
</style>
