<script>
  import { LayerCake, Svg } from 'layercake';
  import LinePath from './chart/LinePath.svelte';

  let { data = [], color = 'var(--color-green)', height = 40 } = $props();

  let points = $derived(data.map((v, i) => ({ x: i, y: v ?? 0 })));
</script>

<div class="spark" style="height:{height}px">
  {#if points.length >= 2}
    <LayerCake
      data={points}
      x="x"
      y="y"
      yDomain={[0, null]}
      padding={{ top: 2, right: 2, bottom: 2, left: 2 }}
    >
      <Svg>
        <LinePath {color} />
      </Svg>
    </LayerCake>
  {/if}
</div>

<style>
  .spark {
    width: 100%;
    background: var(--color-surface);
    border-radius: 3px;
  }
</style>
