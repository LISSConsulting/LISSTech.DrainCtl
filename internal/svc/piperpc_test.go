//go:build windows

package svc

import (
	"context"
	"errors"
	"testing"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/dashboard"
)

func stubBrokerSetupSeams(t *testing.T) {
	t.Helper()
	oldNormalize := normalizeRDConnectionBrokerFunc
	oldProbe := probeRDSessionCollectionsFunc
	oldPersist := persistRDConnectionBrokerFunc
	oldIdentity := serviceIdentityFunc
	t.Cleanup(func() {
		normalizeRDConnectionBrokerFunc = oldNormalize
		probeRDSessionCollectionsFunc = oldProbe
		persistRDConnectionBrokerFunc = oldPersist
		serviceIdentityFunc = oldIdentity
	})
}

func TestHandleBrokerSetup_InvalidBrokerDoesNotProbeOrPersist(t *testing.T) {
	stubBrokerSetupSeams(t)
	probed := false
	persisted := false
	probeRDSessionCollectionsFunc = func(context.Context, string) (dashboard.RDCollectionProbeResult, error) {
		probed = true
		return dashboard.RDCollectionProbeResult{}, nil
	}
	persistRDConnectionBrokerFunc = func(string) error {
		persisted = true
		return nil
	}

	_, err := (&serviceHandler{}).HandleBrokerSetup("broker with spaces")
	if err == nil {
		t.Fatal("expected invalid broker error")
	}
	if probed {
		t.Fatal("invalid broker invoked PowerShell probe")
	}
	if persisted {
		t.Fatal("invalid broker persisted configuration")
	}
}

func TestHandleBrokerSetup_ProbeFailureDoesNotPersist(t *testing.T) {
	stubBrokerSetupSeams(t)
	probeErr := errors.New("RemoteDesktop unavailable")
	persisted := false
	serviceIdentityFunc = func() (string, error) { return `NT AUTHORITY\SYSTEM`, nil }
	probeRDSessionCollectionsFunc = func(_ context.Context, broker string) (dashboard.RDCollectionProbeResult, error) {
		if broker != "rdc.example.test" {
			t.Fatalf("probe broker = %q, want rdc.example.test", broker)
		}
		return dashboard.RDCollectionProbeResult{}, probeErr
	}
	persistRDConnectionBrokerFunc = func(string) error {
		persisted = true
		return nil
	}

	_, err := (&serviceHandler{}).HandleBrokerSetup(" rdc.example.test ")
	if !errors.Is(err, probeErr) {
		t.Fatalf("error = %v, want %v", err, probeErr)
	}
	if persisted {
		t.Fatal("failed probe persisted configuration")
	}
}

func TestHandleBrokerSetup_PersistenceFailureDoesNotSucceed(t *testing.T) {
	stubBrokerSetupSeams(t)
	persistErr := errors.New("config file locked")
	serviceIdentityFunc = func() (string, error) { return `NT AUTHORITY\SYSTEM`, nil }
	probeRDSessionCollectionsFunc = func(context.Context, string) (dashboard.RDCollectionProbeResult, error) {
		return dashboard.RDCollectionProbeResult{CollectionCount: 2, SessionHostCount: 5}, nil
	}
	persistRDConnectionBrokerFunc = func(broker string) error {
		if broker != "rdc.example.test" {
			t.Fatalf("persist broker = %q, want rdc.example.test", broker)
		}
		return persistErr
	}

	result, err := (&serviceHandler{}).HandleBrokerSetup("rdc.example.test")
	if !errors.Is(err, persistErr) {
		t.Fatalf("error = %v, want %v", err, persistErr)
	}
	if result != nil {
		t.Fatalf("result = %+v, want nil on persistence failure", result)
	}
}

func TestHandleBrokerSetup_PersistsOnlyAfterSuccessfulServiceIdentityProbe(t *testing.T) {
	stubBrokerSetupSeams(t)
	var calls []string
	normalizeRDConnectionBrokerFunc = func(broker string) (string, error) {
		calls = append(calls, "normalize:"+broker)
		return "rdc.example.test", nil
	}
	serviceIdentityFunc = func() (string, error) {
		calls = append(calls, "identity")
		return `NT AUTHORITY\SYSTEM`, nil
	}
	probeRDSessionCollectionsFunc = func(_ context.Context, broker string) (dashboard.RDCollectionProbeResult, error) {
		calls = append(calls, "probe:"+broker)
		return dashboard.RDCollectionProbeResult{CollectionCount: 3, SessionHostCount: 9}, nil
	}
	persistRDConnectionBrokerFunc = func(broker string) error {
		calls = append(calls, "persist:"+broker)
		return nil
	}

	result, err := (&serviceHandler{}).HandleBrokerSetup(" rdc.example.test ")
	if err != nil {
		t.Fatalf("HandleBrokerSetup: %v", err)
	}
	want := struct {
		broker      string
		collections int
		hosts       int
		identity    string
	}{
		broker:      "rdc.example.test",
		collections: 3,
		hosts:       9,
		identity:    `NT AUTHORITY\SYSTEM`,
	}
	if result.ConnectionBroker != want.broker || result.CollectionCount != want.collections || result.SessionHostCount != want.hosts || result.ServiceIdentity != want.identity {
		t.Fatalf("result = %+v, want broker=%q collections=%d hosts=%d identity=%q", result, want.broker, want.collections, want.hosts, want.identity)
	}
	calls = append(calls, "respond")
	if got, wantCalls := calls, []string{"normalize: rdc.example.test ", "identity", "probe:rdc.example.test", "persist:rdc.example.test", "respond"}; len(got) != len(wantCalls) || got[0] != wantCalls[0] || got[1] != wantCalls[1] || got[2] != wantCalls[2] || got[3] != wantCalls[3] || got[4] != wantCalls[4] {
		t.Fatalf("calls = %v, want %v", got, wantCalls)
	}
}
