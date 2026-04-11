<script>
  import { appState } from '../lib/state.svelte.js';

  let showCpu     = $state(true);
  let showMem     = $state(true);
  let showDelay   = $state(false);
  let showSessions = $state(false);

  /** @type {HTMLDivElement|null} */
  let containerEl = $state(null);
  let width = $state(600);

  const PAD = { left: 48, right: 12, top: 12, bottom: 24 };
  const H = 200;

  const SERIES = [
    { key: 'cpu',        label: 'CPU %',       color: 'var(--color-accent)', show: () => showCpu,      toggle: () => { showCpu      = !showCpu;      } },
    { key: 'mem',        label: 'Memory %',    color: 'var(--color-green)',  show: () => showMem,      toggle: () => { showMem      = !showMem;      } },
    { key: 'inputDelay', label: 'Input Delay', color: 'var(--color-amber)', show: () => showDelay,    toggle: () => { showDelay    = !showDelay;    } },
    { key: 'sessions',   label: 'Sessions',    color: 'var(--color-red)',   show: () => showSessions, toggle: () => { showSessions = !showSessions; } },
  ];

  let history = $derived(appState.metricsHistory);

  // Normalise sessions to 0-100
  let sessionMax = $derived(
    Math.max(...history.map(h => h.sessions ?? 0), 1)
  );

  /**
   * Build normalised data: each entry has a value 0-100 for each series key.
   * @type {{ cpu: number, mem: number, inputDelay: number, sessions: number }[]}
   */
  // Input delay chart scale: values above this are clamped to 100% on the Y-axis.
  // Chosen to give comfortable headroom for typical RDS farms (most healthy servers
  // stay well below 50ms, 200ms = obvious degradation).
  const MAX_DELAY_CHART_MS = 200;

  let normalised = $derived(
    history.map(h => ({
      cpu:        Math.min(h.cpu ?? 0, 100),
      mem:        Math.min(h.mem ?? 0, 100),
      inputDelay: Math.min((h.inputDelay ?? 0) / MAX_DELAY_CHART_MS * 100, 100),
      sessions:   ((h.sessions ?? 0) / sessionMax) * 100,
    }))
  );

  /**
   * Convert normalised data points into an SVG area+line path pair.
   * @param {string} key
   * @returns {{ area: string, line: string }}
   */
  let paths = $derived(
    (() => {
      /** @type {Record<string, { area: string, line: string }>} */
      const result = {};
      const n = normalised.length;
      if (n < 2) return result;

      const chartW = width - PAD.left - PAD.right;
      const chartH = H - PAD.top - PAD.bottom;

      for (const s of SERIES) {
        const pts = normalised.map((d, i) => {
          const x = PAD.left + (i / (n - 1)) * chartW;
          const y = PAD.top + chartH * (1 - d[s.key] / 100);
          return { x, y };
        });

        const lineParts = pts.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(' ');
        const bottomY  = (PAD.top + chartH).toFixed(1);
        const area     = `${lineParts} L${pts[pts.length - 1].x.toFixed(1)},${bottomY} L${pts[0].x.toFixed(1)},${bottomY} Z`;

        result[s.key] = { line: lineParts, area };
      }
      return result;
    })()
  );

  // Y-axis grid lines at 0%, 25%, 50%, 75%, 100%
  let gridLines = $derived(
    [0, 25, 50, 75, 100].map(pct => {
      const chartH = H - PAD.top - PAD.bottom;
      const y = PAD.top + chartH * (1 - pct / 100);
      return { y, pct };
    })
  );

  // X-axis time labels (up to 5)
  let xLabels = $derived(
    (() => {
      if (history.length < 2) return [];
      const indices = [0, Math.floor((history.length - 1) * 0.25), Math.floor((history.length - 1) * 0.5), Math.floor((history.length - 1) * 0.75), history.length - 1];
      const unique = [...new Set(indices)];
      const chartW = width - PAD.left - PAD.right;
      const now = Date.now();
      return unique.map(i => {
        const x = PAD.left + (i / (history.length - 1)) * chartW;
        const diffMs = now - (history[i]?.time ?? now);
        const label  = i === history.length - 1 ? 'now' : diffMs < 60_000 ? `${Math.round(diffMs / 1000)}s` : `${Math.round(diffMs / 60_000)}m ago`;
        return { x, label };
      });
    })()
  );

  $effect(() => {
    if (!containerEl) return;
    const ro = new ResizeObserver(entries => {
      width = entries[0].contentRect.width;
    });
    ro.observe(containerEl);
    return () => ro.disconnect();
  });
