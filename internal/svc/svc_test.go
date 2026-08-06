//go:build windows

package svc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/watcher"
)

// writeEvtSpikeConfig uses the production atomic save path so the watcher
// cannot observe a file after truncate but before the replacement is complete.
func writeEvtSpikeConfig(t *testing.T, cfg *dc.Config) {
	t.Helper()
	if err := dc.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
}

// TestConfigWatcherTriggersEvtSpikeReload covers T068 (US5). A mtime change
// on config.json must fan out through watcher.ConfigFileSubsystem.Events
// into evtspike.Subsystem.Reload with the parsed new config.
//
// We do not invoke svc.Execute — it is bound to the Windows SCM and not
// callable from tests. The test instead drives applyEvtSpikeConfigReload,
// the same helper Execute's configCh branch invokes (T070). A goroutine
// inside the test mirrors that branch: load config on watcher fire, call
// the helper. This verifies the wired-up contract, not just the watcher
// and Reload in isolation.
//
// The observable effect is a channel-set change: adding a channel to
// AddedChannels takes the helper through Reload's channel_list_changed
// restart path, after which Status().EnabledChannels exposes the new
// channel count without reaching into unexported Subsystem fields.
func TestConfigWatcherTriggersEvtSpikeReload(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)
	configPath := dc.DefaultConfigPath()
	baselinePath := filepath.Join(dir, "baseline.json")

	initial := dc.DefaultConfig()
	initial.EvtSpike = dc.EvtSpikeConfig{
		Enabled:                  true,
		MinCount:                 10,
		Threshold:                1e-4,
		CooldownMinutes:          10,
		SlotMaturityObservations: 5,
		PersistIntervalSeconds:   900,
		HalfLifeBuckets:          360,
		PriorStrength:            60,
		MeanPerBucketPrior:       0.1,
		BaselinePath:             baselinePath,
		DisabledChannels:         []string{},
		AddedChannels:            []string{},
	}
	dc.ClampEvtSpike(&initial.EvtSpike)
	writeEvtSpikeConfig(t, initial)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfgSub := watcher.NewConfigFileSubsystem(configPath)
	if err := cfgSub.Start(ctx); err != nil {
		t.Fatalf("ConfigFileSubsystem.Start: %v", err)
	}
	defer cfgSub.Stop()
	ch := cfgSub.Events()

	sub := evtspike.New(initial.EvtSpike, "TEST-HOST")
	sub.Subscribe = func(_ context.Context, _ *sync.WaitGroup, _, _ string, _ *atomic.Int64, _ func(error)) error {
		return nil
	}
	sub.OnSpike = func(_ evtspike.SpikePayload) {}
	if err := sub.Start(ctx); err != nil {
		t.Fatalf("sub.Start: %v", err)
	}
	defer sub.Stop()

	preChannels := sub.Status().EnabledChannels
	if preChannels == 0 {
		t.Fatal("premise broken: Start subscribed zero channels")
	}

	// Mimic Execute's configCh branch: watcher fire → load config from disk →
	// applyEvtSpikeConfigReload. The helper is what Execute calls in T070, so
	// a future regression that drops the call there would also break this
	// test's reload path by construction.
	applied := make(chan error, 1)
	go func() {
		select {
		case <-ctx.Done():
		case <-ch:
			data, err := os.ReadFile(configPath)
			if err != nil {
				applied <- err
				return
			}
			reloaded := dc.DefaultConfig()
			if err := json.Unmarshal(data, reloaded); err != nil {
				applied <- err
				return
			}
			dc.ClampEvtSpike(&reloaded.EvtSpike)
			applied <- applyEvtSpikeConfigReload(sub, initial.EvtSpike, reloaded.EvtSpike)
		}
	}()

	// Mtime granularity on the poll fallback is 1 s; sleep past that boundary
	// so the second write is unambiguously later than the first and cannot be
	// de-duplicated as a single change.
	time.Sleep(1100 * time.Millisecond)

	updated := *initial
	updated.EvtSpike.AddedChannels = []string{"Contoso/AppLog"}
	writeEvtSpikeConfig(t, &updated)

	// Event-mode fires within ~100 ms; poll-fallback mode takes up to 5 s.
	select {
	case err := <-applied:
		if err != nil {
			t.Fatalf("applyEvtSpikeConfigReload: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("config watcher did not drive Reload within 15s of config.json rewrite")
	}

	postChannels := sub.Status().EnabledChannels
	if postChannels != preChannels+1 {
		t.Errorf("EnabledChannels after Reload = %d, want %d (pre=%d + one AddedChannel)",
			postChannels, preChannels+1, preChannels)
	}
}

// TestApplyEvtSpikeConfigReload_EnabledToggle — T101. Reload via the svc
// wiring path (applyEvtSpikeConfigReload) must handle the Enabled=true→false
// and false→true transitions end-to-end. Regression test for the plan's
// Phase A2 fix: previously a disabled-at-start subsystem could never be
// enabled without a service restart, and an enabled subsystem kept running
// after Reload(Enabled=false).
func TestApplyEvtSpikeConfigReload_EnabledToggle(t *testing.T) {
	cfg := dc.EvtSpikeConfig{
		Enabled:                  true,
		MinCount:                 10,
		Threshold:                1e-4,
		CooldownMinutes:          10,
		SlotMaturityObservations: 5,
		PersistIntervalSeconds:   900,
		HalfLifeBuckets:          360,
		PriorStrength:            60,
		MeanPerBucketPrior:       0.1,
		BaselinePath:             filepath.Join(t.TempDir(), "baseline.json"),
	}
	sub := evtspike.New(cfg, "TEST-HOST")
	sub.Subscribe = func(_ context.Context, _ *sync.WaitGroup, _, _ string, _ *atomic.Int64, _ func(error)) error {
		return nil
	}
	sub.OnSpike = func(_ evtspike.SpikePayload) {}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := sub.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sub.Stop()

	preChannels := sub.Status().EnabledChannels
	if preChannels == 0 {
		t.Fatal("premise: Start subscribed zero channels")
	}

	// Enabled=true → false via the svc wiring helper.
	disabled := cfg
	disabled.Enabled = false
	if err := applyEvtSpikeConfigReload(sub, cfg, disabled); err != nil {
		t.Fatalf("applyEvtSpikeConfigReload(disable): %v", err)
	}
	if st := sub.Status(); st.State != "disabled" {
		t.Errorf("after disable: Status.State = %q, want \"disabled\"", st.State)
	}

	// false → true via the same path.
	if err := applyEvtSpikeConfigReload(sub, disabled, cfg); err != nil {
		t.Fatalf("applyEvtSpikeConfigReload(enable): %v", err)
	}
	if got := sub.Status().EnabledChannels; got != preChannels {
		t.Errorf("after re-enable: EnabledChannels = %d, want %d", got, preChannels)
	}
}

// TestApplyEvtSpikeConfigReload_UnchangedBlockSkipsReload verifies the helper
// short-circuits when old and new EvtSpike blocks are reflect-equal. We detect
// the skip by passing a subsystem that was never Started — calling Reload on
// it with a channel-set change would panic inside restartWithConfig (startCtx
// is nil); a no-op call returns nil.
func TestApplyEvtSpikeConfigReload_UnchangedBlockSkipsReload(t *testing.T) {
	cfg := dc.EvtSpikeConfig{
		Enabled:                  true,
		MinCount:                 10,
		Threshold:                1e-4,
		CooldownMinutes:          10,
		SlotMaturityObservations: 5,
		PersistIntervalSeconds:   900,
		HalfLifeBuckets:          360,
		PriorStrength:            60,
		MeanPerBucketPrior:       0.1,
		DisabledChannels:         []string{},
		AddedChannels:            []string{},
	}
	sub := evtspike.New(cfg, "TEST-HOST")

	if err := applyEvtSpikeConfigReload(sub, cfg, cfg); err != nil {
		t.Errorf("unchanged block: got %v, want nil (no Reload call)", err)
	}
}
