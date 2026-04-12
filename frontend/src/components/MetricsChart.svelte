<script>
  import { LayerCake, Svg } from 'layercake';
  import { appState } from '../lib/state.svelte.js';
  import DualAxisChart from './chart/DualAxisChart.svelte';
  import InputDelayGauge from './InputDelayGauge.svelte';

  let showCpu      = $state(true);
  let showMem      = $state(true);
  let showSessions = $state(true);   // active by default

  const SERIES = [
    { key: 'cpu',      label: 'CPU %',    color: 'var(--color-accent)', axis: 'left',  lineOnly: false, show: () => showCpu,      toggle: () => { showCpu      = !showCpu;      } },
    { key: 'mem',      label: 'Memory %', color: 'var(--color-green)',  axis: 'left',  lineOnly: false, show: () => showMem,      toggle: () => { showMem      = !showMem;      } },
    { key: 'sessions', label: 'Sessions', color: 'var(--color-red)',    axis: 'right', lineOnly: true,  show: () => showSessions, toggle: () => { showSessions = !showSessions; } },
  ];

  let history    = $derived(appState.metricsHistory);
  let sessionMax = $derived(Math.max(...history.map(h => h.sessions ?? 0), 1));
  let hasRight   = $derived(showSessions);

  // All series normalised to 0–100 for shared rendering scale.
  // Raw values are carried for the tooltip.
  let normData = $derived(
    history.map((h, i) => ({
      i,
      time:     h.time,
      cpu:      Math.min(h.cpu ?? 0, 100),
      mem:      Math.min(h.mem ?? 0, 100),
      sessions: ((h.sessions ?? 0) / sessionMax) * 100,
      raw: {
        cpu:      +(h.cpu ?? 0).toFixed(1),
        mem:      +(h.mem ?? 0).toFixed(1),
        sessions: h.sessions ?? 0,
      },
    }))
  );

  // Right-axis tick labels for the sessions scale
  let rightTicks = $derived(
    showSessions
      ? [0, 0.25, 0.5, 0.75, 1].map(f => ({
          pct:   f * 100,
          label: Math.round(f * sessionMax).toString(),
        }))
      : []
  );

  let visible = $derived({
    cpu:      showCpu,
    mem:      showMem,
    sessions: showSessions,
  });

  const Y_DOMAIN = [0, 100];
  let lcData = $derived(normData.map(d => ({ x: d.i, y: 50 })));
</script>

