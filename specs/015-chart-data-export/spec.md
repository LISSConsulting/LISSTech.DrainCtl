# Feature Specification: Excel and CSV Graph Data Export

**Feature Branch**: `015-chart-data-export`
**Created**: 2026-09-30
**Status**: Draft
**Input**: User description: "I want to add Excel and CSV export for DrainCtl's graphs."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Export a graph to CSV (Priority: P1)

As a network and security operator, I want to download the data behind a graph so I can investigate it, compare it with other evidence, and share the numbers without manually copying tooltips.

**Why this priority**: CSV provides a portable, independently useful export for analysis and automation.

**Independent Test**: Select a graph, time range, hosts, and visible series; export CSV and compare its observations and context with the graph's underlying data.

**Acceptance Scenarios**:

1. **Given** a loaded graph with observations, **When** the operator selects CSV export, **Then** one CSV file downloads containing the selected graph's visible series and observations within its displayed time range.
2. **Given** a panned historical window and a restricted host selection, **When** export starts, **Then** the file records that exact window and host scope and excludes other windows and hosts.
3. **Given** a multi-series graph with one series hidden, **When** export starts, **Then** only enabled series are exported using their actual values and units rather than drawing coordinates or normalized axis values.
4. **Given** missing observations and genuine zero values, **When** the file is opened, **Then** missing values remain empty and zero values remain numeric zero.

---

### User Story 2 - Export a graph to Excel (Priority: P1)

As an operator, I want an Excel workbook that opens directly with usable numeric data and enough context for someone else to understand it.

**Why this priority**: Excel export is an explicit part of the requested feature and supports the operator's reporting workflow.

**Independent Test**: Export the same fixed graph snapshot to CSV and Excel; open the workbook in Excel and compare all observations, series definitions, and context.

**Acceptance Scenarios**:

1. **Given** a loaded graph, **When** the operator selects Excel export, **Then** a valid `.xlsx` workbook downloads and opens without a repair warning.
2. **Given** a workbook, **When** the recipient opens it, **Then** a Data sheet contains the observations with numeric metric cells, and a Context sheet identifies the graph, scope, time range, timestamp convention, series units, and aggregation.
3. **Given** identical graph snapshots, **When** exported to both formats, **Then** both files contain equivalent metric values and context; presentation formatting may differ.

---

### User Story 3 - Export confidently from any supported graph (Priority: P2)

As an operator, I want predictable export controls and clear failure messages throughout the dashboard.

**Why this priority**: Consistent controls and honest data boundaries prevent misleading evidence and incomplete reports.

**Independent Test**: Exercise exports across the supported graph inventory, including loading, empty, partially covered, unauthorized, and failed states.

**Acceptance Scenarios**:

1. **Given** any supported graph, **When** the operator uses its export control by keyboard, **Then** both formats are available and the control names the graph being exported.
2. **Given** a loading graph, a failed graph request, no enabled series, or no observations in the selected window, **When** the operator attempts export, **Then** export is unavailable with a reason and no misleading empty file downloads.
3. **Given** a live graph, **When** export starts and new observations arrive or the operator changes scope, **Then** the file uses the captured graph snapshot and does not mix later state into it.
4. **Given** incomplete retained coverage, **When** available observations are exported, **Then** the file identifies the requested window, available coverage, and partial-data condition without claiming full coverage.
5. **Given** a failed export, **When** the failure occurs, **Then** the operator sees an actionable error and can retry without reloading the dashboard.

### Edge Cases

