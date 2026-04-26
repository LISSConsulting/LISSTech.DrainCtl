# drainctl Leak/Perf Remediation Plan — 2026-04-26

Sequenced from the synthesis at `docs/reviews/codex-2026-04-26-leak-perf-synthesis.md`.

## Ordering Rationale

Independent, single-file, low-risk fixes ship first to bank wins and de-risk the branch. The two trickier ones — C2 (cross-package signature/cache change) and C3 (statement lifecycle on modernc.org/sqlite) — go last so reviewers can focus. Within "easy" tier, A1 first because it is literally one line and proves the pipeline.

---

## Step 1 — Unregister ETW provider handle on service exit (A1)

- **Files**: `internal/svc/handler.go:1091-1110` (the `etwH := logging.NewETWHandler(...)` block and the `svc.Run` ladder); `internal/logging/etw.go:187` (existing Close — no change expected).
- **Change**: Add `defer etwH.Close()` immediately after construction at line 1091, before the conditional that may early-return into `svc.Run`. If `fileH` (file sink in `NewMultiHandler`) exposes a Close, defer that too; if not, leave a TODO comment referencing this audit.
- **Verification**: Unit test `TestETWHandlerClose` in `internal/logging/etw_test.go` asserting (a) after `Close()`, `regHandle.Load() == 0` (the swap-to-zero is the post-condition we care about — this directly proves the unregister path ran), and (b) a second `Close()` does not panic. The state-after-swap is the right oracle here; `logman query providers` and `wevtutil gp` only inspect the system-wide manifest registration and would pass even if our process never called EventUnregister, so they're inadequate manual checks. `go test ./internal/logging/...` + `just lint`.
- **Effort**: S
- **Risk**: low. Only regression would be Close ordering vs. final log flush — keep defer order so file/ETW close after the last slog write.

## Step 2 — Preallocate `checkResultSamples` slice (C6)

- **Files**: `internal/dashboard/server.go:2394` and the surrounding `add(...)` call sites in the same function.
- **Change**: Replace `var out []telemetry.Sample` with `out := make([]telemetry.Sample, 0, N)` where N is the static count of `add(...)` sites including optional session/RFX branches (audit suggests 32 as a safe upper bound; pick the exact count after reading the body).
- **Verification**: `go test ./internal/dashboard/...`. No new test needed — existing checkResult tests cover correctness. `just lint`. Optional: a microbench `BenchmarkCheckResultSamples` to record before/after allocs/op.
- **Effort**: S
- **Risk**: low.

## Step 3 — Bound reader connection pools (C1)

- **Files**: `internal/telemetry/db.go:174` (`Open` reader), `:278` (`OpenReadOnly` reader), and `:220` area (DB methods — add a Stats accessor).
- **Change** (two coupled edits):
  1. After each reader `sql.OpenDB`, call `reader.SetMaxOpenConns(4)` and `reader.SetMaxIdleConns(4)`. Comment cites this audit; we can tune via config later.
  2. Add `func (db *DB) ReaderStats() sql.DBStats { return db.reader.Stats() }` so tests can observe pool state without touching the unexported `reader` field. The wrapping `*telemetry.DB` deliberately doesn't expose `*sql.DB`; this small accessor is needed by the verification.
- **Verification**: New test `TestReaderPoolBounded` in `internal/telemetry/db_test.go` (existing test file in the package) opening Open and OpenReadOnly and asserting `db.ReaderStats().MaxOpenConnections == 4`. Run a concurrent-read stress test (50 goroutines doing simple SELECTs) and assert `db.ReaderStats().OpenConnections <= 4`. `go test ./internal/telemetry/...`. `just lint`.
- **Effort**: S
- **Risk**: low-med. Under-sizing could serialize dashboard reads; 4 matches current observed concurrency. Document the knob. ReaderStats accessor is additive, no API break.

## Step 4 — Track `ReportSpike` goroutines and propagate ctx (B2)

