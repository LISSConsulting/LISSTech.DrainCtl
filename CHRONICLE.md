# CHRONICLE

Cumulative changelog for DrainCtl (Roams #1-99).

## Features

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

- `cmd/drainctl/main.go` split into 8 command files; `handler.go` split into handler + check
- `ClassifyState` promoted to root package; `audit.go` streaming refactor with `scanRecords`
- Dashboard: all inline styles extracted to CSS classes; `mock.js` relocated with build tags
- Landing page: zero inline styles, mobile nav, a11y (ARIA roles/labels, focus management, keyboard nav)
- Replaced custom `LogFunc`/`Level` logging API with `log/slog` across entire codebase (~173 call sites); added `internal/logging` package with `CLIHandler`, `FileHandler`, `MultiHandler`, `ParseLevel`, `PrintResult`; service composes dual-sink via `MultiHandler`; `--quiet` replaced with `--log-level debug|info|warn|error`; DLL discard via `slog.DiscardHandler` in `init()`
- Per-sink log levels in `config.json` (`log_file_level`, `log_event_level`): `Validate()` normalises via `ParseLevel` with fallback to defaults; `RunService()` applies levels on startup; `Execute()` config-reload path updates `slog.LevelVar` at runtime without service restart
- Modern ETW manifest provider `LISS Technologies-DrainCtl` replaces legacy `eventlog` sink: `assets/drainctl.man` defines Operational (INFO+, enabled by default) and Debug (DBG, disabled by default) channels with event IDs 1000–3099/4000; `internal/logging.ETWHandler` implements `slog.Handler` via `advapi32.dll` `EventRegister`/`EventEnabled`/`EventWrite`; known event IDs set via `slog.Int("event_id", Evt*)` attribute for precise manifest routing; `internal/svc/handler.go` composes `FileHandler` + `ETWHandler` via `MultiHandler`; `drainctl.mc` retired; installer WXS registers/unregisters provider via `wevtutil im/um`; `just man` recipe added for recompiling `drainctl-msg.dll` from manifest

## Performance

- uPlot `setData()` reuse; HTTP keep-alive body drain on all response paths
- `handleHistory` passes limit directly to `HostHistory()` for non-filter queries
