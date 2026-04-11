<script>
  import { TRIGGER_LABELS, repeatLabel } from '../lib/notify.js';
  import { Pencil, Trash2, Plus } from 'lucide-svelte';

  let { targets = $bindable([]),
        editTarget = $bindable(null),
        editIdx = $bindable(-1),
        deleteIdx = $bindable(-1) } = $props();

  function openEdit(idx) {
    editIdx = idx;
    editTarget = idx >= 0 ? JSON.parse(JSON.stringify(targets[idx]))
      : { type: 'webhook', url: '', to: [], from: '', secret: '', triggers: ['drain_on','drain_off','alert','healthy'], repeat_minutes: 0, enabled: true };
    // Normalize enabled: absent/null → true so the checkbox renders correctly.
    if (editTarget.enabled == null) editTarget.enabled = true;
  }

  function saveTarget(t) {
    if (editIdx >= 0) {
      targets = targets.map((x, i) => i === editIdx ? t : x);
    } else {
      targets = [...targets, { ...t, id: crypto.randomUUID() }];
    }
    editTarget = null;
  }

  function confirmDelete() {
    if (deleteIdx >= 0) {
      targets = targets.filter((_, i) => i !== deleteIdx);
    }
    deleteIdx = -1;
  }
</script>

<div class="settings-group">
  <div class="settings-label">Notification Targets</div>

  <div class="target-tbl-wrap">
    {#if !targets || targets.length === 0}
      <div class="target-tbl-empty">No notification targets configured.</div>
    {:else}
      <table class="target-tbl">
        <thead>
          <tr>
            <th></th>
            <th>Type</th>
            <th>Destination</th>
            <th>Triggers</th>
            <th>Repeat</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {#each targets as t, i}
            <tr>
              <td><span class="status-dot {t.enabled !== false ? 'on' : 'off'}"></span></td>
              <td><span class="pill-type {t.type === 'ntfy' ? 'pill-type-ntfy' : t.type === 'email' ? 'pill-type-email' : ''}">{t.type === 'ntfy' ? 'Ntfy' : t.type === 'email' ? 'Email' : 'Webhook'}</span></td>
              <td><span class="tgt-truncate" title={t.type === 'email' ? (t.to||[]).join(', ') : t.url}>{t.type === 'email' ? (t.to||[]).join(', ') : t.url}</span></td>
              <td>
                {#each (t.triggers || []) as tr}
                  <span class="tgt-pill">{TRIGGER_LABELS[tr] || tr}</span>
                {/each}
              </td>
              <td class="mono">{repeatLabel(t.repeat_minutes || 0)}</td>
              <td>
                <div class="btn-row">
                  <button class="btn-tbl" onclick={() => openEdit(i)}><Pencil size={12} /> Edit</button>
                  <button class="btn-tbl btn-tbl-danger" onclick={() => deleteIdx = i}><Trash2 size={12} /> Delete</button>
                </div>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </div>

  <button class="btn-add-target" onclick={() => openEdit(-1)}><Plus size={14} /> Add Target</button>
</div>


<style>
  .target-tbl-wrap { border: 1.5px solid color-mix(in srgb, var(--color-border) 60%, transparent); border-radius: 8px; overflow: hidden; margin-bottom: 12px; }
  .target-tbl { width: 100%; border-collapse: collapse; font-size: 13px; }
  .target-tbl th { font-family: 'JetBrains Mono', monospace; font-size: 11px; font-weight: 500; text-transform: uppercase; letter-spacing: 0.5px; color: var(--color-muted); text-align: left; padding: 8px 12px; border-bottom: 2px solid var(--color-border); background: var(--color-card); }
  .target-tbl td { padding: 10px 12px; border-bottom: 1px solid var(--color-surface); vertical-align: middle; }
  .target-tbl tr:last-child td { border-bottom: none; }
  .target-tbl tr:hover td { background: var(--color-surface); }
  .target-tbl-empty { text-align: center; padding: 32px; color: var(--color-subtle); font-size: 13px; }
  .pill-type { display: inline-block; font-family: 'JetBrains Mono', monospace; font-size: 10px; font-weight: 700; padding: 3px 10px; border-radius: 4px; text-transform: uppercase; letter-spacing: 0.8px; border: 2px solid var(--color-border); box-shadow: 2px 2px 0 var(--color-shadow); background: var(--color-accent); color: #fff; }
  .pill-type-ntfy { background: var(--color-green); }
  .pill-type-email { background: var(--color-amber); }
  .tgt-pill { display: inline-block; font-family: 'JetBrains Mono', monospace; font-size: 10px; padding: 2px 6px; border-radius: 3px; background: var(--color-surface); color: var(--color-muted); margin: 1px 2px; }
  .tgt-truncate { max-width: 200px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; display: inline-block; vertical-align: middle; font-family: 'JetBrains Mono', monospace; font-size: 12px; }
  .status-dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; vertical-align: middle; }
  .status-dot.on { background: var(--color-green); }
  .status-dot.off { background: var(--color-subtle); }
  .btn-row { display: flex; gap: 10px; }
  .btn-tbl { display: inline-flex; align-items: center; gap: 4px; font-family: 'Work Sans', sans-serif; font-size: 11px; font-weight: 700; padding: 5px 12px; border: var(--spacing-bw) solid var(--color-border); border-radius: var(--radius-default); box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow); cursor: pointer; background: var(--color-card); color: var(--color-fg); transition: transform 0.1s, box-shadow 0.1s; }
  .btn-tbl:hover { transform: translate(-2px, -2px); box-shadow: calc(var(--spacing-so) + 2px) calc(var(--spacing-so) + 2px) 0 var(--color-shadow); }
  .btn-tbl:active { transform: translate(2px, 2px); box-shadow: 1px 1px 0 var(--color-shadow); }
  .btn-tbl-danger { color: var(--color-red); border-color: var(--color-red); }
  .btn-tbl-danger:hover { background: color-mix(in srgb, var(--color-red) 8%, var(--color-card)); }
  .btn-add-target { display: flex; align-items: center; justify-content: center; gap: 6px; width: 50%; padding: 10px; font-family: 'Work Sans', sans-serif; font-size: 0.82rem; font-weight: 700; background: var(--color-card); color: var(--color-accent); border: var(--spacing-bw) solid var(--color-accent); border-radius: var(--radius-default); box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow); cursor: pointer; transition: transform 0.1s, box-shadow 0.1s, background 0.1s, color 0.1s; }
  .btn-add-target:hover { transform: translate(-2px, -2px); box-shadow: calc(var(--spacing-so) + 2px) calc(var(--spacing-so) + 2px) 0 var(--color-shadow); background: var(--color-accent); color: #fff; }
  .btn-add-target:active { transform: translate(2px, 2px); box-shadow: 1px 1px 0 var(--color-shadow); }
  .mono { font-family: 'JetBrains Mono', monospace; }
  .settings-group { margin-bottom: 18px; }
  .settings-label { font-size: 0.75rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.08em; color: var(--color-accent); margin-bottom: 6px; }
</style>
