<script>
  import { appState } from '../lib/state.svelte.js';

  let search   = $state('');
  let expanded = $state(false);

  /** @type {HTMLDivElement|null} */
  let logEl = $state(null);

  // Collapse the expanded overlay when Escape is pressed.
  $effect(() => {
    function onKey(e) { if (e.key === 'Escape' && expanded) expanded = false; }
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  });

  /**
   * Parse a raw event string into a display-friendly object.
   * Strings are produced by App.svelte as:
   *   "[10:30:00 AM] Refreshed — 3 server(s), 12 session(s)"
   *   "[10:30:00 AM] Refresh failed: ..."
   *
   * For structured event objects (future-proofing) pass through as-is.
   *
   * @param {unknown} raw
   * @returns {{ time: string, host: string, text: string, sev: 'ok'|'grace'|'alert'|'off', transition: boolean }}
   */
  function parseEvent(raw) {
    if (raw && typeof raw === 'object') {
      // Already structured — trust it
      const e = /** @type {any} */ (raw);
      return {
        time:       e.time       ?? '',
        host:       e.host       ?? '—',
        text:       e.text       ?? String(raw),
        sev:        e.sev        ?? 'ok',
        transition: e.transition ?? false,
      };
    }

    const str = String(raw);

    // Extract bracketed timestamp, e.g. "[10:30:00 AM]"
    const timeMatch = str.match(/^\[([^\]]+)\]/);
    const time      = timeMatch ? timeMatch[1] : '';
    const rest      = timeMatch ? str.slice(timeMatch[0].length).trim() : str;

    // Determine severity from content keywords
    /** @type {'ok'|'grace'|'alert'|'off'} */
    let sev = 'ok';
    if (/fail|error|disconnect/i.test(rest)) sev = 'alert';
    else if (/warn|grace/i.test(rest))        sev = 'grace';

    return { time, host: '', text: rest, sev, transition: false };
  }

  let parsed = $derived(
    appState.events.map(parseEvent)
  );

  let filtered = $derived(
    search
      ? parsed.filter(e =>
          `${e.time} ${e.host} ${e.text}`.toLowerCase().includes(search.toLowerCase())
        )
      : parsed
  );

  // Auto-scroll to top when new events arrive (newest is prepended = top of list).
  // Only scroll if the user hasn't manually scrolled down to read older entries
  // (threshold: within 80px of the top).
  $effect(() => {
    // Access events to create a reactive dependency on the event list length.
    void appState.events.length;
    if (!logEl) return;
    if (logEl.scrollTop < 80) {
      logEl.scrollTop = 0;
    }
  });
</script>

<div class="log-section">
  <div class="section-label">EVENT LOG</div>
  <div class="log-bar">
    <input
      class="log-search"
      type="search"
      placeholder="Filter events..."
      bind:value={search}
    />
    <button class="btn-expand btn-brutal" onclick={() => { expanded = !expanded; }}>
      {expanded ? '⤡ Collapse' : '⤢ Expand'}
    </button>
  </div>

  <div class="log scrollbar-styled" class:expanded bind:this={logEl}>
    {#if expanded}
      <button class="log-restore" onclick={() => { expanded = false; }}>
        Click to collapse · {filtered.length} events
      </button>
      <div class="log-title">EVENT LOG</div>
    {/if}
    {#if !filtered.length}
      <div class="evt-empty">
        {search ? 'No matching events.' : 'Waiting for data...'}
      </div>
    {:else}
      {#each filtered as evt}
        <div class="evt sev-{evt.sev}" class:is-transition={evt.transition}>
          <span class="evt-time">{evt.time}</span>
          {#if evt.host}<span class="evt-host">{evt.host.split('.')[0]}</span>{/if}
          <span class="evt-msg sev-{evt.sev}">{evt.text}</span>
        </div>
      {/each}
    {/if}
  </div>
</div>

<style>
  .log-section { margin-bottom: 24px; }

  .section-label {
    font-family: 'JetBrains Mono', monospace;
    font-size: 0.65rem;
    font-weight: 700;
    letter-spacing: 0.12em;
    text-transform: uppercase;
    color: var(--color-muted);
    margin-bottom: 8px;
  }

  .log-bar {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-bottom: 10px;
  }

  .log-search {
    flex: 1;
    font-family: 'JetBrains Mono', monospace;
    font-size: 0.78rem;
    padding: 8px 12px;
    background: var(--color-card);
    color: var(--color-fg);
    border: var(--spacing-bw) solid var(--color-border);
    border-radius: var(--radius-default);
    outline: none;
  }

  .log-search:focus {
    box-shadow: 0 0 0 2px var(--color-accent);
  }

  .log-search::placeholder {
    color: var(--color-subtle);
  }

  .btn-expand {
    font-family: 'Work Sans', sans-serif;
    font-size: 0.72rem;
    font-weight: 700;
    padding: 6px 14px;
    background: var(--color-card);
    color: var(--color-fg);
    white-space: nowrap;
    border: var(--spacing-bw) solid var(--color-border);
    border-radius: var(--radius-default);
    box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow);
    cursor: pointer;
    transition: box-shadow 0.12s, transform 0.12s;
  }

  .btn-expand:active {
    box-shadow: 1px 1px 0 var(--color-shadow);
    transform: translate(1px, 1px);
  }

  .log {
    background: var(--color-code-bg);
    border: var(--spacing-bw) solid var(--color-border);
    border-radius: var(--radius-default);
    box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow);
    height: 280px;
    overflow-y: auto;
    padding: 14px 16px;
  }

  .log.expanded {
    position: fixed;
    inset: 0;
    z-index: 200;
    height: 100vh;
    border-radius: 0;
    box-shadow: none;
    border: none;
  }

  .log-restore {
    display: block;
    width: 100%;
    text-align: center;
    padding: 8px;
    font-family: 'JetBrains Mono', monospace;
    font-size: 0.7rem;
    color: #c0a0a0;
    border: none;
    border-bottom: 1px solid rgba(255, 200, 200, 0.12);
    background: transparent;
    cursor: pointer;
    margin-bottom: 8px;
  }

  .log-restore:hover {
    color: #f0d8d8;
  }

  .log-title {
    font-family: 'JetBrains Mono', monospace;
    font-size: 0.72rem;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.1em;
    color: #c0a0a0;
    margin-bottom: 10px;
    padding-bottom: 8px;
    border-bottom: 1px solid rgba(255, 200, 200, 0.12);
  }

  .evt {
    font-family: 'JetBrains Mono', monospace;
    font-size: 0.74rem;
    line-height: 1.8;
    color: #efe0e0;
    padding: 4px 8px;
    border-bottom: 1px solid rgba(255, 200, 200, 0.08);
    border-left: 3px solid transparent;
  }

  .evt.sev-alert     { border-left-color: var(--color-red);   }
  .evt.sev-grace     { border-left-color: var(--color-amber); }
  .evt.is-transition { font-weight: 700; }

  .evt-time {
    color: #d4baba;
  }

  .evt-host {
    color: #f0b8c8;
    font-weight: 700;
    margin: 0 4px;
  }

  .evt-msg {
    color: #efe0e0;
  }

  .evt-msg.sev-alert { color: var(--color-red);   }
  .evt-msg.sev-grace { color: var(--color-amber); }

  .evt-empty {
    color: var(--color-subtle);
    font-style: italic;
    font-family: 'Work Sans', sans-serif;
    font-size: 0.82rem;
    padding: 20px 0;
    text-align: center;
  }
</style>
