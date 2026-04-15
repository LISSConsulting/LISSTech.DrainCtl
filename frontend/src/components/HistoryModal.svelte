<script>
    import { fetchHistory } from '../lib/api.js';
    import { formatTs, dur } from '../lib/utils.js';
    import { Clock, X, ArrowRightLeft, CircleDot } from 'lucide-svelte';

    let { host, onclose } = $props();

    let entries = $state([]);
    let loading = $state(false);
    let error = $state('');
    let changesOnly = $state(false);

    // Sequence counter — incremented on each fetch; only the latest call's
    // results are applied, preventing stale-result overwrites if changesOnly
    // is toggled while a previous fetch is still in flight.
    let fetchSeq = 0;

    $effect(() => {
        if (!host) return;
        // Read changesOnly inside the effect so toggling it triggers a re-fetch.
        const _co = changesOnly;
        loadHistory(_co);
    });

    async function loadHistory(co = false) {
        const mySeq = ++fetchSeq;
        loading = true;
        error = '';
        try {
            const result = (await fetchHistory(host, 100, co)) || [];
            if (mySeq === fetchSeq) entries = result;
        } catch (e) {
            if (mySeq === fetchSeq) error = e?.message ?? String(e);
        } finally {
            if (mySeq === fetchSeq) loading = false;
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
        }
        document.addEventListener('keydown', onKey);
        return () => document.removeEventListener('keydown', onKey);
    });

    // The API returns lowercase status values ('ok', 'grace', 'alert', 'off').
    // statusClass maps these directly to CSS class names (which match the status values).
    function statusClass(s) {
        return { ok: 'ok', warning: 'warning', grace: 'grace', alert: 'alert', off: 'off' }[s] ?? 'off';
    }

    function statusLabel(s) {
        return { ok: 'Healthy', warning: 'Warning', grace: 'Grace', alert: 'Alert', off: 'Offline' }[s] || s;
    }
</script>

