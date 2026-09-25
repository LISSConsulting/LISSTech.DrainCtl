//go:build windows

package svc

import (
	"encoding/json"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/dashboard"
)

// ── applyRemoteConfig ─────────────────────────────────────────────────────────

func TestApplyRemoteConfig_SetsTargets(t *testing.T) {
	cfg := &dc.ServiceConfig{}
	targets := []dc.NotificationTarget{}
	remote := &dashboard.RemoteSettings{
		Notifications:           []dc.NotificationTarget{{Type: "webhook", URL: "https://example.com"}},
		SessionWarningThreshold: -1, // below zero — should not update
		GracePeriod:             0,  // zero — should not update
	}
	applyRemoteConfig(remote, cfg, &targets, nil)
	if len(targets) != 1 {
		t.Errorf("targets len = %d, want 1", len(targets))
	}
}

func TestApplyRemoteConfig_SessionThresholdClampsAbove100(t *testing.T) {
	cfg := &dc.ServiceConfig{SessionWarningThreshold: 80}
	targets := []dc.NotificationTarget{}
	remote := &dashboard.RemoteSettings{SessionWarningThreshold: 999, GracePeriod: 0}
	applyRemoteConfig(remote, cfg, &targets, nil)
	if cfg.SessionWarningThreshold != 100 {
		t.Errorf("SessionWarningThreshold = %d, want 100 (clamped from 999)", cfg.SessionWarningThreshold)
	}
}

func TestApplyRemoteConfig_SessionThresholdZeroAllowed(t *testing.T) {
	cfg := &dc.ServiceConfig{SessionWarningThreshold: 80}
	targets := []dc.NotificationTarget{}
	remote := &dashboard.RemoteSettings{SessionWarningThreshold: 0, GracePeriod: 0}
	applyRemoteConfig(remote, cfg, &targets, nil)
	if cfg.SessionWarningThreshold != 0 {
		t.Errorf("SessionWarningThreshold = %d, want 0 (zero disables session warnings)", cfg.SessionWarningThreshold)
	}
}

func TestApplyRemoteConfig_SessionThresholdNegativeSkipped(t *testing.T) {
	cfg := &dc.ServiceConfig{SessionWarningThreshold: 80}
	targets := []dc.NotificationTarget{}
	remote := &dashboard.RemoteSettings{SessionWarningThreshold: -1, GracePeriod: 0}
	applyRemoteConfig(remote, cfg, &targets, nil)
	if cfg.SessionWarningThreshold != 80 {
		t.Errorf("SessionWarningThreshold = %d, want 80 (negative should be skipped)", cfg.SessionWarningThreshold)
	}
}

func TestApplyRemoteConfig_GracePeriodClampsAbove1440(t *testing.T) {
	cfg := &dc.ServiceConfig{GracePeriod: 30 * time.Minute}
	targets := []dc.NotificationTarget{}
	remote := &dashboard.RemoteSettings{SessionWarningThreshold: -1, GracePeriod: 9999}
	applyRemoteConfig(remote, cfg, &targets, nil)
	if cfg.GracePeriod != 1440*time.Minute {
		t.Errorf("GracePeriod = %v, want %v (clamped from 9999 min)", cfg.GracePeriod, 1440*time.Minute)
	}
}

func TestApplyRemoteConfig_GracePeriodValidValue(t *testing.T) {
	cfg := &dc.ServiceConfig{GracePeriod: 30 * time.Minute}
	targets := []dc.NotificationTarget{}
	remote := &dashboard.RemoteSettings{SessionWarningThreshold: -1, GracePeriod: 60}
	applyRemoteConfig(remote, cfg, &targets, nil)
	if cfg.GracePeriod != 60*time.Minute {
		t.Errorf("GracePeriod = %v, want %v", cfg.GracePeriod, 60*time.Minute)
	}
}

