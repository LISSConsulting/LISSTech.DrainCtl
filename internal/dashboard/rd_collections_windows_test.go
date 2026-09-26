//go:build windows

package dashboard

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRDCollectionResolverJSONSnapshots(t *testing.T) {
	tests := []struct {
		name string
		json string
		want map[string]string
	}{
		{name: "zero", json: `[]`, want: map[string]string{"rd01": ""}},
		{name: "broker FQDN requested short hostname", json: `[ {"host":"rd01.example.test","collection":"Apps"} ]`, want: map[string]string{"rd01": "Apps"}},
		{name: "broker short hostname requested FQDN", json: `[ {"host":"rd01","collection":"Apps"} ]`, want: map[string]string{"rd01.example.test": "Apps", "RD01": "Apps"}},
		{name: "many", json: `[{"host":"rd01.example.test","collection":"Apps"},{"host":"rd02.example.test","collection":"Desktops"}]`, want: map[string]string{"rd01": "Apps", "rd02.example.test": "Desktops"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := newRDCollectionResolver(func(context.Context, string) ([]byte, error) {
				return []byte(test.json), nil
			})
			resolver.refresh(context.Background())
			for host, want := range test.want {
				if got := resolver.CollectionFor(host); got != want {
					t.Errorf("CollectionFor(%q) = %q, want %q", host, got, want)
				}
			}
		})
	}
}

func TestProbeRDSessionCollectionsCountsCollectionsAndHosts(t *testing.T) {
	tests := []struct {
		name            string
		payload         string
		wantCollections int
		wantHosts       int
	}{
		{name: "zero", payload: `[]`},
		{
			name:            "one",
			payload:         `[{"host":"rd01.example.test","collection":"Apps"}]`,
			wantCollections: 1,
			wantHosts:       1,
		},
		{
			name:            "many",
			payload:         `[{"host":"rd01.example.test","collection":"Apps"},{"host":"rd02.example.test","collection":"Apps"},{"host":"rd03.example.test","collection":"Desktops"}]`,
			wantCollections: 2,
			wantHosts:       3,
		},
		{
			name:            "duplicate records",
			payload:         `[{"host":"rd01.example.test","collection":"Apps"},{"host":"RD01.EXAMPLE.TEST.","collection":" Apps "},{"host":"rd02.example.test","collection":"Apps"}]`,
			wantCollections: 1,
			wantHosts:       2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := probeRDSessionCollections(context.Background(), "broker.example.test", func(_ context.Context, broker string) ([]byte, error) {
				if broker != "broker.example.test" {
					t.Errorf("broker = %q, want broker.example.test", broker)
				}
				return []byte(test.payload), nil
			})
			if err != nil {
				t.Fatalf("probeRDSessionCollections: %v", err)
			}
			if result.CollectionCount != test.wantCollections || result.SessionHostCount != test.wantHosts {
				t.Errorf("result = %+v, want collections=%d hosts=%d", result, test.wantCollections, test.wantHosts)
			}
		})
	}
}

func TestProbeRDSessionCollectionsRejectsMalformedPayload(t *testing.T) {
	for _, payload := range []string{
		`not JSON`,
		`null`,
		`{"host":"rd01.example.test","collection":"Apps"}`,
		`[{"host":"","collection":"Apps"}]`,
		`[{"host":"rd01.example.test","collection":" "}]`,
	} {
		t.Run(payload, func(t *testing.T) {
			_, err := probeRDSessionCollections(context.Background(), "", func(context.Context, string) ([]byte, error) {
				return []byte(payload), nil
			})
			if err == nil {
				t.Fatal("probeRDSessionCollections accepted a malformed payload")
			}
		})
	}
}

