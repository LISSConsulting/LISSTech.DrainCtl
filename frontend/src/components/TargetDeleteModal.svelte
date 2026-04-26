<script>
    import { Trash2 } from 'lucide-svelte';

    let { target, deleting = false, onconfirm, oncancel } = $props();

    let dest = $derived(target?.type === 'email' ? (target?.to || []).join(', ') : target?.url || '');

    let closing = $state(false);
    const CLOSE_MS = 150;

    function animateClose(cb) {
        closing = true;
        setTimeout(() => cb?.(), CLOSE_MS);
    }

    // Confirm fires synchronously — the parent owns the network call and will
    // dismiss this modal on success. Animating close before the request
    // resolves would leave a half-faded modal stuck on screen if delete fails.
    $effect(() => {
        function onKey(e) {
            if (e.key === 'Escape' && !deleting) animateClose(oncancel);
            if (e.key === 'Enter' && !deleting) {
                e.preventDefault();
                onconfirm?.();
            }
        }
        document.addEventListener('keydown', onKey);
        return () => document.removeEventListener('keydown', onKey);
    });
</script>

<!-- svelte-ignore a11y_click_events_have_key_events a11y_interactive_supports_focus -->
<div
    class="tgt-del-overlay {closing ? 'closing' : ''}"
    onclick={(e) => e.target === e.currentTarget && animateClose(oncancel)}
    role="dialog"
    aria-modal="true"
    tabindex="-1"
>
    <div class="modal-wrap">
        <span class="modal-badge"><Trash2 size={20} /></span>
        <div class="tgt-del-modal">
            <h3 class="tgt-del-title serif">Delete Notification Target</h3>
            <p>Are you sure you want to delete this notification target?</p>
            <p style="font-family:'JetBrains Mono',monospace;font-size:12px;color:var(--color-muted);margin-top:8px">
                {dest}
            </p>
            <div class="btn-row">
                <button class="btn-brutal btn-cancel" onclick={() => animateClose(oncancel)} disabled={deleting}>Cancel</button>
                <button class="btn-brutal btn-danger" onclick={() => onconfirm?.()} disabled={deleting}>
                    {deleting ? 'Deleting...' : 'Delete'}
                </button>
            </div>
        </div>
    </div>
</div>

<style>
    .tgt-del-overlay {
        position: fixed;
        inset: 0;
        background: rgba(0, 0, 0, 0.55);
        z-index: 210;
        display: flex;
        align-items: center;
        justify-content: center;
        animation: modal-fade-in var(--anim-in-duration) var(--anim-timing);
    }
    .tgt-del-overlay.closing {
        animation: modal-fade-out var(--anim-out-duration) var(--anim-timing) forwards;
    }
    .tgt-del-overlay.closing > .modal-wrap {
        animation: modal-card-out var(--anim-out-duration) var(--anim-timing) forwards;
    }
    .modal-wrap {
        position: relative;
        max-width: 400px;
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
        background: var(--color-red);
        color: #fff;
        border: 3px solid var(--color-border);
        border-radius: 50%;
        box-shadow: 3px 3px 0 var(--color-shadow);
        z-index: 1;
    }
    .tgt-del-modal {
        background: var(--color-card);
        border: 4px solid var(--color-border);
        border-radius: var(--radius-default);
        box-shadow: 10px 10px 0 var(--color-shadow);
        padding: 28px 24px 24px;
        text-align: center;
    }
    .tgt-del-title {
        font-family: 'Fraunces', serif;
        font-size: 1.25rem;
        font-weight: 700;
        margin: 8px 0 16px;
    }
    p {
        font-size: 14px;
        margin-bottom: 8px;
        line-height: 1.5;
    }
    .btn-row {
        display: flex;
        gap: 8px;
        justify-content: center;
        margin-top: 16px;
    }
    .btn-cancel {
        background: var(--color-card);
        color: var(--color-fg);
        padding: 8px 20px;
        font-family: 'Work Sans', sans-serif;
        font-size: 0.85rem;
        font-weight: 700;
    }
    .btn-danger {
        background: var(--color-red);
        color: #fff;
        padding: 8px 20px;
        font-family: 'Work Sans', sans-serif;
        font-size: 0.85rem;
        font-weight: 700;
        border-color: var(--color-red);
    }
</style>