func TestApplyRemoteConfig_GracePeriodZeroSkipped(t *testing.T) {
	cfg := &dc.ServiceConfig{GracePeriod: 30 * time.Minute}
	targets := []dc.NotificationTarget{}
	remote := &dashboard.RemoteSettings{SessionWarningThreshold: -1, GracePeriod: 0}
	applyRemoteConfig(remote, cfg, &targets, nil)
	if cfg.GracePeriod != 30*time.Minute {
		t.Errorf("GracePeriod = %v, want %v (zero should be skipped)", cfg.GracePeriod, 30*time.Minute)
	}
}

func TestApplyRemoteConfig_PollIntervalAppliedAndClamped(t *testing.T) {
	cfg := &dc.ServiceConfig{PollInterval: 60 * time.Second}
	targets := []dc.NotificationTarget{}
	// In-range value is applied verbatim.
	remote := &dashboard.RemoteSettings{PollInterval: 30}
	applyRemoteConfig(remote, cfg, &targets, nil)
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %v, want 30s", cfg.PollInterval)
	}
	// Below floor clamps to 10.
	remote = &dashboard.RemoteSettings{PollInterval: 1}
	applyRemoteConfig(remote, cfg, &targets, nil)
	if cfg.PollInterval != 10*time.Second {
		t.Errorf("PollInterval = %v, want 10s (clamped from 1)", cfg.PollInterval)
	}
	// Above ceiling clamps to MaxPollInterval.
	remote = &dashboard.RemoteSettings{PollInterval: dc.MaxPollInterval + 1000}
	applyRemoteConfig(remote, cfg, &targets, nil)
	if cfg.PollInterval != time.Duration(dc.MaxPollInterval)*time.Second {
		t.Errorf("PollInterval = %v, want %ds (clamped)", cfg.PollInterval, dc.MaxPollInterval)
	}
	// Zero is treated as "field absent" — don't overwrite.
	cfg.PollInterval = 45 * time.Second
	remote = &dashboard.RemoteSettings{PollInterval: 0}
	applyRemoteConfig(remote, cfg, &targets, nil)
	if cfg.PollInterval != 45*time.Second {
		t.Errorf("PollInterval = %v, want 45s (zero should not overwrite)", cfg.PollInterval)
	}
}

func TestApplyRemoteConfig_EvtSpikeEnabledTogglesSubsystemCfg(t *testing.T) {
	cfg := &dc.ServiceConfig{}
	targets := []dc.NotificationTarget{}
	evt := &dc.EvtSpikeConfig{Enabled: false}
	remote := &dashboard.RemoteSettings{EvtSpike: &dashboard.RemoteEvtSpike{Enabled: true}}
	applyRemoteConfig(remote, cfg, &targets, evt)
	if !evt.Enabled {
		t.Error("EvtSpike.Enabled=true not propagated: evt.Enabled still false")
	}
	// Flip back to false via explicit value.
	remote = &dashboard.RemoteSettings{EvtSpike: &dashboard.RemoteEvtSpike{Enabled: false}}
	applyRemoteConfig(remote, cfg, &targets, evt)
	if evt.Enabled {
		t.Error("EvtSpike.Enabled=false not applied: evt.Enabled still true")
	}
	// Nil EvtSpike → do not change.
	evt.Enabled = true
	remote = &dashboard.RemoteSettings{EvtSpike: nil}
	applyRemoteConfig(remote, cfg, &targets, evt)
	if !evt.Enabled {
		t.Error("nil EvtSpike should preserve existing value (true)")
	}
}

