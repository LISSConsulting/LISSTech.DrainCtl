# CHRONICLE

Cumulative changelog for DrainCtl (Roams #1-99).

## Features

- **Spec 002 — Vite + Svelte Dashboard (all phases)**: Migrated monolithic 6 732-line `dashboard.html` to a Vite + Svelte 5 + Tailwind CSS v4 SPA. Preserves full feature parity plus: Layercake-based performance metrics chart (CPU %, Memory %, Input Delay, Sessions); I/O ring gauge threshold colours (Disk Queue / Input Delay / TCP Retransmits now green/amber/red like CPU/Memory/Sessions); config modal UX improvements (dirty-state amber tint, save/error banners, test-on-left layout, `.btn-brutal` hover on all buttons); notification target modal scrollbar consistency; theme toggle (light/dark, localStorage persistence, flash-prevention inline script); all 15 Svelte components; `frontend/` project at repo root; `just frontend` + `just frontend-copy` recipes wire Vite build into `just cli`; Go embed changed from single `dashboard.html` to `//go:embed all:dist`; new `/assets/*` route with `Cache-Control: public, max-age=31536000, immutable`; CSP updated to remove `'unsafe-inline'` for scripts/styles (served from `'self'`). Bundle: 135 KB JS + 43 KB CSS (gzip: 48 KB + 8 KB). Zero build warnings.
- Webhook HMAC-SHA256 signing (`X-DrainCtl-Signature`), per-target secret/triggers/repeat config
- Dashboard REST API: `/health`, `/servers/{host}`, `/history/{host}`, `/notify-test`, `/notify-config` (GET/PUT)
- Dashboard UI: server filter bar, history drill-down modal, dark/light mode, Grace countdown, settings modal (HMAC secret, test button), dynamic page title, filter persistence
- `drainctl configure` interactive wizard + flag-driven path (`--grace-period`, `--poll-interval`, `--retention-days`, `--session-warning-threshold`, `--webhook-url`, `--ntfy-url`)
- `drainctl configure show` read-only config inspection
- `drainctl history --since/--until` RFC 3339 time-bounded queries
- `drainctl service start/stop/status` via SCM
- `drainctl sessions` (WTS enumeration, table/JSON/CSV/plain)
- `drainctl check` plain output includes session data; webhook payload enriched with sessions, grace, version
- Exponential backoff for dashboard config fetch (wall-clock, 5m base, 160m cap)
- Background perfmon sampler (configurable interval) with aggregated snapshots over poll window

## Bug Fixes

- `HistoryModal.svelte` "Transitions Only" toggle had a race condition: if the user toggled the filter while a fetch was in flight, the `if (loading) return` guard silently dropped the second `loadHistory()` call; after the first fetch completed the displayed data was for the wrong filter value; fixed by snapshotting `changesOnly` at fetch-start and re-invoking `loadHistory()` in the `finally` block when the value has since changed
- `EventLog.svelte` rendered "EVENT LOG" twice in normal (non-expanded) view — once as the outer `.section-label` section header and again as the inner `.log-title` inside the scrollable log box; the inner title now only renders when the log is in fullscreen-expanded mode where the outer header is hidden behind the overlay
- `ServerTable.svelte` `graceCountdown()` showed `"0s left"` instead of `"expired"` when the deadline was within the last second (`ms` in 0–999 range): `Math.floor(ms/1000) === 0` produced `"0s left"` because the check was `ms <= 0` (already expired); fixed by testing `s <= 0` after the floor, so any sub-second remaining time collapses to `"expired"`
- `App.svelte` event-log timestamp used `Date.toLocaleTimeString()` with no locale argument, producing locale-dependent output (12/24h, separator differences) that could not be reliably parsed by `EventLog`'s `parseEvent()` regex; now passes `'en-US', { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }` for a stable `HH:MM:SS` format

- `handleNotifyTest` (server.go) could not individually test **email** notification targets from the `TargetEditModal` — the single-target routing condition checked only `t.URL != ""`, which is always empty for email targets (they use `From`/`To`); extended to also match `t.Type == "email"` so the modal's "Test" button correctly sends a test to just that email target instead of silently falling back to "test all targets" mode
- `NotificationTarget.Enabled` field was frontend-only and non-functional — the `enabled` checkbox in `TargetEditModal` was persisted to UI state but the Go backend struct had no `Enabled` field, so the flag was silently dropped on save and all targets always fired regardless of their enabled state; added `Enabled *bool` with `json:"enabled,omitempty"` to the Go struct, updated `SendNotification` and `SendTestNotification` to skip targets where `Enabled == false`, updated `api.js` JSDoc typedef, and fixed `NotificationTargets.svelte` to normalise `null`/`undefined` → `true` when opening the edit modal so the checkbox renders correctly for targets loaded from older configs
- `handleUI` CSP used `script-src 'self' 'unsafe-inline'` — the comment claimed nonce injection but it was never implemented; per-request 128-bit nonce is now generated in `handleUI`, injected into both the `Content-Security-Policy` header (overriding the `securityMiddleware` baseline) and the theme flash-prevention `<script>` attribute in `index.html`; `cspBase` constant now carries `script-src 'self'` (no `unsafe-inline`); `style-src 'unsafe-inline'` is intentionally kept because Svelte components use reactive inline `style=` attributes
- `serverMetrics` Map in `state.svelte.js` retained stale ring buffers after servers were deleted, causing a minor memory leak; added `removeServerMetrics(host)` helper and called it from `ServerTable.removeServer()` immediately after the delete API call succeeds
- `HistoryModal.svelte` `loadHistory()` had no guard against concurrent calls — rapid "Transitions Only" toggle would start a second fetch before the first resolved, risking stale-result overwrite; added `if (loading) return` guard consistent with `App.svelte`'s `refreshing` flag pattern
- `theme.svelte.js` exported a reactive `let` binding (`export let currentTheme = $state('light')`) which does not work as a cross-module reactive value in Svelte 5 — consumers received a snapshot, not a live reference; replaced with `export const theme = $state({ current: 'light' })` so the stable object reference is shared and property access is correctly tracked; `Nav.svelte` updated to use `theme.current`

- `ServerDetail.svelte` sessions ring gauge used an arbitrary `max = sessions * 1.5` heuristic, making the ring always fill to ~67% and the amber/red thresholds meaningless; `ServerView` in `internal/dashboard/server.go` now exposes `max_sessions` from `SessionSummary.MaxSessions`; `ServerDetail` derives `sessionsPct` (0–100 utilisation % when capacity is known, `null` when unknown) and uses `session_warning_threshold` from config as the amber threshold; `RingGauge.svelte` gains an optional `centerLabel` prop so the sessions ring can display the raw count ("10") rather than the %-based `displayValue`; ring color is correctly neutral when max is unknown
- `App.svelte` refresh loop had no guard against concurrent execution — if an API call took longer than 30 s the next tick would start a parallel fetch, potentially interleaving stale state; added `refreshing` boolean flag with `finally` reset so at most one refresh is in flight at a time
- `TargetEditModal.svelte` had no keyboard shortcut to dismiss: `Escape` now closes the modal (consistent with `ConfigModal.svelte` and `HistoryModal.svelte`)
- `HistoryModal.svelte` caught errors with `e.message` which is `undefined` when the thrown value is not an `Error` instance (e.g. a string or non-Error object); fixed to `e?.message ?? String(e)`

- `ServerTable.svelte` grace-period deadline never surfaced: `grace_deadline` existed in the `Server` typedef but was not rendered; added inline countdown badge (e.g., "14m left", "expired") next to the Grace pill so operators immediately see how much drain time remains
- `ConfigModal.svelte` "Custom" grace-period pill always appeared inactive even when a non-preset value was typed into the numeric input; pill now activates (amber fill) whenever `grace_period_minutes` is not in `GRACE_PRESETS`
- `ConfigModal.svelte` / `HistoryModal.svelte` had no keyboard shortcut to dismiss: `Escape` now closes both modals (respecting ConfigModal's dirty-state guard)
- `HistoryModal.svelte` fetch limit was hardcoded to 20, too low for active servers; raised to 100 (matches `api.js` default)

- `RingGauge.svelte` `displayValue` when `unit === '%'` showed the normalised ring position (`value/max*100`) instead of the raw value; for TCP Retransmits (`value=5%`, `max=20`) this rendered "25%" instead of "5%"; fixed to display `Math.round(value)%` directly
- `api.js` all six `res.json()` calls were missing `await`; JSON parse errors escaped the async function error boundary; all call sites now properly await the body parse
- `EventLog.svelte` auto-scroll to latest (T027) was absent; added a `$effect` that scrolls to `scrollTop=0` when the event list changes unless the user has scrolled down >80px to read older entries
- `ServerDetail.svelte` `resolveThresholds()` existed but was never called — CPU/Memory/Input Delay ring gauges always used static `DEFAULTS` regardless of user config; `App.svelte` now fetches `notify-config` once on first refresh and stores it in `appState.config`; `ConfigModal.svelte` writes the saved config back on success; `ServerDetail` derives per-metric thresholds via `resolveThresholds()` so the three configurable metrics respect user settings
- `HistoryModal.svelte` `statusClass()` always returned `'off'` — function checked for capitalised labels (`'Healthy'`/`'Grace'`/`'Alert'`) but API sends lowercase values (`'ok'`/`'grace'`/`'alert'`/`'off'`); all history badges rendered as grey "Offline"; fixed to map lowercase API values directly
- `api.js` JSDoc typedefs mismatched the actual backend wire format: `HistoryEntry.duration_s` renamed to `state_duration_seconds` (nullable), added `transition`/`transition_from` fields; `NotifyTarget.destination` renamed to `url`, added `from`/`to`/`secret`/`enabled` fields
- `state.svelte.js` `totalSessions` used `$derived(() => fn)` which stored the arrow function itself as the derived value instead of a numeric result; corrected to `$derived.by(() => counters.sessions)`
- `ServerTable.svelte` grace countdown and relative timestamps (`rel()`, `graceCountdown()`) called `Date.now()` at render time and only refreshed when `appState.servers` changed (every 30 s); countdown badges went stale between refresh cycles; fixed by adding a reactive `now = $state(Date.now())` clock that ticks every 10 s and threading it as an explicit parameter so Svelte tracks the time dependency
- `ServerDetail.svelte` sparklines (CPU / Memory / Input Delay) drew from the fleet-aggregate `appState.metricsHistory` for every expanded row, so all server detail panels showed identical averaged data regardless of which host was expanded; fixed by adding per-server ring buffers (`appState.serverMetrics: Map<string, MetricsSample[]>`) populated each refresh cycle in `App.svelte`, with a graceful fallback to the fleet aggregate until per-server data is available

- `ConfigModal.svelte` / `ServerDetail.svelte` / `api.js`: five field-name mismatches between the Go backend wire format and Svelte frontend caused the Config Modal to be completely non-functional in production: grace period input was blank (`grace_period_minutes` → `grace_period`), performance section was hidden (`perf_monitoring` → `performance`), notification targets always showed "No targets configured" (`targets` → `notifications`), and all saves silently dropped because PUT body contained unrecognised keys; additionally the PUT response is `{ok:true}` (no body) but the old code overwrote the working config copy with it, breaking the modal on every save; `ServerDetail` was always using static threshold defaults because `appState.config?.perf_monitoring` was always undefined; `TargetEditModal` test button sent `{target:"<uuid>"}` but the backend decodes the full `NotificationTarget` directly from the request body — fixed to send the full target object so the backend tests the exact unsaved configuration; `PerfMonitoringConfig` typedef renamed to match Go struct tags (`sample_interval_sec`, `collect_per_session`, `collect_remotefx`)
- Dashboard API response shape completely mismatched the frontend contract: `GET /api/v1/servers` returned raw `ServerInfo` (nested `last_result`, capitalized status tokens "Healthy"/"Grace"/"Alert", no `sessions`/`perf` at top level) while the frontend expected a flat `ServerView` with lowercase status tokens, `sessions` (int), and canonical `PerfSnapshot` fields; `GET /api/v1/history/{host}` similarly returned raw `CheckResult` with capitalized status; five PerfSnapshot field names in the frontend differed from Go JSON tags (`mem_free_mb`→`mem_avail_mb`, `input_delay_ms`→`input_delay_p95_ms`, `tcp_retransmits_pct`→`tcp_retrans_sec`, non-existent `sample_count`); fixed by adding `ServerView`/`HistoryView` projections and `statusToken()` in `internal/dashboard/server.go`, updating all three handlers, correcting all five frontend components and `api.js` typedef, rewriting `testdata/mock.js` to emit canonical wire-format objects, and updating all affected Go tests
- `NotifyState` session-warn map split from alert map (shared map caused silent deletion)
- `loadNotifyConfig` JS falsy-zero trap (`parseInt("0") || 80` overrode disabled threshold)
- `serviceHandler.cfg` race fixed with `atomic.Pointer`; `pipeConn.SetDeadline` forwarding fix
- `registerWithDashboard` retry loop; `handler.cfg.Store` after `applyRemoteConfig`
- `generateSelfSigned` partial file cleanup; `CreateMutex` `ERROR_ALREADY_EXISTS` handling
- Dashboard: history modal field name mismatch, stale DOM refs, async race conditions
- Session-warning cooldown reset narrowed to exclude transient WTS failures
- Perfmon PdhCollectQueryData AV on memory-constrained DCs (idle gap between collects)

## Security

- SSPI `RequireGroup` with `RevertToSelf`; IP rate limiter (10 req/s, burst 60, `Retry-After`)
- `Content-Security-Policy`, `Cache-Control: no-store`, security headers on all responses
- RFC 1123 hostname validation; URL scheme validation; target validation before save (400 on invalid)
- Dashboard: delegated event handlers (no inline `onclick`), XSS escaping, client-side URL validation

## Code Quality

- Removed `internal/dashboard/dashboard.html` — leftover from the pre-Svelte monolith; the Go embed now uses `//go:embed all:dist` exclusively (T048)

- `cmd/drainctl/main.go` split into 8 command files; `handler.go` split into handler + check
- `ClassifyState` promoted to root package; `audit.go` streaming refactor with `scanRecords`
- Dashboard: all inline styles extracted to CSS classes; `mock.js` relocated with build tags
- Landing page: zero inline styles, mobile nav, a11y (ARIA roles/labels, focus management, keyboard nav)
- Replaced custom `LogFunc`/`Level` logging API with `log/slog` across entire codebase (~173 call sites); added `internal/logging` package with `CLIHandler`, `FileHandler`, `MultiHandler`, `ParseLevel`, `PrintResult`; service composes dual-sink via `MultiHandler`; `--quiet` replaced with `--log-level debug|info|warn|error`; DLL discard via `slog.DiscardHandler` in `init()`
- Per-sink log levels in `config.json` (`log_file_level`, `log_event_level`): `Validate()` normalises via `ParseLevel` with fallback to defaults; `RunService()` applies levels on startup; `Execute()` config-reload path updates `slog.LevelVar` at runtime without service restart
- Modern ETW manifest provider `LISS Technologies-DrainCtl` replaces legacy `eventlog` sink: `assets/drainctl.man` defines Operational (INFO+, enabled by default) and Debug (DBG, disabled by default) channels with event IDs 1000–3099/4000; `internal/logging.ETWHandler` implements `slog.Handler` via `advapi32.dll` `EventRegister`/`EventEnabled`/`EventWrite`; known event IDs set via `slog.Int("event_id", Evt*)` attribute for precise manifest routing; `internal/svc/handler.go` composes `FileHandler` + `ETWHandler` via `MultiHandler`; `drainctl.mc` retired; installer WXS registers/unregisters provider via `wevtutil im/um`; `just man` recipe added for recompiling `drainctl-msg.dll` from manifest
- US5/US6 polish: `FileHandler` timestamps use local time with offset (`2006-01-02T15:04:05.000-07:00`); `PrintResult()` writes `--- <msg> [fields]\n` to stdout with no timestamp or level tag, suppressed when `--format json/csv/table` is set (verified across all CLI commands: `check_cmd`, `dashboard_cmd`, `notify_cmd`, `register_cmd`, `service_cmd`); CLAUDE.md Architecture updated to reflect slog/ETW logging architecture

- Spec 001 tiered-logging complete (T001-T063): `just lint` 0 issues, `just gotest` all pass, `just all` succeeds; zero references to `LogFunc`/`LvlOK`/`DefaultLogger`/`DiscardLogger`/`MultiLogger`/`EventLogLogger`/`FileLogger`/`LogMsg` remain in non-test `.go` files

## Performance

- uPlot `setData()` reuse; HTTP keep-alive body drain on all response paths
- `handleHistory` passes limit directly to `HostHistory()` for non-filter queries
