//go:build windows

package svc

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/lifecycle"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/perfmon"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

var _ lifecycle.Subsystem = (*performanceSubsystem)(nil)

type perfmonOpener func(dc.PerformanceConfig) (*perfmon.Collector, error)

// performanceSubsystem owns the PDH collector lifecycle. Execute still passes
// the current collector into svcRunCheck, but open/prime/close/reload now have
// one Stop point instead of inline defer state.
type performanceSubsystem struct {
	mu sync.Mutex

	cfg          dc.PerformanceConfig
	collector    *perfmon.Collector
	triggerState *perfmon.PerfTriggerState
	lastPerf     *atomic.Pointer[dc.PerfSnapshot]
	open         perfmonOpener

	stopOnce sync.Once
}

func newPerformanceSubsystem(cfg dc.PerformanceConfig, lastPerf *atomic.Pointer[dc.PerfSnapshot]) *performanceSubsystem {
	return &performanceSubsystem{
		cfg:      cfg,
		lastPerf: lastPerf,
		open:     perfmon.Open,
	}
}

func (s *performanceSubsystem) Start(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cfg.Enabled {
		return nil
	}
	if err := s.startLocked(); err != nil {
		return err
	}
	slog.Info("perfmon=started")
	return nil
}

func (s *performanceSubsystem) Stop() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.closeLocked()
	})
}

func (s *performanceSubsystem) Collector() *perfmon.Collector {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.collector
}

func (s *performanceSubsystem) TriggerState() *perfmon.PerfTriggerState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.triggerState
}

// Reload reconciles the running collector with a config change. It returns true
// when the config changed and callers should run an immediate check.
func (s *performanceSubsystem) Reload(newCfg dc.PerformanceConfig) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg == newCfg {
		return false
	}

	s.closeLocked()
	if s.lastPerf != nil {
		s.lastPerf.Store(nil)
	}
	s.cfg = newCfg

	if newCfg.Enabled {
		if err := s.startLocked(); err != nil {
			slog.Warn("perfmon start failed on config change", "error", err)
			return true
		}
		slog.Info("perfmon=restarted")
	} else {
		slog.Info("perfmon=stopped")
	}
	return true
}

func (s *performanceSubsystem) startLocked() error {
	pc, err := s.open(s.cfg)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	if err := pc.Prime(); err != nil {
		pc.Close()
		return fmt.Errorf("prime: %w", err)
	}
	s.collector = pc
	s.triggerState = &perfmon.PerfTriggerState{}
	return nil
}

func (s *performanceSubsystem) closeLocked() {
	if s.collector != nil {
		s.collector.Close()
		s.collector = nil
		s.triggerState = nil
	}
}

// newRetentionProvider returns a closure the retention worker calls at the
// start of every pass to fetch current per-tier retention windows. Reloading
// config from disk keeps the worker honoring live edits to config.json
// without a separate hot-reload path; if the reload fails the startup values
// are reused so a transient disk error cannot widen retention.
func newRetentionProvider(startup *dc.Config) func() telemetry.RetentionSettings {
	startupMetrics := startup.Retention.MetricsDays
	startupAudit := startup.Retention.AuditDays
	return func() telemetry.RetentionSettings {
		c, err := dc.LoadConfig()
		if err != nil {
			slog.Warn("telemetry: retention provider load config failed, using startup values",
				"error", err, "metrics_days", startupMetrics, "audit_days", startupAudit)
			return telemetry.RetentionSettings{
				MetricsDays: startupMetrics,
				AuditDays:   startupAudit,
			}
		}
		return telemetry.RetentionSettings{
			MetricsDays: c.Retention.MetricsDays,
			AuditDays:   c.Retention.AuditDays,
		}
	}
}
