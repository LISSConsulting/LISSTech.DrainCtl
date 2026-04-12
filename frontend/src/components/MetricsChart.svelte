<script>
  import { LayerCake, Svg } from 'layercake';
  import { appState } from '../lib/state.svelte.js';
  import DualAxisChart from './chart/DualAxisChart.svelte';
  import MiniHealthChart from './chart/MiniHealthChart.svelte';

  // ── Upper chart (LOAD): CPU %, Memory %, Sessions ────────────────────────
  let showCpu      = $state(true);
  let showMem      = $state(true);
  let showSessions = $state(true);

  const LOAD_SERIES = [
    { key: 'cpu',      label: 'CPU %',    color: 'var(--color-accent)', axis: 'left',  lineOnly: false, show: () => showCpu,      toggle: () => { showCpu      = !showCpu;      } },
    { key: 'mem',      label: 'Memory %', color: 'var(--color-green)',  axis: 'left',  lineOnly: false, show: () => showMem,      toggle: () => { showMem      = !showMem;      } },
    { key: 'sessions', label: 'Sessions', color: 'var(--color-red)',    axis: 'right', lineOnly: true,  show: () => showSessions, toggle: () => { showSessions = !showSessions; } },
  ];

  // ── Lower section (HEALTH INDICATORS): 4 individual mini-charts ──────────
  // Each chart has its own Y-axis scale, threshold zones, and current P95 value.
  // Thresholds: warn = start of yellow zone, crit = start of red zone.

  /** @type {Array<{key:string,label:string,unit:string,yMax:number,thresholds:{warn:number,crit:number},color:string,fmt:(v:number)=>string}>} */
  const MINI_CHARTS = [
    {
      key:        'inputDelay',
      label:      'Input Delay',
      unit:       'ms',
      yMax:       500,
      thresholds: { warn: 100, crit: 250 },
      color:      'var(--color-amber)',
      fmt:        v => `${Math.round(v)}ms`,
    },
    {
      key:        'pagesPerSec',
      label:      'Pages/sec',
      unit:       '/sec',
      yMax:       200,
      thresholds: { warn: 80, crit: 150 },
      color:      'var(--color-accent)',
      fmt:        v => `${Math.round(v)}/s`,
    },
    {
      key:        'tcpRetrans',
      label:      'TCP Retrans',
      unit:       '/sec',
      yMax:       50,
      thresholds: { warn: 10, crit: 25 },
      color:      'var(--color-red)',
      fmt:        v => `${v.toFixed(1)}/s`,
    },
    {
      key:        'diskQueue',
      label:      'Avg Disk Queue',
      unit:       '',
      yMax:       10,
      thresholds: { warn: 2, crit: 5 },
      color:      'var(--color-green)',
      fmt:        v => v.toFixed(2),
    },
  ];

  let history    = $derived(appState.metricsHistory);
  let sessionMax = $derived(Math.max(...history.map(h => h.sessions ?? 0), 1));
  let hasRight   = $derived(showSessions);

  // LOAD chart — normalise to 0–100; raw values carried for tooltip
  let loadNormData = $derived(
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

  // Right-axis tick labels for sessions scale
  let rightTicks = $derived(
    showSessions
      ? [0, 0.25, 0.5, 0.75, 1].map(f => ({
          pct:   f * 100,
          label: Math.round(f * sessionMax).toString(),
        }))
      : []
  );

  let loadVisible = $derived({
    cpu:      showCpu,
    mem:      showMem,
    sessions: showSessions,
  });

  const Y_DOMAIN = [0, 100];
  let lcData = $derived(history.map((_, i) => ({ x: i, y: 50 })));
</script>

<div class="chart-wrap">
  <div class="section-label">Performance Metrics</div>
  <div class="chart-card">

    <!-- ── LOAD: CPU, Memory, Sessions ── -->
    <div class="sub-label">LOAD <span class="sub-label-note">· Average across fleet</span></div>
    <p class="chart-desc">Average CPU and memory utilization across all active servers. Sessions shows total connected users. Sustained high values indicate the farm needs more capacity or drain rotation.</p>
    <div class="chart-panel">
      <div class="chart-toggles">
        {#each LOAD_SERIES as s}
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

      <div class="chart-body upper-chart">
        {#if history.length < 2}
          <div class="chart-placeholder">Collecting data… {history.length}/2</div>
        {:else}
          <LayerCake
            data={lcData}
            x="x"
            y="y"
            yDomain={Y_DOMAIN}
            padding={{ top: 16, right: hasRight ? 64 : 16, bottom: 8, left: 48 }}
          >
            <Svg>
              <DualAxisChart
                normData={loadNormData}
                SERIES={LOAD_SERIES}
                {rightTicks}
                {history}
                visible={loadVisible}
                showXAxis={false}
              />
            </Svg>
          </LayerCake>
        {/if}
      </div>
    </div>

    <!-- ── Separator ── -->
    <div class="chart-separator"></div>

    <!-- ── HEALTH INDICATORS: 4 individual mini-charts ── -->
    <div class="sub-label">HEALTH INDICATORS <span class="sub-label-note">· P95 across fleet</span></div>
    <p class="chart-desc">95th percentile health metrics across the fleet — showing the worst-performing servers. Spikes indicate individual servers experiencing issues that may require drain intervention.</p>

    <div class="mini-charts-row">
      {#each MINI_CHARTS as mc}
        <MiniHealthChart
          {history}
          valueKey={mc.key}
          label={mc.label}
          unit={mc.unit}
          yMax={mc.yMax}
          thresholds={mc.thresholds}
          color={mc.color}
          fmt={mc.fmt}
        />
      {/each}
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

  /* Small sub-heading above each chart section */
  .sub-label {
    font-family: 'JetBrains Mono', monospace;
    font-size: 0.6rem;
    font-weight: 700;
    letter-spacing: 0.14em;
    text-transform: uppercase;
    color: var(--color-muted);
    margin-bottom: 10px;
    opacity: 0.65;
  }

  .sub-label-note {
    font-weight: 400;
    letter-spacing: 0.08em;
    opacity: 0.7;
  }

  .chart-desc {
    font-family: 'Work Sans', sans-serif;
    font-size: 0.72rem;
    line-height: 1.5;
    color: var(--color-muted);
    opacity: 0.7;
    margin: 0 0 12px 0;
    max-width: 640px;
  }

  /* Thin horizontal separator between the two chart sections */
  .chart-separator {
    height: 1px;
    background: var(--color-border);
    opacity: 0.25;
    margin: 14px 0;
  }

  .chart-panel {
    display: flex;
    flex-direction: column;
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

  /* Area series indicator: solid square */
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

  /* Dashed line indicator (Sessions — right-axis series) */
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

  /* ── Load chart area ── */
  .chart-body {
    position: relative;
  }

  .upper-chart {
    height: 220px;
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

  /* ── Health Indicators: 4 mini charts side by side ── */
  .mini-charts-row {
    display: flex;
    gap: 16px;
  }
</style>