- **Files**: `internal/svc/handler.go:792` (the `go dashboard.ReportSpike(...)` call site) and `:483-486` / `:766` (where `telemetryWG` is declared and waited via `waitTelemetryWorkers(&telemetryWG, 10*time.Second)`); `internal/dashboard/client.go:138` (`ReportSpike` signature) and `:188-225` (`negotiateRequest` — currently uses `http.NewRequest`, no caller ctx).
- **Change** (three coupled edits, all required):
  1. Change `negotiateRequest(method, rawURL string, body []byte)` → `negotiateRequest(ctx context.Context, method, rawURL string, body []byte)` and replace both `http.NewRequest` sites at `client.go:195` and `:225` with `http.NewRequestWithContext`. Update every caller in `internal/dashboard/client.go` (ReportSpike, ReportState, the handshake helpers, the DELETE at :392).
  2. Change `ReportSpike(dashboardURL string, spike *dc.SpikePayload)` → `ReportSpike(ctx context.Context, dashboardURL string, spike *dc.SpikePayload)`.
  3. At the spike-launch site (`handler.go:792`), reuse the existing `telemetryWG`: `telemetryWG.Add(1); go func() { defer telemetryWG.Done(); dashboard.ReportSpike(ctx, dashCfg.URL, &spikeCopy) }()`. The existing `waitTelemetryWorkers(&telemetryWG, 10*time.Second)` at `:766` already bounds the shutdown wait at 10 s — that's the bound we want.
- **Verification**:
  - `internal/dashboard/client_windows_test.go` adds `TestReportSpikeContextCancel`: spin up an `httptest.Server` whose handler blocks on `<-r.Context().Done()`, call `ReportSpike` with a context cancelled after 50 ms, assert the call returns within 200 ms total. (Without the ctx plumb-through, the test would hang on the default Go HTTP timeout — proves the fix is actually wired.)
  - **Production-code seam (required for the verification below):** introduce a package-level function var in `internal/svc/handler.go` (or a small new file) — `var reportSpike = dashboard.ReportSpike` — and switch the call site at `:792` to use it. Tests can then swap `reportSpike` for a hook that blocks forever. This matches the existing test-seam pattern in `internal/evtspike/subscriber_windows.go` (`evtSubscribeCall`, `evtNextCall`, etc.) and the just-shipped `internal/dashboard/sspi.go` (`acquireServerCredentials`). Without this seam the verification below cannot be written.
  - `internal/svc/handler_test.go` adds `TestSpikeReportTracksWaitGroup`: swap the `reportSpike` package var to a hook that **blocks on the passed ctx (`<-ctx.Done()` then return)** — NOT one that blocks forever; a forever-blocking hook would never call `wg.Done()` regardless of correctness, so the test would hang. Fire a spike (the goroutine takes wg.Add and enters the hook, parking on ctx), cancel ctx, then assert `telemetryWG.Wait()` returns within ~200 ms via a parameterized helper. This proves both ctx propagation (the hook unblocks on cancel) and waitgroup tracking (Wait sees Done).
  - `go test ./internal/dashboard/... ./internal/svc/...`; `just lint`. Grep for any remaining `http.NewRequest(` outside test files in `internal/dashboard/` to confirm all callers migrated.
- **Effort**: M
- **Risk**: med. Signature changes to `negotiateRequest` and `ReportSpike` — internal package, no public-API break. Risk: forgetting one `negotiateRequest` caller would compile-fail in CI (good — fail-loud). Risk that the `r.Context()` server-side detection in the test depends on httptest semantics — verify the test fails before the fix is applied.

## Step 5 — Fix per-connection goroutine leak in named-pipe accept (B1)

