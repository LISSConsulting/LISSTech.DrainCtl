# Codex review synthesis — leak & perf audit (2026-04-26)

**Scope:** Codebase-wide audit for memory leaks, memory optimization, handle leaks, CPU leaks, CPU optimization. Out of scope: the `internal/dashboard/sspi.go` LSASS leak fixed in commit `94c9e27`.

**Method:** Three parallel codex `exec` invocations under separate lenses (Win32 handles, goroutine/ticker/CPU spin, allocation/SQLite). Every claim verified against live source by Opus before inclusion.

---

## Confirmed issues, ranked by blast radius × likelihood

### B1 — Per-connection goroutine leak in named-pipe accept (HIGH)

`internal/pipe/conn_windows.go:97` launches `go func() { <-ctx.Done(); SetEvent(cancelEvent) }()` on every accept. On a successful connection the function returns without any way to stop or join that goroutine, so it stays parked on `<-ctx.Done()` for the rest of the service lifetime. Verified: the goroutine is unguarded; `cancelEvent` is closed via defer when `acceptPipeConn` returns, so when ctx eventually fires the goroutine calls SetEvent on a closed handle (benign error, but still wakes only at shutdown).

`internal/pipe/pipe.go:82` calls `acceptPipeConn` once per connection in a `for { … }` server loop, so leak rate = pipe connection rate. CLI invocations and DLL P/Invoke calls hit this path; on hosts with frequent CLI/DLL traffic the goroutine count grows linearly until restart.

**Fix direction:** wrap the cancel-helper goroutine in its own context that's cancelled when accept returns (success or failure), or restructure so the helper is reused across accepts.

### C1 — Reader connection pool unbounded (HIGH)

`internal/telemetry/db.go:174` (`Open`) and `:278` (`OpenReadOnly`) create the reader DB with only `SetConnMaxLifetime` set — no `SetMaxOpenConns`, `SetMaxIdleConns`, or `SetConnMaxIdleTime`. Under concurrent dashboard reads, `database/sql` will open arbitrarily many `modernc.org/sqlite` connections; with pure-Go SQLite each connection carries its own page cache and statement state. This is a real 24/7 heap-and-fd risk on a busy dashboard host.

**Fix direction:** set `SetMaxOpenConns(N)` and `SetMaxIdleConns(N)` to a sane bound (likely 4–8 for read concurrency).

### C2 — Per-heartbeat JSON marshal/unmarshal/remarshal + DB round-trip (MEDIUM-HIGH)

Every agent heartbeat triggers:

1. `state.Update` (`internal/dashboard/server.go:592`) → `json.Marshal(result)` (`internal/dashboard/store.go:114`) → DB write.
2. `OnUpdate` callback fires → `broadcastServerUpdate` (`server.go:2168`) → `state.Get(host)` → DB read → `json.Unmarshal(LastResultJSON)` (`store.go:226`).
3. Re-marshal into SSE envelope (`server.go:2174`).

Per heartbeat, per registered host: **2 marshals + 1 unmarshal + 1 DB read** of data we just wrote. Verified the chain end-to-end.

**Fix direction:** thread the freshly-marshalled payload (or the in-memory `*CheckResult`) directly into `OnUpdate` so `broadcastServerUpdate` doesn't round-trip the DB. Either pass `(host, payload)` to OnUpdate, or have ServerState keep an in-memory cache of the last `*CheckResult` per host that `Get` consults before falling back to the JSON column.

### C3 — Metrics insert prepared per Append (MEDIUM)

`internal/telemetry/metrics.go:95` calls `tx.PrepareContext` on a constant SQL string inside every `Append`. Heartbeats arrive at the configured poll interval so this is a hot path. Each prepare allocates a statement object and re-parses SQL.

**Fix direction:** prepare the insert once at `MetricsStore` construction against the writer DB. Use `tx.StmtContext(stmt)` inside `Append` to bind the cached statement to the per-call transaction.

### A1 — ETW provider handle never unregistered (LOW)

`internal/logging/etw.go:168` calls `EventRegister` and stores the handle. `etw.go:187` defines `Close()` that calls `EventUnregister`. `internal/svc/handler.go:1091, 1098, 1104` constructs `ETWHandler`, stores it on `drainService.etw`, but `Close()` is never invoked anywhere. Verified via grep: no callers.

Process-lifetime leak only (one handle, fires once at exit), so impact is minimal in practice — but the `Close()` exists, is documented, and isn't wired. Easy correctness fix.

**Fix direction:** call `etwH.Close()` from a deferred handler around `svc.Run`, or implement `(*drainService) shutdown()` that closes it during Stop/Shutdown.

### B2 — Fire-and-forget `go dashboard.ReportSpike` (MEDIUM)

`internal/svc/handler.go:792` launches `go dashboard.ReportSpike(...)` per spike with no ctx propagation and no waitgroup tracking. Service shutdown cancels the parent ctx and waits for telemetry/evtspike workers, but never these. Each goroutine can stay in SSPI+HTTP for up to the dashboard client's internal timeout.

`ReportSpike` itself uses `negotiateRequest` with no caller ctx, only its own internal timeout. Verified.

**Fix direction:** track these goroutines in the existing service `wg`; pass a ctx with the dashboard-client timeout so shutdown bounds the wait. Alternatively, drop them into a small bounded worker pool.

### C6 — `checkResultSamples` slice grown without preallocation (LOW)

`internal/dashboard/server.go:2394` declares `var out []telemetry.Sample` then `append`s ~25–30 fixed-shape entries. Element count is bounded by the payload schema. Forces ~6 backing-array reallocations per heartbeat per host.

**Fix direction:** `out := make([]telemetry.Sample, 0, 32)`. One line.

---

## Partial / lower-priority

### C4 — `HandleHistory` N+1 metric lookup (PARTIAL)

`internal/svc/handler.go:228` calls `NearestCounters` once per audit row; `internal/telemetry/metrics.go:731` allocates a fresh `map[string]float64` per row. Real N+1, but on-demand history endpoint with tight `limit` bounds — not the heartbeat hot path. Real, but a refactor risk that's not worth tackling alongside the heartbeat-path fixes.

**Fix direction (deferred):** batch into one query with per-row tolerance windows; return slice instead of map.

### C5 — Sparkline seed endpoint per-host fan-out (PARTIAL)

`internal/dashboard/server.go:1898` issues per-host `QueryRange` in a loop with multiple per-host map allocations. On-demand cold-load endpoint, not a recurring leak. Could batch but not urgent.

**Fix direction (deferred):** single query with `WHERE host IN (...)` then partition in Go.

---

## Rejected / not flagged

- **WTS, registry, EvtSubscribe, named-pipe, DPAPI handle pairings** — codex audited and explicitly cleared these. Verified: WTSFreeMemory, EvtClose, RegCloseKey, token.Close, sigEvent CloseHandle are all paired on every return path.
- **Aggregator / evtspike unbounded growth** — codex explicitly cleared. The aggregator uses watermarks; evtspike baseline file is bounded by configured channel set.
- **Tickers without Stop, busy loops, channel starvation** — codex explicitly cleared.
- **`Rows`/`Stmt` close discipline outside metrics.Append** — codex explicitly cleared.
- **WAL / busy_timeout / synchronous PRAGMAs** — codex confirmed they are set explicitly.

These cleared areas were the highest-prior risk surfaces, so the negative findings are themselves valuable.
