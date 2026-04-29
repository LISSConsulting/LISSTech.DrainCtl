//go:build windows

package telemetry

import (
	"context"
	"log/slog"
	"sync"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/lifecycle"
)

// Compile-time assertion: *Subsystem satisfies lifecycle.Subsystem.
var _ lifecycle.Subsystem = (*Subsystem)(nil)

// Runner is the minimal interface the Subsystem needs from each worker
// it owns. Both *Aggregator and *Retention satisfy it. Defining the
// interface at the consumer (this file) keeps the Subsystem decoupled
// from the concrete worker types and lets tests inject fakes that block
// on ctx without touching SQLite.
type Runner interface {
	Run(ctx context.Context)
}

// Deps bundles the runtime collaborators the Subsystem fans out to. The
// fields are interfaces, not concrete *Aggregator / *Retention pointers,
// so the Subsystem stays testable without a live *DB.
type Deps struct {
	// Aggregator rolls metrics_raw → metrics_5min → metrics_hourly. Required.
	Aggregator Runner
	// Retention purges expired rows + drives WAL checkpoint policy. Required.
	Retention Runner
}

// Config carries the human-readable parameters the Subsystem logs on
// Start. The intervals themselves are baked into the Runner instances
// supplied via Deps; these fields exist so the "telemetry=workers_started"
// log line that used to live in svc.Execute travels with the subsystem.
type Config struct {
	AggregatorIntervalSeconds int
	RetentionIntervalMinutes  int
}

// Subsystem owns the aggregator + retention worker goroutines that were
// previously launched inline in svc.Execute against a borrowed
// telemetryWG. Construct via New, register alongside other
// lifecycle.Subsystems, call Start with a service-scoped ctx, call Stop
// on shutdown. Stop is self-contained — it cancels the derived ctx
// before draining the internal wg, matching the
// pipe / selfmetrics / updater house pattern.
//
// The shutdown bound that the prior waitTelemetryWorkers helper provided
// is NOT inside Stop; LCI Stop is unconditional. Callers that need a
// budget wrap Stop in a select-with-timeout (svc.Execute does this so a
// stuck DB op cannot stall svc.Stop past the SCM wait hint).
type Subsystem struct {
	cfg  Config
	deps Deps

	wg       sync.WaitGroup
	cancel   context.CancelFunc
	stopOnce sync.Once
}

// New constructs the Subsystem. deps.Aggregator and deps.Retention MUST
// be non-nil; passing nil is a programmer error and Start will panic on
// the first goroutine that dereferences it.
func New(cfg Config, deps Deps) *Subsystem {
	return &Subsystem{cfg: cfg, deps: deps}
}

// Start launches the aggregator and retention loops on a context derived
// from ctx so Stop can cancel them independently of the caller. Returns
// nil — neither worker has a synchronous init step that can fail before
// its goroutine launches; runtime errors are surfaced through the
// workers' own slog output.
func (s *Subsystem) Start(ctx context.Context) error {
	derived, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		s.deps.Aggregator.Run(derived)
	}()
	go func() {
		defer s.wg.Done()
		s.deps.Retention.Run(derived)
	}()
	slog.Info("telemetry=workers_started",
		"aggregator_interval_seconds", s.cfg.AggregatorIntervalSeconds,
		"retention_interval_minutes", s.cfg.RetentionIntervalMinutes)
	return nil
}

// Stop cancels the derived ctx and blocks until both workers have
// exited. Idempotent. Safe to call before Start (the cancel field is
// nil-checked) and after a Start that returned an error (no goroutines
// to drain in either case).
func (s *Subsystem) Stop() {
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		s.wg.Wait()
	})
}