<div class="chart-wrap">
  <div class="section-label">Performance Metrics</div>
  <div class="chart-card">

    <p class="chart-desc">Fleet-wide averages. Left axis: CPU &amp; Memory %. Right axis: Sessions count. Input delay gauge shows fleet average. Updated every 30&nbsp;s.</p>

    <div class="perf-layout">

      <!-- ── LEFT: main chart ── -->
      <div class="chart-panel">
        <div class="chart-toggles">
          {#each SERIES as s}
            <button
              class="chart-toggle"
              class:active={s.show()}
              style="--sc: {s.color}"
              aria-pressed={s.show()}
              onclick={s.toggle}
            >
              {#if s.lineOnly}
                <span class="t-dash" aria-hidden="true"></span>
              {:else}
                <span class="t-dot" aria-hidden="true"></span>
              {/if}
              {s.label}
              {#if s.axis === 'right'}<span class="t-axis">R</span>{/if}
            </button>
          {/each}
        </div>

        <div class="chart-body">
          {#if history.length < 2}
            <div class="chart-placeholder">Collecting data… {history.length}/2</div>
          {:else}
            <LayerCake
              data={lcData}
              x="x"
              y="y"
              yDomain={Y_DOMAIN}
              padding={{ top: 16, right: hasRight ? 64 : 16, bottom: 32, left: 48 }}
            >
              <Svg>
                <DualAxisChart {normData} {SERIES} {rightTicks} {history} {visible} />
              </Svg>
            </LayerCake>
          {/if}
        </div>
      </div>

      <!-- ── RIGHT: Input Delay gauge ── -->
      <div class="gauge-panel">
        <InputDelayGauge value={appState.avgInputDelay} />
      </div>

    </div>
  </div>
</div>

<style>
  .chart-wrap { margin-bottom: 24px; }

  .section-label {
    font-family: 'JetBrains Mono', monospace;
    font-size: 0.65rem;
    font-weight: 700;
    letter-spacing: 0.12em;
    text-transform: uppercase;
    color: var(--color-muted);
    margin-bottom: 8px;
  }

  .chart-card {
    background: var(--color-card);
    border: var(--spacing-bw) solid var(--color-border);
    border-radius: var(--radius-default);
    box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow);
    padding: 16px 18px;
  }

  .chart-desc {
    font-family: 'Work Sans', sans-serif;
    font-size: 0.72rem;
    color: var(--color-subtle);
    margin: 0 0 12px;
    line-height: 1.5;
  }

  /* ── Side-by-side layout ── */
  .perf-layout {
    display: flex;
    gap: 16px;
    align-items: stretch;
  }

  .chart-panel {
    flex: 65 1 0;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }

  .gauge-panel {
    flex: 35 1 0;
    min-width: 180px;
    display: flex;
    align-items: center;
    justify-content: center;
  }

  @media (max-width: 680px) {
    .perf-layout {
      flex-direction: column;
    }
    .gauge-panel {
      min-width: 0;
      min-height: 200px;
    }
  }

  /* ── Toggle buttons — neobrutalist ── */
  .chart-toggles {
    display: flex;
    gap: 8px;
    margin-bottom: 14px;
    flex-wrap: wrap;
  }

  .chart-toggle {
    font-family: 'JetBrains Mono', monospace;
    font-size: 0.62rem;
    font-weight: 700;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    padding: 5px 10px;
    border-radius: 0;
    border: var(--spacing-bw) solid var(--color-border);
    box-shadow: 2px 2px 0 var(--color-shadow);
    background: var(--color-surface);
    color: var(--color-fg);
    cursor: pointer;
    display: flex;
    align-items: center;
    gap: 6px;
    transition: transform 0.08s ease, box-shadow 0.08s ease;
    white-space: nowrap;
  }

  .chart-toggle:hover {
    transform: translate(-1px, -1px);
    box-shadow: 3px 3px 0 var(--color-shadow);
  }

  .chart-toggle:active {
    transform: translate(2px, 2px);
    box-shadow: none;
  }

  .chart-toggle.active {
    background: var(--sc);
    color: #fff;
  }

  /* Area series: solid square dot */
  .t-dot {
    width: 8px;
    height: 8px;
    border-radius: 1px;
    background: var(--sc);
    border: 1.5px solid color-mix(in srgb, var(--color-border) 60%, transparent);
    flex-shrink: 0;
  }

  .chart-toggle.active .t-dot {
    background: rgba(255, 255, 255, 0.85);
    border-color: rgba(255, 255, 255, 0.4);
  }

  /* Line-only series: dashed bar indicator */
  .t-dash {
    width: 18px;
    height: 3px;
    border-radius: 0;
    background: repeating-linear-gradient(
      to right,
      var(--sc) 0px, var(--sc) 7px,
      transparent 7px, transparent 11px
    );
    flex-shrink: 0;
  }

  .chart-toggle.active .t-dash {
    background: repeating-linear-gradient(
      to right,
      rgba(255,255,255,0.9) 0px, rgba(255,255,255,0.9) 7px,
      transparent 7px, transparent 11px
    );
  }

  .t-axis {
    font-size: 0.52rem;
    opacity: 0.55;
    margin-left: -2px;
  }

  /* ── Chart area ── */
  .chart-body {
    flex: 1;
    height: 220px;
    position: relative;
  }

  .chart-placeholder {
    display: flex;
    align-items: center;
    justify-content: center;
    height: 100%;
    font-family: 'JetBrains Mono', monospace;
    font-size: 0.8rem;
    color: var(--color-muted);
  }
</style>
