<script>
    import { onMount, tick } from 'svelte';
    import { enqueueSessionAction } from '../lib/session-actions.svelte.js';

    let { host, session, action, onclose = () => {}, onqueued = () => {} } = $props();
    let message = $state('');
    let busy = $state(false);
    let error = $state('');
    let dialog = $state();
    let confirmButton = $state();
    let returnFocus;

    const isMessage = $derived(action === 'message');
    const isDisconnect = $derived(action === 'disconnect');
    const actionLabel = $derived(isMessage ? 'Send message' : isDisconnect ? 'Disconnect' : 'Log off');
    const normalizedMessage = $derived(message.trim());
    const logonBinding = $derived(
        Number.isFinite(session?.logon_at_ms)
            ? `${new Date(session.logon_at_ms).toLocaleString()} · ${session.logon_at_ms} ms`
            : 'Unavailable',
    );
    const target = $derived(
        session?.user ? `${session.user}${session.domain ? ` (${session.domain})` : ''}` : 'identity unavailable',
    );
    const canConfirm = $derived(
        !busy && (!isMessage || (normalizedMessage.length > 0 && [...normalizedMessage].length <= 256)),
    );

    onMount(async () => {
        returnFocus = document.activeElement;
        dialog?.showModal();
        await tick();
        (isMessage ? dialog?.querySelector('textarea') : confirmButton)?.focus();
        return () => returnFocus?.focus?.();
    });

    function close() {
        if (!busy) dialog?.close();
    }

    function trapFocus(event) {
        if (event.key === 'Escape') {
            event.preventDefault();
            close();
            return;
        }
        if (event.key !== 'Tab') return;
        const focusable = [...dialog.querySelectorAll('button:not([disabled]), textarea:not([disabled])')];
        if (!focusable.length) return;
        const first = focusable[0];
        const last = focusable[focusable.length - 1];
        if (event.shiftKey && document.activeElement === first) {
            event.preventDefault();
            last.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
            event.preventDefault();
            first.focus();
        }
    }

    async function confirm() {
        if (!canConfirm) return;
        busy = true;
        error = '';
        try {
            const queued = await enqueueSessionAction({
                host,
                sessionId: session.session_id,
                expectedLogonAtMs: session.logon_at_ms,
                type: action,
                message: normalizedMessage,
            });
            onqueued(queued);
            dialog?.close();
        } catch (cause) {
            error = cause?.message ?? 'Could not queue the session action.';
        } finally {
            busy = false;
        }
    }
</script>

<dialog
    class="session-dialog-backdrop"
    bind:this={dialog}
    aria-modal="true"
    aria-labelledby="session-action-title"
    aria-describedby="session-action-description"
    onkeydown={trapFocus}
    oncancel={(event) => {
        event.preventDefault();
        close();
    }}
    {onclose}
    onclick={(event) => event.target === event.currentTarget && close()}
