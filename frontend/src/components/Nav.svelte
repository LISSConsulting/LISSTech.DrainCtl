<script>
    import { appState } from '../lib/state.svelte.js';
    import { toggleTheme, theme } from '../lib/theme.svelte.js';
    import { authState, logout } from '../lib/auth.svelte.js';
    import { LayoutDashboard, Server, ScrollText, Sun, Moon } from 'lucide-svelte';

    /** @type {{ onconfigopen: () => void }} */
    let { onconfigopen } = $props();

    const counters = $derived(appState.counters);
    const isDark = $derived(theme.current === 'dark');

    /** @type {Array<{id: 'overview'|'servers'|'events', label: string, icon: any}>} */
    const TABS = [
        { id: 'overview', label: 'Overview', icon: LayoutDashboard },
        { id: 'servers', label: 'Servers', icon: Server },
        { id: 'events', label: 'Events', icon: ScrollText },
    ];
</script>

<nav class="nav">
    <div class="nav-in">
        <div class="nav-brand">
            <img src="/logo.png" alt="" class="mark-img" width="28" height="28" />
            DrainCtl
        </div>

        <div class="nav-right">
            <span class="mono status-counts">
                <span class="g">{counters.ok}</span> ok ·
                <span class="w">{counters.grace}</span> grace ·
                <span class="a">{counters.alert}</span> alert ·
                <span class="o">{counters.off}</span> off
            </span>

            {#each TABS as tab}
                {@const Icon = tab.icon}
                <button
                    role="tab"
                    aria-selected={appState.currentView === tab.id}
                    class="btn-tab btn-brutal"
                    class:active={appState.currentView === tab.id}
                    onclick={() => (appState.currentView = tab.id)}
                >
                    <Icon size={13} strokeWidth={2.2} />
                    {tab.label}
                </button>
            {/each}

            <button class="btn-gear btn-brutal" onclick={onconfigopen}>CONFIG</button>
            {#if authState.username}
                <button class="btn-signout btn-brutal" onclick={logout}>SIGN OUT</button>
            {/if}
            <button
                class="btn-theme btn-brutal"
                onclick={toggleTheme}
                aria-label={isDark ? 'Switch to light mode' : 'Switch to dark mode'}
            >
                {#if isDark}<Sun size={15} strokeWidth={2.4} />{:else}<Moon size={15} strokeWidth={2.4} />{/if}
            </button>
        </div>
    </div>
</nav>

<style>
    .nav {
        position: sticky;
        top: 0;
        z-index: 100;
        background: var(--color-bg);
        border-bottom: var(--spacing-bw) solid var(--color-border);
        height: 52px;
    }

    .nav-in {
        max-width: 1400px;
        margin: 0 auto;
        padding: 0 24px;
        display: flex;
        align-items: center;
        justify-content: space-between;
        height: 100%;
    }

    .nav-brand {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.95rem;
        font-weight: 700;
        text-transform: uppercase;
        letter-spacing: 0.08em;
        display: flex;
        align-items: center;
        gap: 10px;
        flex-shrink: 0;
    }

    .mark-img {
        display: block;
    }

    /* ── Right cluster ────────────────────────────────────────────────────── */

    .nav-right {
        display: flex;
        align-items: center;
        gap: 8px;
        font-family: 'JetBrains Mono', monospace;
        flex-shrink: 0;
    }

    .status-counts {
        font-size: 0.8rem;
        margin-right: 8px;
    }

    .g {
        color: var(--color-green);
    }
    .a {
        color: var(--color-red);
    }
    .w {
        color: var(--color-amber);
    }
    .o {
        color: var(--color-subtle);
    }

    /* ── Tabs (match CONFIG button style exactly) ─────────────────────────── */

    .btn-tab {
        display: flex;
        align-items: center;
        gap: 5px;
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.68rem;
        font-weight: 700;
        text-transform: uppercase;
        letter-spacing: 0.08em;
        background: var(--color-card);
        color: var(--color-muted);
        padding: 6px 12px;
        /* btn-brutal handles border, shadow, radius, hover lift */
    }

    .btn-tab:hover {
        color: var(--color-fg);
    }

    .btn-tab.active {
        background: var(--color-accent);
        color: #fff;
        border-color: var(--color-accent);
    }

    /* ── CONFIG button ────────────────────────────────────────────────────── */

    .btn-gear {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.68rem;
        font-weight: 700;
        text-transform: uppercase;
        letter-spacing: 0.08em;
        background: var(--color-card);
        color: var(--color-muted);
        padding: 6px 14px;
    }

    .btn-gear:hover {
        color: var(--color-accent);
    }

    /* ── Sign Out button ──────────────────────────────────────────────────── */

    .btn-signout {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.68rem;
        font-weight: 700;
        text-transform: uppercase;
        letter-spacing: 0.08em;
        background: var(--color-card);
        color: var(--color-muted);
        padding: 6px 14px;
    }

    .btn-signout:hover {
        color: var(--color-red);
    }

    /* ── Theme toggle ─────────────────────────────────────────────────────── */

    .btn-theme {
        padding: 6px 10px;
        background: var(--color-card);
        color: var(--color-fg);
        display: flex;
        align-items: center;
        justify-content: center;
    }
</style>
