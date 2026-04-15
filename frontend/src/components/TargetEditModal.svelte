<script>
    import { sendNotifyTest } from '../lib/api.js';
    import { toast } from '../lib/toast.svelte.js';
    import { ALL_TRIGGERS, TRIGGER_LABELS, REPEAT_OPTIONS, REPEAT_MAP } from '../lib/notify.js';
    import { Bell } from 'lucide-svelte';

    let { target, isNew, onsave, onclose } = $props();

    const SECRET_SENTINEL = '••••••••';

    // Local working copy — snapshot taken at open time; later mutations only touch `t`.
    // Use JSON round-trip instead of structuredClone to avoid Svelte 5 proxy issues.
    // svelte-ignore state_referenced_locally
    let t = $state(JSON.parse(JSON.stringify(target)));

    // Track whether the secret was set on load (server sends sentinel for existing secrets).
    // This is a one-time snapshot, not reactive — intentionally not $state.
    const secretWasSet = t.secret === SECRET_SENTINEL;
    // Clear the sentinel so the password input shows empty with a placeholder.
    if (secretWasSet) t.secret = '';

    let testing = $state(false);

    /** @type {HTMLInputElement|undefined} */
    let urlInput = $state();
    $effect(() => {
        if (urlInput) urlInput.focus();
    });

    function toggleTrigger(tr) {
        if (t.triggers.includes(tr)) {
            t.triggers = t.triggers.filter((x) => x !== tr);
        } else {
            t.triggers = [...t.triggers, tr];
        }
    }

    /**
     * Validate the target before saving.
     * @returns {string|null} error message or null if valid
     */
    const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
    const SMTP_RE = /^smtps?:\/\/.+/;

    function validate() {
        if (t.type === 'email') {
            if (!t.url?.trim()) return 'SMTP server URL is required.';
            if (!SMTP_RE.test(t.url.trim())) return 'SMTP URL must start with smtp:// or smtps://';
            if (!t.from?.trim()) return 'From address is required.';
            if (!EMAIL_RE.test(t.from.trim())) return 'From address is not a valid email.';
            const addrs = (t.to || []).filter((a) => a.trim());
            if (!addrs.length) return 'At least one To address is required.';
            const bad = addrs.find((a) => !EMAIL_RE.test(a));
            if (bad) return `Invalid To address: ${bad}`;
        } else {
            if (!t.url?.trim()) return 'URL is required.';
            try {
                new URL(t.url);
            } catch {
                return 'URL is not valid.';
            }
        }
        if (!t.triggers?.length) return 'At least one trigger must be selected.';
        return null;
    }

    function handleSave() {
        const err = validate();
        if (err) {
            toast.err(err);
            return;
        }
        // If the secret was set on load and the user didn't type a new one,
        // send the sentinel back so the backend preserves the existing secret.
        if (secretWasSet && !t.secret) {
            t.secret = SECRET_SENTINEL;
        }
        onsave?.(t);
        toast.ok(isNew ? 'Target added' : 'Target updated');
    }

    async function testTarget() {
        const err = validate();
        if (err) {
            toast.err(err);
            return;
        }
        testing = true;
        try {
            // Send sentinel if user didn't change the secret, so backend
            // resolves the real credential for SMTP auth / webhook HMAC.
            const payload = JSON.parse(JSON.stringify(t));
            if (secretWasSet && !payload.secret) {
                payload.secret = SECRET_SENTINEL;
            }
            const r = await sendNotifyTest(payload);
            toast.ok(r.message || 'Test sent');
        } catch (e) {
            toast.err(e?.detail ?? e?.message ?? String(e));
        } finally {
            testing = false;
        }
    }

    let closing = $state(false);
    const CLOSE_MS = 150;

    function animateClose() {
        closing = true;
        setTimeout(() => onclose?.(), CLOSE_MS);
    }

    function handleOverlayClick(e) {
        if (e.target === e.currentTarget) animateClose();
    }

    $effect(() => {
        function onKey(e) {
            if (e.key === 'Escape') animateClose();
            if (e.key === 'Enter' && !e.target?.closest('textarea')) {
                e.preventDefault();
                handleSave();
            }
        }
        document.addEventListener('keydown', onKey);
        return () => document.removeEventListener('keydown', onKey);
    });
