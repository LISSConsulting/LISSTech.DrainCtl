//go:build windows

package svc

import (
	"context"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/updater"
)

// reconcileUpdaterForDashboardMode owns updater construction across the
// dashboard_only boundary. A management-only host gets a nil subsystem rather
// than an inactive updater, so no updater worker can start or poll there.
func reconcileUpdaterForDashboardMode(ctx context.Context, current *updater.Subsystem, dashboardOnly bool, cfg dc.UpdateConfig) (*updater.Subsystem, error) {
	if dashboardOnly {
		if current != nil {
			current.Stop()
		}
		return nil, nil
	}
	if current == nil {
		next := updater.New(cfg)
		if err := next.Start(ctx); err != nil {
			return nil, err
		}
		return next, nil
	}
	current.UpdateConfig(cfg)
	return current, nil
}
