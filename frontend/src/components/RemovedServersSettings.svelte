<script>
    import { untrack } from 'svelte';
    import { RotateCcw, Trash2 } from '@lucide/svelte';
    import { fetchRemovedServers, restoreRemovedServer } from '../lib/api.js';
    import { appState } from '../lib/state.svelte.js';
    import ConfirmDialog from './ConfirmDialog.svelte';

    /** @type {import('../lib/api.js').RemovedServer[]} */
    let removedServers = $state([]);
    let loading = $state(false);
    let error = $state('');
    /** @type {Set<string>} */
    let restoringHosts = $state(new Set());
    /** @type {import('../lib/api.js').RemovedServer|null} */
    let restoreCandidate = $state(null);

    async function refresh() {
        if (loading) return;
        loading = true;
        try {
            removedServers = await fetchRemovedServers();
            error = '';
        } catch (e) {
            error = `Could not load removed servers: ${e?.message ?? String(e)}`;
        } finally {
            loading = false;
        }
    }

    async function restore(server) {
        if (restoringHosts.has(server.host)) return;
        restoringHosts = new Set([...restoringHosts, server.host]);
        try {
            await restoreRemovedServer(server.host);
            removedServers = removedServers.filter((item) => item.host !== server.host);
            appState.notifyRemovedServersChanged();
            restoreCandidate = null;
        } catch (e) {
            error = `Restore ${server.host} failed: ${e?.message ?? String(e)}`;
        } finally {
            const next = new Set(restoringHosts);
            next.delete(server.host);
            restoringHosts = next;
        }
    }

    // The server signals durable tombstone changes through this revision. Read
    // it here rather than polling so an operator sees changes from other tabs.
    $effect(() => {
        appState.removedServersRevision;
        untrack(refresh);
    });

    function removalTime(value) {
        const parsed = new Date(value);
        return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString();
    }
</script>

