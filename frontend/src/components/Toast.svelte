<script>
    import { toast } from '../lib/toast.svelte.js';

    /**
     * Copy text to clipboard, briefly flash the button.
     * @param {string} text
     * @param {HTMLButtonElement} btn
     */
    async function copyText(text, btn) {
        try {
            await navigator.clipboard.writeText(text);
            btn.textContent = 'Copied';
            setTimeout(() => {
                btn.textContent = 'Copy';
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
                    {#if t.type === 'ok'}&#10003;{:else if t.type === 'err'}&#10007;{:else}&#9432;{/if}
                </span>
                <span class="toast-msg">{t.msg}</span>
                <button class="toast-copy" onclick={(e) => copyText(t.msg, e.currentTarget)}>Copy</button>
                <button class="toast-close" onclick={() => toast.dismiss(t.id)} aria-label="Dismiss">&times;</button>
            </div>
        {/each}
    </div>
{/if}

<style>
    .toast-container {
        position: fixed;
        top: 60px;
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
        padding: 12px 14px;
        border: var(--spacing-bw) solid var(--color-border);
        border-radius: var(--radius-default);
        box-shadow: 5px 5px 0 var(--color-shadow);
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.78rem;
        font-weight: 600;
        line-height: 1.4;
        animation: toast-slide-in 0.12s linear;
    }

    .toast-ok {
        background: color-mix(in srgb, var(--color-green) 12%, var(--color-card));
        border-color: var(--color-green);
        color: var(--color-green);
    }
    .toast-err {
        background: color-mix(in srgb, var(--color-red) 12%, var(--color-card));
        border-color: var(--color-red);
        color: var(--color-red);
    }
    .toast-info {
        background: color-mix(in srgb, var(--color-accent) 10%, var(--color-card));
        border-color: var(--color-accent);
        color: var(--color-accent);
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
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.65rem;
        font-weight: 700;
        text-transform: uppercase;
        letter-spacing: 0.5px;
        padding: 3px 8px;
        border: 1.5px solid currentColor;
        border-radius: 4px;
        background: transparent;
        color: inherit;
        cursor: pointer;
        opacity: 0.7;
        transition: opacity 0.1s linear;
    }
    .toast-copy:hover {
        opacity: 1;
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
