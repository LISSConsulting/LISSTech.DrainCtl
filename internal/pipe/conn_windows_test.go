//go:build windows

package pipe

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"
)

// uniquePipeName returns a per-test pipe name so tests don't collide with an
// installed service or with each other. Restores the package PipeName when
// the test ends.
func uniquePipeName(t *testing.T) {
	t.Helper()
	orig := PipeName
	PipeName = fmt.Sprintf(`\\.\pipe\drainctl-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	t.Cleanup(func() { PipeName = orig })
}

// waitFor polls cond every 5ms up to timeout. Fails the test on timeout.
// Avoids fixed-sleep flakiness on busy CI runners.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition not met within %v", timeout)
	}
}

// TestAcceptPipeConnHelperExits drives N successful accept/dial cycles on
// the real Windows named pipe and verifies that the cancel-event helper
// goroutine launched inside acceptPipeConn exits each time. Before the B1
// fix, each successful accept leaked one goroutine parked on <-ctx.Done().
//
// Each cycle uses a fresh pipe name so a slow client.Close on the prior
// cycle cannot leave a half-disconnected instance under the same name to
// race the next CreateNamedPipe. The previous shared-name approach was
// flaky on busy CI runners with ERROR_PIPE_CONNECTED on cycle 2+.
func TestAcceptPipeConnHelperExits(t *testing.T) {
	const cycles = 10

	// Baseline: any helper goroutines from earlier tests must have drained.
	waitFor(t, time.Second, func() bool { return helperGoroutineCount.Load() == 0 })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	origName := PipeName
	t.Cleanup(func() { PipeName = origName })

	for i := 0; i < cycles; i++ {
		PipeName = fmt.Sprintf(`\\.\pipe\drainctl-test-%d-%d-%d`, os.Getpid(), time.Now().UnixNano(), i)

		acceptErr := make(chan error, 1)
		acceptConn := make(chan net.Conn, 1)

		go func() {
			c, err := acceptPipeConn(ctx)
			if err != nil {
				acceptErr <- err
				return
			}
			acceptConn <- c
		}()

		// Give the server goroutine a beat to call CreateNamedPipe before
		// we dial. Poll: dial may fail with ERROR_FILE_NOT_FOUND if the
		// pipe instance has not been created yet.
		var client net.Conn
		var dialErr error
		dialDeadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(dialDeadline) {
			client, dialErr = dialPipe()
			if dialErr == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if dialErr != nil {
			t.Fatalf("cycle %d: dialPipe: %v", i, dialErr)
		}

		var server net.Conn
		select {
		case server = <-acceptConn:
		case err := <-acceptErr:
			_ = client.Close()
			t.Fatalf("cycle %d: acceptPipeConn: %v", i, err)
		case <-time.After(2 * time.Second):
			_ = client.Close()
			t.Fatalf("cycle %d: acceptPipeConn did not return", i)
		}

		_ = client.Close()
		_ = server.Close()

		// After each successful accept, the helper goroutine must exit.
		waitFor(t, time.Second, func() bool { return helperGoroutineCount.Load() == 0 })
	}

	if got := helperGoroutineCount.Load(); got != 0 {
		t.Fatalf("helperGoroutineCount = %d after %d cycles, want 0", got, cycles)
	}
}

// TestAcceptPipeConnHelperExitsOnError drives the ctx-cancellation early-return
// path and verifies the helper goroutine still exits. Before the B1 fix it
// would have been parked on the parent ctx, which is exactly the leak we want
// to prove gone.
func TestAcceptPipeConnHelperExitsOnError(t *testing.T) {
	uniquePipeName(t)
	waitFor(t, time.Second, func() bool { return helperGoroutineCount.Load() == 0 })

	ctx, cancel := context.WithCancel(context.Background())

	acceptDone := make(chan error, 1)
	go func() {
		_, err := acceptPipeConn(ctx)
		acceptDone <- err
	}()

	// Give acceptPipeConn time to enter ConnectNamedPipe and spawn the
	// helper goroutine before we cancel.
	waitFor(t, time.Second, func() bool { return helperGoroutineCount.Load() >= 1 })

	cancel()

	select {
	case err := <-acceptDone:
		if err == nil {
			t.Fatal("expected error from cancelled acceptPipeConn, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("acceptPipeConn did not return after ctx cancel")
	}

	waitFor(t, time.Second, func() bool { return helperGoroutineCount.Load() == 0 })
}
