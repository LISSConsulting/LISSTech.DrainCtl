<script>
  import { appState } from '../lib/state.svelte.js';
  import { toggleTheme, theme } from '../lib/theme.svelte.js';

  /** @type {{ onconfigopen: () => void }} */
  let { onconfigopen } = $props();

  const counters = $derived(appState.counters);
  const isDark = $derived(theme.current === 'dark');
</script>

<nav class="nav">
  <div class="nav-in">
    <div class="nav-brand">
      <span class="mark">DC</span>
      DrainCtl
    </div>
    <div class="nav-right">
      <span class="mono">
        <span class="g">{counters.ok}</span> ok ·
        <span class="w">{counters.grace}</span> grace ·
        <span class="a">{counters.alert}</span> alert ·
        <span class="o">{counters.off}</span> off
      </span>
      <button class="btn-gear btn-brutal" onclick={onconfigopen}>CONFIG</button>
      <button class="btn-theme btn-brutal" onclick={toggleTheme} aria-label={isDark ? 'Switch to light mode' : 'Switch to dark mode'}>{isDark ? '☀' : '☽'}</button>
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
  }

  .mark {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    background: var(--color-accent);
    color: #fff;
    font-weight: 900;
    font-size: 0.85rem;
    border: var(--spacing-bw) solid var(--color-border);
    border-radius: 6px;
    box-shadow: 2px 2px 0 var(--color-shadow);
  }

  .nav-right {
    display: flex;
    align-items: center;
    gap: 16px;
    font-size: 0.8rem;
    font-family: 'JetBrains Mono', monospace;
  }

  .g { color: var(--color-green); }
  .a { color: var(--color-red); }
  .w { color: var(--color-amber); }
  .o { color: var(--color-subtle); }

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

  .btn-theme {
    width: 40px;
    padding: 6px;
    font-size: 1.05rem;
    line-height: 1;
    background: var(--color-card);
  }
</style>