// TestApplyRemoteConfig_EvtSpikePropagation_WireFormat pins the wire contract
// between the dashboard's GET /api/v1/config handler and the agent's
// FetchSettings decoder. The previous regression was caused by the JSON shape
// drifting (server emitted "evtspike":{"enabled":...}, client expected a flat
// "evtspike_enabled"), so the agent silently ignored the dashboard toggle.
// Decoding the on-wire shape the server actually emits must surface the flag.
func TestApplyRemoteConfig_EvtSpikePropagation_WireFormat(t *testing.T) {
	wire := []byte(`{"evtspike":{"enabled":true}}`)
	var remote dashboard.RemoteSettings
	if err := json.Unmarshal(wire, &remote); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if remote.EvtSpike == nil || !remote.EvtSpike.Enabled {
		t.Fatalf("EvtSpike not decoded: %+v", remote.EvtSpike)
	}
	cfg := &dc.ServiceConfig{}
	targets := []dc.NotificationTarget{}
	evt := &dc.EvtSpikeConfig{Enabled: false}
	applyRemoteConfig(&remote, cfg, &targets, evt)
	if !evt.Enabled {
		t.Error("wire-decoded EvtSpike.Enabled=true did not propagate to subsystem")
	}
}

func TestEffectiveUpdateConfig_DashboardOverridesLocalPolicy(t *testing.T) {
	local := dc.UpdateConfig{
		Enabled:      false,
		Channel:      dc.ChannelStable,
		PollInterval: dc.Duration(24 * time.Hour),
	}
	remotePolicy := dc.UpdateConfig{
		Enabled:      true,
		Channel:      dc.ChannelPrerelease,
		PollInterval: dc.Duration(6 * time.Hour),
	}
	remote := &dashboard.RemoteSettings{Update: &remotePolicy}

	if got := effectiveUpdateConfig(local, remote); got != remotePolicy {
		t.Errorf("effectiveUpdateConfig = %+v, want dashboard policy %+v", got, remotePolicy)
	}
}

func TestEffectiveUpdateConfig_MissingDashboardPolicyPreservesLocal(t *testing.T) {
	local := dc.UpdateConfig{
		Enabled:      true,
		Channel:      dc.ChannelStable,
		PollInterval: dc.Duration(12 * time.Hour),
	}
	if got := effectiveUpdateConfig(local, &dashboard.RemoteSettings{}); got != local {
		t.Errorf("effectiveUpdateConfig = %+v, want local policy %+v", got, local)
	}
}

// ── pruneNotifyState ──────────────────────────────────────────────────────────

func TestPruneNotifyState_RemovesStaleAlertEntry(t *testing.T) {
	state := &dc.NotifyState{
		LastAlertNotify:       map[string]time.Time{"https://old.example.com": time.Now()},
		LastSessionWarnNotify: map[string]time.Time{},
	}
	targets := []dc.NotificationTarget{
		{URL: "https://active.example.com"},
	}
	pruneNotifyState(state, targets)
	if _, ok := state.LastAlertNotify["https://old.example.com"]; ok {
		t.Error("stale alert entry should have been pruned")
	}
}

func TestPruneNotifyState_KeepsActiveAlertEntry(t *testing.T) {
	state := &dc.NotifyState{
		LastAlertNotify:       map[string]time.Time{"https://active.example.com": time.Now()},
		LastSessionWarnNotify: map[string]time.Time{},
	}
	targets := []dc.NotificationTarget{
		{URL: "https://active.example.com"},
	}
	pruneNotifyState(state, targets)
	if _, ok := state.LastAlertNotify["https://active.example.com"]; !ok {
		t.Error("active alert entry should have been kept")
	}
}

func TestPruneNotifyState_RemovesStaleSessionWarnEntry(t *testing.T) {
	state := &dc.NotifyState{
		LastAlertNotify:       map[string]time.Time{},
		LastSessionWarnNotify: map[string]time.Time{"https://old.example.com": time.Now()},
	}
	targets := []dc.NotificationTarget{
		{URL: "https://active.example.com"},
	}
	pruneNotifyState(state, targets)
	if _, ok := state.LastSessionWarnNotify["https://old.example.com"]; ok {
		t.Error("stale session_warning entry should have been pruned")
	}
}

