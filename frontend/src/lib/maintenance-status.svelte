<script>
    /**
     * maintenance-status.svelte — compact dashboard widget backed by
     * GET /api/v1/maintenance/status (contracts/http-maintenance.md, FR-030).
     *
     * Pure presentation: the parent passes the parsed response shape and
     * drives the refresh cadence. Shows one row per job with a coloured
     * outcome badge, last-run relative timestamp, duration, rows_affected,
     * and an "OVERDUE" warning pill when the server flagged it so.
     */
    import { Wrench, TriangleAlert } from 'lucide-svelte';
    import { rel } from './utils.js';

    /**
     * @typedef {Object} MaintenanceJob
     * @property {string} name
     * @property {string} started
     * @property {string} finished
     * @property {number} duration_ms
     * @property {'success'|'failure'|'skipped'} outcome
     * @property {string} reason
     * @property {number} rows_affected
     * @property {boolean} overdue
     * @property {number} expected_interval_seconds
     */

    /** @type {{
     *   jobs?: MaintenanceJob[],
     *   serverTime?: string|null,
     *   loading?: boolean,
     *   error?: string,
     * }} */
    let { jobs = [], serverTime = null, loading = false, error = '' } = $props();

    const JOB_LABELS = {
        aggregator_5min: 'Aggregator 5m',
        aggregator_hourly: 'Aggregator 1h',
        retention: 'Retention',
        jsonl_migration: 'JSONL Migration',
        drift_reconciliation: 'Drift Reconciliation',
    };

    /** @param {string} name */
    function jobLabel(name) {
        return JOB_LABELS[name] ?? name;
    }

    // Prefer the server's own clock so relative timestamps aren't skewed by
    // the operator's browser being out-of-sync with the service host.
    const nowMs = $derived.by(() => {
        if (!serverTime) return Date.now();
        const t = new Date(serverTime).getTime();
        return Number.isFinite(t) && t > 0 ? t : Date.now();
    });

    /** @param {number} ms */
    function formatDuration(ms) {
        if (ms == null) return '—';
        if (ms < 1000) return `${ms}ms`;
        const s = ms / 1000;
        if (s < 60) return `${s.toFixed(s < 10 ? 2 : 1)}s`;
        const m = Math.floor(s / 60);
        const rs = Math.round(s - m * 60);
        return `${m}m ${rs}s`;
    }

    /** @param {number} n */
    function formatRows(n) {
        if (n == null) return '—';
        return n.toLocaleString();
    }

    /** @param {'success'|'failure'|'skipped'} outcome */
    function outcomeClass(outcome) {
        if (outcome === 'success') return 'ok';
        if (outcome === 'failure') return 'fail';
        return 'skip';
    }

    /** Stable job sort: failures/overdue first, then alphabetical by name. */
    const sortedJobs = $derived(
        [...jobs].sort((a, b) => {
            const aPri = a.outcome === 'failure' ? 0 : a.overdue ? 1 : 2;
            const bPri = b.outcome === 'failure' ? 0 : b.overdue ? 1 : 2;
            if (aPri !== bPri) return aPri - bPri;
            return a.name.localeCompare(b.name);
        }),
    );
</script>

