//go:build windows

package svc

import (
	"context"
	"testing"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

func TestReconcileUpdaterForDashboardMode_DashboardOnlyDoesNotConstructUpdater(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub, err := reconcileUpdaterForDashboardMode(ctx, nil, true, dc.UpdateConfig{Enabled: true})
	if err != nil {
		t.Fatalf("dashboard-only reconcile: %v", err)
	}
	if sub != nil {
		t.Fatal("dashboard-only host constructed an updater")
	}
}

func TestReconcileUpdaterForDashboardMode_ReloadStopsAndRestartsUpdater(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := dc.UpdateConfig{Enabled: false}

	sub, err := reconcileUpdaterForDashboardMode(ctx, nil, false, cfg)
	if err != nil {
		t.Fatalf("agent-mode start: %v", err)
	}
	if sub == nil {
		t.Fatal("agent-mode reload did not construct updater")
	}

	stopped, err := reconcileUpdaterForDashboardMode(ctx, sub, true, cfg)
	if err != nil {
		t.Fatalf("dashboard-only transition: %v", err)
	}
	if stopped != nil {
		t.Fatal("dashboard-only transition retained updater")
	}

	restarted, err := reconcileUpdaterForDashboardMode(ctx, stopped, false, cfg)
	if err != nil {
		t.Fatalf("agent-mode re-enable: %v", err)
	}
	if restarted == nil {
		t.Fatal("agent-mode re-enable did not construct updater")
	}
	restarted.Stop()
}