- Irregular timestamps or series with different sample times: retain each observation's actual timestamp; never invent alignment, interpolate values, or fill gaps with zero.
- Aggregated windows: preserve the same aggregation and bucket resolution as the graph and identify them in the file.
- Percentiles, fleet aggregates, and derived metrics: preserve their graph meaning, including population and derivation; do not reinterpret a fleet statistic as per-host data.
- Unicode host names and labels, commas, quotes, and newlines: round-trip without broken columns or lost characters.
- Text beginning with formula-triggering characters: open as inert text, including after whitespace or control characters.
- All series hidden, all values missing, or all hosts deselected: no usable export; explain the reason.
- Partial retention, offline hosts, or unavailable counters: distinguish unavailable values from measured zero and identify known coverage limitations.
- A host/window change during loading must not allow stale observations to export under the new context.
- Session expiration or lost authorization during any required retrieval must fail visibly and must not expose additional data.
- Large supported windows: no silent row truncation or partial workbook presented as a successful complete export.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Each supported time-series graph MUST offer CSV and Excel export through a clearly associated, keyboard-accessible control.
- **FR-002**: The supported inventory MUST include Overview Load, Health Indicators, Session Metrics, RemoteFX graphs when available, per-host Load, and other existing per-host metric time-series graphs. Decorative sparklines and non-time-series tables are excluded. The implementation plan MUST enumerate the concrete graph inventory before coding.
- **FR-003**: One export MUST represent one selected graph, including all its enabled series. Exporting a page or all graphs together is outside this feature.
- **FR-004**: Export MUST capture one coherent snapshot of the graph's loaded data, displayed absolute time bounds, host scope, enabled series, and aggregation at invocation. It MUST not add undisplayed history or silently switch to a raw-data resolution.
- **FR-005**: Export MUST include actual metric values before visual normalization and display rounding, preserving the available source precision. It MUST preserve displayed derived-metric and fleet-aggregation semantics.
- **FR-006**: Each observation MUST identify its timestamp, stable series identifier, readable series label, unit, value, and host or fleet scope. Rows MUST be ordered chronologically with deterministic series ordering for equal timestamps.
- **FR-007**: Both formats MUST be self-describing: graph name, export time, requested range, known available coverage, selected host identities, series definitions, units, aggregation, and resolution MUST be recoverable from the file alone. Fleet observations MUST retain fleet identity rather than fabricated per-host rows.
- **FR-008**: Timestamps MUST use an unambiguous UTC convention. CSV MUST use ISO 8601 timestamps with a UTC indicator; Excel MUST retain equivalent timestamps and explicitly label them UTC. The graph's display time zone MUST also be recorded.
- **FR-009**: CSV MUST be UTF-8, use a comma separator and a single header row, and correctly escape quotes, commas, and newlines. Context MUST be included in named columns rather than a non-tabular preamble. Observation columns MUST include `timestamp_utc`, `scope`, `series_id`, `series_label`, `unit`, and `value`; additional context columns MUST carry FR-007 information.
- **FR-010**: Excel export MUST produce a genuine `.xlsx` workbook with Data and Context sheets. Metric values MUST be numeric cells; identities and labels MUST be text. Data MUST have readable headers, frozen headers, filtering, and readable column widths.
- **FR-011**: Missing values MUST be empty, genuine zeros MUST remain zero, and invalid non-finite values MUST not become valid numeric measurements. Export MUST not interpolate or fabricate observations.
- **FR-012**: CSV and Excel generated from the same snapshot MUST preserve equivalent observations and context.
- **FR-013**: Files MUST have sanitized, descriptive names identifying DrainCtl, graph, scope, and captured time range, with the correct extension.
- **FR-014**: Export MUST be disabled with an understandable reason while graph data is loading, failed, stale for its selected context, empty, or has no enabled series. Progress and failures MUST be visible without blocking ordinary dashboard navigation.
- **FR-015**: Export MUST preserve existing dashboard authorization and expose only data the operator can view. It MUST require no external upload, third-party export service, Excel installation on the server, or additional operator privileges.
- **FR-016**: Untrusted labels, host identities, and other text MUST remain inert when opened in spreadsheet software; export MUST prevent formula interpretation and workbook macros or executable content.
- **FR-017**: Supported exports MUST complete without silent truncation. If a documented size limit is exceeded, the operator MUST receive an explicit explanation and guidance to narrow the range; no incomplete file may be reported as complete.
- **FR-018**: Partial coverage MUST be explicitly identified, including its known bounds; export MUST not imply missing history was collected.
- **FR-019**: Operator documentation MUST explain graph scope, selected-series behavior, aggregation, timestamp convention, partial coverage, and format differences.

