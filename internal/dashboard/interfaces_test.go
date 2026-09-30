//go:build windows

package dashboard

import (
	"context"
	"reflect"
	"testing"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

type interfaceTestAcceptedWriter struct{}

func (interfaceTestAcceptedWriter) UpdateAccepted(context.Context, string, string, telemetry.SessionDropObservation) (bool, error) {
	return false, nil
}

type interfaceTestInboxWaker struct{}

func (interfaceTestInboxWaker) WakeSessionDropInbox() {}

var _ acceptedResultWriter = interfaceTestAcceptedWriter{}
var _ sessionDropInboxWaker = interfaceTestInboxWaker{}

func TestSessionDropInboxWakerCarriesNoObservation(t *testing.T) {
	method, ok := reflect.TypeOf((*sessionDropInboxWaker)(nil)).Elem().MethodByName("WakeSessionDropInbox")
	if !ok {
		t.Fatal("session-drop inbox waker method is missing")
	}
	if method.Type.NumIn() != 0 {
		t.Fatalf("WakeSessionDropInbox arguments = %d, want 0; wake-only contract must carry no observation payload", method.Type.NumIn())
	}
}
