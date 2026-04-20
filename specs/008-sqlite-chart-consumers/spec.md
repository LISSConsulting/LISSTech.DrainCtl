# Feature Specification: Remaining Persistent Telemetry Consumers

**Feature Branch**: `008-sqlite-chart-consumers`
**Created**: 2026-04-20
**Status**: Draft
**Input**: User description: "Migrate all remaining MemStore and AuditStore consumers to SQLite store: 1. All Overview page charts: LOAD, HIC, SESSIONS, REMOTE FX - Charts retain current design and functionality - Charts gain zoom/pan/scroll and pre-defined pills for 5M, 1H, 1D, 3D, 5D zoom levels 2. Any other consumers and components which will benefit from SQLite persistence store"

## Clarifications

### Session 2026-04-20

- Q: Which fleet aggregation semantics should the Overview charts preserve when they move to retained telemetry? → A: Preserve current fleet aggregation semantics exactly: LOAD uses fleet averages plus CPU P95; HIC, SESSIONS, and REMOTE FX keep the same fleet rollups the dashboard synthesizes today.
- Q: Which surfaces count as "other consumers and components" for this feature? → A: Limit them to dashboard surfaces that currently show historical trend or state data and can be backed by existing retained telemetry.
- Q: Should the Overview chart families share one time-window selection or keep separate windows? → A: One shared time window controls all Overview chart families together.
- Q: What should happen when one chart family has no retained data in the shared selected window? → A: Keep the chart visible with an explicit empty or unavailable state inside the shared selected window.
- Q: What should the dashboard do when a retained-telemetry history query fails? → A: Show an explicit query error state and keep retained telemetry authoritative.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Persistent Overview Charts (Priority: P1)

An RDSH operator opens the Overview page and can inspect historical LOAD, Health
Indicators, SESSIONS, and REMOTE FX trends from the durable telemetry history rather
than from browser-local memory.

**Why this priority**: The Overview page is the primary fleet-wide operational surface.
If it still depends on browser-local history, operators lose the benefit of durable
telemetry as soon as they switch browsers, switch machines, or reload after downtime.

**Independent Test**: Populate historical telemetry, open the Overview page on a fresh
browser profile, and verify all four chart families render meaningful retained history
without requiring any pre-existing local browser state.

**Acceptance Scenarios**:

1. **Given** retained telemetry exists for the farm, **When** an operator opens the
   Overview page from a new browser session, **Then** the LOAD, Health Indicators,
   SESSIONS, and REMOTE FX charts render historical data from the retained window.
2. **Given** the DrainCtl service has restarted since the historical samples were
   recorded, **When** the Overview page loads, **Then** the charts still show the
   pre-restart history alongside newer samples.
3. **Given** no retained data exists yet for a chart family, **When** the Overview
   page loads, **Then** that chart renders an empty but valid state that makes it clear
   data is still being collected rather than showing stale browser-local history.

---

### User Story 2 - Historical Navigation Without Visual Regression (Priority: P1)

An operator keeps the current Overview chart design and interactions they already know,
but can now zoom, pan, scroll, and jump directly to 5-minute, 1-hour, 1-day, 3-day,
and 5-day windows while investigating a fleet incident.

**Why this priority**: Durable history is only useful if operators can navigate it
quickly during diagnosis. The request explicitly requires the current visual design and
functionality to stay intact while adding richer time navigation.

**Independent Test**: Open each Overview chart family, use the 5M/1H/1D/3D/5D pills,
pan and scroll across adjacent windows, and confirm the chart preserves its current
layout, legend behavior, and interpretability while updating to the requested time
window.

**Acceptance Scenarios**:

1. **Given** an Overview chart is displayed, **When** the operator selects 5M, 1H,
   1D, 3D, or 5D, **Then** the chart updates to that visible window without changing
   its established visual style or removing existing information.
2. **Given** an operator is viewing an Overview chart, **When** they zoom, pan, or
   scroll, **Then** the chart updates the time window smoothly and keeps the same chart
   family semantics, colors, and legend behavior already present today.
3. **Given** the requested window crosses telemetry resolution boundaries, **When** the
   chart updates, **Then** it still presents a continuous and readable trend rather than
   dropping to an empty state solely because the raw window is no longer available.

---

### User Story 3 - Remove Remaining Ephemeral History Dependencies (Priority: P2)

An operator uses other dashboard components that currently benefit from history, and
those components now draw from retained telemetry wherever a durable source already
exists or can be derived from it, instead of depending on short-lived browser-local or
process-local buffers.

**Why this priority**: The Overview page is the largest gap, but leaving other history
consumers on ephemeral state would preserve inconsistent operator behavior and keep the
same class of support issue alive in smaller surfaces.

