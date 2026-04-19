//go:build windows

package evtspike

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	wevtapi          = windows.NewLazySystemDLL("wevtapi.dll")
	procEvtSubscribe = wevtapi.NewProc("EvtSubscribe")
	procEvtNext      = wevtapi.NewProc("EvtNext")
	procEvtClose     = wevtapi.NewProc("EvtClose")
)

const evtSubscribeToFutureEvents = 1

// Subscribe creates an EvtSubscribe subscription and drains events in a
// goroutine using EvtNext with a 1-second timeout. EvtRender is skipped
// entirely — only the per-bucket count is needed by the detector, so events
// are counted and their handles closed immediately. The goroutine exits when
// ctx is cancelled; Subscribe itself returns as soon as the subscription is
// established (or fails) so callers can iterate many channels without
// blocking. wg owns the drain goroutine's lifetime — Subscribe runs Add(1)
// synchronously before launching, so Stop's Wait cannot race Add.
//
// loss (may be nil) is invoked from the drain goroutine if EvtNext reports
// an error that signals the subscription has gone bad mid-run — the Windows
// runtime never recovers a bad handle, so the goroutine closes handles,
// calls loss, and exits so the supervisor can drive a re-subscribe.
func Subscribe(ctx context.Context, wg *sync.WaitGroup, channel, query string, counter *atomic.Int64, loss func(error)) error {
	chPtr, _ := windows.UTF16PtrFromString(channel)
	qPtr, _ := windows.UTF16PtrFromString(query)

	sigEvent, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return fmt.Errorf("CreateEvent: %w", err)
	}

	r, _, e := procEvtSubscribe.Call(
		0, uintptr(sigEvent),
		uintptr(unsafe.Pointer(chPtr)),
		uintptr(unsafe.Pointer(qPtr)),
		0, 0, 0,
		evtSubscribeToFutureEvents,
	)
	if r == 0 {
		_ = windows.CloseHandle(sigEvent)
		return fmt.Errorf("EvtSubscribe %s: %w", channel, e)
	}
	sub := r

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			_, _, _ = procEvtClose.Call(sub)
			_ = windows.CloseHandle(sigEvent)
		}()

		var evts [64]uintptr
		var returned uint32

		for {
			_, _ = windows.WaitForSingleObject(sigEvent, 1000)

			select {
			case <-ctx.Done():
				return
			default:
			}

			for {
				r, _, _ := procEvtNext.Call(
					sub, 64,
					uintptr(unsafe.Pointer(&evts[0])),
					0, 0,
					uintptr(unsafe.Pointer(&returned)),
				)
				if r == 0 || returned == 0 {
					break
				}
				counter.Add(int64(returned))
				for j := range returned {
					_, _, _ = procEvtClose.Call(evts[j])
				}
			}
		}
	}()

	return nil
}
