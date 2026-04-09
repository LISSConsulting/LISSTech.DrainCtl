# Perfmon Hot-Reload Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix four bugs where perfmon collector lifecycle is not properly synchronized with config changes, causing stale data, missing data, and service crashes.

**Architecture:** Extract a `syncPerfCollector` helper in `handler.go` that encapsulates close-old / clear-cache / open-new / prime. Wire it into the config-reload path (replaces inline block) and the remote-config-fetch path (new call after `applyRemoteConfig`).

**Tech Stack:** Go, Windows PDH, named pipes

---

## File Map

| File | Action | Responsibility |
|------|--------|----------------|
| `internal/svc/handler.go` | Modify | Add `syncPerfCollector` helper; rewire config-reload and remote-config paths |
| `internal/svc/handler_test.go` | Modify | Add unit tests for `syncPerfCollector` |

No new files. No changes to `check.go`, `pipe.go`, dashboard, or CLI.

---

### Task 1: Add `syncPerfCollector` helper with tests

**Files:**
- Modify: `internal/svc/handler.go` (insert after line 542, after the `Execute` method closing brace)
- Modify: `internal/svc/handler_test.go` (append tests)

- [ ] **Step 1: Write the failing tests**

Add to `internal/svc/handler_test.go`:

```go
// ── syncPerfCollector ────────────────────────────────────────────────────────

func TestSyncPerfCollector_NoChangeIsNoop(t *testing.T) {
	cfg := dc.PerformanceConfig{Enabled: true, CPUWarnPct: 70}
	var collector *perfmon.Collector
	var triggerState *perfmon.PerfTriggerState
	var lastPerf atomic.Pointer[dc.PerfSnapshot]

	// Store a snapshot to verify it is NOT cleared on no-op.
	snap := &dc.PerfSnapshot{CPUPct: 42}
	lastPerf.Store(snap)

	changed := syncPerfCollector(cfg, cfg, &collector, &triggerState, &lastPerf, dc.DiscardLogger())
	if changed {
		t.Error("syncPerfCollector returned changed=true for identical configs")
	}
	if lastPerf.Load() != snap {
		t.Error("lastPerf was cleared despite no config change")
	}
}

func TestSyncPerfCollector_DisableClearsLastPerf(t *testing.T) {
	oldCfg := dc.PerformanceConfig{Enabled: true, CPUWarnPct: 70}
	newCfg := dc.PerformanceConfig{Enabled: false}
	var collector *perfmon.Collector
	var triggerState *perfmon.PerfTriggerState
	var lastPerf atomic.Pointer[dc.PerfSnapshot]

	// Simulate cached snapshot from when perfmon was enabled.
	lastPerf.Store(&dc.PerfSnapshot{CPUPct: 42})

	changed := syncPerfCollector(oldCfg, newCfg, &collector, &triggerState, &lastPerf, dc.DiscardLogger())
	if !changed {
		t.Error("syncPerfCollector returned changed=false when disabling perfmon")
	}
	if lastPerf.Load() != nil {
		t.Error("lastPerf not cleared after disabling perfmon")
	}
}

func TestSyncPerfCollector_ThresholdChangeClearsLastPerf(t *testing.T) {
	oldCfg := dc.PerformanceConfig{Enabled: true, CPUWarnPct: 70}
	newCfg := dc.PerformanceConfig{Enabled: true, CPUWarnPct: 80}
	var collector *perfmon.Collector
	var triggerState *perfmon.PerfTriggerState
	var lastPerf atomic.Pointer[dc.PerfSnapshot]

	lastPerf.Store(&dc.PerfSnapshot{CPUPct: 42})

	changed := syncPerfCollector(oldCfg, newCfg, &collector, &triggerState, &lastPerf, dc.DiscardLogger())
	if !changed {
		t.Error("syncPerfCollector returned changed=false when thresholds changed")
	}
	if lastPerf.Load() != nil {
		t.Error("lastPerf not cleared after threshold change")
	}
}
```

Add the `perfmon` import to the test file's import block:

