<script>
  import { fetchNotifyConfig, saveNotifyConfig, sendNotifyTest } from '../lib/api.js';
  import { appState } from '../lib/state.svelte.js';
  import { toast } from '../lib/toast.svelte.js';
  import NotificationTargets from './NotificationTargets.svelte';

  let { onclose } = $props();

  let config = $state(null);         // working copy
  let original = $state(null);       // snapshot for dirty detection
  let loading = $state(true);
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
      config = JSON.parse(JSON.stringify(c));
      original = JSON.parse(JSON.stringify(c));
    } catch(e) {
      toast.err('Failed to load config: ' + e.message);
    } finally {
      loading = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Fire-based threshold presets
  // ---------------------------------------------------------------------------

  const FIRE_PRESETS = [
    { level: 1, label: 'Relaxed',  cpu_warn: 80, cpu_crit: 95, mem_warn: 80, mem_crit: 95, delay_warn: 50, delay_crit: 100 },
    { level: 2, label: 'Balanced', cpu_warn: 70, cpu_crit: 90, mem_warn: 70, mem_crit: 90, delay_warn: 30, delay_crit: 80  },
    { level: 3, label: 'Strict',   cpu_warn: 60, cpu_crit: 80, mem_warn: 60, mem_crit: 80, delay_warn: 15, delay_crit: 40  },
  ];

  /** Detect which fire preset matches current config, or -1 for custom. */
  let activeFireLevel = $derived.by(() => {
    const p = config?.performance;
    if (!p) return -1;
    for (const preset of FIRE_PRESETS) {
      if (p.cpu_warn_pct === preset.cpu_warn && p.cpu_crit_pct === preset.cpu_crit &&
          p.mem_warn_pct === preset.mem_warn && p.mem_crit_pct === preset.mem_crit &&
          p.input_delay_warn_ms === preset.delay_warn && p.input_delay_crit_ms === preset.delay_crit) {
        return preset.level;
      }
    }
    return -1;
  });

  function applyFirePreset(preset) {
    if (!config?.performance) return;
    config.performance.cpu_warn_pct = preset.cpu_warn;
    config.performance.cpu_crit_pct = preset.cpu_crit;
    config.performance.mem_warn_pct = preset.mem_warn;
    config.performance.mem_crit_pct = preset.mem_crit;
    config.performance.input_delay_warn_ms = preset.delay_warn;
    config.performance.input_delay_crit_ms = preset.delay_crit;
  }

  /** Show/hide manual threshold editor. */
  let showManualThresholds = $state(false);

  // ---------------------------------------------------------------------------
  // Validation
  // ---------------------------------------------------------------------------

  /**
   * Validate threshold pairs: warn must be strictly less than crit.
   * @returns {string|null}
   */
  function validateThresholds() {
    const p = config?.performance;
    if (!p?.enabled) return null;
    if (p.cpu_warn_pct > 0 && p.cpu_crit_pct > 0 && p.cpu_warn_pct >= p.cpu_crit_pct)
      return 'CPU warn threshold must be less than crit threshold.';
    if (p.mem_warn_pct > 0 && p.mem_crit_pct > 0 && p.mem_warn_pct >= p.mem_crit_pct)
      return 'Memory warn threshold must be less than crit threshold.';
    if (p.input_delay_warn_ms > 0 && p.input_delay_crit_ms > 0 && p.input_delay_warn_ms >= p.input_delay_crit_ms)
      return 'Input Delay warn threshold must be less than crit threshold.';
    return null;
  }

  async function save() {
    if (!config) return;
    const validationErr = validateThresholds();
    if (validationErr) {
      toast.err(validationErr);
      return;
    }
    saving = true;
    try {
      await saveNotifyConfig(config);
      original = JSON.parse(JSON.stringify(config));
      appState.config = JSON.parse(JSON.stringify(config));
      toast.ok('Settings saved successfully');
    } catch(e) {
      toast.err('Save failed: ' + e.message);
    } finally {
      saving = false;
    }
  }

  async function sendTest() {
    testing = true;
    try {
      const r = await sendNotifyTest();
      toast.ok(r.message || 'Test notification sent');
    } catch(e) {
      toast.err('Test failed: ' + e.message);
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

  $effect(() => {
    function onKey(e) { if (e.key === 'Escape') handleClose(); }
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  });

  const GRACE_PRESETS = [5, 10, 15, 30, 60, 120, 240];

  /** @type {HTMLInputElement|null} */
  let gracePeriodInput = $state(null);
</script>

<!-- svelte-ignore a11y_click_events_have_key_events a11y_interactive_supports_focus -->
<div class="settings-overlay" onclick={handleOverlayClick} role="dialog" aria-modal="true" tabindex="-1">
  <div class="settings-modal scrollbar-styled" style={dirty ? 'background: color-mix(in srgb, var(--color-amber) 5%, var(--color-card));' : ''}>
    <div class="settings-title">
      <span class="serif">Dashboard Configuration</span>
      <button class="settings-close" onclick={handleClose} aria-label="Close settings">✕</button>
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
              class="repeat-pill {config.grace_period === p ? 'active' : ''}"
              onclick={() => config.grace_period = p}
            >{p < 60 ? p+'m' : (p/60)+'h'}</button>
          {/each}
          <button
            class="repeat-pill repeat-pill--dashed {!GRACE_PRESETS.includes(config.grace_period) ? 'active' : ''}"
            onclick={() => gracePeriodInput?.focus()}
          >Custom</button>
        </div>
        <div style="display:flex;align-items:center;gap:8px">
          <input type="number" class="settings-num" bind:value={config.grace_period} bind:this={gracePeriodInput} min="1" max="1440" />
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
      {#if config.performance}
        <div class="settings-group">
          <div class="settings-label">Performance Monitoring</div>
          <label class="settings-check">
            <input type="checkbox" bind:checked={config.performance.enabled} disabled={config.performance.force_disabled} />
            Enable performance monitoring{config.performance.force_disabled ? ' (disabled by server policy)' : ''}
          </label>
          {#if config.performance.enabled && !config.performance.force_disabled}
            <!-- Fire threshold presets -->
            <div class="fire-presets" style="margin-top:12px">
              <div class="settings-label">Alert Sensitivity</div>
              <div class="fire-row">
                {#each FIRE_PRESETS as preset}
                  <button
                    class="fire-card {activeFireLevel === preset.level ? 'active' : ''}"
                    onclick={() => applyFirePreset(preset)}
                  >
                    <span class="fire-icons">{#each { length: preset.level } as _}🔥{/each}</span>
                    <span class="fire-label">{preset.label}</span>
                    <span class="fire-detail">
                      CPU {preset.cpu_warn}/{preset.cpu_crit}%
                      · Mem {preset.mem_warn}/{preset.mem_crit}%
                      · Delay {preset.delay_warn}/{preset.delay_crit}ms
                    </span>
                  </button>
                {/each}
              </div>

              <button
                class="fire-custom-toggle"
                onclick={() => showManualThresholds = !showManualThresholds}
              >
                {showManualThresholds ? '▾ Hide manual thresholds' : '▸ Customize thresholds manually'}
              </button>

              {#if showManualThresholds}
                <div class="settings-cfg-grid" style="margin-top:8px">
                  <div>
                    <div class="settings-label">CPU Thresholds</div>
                    <div class="threshold-row">
                      <span class="settings-num-label threshold-lbl">Warn</span>
                      <input type="number" class="settings-num" bind:value={config.performance.cpu_warn_pct} min="0" max="100"/>
                      <span class="settings-num-label threshold-unit">%</span>
                      <span class="settings-num-label threshold-lbl">Crit</span>
                      <input type="number" class="settings-num" bind:value={config.performance.cpu_crit_pct} min="0" max="100"/>
                      <span class="settings-num-label threshold-unit">%</span>
                    </div>
                    <div class="settings-label">Memory Thresholds</div>
                    <div class="threshold-row">
                      <span class="settings-num-label threshold-lbl">Warn</span>
                      <input type="number" class="settings-num" bind:value={config.performance.mem_warn_pct} min="0" max="100"/>
                      <span class="settings-num-label threshold-unit">%</span>
                      <span class="settings-num-label threshold-lbl">Crit</span>
                      <input type="number" class="settings-num" bind:value={config.performance.mem_crit_pct} min="0" max="100"/>
                      <span class="settings-num-label threshold-unit">%</span>
                    </div>
                  </div>
                  <div>
                    <div class="settings-label">Input Delay Thresholds</div>
                    <div class="threshold-row">
                      <span class="settings-num-label threshold-lbl">Warn</span>
                      <input type="number" class="settings-num" bind:value={config.performance.input_delay_warn_ms} min="0"/>
                      <span class="settings-num-label threshold-unit">ms</span>
                      <span class="settings-num-label threshold-lbl">Crit</span>
                      <input type="number" class="settings-num" bind:value={config.performance.input_delay_crit_ms} min="0"/>
                      <span class="settings-num-label threshold-unit">ms</span>
                    </div>
                  </div>
                </div>
              {/if}

              <!-- Options always visible -->
              <div style="margin-top:8px">
                <label class="settings-check">
                  <input type="checkbox" bind:checked={config.performance.collect_per_session} />
                  Per-session CPU accounting
                </label>
                <label class="settings-check">
                  <input type="checkbox" bind:checked={config.performance.collect_remotefx} />
                  RemoteFX monitoring
                </label>
              </div>
            </div>
          {/if}
        </div>
        <div class="settings-divider"></div>
      {/if}

      <!-- Notification Targets -->
      <NotificationTargets bind:targets={config.notifications} />

      <!-- Actions bar -->
      <div class="settings-actions-wrap">
        <div class="settings-actions">
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
      </div>
    {/if}
  </div>
</div>

<style>
  .settings-overlay { position: fixed; inset: 0; background: rgba(0,0,0,0.5); z-index: 150; display: flex; justify-content: center; align-items: center; }
  .settings-modal { width: 860px; max-width: 94vw; max-height: 90vh; background: var(--color-card); border: var(--spacing-bw) solid var(--color-border); border-radius: var(--radius-default); box-shadow: 8px 8px 0 var(--color-shadow); overflow-y: auto; padding: 32px 36px; transition: background 0.3s; }
  .settings-title { font-family: 'Fraunces', serif; font-size: 1.3rem; margin-bottom: 24px; display: flex; align-items: center; justify-content: space-between; }
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
  .settings-cfg-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 28px; margin-bottom: 12px; }
  .threshold-row { display: grid; grid-template-columns: auto 80px auto auto 80px auto; gap: 8px; align-items: center; margin-bottom: 6px; }
  .threshold-lbl { justify-self: end; }
  .threshold-unit { justify-self: start; }

  /* Fire preset cards */
  .fire-row { display: grid; grid-template-columns: 1fr 1fr 1fr; gap: 10px; margin-bottom: 10px; }
  .fire-card {
    display: flex; flex-direction: column; align-items: center; gap: 4px;
    padding: 14px 10px 10px;
    background: var(--color-surface);
    border: 2px solid var(--color-border);
    border-radius: var(--radius-default);
    cursor: pointer;
    transition: all 0.15s;
    box-shadow: 3px 3px 0 var(--color-shadow);
  }
  .fire-card:hover { border-color: var(--color-accent); transform: translate(-1px, -1px); box-shadow: 4px 4px 0 var(--color-shadow); }
  .fire-card.active { border-color: var(--color-accent); background: color-mix(in srgb, var(--color-accent) 10%, var(--color-card)); box-shadow: 0 0 0 2px var(--color-accent), 3px 3px 0 var(--color-shadow); }
  .fire-icons { font-size: 1.4rem; line-height: 1; letter-spacing: -2px; }
  .fire-label { font-family: 'Work Sans', sans-serif; font-size: 0.82rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.05em; }
  .fire-detail { font-family: 'JetBrains Mono', monospace; font-size: 0.58rem; color: var(--color-muted); text-align: center; line-height: 1.4; }
  .fire-custom-toggle {
    background: none; border: none; cursor: pointer;
    font-family: 'JetBrains Mono', monospace; font-size: 0.72rem; font-weight: 600;
    color: var(--color-muted); padding: 4px 0; transition: color 0.15s;
  }
  .fire-custom-toggle:hover { color: var(--color-accent); }

  .settings-actions-wrap { margin-top: 16px; padding-top: 16px; border-top: 1px solid var(--color-surface); }
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
