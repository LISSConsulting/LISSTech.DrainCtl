<script>
  /**
   * CellSparkline — a tiny SVG area-fill sparkline rendered as a background
   * visual inside a table cell.
   *
   * Usage: place inside a <td style="position:relative"> (or a class that sets
   * position:relative). The SVG is absolutely positioned and pointer-events:none
   * so it never interferes with clicks on the row.
   *
   * @prop {number[]} data  - Raw metric values in chronological order (≥2 needed to render)
   * @prop {string}  color  - CSS color value that drives the fill (use var(--color-*))
   */
  let { data = [], color = 'var(--color-green)' } = $props();

  /**
   * Build an SVG area-fill path from the data array.
   *
   * viewBox is 0 0 100 100 with preserveAspectRatio="none" so the path
   * always fills the SVG element exactly regardless of its pixel size.
   *
   * The area is normalised so the min value maps to the bottom (y=100) and the
   * max value maps near the top (y=pad), with a small pad so the line isn't
   * clipped against the SVG edge. When all values are equal the line sits at
   * mid-height (y = 50) and the fill covers the lower half.
   */
  let path = $derived.by(() => {
    if (data.length < 2) return '';

    const min = Math.min(...data);
    const max = Math.max(...data);
    const range = max - min || 1; // guard against flat line
    const pad = 6; // vertical breathing room at top

    const pts = data.map((v, i) => {
      const x = (i / (data.length - 1)) * 100;
      // y=pad when v=max (top), y=(100-pad) when v=min (bottom)
      const y = pad + ((max - v) / range) * (100 - pad * 2);
      return [x, y];
    });

    // Line segments across the data
    const line = pts
      .map(([x, y], i) => `${i === 0 ? 'M' : 'L'}${x.toFixed(2)},${y.toFixed(2)}`)
      .join('');

    // Close the area along the bottom edge (x-axis)
    const [lastX] = pts[pts.length - 1];
    return `${line} L${lastX.toFixed(2)},100 L0,100 Z`;
  });
</script>

{#if data.length >= 2}
  <!-- aria-hidden: purely decorative, the text value beside it is the semantic content -->
  <svg
    aria-hidden="true"
    focusable="false"
    preserveAspectRatio="none"
    viewBox="0 0 100 100"
    class="cell-spark"
    style="color:{color}"
  >
    <path d={path} fill="currentColor" />
  </svg>
{/if}

<style>
  .cell-spark {
    position: absolute;
    left: 0;
    top: 15%;
    width: 100%;
    height: 70%;
    opacity: 0.18;
    pointer-events: none;
  }
</style>
