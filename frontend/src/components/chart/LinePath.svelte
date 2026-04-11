<script>
  import { getContext } from 'svelte';

  let { color = 'var(--color-green)', strokeWidth = 1.5, filled = false } = $props();

  const { data, xGet, yGet, width, height } = getContext('LayerCake');
</script>

{#if $data && $data.length >= 2}
  {@const d      = $data}
  {@const pts    = d.map(p => ({ x: $xGet(p), y: $yGet(p) }))}
  {@const first  = pts[0]}
  {@const last   = pts[pts.length - 1]}
  {@const lineParts = pts.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x.toFixed(2)},${p.y.toFixed(2)}`).join(' ')}
  {@const fillPath  = `${lineParts} L${last.x.toFixed(2)},${$height.toFixed(2)} L${first.x.toFixed(2)},${$height.toFixed(2)} Z`}

  {#if filled}
    <path d={fillPath} fill="{color}33" stroke="none" />
  {/if}
  <path
    d={lineParts}
    stroke={color}
    stroke-width={strokeWidth}
    fill="none"
    stroke-linejoin="round"
    stroke-linecap="round"
  />
{/if}