**Independent Test**: Identify each remaining historical consumer in the dashboard,
clear browser-local state, reload from a fresh session, and verify every in-scope
consumer still presents the same logical information from durable retained telemetry.

**Acceptance Scenarios**:

1. **Given** a dashboard surface currently shows retained historical information,
   **When** durable telemetry already contains or can derive that history, **Then** the
   surface reads from the durable source instead of browser-local history.
2. **Given** a dashboard surface does not have a durable historical source or would
   require a new telemetry class to support it, **When** the feature is delivered,
   **Then** that surface is left unchanged and is documented as out of scope for this
   feature.
3. **Given** the operator reloads the dashboard from another workstation or browser,
   **When** they revisit the in-scope historical surfaces, **Then** the information is
   still available without having to warm a local ring buffer first.

---

### Edge Cases

- What happens when one Overview chart family has retained data but another does not?
  Each chart must render independently so one empty chart does not blank the whole page;
  the chart with no retained data stays visible and shows an explicit empty or
  unavailable state inside the shared selected window.
- How does the system handle a requested time window that partially predates retention?
  The chart should show the available retained portion and make the missing older window
  obvious instead of fabricating continuity.
- What happens when REMOTE FX counters are unavailable for part or all of the selected
  window? The chart must remain structurally valid and communicate unavailable data
  without breaking the rest of the page.
- How does the system handle an operator rapidly switching time pills or dragging across
  the chart? The final requested window should win, and the chart should not settle on a
  stale earlier selection.
- What happens when a browser still has legacy local history cached from an older build?
  Retained telemetry must become authoritative so stale local history is not shown as if
  it were current truth.
- What happens when a retained-telemetry history query fails for the selected window?
  The affected chart stays visible and shows an explicit query error state; the dashboard
  must not fall back to browser-local history as if it were authoritative retained data.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST source the Overview page's LOAD chart history from the
  retained telemetry store rather than from browser-local historical buffers.
- **FR-002**: The system MUST source the Overview page's Health Indicators chart history
  from the retained telemetry store rather than from browser-local historical buffers.
- **FR-003**: The system MUST source the Overview page's SESSIONS chart history from the
  retained telemetry store rather than from browser-local historical buffers.
- **FR-004**: The system MUST source the Overview page's REMOTE FX chart history from
  the retained telemetry store whenever retained data exists for the selected window.
- **FR-005**: LOAD, Health Indicators, SESSIONS, and REMOTE FX charts MUST retain their
  current design language, chart meaning, legend behavior, and existing operator-facing
  functionality after the history source changes.
- **FR-005a**: The retained-telemetry migration MUST preserve the Overview page's current
  fleet aggregation semantics exactly: LOAD continues to use fleet averages plus CPU
  P95, and Health Indicators, SESSIONS, and REMOTE FX continue to use the same fleet
  rollups the dashboard synthesizes today.
- **FR-006**: Each Overview chart family MUST support operator-controlled zoom, pan, and
  scroll over the retained history window.
- **FR-007**: Each Overview chart family MUST provide direct time-window controls for
  5M, 1H, 1D, 3D, and 5D views.
- **FR-008**: The selected time-window controls MUST produce the same logical retained
  window across all Overview chart families so operators can compare correlated fleet
  signals over the same time span.
- **FR-008a**: The Overview page's time-window selection is shared across LOAD, Health
  Indicators, SESSIONS, and REMOTE FX so all chart families update to the same selected
  window together.
- **FR-009**: When a requested time window crosses retention or resolution boundaries,
  the system MUST show the best available retained history for that window rather than
  failing solely because one source tier is unavailable.
- **FR-010**: When no retained data exists for a requested chart family and window, the
  system MUST render an explicit empty-data state and MUST NOT fall back to stale
  browser-local history as if it were durable truth.
- **FR-010a**: A chart family with no retained data in the shared selected window MUST
  remain visible and show an explicit empty or unavailable state rather than being
  hidden or silently shifted to a different time window.
- **FR-010b**: When a retained-telemetry history query fails, the affected chart family
  MUST remain visible and show an explicit query error state; the dashboard MUST NOT
  fall back to browser-local history as an alternate source of record.
- **FR-011**: Any remaining dashboard consumer that currently presents retained or
  quasi-retained history MUST be reviewed for migration to the durable telemetry source.
- **FR-011a**: For this feature, "other consumers and components" is limited to
  dashboard surfaces that currently show historical trend or state data and can be
  backed by existing retained telemetry.
- **FR-012**: A reviewed consumer MUST be migrated when durable telemetry already
  contains the needed history or can derive it without introducing a new telemetry class.
