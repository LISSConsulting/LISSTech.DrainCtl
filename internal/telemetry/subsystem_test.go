//go:build windows

package telemetry

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRunner blocks on ctx.Done so the Subsystem's Stop must cancel the
// derived ctx for the goroutine to exit. Tracks invocation + exit so the
// test can assert both happened.
type fakeRunner struct {
	started atomic.Bool
	exited  atomic.Bool
}

func (f *fakeRunner) Run(ctx context.Context) {
	f.started.Store(true)
	<-ctx.Done()
	f.exited.Store(true)
}

// TestSubsystem_StartStopDrains exercises the LCI happy path: Start
// launches both workers, Stop cancels the derived ctx and Waits.
func TestSubsystem_StartStopDrains(t *testing.T) {
	agg, ret := &fakeRunner{}, &fakeRunner{}
	s := New(Config{AggregatorIntervalSeconds: 60, RetentionIntervalMinutes: 30}, Deps{
		Aggregator: agg,
		Retention:  ret,
	})

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give both goroutines time to enter Run.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if agg.started.Load() && ret.started.Load() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !agg.started.Load() || !ret.started.Load() {
		t.Fatal("workers did not enter Run")
	}

	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not drain within 2s")
	}

	if !agg.exited.Load() || !ret.exited.Load() {
		t.Fatal("workers did not observe ctx cancellation on Stop")
	}
}

// TestSubsystem_StopCancelsItsOwnCtx — Stop is self-contained per the
// LCI house pattern. Pass context.Background() (non-cancellable) and
// assert Stop still drains.
func TestSubsystem_StopCancelsItsOwnCtx(t *testing.T) {
	agg, ret := &fakeRunner{}, &fakeRunner{}
	s := New(Config{}, Deps{Aggregator: agg, Retention: ret})

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not drain within 2s on a non-cancellable parent ctx")
	}
}

// TestSubsystem_StopWithoutStartIsSafe — LCI contract: Stop tolerates a
// Start that never ran. No panic, no hang.
func TestSubsystem_StopWithoutStartIsSafe(t *testing.T) {
	s := New(Config{}, Deps{Aggregator: &fakeRunner{}, Retention: &fakeRunner{}})
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop on never-started subsystem did not return")
	}
}

// TestSubsystem_DoubleStopIsIdempotent — second Stop returns
// immediately, doesn't double-close cancel or double-Wait.
func TestSubsystem_DoubleStopIsIdempotent(t *testing.T) {
	s := New(Config{}, Deps{Aggregator: &fakeRunner{}, Retention: &fakeRunner{}})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	s.Stop()
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("second Stop did not return immediately")
	}
}

// TestSubsystem_ParentCtxCancelDrains — even if the caller forgets to
// invoke Stop, cancelling the parent ctx drains the workers (the
// derived ctx propagates the cancellation). Stop afterwards is a no-op.
func TestSubsystem_ParentCtxCancelDrains(t *testing.T) {
	agg, ret := &fakeRunner{}, &fakeRunner{}
	s := New(Config{}, Deps{Aggregator: agg, Retention: ret})

	ctx, cancel := context.WithCancel(context.Background())
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	cancel()

	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not drain after parent ctx cancel")
	}
}
