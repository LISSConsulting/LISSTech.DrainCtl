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
            <div class="toast toast-{t.type} {t.dismissing ? 'toast-out' : ''}">
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
        z-index: 9999;
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
        border: none;
        border-radius: var(--radius-default);
        box-shadow: 0 4px 20px rgba(0, 0, 0, 0.15), 0 1px 4px rgba(0, 0, 0, 0.1);
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.78rem;
        font-weight: 600;
        line-height: 1.4;
        animation: toast-slide-in 0.12s linear;
    }

    .toast-ok {
        background: linear-gradient(135deg, #1a4d2e, #2d6a4f);
        color: #b8e6cc;
    }
    .toast-err {
        background: linear-gradient(135deg, #6b1a2a, #a3475b);
        color: #f5d0d8;
    }
    .toast-info {
        background: linear-gradient(135deg, #5c3a1e, #8b6914);
        color: #f5e6c8;
    }

    .toast-out {
        animation: toast-slide-out 0.12s linear forwards;
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

    @keyframes toast-slide-in {
        from {
            opacity: 0;
            transform: translateX(40px);
        }
        to {
            opacity: 1;
            transform: translateX(0);
        }
    }
    @keyframes toast-slide-out {
        from {
            opacity: 1;
            transform: translateX(0);
        }
        to {
            opacity: 0;
            transform: translateX(40px);
        }
    }
</style>
