//go:build windows

// Package lifecycle defines the contract every long-running drainctl
// service subsystem implements. The shape is intentionally minimal — see
// docs/architecture/lifecycle.md for the design rationale and the rules
// every implementer MUST follow (goroutine ownership, error handling,
// what the LCI deliberately does NOT do).
package lifecycle

import "context"

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

// Statusable is an optional capability for subsystems whose state operators
// care about. The dashboard and selfmetrics paths use a type assertion to
// detect Statusable subsystems. Subsystems without observable state MUST
// NOT implement Status to avoid the temptation of adding meaningless fields.
type Statusable interface {
	// Status returns a subsystem-defined snapshot. The dynamic type is
	// per-subsystem; callers type-assert to the expected concrete type.
	Status() any
}