<section class="settings-group" aria-labelledby="removed-servers-heading">
    <div class="removed-heading">
        <div>
            <div class="section-header" id="removed-servers-heading">
                <Trash2 size={14} strokeWidth={2.5} /> Removed Servers
            </div>
            <p class="section-hint">
                Restore removes a server's tombstone only. The agent must register again before it returns to the live
                Servers view.
            </p>
        </div>
        <button class="btn-tbl" type="button" onclick={refresh} disabled={loading}>
            <RotateCcw size={12} />
            {loading ? 'Refreshing…' : 'Refresh'}
        </button>
    </div>

    <div class="target-tbl-wrap">
        <table class="target-tbl">
            <thead>
                <tr>
                    <th scope="col">Host</th>
                    <th scope="col">Removed At</th>
                    <th scope="col">Removed By</th>
                    <th scope="col">Reason</th>
                    <th scope="col"><span class="sr-only">Restore</span></th>
                </tr>
            </thead>
            <tbody>
                {#if error}
                    <tr class="empty-row">
                        <td colspan="5" class="target-tbl-empty removed-error"><span role="alert">{error}</span></td>
                    </tr>
                {:else if loading && removedServers.length === 0}
                    <tr class="empty-row">
                        <td colspan="5" class="target-tbl-empty"><span role="status">Loading removed servers…</span></td
                        >
                    </tr>
                {:else if removedServers.length === 0}
                    <tr class="empty-row"
                        ><td colspan="5" class="target-tbl-empty">No permanently removed servers.</td></tr
                    >
                {:else}
                    {#each removedServers as server (server.host)}
                        <tr>
                            <td class="removed-host">{server.host}</td>
                            <td>{removalTime(server.removed_at)}</td>
                            <td>{server.removed_by || '—'}</td>
                            <td class="removed-reason">{server.reason || '—'}</td>
                            <td class="removed-restore">
                                <button
                                    class="btn-tbl"
                                    type="button"
                                    onclick={() => (restoreCandidate = server)}
                                    disabled={restoringHosts.has(server.host)}
                                    aria-label="Restore {server.host}"
                                >
                                    <RotateCcw size={12} />
                                    {restoringHosts.has(server.host) ? 'Restoring…' : 'Restore'}
                                </button>
                            </td>
                        </tr>
                    {/each}
                {/if}
            </tbody>
        </table>
    </div>
</section>

{#if restoreCandidate}
    <ConfirmDialog
        title="Restore {restoreCandidate.host}?"
        message="This removes the durable tombstone. The agent must register again before it can return to the Servers view."
        confirmLabel="Restore"
        cancelLabel="Cancel"
        onconfirm={() => restore(restoreCandidate)}
        oncancel={() => (restoreCandidate = null)}
    />
{/if}

<style>
    .settings-group {
        margin-bottom: 30px;
    }
    .removed-heading {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 14px;
        margin-bottom: 12px;
    }
    .section-header {
        display: flex;
        align-items: center;
        gap: 8px;
        font-size: 0.78rem;
        font-weight: 800;
        text-transform: uppercase;
        letter-spacing: 0.1em;
        color: var(--color-accent);
        margin-bottom: 10px;
        padding: 6px 0;
    }
    .section-hint {
        margin: -4px 0 0;
        color: var(--color-muted);
        font-size: 0.72rem;
        line-height: 1.5;
    }
    .target-tbl-wrap {
        border: var(--spacing-bw) solid var(--color-border);
        border-radius: var(--radius-default);
        box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow);
        margin-bottom: 12px;
        overflow: hidden;
    }
    .target-tbl {
        width: 100%;
        border-collapse: collapse;
        font-size: 13px;
    }
    .target-tbl th,
    .target-tbl td {
        padding: 11px 14px;
        white-space: nowrap;
    }
    .target-tbl th {
        font-family: 'JetBrains Mono', monospace;
        font-size: 11px;
        font-weight: 500;
        text-transform: uppercase;
        letter-spacing: 0.5px;
        color: var(--color-muted);
        text-align: left;
        border-bottom: 2px solid var(--color-border);
        background: var(--color-card);
    }
    .target-tbl td {
        border-bottom: 1px solid var(--color-surface);
        vertical-align: middle;
        height: 52px;
        box-sizing: border-box;
    }
    .target-tbl tbody tr:last-child td {
        border-bottom: none;
    }
    .target-tbl tbody tr:not(.empty-row):hover td {
        background: var(--color-surface);
    }
    .empty-row td {
        height: 52px;
        box-sizing: border-box;
    }
    .target-tbl-empty {
        text-align: center;
        color: var(--color-subtle);
        font-size: 13px;
    }
    .removed-host,
    .removed-reason {
        overflow: hidden;
        text-overflow: ellipsis;
        max-width: 0;
    }
    .removed-host {
        width: 25%;
        font-family: 'JetBrains Mono', monospace;
        font-weight: 600;
    }
    .removed-reason {
        width: 99%;
    }
    .removed-restore {
        width: 1%;
    }
    .removed-error {
        color: var(--color-red);
    }
    .btn-tbl {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        font-family: 'Work Sans', sans-serif;
        font-size: 11px;
        font-weight: 700;
        padding: 4px 9px;
        border: 2px solid var(--color-border);
        border-radius: 5px;
        box-shadow: 2px 2px 0 var(--color-shadow);
        cursor: pointer;
        background: var(--color-card);
        color: var(--color-fg);
        transition:
            transform 0.1s,
            box-shadow 0.1s;
        white-space: nowrap;
    }
    .btn-tbl:hover {
        transform: translate(-1px, -1px);
        box-shadow: 3px 3px 0 var(--color-shadow);
    }
    .btn-tbl:active {
        transform: translate(1px, 1px);
        box-shadow: 1px 1px 0 var(--color-shadow);
    }
    .btn-tbl:disabled {
        opacity: 0.65;
        cursor: wait;
    }
    .sr-only {
        position: absolute;
        width: 1px;
        height: 1px;
        overflow: hidden;
        clip: rect(0, 0, 0, 0);
        white-space: nowrap;
    }
    @media (max-width: 640px) {
        .removed-heading {
            flex-direction: column;
        }
    }
</style>