- **Files**: `internal/pipe/conn_windows.go:97` and the helper-goroutine block above it that does `go func() { <-ctx.Done(); SetEvent(cancelEvent) }()`.
- **Change**: Derive a per-call `acceptCtx, cancelAccept := context.WithCancel(ctx)` and use `acceptCtx` as the helper goroutine's wait target (`<-acceptCtx.Done()`). Defer order at function return: `defer cancelAccept()` declared **first** (so it runs **last** — see Go's LIFO defer order), `defer windows.CloseHandle(cancelEvent)` declared **after** it. Wait — that's wrong: we want `cancelAccept()` to run **first** at function return so the helper wakes and exits before `cancelEvent` is closed. So declare `defer windows.CloseHandle(cancelEvent)` **before** `defer cancelAccept()`. Walk the defer order in implementation — the comment in code must call this out explicitly.
- **Verification**: Replace the proposed `runtime.NumGoroutine()` count (flaky on Windows due to background goroutines) with a deterministic counter. Add an unexported package-level `var helperGoroutineCount atomic.Int64` (or expose via a test-only build tag) that the helper goroutine increments on entry and decrements on exit. Test `TestAcceptPipeConnHelperExits` in `internal/pipe/conn_windows_test.go` performs N successful accept cycles and then asserts `helperGoroutineCount.Load() == 0` using an **eventually-with-timeout** poll, NOT a fixed sleep — e.g., a small helper `waitFor(t, 1*time.Second, func() bool { return helperGoroutineCount.Load() == 0 })` that polls every 5 ms and fails on timeout. A fixed sleep would still flake on a busy CI runner. Also add `TestAcceptPipeConnHelperExitsOnError` covering the early-return paths (CreateEvent failure, ConnectNamedPipe error, cancellation), each using the same poll helper. `go test ./internal/pipe/...`; `just lint`.
- **Effort**: M
- **Risk**: med. Pipe accept is in the hot path for every CLI invocation and DLL P/Invoke; defer ordering mistake would either re-introduce the leak or fire SetEvent on a closed handle. Documented explicitly in the code comment.

## Step 6 — Cache prepared statement in `MetricsStore.Append` (C3)

