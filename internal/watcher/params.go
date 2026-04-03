//go:build windows

package watcher

import (
	"context"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// WatchParametersKey monitors the service's Parameters registry key for
// changes. Sends on the returned channel when any parameter value changes.
// Used for hot-reloading configuration. Exits when ctx is cancelled.
func WatchParametersKey(ctx context.Context, log dc.LogFunc) (<-chan struct{}, error) {
	return watchRegistryKey(ctx, dc.ParametersKeyPath, log)
}
