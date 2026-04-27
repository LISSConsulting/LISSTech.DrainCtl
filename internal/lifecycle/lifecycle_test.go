//go:build windows

package lifecycle

import (
	"context"
	"testing"
)

// noopSubsystem proves the Subsystem interface compiles against a trivial
// implementation. The test exists to fail at compile time if the interface
// shape ever drifts away from what existing implementers (evtspike, the
// upcoming updater) provide.
type noopSubsystem struct{}

func (noopSubsystem) Start(ctx context.Context) error { return nil }
func (noopSubsystem) Stop()                           {}

func TestSubsystemInterfaceCompiles(t *testing.T) {
	var s Subsystem = noopSubsystem{}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("noop Start: %v", err)
	}
	s.Stop()
}

// statusableSubsystem proves Statusable is independently satisfiable.
type statusableSubsystem struct{ noopSubsystem }

func (statusableSubsystem) Status() any { return "ok" }

func TestStatusableInterfaceCompiles(t *testing.T) {
	var s Statusable = statusableSubsystem{}
	if got := s.Status(); got != "ok" {
		t.Fatalf("Status = %v, want \"ok\"", got)
	}

	// Subsystems may implement both — a type assertion is the documented
	// detection pattern.
	var sub Subsystem = statusableSubsystem{}
	if _, ok := sub.(Statusable); !ok {
		t.Fatal("statusableSubsystem must satisfy Statusable when accessed via Subsystem")
	}
}
