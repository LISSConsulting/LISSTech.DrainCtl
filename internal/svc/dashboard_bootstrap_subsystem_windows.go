//go:build windows

package svc

import (
	"context"
	"sync"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/lifecycle"
)

var _ lifecycle.Subsystem = (*dashboardBootstrapSubsystem)(nil)

// dashboardBootstrapSubsystem owns the one-shot startup timer that nudges
// dashboard registration/config fetch shortly after SCM Running is reported.
type dashboardBootstrapSubsystem struct {
	enabled bool
	delay   time.Duration
	events  chan struct{}

	mu     sync.Mutex
	cancel context.CancelFunc

	wg       sync.WaitGroup
	stopOnce sync.Once
}

func newDashboardBootstrapSubsystem(enabled bool, delay time.Duration) *dashboardBootstrapSubsystem {
	return &dashboardBootstrapSubsystem{
		enabled: enabled,
		delay:   delay,
		events:  make(chan struct{}, 1),
	}
}

func (s *dashboardBootstrapSubsystem) Events() <-chan struct{} {
	return s.events
}

func (s *dashboardBootstrapSubsystem) Start(ctx context.Context) error {
	if !s.enabled {
		return nil
	}
	derived, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		timer := time.NewTimer(s.delay)
		defer timer.Stop()
		select {
		case <-derived.Done():
			return
		case <-timer.C:
		}
		select {
		case s.events <- struct{}{}:
		case <-derived.Done():
		}
	}()
	return nil
}

func (s *dashboardBootstrapSubsystem) Stop() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		cancel := s.cancel
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		s.wg.Wait()
	})
}