### Key Entities

- **Graph Snapshot**: Immutable export context linking one graph's observations, absolute range, host scope, enabled series, aggregation, resolution, and coverage.
- **Series Definition**: Stable identifier, readable label, unit, and meaning, including any derivation or fleet statistic.
- **Observation**: Timestamped value or missing value for a series and scope.
- **Export Artifact**: CSV or Excel representation of a snapshot, with descriptive filename and self-contained context.

## Constitution Alignment *(mandatory)*

### Operator Surface Impact

- **Affected surfaces**: Dashboard and operator documentation. Any supporting dashboard retrieval remains within existing authorization boundaries.
- **Public behavior changes**: Additive file-download capability and documented export formats. Existing graph interaction and telemetry semantics remain compatible.
- **Other surfaces assessed**: Root package, CLI, DLL/interop, PowerShell, agent telemetry, drain control, and installer gain no new commands or behavior. The Windows service continues its existing telemetry and dashboard responsibilities.
- **Compatibility / migration**: No operator migration, new credentials, or server-side Excel installation. Windows Server remains the supported deployment platform.

### Quality and Observability Impact

- **Required tests**: Snapshot/series selection, actual values versus normalized drawing values, derived and fleet semantics, chronological ordering, UTC conversion, CSV escaping and Unicode, missing/zero values, text formula safety, valid Excel structure and numeric cells, and cross-format parity. Dashboard boundary tests MUST cover inventory, scope changes, live updates, loading/empty/partial/failure states, keyboard access, and authorization wherever retrieval occurs. Verify Excel opening without repair warnings.
- **Operational signals**: Visible export availability, progress where work is asynchronous, partial-coverage information, and actionable errors. Diagnostics MUST identify failures without logging exported telemetry, credentials, or full file contents.
- **Configuration / data impact**: No new runtime configuration, telemetry retention policy, stored export history, or schema migration. Exports are operator-initiated downloads; collection and retention remain under existing ownership.
- **Release and verification discipline**: Preserve git-derived release versioning and existing packaging. Implementation MUST pass the repository's required unit/boundary checks, `go test ./...`, `just lint`, and pre-commit checks in the supported environment. Update relevant operator guidance with the behavior change.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: An operator can start either export format from any supported, loaded graph in no more than two interactions.
- **SC-002**: For fixed acceptance fixtures, 100% of exported observations, selected series, units, scope, aggregation, and time boundaries match the captured graph snapshot, with no invented values or silent truncation.
- **SC-003**: Every acceptance CSV parses into one consistent table, and every acceptance Excel workbook opens without repair warnings and supports numeric sorting/filtering.
- **SC-004**: A 10,000-observation export completes within five seconds on the agreed supported acceptance environment; larger cases either complete within documented limits or return an explicit limit message.
- **SC-005**: All supported graph inventory entries pass keyboard-triggered CSV and Excel export checks, and all defined loading, empty, partial, stale, and failed scenarios produce the specified outcome.
- **SC-006**: Formula-safety fixtures execute zero formulas, and cross-format fixtures preserve equivalent values and context.

## Assumptions

- "Export graphs" means export their underlying numeric data. PNG/PDF graph images, embedded Excel charts, raw-history bulk extraction, combined page workbooks, scheduled reports, email delivery, and saved export history are outside v1.
- Export respects the current visible series, displayed window including pan, and host filter. Fleet graphs export their displayed aggregate, not individual host histories.
- Data is the graph's loaded snapshot at its current aggregation/resolution, rather than a fresh moving query or higher-resolution historical extract.
- Excel means `.xlsx`, not a CSV renamed to an Excel extension.
- Existing dashboard access is sufficient; no new role or permission model is introduced.
- CSV favors a consistent observation table with repeated context columns; Excel separates context for readability.
- The live interface could not be inspected because browser access to the supplied private address was blocked. Graph inventory is based on current `develop` source; visual placement should be verified against the running dashboard during planning.
