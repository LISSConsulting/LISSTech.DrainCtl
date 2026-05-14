//go:build windows

// Package spikereport owns the fan-out goroutines that forward confirmed
// evtspike spikes from the local agent to a remote dashboard. See
// docs/architecture/strangler-plan.md item (9): this was previously the
// inline `var spikeReportWG sync.WaitGroup` + `startSpikeReport` pair in
// svc.Execute, sharing telemetryWG with the aggregator/retention workers
// (B2 / commit 1bacf6a). The aggregator/retention extraction left this
// dangling under its own waitgroup; this package promotes that waitgroup
// into an LCI Subsystem so Stop can bound the drain alongside the other
// subsystems.
package spikereport

import (
	"context"
	"sync"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/lifecycle"
)

// Compile-time assertion: *Subsystem satisfies lifecycle.Subsystem.
var _ lifecycle.Subsystem = (*Subsystem)(nil)

// Reporter is the indirection seam over dashboard.ReportSpike. Production
// callers pass dashboard.ReportSpike via New; tests substitute a hook that
// observes ctx cancellation without booting the dashboard HTTP client.
// The Reporter MUST honour ctx — Stop cancels the derived ctx and waits
// for every in-flight Forward goroutine to exit, so a Reporter that
// ignores ctx will hang the SCM-stop drain.
type Reporter func(ctx context.Context, url string, spike *dc.SpikePayload)

// Subsystem owns the wg + derived ctx for spike-report fan-out goroutines.
// Forward is called from svc.Execute's `case spike := <-spikeCh:` arm; Stop
// is called alongside the other subsystems in the SCM-stop branch (wrapped
// in waitWithTimeout so a stuck dashboard HTTP call cannot stall svc.Stop
// past the SCM wait hint).
type Subsystem struct {
	report Reporter

	wg sync.WaitGroup

	mu      sync.Mutex
	derived context.Context
	cancel  context.CancelFunc
	stopped bool

	stopOnce sync.Once
}

// New constructs the Subsystem. report MUST be non-nil; passing nil is a
// programmer error and Forward would panic on the first invocation.
func New(report Reporter) *Subsystem {
	return &Subsystem{report: report}
}

// Start derives a ctx Stop can cancel independently of the caller's. Returns
// nil — there is no synchronous init that can fail before any goroutine
// launches; Forward is what launches goroutines, not Start.
func (s *Subsystem) Start(ctx context.Context) error {
	derived, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.derived = derived
	s.cancel = cancel
	s.mu.Unlock()
	return nil
}

// Forward launches a tracked goroutine that invokes the Reporter with the
// subsystem's derived ctx. A Forward call before Start or after Stop is
// dropped (the spike is delivered via notifications regardless, see the
// dashboard.ReportSpike contract). Safe to call concurrently.
func (s *Subsystem) Forward(url string, spike *dc.SpikePayload) {
	s.mu.Lock()
	if s.stopped || s.derived == nil {
		s.mu.Unlock()
		return
	}
	ctx := s.derived
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		s.report(ctx, url, spike)
	}()
}

// Stop cancels the derived ctx and blocks until every Forward goroutine
// has exited. Idempotent. Safe to call before Start (cancel is nil-checked)
// and after Start returned (no goroutines to drain in that case either).
func (s *Subsystem) Stop() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.stopped = true
		cancel := s.cancel
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		s.wg.Wait()
	})
}
