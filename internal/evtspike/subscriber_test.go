//go:build windows

package evtspike

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestSubscribe_FiresLossOnTerminalError(t *testing.T) {
	origCreateEvent := createEvent
	origCloseHandle := closeHandle
	origWaitForSingleObject := waitForSingleObject
	origEvtSubscribeCall := evtSubscribeCall
	origEvtNextCall := evtNextCall
	origEvtCloseCall := evtCloseCall
	t.Cleanup(func() {
		createEvent = origCreateEvent
		closeHandle = origCloseHandle
		waitForSingleObject = origWaitForSingleObject
		evtSubscribeCall = origEvtSubscribeCall
		evtNextCall = origEvtNextCall
		evtCloseCall = origEvtCloseCall
	})

	var subHandle uintptr
	var closed atomic.Bool

	createEvent = func(*windows.SecurityAttributes, uint32, uint32, *uint16) (windows.Handle, error) {
		return windows.Handle(1), nil
	}
	closeHandle = func(windows.Handle) error { return nil }
	waitForSingleObject = func(windows.Handle, uint32) (uint32, error) {
		return uint32(windows.WAIT_OBJECT_0), nil
	}
	evtSubscribeCall = func(a ...uintptr) (uintptr, uintptr, error) {
		subHandle = 2
		return subHandle, 0, nil
	}
	evtNextCall = func(a ...uintptr) (uintptr, uintptr, error) {
		if closed.Load() {
			return 0, 0, windows.ERROR_INVALID_HANDLE
		}
		return 0, 0, windows.ERROR_TIMEOUT
	}
	evtCloseCall = func(a ...uintptr) (uintptr, uintptr, error) {
		if len(a) > 0 && a[0] == subHandle {
			closed.Store(true)
		}
		return 1, 0, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	var counter atomic.Int64
	lossCh := make(chan error, 1)

	if err := Subscribe(ctx, &wg, "Application", "*", &counter, func(err error) {
		lossCh <- err
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if _, _, err := evtCloseCall(subHandle); err != nil {
		t.Fatalf("evtCloseCall(sub): %v", err)
	}

	select {
	case err := <-lossCh:
		if err == nil {
			t.Fatal("loss error is nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("loss callback did not fire within 2s")
	}

	cancel()
	wg.Wait()
}