<section class="maint" aria-label="Maintenance status">
    <header class="maint-head">
        <Wrench size={12} strokeWidth={2.4} />
        <span class="maint-title">Maintenance</span>
    </header>

    {#if error}
        <div class="maint-error" role="alert">{error}</div>
    {:else if loading && sortedJobs.length === 0}
        <div class="maint-empty">Loading…</div>
    {:else if sortedJobs.length === 0}
        <div class="maint-empty">No jobs have run yet.</div>
    {:else}
        <ul class="maint-list">
            {#each sortedJobs as job (job.name)}
                <li class="maint-row" class:failing={job.outcome === 'failure'}>
                    <span class="job-name" title={job.name}>{jobLabel(job.name)}</span>
                    <span class="job-outcome {outcomeClass(job.outcome)}">{job.outcome}</span>
                    {#if job.overdue}
                        <span class="overdue-pill" title="No run seen within 2× the expected interval">
                            <TriangleAlert size={10} strokeWidth={2.6} /> OVERDUE
                        </span>
                    {/if}
                    <span class="job-duration" title="Duration of the last run">{formatDuration(job.duration_ms)}</span>
                    <span class="job-rows" title="Rows affected by the last run"
                        >{formatRows(job.rows_affected)} rows</span
                    >
                    <span class="job-when" title={job.finished}>{rel(job.finished, nowMs)}</span>
                </li>
                {#if job.outcome === 'failure' && job.reason}
                    <li class="maint-reason">{job.reason}</li>
                {/if}
            {/each}
        </ul>
    {/if}
</section>

<style>
    .maint {
        background: var(--color-card);
        border: var(--spacing-bw) solid var(--color-border);
        border-radius: var(--radius-default);
        box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow);
        padding: 10px 12px;
        font-family: 'JetBrains Mono', monospace;
    }

    .maint-head {
        display: flex;
        align-items: center;
        gap: 6px;
        color: var(--color-muted);
        padding-bottom: 8px;
        border-bottom: 1.5px dashed var(--color-border);
        margin-bottom: 8px;
    }

    .maint-title {
        font-size: 0.65rem;
        font-weight: 800;
        letter-spacing: 0.12em;
        text-transform: uppercase;
        color: var(--color-fg);
    }

    .maint-empty,
    .maint-error {
        font-size: 0.68rem;
        color: var(--color-muted);
        padding: 8px 2px;
    }

    .maint-error {
        color: var(--color-red);
        font-weight: 700;
    }

    .maint-list {
        list-style: none;
        margin: 0;
        padding: 0;
        display: flex;
        flex-direction: column;
        gap: 4px;
    }

    .maint-row {
        display: grid;
        grid-template-columns: 1fr auto auto auto auto auto;
        align-items: center;
        gap: 8px;
        font-size: 0.68rem;
        padding: 4px 2px;
    }

    .maint-row.failing {
        color: var(--color-red);
    }

    .job-name {
        font-weight: 700;
        color: var(--color-fg);
        text-transform: uppercase;
        letter-spacing: 0.04em;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .job-outcome {
        display: inline-flex;
        align-items: center;
        font-size: 0.58rem;
        font-weight: 800;
        letter-spacing: 0.1em;
        text-transform: uppercase;
        padding: 2px 7px;
        border-radius: var(--radius-default);
        border: var(--spacing-bw) solid var(--color-border);
        box-shadow: 2px 2px 0 var(--color-shadow);
        color: #fff;
    }

    .job-outcome.ok {
        background: var(--color-green);
    }

    .job-outcome.fail {
        background: var(--color-red);
    }

    .job-outcome.skip {
        background: var(--color-amber);
    }

    .overdue-pill {
        display: inline-flex;
        align-items: center;
        gap: 3px;
        font-size: 0.55rem;
        font-weight: 800;
        letter-spacing: 0.1em;
        padding: 2px 6px;
        border-radius: var(--radius-default);
        border: var(--spacing-bw) solid var(--color-border);
        box-shadow: 2px 2px 0 var(--color-shadow);
        background: var(--color-amber);
        color: #fff;
    }

    .job-duration,
    .job-rows {
        font-size: 0.62rem;
        color: var(--color-muted);
        font-variant-numeric: tabular-nums;
    }

    .job-when {
        font-size: 0.62rem;
        color: var(--color-subtle);
        font-variant-numeric: tabular-nums;
        min-width: 56px;
        text-align: right;
    }

    .maint-reason {
        list-style: none;
        font-size: 0.6rem;
        color: var(--color-red);
        padding: 0 2px 4px 2px;
        margin-top: -2px;
        font-family: 'JetBrains Mono', monospace;
        word-break: break-word;
    }

    @media (max-width: 720px) {
        .maint-row {
            grid-template-columns: 1fr auto auto;
            row-gap: 2px;
        }

        .job-duration,
        .job-rows {
            grid-column: 2;
        }

        .job-when {
            grid-column: 3;
        }
    }
</style>
