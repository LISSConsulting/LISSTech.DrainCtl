<script>
    import { toast } from '../lib/toast.svelte.js';
    import { CheckCircle, XCircle, Info, Clipboard, ClipboardCheck, X } from 'lucide-svelte';

    /** @type {Set<string>} */
    let copiedIds = $state(new Set());

    /**
     * Copy text to clipboard, briefly show check icon.
     * @param {string} text
     * @param {string} id
     */
    async function copyText(text, id) {
        try {
            await navigator.clipboard.writeText(text);
            copiedIds = new Set([...copiedIds, id]);
            setTimeout(() => {
                copiedIds = new Set([...copiedIds].filter((x) => x !== id));
            }, 1200);
        } catch {
            /* clipboard unavailable in some contexts */
        }
    }
</script>

{#if toast.items.length > 0}
    <div class="toast-container" aria-live="polite">
        {#each toast.items as t (t.id)}
            <div class="toast toast-{t.type} {t.dismissing ? 'toast-out' : ''}" style="--duration:{t.duration}ms">
                <span class="toast-icon">
                    {#if t.type === 'ok'}<CheckCircle size={16} />{:else if t.type === 'err'}<XCircle size={16} />{:else}<Info size={16} />{/if}
                </span>
                <span class="toast-msg">{t.msg}</span>
                <button class="toast-copy" onclick={() => copyText(t.msg, t.id)} aria-label="Copy message">
                    {#if copiedIds.has(t.id)}<ClipboardCheck size={13} />{:else}<Clipboard size={13} />{/if}
                </button>
                <button class="toast-close" onclick={() => toast.dismiss(t.id)} aria-label="Dismiss"><X size={14} /></button>
            </div>
        {/each}
    </div>
{/if}

<style>
    .toast-container {
        position: fixed;
        top: 76px;
        right: 24px;
        z-index: 10000;
        display: flex;
        flex-direction: column;
        gap: 10px;
        max-width: 460px;
        pointer-events: none;
    }

    .toast {
        pointer-events: auto;
        display: flex;
        align-items: center;
        gap: 10px;
        padding: 13px 16px;
        border: 3px solid var(--border-dim);
        border-radius: var(--radius-default);
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.78rem;
        font-weight: 600;
        line-height: 1.4;
        position: relative;
        animation: toast-slam-in 0.15s cubic-bezier(0.2, 0, 0, 1);
    }

    /* Bright border overlay — recedes clockwise like a snake's tail */
    .toast::after {
        content: '';
        position: absolute;
        inset: -3px;
        border: 3px solid var(--border-bright);
        border-radius: inherit;
        pointer-events: none;
        opacity: 0.7;
        -webkit-mask: conic-gradient(from 0deg, #000 var(--countdown), transparent 0);
        mask: conic-gradient(from 0deg, #000 var(--countdown), transparent 0);
        animation: border-countdown var(--duration) linear forwards;
    }

    @keyframes border-countdown {
        from { --countdown: 100%; }
        to { --countdown: 0%; }
    }

    .toast-ok {
        --border-dim: #c0d8ca;
        --border-bright: #2d8a54;
        background: #e8f5ee;
        color: #1e4d30;
        box-shadow: 4px 4px 0 #92b09e;
    }
    .toast-err {
        --border-dim: #d8c0c4;
        --border-bright: #b84858;
        background: #fce8ed;
        color: #6b1e2e;
        box-shadow: 4px 4px 0 #a88892;
    }
    .toast-info {
        --border-dim: #d8ccb0;
        --border-bright: #aa8820;
        background: #fdf3e0;
        color: #5c3820;
        box-shadow: 4px 4px 0 #a09470;
    }

    :global([data-theme='dark']) .toast-ok {
        --border-dim: #284838;
        --border-bright: #48c880;
        background: #1a4028;
        color: #b4dcc6;
        box-shadow: 4px 4px 0 #10281a;
    }
    :global([data-theme='dark']) .toast-err {
        --border-dim: #502030;
        --border-bright: #e07080;
        background: #501a28;
        color: #e4c4cc;
        box-shadow: 4px 4px 0 #280c14;
    }
    :global([data-theme='dark']) .toast-info {
        --border-dim: #483418;
        --border-bright: #d8a830;
        background: #483018;
        color: #dccca8;
        box-shadow: 4px 4px 0 #281c0c;
    }

    .toast-out {
        animation: toast-slam-out 0.12s cubic-bezier(0.6, 0, 1, 0.8) forwards;
    }

    .toast-icon {
        font-size: 1.1rem;
        line-height: 1;
        flex-shrink: 0;
    }

    .toast-msg {
        flex: 1;
        min-width: 0;
        word-break: break-word;
    }

    .toast-copy {
        flex-shrink: 0;
        padding: 4px;
        border: none;
        border-radius: 4px;
        background: rgba(255, 255, 255, 0.1);
        color: inherit;
        cursor: pointer;
        opacity: 0.7;
        transition: opacity 0.1s, background 0.1s;
        display: flex;
        align-items: center;
    }
    .toast-copy:hover {
        opacity: 1;
        background: rgba(255, 255, 255, 0.2);
    }

    .toast-close {
        flex-shrink: 0;
        background: none;
        border: none;
        font-size: 1.2rem;
        line-height: 1;
        cursor: pointer;
        color: inherit;
        opacity: 0.5;
        padding: 0 2px;
        transition: opacity 0.1s linear;
    }
    .toast-close:hover {
        opacity: 1;
    }

    @keyframes toast-slam-in {
        0% {
            opacity: 0;
            transform: translateX(60px) scale(0.95);
        }
        70% {
            opacity: 1;
            transform: translateX(-4px) scale(1.02);
        }
        100% {
            transform: translateX(0) scale(1);
        }
    }
    @keyframes toast-slam-out {
        0% {
            opacity: 1;
            transform: translateX(0);
        }
        100% {
            opacity: 0;
            transform: translateX(60px) scale(0.95);
        }
    }
</style>
