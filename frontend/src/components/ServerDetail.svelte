<script>
  import RingGauge from './RingGauge.svelte';
  import Sparkline from './Sparkline.svelte';
  import { DEFAULTS, resolveThresholds } from '../lib/thresholds.js';
  import { appState } from '../lib/state.svelte.js';

  let { server } = $props();

  function rel(iso) {
    if (!iso) return 'never';
    const d = new Date(iso);
    if (isNaN(d)) return 'never';
    const s = Math.floor((Date.now() - d) / 1000);
    if (s < 0) return 'now';
    if (s < 60) return s + 's ago';
    const m = Math.floor(s / 60);
    if (m < 60) return m + 'm ago';
    const h = Math.floor(m / 60);
    if (h < 24) return h + 'h ago';
    return Math.floor(h / 24) + 'd ago';
  }

  let perf = $derived(server.perf || {});
  let memPct = $derived(perf.mem_total_mb > 0 ? (1 - perf.mem_avail_mb / perf.mem_total_mb) * 100 : 0);

  // Sessions ring: when max_sessions is known, show utilisation % (0–100) so
  // the warn/crit thresholds (session_warning_threshold from config) map directly
  // to the fill position.  When max_sessions is unknown, show the raw count on a
  // fixed 100-unit scale with neutral color until real capacity data arrives.
  let sessionWarnThresh = $derived(appState.config?.session_warning_threshold ?? 80);
  let sessionCritThresh = $derived(Math.min(sessionWarnThresh + 15, 100));
  let sessionsPct = $derived(
    server.max_sessions > 0
      ? Math.min((server.sessions / server.max_sessions) * 100, 100)
      : null
  );

  // Per-server ring buffer; fall back to fleet aggregate when no server-specific
  // data is available yet (e.g., first render before any refresh cycle completes).
  let serverHistory = $derived(
    appState.serverMetrics.get(server.host) ?? appState.metricsHistory
  );
  let cpuHistory     = $derived(serverHistory.map(h => h.cpu));
  let memHistory     = $derived(serverHistory.map(h => h.mem));
  let delayHistory   = $derived(serverHistory.map(h => h.inputDelay));

  // Config-aware thresholds — prefer user-configured values over static defaults.
  let perfCfg = $derived(appState.config?.performance ?? null);
  let cpuThresh   = $derived(resolveThresholds('cpu',        perfCfg));
  let memThresh   = $derived(resolveThresholds('mem',        perfCfg));
  let delayThresh = $derived(resolveThresholds('inputDelay', perfCfg));
</script>