- **Files**: `internal/telemetry/metrics.go:79` (NewMetricsStore) and `:85-102` (Append); `internal/svc/handler.go:439` (NewMetricsStore call site) and `:415-ish` (DB-close site — find the actual telDB.Close site and add metricsStore.Close before it); any tests that call `telemetry.NewMetricsStore(...)`.
- **Change** (five coupled edits, all required):
  1. Change `NewMetricsStore(db *DB) *MetricsStore` → `NewMetricsStore(ctx context.Context, db *DB) (*MetricsStore, error)`. **Guard `db.writer == nil` first** and return a sentinel `errReadOnlyDB` (or wrap an existing one) — `OpenReadOnly` returns a `*DB` with only `reader` populated, and although history.go uses `NewReadOnlyAuditStore` (not NewMetricsStore) today, the constructor's contract should be self-defending. Then call `db.writer.PrepareContext(ctx, insertSQL)` and store the `*sql.Stmt` on the struct as `appendStmt`. Return error on prepare failure.
  2. Add a `sync.RWMutex` (`stmtMu`) to MetricsStore. Append takes `RLock` for the duration of the StmtContext call; Close takes `Lock`. This is the required synchronization between Append-in-flight and Close — a nil-check is not synchronization (atomics fix visibility but not the use-after-close race; the StmtContext call holds a reference into the cached stmt, so we need mutual exclusion until the stmt is no longer touched). Set `appendStmt = nil` only while holding the write lock.
  3. Add `(s *MetricsStore) Close() error` that takes `stmtMu.Lock()`, closes `s.appendStmt` if non-nil, sets it nil, returns. Idempotent.
  4. In `Append`, take `s.stmtMu.RLock()`, check `s.appendStmt == nil` (return `errStoreClosed` if so), use `tx.StmtContext(ctx, s.appendStmt)` to get the per-tx wrapper, do the inserts, release the RLock at the end of the function. Drop the `defer stmt.Close()` on the tx-scoped wrapper (per `database/sql` docs the wrapper from `Tx.StmtContext` doesn't own the underlying stmt).
  5. Update `internal/svc/handler.go:439` and the two test sites (`internal/dashboard/server_test.go:2745, 2939`) and `internal/svc/handler_test.go:133` to handle the new error return. Ensure `metricsStore.Close()` is invoked **before** `telDB.Close()` on every shutdown path. `defer` immediately after `metricsStore, err := telemetry.NewMetricsStore(ctx, telDB)`.
- **Verification**:
  - `TestNewMetricsStoreFailsOnClosedDB`: closed DB → constructor returns error.
  - `TestNewMetricsStoreFailsOnReadOnlyDB`: pass a DB returned from `OpenReadOnly` (writer nil) → constructor returns `errReadOnlyDB`. Proves the nil-writer guard.
  - `TestMetricsStoreAppendReusesStmt`: introduce a counting wrapper around `db.writer.PrepareContext` (via test seam — package-level function var, matching the pattern in `internal/evtspike/subscriber_windows.go` and the just-fixed `internal/dashboard/sspi.go`). Call `Append` 100× and assert the writer's PrepareContext was invoked exactly once for the lifetime of the store. **This is the load-bearing verification — not the benchmark.** A `BenchmarkMetricsStoreAppend` ns/op comparison would be dominated by `BeginTx`/`Commit` cost on WAL and would not isolate the prepare savings.
  - `TestMetricsStoreCloseDuringAppendIsSafe`: needs a deterministic interleave, not a tight-loop race. Add a test seam — a package-level function var hooked inside `Append` between RLock acquisition and StmtContext use, e.g., `var appendHook = func() {}` called once mid-Append. The test sets `appendHook` to block on a channel, launches one Append goroutine (which takes RLock, then blocks in the hook), then calls `Close` from the test goroutine and asserts (a) Close blocks until the hook returns (proving it waited for the inflight Append's RLock release), and (b) the next Append after Close returns `errStoreClosed`. Run with `-race`. This is the test that proves the RWMutex synchronization. The "tight loop + short delay" approach can pass without ever overlapping Close with an in-flight Append, so it doesn't actually verify the lock handoff.
  - `TestMetricsStoreAppendAfterCloseFails`: Close, then Append should return `errStoreClosed`.
  - Run existing telemetry suite under `-race`: `go test -race ./internal/telemetry/...`. `just lint`.
- **Effort**: M
- **Risk**: med. Constructor signature change requires updating every caller — grep for `telemetry.NewMetricsStore(` (only 4 sites in the live tree per audit: handler.go, handler_test.go, two in server_test.go). Stmt lifetime now exceeds individual tx; modernc.org/sqlite supports `tx.StmtContext` reuse in WAL mode but verify under `-race`. Shutdown ordering is critical: `metricsStore.Close` must complete before `telDB.Close` to avoid "statement is closed" on inflight Append. RWMutex (not a nil-check) provides the close-during-Append safety.

## Step 7 — Skip DB round-trip in heartbeat broadcast (C2)

- **Files**: `internal/dashboard/server.go:592` (`state.Update` call), `internal/dashboard/store.go:111-138` (Update marshal+OnUpdate), `:142-159` (Get + toDashboardServerInfo), `internal/dashboard/server.go:2168-2185` (broadcastServerUpdate re-marshal).
- **Change**: Cache an **immutable, full** `ServerInfo` value (NOT just `LastResultJSON` bytes — `toServerView` at `server.go:91` reads `info.RegisteredAt`, `info.LastSeen`, and uses `time.Since(info.LastSeen) > staleThreshold`, none of which are inside `LastResultJSON`). Add to `ServerState` a `sync.Map` keyed by hostname holding `*ServerInfo` (treated as immutable post-construction — never mutated after Store, only replaced). `Update` populates the cache **after** the DB commit succeeds (not before — a failed write must not poison the cache) by constructing a fresh `ServerInfo{Hostname, RegisteredAt, LastSeen, LastResult: result}` from values it already has, plus the registered_at fetched once at first Update (or read from the same DB write). On `Remove`, delete from the cache. Add a new method `ServerState.GetCached(host) *ServerInfo` that reads the cache and returns nil on miss (does NOT fall back to DB). Modify `broadcastServerUpdate` at `server.go:2168` to call `GetCached` first; on hit, call `toServerView` on the cached info; on miss, fall back to the existing `Get` path. Keep the JSON column as source of truth on cold start — cache is purely an optimization. Eliminates 1 unmarshal + 1 marshal + 1 DB read per heartbeat per host. `toServerView`'s stale-status computation still runs at broadcast time (it's based on `time.Since(info.LastSeen)`, not on cached state), so the cache freshness story is preserved.
  - Pointer-cache (`*CheckResult` directly) was the wrong shape: it shares mutable caller-owned state across goroutines and would create races in the SSE path. Bytes-only cache (LastResultJSON) was insufficient because toServerView needs RegisteredAt + LastSeen too. The full immutable ServerInfo struct, replaced atomically per Update, addresses both — **but only if the embedded `LastResult` is a deep copy of the caller's `*CheckResult`, not the same pointer.** Wrapping the caller's pointer inside a struct does not make it immutable. Fix: in `Update`, before storing into the cache, build `cached := *result` (struct value copy) and use `LastResult: &cached`. The struct copy is cheap (~few hundred bytes for `CheckResult`) and breaks aliasing with the caller. Note: `*dc.PerfSnapshot` and other pointer fields inside `CheckResult` are themselves not deep-copied by `*result`; if any caller mutates those after Update, we still race. Audit the heartbeat path (`internal/svc/handler.go` around `:592` callers) to confirm the caller does not retain `*result` after passing it to `Update` — if confirmed, document the contract in `Update`'s godoc; if not, deep-copy the inner pointer fields too.
- **Verification**:
  - Add a test seam to `ServerState`: a counting wrapper around the underlying `store.Get` invocation, exposed via an unexported method or a `storeGetCalls atomic.Int64` field that the production code increments on each store.Get call. Tests read it.
  - `TestBroadcastServerUpdateUsesCache`: register a host, call Update, then call `broadcastServerUpdate` directly; assert `storeGetCalls` did not increment between Update and the broadcast — proves the SSE hot-path skips the DB. (The previously proposed "Get-after-Update returns cached" was the wrong test: Get is not on the SSE path; broadcastServerUpdate is.)
  - `TestUpdateDoesNotPoisonCacheOnDBError`: inject a store stub that returns error from Update; assert cache state — if not previously populated, miss; if previously populated, the prior cached value remains (not overwritten with stale data, not replaced with a partial value).
  - `TestRemoveClearsCache`: Update, Remove, then assert `GetCached` returns nil and broadcastServerUpdate falls back to store.Get (visible via `storeGetCalls` increment).
  - `TestCachedServerInfoIsImmutable`: Update once, save the cached pointer, Update a second time, assert the first pointer's fields are unchanged (proves we replace, not mutate).
  - `go test -race ./internal/dashboard/...` to catch any cache/store data race. Manual: run service for 5 min under load, observe selfmetrics — total allocs/sec on the heartbeat path should drop. `just lint`.
- **Effort**: L
- **Risk**: med-high. Cache coherency between writer and reader paths; ensure Update writes cache **after** the DB commit returns success. Verify Remove invalidates. Race-test thoroughly. (Earlier drafts of this plan considered caching just `LastResultJSON` bytes — that path is rejected because `toServerView` reads `RegisteredAt`/`LastSeen` which aren't in `LastResultJSON`. The cache must hold a full `ServerInfo`.)

---

## Explicitly Deferred

- **C4** (handler.go:228 HandleHistory N+1) — on-demand endpoint with tight `limit`, not on heartbeat hot path. Track in backlog.
- **C5** (server.go:1898 sparkline seed fan-out) — cold-load endpoint, runs once per dashboard session. Track in backlog.

Both should get GitHub issues referencing this audit so they aren't lost.

---

## Rollout

**Recommendation: split into two PRs from `develop`.**

- **PR #1 — "leak/perf: low-risk cleanups"**: Steps 1, 2, 3, 4, 5 as five separate commits. Each commit is independently revertable. Net effect is observable (goroutine count flat, ETW handle released, bounded reader pool, bounded shutdown) with low review burden.
- **PR #2 — "leak/perf: heartbeat hot-path"**: Steps 6 and 7 as two commits. These touch shared telemetry/dashboard state and warrant focused review plus a soak run on a staging host before merge to `develop`. Land PR #1 first, let it bake a day, then open PR #2.

Single PR with seven commits would also be acceptable if reviewer bandwidth is tight, but the hot-path changes deserve a separate soak window. Per project convention, every step is its own commit on `develop`; trunk gets the merge from `develop` after both PRs land.
