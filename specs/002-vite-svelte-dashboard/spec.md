# Feature Specification: Vite + Svelte Dashboard Migration

**Feature Branch**: `002-vite-svelte-dashboard`
**Created**: 2026-04-09
**Status**: Draft
**Input**: User description: "Migrate dashboard to Vite + Svelte + Tailwind. Retain full parity with existing dashboard, while improving existing functionality, code organization, quality, and simplicity. This should include several fixes in current implementation."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Core Dashboard Monitoring (Priority: P1)

An IT administrator opens the DrainCtl dashboard to monitor the health and drain status of their Remote Desktop Services farm. They see the same counters, state bar, performance metrics chart, event log, and server table with expandable accordion detail rows they relied on before the migration. All real-time data updates, server registration, status reporting, and API interactions continue to function identically.

**Why this priority**: The dashboard is a mission-critical monitoring tool. Any regression in core monitoring functionality would break the primary value proposition.

**Independent Test**: Navigate to the dashboard URL, verify all five counter cards render with live data, the state bar reflects server proportions, the performance metrics chart displays CPU/Memory/Input Delay/Sessions time-series data, the event log streams entries, and the server table shows all registered servers with expandable detail rows containing utilization rings, sparklines, and metadata.

**Acceptance Scenarios**:

1. **Given** the dashboard is served by the Go backend, **When** an administrator navigates to the root URL, **Then** the full dashboard renders with all existing sections (counters, state bar, performance metrics chart, event log, server table) populated with live data.
2. **Given** servers are reporting status, **When** the dashboard receives updates, **Then** counters, state bar segments, performance chart data points (CPU%, Memory%, Input Delay, Sessions), and server rows update in real time without page reload.
3. **Given** a server row in the table, **When** the administrator clicks to expand it, **Then** the accordion detail row opens showing three tiles: resource utilization (CPU/Memory/Sessions rings + sparklines), I/O metrics (Disk Queue/Input Delay/TCP Retransmits rings), and server details (registration date, last seen, version).

---

### User Story 2 - I/O and Network Metric Ring Gauges with Status Colors (Priority: P1)

An administrator expands a server's detail row to inspect I/O and network performance. The Disk Queue, Input Delay, and TCP Retransmits ring gauges display color-coded meters (green/amber/red) based on severity thresholds, matching the behavior already present for CPU, Memory, and Session utilization rings.

**Why this priority**: This is a bug fix. The I/O and network metric rings currently do not show green/amber/red status coloring, making it impossible to quickly assess severity at a glance. This undermines the visual monitoring value of the accordion detail tiles.

**Independent Test**: Expand any server detail row, observe the I/O tile rings. Verify Disk Queue, Input Delay, and TCP Retransmits rings render with green fill when values are low, amber when moderate, and red when critical, using the same color thresholds logic as CPU/Memory/Sessions rings.

**Acceptance Scenarios**:

1. **Given** a server with low I/O metrics, **When** the detail row is expanded, **Then** all three I/O ring gauges display with green fill color.
2. **Given** a server with moderate I/O metrics (approaching warn thresholds), **When** the detail row is expanded, **Then** the affected ring gauges display with amber fill color.
3. **Given** a server with critical I/O metrics (exceeding critical thresholds), **When** the detail row is expanded, **Then** the affected ring gauges display with red fill color.
4. **Given** the configurable performance monitoring thresholds, **When** thresholds are changed in dashboard configuration, **Then** the ring color breakpoints reflect the updated thresholds.

---

### User Story 3 - Dashboard Configuration Modal UX Improvements (Priority: P2)

An administrator opens the Dashboard Configuration modal to adjust settings. The button layout is intuitive: "Send Test" is positioned on the far left, while "Save" and "Close" are grouped together on the far right. All buttons share a consistent hover state matching the landing page Download MSI button style (translate + shadow shift). When settings are saved, a success message appears and the modal background briefly changes to a positive color. When settings are modified but unsaved (dirty form), the modal background subtly changes to indicate unsaved changes.

