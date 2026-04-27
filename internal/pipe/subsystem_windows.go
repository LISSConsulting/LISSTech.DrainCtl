//go:build windows

package pipe

import (
	"context"
	"sync"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/lifecycle"
)

// Compile-time assertion: *Subsystem satisfies lifecycle.Subsystem.
var _ lifecycle.Subsystem = (*Subsystem)(nil)

// Subsystem owns the named-pipe server goroutine launched by ServePipe.
// Construct via New, register alongside other lifecycle.Subsystems, call
// Start with a service-scoped ctx, call Stop on shutdown. Stop is
// self-contained: it cancels the derived ctx itself before draining,
// matching the evtspike / updater / selfmetrics house pattern.
//
// Per-accept helper goroutines are already drained inside acceptPipeConn
// via the B1 fix; this Subsystem just promotes the package-level
// `go pipe.ServePipe(ctx, h)` call into an LCI-conformant struct.
type Subsystem struct {
	handler PipeHandler

	wg       sync.WaitGroup
	cancel   context.CancelFunc
	stopOnce sync.Once
}

// New constructs the Subsystem. handler must outlive Stop — it's
// invoked from per-accept goroutines whose lifetimes the Subsystem
// drains, so a handler reset between Start and Stop is undefined.
func New(handler PipeHandler) *Subsystem {
	return &Subsystem{handler: handler}
}

// Start launches the ServePipe accept loop. Returns nil — there's no
// synchronous init that can fail before the goroutine launches; the
// pipe creation itself happens inside acceptPipeConn on every accept,
// and a transient failure there logs and retries rather than aborting
// Start (matches the prior inline `go pipe.ServePipe(ctx, h)` semantics).
func (s *Subsystem) Start(ctx context.Context) error {
	derived, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ServePipe(derived, s.handler)
	}()
	return nil
}

// Stop cancels the derived ctx and blocks until the accept loop has
// exited. Idempotent. Safe to call before Start (the cancel field is
// nil-checked).
func (s *Subsystem) Stop() {
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		s.wg.Wait()
	})
}
