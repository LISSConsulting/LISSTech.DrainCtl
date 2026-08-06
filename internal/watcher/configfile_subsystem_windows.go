//go:build windows

package watcher

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/lifecycle"
)

// Compile-time assertion: *ConfigFileSubsystem satisfies lifecycle.Subsystem.
var _ lifecycle.Subsystem = (*ConfigFileSubsystem)(nil)

// configFilePollInterval is the cadence used by the fallback poll loop
// when FindFirstChangeNotification cannot be established (e.g. directory
// permission anomalies). 5 s matches the prior standalone watcher.
const configFilePollInterval = 5 * time.Second

// ConfigFileSubsystem watches a single file for content or replacement
// changes. It prefers FindFirstChangeNotification on the file's directory
// and filters notifications by the target file's mtime. Watching both file
// names and writes is required because SaveConfig atomically replaces the
// file rather than modifying it in place. If the directory watch cannot be set up,
// it falls back to a 5 s mtime-poll loop. Either way the surface is one
// channel that ticks once per detected change.
//
// Construct via NewConfigFileSubsystem, register alongside other
// lifecycle.Subsystems, call Start with a service-scoped ctx, call Stop
// on shutdown. Stop is self-contained.
type ConfigFileSubsystem struct {
	path   string
	events chan struct{}

	wg       sync.WaitGroup
	cancel   context.CancelFunc
	stopOnce sync.Once
}

// NewConfigFileSubsystem constructs a watcher for path. Mode (event vs
// poll) is decided at Start time based on whether
// FindFirstChangeNotification succeeds for the parent directory.
func NewConfigFileSubsystem(path string) *ConfigFileSubsystem {
	return &ConfigFileSubsystem{
		path:   path,
		events: make(chan struct{}, 1),
	}
}

// Events returns the channel that fires each time the watched file is
// rewritten. Closed when the watch goroutine exits.
func (s *ConfigFileSubsystem) Events() <-chan struct{} {
	return s.events
}

// Start launches the watch goroutine. Returns nil unconditionally — a
// failure to acquire the directory notification handle silently drops
// to the poll fallback rather than failing Start, matching the prior
// WatchConfigFile contract (poll mode is degraded but functional).
func (s *ConfigFileSubsystem) Start(ctx context.Context) error {
	derived, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	dir := filepath.Dir(s.path)
	handle, err := windows.FindFirstChangeNotification(dir, false, windows.FILE_NOTIFY_CHANGE_FILE_NAME|windows.FILE_NOTIFY_CHANGE_LAST_WRITE)
	if err == nil && handle != windows.InvalidHandle {
		cancelEvent, ceErr := windows.CreateEvent(nil, 1, 0, nil)
		if ceErr == nil {
			s.wg.Add(1)
			go s.runEvent(derived, dir, handle, cancelEvent)
			slog.Info("config_watcher=started", "dir", dir, "mode", "event-based")
			return nil
		}
		_ = windows.FindCloseChangeNotification(handle)
		slog.Warn("config watcher: CreateEvent failed, falling back to poll", "dir", dir, "error", ceErr)
	} else {
		slog.Warn("config watcher: FindFirstChangeNotification failed, falling back to poll", "dir", dir, "error", err)
	}

	s.wg.Add(1)
	go s.runPoll(derived)
	slog.Info("config_watcher=started", "path", s.path, "mode", "poll fallback")
	return nil
}

// Stop cancels the derived ctx and blocks until the watch goroutine has
// exited. Idempotent.
func (s *ConfigFileSubsystem) Stop() {
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		s.wg.Wait()
	})
}

// runEvent is the event-driven watch loop using FindNextChangeNotification
// on the parent directory, filtered by the target file's mtime.
func (s *ConfigFileSubsystem) runEvent(ctx context.Context, _dir string, handle, cancelEvent windows.Handle) {
	defer s.wg.Done()
	defer func() {
		_ = windows.FindCloseChangeNotification(handle)
		_ = windows.CloseHandle(cancelEvent)
		close(s.events)
	}()

	var lastMtime time.Time
	if mt, err := statFile(s.path); err == nil {
		lastMtime = mt
	}

	// Bridge ctx.Done() to the cancel event so WaitForMultipleObjects
	// can observe cancellation. Tracked on wg so Stop drains it.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		<-ctx.Done()
		_ = windows.SetEvent(cancelEvent)
	}()

	handles := []windows.Handle{handle, cancelEvent}

	for {
		idx, err := windows.WaitForMultipleObjects(handles, false, windows.INFINITE)
		if err != nil {
			slog.Error("config watcher: WaitForMultipleObjects failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}

		switch idx {
		case windows.WAIT_OBJECT_0:
			if mt, err := statFile(s.path); err == nil && mt.After(lastMtime) {
				lastMtime = mt
				select {
				case s.events <- struct{}{}:
				default:
				}
			}
			if err := windows.FindNextChangeNotification(handle); err != nil {
				slog.Error("config watcher: FindNextChangeNotification failed", "error", err)
				return
			}
		default:
			return
		}
	}
}

// runPoll is the fallback 5-second mtime polling loop.
func (s *ConfigFileSubsystem) runPoll(ctx context.Context) {
	defer s.wg.Done()
	defer close(s.events)

	var lastMtime time.Time
	if mt, err := statFile(s.path); err == nil {
		lastMtime = mt
	}

	t := time.NewTicker(configFilePollInterval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			mt, err := statFile(s.path)
			if err != nil {
				continue
			}
			if mt.After(lastMtime) {
				lastMtime = mt
				select {
				case s.events <- struct{}{}:
				default:
				}
			}
		}
	}
}

// statFile returns the mtime of a file, or an error.
func statFile(path string) (time.Time, error) {
	var fd windows.Win32finddata
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return time.Time{}, err
	}
	h, err := windows.FindFirstFile(p, &fd)
	if err != nil {
		return time.Time{}, err
	}
	_ = windows.FindClose(h)
	return time.Unix(0, fd.LastWriteTime.Nanoseconds()), nil
}
