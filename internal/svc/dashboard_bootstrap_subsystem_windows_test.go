//go:build windows

package svc

import (
	"context"
	"testing"
	"time"
)

func TestDashboardBootstrapSubsystem_FiresOnce(t *testing.T) {
	s := newDashboardBootstrapSubsystem(true, 10*time.Millisecond)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(s.Stop)

	select {
	case <-s.Events():
	case <-time.After(time.Second):
		t.Fatal("bootstrap event did not fire")
	}

	select {
	case <-s.Events():
		t.Fatal("bootstrap event fired more than once")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestDashboardBootstrapSubsystem_StopDrains(t *testing.T) {
	s := newDashboardBootstrapSubsystem(true, time.Hour)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	done := make(chan struct{})
	go func() {
		s.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop did not drain bootstrap worker")
	}
}

func TestDashboardBootstrapSubsystem_DisabledDoesNotFire(t *testing.T) {
	s := newDashboardBootstrapSubsystem(false, 10*time.Millisecond)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(s.Stop)

	select {
	case <-s.Events():
		t.Fatal("disabled bootstrap fired")
	case <-time.After(50 * time.Millisecond):
	}
}