**Why this priority**: These are usability improvements that reduce confusion and provide clear feedback about form state. Inconsistent button hover states and unclear save status create friction in routine configuration tasks.

**Independent Test**: Open Dashboard Configuration, modify a setting, observe dirty indicator. Click Save, observe success message and positive color feedback. Verify Send Test is on the left, Save and Close are grouped on the right. Hover over all buttons and verify consistent translate/shadow animation.

**Acceptance Scenarios**:

1. **Given** the Dashboard Configuration modal is open, **When** the administrator views the button bar, **Then** "Send Test" is aligned to the far left and "Save" and "Close" are grouped together on the far right.
2. **Given** the modal is open with no changes, **When** the administrator modifies any setting (grace period, threshold, notification target), **Then** the modal background subtly changes color to indicate unsaved changes (dirty state).
3. **Given** unsaved changes exist, **When** the administrator clicks "Save", **Then** a "Settings saved successfully" message appears and the modal background briefly changes to a positive (green-tinted) color.
4. **Given** the saved state, **When** the administrator makes no further changes, **Then** the dirty indicator clears and the background returns to the default color.
5. **Given** any button in the configuration modal, **When** the administrator hovers over it, **Then** the button exhibits the same hover animation (translate -2px/-2px, increased shadow) and active animation (translate +2px/+2px, reduced shadow) as the landing page Download MSI button.

---

### User Story 4 - Notification Target Modal Scrollbar Consistency (Priority: P2)

An administrator opens the Add/Edit Notification Target modal (which appears above the Dashboard Configuration modal). If the content overflows, the scrollbar is styled identically to the Dashboard Configuration modal's scrollbar: thin, subtle-colored thumb, accent color on hover, transparent track.

**Why this priority**: Visual inconsistency between stacked modals breaks the polished feel of the dashboard. The notification target modal currently uses unstyled browser-default scrollbars.

**Independent Test**: Open Dashboard Configuration, then open the Add Notification Target modal. Resize the browser window vertically to force scrollbar appearance on both modals. Verify both scrollbars use the same thin, styled appearance.

**Acceptance Scenarios**:

1. **Given** the Add/Edit Notification Target modal content exceeds the viewport, **When** a scrollbar appears, **Then** it uses thin scrollbar styling with a subtle-colored thumb and transparent track, identical to the Dashboard Configuration modal.
2. **Given** the scrollbar is visible, **When** the administrator hovers over the scrollbar thumb, **Then** the thumb color changes to the accent color, matching the Dashboard Configuration modal's scrollbar hover behavior.

---

### User Story 5 - Migrated Build and Embedding Workflow (Priority: P1)

The development team builds the dashboard using a modern toolchain. The build output is a set of static assets that the Go backend embeds and serves, preserving the existing single-binary deployment model. The migration does not change any API contracts, authentication, or Content Security Policy behavior.

**Why this priority**: The build and deployment pipeline must work correctly for any of the other stories to be deliverable. Breaking the embed workflow would prevent shipping the dashboard entirely.

**Independent Test**: Run the build command, verify output assets are generated. Build the Go binary, verify the dashboard assets are embedded. Start the server, navigate to the dashboard, verify it loads and functions with all API endpoints responding correctly.

**Acceptance Scenarios**:

1. **Given** the new dashboard source code, **When** the build command is executed, **Then** optimized static assets are produced (bundled, minified).
2. **Given** the build output, **When** the Go binary is compiled, **Then** the dashboard assets are embedded into the single binary.
3. **Given** the compiled binary, **When** the dashboard server starts and a browser navigates to the root URL, **Then** the full dashboard loads and all API endpoints (health, register, report, servers, notify-config, notify-test) respond as before.
4. **Given** the migrated dashboard, **When** inspecting HTTP response headers, **Then** Content Security Policy, X-Frame-Options, HSTS, and Cache-Control headers remain functionally equivalent.