{#if host}
    <!-- svelte-ignore a11y_click_events_have_key_events a11y_interactive_supports_focus -->
    <div
        class="hist-overlay {closing ? 'closing' : ''}"
        onclick={handleOverlayClick}
        role="dialog"
        aria-modal="true"
        tabindex="-1"
    >
        <div class="modal-wrap">
            <span class="modal-badge"><Clock size={20} /></span>
            <div class="hist-modal">
                <div class="hist-head">
                    <div>
                        <div class="hist-title serif">Server History</div>
                        <div class="hist-hostname">{host.split('.')[0]}</div>
                    </div>
                    <div class="hist-head-actions">
                        <button
                            class="filter-pill {changesOnly ? 'active' : ''}"
                            onclick={() => (changesOnly = !changesOnly)}>Transitions Only</button
                        >
                        <button class="settings-close" onclick={animateClose} aria-label="Close history"
                            ><X size={18} /></button
                        >
                    </div>
                </div>
                <div class="hist-body scrollbar-styled">
                    {#if loading}
                        <div class="hist-empty">Loading...</div>
                    {:else if error}
                        <div class="hist-empty err">
                            <span>Error: {error}</span>
                            <button class="btn-brutal retry-btn" onclick={() => loadHistory(changesOnly)}>Retry</button>
                        </div>
                    {:else if !entries.length}
                        <div class="hist-empty">No history recorded yet.</div>
                    {:else}
                        {#each entries as e}
                            {@const sc = statusClass(e.status)}
                            <div class="hist-entry" class:hist-entry-transition={e.transition}>
                                <span class="hist-icon {e.transition ? 'transition' : ''}">
                                    {#if e.transition}<ArrowRightLeft size={12} />{:else}<CircleDot size={12} />{/if}
                                </span>
                                <div class="hist-ts">{formatTs(e.timestamp)}</div>
                                <div class="hist-badge {sc}">{statusLabel(sc)}</div>
                                <div class="hist-detail">
                                    {#if e.transition && e.transition_from}<span class="hist-from"
                                            >{statusLabel(e.transition_from)} →</span
                                        >{/if}
                                    {#if e.state_duration_seconds != null}{dur(e.state_duration_seconds)}{/if}
                                    {#if e.changed_by}
                                        · {e.changed_by}{/if}
                                </div>
                            </div>
                        {/each}
                    {/if}
                </div>
            </div>
        </div>
    </div>
{/if}

<style>
    .hist-overlay {
        position: fixed;
        inset: 0;
        background: rgba(0, 0, 0, 0.55);
        z-index: 160;
        display: flex;
        justify-content: center;
        align-items: center;
        animation: modal-fade-in var(--anim-in-duration) var(--anim-timing);
    }
    .hist-overlay.closing {
        animation: modal-fade-out var(--anim-out-duration) var(--anim-timing) forwards;
    }
    .hist-overlay.closing > .modal-wrap {
        animation: modal-card-out var(--anim-out-duration) var(--anim-timing) forwards;
    }
    .modal-wrap {
        position: relative;
        width: 640px;
        max-width: 94vw;
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
    .hist-modal {
        max-height: 82vh;
        background: var(--color-card);
        border: 4px solid var(--color-border);
        border-radius: var(--radius-default);
        box-shadow: 10px 10px 0 var(--color-shadow);
        overflow: hidden;
        display: flex;
        flex-direction: column;
    }
    .hist-head {
        padding: 20px 24px 16px;
        border-bottom: 1px solid var(--color-surface);
        display: flex;
        align-items: center;
        justify-content: space-between;
        flex-shrink: 0;
    }
    .hist-head-actions {
        display: flex;
        align-items: center;
        gap: 10px;
    }
    .hist-title {
        font-family: 'Fraunces', serif;
        font-size: 1.25rem;
        font-weight: 700;
        margin-bottom: 2px;
    }
    .hist-hostname {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.82rem;
        color: var(--color-accent);
    }
    .hist-body {
        overflow-y: auto;
        padding: 8px 24px 20px;
        flex: 1;
    }
    .hist-empty {
        display: flex;
        flex-direction: column;
        align-items: center;
        gap: 12px;
        text-align: center;
        padding: 32px;
        color: var(--color-subtle);
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.8rem;
    }
    .hist-empty.err {
        color: var(--color-red);
    }
    .retry-btn {
        font-size: 0.72rem;
        padding: 4px 14px;
        background: var(--color-surface);
        color: var(--color-fg);
    }
    .hist-entry {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 8px 4px;
        border-bottom: 1px solid var(--color-surface);
    }
    .hist-entry:last-child {
        border-bottom: none;
    }
    .hist-entry-transition {
        background: color-mix(in srgb, var(--color-accent) 4%, transparent);
        border-radius: 4px;
    }
    .hist-icon {
        display: flex;
        align-items: center;
        color: var(--color-subtle);
        flex-shrink: 0;
    }
    .hist-icon.transition {
        color: var(--color-accent);
    }
    .hist-from {
        color: var(--color-subtle);
        margin-right: 2px;
    }
    .hist-ts {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.7rem;
        color: var(--color-muted);
        white-space: nowrap;
        min-width: 96px;
    }
    .hist-badge {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.6rem;
        font-weight: 700;
        text-transform: uppercase;
        letter-spacing: 0.06em;
        padding: 2px 8px;
        border-radius: 4px;
        color: #fff;
        white-space: nowrap;
        min-width: 54px;
        text-align: center;
    }
    .hist-badge.ok {
        background: var(--color-green);
    }
    .hist-badge.warning {
        background: var(--color-amber);
    }
    .hist-badge.grace {
        background: var(--color-amber);
    }
    .hist-badge.alert {
        background: var(--color-red);
    }
    .hist-badge.off {
        background: var(--color-subtle);
    }
    .hist-detail {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.72rem;
        color: var(--color-muted);
        flex: 1;
    }
    .filter-pill {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.68rem;
        font-weight: 700;
        padding: 5px 12px;
        border: var(--spacing-bw) solid var(--color-border);
        border-radius: var(--radius-default);
        box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow);
        background: var(--color-card);
        color: var(--color-muted);
        cursor: pointer;
        text-transform: uppercase;
        letter-spacing: 0.06em;
        transition:
            transform 0.1s,
            box-shadow 0.1s,
            background 0.1s,
            color 0.1s;
    }
    .filter-pill:hover {
        transform: translate(-2px, -2px);
        box-shadow: calc(var(--spacing-so) + 2px) calc(var(--spacing-so) + 2px) 0 var(--color-shadow);
        border-color: var(--color-accent);
        color: var(--color-fg);
    }
    .filter-pill:active {
        transform: translate(2px, 2px);
        box-shadow: 1px 1px 0 var(--color-shadow);
    }
    .filter-pill.active {
        background: var(--color-accent);
        color: #fff;
        border-color: var(--color-accent);
    }
    .settings-close {
        background: none;
        border: none;
        cursor: pointer;
        color: var(--color-muted);
        padding: 4px 8px;
        display: flex;
        align-items: center;
    }
    .settings-close:hover {
        color: var(--color-fg);
    }
</style>