func TestPruneNotifyState_KeepsActiveSessionWarnEntry(t *testing.T) {
	state := &dc.NotifyState{
		LastAlertNotify:       map[string]time.Time{},
		LastSessionWarnNotify: map[string]time.Time{"https://active.example.com": time.Now()},
	}
	targets := []dc.NotificationTarget{
		{URL: "https://active.example.com"},
	}
	pruneNotifyState(state, targets)
	if _, ok := state.LastSessionWarnNotify["https://active.example.com"]; !ok {
		t.Error("active session_warning entry should have been kept")
	}
}

func TestPruneNotifyState_EmptyTargets_ClearsAll(t *testing.T) {
	state := &dc.NotifyState{
		LastAlertNotify:       map[string]time.Time{"https://a.example.com": time.Now()},
		LastSessionWarnNotify: map[string]time.Time{"https://b.example.com": time.Now()},
	}
	pruneNotifyState(state, nil)
	if len(state.LastAlertNotify) != 0 {
		t.Errorf("LastAlertNotify should be empty after prune with no targets, got %d entries", len(state.LastAlertNotify))
	}
	if len(state.LastSessionWarnNotify) != 0 {
		t.Errorf("LastSessionWarnNotify should be empty after prune with no targets, got %d entries", len(state.LastSessionWarnNotify))
	}
}

func TestPruneNotifyState_SkipsBlankURLTargets(t *testing.T) {
	state := &dc.NotifyState{
		LastAlertNotify:       map[string]time.Time{"https://a.example.com": time.Now()},
		LastSessionWarnNotify: map[string]time.Time{},
	}
	// A target with an empty URL should NOT be treated as active.
	targets := []dc.NotificationTarget{{URL: ""}}
	pruneNotifyState(state, targets)
	if _, ok := state.LastAlertNotify["https://a.example.com"]; ok {
		t.Error("entry for non-blank URL should be pruned when only blank-URL targets remain")
	}
}

// TestApplyRemoteConfig_EvtSpikeFullKnobs_PropagatesAndClamps verifies every
// operator-safe evtspike field is overlaid onto the local config by
// applyRemoteConfig, with out-of-range values clamped to the documented
// bounds. This guards the contract documented at the top of
// overlayEvtSpikeFromRemote: "every operator-safe field from the dashboard
// is layered on top of the locally-loaded EvtSpikeConfig".
func TestApplyRemoteConfig_EvtSpikeFullKnobs_PropagatesAndClamps(t *testing.T) {
	cfg := &dc.ServiceConfig{}
	targets := []dc.NotificationTarget{}
	evt := &dc.EvtSpikeConfig{
		// Start from non-default local values so we can prove they got
		// replaced, not merged.
		MinCount:                 99,
		Threshold:                0.5,
		CooldownMinutes:          99,
		SlotMaturityObservations: 99,
		PersistIntervalSeconds:   9999,
		HalfLifeBuckets:          9999,
		PriorStrength:            9999,
		MeanPerBucketPrior:       99,
		DisabledChannels:         []string{"old"},
		AddedChannels:            []string{"old"},
		SecurityChannelEnabled:   false,
	}
	remote := &dashboard.RemoteSettings{
		EvtSpike: &dashboard.RemoteEvtSpike{
			Enabled:                  true,
			MinCount:                 25,
			Threshold:                5e-5,
			CooldownMinutes:          7,
			SlotMaturityObservations: 45,
			PersistIntervalSeconds:   1800,
			HalfLifeBuckets:          720,
			PriorStrength:            120,
			MeanPerBucketPrior:       0.25,
			DisabledChannels:         []string{"Setup"},
			AddedChannels:            []string{"Custom/Op"},
			SecurityChannelEnabled:   true,
		},
	}
	applyRemoteConfig(remote, cfg, &targets, evt)

	if !evt.Enabled {
		t.Error("Enabled not propagated")
	}
	if evt.MinCount != 25 {
		t.Errorf("MinCount = %d, want 25", evt.MinCount)
	}
	if evt.Threshold != 5e-5 {
		t.Errorf("Threshold = %g, want 5e-5", evt.Threshold)
	}
	if evt.CooldownMinutes != 7 {
		t.Errorf("CooldownMinutes = %d, want 7", evt.CooldownMinutes)
	}
	if evt.SlotMaturityObservations != 45 {
		t.Errorf("SlotMaturityObservations = %d, want 45", evt.SlotMaturityObservations)
	}
	if evt.PersistIntervalSeconds != 1800 {
		t.Errorf("PersistIntervalSeconds = %d, want 1800", evt.PersistIntervalSeconds)
	}
	if evt.HalfLifeBuckets != 720 {
		t.Errorf("HalfLifeBuckets = %d, want 720", evt.HalfLifeBuckets)
	}
	if evt.PriorStrength != 120 {
		t.Errorf("PriorStrength = %g, want 120", evt.PriorStrength)
	}
	if evt.MeanPerBucketPrior != 0.25 {
		t.Errorf("MeanPerBucketPrior = %g, want 0.25", evt.MeanPerBucketPrior)
	}
	if !evt.SecurityChannelEnabled {
		t.Error("SecurityChannelEnabled not propagated")
	}
	if len(evt.DisabledChannels) != 1 || evt.DisabledChannels[0] != "Setup" {
		t.Errorf("DisabledChannels = %v, want [Setup]", evt.DisabledChannels)
	}
	if len(evt.AddedChannels) != 1 || evt.AddedChannels[0] != "Custom/Op" {
		t.Errorf("AddedChannels = %v, want [Custom/Op]", evt.AddedChannels)
	}
}

