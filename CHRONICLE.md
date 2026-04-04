> [Project]: spec-driven AI coding loop.
> Current state: **First roam-mode pass complete.** Security, validation, and test coverage improvements shipped.

## Completed Work

| Phase | Features | Tags |
|-------|----------|------|
| Roam #1 | Webhook HMAC-SHA256 signing (`Secret` field on `NotificationTarget`; `X-DrainCtl-Signature` header) | security, notify |
| Roam #1 | `GET /api/v1/health` dashboard endpoint (unauthenticated; version + server counts) | feature, dashboard |
| Roam #1 | Hostname validation in `handleRegister` (trim whitespace, DNS 253-char limit) | security, validation |
| Roam #1 | URL scheme validation in `Config.Validate` (strip non-http/https notification targets) | security, validation |
| Roam #1 | First unit test suite — 27 tests across `config_test.go` and `notify_test.go` | testing |
| Roam #1 | gofmt pre-existing violations fixed across all Go source files | code quality |

## Remaining Work

| Priority | Item | Location | Notes |
|----------|------|----------|-------|
| High | Add `internal/dashboard` tests (handleHealth, handleRegister validation, handleReport) | `internal/dashboard/` | Needs httptest + auth bypass via devmode build tag |
| High | Stream-based audit history (`readAll` loads entire JSONL into memory) | `audit.go`, `internal/store/memstore.go` | Risk of OOM on long-lived high-frequency deployments |
| Medium | `GET /api/v1/history/{host}` endpoint — expose per-host audit trail from dashboard | `internal/dashboard/server.go` | Currently only accessible via CLI/pipe |
| Medium | Exponential backoff for dashboard client (currently retries every 10 polls flat) | `internal/svc/handler.go` | Better resilience when dashboard is temporarily unreachable |
| Medium | `drainctl configure` interactive UX polish — show current value next to each prompt | `cmd/drainctl/main.go` | Quality-of-life for operators |
| Low | SSPI `RequireGroup` — add explicit `RevertToSelf` in error paths | `internal/dashboard/sspi.go` | Defensive; thread token impersonation cleanup |
| Low | Config hot-reload: propagate new notification config to in-flight `NotifyState` | `internal/svc/handler.go` | Currently stale state survives until service restart |
| Low | `drainctl history` — add `--since` / `--until` flags for time-bounded queries | `cmd/drainctl/main.go` | Convenience for operators |

## Key Learnings

- **Zero tests existed** prior to this session. All Go files carry `//go:build windows`; test files need the same tag. The root package contains enough pure logic (config, notify, format) to be tested without syscall mocks.
- **Webhook signing pattern**: `X-DrainCtl-Signature: sha256=<hmac>` matches GitHub-style webhook signatures. Secret stored in `NotificationTarget.Secret`; empty secret = no header (backwards-compatible).
- **Health endpoint design**: Placed at `GET /api/v1/health` with no auth so load balancers and uptime monitors can probe without Kerberos. Returns `ok`, `version`, `servers`, `healthy`, `alerting`, `unknown` counts.
- **URL scheme validation**: Added to `Config.Validate` so it applies uniformly on every config load/save path. Empty URLs are preserved (existing behaviour handles them downstream).
- **`svc/handler.go` is large** (800+ lines) — split into sub-files would improve navigability but isn't blocking anything yet.
- **`readAll` in audit.go** loads the entire JSONL into memory on every operation — fine for 90-day retention at 300s poll intervals (~25 K records), but worth streaming if retention or frequency increases.
