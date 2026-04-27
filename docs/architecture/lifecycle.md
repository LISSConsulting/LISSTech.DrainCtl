# Subsystem Lifecycle Interface (LCI)

**Status:** stable. Changes require a CODEOWNERS review and a migration note.

The drainctl service is a small set of long-running subsystems (event-log spike detection, telemetry aggregation, registry watcher, named-pipe server, dashboard, ETW handler, etc.) coordinated by a single `Execute` goroutine. The LCI is the contract every subsystem implements so `Execute` can start, observe, and stop them uniformly. The shape is deliberately minimal — it documents what's already common, not what we'd like to add.

## Interface

```go
// Subsystem is a long-running component owned by the service. Every
// subsystem MUST be safe to construct, MUST tie its goroutine lifetimes
// to the ctx passed to Start, and MUST drain those goroutines from Stop.
type Subsystem interface {
    // Start launches background goroutines tied to ctx. Returns an error
    // if initialization fails before any goroutine launches; once Start
    // returns nil, the subsystem owns its workers and is responsible for
    // unwinding them on ctx cancellation or Stop.
    //
    // Start MUST be safe to call exactly once. Calling Start a second time
    // is a programmer error and may panic.
    Start(ctx context.Context) error

    // Stop blocks until every goroutine launched by Start has exited.
    // Idempotent — a second Stop is a no-op. Stop MUST tolerate a Start
    // that returned an error (in which case there is nothing to drain).
    //
    // Stop does NOT cancel ctx itself; the service is responsible for
    // calling cancel() on the ctx passed to Start before invoking Stop.
    // The split lets the service cancel many subsystems in parallel and
    // then collect them serially.
    Stop()
}
```

That's the whole contract. Two methods.

## Optional capability: Status

Subsystems whose state operators care about may also implement:

```go
type Statusable interface {
    Status() any  // subsystem-defined status value
}
```

The dashboard and selfmetrics paths use a type-assertion to detect Statusable subsystems. Subsystems without observable state (e.g., a registry watcher that only fires callbacks) MUST NOT implement Status to avoid the temptation of adding meaningless fields.

## Goroutine ownership

- The subsystem owns one or more `sync.WaitGroup`s internally; Stop calls Wait on them.
- The subsystem MUST NOT accept a `*sync.WaitGroup` from the caller. Past code did this (the pre-LCI `dashboard.ReportSpike` goroutine borrowed `telemetryWG`), and it conflated lifetimes — service shutdown couldn't tell whose work was outstanding. Each subsystem's Stop is the caller's complete signal.
- Goroutines launched by Start MUST select on `ctx.Done()` at every blocking point. Polling without a ctx-aware select is a leak.

## Errors

- `Start` returns errors only for synchronous initialization failures (e.g., DB open failed, port bind refused). It MUST NOT return errors discovered after a goroutine has launched.
- Runtime errors discovered by background goroutines are reported via the subsystem's own callbacks (e.g., `OnStatusChange`, `OnLoss`) or its logs. The service does not poll subsystems for errors.
- A subsystem whose primary worker has irrecoverably failed at runtime (e.g., evtspike all subscriptions in StateFailed) MUST keep running its other workers so Stop can still drain cleanly. The status surface signals the failure to operators; the lifecycle does not flap.

## What the LCI explicitly does NOT do

- **No DI container.** The service constructs subsystems directly with their dependencies as constructor arguments. No registry-of-registries.
- **No event bus.** Subsystems communicate via direct callbacks (`OnSpike`, `OnUpdate`) or by holding pointers to each other. The graph is small enough that explicit wiring is clearer than indirection.
- **No reflection-based discovery.** Subsystems are added to the service's start list by hand. The list is intentionally a thing to read in code review.
- **No per-subsystem health endpoints, ready/live distinctions, or restart policies.** If a subsystem can't recover, the service crashes and the SCM restarts the process. We don't have liveness probes; we have crash-loop telemetry and operator alerts.
- **No `Reload(cfg)` in the interface.** Some subsystems support hot config reload (evtspike does); others would have to rebuild themselves. Reload is per-subsystem and not on the lifecycle path.

## Reference implementation

`internal/evtspike` predates the LCI by several months and matches it shape-for-shape. When in doubt, read `internal/evtspike/subsystem.go` for the canonical pattern (Start that returns early on `Enabled=false`, Stop that drains via `quiesce`, internal supervisor + scoring + persistence loops each owning a wg.Add).

## Versioning

This interface is part of `internal/lifecycle/`. It is not exported outside the module. Adding a method to `Subsystem` is a breaking change for every existing implementation; do it via STP-tracked migration, not by adding methods speculatively.