- **FR-013**: A reviewed consumer MUST remain unchanged when supporting it would require
  a brand-new telemetry class, a net-new operator workflow, or a behavioral redesign;
  such exclusions MUST be documented in the delivered plan and task list.
- **FR-014**: The dashboard MUST treat retained telemetry as the authoritative history
  source across the migrated surfaces, regardless of browser reload, workstation change,
  or service restart.
- **FR-015**: Migrated historical surfaces MUST preserve operator-visible continuity
  across service restarts so previously retained history remains available after the next
  page load.

### Key Entities *(include if feature involves data)*

- **Overview Chart Window**: The operator-selected time span applied to a fleet chart,
  such as 5 minutes, 1 hour, 1 day, 3 days, or 5 days. On the Overview page this window
  is shared across all chart families.
- **Fleet History Series**: A retained time-ordered sequence representing fleet-wide
  trend data for one chart family, used to render LOAD, Health Indicators, SESSIONS, or
  REMOTE FX views.
- **Fleet Aggregation Semantics**: The established meaning of each Overview chart family,
  including which values are fleet averages, which are percentile rollups, and how
  multi-host values are combined so the migrated charts remain operator-familiar.
- **Historical Consumer**: Any dashboard component that presents past-state information
  rather than only the latest snapshot, and therefore may benefit from durable retained
  telemetry.
- **In-Scope Historical Surface**: A dashboard surface that already presents historical
  trend or state information and can be backed by retained telemetry without inventing a
  new telemetry class.
- **Empty Data State**: The operator-visible result shown when a chart or historical
  consumer has no retained data for the requested window.

## Constitution Alignment *(mandatory)*

### Operator Surface Impact

- **Affected surfaces**: dashboard, service query surfaces, operator docs
- **Public behavior changes**: Overview history becomes durable across browsers and
  restarts; Overview charts gain 5M/1H/1D/3D/5D window pills plus zoom, pan, and scroll;
  in-scope historical dashboard consumers stop depending on browser-local warm-up
- **Compatibility / migration**: Existing visuals and chart semantics remain familiar;
  stale browser-local history stops being authoritative after rollout

### Quality and Observability Impact

- **Required tests**: unit tests for time-window selection and empty-state behavior;
  integration tests for retained-history queries and restart continuity; UI validation for
  chart interaction parity across all Overview chart families
- **Operational signals**: dashboard rendering of retained history, logs or diagnostics
  for historical query failures, and visible empty-data states when history is absent
- **Configuration / data impact**: reuses existing retained telemetry and retention
  behavior; no new operator-managed data store is introduced by this feature

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: From a fresh browser profile with no prior local dashboard state, an
  operator can open the Overview page and see retained history for every chart family
  that has data in the selected window.
- **SC-002**: After a service restart, previously retained Overview history remains
  visible on the next page load with no operator action beyond refresh.
- **SC-003**: On each Overview chart family, selecting 5M, 1H, 1D, 3D, or 5D updates
  the visible window within 1 second on a typical LAN-backed operator session.
- **SC-004**: On each Overview chart family, interactive zoom, pan, and scroll complete
  without causing the chart to render stale data from an earlier operator action.
- **SC-005**: Existing Overview visual cues and legend meanings remain recognizable
  enough that current operators can use the upgraded charts without new training.
- **SC-006**: Every migrated historical dashboard consumer continues to show the same
  logical information after clearing browser-local storage and reloading from a new
  workstation.
- **SC-007**: The implementation leaves no in-scope dashboard history surface relying on
  browser-local retention when durable telemetry already provides the authoritative data.
- **SC-008**: When a retained-history query fails during Overview rendering, the affected
  chart shows an explicit error state instead of silently displaying browser-local
  history as if it were authoritative.

## Assumptions

- The retained telemetry produced by the existing service already covers the fleet and
  per-host data needed for the requested Overview chart families, or can derive the fleet
  view from stored host-level telemetry without changing the operator-facing meaning.
- The Overview page remains the primary fleet-wide investigation surface, so preserving
  its current visual language is more important than introducing a redesign.
- Browser-local storage may still exist for transient UI state, but it is no longer the
  authoritative source for retained operational history on migrated surfaces.
- The requested "other consumers and components" scope applies to historical dashboard
  consumers that benefit from durable retained telemetry, not to unrelated snapshot-only
  widgets, non-dashboard consumers, or surfaces that would require a brand-new telemetry
  class.
- If a candidate surface would require collecting a wholly new kind of telemetry rather
  than reusing retained data already owned by DrainCtl, that work belongs in a follow-up
  feature rather than this one.
