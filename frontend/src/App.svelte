<script>
  import { initTheme } from './lib/theme.svelte.js';
  import { appState, addEvent, appendMetricsSample, appendServerMetricsSample } from './lib/state.svelte.js';
  import { fetchServers, fetchHealth, fetchNotifyConfig } from './lib/api.js';

  import Nav from './components/Nav.svelte';
  import Footer from './components/Footer.svelte';
  import CounterGrid from './components/CounterGrid.svelte';
  import StateBar from './components/StateBar.svelte';
  import MetricsChart from './components/MetricsChart.svelte';
  import EventLog from './components/EventLog.svelte';
  import ServerTable from './components/ServerTable.svelte';
  import ConfigModal from './components/ConfigModal.svelte';
  import HistoryModal from './components/HistoryModal.svelte';
  import Toast from './components/Toast.svelte';

  // ---------------------------------------------------------------------------
  // Initialise theme once on load
  // ---------------------------------------------------------------------------
  initTheme();

  // ---------------------------------------------------------------------------
  // Modal visibility state
  // ---------------------------------------------------------------------------
  let configOpen = $state(false);
  /** @type {string|null} */
  let historyHost = $state(null);

  // ---------------------------------------------------------------------------
  // State transition tracking — detect status changes between refresh cycles
  // ---------------------------------------------------------------------------

  /** Previous server statuses keyed by hostname. Populated after first refresh. */
  const prevStates = /** @type {Map<string, string>} */ (new Map());
  /** True after the first successful refresh completes. */
  let hasRefreshed = false;

  /** @param {'ok'|'grace'|'alert'|'off'} s @returns {string} */
  function statusLabel(s) {
    return { ok: 'Healthy', grace: 'Grace', alert: 'Alert', off: 'Offline' }[s] ?? s;
  }

  /** Map a server status to the event severity used by EventLog for colouring. */
  function statusSev(s) {
    if (s === 'alert' || s === 'off') return 'alert';
    if (s === 'grace') return 'grace';
    return 'ok';
  }

  // ---------------------------------------------------------------------------
  // Event helpers
  // ---------------------------------------------------------------------------

  /**
   * Build a rich structured event object for a per-server state transition.
   * Embeds the server's current drain state, perf metrics, and session counts
   * so EventLog can render a detailed snapshot inline.
   * @param {string} time
   * @param {import('./lib/api.js').Server} sv
   * @param {string} text
   * @param {'ok'|'grace'|'alert'|'off'} sev
   */
  function serverEvent(time, sv, text, sev) {
    const memFreePct = sv.perf && sv.perf.mem_total_mb > 0
      ? Math.round(sv.perf.mem_avail_mb / sv.perf.mem_total_mb * 100)
      : null;
    return {
      time,
      host: sv.host.split('.')[0],
      text,
      sev,
      transition: true,
      drain_state: sv.status,
      drain_mode: sv.drain_mode ?? null,
      state_duration_seconds: sv.state_duration_seconds ?? null,
      changed_by: sv.changed_by ?? '',
      sessions_active: sv.sessions_active ?? sv.sessions,
      sessions_disconnected: sv.sessions_disconnected ?? 0,
      sessions_max: sv.max_sessions,
      cpu_pct: sv.perf?.cpu_pct ?? null,
      mem_free_pct: memFreePct,
      input_delay_p95_ms: sv.perf?.input_delay_p95_ms ?? null,
      pages_sec: sv.perf?.pages_sec ?? null,
      tcp_retrans_sec: sv.perf?.tcp_retrans_sec ?? null,
      disk_queue: sv.perf?.disk_queue ?? null,
    };
  }

  // ---------------------------------------------------------------------------
  // Refresh logic
  // ---------------------------------------------------------------------------

  let refreshing = false;

  /**
   * Pull fresh data from the API and update global state.
   * Guard prevents concurrent calls — if the previous fetch hasn't resolved
   * before the 30-second tick fires, the tick is skipped.
   */
  async function refresh() {
    if (refreshing) return;
    refreshing = true;
    try {
      // Fetch config once on the first successful refresh (lazy load).
      const calls = /** @type {Promise<any>[]} */ ([fetchServers(), fetchHealth()]);
      const needsConfig = appState.config === null;
      if (needsConfig) calls.push(fetchNotifyConfig());

      const results = await Promise.all(calls);
      const [servers, health] = results;

      appState.servers = servers || [];
      appState.health = health;
      if (needsConfig) appState.config = results[2] ?? null;
      appState.connected = true;
      appState.lastUpdated = new Date();

      // Detect and log server state transitions (skipped on first refresh so we
      // don't flood the log with N "registered" lines when the page loads).
      const evtTime = new Date().toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });
      if (hasRefreshed) {
        const seenHosts = new Set((servers || []).map(sv => sv.host));
        for (const sv of (servers || [])) {
          const prev = prevStates.get(sv.host);
          if (prev === undefined) {
            addEvent(serverEvent(evtTime, sv, `registered (${statusLabel(sv.status)})`, 'ok'));
          } else if (prev !== sv.status) {
            addEvent(serverEvent(evtTime, sv, `${statusLabel(prev)} → ${statusLabel(sv.status)}`, statusSev(sv.status)));
          }
        }
        for (const host of prevStates.keys()) {
          if (!seenHosts.has(host)) {
            addEvent({ time: evtTime, host: host.split('.')[0], text: 'removed from dashboard', sev: 'alert', transition: true, drain_state: 'off' });
          }
        }
      }
      // Snapshot for next cycle.
      prevStates.clear();
      for (const sv of (servers || [])) prevStates.set(sv.host, sv.status);
      hasRefreshed = true;

      // Compute average metrics across all servers that have perf data.
      // Divide by the count of servers with perf data, not total server count —
      // servers with perf=null would otherwise pull averages toward 0.
      const s = appState.servers;
      const perfSvs = s.filter(sv => sv.perf);
      const cpu = perfSvs.length
        ? perfSvs.reduce((a, sv) => a + (sv.perf.cpu_pct || 0), 0) / perfSvs.length
        : 0;
      const memSvs = s.filter(sv => sv.perf?.mem_total_mb > 0);
      const memPct = memSvs.length
        ? memSvs.reduce((a, sv) => a + (1 - sv.perf.mem_avail_mb / sv.perf.mem_total_mb) * 100, 0) / memSvs.length
        : 0;
      const inputDelay = perfSvs.length
        ? perfSvs.reduce((a, sv) => a + (sv.perf.input_delay_p95_ms || 0), 0) / perfSvs.length
        : 0;
      const pagesPerSec = perfSvs.length
        ? perfSvs.reduce((a, sv) => a + (sv.perf.pages_sec || 0), 0) / perfSvs.length
        : 0;
      const tcpRetrans = perfSvs.length
        ? perfSvs.reduce((a, sv) => a + (sv.perf.tcp_retrans_sec || 0), 0) / perfSvs.length
        : 0;
      const diskQueue = perfSvs.length
        ? perfSvs.reduce((a, sv) => a + (sv.perf.disk_queue || 0), 0) / perfSvs.length
        : 0;
      const sessions = s.reduce((a, sv) => a + (sv.sessions || 0), 0);

      const ts = Date.now();
      appendMetricsSample({ time: ts, cpu, mem: memPct, inputDelay, sessions, pagesPerSec, tcpRetrans, diskQueue });

      // Per-server ring buffers for per-host sparklines in ServerDetail.
      for (const sv of s) {
        if (sv.perf) {
          const svMemPct = sv.perf.mem_total_mb > 0
            ? (1 - sv.perf.mem_avail_mb / sv.perf.mem_total_mb) * 100
            : 0;
          appendServerMetricsSample(sv.host, {
            time:       ts,
            cpu:        sv.perf.cpu_pct,
            mem:        svMemPct,
            inputDelay: sv.perf.input_delay_p95_ms,
            sessions:   sv.sessions ?? 0,
          });
        }
      }

      addEvent({
        time: evtTime,
        host: '',
        text: `Refreshed — ${s.length} server(s), ${sessions} session(s)`,
        sev: 'ok',
        transition: false,
        fleet_servers: s.length,
        fleet_sessions: sessions,
        fleet_cpu_pct: Math.round(cpu * 10) / 10,
        fleet_mem_used_pct: Math.round(memPct * 10) / 10,
        fleet_input_delay_p95: Math.round(inputDelay * 10) / 10,
        fleet_pages_sec: Math.round(pagesPerSec * 10) / 10,
        fleet_tcp_retrans_sec: Math.round(tcpRetrans * 10) / 10,
        fleet_disk_queue: Math.round(diskQueue * 100) / 100,
      });
    } catch (e) {
      appState.connected = false;
      console.error('refresh:', e);
      addEvent({
        time: new Date().toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }),
        host: '',
        text: `Refresh failed: ${e?.message ?? e}`,
        sev: 'alert',
        transition: false,
      });
    } finally {
      refreshing = false;
    }
  }

  // Run immediately on mount, then every 30 seconds.
  $effect(() => {
    refresh();
    const interval = setInterval(refresh, 30_000);
    return () => clearInterval(interval);
  });
</script>

<Nav onconfigopen={() => (configOpen = true)} />

<main class="main">
  <CounterGrid />
  <StateBar />
  <MetricsChart />
  <EventLog />
  <ServerTable onhistoryclick={(host) => (historyHost = host)} />
</main>

<Footer />

{#if configOpen}
  <ConfigModal onclose={() => (configOpen = false)} />
{/if}

{#if historyHost}
  <HistoryModal host={historyHost} onclose={() => (historyHost = null)} />
{/if}

<Toast />

<style>
  .main {
    max-width: 1400px;
    margin: 0 auto;
    padding: 28px 24px 48px;
  }
</style>
