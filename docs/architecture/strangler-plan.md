# Strangler Plan (STP) — migrating to the Lifecycle Interface

**Status:** living document. Updated as migrations land. Owned by whoever ships the next migration.

The STP is how we get from today's 1k+ line `internal/svc/handler.go:Execute` to a small `Execute` that delegates to a list of `Subsystem`s. The principle is **strangler-fig with a discipline ratchet**: every PR that adds a new feature also migrates one existing subsystem to the LCI. Big-bang refactors are out — they don't ship and they don't get reviewed.

See [lifecycle.md](./lifecycle.md) for the interface itself.

## The ratchet rule

> **Every PR that introduces a new long-running subsystem to the service ships, in the same PR, the migration of one existing subsystem to LCI.**

If a PR has nowhere reasonable to do that (hot fix, doc-only change, dependency bump), the next non-trivial PR pays the debt. The PR template carries a checkbox; reviewers check it. Two skips in a row require a maintainer signoff with a reason in the PR description.

The point of the rule is not bureaucracy. It's that `Execute` only ever shrinks; it never grows. The existing god-function is a one-way street toward zero.

## End state

`internal/svc/handler.go:Execute` becomes roughly:

```go
func (s *drainService) Execute(args []string, r <-chan svc.ChangeRequest, statusCh chan<- svc.Status) (bool, uint32) {
    ctx, cancel := context.WithCancel(s.rootCtx)
    defer cancel()

    for _, sub := range s.subsystems {
        if err := sub.Start(ctx); err != nil {
            slog.Error("service: subsystem start failed", "name", sub.Name(), "error", err)
            return false, 1
        }
    }

    statusCh <- svc.Status{State: svc.Running, Accepts: ...}

    // Drive control-plane events: SCM commands, config reload, dashboard
    // bootstrap. Subsystem internal work runs on its own goroutines.
    for {
        select {
        case c := <-r:
            switch c.Cmd {
            case svc.Stop, svc.Shutdown:
                statusCh <- svc.Status{State: svc.StopPending, WaitHint: 10000}
                cancel()
                for _, sub := range s.subsystems {
                    sub.Stop()
                }
                return false, 0
            case svc.Interrogate:
                statusCh <- c.CurrentStatus
            }
        case <-s.configChangedCh:
            s.handleConfigReload(ctx) // dispatches to subsystems that opt-in
        }
    }
}
```

This is ~30 lines. Whatever's currently in Execute that doesn't fit this shape is what each migration step extracts.

`drainService.subsystems` is a `[]lifecycle.Subsystem` constructed at startup (in some `assemble` function). The order matters: dependencies start before dependents, stop in reverse.

## Migration order

Ranked by how close each is to LCI shape today (closest first), with a one-line note on what blocks each.

1. **`internal/evtspike`** — already implements `Start(ctx) error / Stop() / Status()`. The migration is: declare the interface in `internal/lifecycle/`, add a compile-time assertion `var _ lifecycle.Subsystem = (*evtspike.Subsystem)(nil)`. **Effort: S. This is the demonstration migration.**

2. **`internal/dashboard`** — `DashboardServer` has a clear lifecycle but the start/stop is currently inline in `StartDashboard`. Wrap to satisfy LCI; move the broker/state plumbing inside. **Effort: S-M.** Blocker: `StartDashboard` returns an `*http.Server` to the caller for tests; the wrapper has to preserve that test seam.

3. **Telemetry aggregator + retention workers** — currently two inline goroutines launched by `telemetryWG.Add(2)` in `Execute` (handler.go:483-498). Extract into a `telemetry.Subsystem` (or named more narrowly — `telemetry.AggregatorSubsystem`) that owns the goroutines and the wg. **Effort: M.** Blocker: the goroutines today share `telemetryWG` with the spike-report fan-out we added in B2 (commit `1bacf6a`). The migration disentangles those: spike-report lives in its own LCI subsystem, aggregator+retention in another.

