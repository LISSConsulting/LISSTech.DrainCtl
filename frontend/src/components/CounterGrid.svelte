<script>
  import { appState } from '../lib/state.svelte.js';

  const counters = $derived(appState.counters);

  /**
   * Navigate to the Servers view with a specific status filter.
   * @param {'all'|'ok'|'grace'|'alert'|'off'} filter
   */
  function goToServers(filter) {
    appState.serverFilter = filter;
    appState.currentView = 'servers';
  }
</script>

<div class="counters">
  <button class="card ctr total" onclick={() => goToServers('all')}>
    <div class="ctr-v">{counters.total}</div>
    <div class="ctr-l">Total Servers</div>
  </button>
  <button class="card ctr ok" onclick={() => goToServers('ok')}>
    <div class="ctr-v">{counters.ok}</div>
    <div class="ctr-l">Healthy</div>
  </button>
  <button class="card ctr grace" onclick={() => goToServers('grace')}>
    <div class="ctr-v">{counters.grace}</div>
    <div class="ctr-l">Grace</div>
  </button>
  <button class="card ctr alert" onclick={() => goToServers('alert')}>
    <div class="ctr-v">{counters.alert}</div>
    <div class="ctr-l">Alert</div>
  </button>
  <button class="card ctr off" onclick={() => goToServers('off')}>
    <div class="ctr-v">{counters.off}</div>
    <div class="ctr-l">Offline</div>
  </button>
  <div class="card ctr sessions no-click">
    <div class="ctr-v mono">{counters.sessions}</div>
    <div class="ctr-l">Sessions</div>
  </div>
</div>

<style>
  .counters {
    display: grid;
    grid-template-columns: repeat(6, 1fr);
    gap: 14px;
    margin-bottom: 20px;
  }

  .ctr {
    padding: 16px 18px;
    border-left: 5px solid var(--color-border);
    cursor: pointer;
    text-align: left;
    width: 100%;
    transition: transform 0.12s, box-shadow 0.12s;
  }
  .ctr:hover {
    transform: translate(-3px, -3px);
    box-shadow: calc(var(--spacing-so) + 3px) calc(var(--spacing-so) + 3px) 0 var(--color-shadow);
  }

  .ctr.no-click {
    cursor: default;
  }
  .ctr.no-click:hover {
    transform: none;
    box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow);
  }

  .ctr-v {
    font-family: 'JetBrains Mono', monospace;
    font-size: 2.2rem;
    font-weight: 700;
    line-height: 1;
    font-variant-numeric: tabular-nums;
  }

  .ctr-l {
    font-size: 0.75rem;
    color: var(--color-muted);
    font-weight: 600;
    margin-top: 3px;
  }

  .ctr.total { border-left-color: var(--color-accent); }

  .ctr.ok { border-left-color: var(--color-green); }
  .ctr.ok .ctr-v { color: var(--color-green); }

  .ctr.grace { border-left-color: var(--color-amber); }
  .ctr.grace .ctr-v { color: var(--color-amber); }

  .ctr.alert { border-left-color: var(--color-red); }
  .ctr.alert .ctr-v { color: var(--color-red); }

  .ctr.off { border-left-color: var(--color-subtle); }
  .ctr.off .ctr-v { color: var(--color-subtle); }

  .ctr.sessions { border-left-color: var(--color-muted); }
</style>