</script>

<div class="chart-wrap">
  <div class="section-label">PERFORMANCE METRICS</div>
  <div class="chart card" bind:this={containerEl}>
    <div class="chart-toggles">
      {#each SERIES as s}
        <button
          class="chart-toggle {s.show() ? 'active' : ''}"
          style="--series-color: {s.color}"
          aria-pressed={s.show()}
          onclick={s.toggle}
        >
          <span class="dot"></span>{s.label}
        </button>
      {/each}
    </div>

    {#if history.length < 2}
      <div class="chart-placeholder">Collecting data... {history.length}/2</div>
    {:else}
      <svg width={width} height={H} class="chart-svg">
        <!-- Grid lines -->
        {#each gridLines as gl}
          <line
            x1={PAD.left} y1={gl.y.toFixed(1)}
            x2={(width - PAD.right).toFixed(1)} y2={gl.y.toFixed(1)}
            stroke="var(--color-border)" stroke-width="1"
            stroke-dasharray={gl.pct === 0 || gl.pct === 100 ? '' : '4,4'}
            opacity="0.5"
          />
          <text x={(PAD.left - 4).toFixed(1)} y={(gl.y + 4).toFixed(1)} class="axis-label" text-anchor="end">{gl.pct}%</text>
        {/each}

        <!-- X-axis line -->
        <line
          x1={PAD.left} y1={(H - PAD.bottom).toFixed(1)}
          x2={(width - PAD.right).toFixed(1)} y2={(H - PAD.bottom).toFixed(1)}
          stroke="var(--color-border)" stroke-width="1" opacity="0.5"
        />

        <!-- Series (area first, then lines on top) -->
        {#each SERIES as s}
          {#if s.show() && paths[s.key]}
            <path d={paths[s.key].area}  fill={s.color} opacity="0.18" />
            <path d={paths[s.key].line}  stroke={s.color} stroke-width="2" fill="none" stroke-linejoin="round" stroke-linecap="round" />
          {/if}
        {/each}

        <!-- X-axis labels -->
        {#each xLabels as xl}
          <text x={xl.x.toFixed(1)} y={(H - 4).toFixed(1)} class="axis-label" text-anchor="middle">{xl.label}</text>
        {/each}
      </svg>
    {/if}
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

  .chart {
    background: var(--color-card);
    border: var(--spacing-bw) solid var(--color-border);
    border-radius: var(--radius-default);
    box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow);
    padding: 16px 18px;
  }

  .chart-toggles {
    display: flex;
    gap: 10px;
    margin-bottom: 12px;
    flex-wrap: wrap;
  }

  .chart-toggle {
    font-family: 'JetBrains Mono', monospace;
    font-size: 0.7rem;
    font-weight: 700;
    padding: 4px 12px;
    border-radius: 20px;
    border: 1.5px solid var(--color-border);
    background: var(--color-surface);
    color: var(--color-muted);
    cursor: pointer;
    display: flex;
    align-items: center;
    gap: 6px;
    transition: all 0.15s;
  }

  .chart-toggle.active {
    background: var(--series-color);
    color: #fff;
    border-color: var(--series-color);
  }

  .chart-toggle .dot {
    width: 8px;
    height: 8px;
    border-radius: 2px;
    background: var(--series-color);
    flex-shrink: 0;
  }

  .chart-toggle.active .dot {
    background: rgba(255, 255, 255, 0.8);
  }

  .chart-placeholder {
    display: flex;
    align-items: center;
    justify-content: center;
    height: 200px;
    font-family: 'JetBrains Mono', monospace;
    font-size: 0.8rem;
    color: var(--color-muted);
  }

  .chart-svg {
    display: block;
    overflow: visible;
  }

  .axis-label {
    font-family: 'JetBrains Mono', monospace;
    font-size: 10px;
    fill: var(--color-muted);
  }
</style>
