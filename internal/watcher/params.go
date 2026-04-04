//go:build windows

package watcher

import (
	"context"
	"os"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// WatchConfigFile polls config.json for mtime changes every 5 seconds.
// Sends on the returned channel when the file is modified. Exits when ctx
// is cancelled.
func WatchConfigFile(ctx context.Context, path string, log dc.LogFunc) (<-chan struct{}, error) {
	ch := make(chan struct{}, 1)

	initialMtime := time.Time{}
	if info, err := os.Stat(path); err == nil {
		initialMtime = info.ModTime()
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
				info, err := os.Stat(path)
				if err != nil {
					continue
				}
				if info.ModTime().After(lastMtime) {
					lastMtime = info.ModTime()
					select {
					case ch <- struct{}{}:
					default:
					}
				}
			}
		}
	}()

	return ch, nil
}

// WatchParametersKey monitors the service's Parameters registry key for
// changes. Kept for backwards compatibility — new code should use
// WatchConfigFile instead.
func WatchParametersKey(ctx context.Context, log dc.LogFunc) (<-chan struct{}, error) {
	return watchRegistryKey(ctx, dc.ParametersKeyPath, log)
}