</script>

<!-- svelte-ignore a11y_click_events_have_key_events a11y_interactive_supports_focus -->
<div
    class="tgt-edit-overlay {closing ? 'closing' : ''}"
    onclick={handleOverlayClick}
    role="dialog"
    aria-modal="true"
    tabindex="-1"
>
    <div class="modal-wrap">
        <span class="modal-badge"><Bell size={20} /></span>
        <div class="tgt-edit-modal scrollbar-styled">
            <h3 class="tgt-modal-title serif">{isNew ? 'Add Notification Target' : 'Edit Notification Target'}</h3>

            <!-- Type selection -->
            <div class="tgt-form-row">
                <div class="tgt-form-label">Type</div>
                <div class="tgt-type-pills">
                    {#each ['webhook', 'ntfy', 'email'] as type}
                        <button class="tgt-type-pill {t.type === type ? 'active' : ''}" onclick={() => (t.type = type)}>
                            {type === 'ntfy' ? 'Ntfy' : type.charAt(0).toUpperCase() + type.slice(1)}
                        </button>
                    {/each}
                </div>
            </div>

            <!-- Destination -->
            {#if t.type === 'email'}
                <div class="tgt-form-row">
                    <div class="tgt-form-label">SMTP Server</div>
                    <input
                        class="tgt-form-input"
                        type="url"
                        bind:value={t.url}
                        placeholder="smtp://mail.example.com:587"
                    />
                </div>
                <div class="tgt-form-row">
                    <div class="tgt-form-label">From Address</div>
                    <input class="tgt-form-input" type="email" bind:value={t.from} placeholder="from@example.com" />
                </div>
                <div class="tgt-form-row">
                    <div class="tgt-form-label">To Addresses (comma-separated)</div>
                    <input
                        class="tgt-form-input"
                        type="text"
                        value={(t.to || []).join(', ')}
                        oninput={(e) =>
                            (t.to = e.target.value
                                .split(',')
                                .map((x) => x.trim())
                                .filter(Boolean))}
                        placeholder="user1@example.com, user2@example.com"
                    />
                </div>
            {:else}
                <div class="tgt-form-row">
                    <div class="tgt-form-label">URL</div>
                    <input
                        class="tgt-form-input"
                        type="url"
                        bind:value={t.url}
                        placeholder={t.type === 'ntfy' ? 'https://ntfy.sh/topic' : 'https://example.com/webhook'}
                        bind:this={urlInput}
                    />
                </div>
            {/if}

            <!-- HMAC Secret (webhook) / SMTP Password (email) -->
            {#if t.type === 'webhook' || t.type === 'email'}
                <div class="tgt-form-row">
                    <div class="tgt-form-label">{t.type === 'email' ? 'SMTP Password' : 'HMAC Secret'} (optional)</div>
                    <input
                        class="tgt-form-input"
                        type="password"
                        autocomplete="off"
                        bind:value={t.secret}
                        placeholder={secretWasSet ? 'Secret is set — leave blank to keep' : 'Leave blank for none'}
                    />
                </div>
            {/if}

            <hr class="tgt-form-divider" />

            <!-- Triggers -->
            <div class="tgt-form-row">
                <div class="tgt-form-label">Triggers</div>
                <div class="tgt-trigger-grid">
                    {#each ALL_TRIGGERS as tr}
                        <label class="tgt-trigger-check {t.triggers.includes(tr) ? 'checked' : ''}">
                            <input
                                type="checkbox"
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
                        <button
                            class="tgt-repeat-pill {t.repeat_minutes === opt ? 'active' : ''}"
                            onclick={() => (t.repeat_minutes = opt)}
                        >
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

            <!-- Actions -->
            <div class="tgt-form-actions">
                <button class="btn-brutal btn-test-sm" onclick={testTarget} disabled={testing}>
                    {testing ? 'Sending...' : '▶ Test'}
                </button>
                <div class="tgt-form-actions-right">
                    <button class="btn-brutal btn-secondary-sm" onclick={animateClose}>Cancel</button>
                    <button class="btn-brutal btn-save-sm" onclick={handleSave}>
                        {isNew ? 'Add Target' : 'Save'}
                    </button>
                </div>
            </div>
        </div>
    </div>
</div>

<style>
    .tgt-edit-overlay {
        position: fixed;
        inset: 0;
        background: rgba(0, 0, 0, 0.55);
        z-index: 200;
        display: flex;
        align-items: center;
        justify-content: center;
        animation: modal-fade-in var(--anim-in-duration) var(--anim-timing);
    }
    .tgt-edit-overlay.closing {
        animation: modal-fade-out var(--anim-out-duration) var(--anim-timing) forwards;
    }
    .tgt-edit-overlay.closing > .modal-wrap {
        animation: modal-card-out var(--anim-out-duration) var(--anim-timing) forwards;
    }
    .modal-wrap {
        position: relative;
        max-width: 560px;
        width: 94vw;
        animation: modal-card-in var(--anim-in-duration) var(--anim-timing);
    }
    .modal-badge {
        position: absolute;
        top: -16px;
        left: 50%;
        transform: translateX(-50%);
        display: flex;
        align-items: center;
        justify-content: center;
        width: 36px;
        height: 36px;
        background: var(--color-accent);
        color: #fff;
        border: 3px solid var(--color-border);
        border-radius: 50%;
        box-shadow: 3px 3px 0 var(--color-shadow);
        z-index: 1;
    }
    .tgt-edit-modal {
        background: var(--color-card);
        border: 4px solid var(--color-border);
        border-radius: var(--radius-default);
        box-shadow: 10px 10px 0 var(--color-shadow);
        max-height: 90vh;
        overflow-y: auto;
        padding: 24px;
    }
    .tgt-modal-title {
        font-family: 'Fraunces', serif;
        font-size: 1.25rem;
        font-weight: 700;
        margin: 8px 0 16px;
        text-align: center;
    }
    .tgt-form-row {
        margin-bottom: 14px;
    }
    .tgt-form-label {
        font-family: 'JetBrains Mono', monospace;
        font-size: 11px;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.5px;
        color: var(--color-muted);
        margin-bottom: 4px;
    }
    .tgt-form-input {
        width: 100%;
        padding: 8px 10px;
        border: 1.5px solid color-mix(in srgb, var(--color-border) 60%, transparent);
        border-radius: 6px;
        font-family: 'JetBrains Mono', monospace;
        font-size: 12px;
        background: var(--color-bg);
        color: var(--color-fg);
        box-sizing: border-box;
    }
    .tgt-type-pills {
        display: flex;
        gap: 8px;
    }
    .tgt-type-pill {
        font-family: 'JetBrains Mono', monospace;
        font-size: 11px;
        font-weight: 700;
        padding: 7px 16px;
        border: var(--spacing-bw) solid var(--color-border);
        border-radius: var(--radius-default);
        box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow);
        cursor: pointer;
        background: var(--color-card);
        color: var(--color-muted);
        text-transform: uppercase;
        letter-spacing: 0.5px;
        transition:
            transform 0.1s,
            box-shadow 0.1s,
            background 0.1s,
            color 0.1s;
    }
    .tgt-type-pill:hover {
        transform: translate(-2px, -2px);
        box-shadow: calc(var(--spacing-so) + 2px) calc(var(--spacing-so) + 2px) 0 var(--color-shadow);
        border-color: var(--color-accent);
        color: var(--color-fg);
    }
    .tgt-type-pill:active {
        transform: translate(2px, 2px);
        box-shadow: 1px 1px 0 var(--color-shadow);
    }
    .tgt-type-pill.active {
        background: var(--color-accent);
        color: #fff;
        border-color: var(--color-accent);
    }
    .tgt-trigger-grid {
        display: grid;
        grid-template-columns: 1fr 1fr;
        gap: 7px;
    }
    .tgt-trigger-check {
        display: flex;
        align-items: center;
        gap: 7px;
        font-size: 12px;
        font-weight: 600;
        padding: 6px 10px;
        background: var(--color-card);
        border: var(--spacing-bw) solid var(--color-border);
        border-radius: var(--radius-default);
        box-shadow: 2px 2px 0 var(--color-shadow);
        cursor: pointer;
        transition:
            transform 0.1s,
            box-shadow 0.1s,
            background 0.1s;
    }
    .tgt-trigger-check:hover {
        transform: translate(-1px, -1px);
        box-shadow: 3px 3px 0 var(--color-shadow);
    }
    .tgt-trigger-check:active {
        transform: translate(1px, 1px);
        box-shadow: 1px 1px 0 var(--color-shadow);
    }
    .tgt-trigger-check.checked {
        background: color-mix(in srgb, var(--color-accent) 12%, var(--color-card));
        border-color: var(--color-accent);
    }
    .tgt-trigger-check input {
        appearance: none;
        width: 15px;
        height: 15px;
        border: 2px solid var(--color-border);
        border-radius: 3px;
        background: var(--color-surface);
        cursor: pointer;
        position: relative;
        flex-shrink: 0;
    }
    .tgt-trigger-check input:checked {
        background: var(--color-accent);
        border-color: var(--color-accent);
    }
    .tgt-trigger-check input:checked::after {
        content: '';
        position: absolute;
        left: 3px;
        top: 0px;
        width: 4px;
        height: 8px;
        border: solid #fff;
        border-width: 0 2px 2px 0;
        transform: rotate(45deg);
    }
    .tgt-repeat-pills {
        display: flex;
        gap: 8px;
        flex-wrap: wrap;
    }
    .tgt-repeat-pill {
        font-family: 'JetBrains Mono', monospace;
        font-size: 11px;
        font-weight: 700;
        padding: 7px 14px;
        border: var(--spacing-bw) solid var(--color-border);
        border-radius: var(--radius-default);
        box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow);
        cursor: pointer;
        background: var(--color-card);
        color: var(--color-muted);
        transition:
            transform 0.1s,
            box-shadow 0.1s,
            background 0.1s,
            color 0.1s;
    }
    .tgt-repeat-pill:hover {
        transform: translate(-2px, -2px);
        box-shadow: calc(var(--spacing-so) + 2px) calc(var(--spacing-so) + 2px) 0 var(--color-shadow);
        border-color: var(--color-accent);
        color: var(--color-fg);
    }
    .tgt-repeat-pill:active {
        transform: translate(2px, 2px);
        box-shadow: 1px 1px 0 var(--color-shadow);
    }
    .tgt-repeat-pill.active {
        background: var(--color-accent);
        color: #fff;
        border-color: var(--color-accent);
    }
    .tgt-form-divider {
        border: none;
        border-top: 1px solid var(--color-surface);
        margin: 16px 0;
    }
    .tgt-form-actions {
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-top: 16px;
        padding-top: 14px;
        border-top: 2px solid var(--color-surface);
    }
    .tgt-form-actions-right {
        display: flex;
        gap: 8px;
    }
    .settings-check {
        display: flex;
        align-items: center;
        gap: 10px;
        cursor: pointer;
        font-size: 0.85rem;
        font-weight: 600;
    }
    .settings-check input[type='checkbox'] {
        appearance: none;
        width: 18px;
        height: 18px;
        border: 2px solid var(--color-border);
        border-radius: 3px;
        background: var(--color-surface);
        cursor: pointer;
        position: relative;
    }
    .settings-check input[type='checkbox']:checked {
        background: var(--color-accent);
        border-color: var(--color-accent);
    }
    .settings-check input[type='checkbox']:checked::after {
        content: '';
        position: absolute;
        left: 4px;
        top: 1px;
        width: 5px;
        height: 9px;
        border: solid #fff;
        border-width: 0 2px 2px 0;
        transform: rotate(45deg);
    }
    .btn-test-sm,
    .btn-secondary-sm,
    .btn-save-sm {
        padding: 8px 16px;
        font-family: 'Work Sans', sans-serif;
        font-size: 0.82rem;
        font-weight: 700;
    }
    .btn-save-sm {
        background: var(--color-accent);
        color: #fff;
    }
    .btn-test-sm,
    .btn-secondary-sm {
        background: var(--color-card);
        color: var(--color-fg);
    }
    .btn-test-sm:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
</style>
