//go:build windows

package watcher

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// WatchDrainModeKey monitors the Terminal Server registry key for value
// changes using RegNotifyChangeKeyValue. It sends on the returned channel
// each time a change is detected. The goroutine exits when ctx is cancelled.
func WatchDrainModeKey(ctx context.Context, log dc.LogFunc) (<-chan struct{}, error) {
	return watchRegistryKey(ctx, dc.RegPath, log)
}

// watchRegistryKey is the shared implementation for registry key watchers.
// It opens the key with KEY_NOTIFY, creates an event, and loops:
// register notification -> wait -> send on channel -> re-register.
func watchRegistryKey(ctx context.Context, keyPath string, log dc.LogFunc) (<-chan struct{}, error) {
	if log == nil {
		log = dc.DiscardLogger()
	}

	key, err := registry.OpenKey(registry.LOCAL_MACHINE, keyPath,
		registry.QUERY_VALUE|registry.NOTIFY)
	if err != nil {
		return nil, fmt.Errorf("open key for notify: %w", err)
	}

	// Auto-reset event for RegNotifyChangeKeyValue.
	notifyEvent, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		_ = key.Close()
		return nil, fmt.Errorf("create notify event: %w", err)
	}

	// Manual-reset event signaled when ctx is cancelled.
	cancelEvent, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(notifyEvent)
		_ = key.Close()
		return nil, fmt.Errorf("create cancel event: %w", err)
	}

	ch := make(chan struct{}, 1)

	go func() {
		defer func() {
			_ = key.Close()
			_ = windows.CloseHandle(notifyEvent)
			_ = windows.CloseHandle(cancelEvent)
			close(ch)
		}()

		// Bridge ctx.Done() to the cancel event so WaitForMultipleObjects
		// can observe cancellation.
		go func() {
			<-ctx.Done()
			_ = windows.SetEvent(cancelEvent)
		}()

		handles := []windows.Handle{notifyEvent, cancelEvent}

		for {
			// Register for the next change notification (one-shot).
			err := windows.RegNotifyChangeKeyValue(
				windows.Handle(key),
				false, // don't watch subtree
				windows.REG_NOTIFY_CHANGE_LAST_SET|windows.REG_NOTIFY_THREAD_AGNOSTIC,
				notifyEvent,
				true, // async
			)
			if err != nil {
				dc.LogMsg(log, dc.LvlERR, "RegNotifyChangeKeyValue failed", fmt.Sprintf("error=%q key=%s", err, keyPath))
				// Back off and retry unless cancelled.
				select {
				case <-ctx.Done():
					return
				case <-time.After(10 * time.Second):
					continue
				}
			}

			// Wait for either the notification or cancellation.
			idx, err := windows.WaitForMultipleObjects(handles, false, windows.INFINITE)
			if err != nil {
				dc.LogMsg(log, dc.LvlERR, "WaitForMultipleObjects failed", fmt.Sprintf("error=%q", err))
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
					continue
				}
			}

			switch idx {
			case windows.WAIT_OBJECT_0:
				// Registry key changed — notify and loop to re-register.
				select {
				case ch <- struct{}{}:
				default:
				}
			default:
				// Cancel event or error — exit.
				return
			}
		}
	}()

	log(dc.LvlINF, fmt.Sprintf("registry_watcher=started key=%s", keyPath))
	return ch, nil
}
