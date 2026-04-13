<script>
    import { appState } from '../lib/state.svelte.js';
    import { Wifi, WifiOff, RefreshCw, Users, BookOpen } from 'lucide-svelte';

    let { onrefresh } = $props();

    const lastUpdatedStr = $derived(
        appState.lastUpdated
            ? appState.lastUpdated.toLocaleTimeString('en-US', {
                  hour: '2-digit',
                  minute: '2-digit',
                  second: '2-digit',
                  hour12: false,
              })
            : '—',
    );

    const version = $derived(appState.health?.version ?? '—');
    const sessions = $derived(appState.counters.sessions);
    const servers = $derived(appState.counters.total);
</script>

<footer class="footer sticky">
    <div class="footer-left">
        <span class="footer-brand">LISSTECH DRAINCTL <span class="footer-version">{version}</span></span>
        <span class="footer-copy"
            >Copyright &copy; 2026 LISS Consulting, Corp. · <a
                href="https://lissconsulting.github.io/LISSTech.DrainCtl/guide.html"
                target="_blank"
                rel="noopener"
                class="footer-link"><BookOpen size={9} strokeWidth={2.2} /> Guide</a
            ></span
        >
    </div>
    <div class="footer-right">
        <span class="session-badge">
            <Users size={11} strokeWidth={2.4} />
            <span class="session-count">{sessions.toLocaleString()}</span>
            <span class="session-label">sessions · {servers} servers</span>
        </span>
        <span class="status-pill {appState.connected ? 'connected' : 'disconnected'}">
            {#if appState.connected}
                <Wifi size={11} strokeWidth={2.4} />
                CONNECTED
            {:else}
                <WifiOff size={11} strokeWidth={2.4} />
                DISCONNECTED
            {/if}
        </span>
        <span class="footer-updated">{lastUpdatedStr}</span>
        {#if onrefresh}
            <button class="btn-brutal footer-refresh" onclick={onrefresh} aria-label="Refresh now">
                <RefreshCw size={12} strokeWidth={2.4} />
            </button>
        {/if}
    </div>
</footer>

<style>
    .footer {
        display: flex;
        align-items: center;
        justify-content: space-between;
        background: var(--color-bg);
        border-top: 3px solid var(--color-border);
        padding: 10px 24px;
        font-family: 'JetBrains Mono', monospace;
    }

    .footer.sticky {
        position: sticky;
        bottom: 0;
        z-index: 10;
    }

    .footer-left {
        display: flex;
        flex-direction: column;
        gap: 1px;
    }

    .footer-brand {
        font-size: 0.7rem;
        font-weight: 800;
        letter-spacing: 0.1em;
        text-transform: uppercase;
        color: var(--color-fg);
    }

    .footer-version {
        font-size: 0.58rem;
        font-weight: 700;
        color: var(--color-muted);
        padding: 0 5px;
        border: 1.5px solid var(--color-border);
        border-radius: 3px;
        letter-spacing: 0.04em;
        margin-left: 6px;
    }

    .footer-copy {
        font-size: 0.55rem;
        color: var(--color-subtle);
        letter-spacing: 0.06em;
        display: flex;
        align-items: center;
        gap: 3px;
    }

    .footer-link {
        display: inline-flex;
        align-items: center;
        gap: 2px;
        color: var(--color-subtle);
        text-decoration: none;
    }

    .footer-link:hover {
        color: var(--color-accent);
    }

    .footer-right {
        display: flex;
        align-items: center;
        gap: 10px;
    }

    .session-badge {
        display: inline-flex;
        align-items: center;
        gap: 5px;
        font-size: 0.62rem;
        font-weight: 800;
        padding: 3px 10px;
        border-radius: var(--radius-default);
        background: var(--color-accent);
        color: #fff;
        border: var(--spacing-bw) solid var(--color-border);
        box-shadow: 2px 2px 0 var(--color-shadow);
        letter-spacing: 0.06em;
        text-transform: uppercase;
    }

    .session-count {
        font-size: 0.75rem;
        letter-spacing: 0;
    }

    .session-label {
        font-weight: 600;
        opacity: 0.8;
    }

    .status-pill {
        display: inline-flex;
        align-items: center;
        gap: 5px;
        font-size: 0.62rem;
        font-weight: 800;
        padding: 3px 10px;
        border-radius: var(--radius-default);
        text-transform: uppercase;
        letter-spacing: 0.1em;
    }

    .connected {
        background: var(--color-green);
        color: #fff;
        border: var(--spacing-bw) solid var(--color-border);
        box-shadow: 2px 2px 0 var(--color-shadow);
    }

    .disconnected {
        background: var(--color-red);
        color: #fff;
        border: var(--spacing-bw) solid var(--color-border);
        box-shadow: 2px 2px 0 var(--color-shadow);
    }

    .footer-updated {
        font-size: 0.62rem;
        color: var(--color-muted);
        letter-spacing: 0.04em;
    }

    .footer-refresh {
        padding: 4px 7px;
        background: var(--color-surface);
        color: var(--color-muted);
        font-size: 0;
        line-height: 0;
    }

    .footer-refresh:hover {
        color: var(--color-fg);
    }
</style>