func TestApplyRemoteConfig_EvtSpikeClampsOutOfRange(t *testing.T) {
	evt := &dc.EvtSpikeConfig{}
	remote := &dashboard.RemoteSettings{
		EvtSpike: &dashboard.RemoteEvtSpike{
			// One in-range value to prove clamping is per-field, not
			// "discard the whole block on any out-of-range".
			MinCount:        dc.MaxEvtSpikeMinCount * 10,
			Threshold:       dc.MaxEvtSpikeThreshold * 2,
			CooldownMinutes: dc.MaxEvtSpikeCooldownMinutes + 1,
			HalfLifeBuckets: 1, // below min
			PriorStrength:   -5,
		},
	}
	applyRemoteConfig(remote, &dc.ServiceConfig{}, &[]dc.NotificationTarget{}, evt)

	if evt.MinCount != dc.MaxEvtSpikeMinCount {
		t.Errorf("MinCount = %d, want %d (clamped to max)", evt.MinCount, dc.MaxEvtSpikeMinCount)
	}
	if evt.Threshold != dc.MaxEvtSpikeThreshold {
		t.Errorf("Threshold = %g, want %g (clamped to max)", evt.Threshold, dc.MaxEvtSpikeThreshold)
	}
	if evt.CooldownMinutes != dc.MaxEvtSpikeCooldownMinutes {
		t.Errorf("CooldownMinutes = %d, want %d (clamped to max)", evt.CooldownMinutes, dc.MaxEvtSpikeCooldownMinutes)
	}
	if evt.HalfLifeBuckets != dc.MinEvtSpikeHalfLifeBuckets {
		t.Errorf("HalfLifeBuckets = %d, want %d (clamped to min)", evt.HalfLifeBuckets, dc.MinEvtSpikeHalfLifeBuckets)
	}
	if evt.PriorStrength != dc.MinEvtSpikePriorStrength {
		t.Errorf("PriorStrength = %g, want %g (clamped to min)", evt.PriorStrength, dc.MinEvtSpikePriorStrength)
	}
}