---

### User Story 6 - Theme Support and Visual Fidelity (Priority: P2)

The migrated dashboard preserves the existing light and dark theme support with the same color palette, typography (DM Serif Display, Fraunces, Work Sans, JetBrains Mono), and visual design language (offset drop shadows, pill badges, monospace metrics, mauve/rose accent).

**Why this priority**: Preserving the visual identity ensures administrators experience a seamless transition and recognizable brand continuity.

**Independent Test**: Toggle between light and dark themes. Verify all color variables, fonts, shadow styles, badge styles, and component appearances match the existing dashboard.

**Acceptance Scenarios**:

1. **Given** the migrated dashboard in light mode, **When** compared to the existing dashboard, **Then** color palette, typography, spacing, shadows, and component styles are visually identical.
2. **Given** light mode, **When** the administrator clicks the theme toggle, **Then** the dashboard switches to dark mode with the same inverted color scheme as the existing implementation.

---

### Edge Cases

- What happens when the dashboard configuration modal has many notification targets causing overflow? The scrollbar should be styled consistently.
- How does the dirty form indicator behave when the administrator saves, then makes another change? It should re-enter the dirty state.
- What happens when the save operation fails (network error, validation error)? The modal displays an inline error banner with red-tinted background flash and descriptive error message text, mirroring the success feedback pattern. The dirty-state indicator persists.
- How do I/O ring gauges render when metric data is missing or zero? They should show an empty ring (no fill) in a neutral color.
- What happens when performance monitoring is disabled? The I/O ring thresholds should use sensible defaults since custom thresholds are unavailable.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: System MUST render all existing dashboard sections (counters, state bar, history chart, event log, server table with accordion details) with full feature parity.
- **FR-002**: System MUST display I/O and network metric ring gauges (Disk Queue, Input Delay, TCP Retransmits) with green/amber/red color coding based on severity thresholds, consistent with CPU/Memory/Sessions rings.
- **FR-003**: System MUST position the "Send Test" button on the far left of the Dashboard Configuration modal button bar, with "Save" and "Close" buttons grouped on the far right.
- **FR-004**: System MUST apply a consistent hover animation to all interactive buttons across the dashboard and configuration modals, matching the landing page Download MSI button style (translate shift + shadow adjustment on hover, reverse on active).
- **FR-005**: System MUST display a visual dirty-state indicator (subtle background color change) on the Dashboard Configuration modal when any setting has been modified but not saved.
- **FR-006**: System MUST display a "Settings saved successfully" message and a positive-color background flash on the Dashboard Configuration modal when settings are saved.
- **FR-007**: System MUST style scrollbars in the Add/Edit Notification Target modal identically to the Dashboard Configuration modal (thin, subtle thumb, accent on hover, transparent track).
- **FR-008**: System MUST produce build artifacts that are embeddable into the Go binary, preserving the single-binary deployment model.
- **FR-009**: System MUST preserve all existing API contracts, authentication, and security headers (CSP, X-Frame-Options, HSTS, Cache-Control).
- **FR-010**: System MUST support light and dark themes with the existing color palette and typography.
- **FR-011**: System MUST render a performance metrics chart using Layercake (Svelte-native, SVG-based) displaying four time-series: CPU%, Memory%, Input Delay (ms), and Session count, with toggleable series visibility and neobrutalist styling (bold strokes, offset shadows).
- **FR-012**: System MUST render per-server sparkline mini-charts in accordion detail rows using Layercake.
- **FR-013**: System MUST preserve real-time data update behavior (SSE/polling) without page reload.
- **FR-014**: System MUST preserve all modal dialogs (Dashboard Configuration, History, Target Edit, Target Delete Confirmation) with their existing functionality and z-index stacking order.
- **FR-015**: System MUST display an inline error banner within the Dashboard Configuration modal when a save operation fails, using a red-tinted background flash and error message text, mirroring the success feedback pattern. The dirty-state indicator MUST persist after a failed save.

