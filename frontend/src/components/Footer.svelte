<script>
    import { appState } from '../lib/state.svelte.js';
    import { authState } from '../lib/auth.svelte.js';
    import { Wifi, WifiOff, RefreshCw, Users, BookOpen, User, Wrench } from 'lucide-svelte';
    import { rel } from '../lib/utils.js';

    /** @typedef {import('../lib/api.js').MaintenanceJob} MaintenanceJob */

    /** @type {{
     *   onrefresh?: () => void,
     *   refreshing?: boolean,
     *   maintenanceJobs?: MaintenanceJob[],
     *   maintenanceServerTime?: string|null,
     *   maintenanceLoading?: boolean,
     *   maintenanceError?: string,
     * }} */
    let {
        onrefresh,
        refreshing = false,
        maintenanceJobs = [],
        maintenanceServerTime = null,
        maintenanceLoading = false,
        maintenanceError = '',
    } = $props();

    // Per-job maintenance chips. Short labels match operator shorthand; state
    // indicator colours by outcome. Each job shows its own tiny status cell
    // in the footer's centre slot so you can glance and see which specific
    // pass is behaving.
    const JOB_SHORT = {
        aggregator_5min: 'AGG5M',
        aggregator_hourly: 'AGG1H',
        retention: 'RET',
        jsonl_migration: 'JSONL',
        drift_reconciliation: 'DRIFT',
    };
    const JOB_LONG = {
        aggregator_5min: 'Aggregator 5m',
        aggregator_hourly: 'Aggregator 1h',
        retention: 'Retention',
        jsonl_migration: 'JSONL Migration',
        drift_reconciliation: 'Drift Reconciliation',
    };
    // Stable display order so the chip row doesn't reshuffle when the backend
    // re-sorts; jobs not in this list fall through to the tail alphabetically.
    const JOB_ORDER = ['aggregator_5min', 'aggregator_hourly', 'retention', 'drift_reconciliation', 'jsonl_migration'];

    const maintNowMs = $derived.by(() => {
        if (!maintenanceServerTime) return Date.now();
        const t = new Date(maintenanceServerTime).getTime();
        return Number.isFinite(t) && t > 0 ? t : Date.now();
    });

    // JSONL migration is a one-shot boot job; showing it in the perpetual
    // footer status row is noise since it's almost always `skipped`.
    const HIDDEN_JOBS = new Set(['jsonl_migration']);

    const orderedMaintJobs = $derived.by(() => {
        const idx = (name) => {
            const p = JOB_ORDER.indexOf(name);
            return p === -1 ? JOB_ORDER.length : p;
        };
        return maintenanceJobs
            .filter((j) => !HIDDEN_JOBS.has(j.name))
            .sort((a, b) => {
                const ai = idx(a.name);
                const bi = idx(b.name);
                if (ai !== bi) return ai - bi;
                return a.name.localeCompare(b.name);
            });
    });

    function jobShort(name) {
        return JOB_SHORT[name] ?? name.slice(0, 6).toUpperCase();
    }

    /** @param {MaintenanceJob} job */
    function jobGlyph(job) {
        if (job.outcome === 'failure') return '✕';
        if (job.overdue) return '!';
        if (job.outcome === 'skipped') return '—';
        return '✓';
    }

    /** @param {MaintenanceJob} job */
    function jobStateClass(job) {
        if (job.outcome === 'failure') return 'fail';
        if (job.overdue) return 'warn';
        if (job.outcome === 'skipped') return 'skip';
        return 'ok';
    }

    /** @param {MaintenanceJob} job */
    function jobTitle(job) {
        const label = JOB_LONG[job.name] ?? job.name;
        const lines = [`${label}: ${job.outcome}`];
        if (job.overdue) lines.push('OVERDUE — no run seen within 2× expected interval');
        if (job.finished) lines.push(`Last run: ${rel(job.finished, maintNowMs)}`);
        if (job.duration_ms != null) lines.push(`Duration: ${job.duration_ms} ms`);
        if (job.rows_affected != null) lines.push(`Rows: ${job.rows_affected.toLocaleString()}`);
        if (job.outcome === 'failure' && job.reason) lines.push(`Reason: ${job.reason}`);
        return lines.join('\n');
    }

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

    // Strip DOMAIN\ prefix for display — "CORP\jsmith" → "JSMITH".
    const displayUser = $derived(
        authState.username
            ? (authState.username.includes('\\')
                ? authState.username.split('\\').pop()
                : authState.username
              ).toUpperCase()
            : null,
    );
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
    <div class="footer-center" aria-label="Maintenance status">
        <Wrench size={10} strokeWidth={2.2} class="maint-icon" />
        {#if maintenanceError}
            <span class="maint-chip maint-fail" title={maintenanceError}>ERR</span>
        {:else if maintenanceLoading && orderedMaintJobs.length === 0}
            <span class="maint-chip">…</span>
        {:else if orderedMaintJobs.length === 0}
            <span class="maint-chip">IDLE</span>
        {:else}
            {#each orderedMaintJobs as job (job.name)}
                <span class="maint-chip" title={jobTitle(job)}>
                    <span class="maint-chip-label">{jobShort(job.name)}</span>
                    <span class="maint-chip-glyph maint-{jobStateClass(job)}">{jobGlyph(job)}</span>
                </span>
            {/each}
        {/if}
    </div>
    <div class="footer-right">
        {#if displayUser}
            <span class="user-pill">
                <User size={11} strokeWidth={2.4} class="user-icon" />
                {displayUser}
            </span>
        {/if}
        <span class="session-badge">
            <Users size={11} strokeWidth={2.4} />
            <span class="session-count">{sessions.toLocaleString()}</span>
            <span class="session-label">sessions · {servers} servers</span>
        </span>
        <span class="status-pill {appState.connected ? (appState.sseConnected ? 'live' : appState.sseReconnecting ? 'reconnecting' : 'connected') : 'disconnected'}">
            {#if appState.connected && appState.sseConnected}
                <Wifi size={11} strokeWidth={2.4} />
                LIVE
            {:else if appState.connected && appState.sseReconnecting}
                <Wifi size={11} strokeWidth={2.4} />
                RECONNECTING
            {:else if appState.connected}
                <Wifi size={11} strokeWidth={2.4} />
                CONNECTED
            {:else}
                <WifiOff size={11} strokeWidth={2.4} />
                DISCONNECTED
            {/if}
        </span>
        <span class="footer-updated">{lastUpdatedStr}</span>
        {#if onrefresh}
            <button
                class="btn-brutal footer-refresh"
                onclick={onrefresh}
                aria-label="Refresh now"
                aria-busy={refreshing}
                disabled={refreshing}
            >
                <span class={refreshing ? 'spin' : ''}><RefreshCw size={12} strokeWidth={2.4} /></span>
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

    /* Center slot: per-job maintenance chips. One chip per job with a short
       label + colored glyph so an operator can see at a glance which specific
       pass is healthy, overdue, or failing. Quiet chrome; colour lives only
       on the glyph. */
    .footer-center {
        display: flex;
        align-items: center;
        gap: 10px;
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.55rem;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        color: var(--color-subtle);
    }
    .footer-center :global(.maint-icon) {
        opacity: 0.7;
    }
    .maint-chip {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        cursor: help;
    }
    .maint-chip-label {
        opacity: 0.8;
    }
    .maint-chip-glyph {
        font-weight: 800;
    }
    .maint-chip-glyph.maint-ok {
        color: var(--color-green);
    }
    .maint-chip-glyph.maint-warn {
        color: var(--color-amber);
    }
    .maint-chip-glyph.maint-fail {
        color: var(--color-red);
    }
    .maint-chip-glyph.maint-skip {
        color: var(--color-subtle);
    }

    .footer-right {
        display: flex;
        align-items: center;
        gap: 10px;
    }

    .user-pill {
        display: inline-flex;
        align-items: center;
        gap: 5px;
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.62rem;
        font-weight: 700;
        text-transform: uppercase;
        letter-spacing: 0.08em;
        color: var(--color-muted);
        padding: 3px 10px;
        border-radius: var(--radius-default);
        background: var(--color-card);
        border: var(--spacing-bw) solid var(--color-border);
        box-shadow: 2px 2px 0 var(--color-shadow);
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
        border: var(--spacing-bw) solid var(--color-border);
        box-shadow: 2px 2px 0 var(--color-shadow);
    }

    .live {
        background: var(--color-green);
        color: #fff;
    }

    .connected {
        background: var(--color-amber);
        color: #fff;
    }

    .reconnecting {
        background: var(--color-amber);
        color: #fff;
        animation: blink 1s step-start infinite;
    }

    @keyframes blink {
        0%, 100% { opacity: 1; }
        50% { opacity: 0.4; }
    }

    .disconnected {
        background: var(--color-red);
        color: #fff;
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

    .footer-refresh:disabled {
        opacity: 0.5;
        cursor: not-allowed;
        transform: none;
        box-shadow: 2px 2px 0 var(--color-shadow);
    }

    @keyframes spin {
        from { transform: rotate(0deg); }
        to { transform: rotate(360deg); }
    }

    .spin {
        display: inline-flex;
        animation: spin 0.7s linear infinite;
    }
</style>