func TestProbeRDSessionCollectionsReturnsLoaderError(t *testing.T) {
	want := errors.New("broker unavailable")
	_, err := probeRDSessionCollections(context.Background(), "", func(context.Context, string) ([]byte, error) {
		return nil, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("probeRDSessionCollections error = %v, want %v", err, want)
	}
}

func TestRDCollectionPowerShellSerializesRecordsArray(t *testing.T) {
	if !strings.Contains(rdCollectionPowerShell, "$records = @(") {
		t.Fatal("PowerShell script does not collect records into an array")
	}
	if !strings.Contains(rdCollectionPowerShell, "ConvertTo-Json -InputObject $records -Compress") {
		t.Fatal("PowerShell script does not serialize the records array directly")
	}
}

func TestRDCollectionResolverRejectsAmbiguousAndConflictingMappings(t *testing.T) {
	resolver := newRDCollectionResolver(func(context.Context, string) ([]byte, error) {
		return []byte(`[
			{"host":"rd01.a.example.test","collection":"Apps"},
			{"host":"rd01.b.example.test","collection":"Desktops"},
			{"host":"rd02.example.test","collection":"Apps"},
			{"host":"RD02.EXAMPLE.TEST","collection":"Desktops"},
			{"host":"rd03.example.test","collection":"Apps"},
			{"host":"rd03.example.test","collection":"Apps"}
		]`), nil
	})
	resolver.refresh(context.Background())

	for _, host := range []string{"rd01", "rd02", "rd02.example.test"} {
		if got := resolver.CollectionFor(host); got != "" {
			t.Errorf("CollectionFor(%q) = %q, want empty for ambiguous/conflicting mapping", host, got)
		}
	}
	for host, want := range map[string]string{
		"rd01.a.example.test": "Apps",
		"rd01.b.example.test": "Desktops",
	} {
		if got := resolver.CollectionFor(host); got != want {
			t.Errorf("CollectionFor(%q) = %q, want exact mapping %q", host, got, want)
		}
	}
	if got := resolver.CollectionFor("RD03"); got != "Apps" {
		t.Errorf("duplicate identical memberships should remain resolvable, got %q", got)
	}
}

func TestRDCollectionResolverDoesNotMatchMissingFQDNByShortName(t *testing.T) {
	resolver := newRDCollectionResolver(func(context.Context, string) ([]byte, error) {
		return []byte(`[{"host":"rd01.example.test","collection":"Apps"}]`), nil
	})
	resolver.refresh(context.Background())

	if got := resolver.CollectionFor("rd01.other.example.test"); got != "" {
		t.Fatalf("CollectionFor missing FQDN = %q, want empty", got)
	}
	if got := resolver.CollectionFor("rd01"); got != "Apps" {
		t.Fatalf("CollectionFor short hostname = %q, want Apps", got)
	}
}

func TestRDCollectionResolverRetainsLastGoodSnapshot(t *testing.T) {
	var attempt atomic.Int32
	resolver := newRDCollectionResolver(func(context.Context, string) ([]byte, error) {
		switch attempt.Add(1) {
		case 1:
			return []byte(`[{"host":"rd01.example.test","collection":"Apps"}]`), nil
		case 2:
			return nil, errors.New("broker unavailable")
		default:
			return []byte(`not JSON`), nil
		}
	})
	resolver.refresh(context.Background())
	resolver.refresh(context.Background())
	resolver.refresh(context.Background())

	if got := resolver.CollectionFor("rd01"); got != "Apps" {
		t.Fatalf("CollectionFor retained snapshot = %q, want Apps", got)
	}
}

func TestRDCollectionResolverRetainsSnapshotForMalformedPayloads(t *testing.T) {
	payloads := []string{
		`null`,
		`{"host":"rd02.example.test","collection":"Desktops"}`,
		`[{"host":"","collection":"Desktops"}]`,
		`[{"host":"rd02.example.test","collection":" "}]`,
	}
	for _, payload := range payloads {
		t.Run(payload, func(t *testing.T) {
			var call atomic.Int32
			resolver := newRDCollectionResolver(func(context.Context, string) ([]byte, error) {
				if call.Add(1) == 1 {
					return []byte(`[{"host":"rd01.example.test","collection":"Apps"}]`), nil
				}
				return []byte(payload), nil
			})
			resolver.refresh(context.Background())
			resolver.refresh(context.Background())
			if got := resolver.CollectionFor("rd01"); got != "Apps" {
				t.Fatalf("malformed payload replaced snapshot: got %q, want Apps", got)
			}
		})
	}
}

func TestRDCollectionResolverEmptyArrayClearsSnapshot(t *testing.T) {
	var call atomic.Int32
	resolver := newRDCollectionResolver(func(context.Context, string) ([]byte, error) {
		if call.Add(1) == 1 {
			return []byte(`[{"host":"rd01.example.test","collection":"Apps"}]`), nil
		}
		return []byte(`[]`), nil
	})
	resolver.refresh(context.Background())
	resolver.refresh(context.Background())

	if got := resolver.CollectionFor("rd01"); got != "" {
		t.Fatalf("empty discovery result retained snapshot: got %q, want empty", got)
	}
}

func TestRDCollectionResolverRunRefreshesInitiallyPeriodicallyAndOnBrokerUpdate(t *testing.T) {
	var mu sync.Mutex
	var brokers []string
	loaded := make(chan struct{}, 8)
	resolver := newRDCollectionResolver(func(_ context.Context, broker string) ([]byte, error) {
		mu.Lock()
		brokers = append(brokers, broker)
		mu.Unlock()
		loaded <- struct{}{}
		return []byte(`[]`), nil
	})
	resolver.refreshInterval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		resolver.Run(ctx, "initial-broker")
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	awaitRDCollectionLoad(t, loaded) // immediate
	awaitRDCollectionLoad(t, loaded) // periodic
	resolver.UpdateBroker("updated-broker")

	deadline := time.After(time.Second)
	for {
		mu.Lock()
		found := false
		for _, broker := range brokers {
			found = found || broker == "updated-broker"
		}
		mu.Unlock()
		if found {
			break
		}
		select {
		case <-loaded:
		case <-deadline:
			t.Fatal("broker update did not trigger a refresh")
		}
	}
}

func TestRDCollectionResolverBrokerUpdatesCoalesce(t *testing.T) {
	resolver := newRDCollectionResolver(func(context.Context, string) ([]byte, error) {
		return []byte(`[]`), nil
	})

	resolver.UpdateBroker("one")
	resolver.UpdateBroker("two")
	resolver.UpdateBroker("three")

	select {
	case <-resolver.trigger:
	default:
		t.Fatal("broker updates did not schedule a refresh")
	}
	select {
	case <-resolver.trigger:
		t.Fatal("broker updates scheduled more than one pending refresh")
	default:
	}
	resolver.brokerMu.RLock()
	broker := resolver.broker
	resolver.brokerMu.RUnlock()
	if broker != "three" {
		t.Fatalf("latest broker = %q, want three", broker)
	}
}

func TestRDCollectionResolverRunPreservesPreStartBrokerUpdate(t *testing.T) {
	loaded := make(chan string, 2)
	resolver := newRDCollectionResolver(func(_ context.Context, broker string) ([]byte, error) {
		loaded <- broker
		return []byte(`[]`), nil
	})
	resolver.UpdateBroker("updated-broker")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		resolver.Run(ctx, "initial-broker")
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	select {
	case broker := <-loaded:
		if broker != "updated-broker" {
			t.Fatalf("initial refresh broker = %q, want updated-broker", broker)
		}
	case <-time.After(time.Second):
		t.Fatal("resolver did not refresh")
	}
}

func TestRDCollectionResolverRunPassesCancellationToLoader(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	resolver := newRDCollectionResolver(func(ctx context.Context, _ string) ([]byte, error) {
		close(started)
		<-ctx.Done()
		close(finished)
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		resolver.Run(ctx, "")
		close(done)
	}()
	<-started
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("loader did not receive Run cancellation")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestNewRDCollectionCommandUsesFixedPowerShellAndBrokerEnvironment(t *testing.T) {
	t.Setenv(rdConnectionBrokerEnv, "inherited-broker")

	cmd := newRDCollectionCommand(context.Background(), "broker.example.test")

	wantArgs := []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", rdCollectionPowerShell}
	if len(cmd.Args) != len(wantArgs) {
		t.Fatalf("command arguments = %#v, want %#v", cmd.Args, wantArgs)
	}
	for i := range wantArgs {
		if cmd.Args[i] != wantArgs[i] {
			t.Fatalf("command argument %d = %q, want %q", i, cmd.Args[i], wantArgs[i])
		}
	}

	configuredBroker := false
	for _, entry := range cmd.Env {
		if entry == rdConnectionBrokerEnv+"=inherited-broker" {
			t.Fatal("command inherited a Connection Broker environment value")
		}
		if entry == rdConnectionBrokerEnv+"=broker.example.test" {
			configuredBroker = true
		}
	}
	if !configuredBroker {
		t.Fatal("command did not set the configured Connection Broker environment value")
	}
}

func TestRDCollectionEnvironmentOmitsEmptyBroker(t *testing.T) {
	t.Setenv(rdConnectionBrokerEnv, "inherited-broker")

	for _, entry := range rdCollectionEnvironment("") {
		if entry == rdConnectionBrokerEnv+"=inherited-broker" {
			t.Fatal("empty broker inherited a Connection Broker environment value")
		}
	}
}

func awaitRDCollectionLoad(t *testing.T, loaded <-chan struct{}) {
	t.Helper()
	select {
	case <-loaded:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for collection refresh")
	}
}
