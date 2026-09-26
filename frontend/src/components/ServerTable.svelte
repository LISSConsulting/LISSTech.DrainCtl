<script>
    import { untrack } from 'svelte';
    import {
        appState,
        removeServerMetrics,
        removeEvtSpikeState,
        setDetectorStatus,
        setSelection,
        toggleSelection,
        pruneSelectionFor,
        clearSelection,
    } from '../lib/state.svelte.js';
    import {
        deleteServer,
        fetchEvtSpikeStatus,
        fetchServers,
        permanentRemoveHosts,
        forceUpdateHosts,
    } from '../lib/api.js';
    import { getThresholdColor, resolveThresholds } from '../lib/thresholds.js';
    import { rel, modeLabel } from '../lib/utils.js';
    import ServerDetail from './ServerDetail.svelte';
    import CellSparkline from './CellSparkline.svelte';
    import ConfirmDialog from './ConfirmDialog.svelte';
    import NotificationExclusionAction from './NotificationExclusionAction.svelte';
    import { toast } from '../lib/toast.svelte.js';
    import { groupServersByRdSessionPool } from '../lib/server-groups.js';

    let { onhistoryclick } = $props();

    /** @type {string|null} */
    let confirmRemoveHost = $state(null);

    /** Hosts queued for permanent-remove confirmation. null when modal closed. */
    /** @type {string[]|null} */
    let confirmBatchRemoveHosts = $state(null);

    /** @type {Set<string>} */
    let expandedHosts = $state(new Set());
    let sortCol = $state(localStorage.getItem('drainctl-sort-col') || 'host');
    let sortDir = $state(parseInt(localStorage.getItem('drainctl-sort-dir') ?? '1', 10) || 1);
    let search = $state(localStorage.getItem('drainctl-search') || '');
    let page = $state(0);
    const PAGE_SIZES = [15, 30, 50];
    let pageSize = $state(parseInt(localStorage.getItem('drainctl-page-size') ?? '15', 10) || 15);
    let removeError = $state('');
    /** @type {ReturnType<typeof setTimeout>|null} */
    let removeErrorTimer = null;
    // Clear the error-dismiss timer on component destroy so it never fires on
    // an unmounted instance (e.g. navigating away while a remove was in flight).
    $effect(() => () => {
        clearTimeout(removeErrorTimer);
    });
    /** @type {Set<string>} */
    let removingHosts = $state(new Set());

    // Prune expandedHosts when servers are removed via SSE (external deletion).
    // When the local UI deletes a server via doRemoveServer(), expandedHosts is
    // cleaned up there. But an SSE server_deleted event from another browser only
    // removes the host from appState.servers — expandedHosts retains the stale
    // entry. This effect tracks appState.servers reactively and removes any host
    // from expandedHosts that is no longer in the server list, so that if the
    // same hostname re-registers later it does not appear pre-expanded.
    $effect(() => {
        const liveHosts = new Set(appState.servers.map((s) => s.host));
        const current = untrack(() => expandedHosts);
        const stale = [...current].filter((h) => !liveHosts.has(h));
        if (stale.length > 0) {
            const next = new Set(current);
            for (const h of stale) next.delete(h);
            expandedHosts = next;
        }
    });
    // The SSE broker suppresses only identical detector-status snapshots.
    // Cold pages still seed through REST before the next readiness, warm-up,
    // or state change arrives.
    /** @type {Set<string>} */
    const seedingEvtSpike = new Set();
    $effect(() => {
        for (const srv of appState.servers) {
            if (appState.detectorStatuses.has(srv.host) || seedingEvtSpike.has(srv.host)) continue;
            seedingEvtSpike.add(srv.host);
            fetchEvtSpikeStatus(srv.host)
                .then((status) => {
                    if (status) setDetectorStatus(srv.host, status);
                })
                .catch(() => {})
                .finally(() => {
                    seedingEvtSpike.delete(srv.host);
                });
        }
    });

    // Reactive clock — ticks every 10 s so that relative timestamps and the
    // grace-period countdown badge stay fresh between 30-second server refreshes.
    let now = $state(Date.now());
    $effect(() => {
        const t = setInterval(() => {
            now = Date.now();
        }, 10_000);
        return () => clearInterval(t);
    });

    // Promote stale rows locally between successful API refreshes. Browser
    // sleep/background throttling can resume the SSE connection before the
    // next 30-second roster fetch completes; without this guard, the last
    // reported Grace/Alert badge can survive for hours even though Last Seen
    // is visibly stale. A fresh poll/SSE heartbeat replaces `off` normally.
    $effect(() => {
        const currentTime = now;
        const configuredPoll = Number(appState.config?.poll_interval);
        const pollSeconds = Number.isFinite(configuredPoll) && configuredPoll > 0 ? configuredPoll : 300;
        const staleAfterMs = pollSeconds * 3 * 1000;
        let changed = false;
        const next = appState.servers.map((server) => {
            if (server.status === 'off' || !server.last_seen) return server;
            const lastSeen = new Date(server.last_seen).getTime();
            if (!Number.isFinite(lastSeen) || currentTime - lastSeen < staleAfterMs) return server;
            changed = true;
            return { ...server, status: 'off' };
        });
        if (changed) appState.servers = next;
    });

    const STATUS_ORDER = { alert: 0, warning: 1, grace: 2, off: 3, ok: 4 };

    /**
     * Returns a human-readable countdown string for a grace deadline ISO timestamp.
     * Takes `now` explicitly so the template tracks it as a reactive dependency.
     * @param {string|null|undefined} iso
     * @param {number} _now - current epoch ms (reactive)
     * @returns {string|null}
     */
    function graceCountdown(iso, _now) {
        if (!iso) return null;
        const d = new Date(iso);
        if (isNaN(d)) return null;
        const ms = d - _now;
        const s = Math.floor(ms / 1000);
        if (s <= 0) return 'expired';
        if (s < 60) return s + 's left';
        const m = Math.floor(s / 60);
        if (m < 60) return m + 'm left';
        const h = Math.floor(m / 60);
        if (h < 24) {
            const rem = m % 60;
            return h + 'h ' + (rem > 0 ? rem + 'm ' : '') + 'left';
        }
        const days = Math.floor(h / 24);
        const remH = h % 24;
        return days + 'd ' + (remH > 0 ? remH + 'h ' : '') + 'left';
    }

    function statusLabel(s) {
        return { ok: 'Healthy', warning: 'Warning', grace: 'Grace', alert: 'Alert', off: 'Offline' }[s] || s;
    }

    // Counts per status for the filter pill labels ("Grace (2)").
    let statusCounts = $derived.by(() => {
        const counts = { ok: 0, warning: 0, grace: 0, alert: 0, off: 0 };
        for (const sv of appState.servers) {
            if (sv.status in counts) counts[sv.status]++;
        }
        return counts;
    });

    let sorted = $derived.by(() => {
        let s = appState.servers.filter((sv) => {
            const matchText = !search || sv.host.toLowerCase().includes(search.toLowerCase());
            const matchStatus = appState.serverFilter === 'all' || sv.status === appState.serverFilter;
            return matchText && matchStatus;
        });
        return s.sort((a, b) => {
            let va, vb;
            if (sortCol === 'status') {
                va = STATUS_ORDER[a.status] ?? 4;
                vb = STATUS_ORDER[b.status] ?? 4;
            } else if (sortCol === 'host') {
                va = a.host;
                vb = b.host;
            } else if (sortCol === 'sessions') {
                va = a.sessions || 0;
                vb = b.sessions || 0;
            } else if (sortCol === 'cpu') {
                va = a.perf?.cpu_pct || 0;
                vb = b.perf?.cpu_pct || 0;
            } else if (sortCol === 'mem') {
                va = a.perf?.mem_total_mb > 0 ? (1 - a.perf.mem_avail_mb / a.perf.mem_total_mb) * 100 : 0;
                vb = b.perf?.mem_total_mb > 0 ? (1 - b.perf.mem_avail_mb / b.perf.mem_total_mb) * 100 : 0;
            } else if (sortCol === 'delay') {
                va = a.perf?.input_delay_p95_ms || 0;
                vb = b.perf?.input_delay_p95_ms || 0;
            } else if (sortCol === 'last_seen') {
                va = new Date(a.last_seen || 0);
                vb = new Date(b.last_seen || 0);
            } else {
                va = a[sortCol];
                vb = b[sortCol];
            }
            if (va < vb) return -sortDir;
            if (va > vb) return sortDir;
            return 0;
        });
    });

    function sort(col) {
        if (sortCol === col) sortDir = -sortDir;
        else {
            sortCol = col;
            sortDir = 1;
        }
    }

    function toggleRow(host) {
        const next = new Set(expandedHosts);
        if (next.has(host)) next.delete(host);
        else next.add(host);
        expandedHosts = next;
    }

    function requestRemoveServer(host) {
        confirmRemoveHost = host;
    }

    async function doRemoveServer() {
        const host = confirmRemoveHost;
        confirmRemoveHost = null;
        if (!host || removingHosts.has(host)) return;
        removingHosts = new Set([...removingHosts, host]);
        try {
            await deleteServer(host);
            appState.servers = appState.servers.filter((s) => s.host !== host);
            removeServerMetrics(host);
            removeEvtSpikeState(host);
            if (expandedHosts.has(host)) {
                const next = new Set(expandedHosts);
                next.delete(host);
                expandedHosts = next;
            }
        } catch (e) {
            removeError = 'Remove failed: ' + (e?.message ?? String(e));
            clearTimeout(removeErrorTimer);
            removeErrorTimer = setTimeout(() => (removeError = ''), 5000);
        } finally {
            const next = new Set(removingHosts);
            next.delete(host);
            removingHosts = next;
        }
    }

    // ------------------------------------------------------------------
    // Batch actions (BatchOperations)
    //
    // Selection state is owned by `appState.selectedHosts`; this component is
    // the integration owner for checkbox UI, toolbar rendering, and
    // reconciliation. The actual REST calls are delegated to the PermanentRemoval
    // and ForceUpdate contracts via `permanentRemoveHosts` / `forceUpdateHosts`
    // in lib/api.js — this file does NOT define endpoints.
    // ------------------------------------------------------------------

    /**
     * Action hosts are the current selection intersected with the live roster.
     * Filters and pagination only control which rows are visible; they never
     * silently narrow an already selected batch.
     * @type {string[]}
     */
    let actionHosts = $derived.by(() => {
        const live = new Set(appState.servers.map((server) => server.host));
        return [...appState.selectedHosts].filter((host) => live.has(host)).sort();
    });

    /** All currently-rendered hosts (sorted by host) for select-all toggle. */
    let visibleHosts = $derived(paged.map((s) => s.host).sort());

    /** Tri-state select-all: 'none' | 'some' | 'all'. */
    let selectAllState = $derived.by(() => {
        const total = visibleHosts.length;
        if (total === 0) return 'none';
        let count = 0;
        for (const h of visibleHosts) if (appState.selectedHosts.has(h)) count++;
        if (count === 0) return 'none';
        if (count === total) return 'all';
        return 'some';
    });

    /**
     * Header selection affects only visible rows: add them when any is absent,
     * or subtract them when all are already selected. Existing selection on
     * another page or behind a filter is deliberately preserved.
     */
    function onSelectAllClick() {
        const next = new Set(appState.selectedHosts);
        if (selectAllState === 'all') {
            for (const host of visibleHosts) next.delete(host);
        } else {
            for (const host of visibleHosts) next.add(host);
        }
        setSelection(next);
    }

    /** Toggle a single host's selection. Stops propagation so the row does not expand. */
    function onRowCheckboxClick(host, e) {
        e.stopPropagation();
        toggleSelection(host);
    }

    /** Stop propagation for the row checkbox keydown so it doesn't expand the row. */
    function onRowCheckboxKeydown(host, e) {
        if (e.key === ' ' || e.key === 'Enter') {
            e.preventDefault();
            e.stopPropagation();
            toggleSelection(host);
        }
    }

    /**
     * Reconcile selection against the live server list whenever it changes.
     * This is the safety net that ensures a batch action can never target a
     * host that has just been removed via SSE or a /servers poll refresh.
     * Reading `appState.servers` (and not `paged`) here is intentional — we
     * want to drop removed hosts from selection regardless of which page or
     * filter is active when the SSE event arrives.
     */
    $effect(() => {
        const liveHosts = appState.servers.map((s) => s.host);
        pruneSelectionFor(liveHosts);
    });

    /** Open the destructive confirm modal with the exact live selection. */
    function requestBatchRemove() {
        if (actionHosts.length === 0) return;
        confirmBatchRemoveHosts = actionHosts;
    }

    /**
     * After confirmation, invoke PermanentRemoval's batch endpoint. Outcomes
     * are reflected in the UI:
     *   - removed[]  → row is dropped locally and from selection
     *   - skipped[]  → left alone (already gone); cleaned from selection
     *   - errors[]   → left alone, surfaced as a non-blocking error banner
     */
    async function doBatchRemove() {
        const hosts = confirmBatchRemoveHosts;
        confirmBatchRemoveHosts = null;
        if (!hosts || hosts.length === 0) return;
        try {
            const result = await permanentRemoveHosts(hosts);
            const removedSet = new Set(result.removed);
            const skippedSet = new Set(result.skipped);
            // Filter servers list locally so the table updates immediately;
            // the SSE server_permanently_removed event will reconcile other
            // browser sessions a moment later.
            if (removedSet.size > 0 || skippedSet.size > 0) {
                appState.servers = appState.servers.filter((s) => !removedSet.has(s.host) && !skippedSet.has(s.host));
            }
            for (const host of new Set([...removedSet, ...skippedSet])) {
                removeServerMetrics(host);
                removeEvtSpikeState(host);
            }

            // Always drop the targeted hosts from selection — even on errors
            // we want to clear them so the operator doesn't double-click.
            const next = new Set(appState.selectedHosts);
            for (const h of hosts) next.delete(h);
            // setSelection preserves Svelte 5 reactivity by assigning a fresh Set.
            setSelection(next);
            if (result.errors && result.errors.length > 0) {
                const sample = result.errors
                    .slice(0, 3)
                    .map((e) => `${e.host}: ${e.reason}`)
                    .join('; ');
                removeError =
                    `Removed ${result.removed.length}, skipped ${result.skipped.length}, failed ${result.errors.length}. ` +
                    sample +
                    (result.errors.length > 3 ? '…' : '');
                clearTimeout(removeErrorTimer);
                // An error can follow a backend transaction that changed the
                // live roster before returning its failure. Re-read that
                // authoritative roster rather than leaving a stale row until
                // the next 30-second poll.
                try {
                    appState.servers = await fetchServers();
                } catch {
                    removeError += ' Live roster could not be reconciled.';
                }
                removeErrorTimer = setTimeout(() => (removeError = ''), 8000);
            }
        } catch (e) {
            removeError = 'Batch remove failed: ' + (e?.message ?? String(e));
            clearTimeout(removeErrorTimer);
            removeErrorTimer = setTimeout(() => (removeError = ''), 8000);
        }
    }

    /**
     * Force-update dispatch returns an immediate per-host acceptance result;
     * terminal agent outcomes arrive separately over SSE. Both are transient
     * toasts so they never displace the live server table.
     */
    function formatForceUpdateResult(result) {
        const groups = new Map();
        const add = (outcome, host, version = '') => {
            const current = groups.get(outcome) ?? { hosts: [], versions: new Set() };
            current.hosts.push(host);
            if (version) current.versions.add(version);
            groups.set(outcome, current);
        };
        for (const item of result.results ?? []) add(item.outcome, item.host, item.version);
        for (const item of result.errors ?? []) add('failed', item.host);

        const order = ['accepted', 'duplicate', 'offline', 'unsupported', 'failed'];
        const segments = [];
        for (const outcome of order) {
            const group = groups.get(outcome);
            if (!group) continue;
            const sample = group.hosts.slice(0, 2).join(', ');
            const more = group.hosts.length > 2 ? `, +${group.hosts.length - 2}` : '';
            const versions =
                outcome === 'unsupported' && group.versions.size > 0 ? ` @ ${[...group.versions].join('/')}` : '';
            segments.push(`${group.hosts.length} ${outcome}${versions}${sample ? `: ${sample}${more}` : ''}`);
        }
        const total = [...groups.values()].reduce((sum, group) => sum + group.hosts.length, 0);
        return total > 0
            ? `${total} server${total === 1 ? '' : 's'} · ${segments.join(' · ')}`
            : 'No servers accepted the force-update request.';
    }

    function notifyForceUpdateCompletion(result) {
        const detail =
            result.old_version || result.new_version
                ? `${result.old_version ?? 'unknown'} → ${result.new_version ?? 'unknown'}`
                : result.reason;
        const message = `Force update ${result.outcome}: ${result.host}${detail ? ` (${detail})` : ''}`;
        if (result.outcome === 'completed') toast.ok(message);
        else if (result.outcome === 'duplicate') toast.info(message);
        else toast.err(message);
    }

    let notifiedForceUpdateCompletions = new Set();
    $effect(() => {
        for (const [key, result] of appState.forceUpdateCompletions) {
            if (notifiedForceUpdateCompletions.has(key)) continue;
            notifiedForceUpdateCompletions.add(key);
            notifyForceUpdateCompletion(result);
        }
    });

    async function doBatchForceUpdate() {
        const hosts = actionHosts;
        if (hosts.length === 0) return;
        try {
            const result = await forceUpdateHosts(hosts);
            const hasFailure =
                (result.errors?.length ?? 0) > 0 || (result.results ?? []).some((item) => item.outcome !== 'accepted');
            toast[hasFailure ? 'err' : 'ok'](`Force update request: ${formatForceUpdateResult(result)}`);
        } catch (e) {
            toast.err('Force update failed: ' + (e?.message ?? String(e)));
        }
    }

    /** The toolbar count and action scope are the same live selection. */
    let selectionCount = $derived(actionHosts.length);

    // Per-metric thresholds derived from config (same logic as ServerDetail).
    let perfCfg = $derived(appState.config?.performance ?? null);
    let cpuThresh = $derived(resolveThresholds('cpu', perfCfg));
    let memThresh = $derived(resolveThresholds('mem', perfCfg));
    let delayThresh = $derived(resolveThresholds('inputDelay', perfCfg));

    // Session warning threshold — raw session count from alert-sensitivity config.
    // Used for both the sparkline color and (potentially) future session-cell coloring.
    let sessionWarnThresh = $derived(appState.config?.session_warning_threshold ?? 80);

    /**
     * Map a getThresholdColor result to a CSS color variable string.
     * Returns empty string when there is no data ('neutral').
     * @param {'green'|'amber'|'red'|'neutral'} color
     * @returns {string}
     */
    function thresholdStyle(color) {
        if (color === 'green') return 'color:var(--color-green)';
        if (color === 'amber') return 'color:var(--color-amber)';
        if (color === 'red') return 'color:var(--color-red)';
        return '';
    }

    /**
     * Map a getThresholdColor token to the matching CSS color variable.
     * Used to tint sparklines — returns the muted variable for 'neutral'
     * (no data), though the sparkline won't render at all when history is empty.
     * @param {'green'|'amber'|'red'|'neutral'} color
     * @returns {string}
     */
    function sparkColor(color) {
        if (color === 'green') return 'var(--color-green)';
        if (color === 'amber') return 'var(--color-amber)';
        if (color === 'red') return 'var(--color-red)';
        return 'var(--color-muted)';
    }

    // Persist search
    $effect(() => {
        localStorage.setItem('drainctl-search', search);
    });
    $effect(() => {
        localStorage.setItem('drainctl-sort-col', sortCol);
    });
    $effect(() => {
        localStorage.setItem('drainctl-sort-dir', String(sortDir));
    });

    // Reset page when filters/search change.
    $effect(() => {
        search;
        appState.serverFilter;
        page = 0;
    });

    // Persist page size preference.
    $effect(() => {
        localStorage.setItem('drainctl-page-size', String(pageSize));
    });

    let grouped = $derived.by(() => groupServersByRdSessionPool(sorted));
    let groupedServers = $derived(grouped.flatMap((group) => group.servers));
    let totalPages = $derived(Math.max(1, Math.ceil(groupedServers.length / pageSize)));
    let paged = $derived(groupedServers.slice(page * pageSize, (page + 1) * pageSize));
    let pagedGroups = $derived.by(() => {
        const filteredCounts = new Map(grouped.map((group) => [group.pool, group.servers.length]));
        return groupServersByRdSessionPool(paged).map((group) => ({
            ...group,
            count: filteredCounts.get(group.pool) ?? group.servers.length,
        }));
    });
