<script>
  import { TRIGGER_LABELS, repeatLabel } from '../lib/notify.js';
  import { Pencil, Trash2, Plus, ChevronLeft, ChevronRight, Search } from 'lucide-svelte';

  let { targets = $bindable([]),
        editTarget = $bindable(null),
        editIdx = $bindable(-1),
        deleteIdx = $bindable(-1) } = $props();

  const PAGE_SIZE = 5;
  let page = $state(0);
  let search = $state('');

  /** Filtered targets with their original indices preserved. */
  let filtered = $derived.by(() => {
    const q = search.trim().toLowerCase();
    if (!q) return (targets || []).map((t, i) => ({ t, idx: i }));
    return (targets || []).map((t, i) => ({ t, idx: i })).filter(({ t }) => {
      const dest = t.type === 'email' ? (t.to || []).join(' ') : (t.url || '');
      const triggers = (t.triggers || []).map(tr => TRIGGER_LABELS[tr] || tr).join(' ');
      const haystack = `${t.type} ${dest} ${triggers} ${repeatLabel(t.repeat_minutes || 0)}`.toLowerCase();
      return haystack.includes(q);
    });
  });

  let totalPages = $derived(Math.max(1, Math.ceil(filtered.length / PAGE_SIZE)));
  let pagedItems = $derived.by(() => {
    const start = page * PAGE_SIZE;
    return filtered.slice(start, start + PAGE_SIZE);
  });
  let emptyRows = $derived(PAGE_SIZE - pagedItems.length);

  // Reset page when search changes or targets shrink.
  $effect(() => { search; if (page >= totalPages) page = Math.max(0, totalPages - 1); });

  function openEdit(idx) {
    editIdx = idx;
    editTarget = idx >= 0 ? JSON.parse(JSON.stringify(targets[idx]))
      : { type: 'webhook', url: '', to: [], from: '', secret: '', triggers: ['drain_on','drain_off','alert','healthy'], repeat_minutes: 0, enabled: true };
    if (editTarget.enabled == null) editTarget.enabled = true;
  }
</script>

