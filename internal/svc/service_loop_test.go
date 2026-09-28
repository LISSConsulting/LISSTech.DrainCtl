//go:build windows

package svc

import (
	"context"
	"testing"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/dashboard"
)

type testFeatureRuntime struct{}

func (testFeatureRuntime) Recover(context.Context) error            { return nil }
func (testFeatureRuntime) DrainInbox(context.Context) error         { return nil }
func (testFeatureRuntime) PruneExpiredQueued(context.Context) error { return nil }
func (testFeatureRuntime) StartWorker(context.Context) error        { return nil }
func (testFeatureRuntime) Stop()                                    {}
func (testFeatureRuntime) WakeSessionDropInbox()                    {}

func TestDashboardSubsystemConstruction_DefaultProviderDisabled(t *testing.T) {
	cfg := dc.DefaultConfig()
	if cfg.InvestigationProvider.AccessEnabled {
		t.Fatal("default configuration enables provider access")
	}
	if cfg.InvestigationProvider.AutomaticEnabled {
		t.Fatal("default configuration enables automatic investigation")
	}

	factoryCalls := 0
	deps := dashboardSubsystemDependencies{
		featureRuntimeFactory: func() dashboard.FeatureRuntime {
			factoryCalls++
			return testFeatureRuntime{}
		},
	}
	initial := newDashboardSubsystem(cfg.ToDashboardConfig(), cfg.ToServiceConfig(), deps)
	reenabled := newDashboardSubsystem(cfg.ToDashboardConfig(), cfg.ToServiceConfig(), deps)
	if initial == reenabled {
		t.Fatal("dashboard re-enable reused a stopped subsystem")
	}
	if factoryCalls != 2 {
		t.Fatalf("feature runtime factory calls = %d, want 2 for initial and re-enable construction", factoryCalls)
	}
	if initial.State() != nil || initial.Server() != nil {
		t.Fatal("dashboard construction started runtime work before Start")
	}
	if reenabled.State() != nil || reenabled.Server() != nil {
		t.Fatal("re-enabled dashboard construction started runtime work before Start")
	}
}