```go
"sync/atomic"

dc "github.com/LISSConsulting/LISSTech.DrainCtl"
"github.com/LISSConsulting/LISSTech.DrainCtl/internal/perfmon"
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/svc/ -run TestSyncPerfCollector -v`
Expected: FAIL — `syncPerfCollector` is undefined.

- [ ] **Step 3: Implement `syncPerfCollector`**

Add to `internal/svc/handler.go` after the closing brace of `Execute` (after line 542):

```go
// syncPerfCollector reconciles the running performance collector with a config
// change. It tears down the old collector (if any), clears the cached snapshot,
// and starts a new collector when the new config enables perfmon.
// Returns true if the config actually changed (and action was taken).
func syncPerfCollector(
	oldPerf, newPerf dc.PerformanceConfig,
	perfCollector **perfmon.Collector,
	perfTriggerState **perfmon.PerfTriggerState,
	lastPerf *atomic.Pointer[dc.PerfSnapshot],
	log dc.LogFunc,
) bool {
	if oldPerf == newPerf {
		return false
	}

	// Tear down old collector.
	if *perfCollector != nil {
		(*perfCollector).Close()
		*perfCollector = nil
		*perfTriggerState = nil
	}

	// Always clear cached snapshot so stale data is never served.
	lastPerf.Store(nil)

	if newPerf.Enabled {
		pc, err := perfmon.Open(newPerf, log)
		if err != nil {
			dc.LogMsg(log, dc.LvlWRN, "perfmon start failed on config change", fmt.Sprintf("error=%q", err))
			return true
		}
		if err := pc.Prime(); err != nil {
			dc.LogMsg(log, dc.LvlWRN, "perfmon prime failed on config change", fmt.Sprintf("error=%q", err))
			pc.Close()
			return true
		}
		*perfCollector = pc
		*perfTriggerState = &perfmon.PerfTriggerState{}
		log(dc.LvlINF, "perfmon=restarted")
	} else {
		log(dc.LvlINF, "perfmon=stopped")
	}
	return true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/svc/ -run TestSyncPerfCollector -v`
Expected: PASS (3 tests). The tests exercise the no-op path, disable path, and threshold-change path. The enable path calls `perfmon.Open` which requires PDH (Windows-only); the nil-collector paths exercise the logic without PDH.

- [ ] **Step 5: Run all svc tests to verify no regressions**

Run: `go test ./internal/svc/ -v`
Expected: All existing tests still pass.

- [ ] **Step 6: Commit**

```bash
git add internal/svc/handler.go internal/svc/handler_test.go
git commit -m "feat(svc): add syncPerfCollector helper for perfmon lifecycle"
```

---

### Task 2: Wire helper into config-reload path

**Files:**
- Modify: `internal/svc/handler.go:505-528` (replace inline perfmon block)

- [ ] **Step 1: Replace inline perfmon block with helper call**

In `internal/svc/handler.go`, replace lines 505-528 (the entire `// Re-create performance collector if enabled state changed.` block):

```go
			// Re-create performance collector if enabled state changed.
			perfEnabledChanged := cfg.Performance.Enabled != oldPerfCfg.Enabled
			if perfEnabledChanged || (cfg.Performance.Enabled && cfg.Performance != oldPerfCfg) {
				if perfCollector != nil {
					perfCollector.Close()
					perfCollector = nil
					perfTriggerState = nil
				}
				if cfg.Performance.Enabled {
					pc, err := perfmon.Open(newCfg.Performance, s.log)
					if err != nil {
						dc.LogMsg(s.log, dc.LvlWRN, "perfmon restart failed on config reload", fmt.Sprintf("error=%q", err))
					} else {
						if err := pc.Prime(); err != nil {
							dc.LogMsg(s.log, dc.LvlWRN, "perfmon prime failed on config reload", fmt.Sprintf("error=%q", err))
							pc.Close()
						} else {
							perfCollector = pc
							perfTriggerState = &perfmon.PerfTriggerState{}
							s.log(dc.LvlINF, "perfmon=restarted")
						}
					}
				}
			}
```

With:

```go
			// Sync performance collector with new config.
			if syncPerfCollector(oldPerfCfg, cfg.Performance, &perfCollector, &perfTriggerState, &handler.lastPerf, s.log) {
				svcRunCheck(st, &cfg, notifyTargets, notifyState, &dashCfg, dashState, evtSub, perfCollector, perfTriggerState, &handler.lastPerf, &handler.lastSessions, s.log, s.elog)
			}
```