<div class="settings-group">
  <div class="tgt-header">
    <div class="settings-label" style="margin-bottom:0">Notification Targets</div>
    <div class="tgt-search-wrap" style={targets?.length > PAGE_SIZE ? '' : 'visibility:hidden'}>
      <Search size={13} />
      <input class="tgt-search" type="search" placeholder="Filter targets..." bind:value={search} />
    </div>
  </div>

  <div class="target-tbl-wrap">
    <table class="target-tbl">
      <thead>
        <tr>
          <th></th>
          <th>Type</th>
          <th class="col-dest">Destination</th>
          <th>Triggers</th>
          <th>Repeat</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        {#if !targets?.length}
          <tr class="empty-row"><td colspan="6" class="target-tbl-empty">No notification targets configured.</td></tr>
          {#each { length: PAGE_SIZE - 1 } as _}<tr class="empty-row"><td colspan="6">&nbsp;</td></tr>{/each}
        {:else if pagedItems.length === 0}
          <tr class="empty-row"><td colspan="6" class="target-tbl-empty">No targets match "{search}"</td></tr>
          {#each { length: PAGE_SIZE - 1 } as _}<tr class="empty-row"><td colspan="6">&nbsp;</td></tr>{/each}
        {:else}
          {#each pagedItems as { t, idx }}
            {@const trigs = t.triggers || []}
            <tr>
              <td><span class="status-dot {t.enabled !== false ? 'on' : 'off'}"></span></td>
              <td><span class="pill-type {t.type === 'ntfy' ? 'pill-type-ntfy' : t.type === 'email' ? 'pill-type-email' : ''}">{t.type === 'ntfy' ? 'Ntfy' : t.type === 'email' ? 'Email' : 'Webhook'}</span></td>
              <td class="td-dest" title={t.type === 'email' ? (t.to||[]).join(', ') : t.url}>{t.type === 'email' ? (t.to||[]).join(', ') : t.url}</td>
              <td>
                {#if trigs.length <= 2}
                  <span class="tgt-triggers">{#each trigs as tr}<span class="tgt-pill">{TRIGGER_LABELS[tr] || tr}</span>{/each}</span>
                {:else}
                  <span class="tgt-triggers"><span class="tgt-pill">{TRIGGER_LABELS[trigs[0]] || trigs[0]}</span><span class="tgt-pill tgt-pill-more" title={trigs.map(tr => TRIGGER_LABELS[tr] || tr).join(', ')}>+{trigs.length - 1}</span></span>
                {/if}
              </td>
              <td class="mono">{repeatLabel(t.repeat_minutes || 0)}</td>
              <td>
                <div class="btn-row">
                  <button class="btn-tbl" onclick={() => openEdit(idx)}><Pencil size={12} /> Edit</button>
                  <button class="btn-tbl btn-tbl-danger" onclick={() => deleteIdx = idx}><Trash2 size={12} /> Delete</button>
                </div>
              </td>
            </tr>
          {/each}
          {#each { length: emptyRows } as _}
            <tr class="empty-row"><td colspan="6">&nbsp;</td></tr>
          {/each}
        {/if}
      </tbody>
    </table>
  </div>

  <div class="tgt-footer">
    <button class="btn-add-target" onclick={() => openEdit(-1)}><Plus size={14} /> Add Target</button>
    <div class="tgt-pager" style={totalPages > 1 ? '' : 'visibility:hidden'}>
      <button class="btn-page" disabled={page === 0} onclick={() => page--}><ChevronLeft size={14} /></button>
      <span class="page-info">{page + 1} / {totalPages}</span>
      <button class="btn-page" disabled={page >= totalPages - 1} onclick={() => page++}><ChevronRight size={14} /></button>
    </div>
  </div>
</div>

<style>
  .tgt-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
  .tgt-search-wrap { display: flex; align-items: center; gap: 6px; color: var(--color-muted); }
  .tgt-search { width: 160px; font-family: 'JetBrains Mono', monospace; font-size: 0.72rem; padding: 5px 8px; background: var(--color-surface); color: var(--color-fg); border: var(--spacing-bw) solid var(--color-border); border-radius: var(--radius-default); outline: none; }
  .tgt-search::placeholder { color: var(--color-subtle); }

  .target-tbl-wrap { border: var(--spacing-bw) solid var(--color-border); border-radius: var(--radius-default); box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow); margin-bottom: 12px; overflow: hidden; }
  .target-tbl { width: 100%; border-collapse: collapse; font-size: 13px; }
  .target-tbl th, .target-tbl td { padding: 11px 14px; white-space: nowrap; }
  .target-tbl th { font-family: 'JetBrains Mono', monospace; font-size: 11px; font-weight: 500; text-transform: uppercase; letter-spacing: 0.5px; color: var(--color-muted); text-align: left; border-bottom: 2px solid var(--color-border); background: var(--color-card); }
  .target-tbl td { border-bottom: 1px solid var(--color-surface); vertical-align: middle; height: 52px; box-sizing: border-box; }
  .col-dest { width: 99%; }
  .td-dest { overflow: hidden; text-overflow: ellipsis; max-width: 0; }
  .target-tbl tbody tr:last-child td { border-bottom: none; }
  .target-tbl tbody tr:not(.empty-row):hover td { background: var(--color-surface); }
  .empty-row td { height: 52px; box-sizing: border-box; }
  .target-tbl-empty { text-align: center; color: var(--color-subtle); font-size: 13px; }
  .tgt-triggers { display: flex; flex-wrap: nowrap; gap: 2px; overflow: hidden; }
  .pill-type { display: inline-block; font-family: 'JetBrains Mono', monospace; font-size: 10px; font-weight: 700; padding: 3px 10px; border-radius: 4px; text-transform: uppercase; letter-spacing: 0.8px; border: 2px solid var(--color-border); box-shadow: 2px 2px 0 var(--color-shadow); background: var(--color-accent); color: #fff; }
  .pill-type-ntfy { background: var(--color-green); }
  .pill-type-email { background: var(--color-amber); }
  .tgt-pill { display: inline-block; font-family: 'JetBrains Mono', monospace; font-size: 10px; padding: 2px 6px; border-radius: 3px; background: var(--color-surface); color: var(--color-muted); white-space: nowrap; }
  .tgt-pill-more { background: var(--color-accent); color: #fff; cursor: help; }
  .status-dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; vertical-align: middle; }
  .status-dot.on { background: var(--color-green); }
  .status-dot.off { background: var(--color-subtle); }
  .btn-row { display: flex; gap: 6px; }
  .btn-tbl { display: inline-flex; align-items: center; gap: 4px; font-family: 'Work Sans', sans-serif; font-size: 11px; font-weight: 700; padding: 4px 9px; border: 2px solid var(--color-border); border-radius: 5px; box-shadow: 2px 2px 0 var(--color-shadow); cursor: pointer; background: var(--color-card); color: var(--color-fg); transition: transform 0.1s, box-shadow 0.1s; white-space: nowrap; }
  .btn-tbl:hover { transform: translate(-1px, -1px); box-shadow: 3px 3px 0 var(--color-shadow); }
  .btn-tbl:active { transform: translate(1px, 1px); box-shadow: 1px 1px 0 var(--color-shadow); }
  .btn-tbl-danger { color: var(--color-red); border-color: var(--color-red); }
  .btn-tbl-danger:hover { background: color-mix(in srgb, var(--color-red) 8%, var(--color-card)); }

  .tgt-footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
  .btn-add-target { display: flex; align-items: center; justify-content: center; gap: 6px; padding: 10px 20px; font-family: 'Work Sans', sans-serif; font-size: 0.82rem; font-weight: 700; background: var(--color-card); color: var(--color-accent); border: var(--spacing-bw) solid var(--color-accent); border-radius: var(--radius-default); box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow); cursor: pointer; transition: transform 0.1s, box-shadow 0.1s, background 0.1s, color 0.1s; }
  .btn-add-target:hover { transform: translate(-2px, -2px); box-shadow: calc(var(--spacing-so) + 2px) calc(var(--spacing-so) + 2px) 0 var(--color-shadow); background: var(--color-accent); color: #fff; }
  .btn-add-target:active { transform: translate(2px, 2px); box-shadow: 1px 1px 0 var(--color-shadow); }

  .tgt-pager { display: flex; align-items: center; gap: 8px; }
  .page-info { font-family: 'JetBrains Mono', monospace; font-size: 0.72rem; font-weight: 600; color: var(--color-muted); }
  .btn-page { display: flex; align-items: center; justify-content: center; width: 28px; height: 28px; padding: 0; border: var(--spacing-bw) solid var(--color-border); border-radius: var(--radius-default); box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow); cursor: pointer; background: var(--color-card); color: var(--color-fg); transition: transform 0.1s, box-shadow 0.1s; }
  .btn-page:hover { transform: translate(-1px, -1px); box-shadow: calc(var(--spacing-so) + 1px) calc(var(--spacing-so) + 1px) 0 var(--color-shadow); }
  .btn-page:active { transform: translate(1px, 1px); box-shadow: 1px 1px 0 var(--color-shadow); }
  .btn-page:disabled { opacity: 0.35; cursor: not-allowed; transform: none !important; box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow) !important; }

  .mono { font-family: 'JetBrains Mono', monospace; font-size: 11px; white-space: nowrap; }
  .settings-group { margin-bottom: 18px; }
  .settings-label { font-size: 0.75rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.08em; color: var(--color-accent); margin-bottom: 6px; }
</style>
