<script>
  import { appState } from '../lib/state.svelte.js';

  const lastUpdatedStr = $derived(
    appState.lastUpdated
      ? `Updated ${appState.lastUpdated.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })}`
      : 'Not yet updated'
  );
</script>

<footer class="footer">
  <span class="status-pill {appState.connected ? 'connected' : 'disconnected'}">
    {appState.connected ? 'Connected' : 'Disconnected'}
  </span>
  <p class="footer-brand mono">LISSTech DrainCtl</p>
  <p>{lastUpdatedStr}</p>
</footer>

<style>
  .footer {
    background: var(--color-bg);
    border-top: var(--spacing-bw) solid var(--color-border);
    padding: 10px 24px;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 16px;
  }

  .footer p {
    font-size: 0.75rem;
    color: var(--color-subtle);
  }

  .footer-brand {
    font-weight: 700;
    font-size: 0.72rem;
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }

  .status-pill {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-family: 'JetBrains Mono', monospace;
    font-size: 0.68rem;
    font-weight: 700;
    padding: 3px 10px;
    border-radius: 20px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }

  .status-pill::before {
    content: '';
    width: 6px;
    height: 6px;
    border-radius: 50%;
  }

  .connected {
    border: var(--spacing-bw) solid var(--color-green);
    color: var(--color-green);
  }

  .connected::before {
    background: var(--color-green);
  }

  .disconnected {
    border: var(--spacing-bw) solid var(--color-red);
    color: var(--color-red);
  }

  .disconnected::before {
    background: var(--color-red);
  }
</style>