>
    <div class="session-dialog card">
        <p class="eyebrow">Explicit confirmation required</p>
        <h2 id="session-action-title">{actionLabel} session?</h2>
        <p id="session-action-description">
            Confirm the exact session below. This queues an agent action; it does not open a remote desktop connection.
        </p>
        <dl class="target-details">
            <div>
                <dt>Host</dt>
                <dd class="mono">{host}</dd>
            </div>
            <div>
                <dt>Session ID</dt>
                <dd class="mono">{session.session_id}</dd>
            </div>
            <div>
                <dt>Target</dt>
                <dd>{target}</dd>
            </div>
            <div>
                <dt>Logon binding</dt>
                <dd class="mono">{logonBinding}</dd>
            </div>
            <div>
                <dt>Action</dt>
                <dd>{actionLabel}</dd>
            </div>
        </dl>
        {#if isMessage}
            <label for="session-action-message">Message <span class="required">required</span></label>
            <textarea
                id="session-action-message"
                bind:value={message}
                maxlength="256"
                rows="4"
                aria-describedby="message-limit"
                disabled={busy}></textarea>
            <p id="message-limit" class="field-help">
                {[...normalizedMessage].length}/256 characters. Leading and trailing whitespace is removed.
            </p>
            {#if normalizedMessage}
                <div class="message-preview">
                    <span>Exact outbound message</span>
                    <p>{normalizedMessage}</p>
                </div>
            {/if}
        {/if}
        {#if error}<p class="dialog-error" role="alert">{error}</p>{/if}
        <div class="dialog-actions">
            <button class="btn-brutal secondary" onclick={close} disabled={busy}>Cancel</button>
            <button
                class={`btn-brutal ${isDisconnect ? 'warning' : 'danger'}`}
                bind:this={confirmButton}
                onclick={confirm}
                disabled={!canConfirm}
                aria-busy={busy}
            >
                {busy ? 'Queueing…' : `Confirm ${actionLabel}`}
            </button>
        </div>
    </div>
</dialog>

<style>
    .session-dialog-backdrop {
        position: fixed;
        inset: 0;
        z-index: 50;
        box-sizing: border-box;
        width: 100%;
        height: 100%;
        margin: 0;
        padding: 1rem;
        border: 0;
        max-width: none;
        max-height: none;
        display: grid;
        place-items: center;
        background: color-mix(in srgb, var(--color-fg) 28%, transparent);
    }
    .session-dialog-backdrop:not([open]) {
        display: none;
    }
    .session-dialog {
        width: min(100%, 34rem);
        padding: 1.25rem;
    }
    .eyebrow {
        color: var(--color-accent);
        font:
            700 0.68rem/1 'JetBrains Mono',
            monospace;
        text-transform: uppercase;
        letter-spacing: 0.08em;
    }
    h2 {
        margin: 0.4rem 0;
        font-size: 1.35rem;
    }
    .target-details {
        display: flex;
        flex-direction: column;
        margin: 1rem 0;
    }
    .target-details div {
        display: flex;
        justify-content: space-between;
        gap: 1rem;
        padding: 0.45rem 0.5rem;
    }
    .target-details div:nth-child(even) {
        background: var(--color-surface);
    }
    dt {
        color: var(--color-muted);
        font:
            700 0.65rem/1 'JetBrains Mono',
            monospace;
        text-transform: uppercase;
    }
    dd {
        margin: 0;
        max-width: 70%;
        text-align: right;
        overflow-wrap: anywhere;
    }
    label {
        display: block;
        font-weight: 700;
        margin-top: 0.75rem;
    }
    textarea {
        display: block;
        width: 100%;
        margin-top: 0.35rem;
        padding: 0.55rem;
        resize: vertical;
        border: var(--spacing-bw) solid var(--color-border);
        border-radius: var(--radius-default);
        background: var(--color-card);
        color: var(--color-fg);
        font: inherit;
    }
    .required,
    .dialog-error {
        color: var(--color-red);
    }
    .field-help {
        margin-top: 0.25rem;
        color: var(--color-muted);
        font-size: 0.75rem;
    }
    .message-preview {
        margin-top: 0.55rem;
        padding: 0.5rem;
        background: var(--color-surface);
    }
    .message-preview span {
        color: var(--color-muted);
        font:
            700 0.65rem/1 'JetBrains Mono',
            monospace;
        text-transform: uppercase;
    }
    .message-preview p {
        margin-top: 0.3rem;
        white-space: pre-wrap;
        overflow-wrap: anywhere;
    }
    .dialog-actions {
        display: flex;
        justify-content: flex-end;
        gap: 0.6rem;
        margin-top: 1rem;
    }
    .btn-brutal {
        padding: 0.55rem 0.75rem;
        background: var(--color-card);
    }
    .danger {
        background: var(--color-red);
        color: white;
    }
    .warning {
        background: var(--color-amber);
        color: var(--color-fg);
    }
    .secondary {
        background: var(--color-surface);
    }
    button:disabled {
        cursor: not-allowed;
        opacity: 0.55;
        transform: none;
        box-shadow: none;
    }
    @media (max-width: 420px) {
        .target-details {
            grid-template-columns: 1fr;
        }
        .dialog-actions {
            flex-direction: column-reverse;
        }
    }
</style>
