> [Project]: spec-driven AI coding loop.
> Current state: **Fourth roam-mode pass complete.** Exponential backoff for dashboard config fetch; interactive `drainctl configure` wizard.

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

## Remaining Work

| Priority | Item | Location | Notes |
|----------|------|----------|-------|
| Low | SSPI `RequireGroup` — add explicit `RevertToSelf` in error paths | `internal/dashboard/sspi.go` | Defensive; thread token impersonation cleanup |
| Low | Config hot-reload: propagate new notification config to in-flight `NotifyState` | `internal/svc/handler.go` | Currently stale state survives until service restart |
| Low | `drainctl history` — add `--since` / `--until` flags for time-bounded queries | `cmd/drainctl/main.go` | Convenience for operators |

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