The `svcRunCheck` inside the `if` block ensures `lastPerf` is populated immediately when perfmon is enabled, or confirmed-nil when disabled. The boolean return from `syncPerfCollector` gates this so no extra check runs when config is unchanged.

- [ ] **Step 2: Verify build compiles**

Run: `go build ./internal/svc/`
Expected: No errors.

- [ ] **Step 3: Run all svc tests**

Run: `go test ./internal/svc/ -v`
Expected: All tests pass.

- [ ] **Step 4: Commit**

```bash
git add internal/svc/handler.go
git commit -m "refactor(svc): replace inline perfmon restart with syncPerfCollector in config-reload"
```

---

### Task 3: Wire helper into remote-config-fetch path

**Files:**
- Modify: `internal/svc/handler.go:442-446` (add oldPerf capture + helper call)

- [ ] **Step 1: Add perfmon sync after `applyRemoteConfig`**

In `internal/svc/handler.go`, replace lines 442-446 inside the `else` block of the dashboard config fetch (poll tick path):

```go
				} else {
					dashConfigFailures = 0
					useRemoteConfig = true
					applyRemoteConfig(cfgRemote, &cfg, &notifyTargets)
					handler.cfg.Store(&cfg) // sync updated GracePeriod/threshold to pipe handler
				}
```

With:

```go
				} else {
					dashConfigFailures = 0
					useRemoteConfig = true
					oldPerfCfg := cfg.Performance
					applyRemoteConfig(cfgRemote, &cfg, &notifyTargets)
					handler.cfg.Store(&cfg) // sync updated GracePeriod/threshold to pipe handler
					syncPerfCollector(oldPerfCfg, cfg.Performance, &perfCollector, &perfTriggerState, &handler.lastPerf, s.log)
				}
```

Note: no `svcRunCheck` call here because line 449 already calls it unconditionally right after this block.

- [ ] **Step 2: Verify build compiles**

Run: `go build ./internal/svc/`
Expected: No errors.

- [ ] **Step 3: Run all svc tests**

Run: `go test ./internal/svc/ -v`
Expected: All tests pass.

- [ ] **Step 4: Run full project lint**

Run: `just lint`
Expected: Clean — no vet/fmt/lint issues.

- [ ] **Step 5: Commit**

```bash
git add internal/svc/handler.go
git commit -m "fix(svc): sync perfmon collector when dashboard pushes config changes"
```

---

### Task 4: Manual verification checklist

This task is for the operator to verify on a live system. Not automatable in CI.

- [ ] **Step 1: Verify stale-data fix (Bug 1)**

On a test server with the service running:
1. Enable perfmon in config.json (`performance.enabled = true`), save
2. Wait for `config=reloaded` + `perfmon=restarted` in log
3. Run `drainctl check` — should show cpu/mem/disk lines
4. Disable perfmon in config.json (`performance.enabled = false`), save
5. Wait for `config=reloaded` + `perfmon=stopped` in log
6. Run `drainctl check` — should NOT show cpu/mem/disk lines

- [ ] **Step 2: Verify immediate-check fix (Bug 2)**

1. With perfmon disabled, enable it in config.json, save
2. Immediately run `drainctl check` (within seconds)
3. Should show fresh perf data (not stale or missing)

- [ ] **Step 3: Verify dashboard propagation (Bug 3)**

On the dashboard server:
1. Enable perfmon via dashboard API or config
2. On a connected server, wait for next config fetch (up to 5 min)
3. Verify `perfmon=restarted` appears in connected server's log
4. Run `drainctl check` on connected server — should show perf data
5. On connected server with `force_disabled = true`, verify perfmon stays off

- [ ] **Step 4: Verify crash fix (Bug 4)**

1. On a connected server (e.g., CST-ISLAB-DC1), start the service
2. Toggle perfmon on/off on the dashboard multiple times
3. Verify no crash — service stays running through config fetches
4. Check Event Viewer — no Event 7034 (unexpected termination)
