<script>
    import { appState } from '../lib/state.svelte.js';
</script>

<div class="sbar">
    {#each appState.stateBarSegments as seg}
        {#if seg.pct > 0}
            <div
                class="sseg {seg.state}"
                style="flex: {seg.pct}"
                title="{seg.count} {seg.state} ({seg.pct.toFixed(1)}%)"
            >
                {#if seg.pct > 8}{Math.round(seg.pct)}%{/if}
            </div>
        {/if}
    {/each}
</div>

<style>
    .sbar {
        display: flex;
        height: 28px;
        border: var(--spacing-bw) solid var(--color-border);
        border-radius: var(--radius-default);
        overflow: hidden;
        box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow);
        margin-bottom: 24px;
    }

    .sseg {
        display: flex;
        align-items: center;
        justify-content: center;
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.7rem;
        font-weight: 700;
        color: #fff;
        transition: flex 0.15s linear;
        min-width: 0;
    }

    .sseg.ok {
        background: var(--color-green);
    }
    .sseg.grace {
        background: var(--color-amber);
    }
    .sseg.alert {
        background: var(--color-red);
    }
    .sseg.off {
        background: var(--color-subtle);
    }
</style>
