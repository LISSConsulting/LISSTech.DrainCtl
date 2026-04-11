<script>
  import { appState } from '../lib/state.svelte.js';
  import { deleteServer } from '../lib/api.js';
  import ServerDetail from './ServerDetail.svelte';

  let { onhistoryclick } = $props();

  let expandedHost = $state(null);
  let sortCol = $state('status');
  let sortDir = $state(1); // 1 = asc, -1 = desc
  let search = $state(localStorage.getItem('drainctl-search') || '');
  let statusFilter = $state('all');
  let removeError = $state('');

  const STATUS_ORDER = { alert: 0, grace: 1, off: 2, ok: 3 };

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

  /**
   * Returns a human-readable countdown string for a grace deadline ISO timestamp.
   * @param {string|null|undefined} iso
   * @returns {string|null}
   */
  function graceCountdown(iso) {
    if (!iso) return null;
    const d = new Date(iso);
    if (isNaN(d)) return null;
    const ms = d - Date.now();
    if (ms <= 0) return 'expired';
    const s = Math.floor(ms / 1000);
    if (s < 60) return s + 's left';
    const m = Math.floor(s / 60);
    if (m < 60) return m + 'm left';
    const h = Math.floor(m / 60);
    const rem = m % 60;
    return h + 'h ' + (rem > 0 ? rem + 'm ' : '') + 'left';
  }

  function modeLabel(m) {
    const ML = {
      ALLOW_ALL_CONNECTIONS: 'Open',
      ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS: 'Drain',
      ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS_UNTIL_RESTART: 'Drain (Restart)',
      none: 'Open', graceful: 'Graceful', immediate: 'Immediate'
    };
    return ML[m] || m || '—';
  }

  function statusLabel(s) {
    return { ok: 'Healthy', grace: 'Grace', alert: 'Alert', off: 'Offline' }[s] || s;
  }

  let sorted = $derived.by(() => {
    let s = appState.servers.filter(sv => {
      const matchText = !search || sv.host.toLowerCase().includes(search.toLowerCase());
      const matchStatus = statusFilter === 'all' || sv.status === statusFilter;
      return matchText && matchStatus;
    });
    return s.sort((a, b) => {
      let va, vb;
      if (sortCol === 'status') { va = STATUS_ORDER[a.status] ?? 4; vb = STATUS_ORDER[b.status] ?? 4; }
      else if (sortCol === 'host') { va = a.host; vb = b.host; }
      else if (sortCol === 'sessions') { va = a.sessions || 0; vb = b.sessions || 0; }
      else if (sortCol === 'cpu') { va = a.perf?.cpu_pct || 0; vb = b.perf?.cpu_pct || 0; }
      else if (sortCol === 'last_seen') { va = new Date(a.last_seen || 0); vb = new Date(b.last_seen || 0); }
      else { va = a[sortCol]; vb = b[sortCol]; }
      if (va < vb) return -sortDir;
      if (va > vb) return sortDir;
      return 0;
    });
  });

  function sort(col) {
    if (sortCol === col) sortDir = -sortDir;
    else { sortCol = col; sortDir = 1; }
  }

  function toggleRow(host) {
    expandedHost = expandedHost === host ? null : host;
  }

  async function removeServer(host) {
    if (!confirm('Remove ' + host + ' from the dashboard?')) return;
    try {
      await deleteServer(host);
      appState.servers = appState.servers.filter(s => s.host !== host);
    } catch(e) {
      removeError = 'Remove failed: ' + e.message;
      setTimeout(() => removeError = '', 5000);
    }
  }

  // Persist search
  $effect(() => {
    localStorage.setItem('drainctl-search', search);
  });
</script>

