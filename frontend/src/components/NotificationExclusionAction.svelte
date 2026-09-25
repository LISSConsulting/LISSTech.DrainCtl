<script>
    import { BellOff, BellRing } from '@lucide/svelte';
    import { toggleNotificationExclusion } from '../lib/api.js';
    import { appState, setNotificationExclusion } from '../lib/state.svelte.js';
    import { toast } from '../lib/toast.svelte.js';

    let { host, compact = false, onchange = () => {} } = $props();
    let pending = $state(false);

    const canonicalHost = $derived(host.trim().replace(/\.$/, '').toLowerCase());
    const excluded = $derived(
        !!canonicalHost &&
            (appState.config?.notification_exclusions ?? []).some(
                (item) => item.trim().replace(/\.$/, '').toLowerCase() === canonicalHost,
            ),
    );
    const label = $derived(
        excluded
            ? `Notifications suppressed for ${host}. Activate to enable notifications.`
            : `Notifications enabled for ${host}. Activate to suppress notifications.`,
    );

    async function toggle() {
        if (pending || !canonicalHost) return;
        pending = true;
        try {
            const result = await toggleNotificationExclusion(canonicalHost);
            setNotificationExclusion(result.host, result.excluded);
            onchange(result);
            toast.ok(
                result.excluded
                    ? `Notifications suppressed for ${result.host}`
                    : `Notifications enabled for ${result.host}`,
            );
        } catch (error) {
            toast.err(`Unable to update notification exclusion: ${error?.detail ?? error?.message ?? error}`);
        } finally {
            pending = false;
        }
    }
</script>

<button
    type="button"
    class="notification-exclusion-action"
    class:compact
    class:excluded
    aria-label={label}
    aria-pressed={excluded}
    aria-busy={pending}
    title={label}
    disabled={pending || !canonicalHost}
    onclick={toggle}
>
    {#if excluded}
        <BellOff size={12} aria-hidden="true" />
    {:else}
        <BellRing size={12} aria-hidden="true" />
    {/if}
    <span>{excluded ? 'Notify' : 'Mute'}</span>
</button>

<style>
    .notification-exclusion-action {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        gap: 4px;
        padding: 4px 10px;
        border: var(--spacing-bw) solid var(--color-accent);
        border-radius: var(--radius-default);
        background: var(--color-card);
        color: var(--color-accent);
        box-shadow: 3px 3px 0 var(--color-shadow);
        font-family: 'Work Sans', sans-serif;
        font-size: 0.7rem;
        font-weight: 700;
        cursor: pointer;
        transition:
            transform 0.1s,
            box-shadow 0.1s,
            background 0.1s,
            color 0.1s;
    }

    .notification-exclusion-action.excluded {
        border-color: var(--color-amber);
        color: var(--color-amber);
    }

    .notification-exclusion-action:hover:not(:disabled) {
        transform: translate(-1px, -1px);
        box-shadow: 4px 4px 0 var(--color-shadow);
        background: var(--color-accent);
        color: #fff;
    }

    .notification-exclusion-action.excluded:hover:not(:disabled) {
        background: var(--color-amber);
        color: #1a1200;
    }

    .notification-exclusion-action:active:not(:disabled) {
        transform: translate(1px, 1px);
        box-shadow: 1px 1px 0 var(--color-shadow);
    }

    .notification-exclusion-action:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: 2px;
    }

    .notification-exclusion-action:disabled {
        cursor: not-allowed;
        opacity: 0.6;
    }
</style>
