> [Project]: spec-driven AI coding loop.
> Current state: **Fifth roam-mode pass complete.** SSPI RevertToSelf cleanup; config hot-reload NotifyState pruning; `drainctl history --since/--until` flags.

## Completed Work

| Phase | Features | Tags |
|-------|----------|------|
| Roam #1 | Webhook HMAC-SHA256 signing (`Secret` field on `NotificationTarget`; `X-DrainCtl-Signature` header) | security, notify |
| Roam #1 | `GET /api/v1/health` dashboard endpoint (unauthenticated; version + server counts) | feature, dashboard |
| Roam #1 | Hostname validation in `handleRegister` (trim whitespace, DNS 253-char limit) | security, validation |
| Roam #1 | URL scheme validation in `Config.Validate` (strip non-http/https notification targets) | security, validation |
| Roam #1 | First unit test suite — 27 tests across `config_test.go` and `notify_test.go` | testing |
| Roam #1 | gofmt pre-existing violations fixed across all Go source files | code quality |
| Roam #2 | `internal/dashboard` handler tests — 19 tests for `handleHealth`, `handleRegister`, `handleReport` | testing, dashboard |
| Roam #2 | `audit.go` streaming refactor — `readAll` replaced with `scanRecords(fn)` + rolling-window `History`/`Changes` | performance, code quality |
| Roam #3 | `GET /api/v1/history/{host}` — per-host CheckResult ring buffer (cap 100) in `ServerState`; newest-first JSON; `?limit` param; 7 tests | feature, dashboard, testing |
| Roam #4 | Exponential backoff for dashboard config fetch — `backoffTicks(failures)` doubles interval per failure (10→20→40→80→160→320 polls, cap); resets on success or URL change; 1 test | resilience, svc |
| Roam #4 | `drainctl configure` interactive wizard — prompts each setting with current value as default; flag-driven path unchanged for MSI installer; command unhidden | feature, UX, CLI |
| Roam #5 | SSPI `RequireGroup` — `RevertToSelf` called after `isGroupMember` (all paths); `procRevertToSelf` via advapi32.dll | security, dashboard |
| Roam #5 | Config hot-reload: `pruneNotifyState` removes stale `LastAlertNotify` entries for URLs removed from targets | correctness, svc |
| Roam #5 | `drainctl history --since`/`--until` — RFC3339 time-bounded queries; `HistoryFiltered`/`ChangesFiltered` on `AuditStore`; post-filter on pipe path; 11 tests | feature, CLI, testing |

## Remaining Work

*(All tracked items complete — nothing pending.)*

## Key Learnings

- **Zero tests existed** prior to this session. All Go files carry `//go:build windows`; test files need the same tag. The root package contains enough pure logic (config, notify, format) to be tested without syscall mocks.
- **Webhook signing pattern**: `X-DrainCtl-Signature: sha256=<hmac>` matches GitHub-style webhook signatures. Secret stored in `NotificationTarget.Secret`; empty secret = no header (backwards-compatible).
- **Health endpoint design**: Placed at `GET /api/v1/health` with no auth so load balancers and uptime monitors can probe without Kerberos. Returns `ok`, `version`, `servers`, `healthy`, `alerting`, `unknown` counts.
- **URL scheme validation**: Added to `Config.Validate` so it applies uniformly on every config load/save path. Empty URLs are preserved (existing behaviour handles them downstream).
- **`svc/handler.go` is large** (800+ lines) — split into sub-files would improve navigability but isn't blocking anything yet.
- **Dashboard backoff is poll-count-based, not time-based**: `backoffTicks(n)` returns polls-until-next-retry so it composes cleanly with the existing `pollsSinceConfigFetch` counter. Time-based backoff would require an extra timer or timestamp comparison.
- **pflag flag detection**: `cmd.Flags().NFlag()` counts explicitly set flags (cobra/pflag); `NChanged()` does not exist. The pattern `NFlag() == 0` reliably separates interactive from scripted invocation.
- **History ring is in-memory only** — `ServerState.history` is not persisted to `servers.json`. It resets on service restart. This keeps the implementation simple; records reaccumulate as reports arrive. Persisting 100×N `CheckResult` JSON per host would bloat `servers.json` significantly.
- **Dashboard handler tests need no `devmode` tag**: handlers are methods on `*DashboardServer`; calling them directly with `httptest.NewRecorder` bypasses all middleware. Auth is only injected by the mux wrappers, not by the handlers themselves.
- **`audit.go` streaming pattern**: `scanRecords(fn func(AuditRecord) bool) error` replaces `readAll`. `History(n)` uses a copy-shift ring buffer — `O(n)` memory regardless of file size. `Changes(n)` uses the same ring. `StateSince` still collects all records (needs backward walk). `LastObservation` retains a single record.
- **SSPI `RevertToSelf` placement**: Calling `RevertToSelf` immediately after `isGroupMember` (before any early return) ensures cleanup in both error and success paths. If no impersonation happened, `RevertToSelf` is a no-op.
- **`NotifyState.LastAlertNotify` keyed by URL**: On config hot-reload, prune stale entries by building an active-URL set and deleting entries not in it. Only prune when using local config (not dashboard remote), since remote config manages its own targets.
- **`HistoryFiltered` ring buffer with time filter**: The scan is oldest-first; applying the time predicate before the ring-shift means only matching records count toward the limit. The pipe path returns `[]HistoryRecord` (string timestamps), so time filtering there is done post-fetch with RFC3339 parsing.
