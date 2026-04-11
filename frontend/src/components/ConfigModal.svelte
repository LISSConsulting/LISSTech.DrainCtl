<script>
  import { fetchNotifyConfig, saveNotifyConfig, sendNotifyTest } from '../lib/api.js';
  import { appState } from '../lib/state.svelte.js';
  import NotificationTargets from './NotificationTargets.svelte';

  let { onclose } = $props();

  let config = $state(null);         // working copy
  let original = $state(null);       // snapshot for dirty detection
  let loading = $state(true);
  let saveStatus = $state('');       // 'ok' | 'err' | ''
  let saveMsg = $state('');
  let testStatus = $state('');       // 'ok' | 'err' | ''
  let testMsg = $state('');
  let saving = $state(false);
  let testing = $state(false);

  let dirty = $derived.by(() => {
    if (!config || !original) return false;
    return JSON.stringify(config) !== JSON.stringify(original);
  });

  $effect(() => {
    loadConfig();
  });

  async function loadConfig() {
    loading = true;
    try {
      const c = await fetchNotifyConfig();
      config = JSON.parse(JSON.stringify(c));  // deep clone
      original = JSON.parse(JSON.stringify(c));
    } catch(e) {
      saveMsg = 'Failed to load config: ' + e.message;
      saveStatus = 'err';
    } finally {
      loading = false;
    }
  }

  async function save() {
    if (!config) return;
    saving = true;
    saveStatus = '';
    saveMsg = '';
    try {
      const saved = await saveNotifyConfig(config);
      config = JSON.parse(JSON.stringify(saved));
      original = JSON.parse(JSON.stringify(saved));
      appState.config = saved;
      saveStatus = 'ok';
      saveMsg = 'Settings saved successfully';
      setTimeout(() => { saveStatus = ''; saveMsg = ''; }, 3000);
    } catch(e) {
      saveStatus = 'err';
      saveMsg = 'Save failed: ' + e.message;
    } finally {
      saving = false;
    }
  }

  async function sendTest() {
    testing = true;
    testStatus = '';
    testMsg = '';
    try {
      const r = await sendNotifyTest();
      testStatus = 'ok';
      testMsg = r.message || 'Test notification sent';
      setTimeout(() => { testStatus = ''; testMsg = ''; }, 4000);
    } catch(e) {
      testStatus = 'err';
      testMsg = 'Test failed: ' + e.message;
    } finally {
      testing = false;
    }
  }

  function handleOverlayClick(e) {
    if (e.target !== e.currentTarget) return;
    if (dirty && !confirm('You have unsaved changes. Close without saving?')) return;
    onclose?.();
  }

  function handleClose() {
    if (dirty && !confirm('You have unsaved changes. Close without saving?')) return;
    onclose?.();
  }

  const GRACE_PRESETS = [5, 10, 15, 30, 60, 120, 240];
</script>