<div class="d-inner">
  <!-- Tile 1: Resource Utilization -->
  <div class="d-tile d-tile-util">
    <div class="d-tile-label">Resource Utilization</div>
    <div class="d-ring-row">
      <div class="d-ring-cell">
        <RingGauge value={perf.cpu_pct} label="CPU" unit="%" warnThreshold={cpuThresh.warn} critThreshold={cpuThresh.crit} />
      </div>
      <div class="d-ring-cell">
        <RingGauge value={memPct} label="Memory" unit="%" warnThreshold={memThresh.warn} critThreshold={memThresh.crit} />
      </div>
      <div class="d-ring-cell">
        <RingGauge
          value={sessionsPct}
          max={100}
          label="Sessions"
          unit=" "
          warnThreshold={sessionWarnThresh}
          critThreshold={sessionCritThresh}
          centerLabel={String(server.sessions)}
        />
      </div>
    </div>
    <div class="d-spark-row">
      <div class="d-spark-cell">
        <Sparkline data={cpuHistory} color="var(--color-accent)" />
        <div class="d-spark-labels"><span>CPU</span><span>{perf.cpu_pct?.toFixed(1) ?? '—'}%</span></div>
      </div>
      <div class="d-spark-cell">
        <Sparkline data={memHistory} color="var(--color-green)" />
        <div class="d-spark-labels"><span>Mem</span><span>{memPct.toFixed(1)}%</span></div>
      </div>
      <div class="d-spark-cell">
        <Sparkline data={delayHistory} color="var(--color-amber)" />
        <div class="d-spark-labels"><span>Delay</span><span>{perf.input_delay_p95_ms?.toFixed(1) ?? '—'}ms</span></div>
      </div>
    </div>
  </div>

  <!-- Tile 2: I/O Metrics -->
  <div class="d-tile d-tile-io">
    <div class="d-tile-label">I/O Metrics</div>
    <div class="d-ring-row">
      <div class="d-ring-cell">
        <RingGauge
          value={perf.disk_queue}
          max={DEFAULTS.diskQueue.crit * 2}
          label="Disk Queue"
          unit=" "
          warnThreshold={DEFAULTS.diskQueue.warn}
          critThreshold={DEFAULTS.diskQueue.crit}
        />
      </div>
      <div class="d-ring-cell">
        <RingGauge
          value={perf.input_delay_p95_ms}
          max={delayThresh.crit * 2}
          label="Input Delay"
          unit="ms"
          warnThreshold={delayThresh.warn}
          critThreshold={delayThresh.crit}
        />
      </div>
      <div class="d-ring-cell">
        <RingGauge
          value={perf.tcp_retrans_sec}
          max={DEFAULTS.tcpRetransmits.crit * 2}
          label="TCP Retrans"
          unit="/s"
          warnThreshold={DEFAULTS.tcpRetransmits.warn}
          critThreshold={DEFAULTS.tcpRetransmits.crit}
        />
      </div>
    </div>
  </div>

  <!-- Tile 3: Server Details -->
  <div class="d-tile d-tile-details">
    <div class="d-tile-label">Server Details</div>
    {#each [
      ['Registered', rel(server.registered_at)],
      ['Last Seen', rel(server.last_seen)],
      ['Version', server.version || '—'],
      ['Changed By', server.changed_by || '—'],
      ['Pages/sec', perf.pages_sec != null ? perf.pages_sec.toFixed(1) : '—'],
      ['Mem Avail', perf.mem_avail_mb ? (perf.mem_avail_mb/1024).toFixed(1)+' GB' : '—'],
      ['Mem Total', perf.mem_total_mb ? (perf.mem_total_mb/1024).toFixed(1)+' GB' : '—'],
    ] as [k, v]}
      <div class="d-kv-row">
        <span class="d-kv-k">{k}</span>
        <span class="d-kv-v mono">{v}</span>
      </div>
    {/each}
  </div>
</div>

<style>
  .d-inner { padding: 12px 16px; background: var(--color-bg); display: flex; gap: 10px; align-items: stretch; font-family: 'JetBrains Mono', monospace; font-size: 12px; flex-wrap: wrap; }
  .d-tile { background: var(--color-card); border: 1.5px solid color-mix(in srgb, var(--color-border) 50%, transparent); border-radius: 8px; padding: 10px 12px; display: flex; flex-direction: column; }
  .d-tile-label { font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.6px; color: var(--color-subtle); margin-bottom: 8px; }
  .d-tile-util { flex: 1.4; min-width: 240px; }
  .d-tile-io { flex: 1; min-width: 180px; }
  .d-tile-details { flex: 1; min-width: 170px; }
  .d-ring-row { display: flex; gap: 12px; margin-bottom: 8px; }
  .d-ring-cell { flex: 1; display: flex; justify-content: center; }
  .d-spark-row { display: flex; gap: 8px; margin-top: 6px; }
  .d-spark-cell { flex: 1; }
  .d-spark-labels { display: flex; justify-content: space-between; font-size: 9px; color: var(--color-subtle); margin-top: 2px; }
  .d-kv-row { display: flex; justify-content: space-between; font-size: 12px; padding: 3px 4px; border-bottom: 1px solid var(--color-surface); }
  .d-kv-row:last-child { border-bottom: none; }
  .d-kv-k { color: var(--color-muted); }
  .d-kv-v { font-weight: 600; }
  .mono { font-family: 'JetBrains Mono', monospace; }
</style>
