<script>
  import { fetchHistory } from '../lib/api.js';
  import { formatTs, dur } from '../lib/utils.js';

  let { host, onclose } = $props();

  let entries = $state([]);
  let loading = $state(false);
  let error = $state('');
  let changesOnly = $state(false);

  $effect(() => {
    if (!host) return;
    loadHistory();
  });

  async function loadHistory() {
    if (loading) return;
    loading = true;
    error = '';
    // Snapshot the filter at fetch time so we can detect if it changed while
    // the request was in flight (e.g. rapid toggle of "Transitions Only").
    const fetchedChangesOnly = changesOnly;
    try {
      entries = await fetchHistory(host, 100, fetchedChangesOnly) || [];
    } catch(e) {
      error = e?.message ?? String(e);
    } finally {
      loading = false;
      // If the filter toggled while we were fetching, re-fetch with the new value.
      if (changesOnly !== fetchedChangesOnly) loadHistory();
    }
  }

  function handleOverlayClick(e) {
    if (e.target === e.currentTarget) onclose?.();
  }

  $effect(() => {
    function onKey(e) { if (e.key === 'Escape') onclose?.(); }
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  });

  // The API returns lowercase status values ('ok', 'grace', 'alert', 'off').
  // statusClass maps these directly to CSS class names (which match the status values).
  function statusClass(s) {
    return { ok: 'ok', grace: 'grace', alert: 'alert', off: 'off' }[s] ?? 'off';
  }

  function statusLabel(s) {
    return { ok: 'Healthy', grace: 'Grace', alert: 'Alert', off: 'Offline' }[s] || s;
  }
</script>

{#if host}
  <!-- svelte-ignore a11y_click_events_have_key_events a11y_interactive_supports_focus -->
  <div class="hist-overlay" onclick={handleOverlayClick} role="dialog" aria-modal="true" tabindex="-1">
    <div class="hist-modal">
      <div class="hist-head">
        <div>
          <div class="hist-title serif">Server History</div>
          <div class="hist-hostname">{host.split('.')[0]}</div>
        </div>
        <div class="hist-head-actions">
          <button
            class="filter-pill {changesOnly ? 'active' : ''}"
            onclick={() => { changesOnly = !changesOnly; loadHistory(); }}
          >Transitions Only</button>
          <button class="settings-close" onclick={onclose}>✕</button>
        </div>
      </div>
      <div class="hist-body scrollbar-styled">
        {#if loading}
          <div class="hist-empty">Loading...</div>
        {:else if error}
          <div class="hist-empty err">Error: {error}</div>
        {:else if !entries.length}
          <div class="hist-empty">No history recorded yet.</div>
        {:else}
          {#each entries as e}
            {@const sc = statusClass(e.status)}
            <div class="hist-entry" class:hist-entry-transition={e.transition}>
              <div class="hist-ts">{formatTs(e.timestamp)}</div>
              <div class="hist-badge {sc}">{statusLabel(sc)}</div>
              <div class="hist-detail">
                {#if e.drain_mode && e.drain_mode !== 'none'}mode={e.drain_mode}{/if}
                {#if e.state_duration_seconds != null} dur={dur(e.state_duration_seconds)}{/if}
                {#if e.changed_by} by={e.changed_by}{/if}
              </div>
            </div>
          {/each}
        {/if}
      </div>
    </div>
  </div>
{/if}

<style>
  .hist-overlay { position: fixed; inset: 0; background: rgba(0,0,0,0.5); z-index: 160; display: flex; justify-content: center; align-items: center; }
  .hist-modal { width: 640px; max-width: 94vw; max-height: 82vh; background: var(--color-card); border: var(--spacing-bw) solid var(--color-border); border-radius: var(--radius-default); box-shadow: 8px 8px 0 var(--color-shadow); overflow: hidden; display: flex; flex-direction: column; }
  .hist-head { padding: 20px 24px 16px; border-bottom: 1px solid var(--color-surface); display: flex; align-items: center; justify-content: space-between; flex-shrink: 0; }
  .hist-head-actions { display: flex; align-items: center; gap: 10px; }
  .hist-title { font-family: 'DM Serif Display', serif; font-size: 1.2rem; margin-bottom: 2px; }
  .hist-hostname { font-family: 'JetBrains Mono', monospace; font-size: 0.82rem; color: var(--color-accent); }
  .hist-body { overflow-y: auto; padding: 8px 24px 20px; flex: 1; }
  .hist-empty { text-align: center; padding: 32px; color: var(--color-subtle); font-family: 'JetBrains Mono', monospace; font-size: 0.8rem; }
  .hist-empty.err { color: var(--color-red); }
  .hist-entry { display: flex; align-items: baseline; gap: 10px; padding: 9px 0; border-bottom: 1px solid var(--color-surface); }
  .hist-entry:last-child { border-bottom: none; }
  .hist-entry-transition { background: color-mix(in srgb, var(--color-accent) 8%, transparent); padding-left: 8px; border-radius: 4px; }
  .hist-ts { font-family: 'JetBrains Mono', monospace; font-size: 0.7rem; color: var(--color-muted); white-space: nowrap; min-width: 96px; }
  .hist-badge { font-family: 'JetBrains Mono', monospace; font-size: 0.6rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.06em; padding: 2px 8px; border-radius: 4px; color: #fff; white-space: nowrap; min-width: 54px; text-align: center; }
  .hist-badge.ok { background: var(--color-green); }
  .hist-badge.grace { background: var(--color-amber); }
  .hist-badge.alert { background: var(--color-red); }
  .hist-badge.off { background: var(--color-subtle); }
  .hist-detail { font-family: 'JetBrains Mono', monospace; font-size: 0.72rem; color: var(--color-muted); flex: 1; }
  .filter-pill { font-family: 'JetBrains Mono', monospace; font-size: 0.68rem; font-weight: 700; padding: 4px 10px; border-radius: 20px; border: var(--spacing-bw) solid var(--color-border); background: var(--color-card); color: var(--color-muted); cursor: pointer; text-transform: uppercase; letter-spacing: 0.06em; transition: all 0.15s; }
  .filter-pill.active { background: var(--color-accent); color: #fff; border-color: var(--color-accent); }
  .settings-close { background: none; border: none; font-size: 1.2rem; cursor: pointer; color: var(--color-muted); padding: 4px 8px; }
  .settings-close:hover { color: var(--color-fg); }
</style>