// TestApplyRemoteConfig_EvtSpikeEmptySlicesClear verifies that sending an
// explicit empty slice on the wire CLEARS the agent's local list. This is
// the only sensible interpretation of "the dashboard operator pressed Clear
// on the disabled channels row".
func TestApplyRemoteConfig_EvtSpikeEmptySlicesClear(t *testing.T) {
	evt := &dc.EvtSpikeConfig{
		DisabledChannels: []string{"Setup", "System"},
		AddedChannels:    []string{"Custom-A"},
	}
	remote := &dashboard.RemoteSettings{
		EvtSpike: &dashboard.RemoteEvtSpike{
			DisabledChannels: []string{},
			AddedChannels:    []string{},
		},
	}
	applyRemoteConfig(remote, &dc.ServiceConfig{}, &[]dc.NotificationTarget{}, evt)
	if len(evt.DisabledChannels) != 0 {
		t.Errorf("DisabledChannels = %v, want [] (cleared)", evt.DisabledChannels)
	}
	if len(evt.AddedChannels) != 0 {
		t.Errorf("AddedChannels = %v, want [] (cleared)", evt.AddedChannels)
	}
}

// TestApplyRemoteConfig_EvtSpikeFullWireFormatRoundTrip pins the wire shape
// the dashboard emits via GET /api/v1/config: every operator-safe field must
// be present and parseable, and applying that decoded shape must populate the
// corresponding subsystem fields exactly.
func TestApplyRemoteConfig_EvtSpikeFullWireFormatRoundTrip(t *testing.T) {
	wire := []byte(`{
		"evtspike": {
			"enabled": true,
			"min_count": 25,
			"threshold": 5e-05,
			"cooldown_minutes": 7,
			"slot_maturity_observations": 45,
			"persist_interval_seconds": 1800,
			"half_life_buckets": 720,
			"prior_strength": 120,
			"mean_per_bucket_prior": 0.25,
			"disabled_channels": ["Setup"],
			"added_channels": ["Custom/Op"],
			"security_channel_enabled": true
		}
	}`)
	var remote dashboard.RemoteSettings
	if err := json.Unmarshal(wire, &remote); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if remote.EvtSpike == nil {
		t.Fatal("EvtSpike block missing from decoded payload")
	}
	evt := &dc.EvtSpikeConfig{}
	applyRemoteConfig(&remote, &dc.ServiceConfig{}, &[]dc.NotificationTarget{}, evt)
	if !evt.Enabled || evt.MinCount != 25 || evt.HalfLifeBuckets != 720 {
		t.Errorf("wire-decoded fields did not propagate: %+v", evt)
	}
	if !evt.SecurityChannelEnabled {
		t.Error("security_channel_enabled not decoded/propagated")
	}
}

func TestToDashboardCompletionPreservesCanonicalHost(t *testing.T) {
	completion := toDashboardCompletion(&dashboard.ForceUpdateCompletion{
		CommandID: "cmd-00001-001",
		Outcome:   "completed",
	}, "rdsh-01")
	if completion == nil {
		t.Fatal("toDashboardCompletion returned nil")
	}
	if completion.Host != "rdsh-01" {
		t.Errorf("Host = %q, want canonical checked host", completion.Host)
	}
}

func TestOverlayEvtSpikeFromRemote_ChannelCooldownPresence(t *testing.T) {
	local := &dc.EvtSpikeConfig{ChannelCooldownMinutes: map[string]int{"Application": 30}}
	overlayEvtSpikeFromRemote(local, &dashboard.RemoteEvtSpike{})
	if got := local.ChannelCooldownMinutes["Application"]; got != 30 {
		t.Errorf("omitted channel cooldowns = %v, want local override preserved", local.ChannelCooldownMinutes)
	}

	empty := map[string]int{}
	overlayEvtSpikeFromRemote(local, &dashboard.RemoteEvtSpike{ChannelCooldownMinutes: &empty})
	if len(local.ChannelCooldownMinutes) != 0 {
		t.Errorf("explicit empty channel cooldowns = %v, want cleared", local.ChannelCooldownMinutes)
	}
}
