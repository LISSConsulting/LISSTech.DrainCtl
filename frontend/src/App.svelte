<script>
  import { initTheme } from './lib/theme.js';
  import { appState, addEvent, appendMetricsSample } from './lib/state.svelte.js';
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
  // Refresh logic
  // ---------------------------------------------------------------------------

  /**
   * Pull fresh data from the API and update global state.
   */
  async function refresh() {
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

      // Compute average metrics across all servers that have perf data
      const s = appState.servers;
      const cpu = s.length
        ? s.reduce((a, sv) => a + (sv.perf?.cpu_pct || 0), 0) / s.length
        : 0;
      const memPct = s.length
        ? s.reduce((a, sv) => {
            const total = sv.perf?.mem_total_mb || 0;
            const free = sv.perf?.mem_free_mb || 0;
            return a + (total > 0 ? (1 - free / total) * 100 : 0);
          }, 0) / s.length
        : 0;
      const inputDelay = s.length
        ? s.reduce((a, sv) => a + (sv.perf?.input_delay_ms || 0), 0) / s.length
        : 0;
      const sessions = s.reduce((a, sv) => a + (sv.sessions || 0), 0);

      appendMetricsSample({ time: Date.now(), cpu, mem: memPct, inputDelay, sessions });
      addEvent(`[${new Date().toLocaleTimeString()}] Refreshed — ${s.length} server(s), ${sessions} session(s)`);
    } catch (e) {
      appState.connected = false;
      console.error('refresh:', e);
      addEvent(`[${new Date().toLocaleTimeString()}] Refresh failed: ${e?.message ?? e}`);
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

<style>
  .main {
    max-width: 1400px;
    margin: 0 auto;
    padding: 28px 24px 48px;
  }
</style>
