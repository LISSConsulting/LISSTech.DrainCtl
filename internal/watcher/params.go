//go:build windows

package watcher

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

// WatchConfigFile monitors the config file's directory for write changes
// using FindFirstChangeNotification (event-based, no polling). Falls back
// to a 5-second mtime poll if the directory watch cannot be established.
// Sends on the returned channel when the file is modified. Exits when ctx
// is cancelled.
func WatchConfigFile(ctx context.Context, path string) (<-chan struct{}, error) {
	dir := filepath.Dir(path)

	handle, err := windows.FindFirstChangeNotification(dir, false, windows.FILE_NOTIFY_CHANGE_LAST_WRITE)
	if err != nil || handle == windows.InvalidHandle {
		slog.Warn("config watcher: FindFirstChangeNotification failed, falling back to poll", "dir", dir, "error", err)
		return watchConfigFilePoll(ctx, path)
	}

	cancelEvent, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		_ = windows.FindCloseChangeNotification(handle)
		return watchConfigFilePoll(ctx, path)
	}

	ch := make(chan struct{}, 1)

	// Track config.json mtime to filter out writes to other files in the directory.
	configPath := path
	var lastMtime time.Time
	if mt, err := statFile(configPath); err == nil {
		lastMtime = mt
	}

	go func() {
		defer func() {
			_ = windows.FindCloseChangeNotification(handle)
			_ = windows.CloseHandle(cancelEvent)
			close(ch)
		}()

		go func() {
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
				// Directory write detected — only notify if config.json actually changed.
				if mt, err := statFile(configPath); err == nil && mt.After(lastMtime) {
					lastMtime = mt
					select {
					case ch <- struct{}{}:
					default:
					}
				}
				// Re-arm the notification.
				if err := windows.FindNextChangeNotification(handle); err != nil {
					slog.Error("config watcher: FindNextChangeNotification failed", "error", err)
					return
				}
			default:
				// Cancel event or error — exit.
				return
			}
		}
	}()

	slog.Info("config_watcher=started", "dir", dir, "mode", "event-based")
	return ch, nil
}

// watchConfigFilePoll is the fallback 5-second mtime polling watcher.
func watchConfigFilePoll(ctx context.Context, path string) (<-chan struct{}, error) {
	ch := make(chan struct{}, 1)

	var initialMtime time.Time
	if mt, err := statFile(path); err == nil {
		initialMtime = mt
	}

	go func() {
		lastMtime := initialMtime
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				mt, err := statFile(path)
				if err != nil {
					continue
				}
				if mt.After(lastMtime) {
					lastMtime = mt
					select {
					case ch <- struct{}{}:
					default:
					}
				}
			}
		}
	}()

	slog.Info("config_watcher=started", "path", path, "mode", "poll fallback")
	return ch, nil
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
