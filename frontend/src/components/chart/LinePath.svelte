<script>
    import { getContext } from 'svelte';

    let { color = 'var(--color-green)', strokeWidth = 1.5, filled = false } = $props();

    const { data, xGet, yGet, width, height } = getContext('LayerCake');
</script>

{#if $data && $data.length >= 2}
    {@const d = $data}
    {@const pts = d.map((p) => ({ x: $xGet(p), y: $yGet(p) }))}
    {@const first = pts[0]}
    {@const last = pts[pts.length - 1]}
    {@const lineParts = pts.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x.toFixed(2)},${p.y.toFixed(2)}`).join(' ')}
    {@const fillPath = `${lineParts} L${last.x.toFixed(2)},${$height.toFixed(2)} L${first.x.toFixed(2)},${$height.toFixed(2)} Z`}

    {#if filled}
        <!-- fill-opacity keeps alpha orthogonal to color so CSS variables
             (`var(--color-accent)`) work alongside hex literals. The previous
             `fill="{color}33"` idiom concatenated the "33" alpha directly onto
             the color string, which produced a valid 8-digit hex for literals
             but an invalid `var(--color-accent)33` for CSS-var colors —
             browsers fell back to black and the filled area below the line
             rendered as a solid black rectangle. -->
        <path d={fillPath} fill={color} fill-opacity="0.2" stroke="none" />
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
