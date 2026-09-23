/**
 * Chart theme tokens — pure data shared across the chart primitives.
 *
 * Extracted from HostLoadChart.svelte and MetricsChart.svelte in commit 4
 * of the chart-library-migration PR. Color values reference CSS custom
 * properties on `:root` (frontend/src/app.css) so light/dark themes flow
 * through automatically; the threshold opacities are pure numbers because
 * they're applied as fill-opacity directly.
 *
 * IMPORTANT — KEEP PURE. No Svelte imports, no I/O.
 */

/**
 * Opacity applied to the warn-threshold band overlay. 0.20 keeps the band
 * legible without obscuring the line plot.
 */
export const THRESHOLD_WARN_OPACITY = 0.20;

/**
 * Opacity applied to the crit-threshold band overlay. Higher than WARN so
 * the eye reads it as "more serious" without crossing into "obscures data".
 */
export const THRESHOLD_CRIT_OPACITY = 0.30;

/**
 * Stroke width for the chart's axis lines (in px). 2 px reads cleanly on
 * hi-DPI displays without becoming heavy at small chart sizes.
 */
export const AXIS_STROKE_WIDTH_PX = 2;

/**
 * Stroke width for the chart's primary line/area plot (in px).
 */
export const LINE_STROKE_WIDTH_PX = 2;

/**
 * Crosshair line stroke width (in px). Thin enough not to dominate the
 * chart, thick enough to track the cursor cleanly.
 */
export const CROSSHAIR_STROKE_WIDTH_PX = 1.5;

/**
 * Crosshair dot radius at the active sample (in px).
 */
export const CROSSHAIR_DOT_RADIUS_PX = 3;

/**
 * Tooltip background color with alpha. Pairs with the theme's foreground
 * text color.
 */
export const TOOLTIP_BG = 'rgba(0, 0, 0, 0.85)';

/**
 * Tooltip padding in px.
 */
export const TOOLTIP_PADDING_PX = 4;
