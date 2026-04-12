<script>
  import { LayerCake, Svg } from 'layercake';
  import { appState } from '../lib/state.svelte.js';
  import DualAxisChart from './chart/DualAxisChart.svelte';

  let showCpu      = $state(true);
  let showMem      = $state(true);
  let showDelay    = $state(false);
  let showSessions = $state(false);

  const MAX_DELAY_MS = 200;

  const SERIES = [
    { key: 'cpu',        label: 'CPU %',       color: 'var(--color-accent)', axis: 'left',  show: () => showCpu,      toggle: () => { showCpu      = !showCpu;      } },
    { key: 'mem',        label: 'Memory %',    color: 'var(--color-green)',  axis: 'left',  show: () => showMem,      toggle: () => { showMem      = !showMem;      } },
    { key: 'inputDelay', label: 'Input Delay', color: 'var(--color-amber)', axis: 'right', show: () => showDelay,    toggle: () => { showDelay    = !showDelay;    } },
    { key: 'sessions',   label: 'Sessions',    color: 'var(--color-red)',   axis: 'right', show: () => showSessions, toggle: () => { showSessions = !showSessions; } },
  ];

  let history    = $derived(appState.metricsHistory);
  let sessionMax = $derived(Math.max(...history.map(h => h.sessions ?? 0), 1));
  let hasRight   = $derived(showDelay || showSessions);

  // All series normalised to 0–100 for a shared rendering scale.
  // Raw values are carried along for the tooltip.
  let normData = $derived(
    history.map((h, i) => ({
      i,
      time:       h.time,
      cpu:        Math.min(h.cpu ?? 0, 100),
      mem:        Math.min(h.mem ?? 0, 100),
      inputDelay: Math.min((h.inputDelay ?? 0) / MAX_DELAY_MS * 100, 100),
      sessions:   ((h.sessions ?? 0) / sessionMax) * 100,
      raw: {
        cpu:        +(h.cpu ?? 0).toFixed(1),
        mem:        +(h.mem ?? 0).toFixed(1),
        inputDelay: +(h.inputDelay ?? 0).toFixed(1),
        sessions:   h.sessions ?? 0,
      },
    }))
  );

  // Right-axis tick labels: delay takes priority over sessions.
  let rightTicks = $derived((() => {
    if (showDelay) {
      return [0, 50, 100, 150, 200].map(ms => ({
        pct:   (ms / MAX_DELAY_MS) * 100,
        label: String(ms),
      }));
    }
    if (showSessions) {
      return [0, 0.25, 0.5, 0.75, 1].map(f => ({
        pct:   f * 100,
        label: Math.round(f * sessionMax).toString(),
      }));
    }
    return [];
  })());

  // Passed as a plain reactive object so DualAxisChart can use visible[key]
  // without having to call closures, keeping reactivity unambiguous in Svelte 5.
  let visible = $derived({
    cpu:        showCpu,
    mem:        showMem,
    inputDelay: showDelay,
    sessions:   showSessions,
  });

  // Minimal dataset for LayerCake — provides responsive scaling context.
  const Y_DOMAIN = [0, 100];
  let lcData = $derived(normData.map(d => ({ x: d.i, y: 50 })));
</script>

<div class="chart-wrap">
  <div class="section-label">Performance Metrics</div>
  <div class="chart-card">

    <p class="chart-desc">Fleet-wide averages across all reporting servers. Left axis: percentage (0–100%). Right axis: absolute values — toggle Input Delay or Sessions to activate. Updated every 30&nbsp;s.</p>

    <div class="chart-toggles">
      {#each SERIES as s}
        <button
          class="chart-toggle"
          class:active={s.show()}
          style="--sc: {s.color}"
          aria-pressed={s.show()}
          onclick={s.toggle}
        >
          <span class="t-dot"></span>
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

  .t-axis {
    font-size: 0.52rem;
    opacity: 0.55;
    margin-left: -2px;
  }

  /* ── Chart area ── */
  .chart-body {
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