<!-- svelte-ignore a11y_click_events_have_key_events a11y_interactive_supports_focus -->
<div class="settings-overlay" onclick={handleOverlayClick} role="dialog" aria-modal="true" tabindex="-1">
  <div class="settings-modal scrollbar-styled" style={dirty ? 'background: color-mix(in srgb, var(--color-amber) 5%, var(--color-card));' : ''}>
    <div class="settings-title">
      <span class="serif">Dashboard Configuration</span>
      <button class="settings-close" onclick={handleClose}>✕</button>
    </div>

    {#if loading}
      <div style="text-align:center;padding:40px;color:var(--color-muted)">Loading...</div>
    {:else if config}

      <!-- Grace Period -->
      <div class="settings-group">
        <div class="settings-label">Grace Period</div>
        <div class="repeat-pills">
          {#each GRACE_PRESETS as p}
            <button
              class="repeat-pill {config.grace_period_minutes === p ? 'active' : ''}"
              onclick={() => config.grace_period_minutes = p}
            >{p < 60 ? p+'m' : (p/60)+'h'}</button>
          {/each}
          <button class="repeat-pill repeat-pill--dashed">Custom</button>
        </div>
        <div style="display:flex;align-items:center;gap:8px">
          <input type="number" class="settings-num" bind:value={config.grace_period_minutes} min="1" max="1440" />
          <span class="settings-num-label">minutes</span>
        </div>
      </div>

      <div class="settings-divider"></div>

      <!-- Session Warning -->
      <div class="settings-group">
        <div class="settings-label">Session Warning Threshold</div>
        <div style="display:flex;align-items:center;gap:8px">
          <input type="number" class="settings-num" bind:value={config.session_warning_threshold} min="0" max="100" />
          <span class="settings-num-label">% of max sessions (0 = disabled)</span>
        </div>
      </div>

      <div class="settings-divider"></div>

      <!-- Performance Monitoring -->
      {#if config.perf_monitoring}
        <div class="settings-group">
          <div class="settings-label">Performance Monitoring</div>
          <label class="settings-check">
            <input type="checkbox" bind:checked={config.perf_monitoring.enabled} />
            Enable performance monitoring
          </label>
          {#if config.perf_monitoring.enabled}
            <div class="settings-cfg-grid" style="margin-top:12px">
              <div>
                <div class="settings-label">CPU Thresholds</div>
                <div style="display:flex;gap:8px;align-items:center;margin-bottom:6px">
                  <span class="settings-num-label">Warn</span>
                  <input type="number" class="settings-num" bind:value={config.perf_monitoring.cpu_warn_pct} min="0" max="100"/>
                  <span class="settings-num-label">%</span>
                  <span class="settings-num-label">Crit</span>
                  <input type="number" class="settings-num" bind:value={config.perf_monitoring.cpu_crit_pct} min="0" max="100"/>
                  <span class="settings-num-label">%</span>
                </div>
                <div class="settings-label">Memory Thresholds</div>
                <div style="display:flex;gap:8px;align-items:center;margin-bottom:6px">
                  <span class="settings-num-label">Warn</span>
                  <input type="number" class="settings-num" bind:value={config.perf_monitoring.mem_warn_pct} min="0" max="100"/>
                  <span class="settings-num-label">%</span>
                  <span class="settings-num-label">Crit</span>
                  <input type="number" class="settings-num" bind:value={config.perf_monitoring.mem_crit_pct} min="0" max="100"/>
                  <span class="settings-num-label">%</span>
                </div>
              </div>
              <div>
                <div class="settings-label">Input Delay Thresholds</div>
                <div style="display:flex;gap:8px;align-items:center;margin-bottom:6px">
                  <span class="settings-num-label">Warn</span>
                  <input type="number" class="settings-num" bind:value={config.perf_monitoring.input_delay_warn_ms} min="0"/>
                  <span class="settings-num-label">ms</span>
                  <span class="settings-num-label">Crit</span>
                  <input type="number" class="settings-num" bind:value={config.perf_monitoring.input_delay_crit_ms} min="0"/>
                  <span class="settings-num-label">ms</span>
                </div>
                <div class="settings-label" style="margin-top:8px">Options</div>
                <label class="settings-check">
                  <input type="checkbox" bind:checked={config.perf_monitoring.per_session_accounting} />
                  Per-session CPU accounting
                </label>
                <label class="settings-check">
                  <input type="checkbox" bind:checked={config.perf_monitoring.remotefx_enabled} />
                  RemoteFX monitoring
                </label>
              </div>
            </div>
          {/if}
        </div>
        <div class="settings-divider"></div>
      {/if}

      <!-- Notification Targets -->
      <NotificationTargets bind:targets={config.targets} />

      <!-- Status messages -->
      {#if saveMsg}
        <div class="settings-status {saveStatus}">{saveMsg}</div>
      {/if}
      {#if testMsg}
        <div class="settings-status {testStatus}" style="margin-top:4px">{testMsg}</div>
      {/if}

      <!-- Actions bar -->
      <div class="settings-actions" style="margin-top:16px;padding-top:16px;border-top:1px solid var(--color-surface)">
        <button class="btn-brutal btn-test" onclick={sendTest} disabled={testing}>
          {testing ? 'Sending...' : '▶ Send Test'}
        </button>
        <div style="display:flex;gap:8px">
          <button class="btn-brutal btn-save" onclick={save} disabled={saving || !dirty}>
            {saving ? 'Saving...' : 'Save'}
          </button>
          <button class="btn-brutal btn-secondary" onclick={handleClose}>Close</button>
        </div>
      </div>
    {/if}
  </div>
</div>

<style>
  .settings-overlay { position: fixed; inset: 0; background: rgba(0,0,0,0.5); z-index: 150; display: flex; justify-content: center; align-items: center; }
  .settings-modal { width: 860px; max-width: 94vw; max-height: 90vh; background: var(--color-card); border: var(--spacing-bw) solid var(--color-border); border-radius: var(--radius-default); box-shadow: 8px 8px 0 var(--color-shadow); overflow-y: auto; padding: 32px 36px; transition: background 0.3s; }
  .settings-title { font-family: 'DM Serif Display', serif; font-size: 1.3rem; margin-bottom: 24px; display: flex; align-items: center; justify-content: space-between; }
  .settings-close { background: none; border: none; font-size: 1.4rem; cursor: pointer; color: var(--color-muted); padding: 4px 8px; }
  .settings-close:hover { color: var(--color-fg); }
  .settings-group { margin-bottom: 18px; }
  .settings-label { font-size: 0.75rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.08em; color: var(--color-accent); margin-bottom: 6px; }
  .settings-check { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; cursor: pointer; font-size: 0.85rem; font-weight: 600; }
  .settings-check input[type="checkbox"] { width: 18px; height: 18px; accent-color: var(--color-accent); cursor: pointer; }
  .settings-num { width: 80px; font-family: 'JetBrains Mono', monospace; font-size: 0.8rem; padding: 8px 12px; background: var(--color-surface); color: var(--color-fg); border: var(--spacing-bw) solid var(--color-border); border-radius: var(--radius-default); outline: none; }
  .settings-num:focus { box-shadow: 0 0 0 2px var(--color-accent); }
  .settings-num-label { font-size: 0.75rem; color: var(--color-muted); }
  .settings-divider { height: 1px; background: var(--color-border); margin: 18px 0; opacity: 0.4; }
  .settings-cfg-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 28px; margin-bottom: 24px; }
  .settings-status { font-size: 0.78rem; margin-top: 10px; font-weight: 600; padding: 8px 12px; border-radius: var(--radius-default); }
  .settings-status.ok { color: var(--color-green); background: color-mix(in srgb, var(--color-green) 10%, var(--color-card)); border: 1px solid var(--color-green); }
  .settings-status.err { color: var(--color-red); background: color-mix(in srgb, var(--color-red) 10%, var(--color-card)); border: 1px solid var(--color-red); }
  .settings-actions { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 12px; }
  .repeat-pills { display: flex; gap: 6px; margin-bottom: 10px; flex-wrap: wrap; }
  .repeat-pill { font-family: 'Work Sans', sans-serif; font-size: 0.72rem; font-weight: 600; padding: 5px 12px; background: var(--color-card); color: var(--color-muted); border: var(--spacing-bw) solid var(--color-border); border-radius: 20px; cursor: pointer; transition: all 0.15s; }
  .repeat-pill:hover { border-color: var(--color-accent); color: var(--color-fg); }
  .repeat-pill.active { background: var(--color-accent); color: #fff; border-color: var(--color-accent); }
  .repeat-pill--dashed { border-style: dashed; font-size: 0.7rem; }
  .btn-save { background: var(--color-accent); color: #fff; padding: 10px 22px; font-family: 'Work Sans', sans-serif; font-size: 0.85rem; font-weight: 700; }
  .btn-save:disabled { opacity: 0.5; cursor: not-allowed; transform: none!important; box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow)!important; }
  .btn-test { background: var(--color-card); color: var(--color-fg); padding: 10px 22px; font-family: 'Work Sans', sans-serif; font-size: 0.85rem; font-weight: 700; }
  .btn-test:disabled { opacity: 0.5; cursor: not-allowed; transform: none!important; box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow)!important; }
  .btn-secondary { background: var(--color-card); color: var(--color-fg); padding: 10px 22px; font-family: 'Work Sans', sans-serif; font-size: 0.85rem; font-weight: 700; }
</style>
