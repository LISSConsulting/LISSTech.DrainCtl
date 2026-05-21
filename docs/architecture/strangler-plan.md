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

1. ~~**`internal/evtspike`**~~ done — `internal/lifecycle/` declares the two-method `Subsystem` interface plus the optional `Statusable` capability; `internal/evtspike/subsystem.go` carries the compile-time assertion `var _ lifecycle.Subsystem = (*Subsystem)(nil)`. This was the demonstration migration that seeded every subsequent step.

2. ~~**`internal/dashboard`**~~ done — extracted to `dashboard.Subsystem` in `internal/dashboard/subsystem_windows.go`. The legacy `StartDashboard` was retired in the same change; `Subsystem.Start` performs the legacy-servers.json migration, sets up TLS + listener + routes synchronously, then launches the Serve and ctx-Done shutdown-drain goroutines under an internal `sync.WaitGroup`. `Stop` cancels the derived ctx (firing the http.Server.Shutdown bounded at 5 s) and waits the wg; transitively-owned reapers (SessionStore, NegotiateMiddleware SSPI) exit on the same derived-ctx cancel. `Subsystem.State()` preserves the test-seam shape (callers receive `*ServerState` to wire `Register` / `OnEvtSpike*` callbacks). Stop is wired alongside `configSub.Stop()` in the SCM-stop branch; the hot-reload "late start" path constructs a fresh `Subsystem` and replaces `dashSub`.

3. ~~**Telemetry aggregator + retention workers**~~ done — extracted to `telemetry.Subsystem` in `internal/telemetry/subsystem.go`. The two formerly-inline goroutines now live behind `telSub.Start(ctx)` / `telSub.Stop()`; the SCM-stop branch calls `waitWithTimeout("telemetry", telSub.Stop, 10s)` to preserve the prior 10s shutdown bound. The shared `telemetryWG` is dismantled — spike-report fan-out got its own subsystem in step (9).

4. ~~**Registry watcher (`RegNotifyChangeKeyValue`)**~~ done — extracted to `watcher.RegistrySubsystem` in `internal/watcher/registry_subsystem_windows.go`. Stop is wired alongside `pipeSub.Stop()` in the SCM-stop branch; the legacy top-level `WatchDrainModeKey` / `WatchParametersKey` / `watchRegistryKey` were retired in the same change.

5. ~~**Config-file watcher**~~ done — extracted to `watcher.ConfigFileSubsystem` in `internal/watcher/configfile_subsystem_windows.go`. Stop is wired alongside `regSub.Stop()` in the SCM-stop branch; the legacy top-level `WatchConfigFile` / `watchConfigFilePoll` were retired and `params.go` deleted in the same change.

6. ~~**Named-pipe server (`pipe.ServePipe`)**~~ done — extracted to `pipe.Subsystem` in `internal/pipe/subsystem_windows.go`. Stop is wired alongside `selfMetricsSub.Stop()` in the SCM-stop branch.

7. ~~**ETW handler + file log**~~ done — extracted to `logging.Subsystem` in `internal/logging/subsystem_windows.go`. The deferred Close pair in `RunService` (`etwH.Close()` / `fw.Close()`) collapses to a single `defer logSub.Stop()`. Lifetime is the RunService scope rather than Execute's because slog must be wired before svc.Run starts and must outlive every other subsystem so SCM-stop messages still land; the type satisfies LCI for the deferred-Stop pattern. The legacy `drainService.etw` field was retired in the same change.

8. ~~**Selfmetrics + pprof endpoint**~~ done — both extracted as their own LCI subsystems.
   - ~~Selfmetrics~~ done — extracted to `internal/selfmetrics` package as `selfmetrics.Subsystem`. Stop is wired alongside `updaterSub.Stop()` in the SCM-stop branch.
   - ~~pprof endpoint~~ done — extracted to `internal/debugpprof` package as `debugpprof.Subsystem`. The previous in-place `startPprof` collapsed: New() reads `DRAINCTL_PPROF_PORT` and produces a disabled subsystem when the env var is unset/invalid; Start binds the loopback listener and launches the server; Stop bounds graceful drain at 2 s. Stop is wired alongside `selfMetricsSub.Stop()` in the SCM-stop branch.

9. ~~**Service-pipe registration handler / dashboard bootstrap timer / spike forwarder**~~ done — these smaller dispatch loops have been extracted incrementally.
   - ~~Spike forwarder~~ done — extracted to `spikereport.Subsystem` in `internal/spikereport/subsystem.go`. The free-function `startSpikeReport` and the inline `var spikeReportWG sync.WaitGroup` in `Execute` are gone; the `case spike := <-spikeCh:` arm now calls `spikeSub.Forward(dashCfg.URL, &spikeCopy)`. Stop is wired alongside the other subsystems in the SCM-stop branch as `waitWithTimeout("spike_report", spikeSub.Stop, 10s)` to preserve the prior bound. The package-level `var reportSpike = dashboard.ReportSpike` test seam moved into the `Reporter` ctor parameter, so the test no longer needs to mutate package state.
   - ~~Service-pipe registration handler~~ done — extracted to `registrationSubsystem` in `internal/svc/registration_subsystem_windows.go`. `serviceHandler.HandleRegister` now submits pipe-triggered dashboard registration requests to an LCI-owned worker instead of performing unmanaged HTTPS/SSPI work directly in the pipe handler goroutine; `Stop` cancels the worker and any in-flight registration through the service context.
   - ~~Dashboard bootstrap timer~~ done — extracted to `dashboardBootstrapSubsystem` in `internal/svc/dashboard_bootstrap_subsystem_windows.go`. `Execute` now starts the one-shot timer through LCI after reporting Running and stops it in the SCM-stop branch; the select arm consumes its event channel instead of owning `time.After` directly.

After (9), Execute should be the ~30-line skeleton above. There may be a long-tail of small bits (statusCh emission, `dashConfigFailures` counter) that don't belong in a subsystem; those stay in Execute as control-plane housekeeping. The first post-list cleanup hoisted the performance collector open/prime/close/reload path into `performanceSubsystem` in `internal/svc/performance_subsystem_windows.go`; `Execute` still passes the current collector into check cycles, but it no longer owns the PDH lifecycle directly.

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