4. ~~**Registry watcher (`RegNotifyChangeKeyValue`)**~~ done — extracted to `watcher.RegistrySubsystem` in `internal/watcher/registry_subsystem_windows.go`. Stop is wired alongside `pipeSub.Stop()` in the SCM-stop branch; the legacy top-level `WatchDrainModeKey` / `WatchParametersKey` / `watchRegistryKey` were retired in the same change.

5. ~~**Config-file watcher**~~ done — extracted to `watcher.ConfigFileSubsystem` in `internal/watcher/configfile_subsystem_windows.go`. Stop is wired alongside `regSub.Stop()` in the SCM-stop branch; the legacy top-level `WatchConfigFile` / `watchConfigFilePoll` were retired and `params.go` deleted in the same change.

6. ~~**Named-pipe server (`pipe.ServePipe`)**~~ done — extracted to `pipe.Subsystem` in `internal/pipe/subsystem_windows.go`. Stop is wired alongside `selfMetricsSub.Stop()` in the SCM-stop branch.

7. **ETW handler + file log** — `logging.ETWHandler` already has `Close()` (per A1). Wrap `etwH` in a subsystem so `Execute`'s `defer etwH.Close()` becomes `defer sub.Stop()`. **Effort: S.**

8. **Selfmetrics + pprof endpoint** — both started by `startSelfMetricsLogger` and `startPprofServer` today (per `2f26ba6` / `ec092df` / `ab692e4`). Each becomes its own LCI subsystem. **Effort: S each.**
   - ~~Selfmetrics~~ done — extracted to `internal/selfmetrics` package as `selfmetrics.Subsystem`. Stop is wired alongside `updaterSub.Stop()` in the SCM-stop branch.

9. **Service-pipe registration handler / dashboard bootstrap timer / spike forwarder** — these are smaller dispatch loops still in Execute. Extract incrementally as they get touched. **Effort: S each.**

After (9), Execute should be the ~30-line skeleton above. There may be a long-tail of small bits (statusCh emission, the dashBootstrap one-shot, `dashConfigFailures` counter) that don't belong in a subsystem; those stay in Execute as control-plane housekeeping.

## What is NOT migrated

- `cmd/drainctl/` — the CLI is a one-shot per invocation, not a long-running process. LCI does not apply.
- `cmd/cshared/` — the DLL exports per-call functions. LCI does not apply.
- Pure-data-helper packages (`internal/etwids`, `internal/filelog`, `dc.*` types in the root). LCI does not apply.
- Test-only fakes in `_test.go` files. They can choose to satisfy LCI if it makes the test cleaner, but they don't have to.

## Sequencing rules

- The first migration (evtspike → LCI declaration) MUST land before any other subsystem can be migrated, because it introduces the `internal/lifecycle/` package and the type assertion pattern.
- After that, migrations are independent and can interleave freely. Two PRs migrating different subsystems do not conflict.
- A migration PR may introduce new tests asserting LCI-shape behavior (e.g., "Stop returns within N ms after ctx cancel"); those tests live next to the subsystem, not in `internal/lifecycle/`.

## Anti-patterns to refuse in review

- A new long-running goroutine launched directly from `Execute` without a corresponding subsystem. (Direct fix: the PR adds the subsystem.)
- A subsystem whose Stop does not call Wait on its internal wg. (Hidden goroutine leak.)
- A subsystem whose Start spins up goroutines BEFORE returning an error from a synchronous init failure. (Goroutines orphaned, no Stop ever called.)
- A subsystem that takes a `*sync.WaitGroup` parameter from the caller. (Borrowed wg conflates lifetimes — see lifecycle.md §"Goroutine ownership".)
- Adding a method to the `Subsystem` interface to support one specific subsystem's needs. (Interface bloat — extend via a new optional capability interface instead, like `Statusable`.)

## How to update this doc

When a migration lands, edit the migration order section: strike through the migrated entry (or move it to a "Done" section near the end) and note the commit SHA. The STP is a living checklist; over time it should shrink toward zero.
