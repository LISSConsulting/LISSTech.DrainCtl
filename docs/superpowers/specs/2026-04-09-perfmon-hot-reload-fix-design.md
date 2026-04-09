# Perfmon Hot-Reload & Dashboard Propagation Fix

**Date:** 2026-04-09
**Status:** Approved

## Problem

Four related bugs in performance monitoring lifecycle management:

1. **Stale `lastPerf` after disable** — When perfmon is disabled via config reload, the collector is closed but `handler.lastPerf` is never cleared. `HandleStatus()` returns frozen data. CLI shows identical CPU/memory values indefinitely.

2. **No immediate check after config reload** — The `configCh` handler creates/destroys the collector but never calls `svcRunCheck`. Dashboard and CLI see stale data until the next poll tick.

3. **Remote config doesn't restart collector** — `applyRemoteConfig` (check.go:305-308) correctly updates `cfg.Performance` from the dashboard, but the `perfCollector` local variable is never restarted to match. The collector and config fall out of sync.

4. **Crash on connected server config fetch** — On CST-ISLAB-DC1, the service crashes (Event 7034, "terminated unexpectedly") when receiving a dashboard config update that changes perfmon settings. The collector/config mismatch from bug 3 causes a panic with no `recover()` in the service loop.

## Approach

Extract the perfmon lifecycle (close old, clear cache, open new, prime) into a single `syncPerfCollector` helper function. Wire it into both the config-reload and remote-config-fetch paths.

Alternatives considered:
- **Move perfCollector to atomic.Pointer on serviceHandler** — Cleaner long-term but larger refactor with more risk. Rejected in favor of minimal change.

## Design

### New helper: `syncPerfCollector`

Location: `internal/svc/handler.go`

```go
func syncPerfCollector(
    oldPerf, newPerf dc.PerformanceConfig,
    perfCollector **perfmon.Collector,
    perfTriggerState **perfmon.PerfTriggerState,
    lastPerf *atomic.Pointer[dc.PerfSnapshot],
    log dc.LogFunc,
) {
    if oldPerf == newPerf {
        return
    }
    if *perfCollector != nil {
        (*perfCollector).Close()
        *perfCollector = nil
        *perfTriggerState = nil
    }
    lastPerf.Store(nil)
    if newPerf.Enabled {
        pc, err := perfmon.Open(newPerf, log)
        // ... error handling, Prime(), assign on success
    } else {
        log(dc.LvlINF, "perfmon=stopped")
    }
}
```

Key behaviors:
- Early return if config unchanged (cheap struct comparison since all fields are bool/int)
- Always clears `lastPerf` on any change to prevent stale data
- Tears down before building up to prevent resource leaks
- Logs `perfmon=restarted` or `perfmon=stopped` for observability

### Config-reload path (handler.go configCh case, ~line 505)

Replace the inline perfmon restart block (lines 505-528) with:

```go
syncPerfCollector(oldPerfCfg, cfg.Performance, &perfCollector, &perfTriggerState, &handler.lastPerf, s.log)
```

The `oldPerfCfg` capture at line 461 already exists. Add a `svcRunCheck` call after the helper **only when perfmon config actually changed** (i.e., `syncPerfCollector` didn't early-return), so `lastPerf` is populated immediately rather than waiting for the next poll tick.

### Remote config path (handler.go poll tick, ~line 445)

Capture old perf config before `applyRemoteConfig`, call helper after:

```go
oldPerfCfg := cfg.Performance
applyRemoteConfig(cfgRemote, &cfg, &notifyTargets)
handler.cfg.Store(&cfg)
syncPerfCollector(oldPerfCfg, cfg.Performance, &perfCollector, &perfTriggerState, &handler.lastPerf, s.log)
```

`svcRunCheck` already runs at line 449 — no additional call needed.

### Not changed

- **Startup path** (handler.go:278-292) — Works correctly today, no benefit to unifying with the helper.
- **`applyRemoteConfig`** — Correctly updates `cfg.Performance`; just needs collector sync after.
- **Pipe/CLI code** — Stale data was a source problem, not a consumer problem.
- **Dashboard GET/PUT endpoints** — Already return/accept performance config correctly.
- **`check.go`** — No changes needed; it correctly uses `perfCollector != nil` guard.

## Files Changed

| File | Change |
|------|--------|
| `internal/svc/handler.go` | Add `syncPerfCollector` helper; replace inline block in configCh; add call after `applyRemoteConfig`; add `svcRunCheck` after config-reload perfmon change |

## Bug-to-Fix Mapping

| Bug | Fix |
|-----|-----|
| Stale `lastPerf` after disable | Helper calls `lastPerf.Store(nil)` |
| No immediate check after config reload | `svcRunCheck` call added in configCh case |
| Remote config doesn't restart collector | `syncPerfCollector` called after `applyRemoteConfig` |
| Crash on DC1 config fetch | Same — collector/config mismatch eliminated |
