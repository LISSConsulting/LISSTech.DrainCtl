//go:build windows

package watcher

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/lifecycle"
)

// Compile-time assertion: *RegistrySubsystem satisfies lifecycle.Subsystem.
var _ lifecycle.Subsystem = (*RegistrySubsystem)(nil)

// RegistrySubsystem watches a single HKLM registry key for value changes
// using RegNotifyChangeKeyValue and emits a tick on Events() each time
// the key is modified. Construct via NewRegistrySubsystem, register
// alongside other lifecycle.Subsystems, call Start with a service-scoped
// ctx, call Stop on shutdown.
//
// Stop is self-contained: it cancels the derived ctx itself before
// waiting on the wg, matching the evtspike / pipe / selfmetrics house
// pattern.
type RegistrySubsystem struct {
	keyPath string
	events  chan struct{}

	key         registry.Key
	notifyEvent windows.Handle
	cancelEvent windows.Handle

	wg       sync.WaitGroup
	cancel   context.CancelFunc
	stopOnce sync.Once
}

// NewRegistrySubsystem constructs the subsystem for keyPath under HKLM.
// The Events channel is allocated up front so a Stop() before a successful
// Start() — or after a failed Start() — is still safe to await.
func NewRegistrySubsystem(keyPath string) *RegistrySubsystem {
	return &RegistrySubsystem{
		keyPath: keyPath,
		events:  make(chan struct{}, 1),
	}
}

// Events returns the channel that fires once each time the watched key's
// values are modified. Non-blocking: if the previous tick has not been
// drained, additional changes coalesce into the buffered slot. The channel
// is closed when the watch goroutine exits.
func (s *RegistrySubsystem) Events() <-chan struct{} {
	return s.events
}

// Start opens the registry key with KEY_NOTIFY, allocates the notify and
// cancel events, and launches the watch goroutine. Returns the underlying
// Win32 error if any of those synchronous steps fail; the caller is
// responsible for falling back to poll-only operation in that case.
func (s *RegistrySubsystem) Start(ctx context.Context) error {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, s.keyPath,
		registry.QUERY_VALUE|registry.NOTIFY)
	if err != nil {
		return fmt.Errorf("open key for notify: %w", err)
	}

	notifyEvent, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		_ = key.Close()
		return fmt.Errorf("create notify event: %w", err)
	}

	cancelEvent, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(notifyEvent)
		_ = key.Close()
		return fmt.Errorf("create cancel event: %w", err)
	}

	s.key = key
	s.notifyEvent = notifyEvent
	s.cancelEvent = cancelEvent

	derived, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	s.wg.Add(1)
	go s.run(derived)

	slog.Info("registry_watcher=started", "key", s.keyPath)
	return nil
}

// Stop cancels the derived ctx and blocks until the watch goroutine has
// exited and freed its handles. Idempotent.
func (s *RegistrySubsystem) Stop() {
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		s.wg.Wait()
	})
}

func (s *RegistrySubsystem) run(ctx context.Context) {
	defer s.wg.Done()
	defer func() {
		_ = s.key.Close()
		_ = windows.CloseHandle(s.notifyEvent)
		_ = windows.CloseHandle(s.cancelEvent)
		close(s.events)
	}()

	// Bridge ctx.Done() to the cancel event so WaitForMultipleObjects can
	// observe cancellation. Tracked on wg so Stop drains it too.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		<-ctx.Done()
		_ = windows.SetEvent(s.cancelEvent)
	}()

	handles := []windows.Handle{s.notifyEvent, s.cancelEvent}

	for {
		err := windows.RegNotifyChangeKeyValue(
			windows.Handle(s.key),
			false,
			windows.REG_NOTIFY_CHANGE_LAST_SET|windows.REG_NOTIFY_THREAD_AGNOSTIC,
			s.notifyEvent,
			true,
		)
		if err != nil {
			slog.Error("RegNotifyChangeKeyValue failed", "error", err, "key", s.keyPath)
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
				continue
			}
		}

		idx, err := windows.WaitForMultipleObjects(handles, false, windows.INFINITE)
		if err != nil {
			slog.Error("WaitForMultipleObjects failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}

		switch idx {
		case windows.WAIT_OBJECT_0:
			select {
			case s.events <- struct{}{}:
			default:
			}
		default:
			return
		}
	}
}