### Key Entities

- **Server Card**: Represents a monitored RDS server with status, metrics, and expandable detail view.
- **Ring Gauge**: SVG-based circular progress indicator with threshold-driven color (green/amber/red).
- **Notification Target**: A webhook or ntfy endpoint with type, destination, triggers, and repeat interval.
- **Dashboard Configuration**: Grace period, session threshold, performance monitoring toggles and thresholds, notification targets.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: All existing dashboard features are present and functional after migration, with zero regressions verified against current feature set.
- **SC-002**: I/O and network metric ring gauges display correct green/amber/red coloring for 100% of threshold ranges (low, moderate, critical).
- **SC-003**: Administrators can identify unsaved configuration changes within 1 second of making a modification, via visible dirty-state feedback.
- **SC-004**: Configuration save confirmation (success message + visual feedback) appears within 1 second of a successful save operation.
- **SC-005**: All interactive buttons across the dashboard exhibit identical hover/active animation behavior, with zero visual inconsistencies.
- **SC-006**: Scrollbar appearance is visually identical across all modal dialogs that support scrolling.
- **SC-007**: The built dashboard loads in under 2 seconds on a local network, comparable to or faster than the current monolithic HTML implementation.
- **SC-008**: The Go binary embeds the dashboard successfully, and the deployment remains a single binary with no external asset files.

## Clarifications

### Session 2026-04-09

- Q: Which Svelte version should the migration target? → A: Svelte 5 (runes, snippets, current stable)
- Q: Which Tailwind CSS version should the migration use? → A: Tailwind CSS v4 (CSS-based config, Oxide engine)
- Q: What default I/O ring gauge thresholds when perf monitoring disabled? → A: Reuse existing PerformanceConfig thresholds (Input Delay: warn 50ms, crit 100ms); hardcode defaults for Disk Queue (warn: 2, crit: 5) and TCP Retransmits (warn: 5%, crit: 10%)
- Q: How should a failed save operation in the Dashboard Configuration modal be communicated? → A: Inline error banner within modal (red-tinted background flash + error message text, mirroring the success pattern)
- Q: Should uPlot be replaced with a more Svelte-native charting library? → A: Yes, replace uPlot with Layercake (8KB, Svelte-native, SVG-based, full CSS control for neobrutalist styling)
- Q: What should the server state history chart display? → A: Replace drain-mode stacked area chart with performance metrics time-series: CPU%, Memory%, Input Delay (ms), and Session count

## Assumptions

- The migration targets **Svelte 5** (runes, snippets) as the component framework. All components will use Svelte 5's explicit reactivity model (`$state`, `$derived`, `$effect`).
- The migration uses **Tailwind CSS v4** with CSS-based configuration (Oxide engine). The existing CSS custom property system will be mapped to Tailwind's `@theme` directive.
- The existing Go backend API (routes, authentication, data store) remains unchanged; only the frontend assets are migrated.
- **Layercake** replaces uPlot as the charting library. Layercake is Svelte-native (~8KB), SVG-based, and provides full CSS control for neobrutalist styling. All charts (performance metrics and sparklines) use Layercake.
- The landing page (docs/index.html) is a separate static site and is NOT part of this migration; only the internal dashboard (internal/dashboard/dashboard.html) is being migrated.
- The existing CSS custom property system (color palette, typography, spacing) will be translated to the new styling approach while preserving identical visual output.
- The build output will replace the current single monolithic HTML file with a build artifact directory that the Go embed directive references.
- Performance monitoring thresholds (CPU warn/crit, Memory warn/crit, Input Delay warn/crit) from `PerformanceConfig` will be reused as the threshold breakpoints for I/O ring gauge coloring when available. When performance monitoring is disabled, the dashboard will use the existing config defaults (Input Delay: warn 50ms, crit 100ms) and hardcoded defaults for metrics without configurable thresholds (Disk Queue: warn 2, crit 5; TCP Retransmits: warn 5%, crit 10%).