</script>

<div class="grid">
    <div class="section-label">SERVERS</div>

    <!-- Filter bar -->
    <div class="filter-bar">
        <input
            class="srv-search settings-input"
            type="search"
            placeholder="Filter by hostname..."
            aria-label="Filter servers by hostname"
            bind:value={search}
            style="max-width:300px"
        />
        <div class="filter-pills" role="group" aria-label="Filter servers by status">
            {#each ['all', 'ok', 'warning', 'grace', 'alert', 'off'] as f}
                {@const count = f === 'all' ? appState.servers.length : statusCounts[f]}
                <button
                    class="filter-pill {f === 'all' ? '' : f} {appState.serverFilter === f ? 'active' : ''}"
                    aria-pressed={appState.serverFilter === f}
                    onclick={() => (appState.serverFilter = f)}
                >
                    {f === 'all' ? 'All' : statusLabel(f)}{count ? ' (' + count + ')' : ''}
                </button>
            {/each}
        </div>
    </div>

    {#if removeError}
        <div class="grid-toast">{removeError}</div>
    {/if}

    {#if appState.servers.length && !sorted.length}
        <div class="empty"><p>No servers match the current filter.</p></div>
    {/if}

    {#if appState.selectedHosts.size > 0}
        <div class="batch-toolbar" role="region" aria-label="Batch actions" data-test="batch-toolbar">
            <div class="batch-summary">
                <strong>{selectionCount}</strong>
                server{selectionCount === 1 ? '' : 's'} selected
            </div>
            <div class="batch-actions">
                <button
                    class="btn-brutal batch-btn"
                    onclick={doBatchForceUpdate}
                    disabled={actionHosts.length === 0}
                    aria-label="Force update {selectionCount} selected servers"
                >
                    Force Update ({selectionCount})
                </button>
                <button
                    class="btn-brutal batch-btn batch-btn-danger"
                    onclick={requestBatchRemove}
                    disabled={actionHosts.length === 0}
                    aria-label="Permanently remove {selectionCount} selected servers"
                >
                    Remove ({selectionCount})
                </button>
                <button class="btn-brutal batch-btn-batch-cancel" onclick={clearSelection} aria-label="Clear selection">
                    Cancel
                </button>
            </div>
        </div>
    {/if}

    {#if !appState.servers.length}
        <div class="empty">
            <h2 class="serif">No servers registered</h2>
            <p>Waiting for agents to connect...</p>
        </div>
    {:else if sorted.length}
        <div class="card">
            <table class="srv-tbl">
                <thead>
                    <tr>
                        <th class="sel-col">
                            <input
                                type="checkbox"
                                class="srv-check"
                                aria-label="Select all servers on this page"
                                checked={selectAllState === 'all'}
                                indeterminate={selectAllState === 'some'}
                                disabled={visibleHosts.length === 0}
                                onclick={(e) => {
                                    e.stopPropagation();
                                    onSelectAllClick();
                                }}
                                onkeydown={(e) => {
                                    if (e.key === ' ' || e.key === 'Enter') {
                                        e.preventDefault();
                                        e.stopPropagation();
                                        onSelectAllClick();
                                    }
                                }}
                            />
                        </th>
                        <th></th>
                        <th
                            onclick={() => sort('host')}
                            class="sortable"
                            tabindex="0"
                            onkeydown={(e) => {
                                if (e.key === 'Enter' || e.key === ' ') {
                                    e.preventDefault();
                                    e.currentTarget.click();
                                }
                            }}
                            aria-sort={sortCol === 'host' ? (sortDir === 1 ? 'ascending' : 'descending') : 'none'}
                            >Host {sortCol === 'host' ? (sortDir === 1 ? '↑' : '↓') : ''}</th
                        >
                        <th
                            onclick={() => sort('status')}
                            class="sortable"
                            tabindex="0"
                            onkeydown={(e) => {
                                if (e.key === 'Enter' || e.key === ' ') {
                                    e.preventDefault();
                                    e.currentTarget.click();
                                }
                            }}
                            aria-sort={sortCol === 'status' ? (sortDir === 1 ? 'ascending' : 'descending') : 'none'}
                            >Status {sortCol === 'status' ? (sortDir === 1 ? '↑' : '↓') : ''}</th
                        >
                        <th>Mode</th>
                        <th>Since</th>
                        <th
                            onclick={() => sort('sessions')}
                            class="sortable spark-col"
                            tabindex="0"
                            onkeydown={(e) => {
                                if (e.key === 'Enter' || e.key === ' ') {
                                    e.preventDefault();
                                    e.currentTarget.click();
                                }
                            }}
                            aria-sort={sortCol === 'sessions' ? (sortDir === 1 ? 'ascending' : 'descending') : 'none'}
                            >Sessions {sortCol === 'sessions' ? (sortDir === 1 ? '↑' : '↓') : ''}</th
                        >
                        <th
                            onclick={() => sort('cpu')}
                            class="sortable spark-col"
                            tabindex="0"
                            onkeydown={(e) => {
                                if (e.key === 'Enter' || e.key === ' ') {
                                    e.preventDefault();
                                    e.currentTarget.click();
                                }
                            }}
                            aria-sort={sortCol === 'cpu' ? (sortDir === 1 ? 'ascending' : 'descending') : 'none'}
                            >CPU {sortCol === 'cpu' ? (sortDir === 1 ? '↑' : '↓') : ''}</th
                        >
                        <th
                            onclick={() => sort('mem')}
                            class="sortable spark-col"
                            tabindex="0"
                            onkeydown={(e) => {
                                if (e.key === 'Enter' || e.key === ' ') {
                                    e.preventDefault();
                                    e.currentTarget.click();
                                }
                            }}
                            aria-sort={sortCol === 'mem' ? (sortDir === 1 ? 'ascending' : 'descending') : 'none'}
                            >MEM {sortCol === 'mem' ? (sortDir === 1 ? '↑' : '↓') : ''}</th
                        >
                        <th
                            onclick={() => sort('delay')}
                            class="sortable spark-col"
                            tabindex="0"
                            onkeydown={(e) => {
                                if (e.key === 'Enter' || e.key === ' ') {
                                    e.preventDefault();
                                    e.currentTarget.click();
                                }
                            }}
                            aria-sort={sortCol === 'delay' ? (sortDir === 1 ? 'ascending' : 'descending') : 'none'}
                            >Input Delay {sortCol === 'delay' ? (sortDir === 1 ? '↑' : '↓') : ''}</th
                        >
                        <th
                            onclick={() => sort('last_seen')}
                            class="sortable"
                            tabindex="0"
                            onkeydown={(e) => {
                                if (e.key === 'Enter' || e.key === ' ') {
                                    e.preventDefault();
                                    e.currentTarget.click();
                                }
                            }}
                            aria-sort={sortCol === 'last_seen' ? (sortDir === 1 ? 'ascending' : 'descending') : 'none'}
                            >Last Seen {sortCol === 'last_seen' ? (sortDir === 1 ? '↑' : '↓') : ''}</th
                        >
                        <th></th>
                    </tr>
                </thead>
                <tbody>
                    {#each pagedGroups as group (group.pool)}
                        <tr class="pool-group-header">
                            <th colspan="12" scope="rowgroup">
                                <span>RD SESSION POOL</span>
                                <strong>{group.pool}</strong>
                                <span class="pool-group-count">{group.count} server{group.count === 1 ? '' : 's'}</span>
                            </th>
                        </tr>
                        {#each group.servers as srv (srv.host)}
                            {@const memPct =
                                srv.perf?.mem_total_mb > 0
                                    ? (1 - srv.perf.mem_avail_mb / srv.perf.mem_total_mb) * 100
                                    : null}
                            {@const cpuColor = srv.perf
                                ? getThresholdColor(srv.perf.cpu_pct, cpuThresh.warn, cpuThresh.crit)
                                : 'neutral'}
                            {@const memColor =
                                memPct != null ? getThresholdColor(memPct, memThresh.warn, memThresh.crit) : 'neutral'}
                            {@const delayColor = srv.perf
                                ? getThresholdColor(srv.perf.input_delay_p95_ms, delayThresh.warn, delayThresh.crit)
                                : 'neutral'}
                            {@const sessPct =
                                srv.max_sessions > 0 ? ((srv.sessions ?? 0) / srv.max_sessions) * 100 : null}
                            {@const sessColor = getThresholdColor(sessPct, sessionWarnThresh, 100)}
                            {@const cpuStyle = thresholdStyle(cpuColor)}
                            {@const memStyle = thresholdStyle(memColor)}
                            {@const delayStyle = thresholdStyle(delayColor)}
                            {@const srvHistory = appState.serverMetrics.get(srv.host)}
                            <tr
                                class="clickable {expandedHosts.has(srv.host) ? 'sel' : ''} {appState.selectedHosts.has(
                                    srv.host,
                                )
                                    ? 'row-sel'
                                    : ''}"
                                data-host={srv.host}
                                data-status={srv.status}
                                tabindex="0"
                                aria-expanded={expandedHosts.has(srv.host)}
                                aria-selected={appState.selectedHosts.has(srv.host)}
                                onclick={() => toggleRow(srv.host)}
                                onkeydown={(e) => {
                                    if (e.key === 'Enter' || e.key === ' ') {
                                        e.preventDefault();
                                        toggleRow(srv.host);
                                    }
                                }}
                            >
                                <td
                                    class="sel-col"
                                    onclick={(e) => e.stopPropagation()}
                                    onkeydown={(e) => e.stopPropagation()}
                                >
                                    <input
                                        type="checkbox"
                                        class="srv-check"
                                        aria-label="Select {srv.host}"
                                        checked={appState.selectedHosts.has(srv.host)}
                                        onclick={(e) => onRowCheckboxClick(srv.host, e)}
                                        onkeydown={(e) => onRowCheckboxKeydown(srv.host, e)}
                                    />
                                </td>
                                <td><span class="dot {srv.status}"></span></td>
                                <td class="mono fw7">{srv.host.split('.')[0]}</td>
                                <td>
                                    <span class="pill {srv.status}">{statusLabel(srv.status)}</span>
                                    {#if srv.status === 'grace'}
                                        {@const cd = graceCountdown(srv.grace_deadline, now)}
                                        {#if cd}
                                            <span class="grace-cd {cd === 'expired' ? 'grace-cd--expired' : ''}"
                                                >{cd}</span
                                            >
                                        {/if}
                                    {/if}
                                </td>
                                <td class="mono">{modeLabel(srv.drain_mode)}</td>
                                <td class="mono muted">{rel(srv.state_changed_at ?? srv.registered_at, now)}</td>
                                <td class="mono spark-cell">
                                    <CellSparkline
                                        data={srvHistory?.map((s) => s.sessions) ?? []}
                                        color={sparkColor(sessColor)}
                                    />
                                    {srv.sessions ?? '—'}
                                </td>
                                <td class="mono spark-cell" style={cpuStyle}>
                                    <CellSparkline
                                        data={srvHistory?.map((s) => s.cpu) ?? []}
                                        color={sparkColor(cpuColor)}
                                    />
                                    {srv.perf?.cpu_pct != null ? srv.perf.cpu_pct.toFixed(1) + '%' : '—'}
                                </td>
                                <td class="mono spark-cell" style={memStyle}>
                                    <CellSparkline
                                        data={srvHistory?.map((s) => s.mem) ?? []}
                                        color={sparkColor(memColor)}
                                    />
                                    {memPct != null ? memPct.toFixed(0) + '%' : '—'}
                                </td>
                                <td class="mono spark-cell" style={delayStyle}>
                                    <CellSparkline
                                        data={srvHistory?.map((s) => s.inputDelay) ?? []}
                                        color={sparkColor(delayColor)}
                                    />
                                    {srv.perf?.input_delay_p95_ms != null
                                        ? srv.perf.input_delay_p95_ms.toFixed(1) + 'ms'
                                        : '—'}
                                </td>
                                <td class="mono muted">{rel(srv.last_seen, now)}</td>
                                <td onclick={(e) => e.stopPropagation()}>
                                    <div class="btn-row">
                                        <button
                                            class="btn-hist"
                                            onclick={() => {
                                                appState.eventHostFilter = srv.host.split('.')[0];
                                                appState.currentView = 'events';
                                            }}>History</button
                                        >
                                        <NotificationExclusionAction host={srv.host} compact />
                                        <button
                                            class="btn-rm"
                                            onclick={() => requestRemoveServer(srv.host)}
                                            aria-label="Remove {srv.host}"
                                            disabled={removingHosts.has(srv.host)}
                                            >{removingHosts.has(srv.host) ? '…' : '✕'}</button
                                        >
                                    </div>
                                </td>
                            </tr>
                            {#if expandedHosts.has(srv.host)}
                                <tr class="detail-row">
                                    <td colspan="12">
                                        <ServerDetail
                                            server={srv}
                                            {now}
                                            {onhistoryclick}
                                            onremove={requestRemoveServer}
                                        />
                                    </td>
                                </tr>
                            {/if}
                        {/each}
                    {/each}
                </tbody>
            </table>
        </div>

        <div class="pager">
            {#if totalPages > 1}
                <button class="pager-btn btn-brutal" disabled={page === 0} onclick={() => page--}>← Prev</button>
                <span class="pager-info"
                    >{page + 1} / {totalPages} <span class="pager-total">({sorted.length} servers)</span></span
                >
                <button class="pager-btn btn-brutal" disabled={page >= totalPages - 1} onclick={() => page++}
                    >Next →</button
                >
            {/if}
            {#each PAGE_SIZES as sz}
                <button
                    class="btn-brutal pager-pill"
                    class:active={pageSize === sz}
                    onclick={() => {
                        pageSize = sz;
                        page = 0;
                    }}>{sz}</button
                >
            {/each}
        </div>
    {/if}
</div>

{#if confirmRemoveHost}
    <ConfirmDialog
        title="Remove Server"
        message="Remove {confirmRemoveHost} from the dashboard? This cannot be undone."
        confirmLabel="Remove"
        cancelLabel="Cancel"
        onconfirm={doRemoveServer}
        oncancel={() => (confirmRemoveHost = null)}
    />
{/if}

{#if confirmBatchRemoveHosts}
    {@const hostCount = confirmBatchRemoveHosts.length}
    {@const namesList = confirmBatchRemoveHosts.join(', ')}
    <ConfirmDialog
        title="Permanently Remove {hostCount} Server{hostCount === 1 ? '' : 's'}"
        message={`This will durable-tombstone ${hostCount} server${hostCount === 1 ? '' : 's'} and reject any future re-registration until restored: ${namesList}. This cannot be undone from the toolbar.`}
        confirmLabel={`Remove ${hostCount}`}
        cancelLabel="Cancel"
        onconfirm={doBatchRemove}
        oncancel={() => (confirmBatchRemoveHosts = null)}
    />
{/if}

<style>
    .grid {
        margin-bottom: 24px;
    }
    .grid > .card {
        overflow: hidden;
    }
    .filter-bar {
        display: flex;
        align-items: center;
        gap: 12px;
        margin-bottom: 12px;
        flex-wrap: wrap;
    }
    .filter-pills {
        display: flex;
        gap: 6px;
    }
    .filter-pill {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.68rem;
        font-weight: 700;
        padding: 4px 10px;
        border-radius: 20px;
        border: var(--spacing-bw) solid var(--color-border);
        background: var(--color-card);
        color: var(--color-muted);
        cursor: pointer;
        text-transform: uppercase;
        letter-spacing: 0.06em;
        transition: all 0.1s linear;
    }
    .filter-pill.active {
        background: var(--color-fg);
        color: var(--color-bg);
    }
    .filter-pill.ok.active {
        background: var(--color-green);
        color: #fff;
        border-color: var(--color-green);
    }
    .filter-pill.warning.active {
        background: var(--color-amber);
        color: #fff;
        border-color: var(--color-amber);
    }
    .filter-pill.grace.active {
        background: var(--color-amber);
        color: #fff;
        border-color: var(--color-amber);
    }
    .filter-pill.alert.active {
        background: var(--color-red);
        color: #fff;
        border-color: var(--color-red);
    }
    .filter-pill.off.active {
        background: var(--color-subtle);
        color: #fff;
        border-color: var(--color-subtle);
    }
    .grid-toast {
        padding: 8px 14px;
        margin-bottom: 10px;
        background: var(--color-card);
        color: var(--color-red);
        border: 1.5px solid var(--color-red);
        border-radius: var(--radius-default);
        font-size: 0.8rem;
        font-family: 'JetBrains Mono', monospace;
    }
    .pager {
        display: flex;
        align-items: center;
        justify-content: center;
        gap: 12px;
        padding: 14px 0 4px;
        position: relative;
        z-index: 2;
    }
    .pager-btn {
        font-size: 0.7rem;
        padding: 5px 14px;
        background: var(--color-card);
        color: var(--color-fg);
    }
    .pager-btn:disabled {
        opacity: 0.35;
        pointer-events: none;
    }
    .pager-info {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.7rem;
        font-weight: 700;
        color: var(--color-muted);
    }
    .pager-total {
        font-weight: 400;
        opacity: 0.6;
    }
    .pager-pill {
        font-size: 0.65rem;
        font-weight: 700;
        padding: 4px 10px;
        color: var(--color-muted);
    }
    .pager-pill.active {
        background: var(--color-accent);
        color: #fff;
        border-color: var(--color-accent);
    }
    .empty {
        text-align: center;
        padding: 50px 20px;
        color: var(--color-muted);
    }
    .empty h2 {
        font-size: 1.3rem;
        font-weight: 400;
        margin-bottom: 4px;
        color: var(--color-fg);
    }
    table.srv-tbl {
        width: 100%;
        border-collapse: separate;
        border-spacing: 0;
        font-size: 13px;
    }
    table.srv-tbl th {
        font-family: 'JetBrains Mono', monospace;
        font-size: 11px;
        font-weight: 500;
        text-transform: uppercase;
        letter-spacing: 0.5px;
        color: var(--color-muted);
        text-align: left;
        padding: 8px 8px;
        border-bottom: var(--spacing-bw) solid var(--color-border);
        white-space: nowrap;
        background: var(--color-card);
    }
    tr.pool-group-header th {
        padding: 7px 8px;
        background: var(--color-surface);
        border-bottom: 1px solid var(--color-border);
        color: var(--color-muted);
    }
    .pool-group-header strong {
        margin-left: 10px;
        color: var(--color-fg);
        font-weight: 700;
    }
    .pool-group-count {
        margin-left: 8px;
        font-weight: 400;
        opacity: 0.75;
    }
    table.srv-tbl td {
        padding: 9px 8px;
        border-bottom: 1px solid var(--color-border);
        vertical-align: middle;
        color: var(--color-fg);
    }
    th.sortable {
        cursor: pointer;
        user-select: none;
    }
    th.sortable:hover {
        color: var(--color-accent);
    }
    .clickable {
        cursor: pointer;
    }
    .clickable:hover td {
        background: var(--color-surface);
    }
    .clickable:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: -2px;
    }
    .clickable:focus-visible td {
        background: var(--color-surface);
    }
    .sel td {
        background: color-mix(in srgb, var(--color-accent) 8%, var(--color-card)) !important;
    }
    .dot {
        display: inline-block;
        width: 8px;
        height: 8px;
        border-radius: 50%;
        margin-right: 6px;
        vertical-align: middle;
    }
    .dot.ok {
        background: var(--color-green);
    }
    .dot.warning {
        background: var(--color-amber);
    }
    .dot.grace {
        background: var(--color-amber);
    }
    .dot.alert {
        background: var(--color-red);
    }
    .dot.off {
        background: var(--color-subtle);
    }
    .mono {
        font-family: 'JetBrains Mono', monospace;
    }
    /* Equal-width sparkline columns */
    .spark-col {
        width: 10%;
    }
    /* Cells that carry a sparkline background — SVG is position:absolute inside */
    .spark-cell {
        position: relative;
        overflow: hidden;
        border-left: 1px solid color-mix(in srgb, var(--color-border) 40%, transparent);
    }
    .muted {
        color: var(--color-muted);
    }
    .fw7 {
        font-weight: 700;
    }
    .btn-row {
        display: flex;
        gap: 8px;
    }
    .btn-hist {
        font-family: 'Work Sans', sans-serif;
        font-size: 0.7rem;
        font-weight: 700;
        padding: 4px 10px;
        background: var(--color-card);
        color: var(--color-accent);
        border: var(--spacing-bw) solid var(--color-accent);
        border-radius: var(--radius-default);
        box-shadow: 3px 3px 0 var(--color-shadow);
        cursor: pointer;
        transition:
            transform 0.1s,
            box-shadow 0.1s,
            background 0.1s,
            color 0.1s;
    }
    .btn-hist:hover {
        transform: translate(-1px, -1px);
        box-shadow: 4px 4px 0 var(--color-shadow);
        background: var(--color-accent);
        color: #fff;
    }
    .btn-hist:active {
        transform: translate(1px, 1px);
        box-shadow: 1px 1px 0 var(--color-shadow);
    }
    .btn-rm {
        font-family: 'Work Sans', sans-serif;
        font-size: 0.7rem;
        font-weight: 700;
        padding: 4px 10px;
        background: var(--color-card);
        color: var(--color-red);
        border: var(--spacing-bw) solid var(--color-red);
        border-radius: var(--radius-default);
        box-shadow: 3px 3px 0 var(--color-shadow);
        cursor: pointer;
        transition:
            transform 0.1s,
            box-shadow 0.1s,
            background 0.1s,
            color 0.1s;
    }
    .btn-rm:hover {
        transform: translate(-1px, -1px);
        box-shadow: 4px 4px 0 var(--color-shadow);
        background: var(--color-red);
        color: #fff;
    }
    .btn-rm:active {
        transform: translate(1px, 1px);
        box-shadow: 1px 1px 0 var(--color-shadow);
    }
    .detail-row td {
        padding: 0 !important;
        border-bottom: var(--spacing-bw) solid var(--color-surface) !important;
    }
    .pill {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.6rem;
        font-weight: 700;
        text-transform: uppercase;
        letter-spacing: 0.06em;
        padding: 2px 8px;
        border-radius: 4px;
        color: #fff;
    }
    .pill.ok {
        background: var(--color-green);
    }
    .pill.warning {
        background: var(--color-amber);
    }
    .pill.grace {
        background: var(--color-amber);
    }
    .pill.alert {
        background: var(--color-red);
    }
    .pill.off {
        background: var(--color-subtle);
    }
    .section-label {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.65rem;
        font-weight: 700;
        text-transform: uppercase;
        letter-spacing: 0.12em;
        color: var(--color-muted);
        margin-bottom: 10px;
    }
    .grace-cd {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.6rem;
        font-weight: 700;
        color: var(--color-amber);
        margin-left: 6px;
        white-space: nowrap;
    }
    .grace-cd--expired {
        color: var(--color-red);
    }

    /* ─── Batch operations (BatchOperations) ─────────────────────────────── */

    /* The header and data cells share one fixed box so select-all aligns with each row. */
    table.srv-tbl th.sel-col,
    table.srv-tbl td.sel-col {
        width: 32px;
        min-width: 32px;
        max-width: 32px;
        box-sizing: border-box;
        padding: 8px 4px !important;
        text-align: center;
        vertical-align: middle;
    }
    .srv-check {
        appearance: none;
        -webkit-appearance: none;
        display: block;
        width: 16px;
        height: 16px;
        cursor: pointer;
        margin: 0 auto;
        background: var(--color-card);
        border: var(--spacing-bw) solid var(--color-border);
        border-radius: 3px;
        position: relative;
        box-shadow: 1px 1px 0 var(--color-shadow);
        transition:
            background 0.1s,
            transform 0.05s;
    }
    .srv-check:hover:not(:disabled) {
        transform: translate(-1px, -1px);
        box-shadow: 2px 2px 0 var(--color-shadow);
    }
    .srv-check:active:not(:disabled) {
        transform: translate(1px, 1px);
        box-shadow: 0 0 0 var(--color-shadow);
    }
    .srv-check:checked {
        background: var(--color-accent);
        border-color: var(--color-accent);
    }
    .srv-check:checked::after {
        content: '';
        position: absolute;
        left: 4px;
        top: 1px;
        width: 4px;
        height: 8px;
        border: solid #fff;
        border-width: 0 2px 2px 0;
        transform: rotate(45deg);
    }
    .srv-check:indeterminate {
        background: var(--color-accent);
        border-color: var(--color-accent);
    }
    .srv-check:indeterminate::after {
        content: '';
        position: absolute;
        left: 3px;
        top: 6px;
        width: 8px;
        height: 2px;
        background: #fff;
    }
    .srv-check:disabled {
        opacity: 0.35;
        cursor: not-allowed;
    }
    .srv-check:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: 2px;
    }
    /* Row selection visual: subtle accent tint over the existing expanded-state pink */
    .row-sel td {
        background: color-mix(in srgb, var(--color-accent) 12%, var(--color-card)) !important;
    }
    .row-sel.clickable:hover td {
        background: color-mix(in srgb, var(--color-accent) 18%, var(--color-surface)) !important;
    }

    /* Batch action toolbar */
    .batch-toolbar {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 16px;
        padding: 12px 16px;
        margin-bottom: 12px;
        background: var(--color-card);
        border: var(--spacing-bw) solid var(--color-accent);
        border-radius: var(--radius-default);
        box-shadow: 3px 3px 0 var(--color-shadow);
        flex-wrap: wrap;
    }
    .batch-summary {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.78rem;
        color: var(--color-fg);
    }
    .batch-summary strong {
        font-weight: 700;
        color: var(--color-accent);
        font-size: 0.9rem;
        margin-right: 4px;
    }
    .batch-summary-sub {
        color: var(--color-muted);
        margin-left: 6px;
        font-size: 0.7rem;
    }
    .batch-actions {
        display: flex;
        gap: 8px;
        flex-wrap: wrap;
    }
    .batch-btn {
        font-size: 0.72rem;
        padding: 6px 14px;
        background: var(--color-card);
        color: var(--color-accent);
        border-color: var(--color-accent);
    }
    .batch-btn:hover:not(:disabled) {
        background: var(--color-accent);
        color: #fff;
    }
    .batch-btn:disabled {
        opacity: 0.35;
        pointer-events: none;
    }
    .batch-btn-danger {
        color: var(--color-red);
        border-color: var(--color-red);
    }
    .batch-btn-danger:hover:not(:disabled) {
        background: var(--color-red);
        color: #fff;
    }
    .batch-btn-batch-cancel {
        font-size: 0.72rem;
        padding: 6px 14px;
        background: transparent;
        color: var(--color-muted);
        border-color: var(--color-border);
    }
    .batch-btn-batch-cancel:hover {
        background: var(--color-surface);
        color: var(--color-fg);
    }
</style>
