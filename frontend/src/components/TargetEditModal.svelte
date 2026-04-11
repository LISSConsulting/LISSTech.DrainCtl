<script>
  import { sendNotifyTest } from '../lib/api.js';

  let { target, isNew, onsave, onclose } = $props();

  // Local working copy — snapshot taken at open time; later mutations only touch `t`
  // svelte-ignore state_referenced_locally
  let t = $state(JSON.parse(JSON.stringify(/** @type {any} */ (target))));

  const ALL_TRIGGERS = ['drain_on','drain_off','grace_entered','alert','healthy','session_warning','cpu_warning','cpu_critical','memory_warning','memory_critical','input_delay_warning','input_delay_critical'];
  const TRIGGER_LABELS = { drain_on:'Drain On', drain_off:'Drain Off', grace_entered:'Grace', alert:'Alert', healthy:'Healthy', session_warning:'Sessions', cpu_warning:'CPU Warn', cpu_critical:'CPU Crit', memory_warning:'Mem Warn', memory_critical:'Mem Crit', input_delay_warning:'Delay Warn', input_delay_critical:'Delay Crit' };
  const REPEAT_OPTIONS = [0, 15, 60, 240, 480];
  const REPEAT_MAP = { 0: 'Once', 15: '15m', 60: '1h', 240: '4h', 480: '8h' };

  let testResult = $state('');
  let testStatus = $state('');
  let testing = $state(false);

  function toggleTrigger(tr) {
    if (t.triggers.includes(tr)) {
      t.triggers = t.triggers.filter(x => x !== tr);
    } else {
      t.triggers = [...t.triggers, tr];
    }
  }

  async function testTarget() {
    testing = true;
    testResult = '';
    testStatus = '';
    try {
      // Send the full target object so the backend tests this specific
      // configuration (even before it has been saved).
      const r = await sendNotifyTest(t);
      testStatus = 'ok';
      testResult = r.message || 'Test sent';
    } catch(e) {
      testStatus = 'err';
      testResult = e.message;
    } finally {
      testing = false;
    }
  }

  function handleOverlayClick(e) {
    if (e.target === e.currentTarget) onclose?.();
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events a11y_interactive_supports_focus -->
<div class="tgt-edit-overlay" onclick={handleOverlayClick} role="dialog" aria-modal="true" tabindex="-1">
  <div class="tgt-edit-modal scrollbar-styled">
    <h3 class="tgt-modal-title serif">{isNew ? 'Add Notification Target' : 'Edit Notification Target'}</h3>

    <!-- Type selection -->
    <div class="tgt-form-row">
      <div class="tgt-form-label">Type</div>
      <div class="tgt-type-pills">
        {#each ['webhook', 'ntfy', 'email'] as type}
          <button class="tgt-type-pill {t.type === type ? 'active' : ''}" onclick={() => t.type = type}>
            {type === 'ntfy' ? 'Ntfy' : type.charAt(0).toUpperCase() + type.slice(1)}
          </button>
        {/each}
      </div>
    </div>

    <!-- Destination -->
    {#if t.type === 'email'}
      <div class="tgt-form-row">
        <div class="tgt-form-label">From Address</div>
        <input class="tgt-form-input" type="email" bind:value={t.from} placeholder="from@example.com" />
      </div>
      <div class="tgt-form-row">
        <div class="tgt-form-label">To Addresses (comma-separated)</div>
        <input class="tgt-form-input" type="text"
          value={(t.to||[]).join(', ')}
          oninput={(e) => t.to = e.target.value.split(',').map(x => x.trim()).filter(Boolean)}
          placeholder="user1@example.com, user2@example.com" />
      </div>
    {:else}
      <div class="tgt-form-row">
        <div class="tgt-form-label">URL</div>
        <input class="tgt-form-input" type="url" bind:value={t.url} placeholder={t.type === 'ntfy' ? 'https://ntfy.sh/topic' : 'https://example.com/webhook'} />
      </div>
    {/if}

    <!-- HMAC Secret (webhook only) -->
    {#if t.type === 'webhook'}
      <div class="tgt-form-row">
        <div class="tgt-form-label">HMAC Secret (optional)</div>
        <input class="tgt-form-input" type="password" bind:value={t.secret} placeholder="Leave blank for no signing" />
      </div>
    {/if}

    <hr class="tgt-form-divider" />

    <!-- Triggers -->
    <div class="tgt-form-row">
      <div class="tgt-form-label">Triggers</div>
      <div class="tgt-trigger-grid">
        {#each ALL_TRIGGERS as tr}
          <label class="tgt-trigger-check {t.triggers.includes(tr) ? 'checked' : ''}">
            <input type="checkbox"
              checked={t.triggers.includes(tr)}
              onchange={() => toggleTrigger(tr)}
            />
            {TRIGGER_LABELS[tr]}
          </label>
        {/each}
      </div>
    </div>

    <hr class="tgt-form-divider" />

    <!-- Repeat interval -->
    <div class="tgt-form-row">
      <div class="tgt-form-label">Repeat Interval</div>
      <div class="tgt-repeat-pills">
        {#each REPEAT_OPTIONS as opt}
          <button class="tgt-repeat-pill {t.repeat_minutes === opt ? 'active' : ''}" onclick={() => t.repeat_minutes = opt}>
            {REPEAT_MAP[opt]}
          </button>
        {/each}
      </div>
    </div>

    <!-- Enabled toggle -->
    <div class="tgt-form-row">
      <label class="settings-check">
        <input type="checkbox" bind:checked={t.enabled} />
        Enabled
      </label>
    </div>

    <!-- Test result -->
    {#if testResult}
      <div class="tgt-test-result {testStatus}">{testResult}</div>
    {/if}

    <!-- Actions -->
    <div class="tgt-form-actions">
      <button class="btn-brutal btn-test-sm" onclick={testTarget} disabled={testing}>
        {testing ? 'Sending...' : '▶ Test'}
      </button>
      <div class="tgt-form-actions-right">
        <button class="btn-brutal btn-secondary-sm" onclick={onclose}>Cancel</button>
        <button class="btn-brutal btn-save-sm" onclick={() => onsave?.(t)}>
          {isNew ? 'Add Target' : 'Save'}
        </button>
      </div>
    </div>
  </div>
</div>

<style>
  .tgt-edit-overlay { position: fixed; inset: 0; background: rgba(45,26,26,0.5); z-index: 200; display: flex; align-items: center; justify-content: center; }
  .tgt-edit-modal { background: var(--color-card); border: var(--spacing-bw) solid var(--color-border); border-radius: var(--radius-default); box-shadow: 8px 8px 0 var(--color-shadow); max-width: 560px; width: 94vw; max-height: 90vh; overflow-y: auto; padding: 24px; }
  .tgt-modal-title { font-family: 'DM Serif Display', serif; font-size: 18px; margin-bottom: 16px; text-align: center; }
  .tgt-form-row { margin-bottom: 14px; }
  .tgt-form-label { font-family: 'JetBrains Mono', monospace; font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.5px; color: var(--color-muted); margin-bottom: 4px; }
  .tgt-form-input { width: 100%; padding: 8px 10px; border: 1.5px solid color-mix(in srgb, var(--color-border) 60%, transparent); border-radius: 6px; font-family: 'JetBrains Mono', monospace; font-size: 12px; background: var(--color-bg); color: var(--color-fg); box-sizing: border-box; }
  .tgt-form-input:focus { outline: none; border-color: var(--color-accent); box-shadow: 0 0 0 2px color-mix(in srgb, var(--color-accent) 15%, transparent); }
  .tgt-type-pills { display: flex; gap: 6px; }
  .tgt-type-pill { font-family: 'JetBrains Mono', monospace; font-size: 11px; font-weight: 600; padding: 6px 14px; border: 1.5px solid color-mix(in srgb, var(--color-border) 60%, transparent); border-radius: 6px; cursor: pointer; background: var(--color-bg); color: var(--color-muted); text-transform: uppercase; letter-spacing: 0.5px; transition: all 0.15s; }
  .tgt-type-pill:hover { border-color: var(--color-accent); color: var(--color-fg); }
  .tgt-type-pill.active { background: var(--color-accent); color: #fff; border-color: var(--color-accent); }
  .tgt-trigger-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 6px; }
  .tgt-trigger-check { display: flex; align-items: center; gap: 6px; font-size: 12px; padding: 5px 8px; background: var(--color-bg); border: 1.5px solid color-mix(in srgb, var(--color-border) 60%, transparent); border-radius: 6px; cursor: pointer; transition: all 0.15s; }
  .tgt-trigger-check.checked { background: color-mix(in srgb, var(--color-accent) 8%, var(--color-bg)); border-color: var(--color-accent); }
  .tgt-trigger-check input { accent-color: var(--color-accent); }
  .tgt-repeat-pills { display: flex; gap: 6px; flex-wrap: wrap; }
  .tgt-repeat-pill { font-family: 'JetBrains Mono', monospace; font-size: 11px; font-weight: 600; padding: 5px 12px; border: 1.5px solid color-mix(in srgb, var(--color-border) 60%, transparent); border-radius: 6px; cursor: pointer; background: var(--color-bg); color: var(--color-muted); transition: all 0.15s; }
  .tgt-repeat-pill:hover { border-color: var(--color-accent); color: var(--color-fg); }
  .tgt-repeat-pill.active { background: var(--color-accent); color: #fff; border-color: var(--color-accent); }
  .tgt-form-divider { border: none; border-top: 1px solid var(--color-surface); margin: 16px 0; }
  .tgt-test-result { font-family: 'JetBrains Mono', monospace; font-size: 11px; margin-top: 6px; padding: 6px 10px; border-radius: 4px; }
  .tgt-test-result.ok { background: color-mix(in srgb, var(--color-green) 10%, var(--color-card)); color: var(--color-green); border: 1px solid var(--color-green); }
  .tgt-test-result.err { background: color-mix(in srgb, var(--color-red) 10%, var(--color-card)); color: var(--color-red); border: 1px solid var(--color-red); }
  .tgt-form-actions { display: flex; justify-content: space-between; align-items: center; margin-top: 16px; padding-top: 14px; border-top: 2px solid var(--color-surface); }
  .tgt-form-actions-right { display: flex; gap: 8px; }
  .settings-check { display: flex; align-items: center; gap: 10px; cursor: pointer; font-size: 0.85rem; font-weight: 600; }
  .settings-check input[type="checkbox"] { width: 18px; height: 18px; accent-color: var(--color-accent); cursor: pointer; }
  .btn-test-sm, .btn-secondary-sm, .btn-save-sm { padding: 8px 16px; font-family: 'Work Sans', sans-serif; font-size: 0.82rem; font-weight: 700; }
  .btn-save-sm { background: var(--color-accent); color: #fff; }
  .btn-test-sm, .btn-secondary-sm { background: var(--color-card); color: var(--color-fg); }
  .btn-test-sm:disabled { opacity: 0.5; cursor: not-allowed; }
</style>