<div class="grid">
  <div class="section-label">SERVERS</div>

  <!-- Filter bar -->
  <div class="filter-bar">
    <input class="srv-search settings-input" type="search" placeholder="Filter by hostname..." bind:value={search} style="max-width:300px" />
    <div class="filter-pills">
      {#each ['all', 'ok', 'grace', 'alert', 'off'] as f}
        <button class="filter-pill {f === 'all' ? '' : f} {statusFilter === f ? 'active' : ''}" onclick={() => statusFilter = f}>
          {f === 'all' ? 'All' : statusLabel(f)}
        </button>
      {/each}
    </div>
  </div>

  {#if removeError}
    <div class="grid-toast">{removeError}</div>
  {/if}

  {#if !appState.servers.length}
    <div class="empty">
      <h2 class="serif">No servers registered</h2>
      <p>Waiting for agents to connect...</p>
    </div>
  {:else if !sorted.length}
    <div class="empty"><p>No servers match the current filter.</p></div>
  {:else}
    <div class="card">
      <table class="srv-tbl">
        <thead>
          <tr>
            <th></th>
            <th onclick={() => sort('host')} class="sortable">Host {sortCol === 'host' ? (sortDir === 1 ? '↑' : '↓') : ''}</th>
            <th onclick={() => sort('status')} class="sortable">Status {sortCol === 'status' ? (sortDir === 1 ? '↑' : '↓') : ''}</th>
            <th>Mode</th>
            <th>Since</th>
            <th onclick={() => sort('sessions')} class="sortable">Sessions {sortCol === 'sessions' ? (sortDir === 1 ? '↑' : '↓') : ''}</th>
            <th onclick={() => sort('cpu')} class="sortable">CPU {sortCol === 'cpu' ? (sortDir === 1 ? '↑' : '↓') : ''}</th>
            <th>Mem Free</th>
            <th>Input Delay</th>
            <th onclick={() => sort('last_seen')} class="sortable">Last Seen {sortCol === 'last_seen' ? (sortDir === 1 ? '↑' : '↓') : ''}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {#each sorted as srv (srv.host)}
            <tr class="clickable {expandedHost === srv.host ? 'sel' : ''}"
                data-host={srv.host} data-status={srv.status}
                onclick={() => toggleRow(srv.host)}>
              <td><span class="dot {srv.status}"></span></td>
              <td class="mono fw7">{srv.host.split('.')[0]}</td>
              <td>
                <span class="pill {srv.status}">{statusLabel(srv.status)}</span>
                {#if srv.status === 'grace'}
                  {@const cd = graceCountdown(srv.grace_deadline)}
                  {#if cd}
                    <span class="grace-cd {cd === 'expired' ? 'grace-cd--expired' : ''}">{cd}</span>
                  {/if}
                {/if}
              </td>
              <td class="mono">{modeLabel(srv.drain_mode)}</td>
              <td class="mono muted">{rel(srv.registered_at)}</td>
              <td class="mono">{srv.sessions ?? '—'}</td>
              <td class="mono">{srv.perf ? srv.perf.cpu_pct.toFixed(1) + '%' : '—'}</td>
              <td class="mono">{srv.perf ? (srv.perf.mem_free_mb / 1024).toFixed(1) + ' GB' : '—'}</td>
              <td class="mono">{srv.perf ? srv.perf.input_delay_ms + 'ms' : '—'}</td>
              <td class="mono muted">{rel(srv.last_seen)}</td>
              <td onclick={(e) => e.stopPropagation()}>
                <div class="btn-row">
                  <button class="btn-hist" onclick={() => onhistoryclick?.(srv.host)}>History</button>
                  <button class="btn-rm" onclick={() => removeServer(srv.host)}>✕</button>
                </div>
              </td>
            </tr>
            {#if expandedHost === srv.host}
              <tr class="detail-row">
                <td colspan="11">
                  <ServerDetail server={srv} />
                </td>
              </tr>
            {/if}
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<style>
  .grid { margin-bottom: 24px; }
  .filter-bar { display: flex; align-items: center; gap: 12px; margin-bottom: 12px; flex-wrap: wrap; }
  .filter-pills { display: flex; gap: 6px; }
  .filter-pill { font-family: 'JetBrains Mono', monospace; font-size: 0.68rem; font-weight: 700; padding: 4px 10px; border-radius: 20px; border: var(--spacing-bw) solid var(--color-border); background: var(--color-card); color: var(--color-muted); cursor: pointer; text-transform: uppercase; letter-spacing: 0.06em; transition: all 0.15s; }
  .filter-pill.active { background: var(--color-fg); color: var(--color-bg); }
  .filter-pill.ok.active { background: var(--color-green); color: #fff; border-color: var(--color-green); }
  .filter-pill.grace.active { background: var(--color-amber); color: #fff; border-color: var(--color-amber); }
  .filter-pill.alert.active { background: var(--color-red); color: #fff; border-color: var(--color-red); }
  .filter-pill.off.active { background: var(--color-subtle); color: #fff; border-color: var(--color-subtle); }
  .grid-toast { padding: 8px 14px; margin-bottom: 10px; background: var(--color-card); color: var(--color-red); border: 1.5px solid var(--color-red); border-radius: var(--radius-default); font-size: 0.8rem; font-family: 'JetBrains Mono', monospace; }
  .empty { text-align: center; padding: 50px 20px; color: var(--color-muted); }
  .empty h2 { font-size: 1.3rem; font-weight: 400; margin-bottom: 4px; color: var(--color-fg); }
  table.srv-tbl { width: 100%; border-collapse: separate; border-spacing: 0; font-size: 13px; }
  table.srv-tbl th { font-family: 'JetBrains Mono', monospace; font-size: 11px; font-weight: 500; text-transform: uppercase; letter-spacing: 0.5px; color: var(--color-muted); text-align: left; padding: 8px 12px; border-bottom: var(--spacing-bw) solid var(--color-border); white-space: nowrap; background: var(--color-card); }
  table.srv-tbl td { padding: 9px 12px; border-bottom: 1px solid var(--color-surface); vertical-align: middle; }
  th.sortable { cursor: pointer; user-select: none; }
  th.sortable:hover { color: var(--color-accent); }
  .clickable { cursor: pointer; }
  .clickable:hover td { background: var(--color-surface); }
  .sel td { background: color-mix(in srgb, var(--color-accent) 8%, var(--color-card)) !important; }
  .dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 6px; vertical-align: middle; }
  .dot.ok { background: var(--color-green); }
  .dot.grace { background: var(--color-amber); }
  .dot.alert { background: var(--color-red); }
  .dot.off { background: var(--color-subtle); }
  .mono { font-family: 'JetBrains Mono', monospace; }
  .muted { color: var(--color-muted); }
  .fw7 { font-weight: 700; }
  .btn-row { display: flex; gap: 6px; }
  .btn-hist { font-family: 'Work Sans', sans-serif; font-size: 0.7rem; font-weight: 700; padding: 3px 10px; background: var(--color-card); color: var(--color-accent); border: 1.5px solid var(--color-accent); border-radius: 4px; cursor: pointer; transition: background 0.1s, color 0.1s; }
  .btn-hist:hover { background: var(--color-accent); color: #fff; }
  .btn-rm { font-family: 'Work Sans', sans-serif; font-size: 0.7rem; font-weight: 700; padding: 3px 10px; background: var(--color-card); color: var(--color-red); border: 1.5px solid var(--color-red); border-radius: 4px; cursor: pointer; transition: background 0.1s, color 0.1s; }
  .btn-rm:hover { background: var(--color-red); color: #fff; }
  .detail-row td { padding: 0 !important; border-bottom: var(--spacing-bw) solid var(--color-surface) !important; }
  .pill { font-family: 'JetBrains Mono', monospace; font-size: 0.6rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.06em; padding: 2px 8px; border-radius: 4px; color: #fff; }
  .pill.ok { background: var(--color-green); }
  .pill.grace { background: var(--color-amber); }
  .pill.alert { background: var(--color-red); }
  .pill.off { background: var(--color-subtle); }
  .section-label { font-family: 'JetBrains Mono', monospace; font-size: 0.65rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.12em; color: var(--color-muted); margin-bottom: 10px; }
  .grace-cd { font-family: 'JetBrains Mono', monospace; font-size: 0.6rem; font-weight: 700; color: var(--color-amber); margin-left: 6px; white-space: nowrap; }
  .grace-cd--expired { color: var(--color-red); }
</style>
