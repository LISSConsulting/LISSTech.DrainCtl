<script>
  import { getThresholdColor } from '../lib/thresholds.js';

  let {
    value,
    max = 100,
    label,
    unit = '%',
    warnThreshold,
    critThreshold,
    direction = 'higher-worse',
    size = 56,
  } = $props();

  let pct = $derived(max > 0 ? Math.min(Math.max(((value ?? 0) / max) * 100, 0), 100) : 0);
  let r   = $derived(size / 2 - 5);
  let c   = $derived(size / 2);
  let circ   = $derived(2 * Math.PI * r);
  let offset = $derived(circ * (1 - pct / 100));

  let color = $derived(
    value == null
      ? 'var(--color-subtle)'
      : warnThreshold != null && critThreshold != null
        ? (() => {
            const clr = getThresholdColor(value, warnThreshold, critThreshold, direction);
            return clr === 'green'  ? 'var(--color-green)'
                 : clr === 'amber'  ? 'var(--color-amber)'
                 : clr === 'red'    ? 'var(--color-red)'
                 : 'var(--color-subtle)';
          })()
        : pct >= 80 ? 'var(--color-red)'
        : pct >= 60 ? 'var(--color-amber)'
        : 'var(--color-green)'
  );

  let displayValue = $derived(
    unit === '%'
      ? `${Math.round(pct)}%`
      : value != null
        ? String(value)
        : '—'
  );
</script>

<div class="ring-cell">
  <div class="ring" style="width:{size}px;height:{size}px">
    <svg viewBox="0 0 {size} {size}" width={size} height={size} style="transform:rotate(-90deg)">
      <circle class="ring-bg" cx={c} cy={c} r={r} stroke-width="5" />
      <circle
        class="ring-fill"
        cx={c} cy={c} r={r}
        stroke={color}
        stroke-width="5"
        stroke-dasharray={circ.toFixed(1)}
        stroke-dashoffset={offset.toFixed(1)}
      />
    </svg>
    <div class="ring-val">
      <span class="ring-pct">{displayValue}</span>
      {#if unit && unit !== '%'}<span class="ring-unit">{unit}</span>{/if}
    </div>
  </div>
  {#if label}<div class="ring-lbl">{label}</div>{/if}
</div>

<style>
  .ring-cell {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 2px;
  }

  .ring {
    position: relative;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }

  .ring-bg {
    fill: none;
    stroke: var(--color-surface);
  }

  .ring-fill {
    fill: none;
    stroke-linecap: round;
    transition: stroke-dashoffset 0.5s, stroke 0.3s;
  }

  .ring-val {
    position: absolute;
    inset: 0;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    font-family: 'JetBrains Mono', monospace;
    font-weight: 700;
    line-height: 1.1;
  }

  .ring-pct {
    font-size: 13px;
  }

  .ring-unit {
    font-size: 9px;
    font-weight: 500;
    color: var(--color-muted);
  }

  .ring-lbl {
    font-size: 10px;
    font-weight: 600;
    color: var(--color-muted);
    text-transform: uppercase;
    letter-spacing: 0.3px;
  }
</style>
