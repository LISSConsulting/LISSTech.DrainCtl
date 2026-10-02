<script>
    import { Download } from '@lucide/svelte';
    import { exportAriaLabel, shouldDisableExport } from '../lib/export-graph-config.js';

    /** @type {{label:string}} */
    let { exportConfig, state: exportState, onExport = async () => {} } = $props();
    let open = $state(false);
    let disabled = $derived(shouldDisableExport(exportState));
    let reason = $derived(
        exportState.loading ? 'Chart is loading' : exportState.failed ? 'Chart failed to load' : exportState.isStale ? 'Chart data is stale' : exportState.empty ? 'No retained history for this window' : !exportState.hasEnabledSeries ? 'No series are enabled' : '',
    );
    let graphLabel = $derived(exportConfig.label);
    let ariaLabel = $derived(exportAriaLabel(exportConfig));

    async function select(format) {
        open = false;
        await onExport(format);
    }
</script>

<div class="export-control">
    <button
        class="export-trigger"
        disabled={disabled}
        title={reason || `Export ${graphLabel}`}
        aria-label={ariaLabel}
        aria-haspopup="menu"
        aria-expanded={open}
        onclick={() => (open = !open)}
    >
        <Download size={13} strokeWidth={2.4} /> Export
    </button>
    {#if open}
        <div class="export-menu" role="menu" aria-label={`Export ${graphLabel} format`}>
            <button role="menuitem" onclick={() => select('csv')}>CSV</button>
            <button role="menuitem" onclick={() => select('xlsx')}>Excel</button>
        </div>
    {/if}
</div>

<style>
    .export-control { position: relative; display: inline-flex; }
    .export-trigger, .export-menu button { font: inherit; font-size: .62rem; font-weight: 700; letter-spacing: .06em; text-transform: uppercase; border: 1px solid var(--color-border); background: var(--color-card); color: var(--color-muted); cursor: pointer; }
    .export-trigger { display: inline-flex; align-items: center; gap: 4px; padding: 3px 6px; border-radius: 3px; }
    .export-trigger:hover:not(:disabled), .export-trigger:focus-visible { color: var(--color-accent); border-color: var(--color-accent); }
    .export-trigger:disabled { cursor: not-allowed; opacity: .45; }
    .export-menu { position: absolute; z-index: 5; right: 0; top: calc(100% + 3px); display: flex; box-shadow: 2px 2px 0 var(--color-shadow); }
    .export-menu button { padding: 4px 7px; }
    .export-menu button:hover, .export-menu button:focus-visible { color: var(--color-accent); }
</style>
