//go:build windows

package spikereport

import (
	"context"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// TestForwardTracksWaitGroup verifies the wg + derived-ctx wiring: a
// Forward call launches a goroutine that exits when Stop cancels the
// derived ctx, and Stop blocks on that exit. Without that wiring the
// SCM-stop drain drops the goroutine on the floor and it lingers inside
// SSPI/HTTP for the dashboard client's internal timeout.
//
// The hook MUST observe ctx — if it blocked forever instead, the wg
// would never drain and the test would hang regardless of correctness.
func TestForwardTracksWaitGroup(t *testing.T) {
	started := make(chan struct{})
	sub := New(func(ctx context.Context, _ string, _ *dc.SpikePayload) {
		close(started)
		<-ctx.Done()
	})

	if err := sub.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	sub.Forward("http://dashboard.invalid", &dc.SpikePayload{Host: "h"})

	select {
	case <-started:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("reporter goroutine did not start within 200ms")
	}

	done := make(chan struct{})
	go func() {
		sub.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Stop cancelled the derived ctx, the reporter observed it,
		// wg.Done fired, Stop returned.
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("Stop did not drain reporter goroutine within 200ms after ctx cancel — wg/ctx wiring broken")
	}
}

// TestForwardBeforeStartDrops verifies that a Forward call made before
// Start is dropped instead of panicking. The contract is that the spike
// is delivered via notifications regardless; the remote-forward leg is
// best-effort.
func TestForwardBeforeStartDrops(t *testing.T) {
	called := false
	sub := New(func(context.Context, string, *dc.SpikePayload) {
		called = true
	})
	sub.Forward("http://dashboard.invalid", &dc.SpikePayload{Host: "h"})
	// No Start ever called — Stop must still be safe.
	sub.Stop()
	if called {
		t.Fatalf("reporter ran without Start; pre-Start Forward should drop")
	}
}

// TestForwardAfterStopDrops verifies that a Forward call after Stop is
// dropped — no new goroutine is launched, so a second Stop is still a
// no-op and there is no race with the post-Stop wg state.
func TestForwardAfterStopDrops(t *testing.T) {
	called := false
	sub := New(func(context.Context, string, *dc.SpikePayload) {
		called = true
	})
	if err := sub.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	sub.Stop()
	sub.Forward("http://dashboard.invalid", &dc.SpikePayload{Host: "h"})
	// Give a stray goroutine a moment to run if the drop was broken.
	time.Sleep(20 * time.Millisecond)
	if called {
		t.Fatalf("reporter ran after Stop; post-Stop Forward should drop")
	}
	// Second Stop is idempotent.
	sub.Stop()
}
