//go:build windows

package evtspike

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

const (
	scoringIntervalSeconds  = 10
	defaultBaselineFilename = "evtspike-baseline.json"
	defaultChannelQuery     = `*[System[(Level<=4)]]`
)

// SubscribeFunc is the signature the Subsystem uses to subscribe a channel.
// Production wires this to Subscribe; tests swap in a no-op so scoreOnce can
// be driven by direct counter manipulation. The wg parameter owns the drain
// goroutine's lifetime: the caller (Subscribe itself) runs wg.Add(1) before
// launching the goroutine so Stop's wg.Wait cannot race Add.
//
// The loss callback is invoked asynchronously from the drain goroutine when
// the subscription is lost mid-run (handle invalidated, provider unloaded).
// nil is valid — tests that do not exercise the retry state machine pass
// nil. The callback MUST NOT block; the supervisor takes over from there.
type SubscribeFunc func(ctx context.Context, wg *sync.WaitGroup, channel, query string, counter *atomic.Int64, loss func(error)) error

// SubState is the per-channel subscription state for the state machine
// introduced in plan Phase B1. Status counts only channels in
// StateSubscribed toward EnabledChannels / MatureChannels.
type SubState int

const (
	StateSubscribed SubState = iota
	StateRetrying
	StateFailed
)

// String returns a stable lowercase name for logs + SSE payloads.
func (s SubState) String() string {
	switch s {
	case StateSubscribed:
		return "subscribed"
	case StateRetrying:
		return "retrying"
	case StateFailed:
		return "failed"
	}
	return "unknown"
}

// channelSubscription tracks per-channel retry state. All fields are
// guarded by Subsystem.mu.
type channelSubscription struct {
	name     string
	state    SubState
	attempts int   // consecutive retry attempts (reset on subscribed)
	lastErr  error // last error surfaced to retrying/failed states
}

const (
	subRetryInterval = 5 * time.Minute
	subRetryMaxTries = 12 // 12 × 5 min = 1 h
)

// PrivilegeFunc enables SeSecurityPrivilege on the current process token.
// Production wires this to EnableSecurityPrivilege; tests swap in a fake to
// simulate dedicated-account scenarios without touching the real token.
type PrivilegeFunc func() error

// Subsystem owns per-channel subscriptions, detectors, and the scoring and
// baseline-persistence loops inside the DrainCtl service. One instance per
// host. Create with New, set OnSpike, then Start(ctx). Stop flushes state and
// quiesces goroutines.
type Subsystem struct {
	cfg  dc.EvtSpikeConfig
	host string

	// OnSpike is invoked on each confirmed spike. Must be non-nil before Start.
	OnSpike func(SpikePayload)

	// OnStatusChange is invoked with the derived DetectorStatus whenever it
	// could have transitioned (post-initChannels at Start, post-bucket in the
	// scoring loop). Dedup of same-state emissions is the subscriber's
	// responsibility — the broker does this for SSE fan-out.
	OnStatusChange func(DetectorStatus)

	// Subscribe lets tests inject a fake; nil means production Subscribe.
	Subscribe SubscribeFunc

	// EnablePrivilege lets tests inject a fake SeSecurityPrivilege enabler.
	// Defaulted to EnableSecurityPrivilege by New.
	EnablePrivilege PrivilegeFunc

	// DisablePrivilege symmetrically lets tests inject a fake disabler.
	// Defaulted to DisableSecurityPrivilege by New. Called by Reload when the
	// Security-channel opt-in flips on→off so a later AddedChannels=Security
	// attempt cannot silently succeed against a token that still has the
	// privilege enabled from the prior opt-in window (plan Phase A3).
	DisablePrivilege PrivilegeFunc

	// Now lets tests inject a deterministic clock for the persistence write
	// timestamp. The scoring path always takes time from the caller.
	Now func() time.Time

	// PersistTickSource returns the channel that drives persistenceLoop and a
	// stop closure invoked on loop exit. Production uses time.NewTicker; tests
	// supply a channel they send on to trigger WriteBaseline deterministically.
	PersistTickSource func(time.Duration) (<-chan time.Time, func())

	baselinePath string

	// RetryTickSource feeds the supervisor goroutine. Defaults to a 5-min
	// ticker via defaultPersistTickSource style; tests inject a channel
	// they can send on for deterministic retry-cadence assertions.
	RetryTickSource func(time.Duration) (<-chan time.Time, func())

	mu            sync.Mutex
	detectors     map[string]*Detector
	counters      map[string]*atomic.Int64
	channels      []string
	subscriptions map[string]*channelSubscription // per-channel state machine
	startupErr    error
	lastSpikeAt   time.Time

	runMu    sync.Mutex
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	ctx      context.Context
	startCtx context.Context
}

// New returns an unstarted Subsystem. cfg is assumed already clamped by
// ClampEvtSpike on the caller.
func New(cfg dc.EvtSpikeConfig, host string) *Subsystem {
	s := &Subsystem{
		cfg:               cfg,
		host:              host,
		Subscribe:         Subscribe,
		EnablePrivilege:   EnableSecurityPrivilege,
		DisablePrivilege:  DisableSecurityPrivilege,
		Now:               time.Now,
		PersistTickSource: defaultPersistTickSource,
		RetryTickSource:   defaultPersistTickSource,
		detectors:         make(map[string]*Detector),
		counters:          make(map[string]*atomic.Int64),
		subscriptions:     make(map[string]*channelSubscription),
	}
	s.baselinePath = s.resolveBaselinePath()
	return s
}

// Start loads the baseline, resolves channels, subscribes each one, and
// launches the scoring and persistence goroutines. Per-channel subscribe
// errors are logged and the channel is skipped (FR-009); other channels
// continue. Returns an error only if OnSpike was not set.
//
// Start is idempotent w.r.t. cfg.Enabled: when Enabled=false it captures
// ctx as s.startCtx and returns without subscribing or launching loops, so
// a later Reload(Enabled=true) has a parent context to reuse.
func (s *Subsystem) Start(ctx context.Context) error {
	if s.OnSpike == nil {
		return fmt.Errorf("evtspike: Subsystem.OnSpike must be set before Start")
	}

	s.runMu.Lock()
	s.startCtx = ctx
	s.runMu.Unlock()

	if !s.cfg.Enabled {
		if s.OnStatusChange != nil {
			s.OnStatusChange(s.Status())
		}
		return nil
	}

	return s.startEnabled()
}

// startEnabled holds the live-run body of Start: baseline load, channel
// subscription, scoring+persistence goroutines. Factored so Start can skip
// it when Enabled=false and Reload can call it when flipping false→true
// without re-entering Start (which would double-capture startCtx).
func (s *Subsystem) startEnabled() error {
	bf, _ := LoadBaseline(s.baselinePath)

	s.runMu.Lock()
	parent := s.startCtx
	runCtx, cancel := context.WithCancel(parent)
	s.ctx = runCtx
	s.cancel = cancel
	s.runMu.Unlock()

	s.initChannels(runCtx, bf)

	if s.OnStatusChange != nil {
		s.OnStatusChange(s.Status())
	}

	s.wg.Add(3)
	go s.scoringLoop(runCtx)
	go s.persistenceLoop(runCtx)
	go s.supervisorLoop(runCtx)

	return nil
}

// supervisorLoop drives retries for channels in StateRetrying. On each
// RetryTickSource tick, iterates over retrying channels and re-invokes
// Subscribe; success transitions back to StateSubscribed, failure
// increments attempts until subRetryMaxTries → StateFailed.
func (s *Subsystem) supervisorLoop(ctx context.Context) {
	defer s.wg.Done()
	src := s.RetryTickSource
	if src == nil {
		src = defaultPersistTickSource
	}
	ch, stop := src(subRetryInterval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			s.retryOnce(ctx)
		}
	}
}

// retryOnce attempts one re-Subscribe for each channel currently in
// StateRetrying. Exposed at package scope so tests can drive the retry
// cadence deterministically without waiting 5 min.
func (s *Subsystem) retryOnce(ctx context.Context) {
	s.mu.Lock()
	type pending struct {
		name string
		c    *atomic.Int64
	}
	var targets []pending
	for name, sub := range s.subscriptions {
		if sub.state != StateRetrying {
			continue
		}
		targets = append(targets, pending{name: name, c: s.counters[name]})
	}
	s.mu.Unlock()

	var statusDirty bool
	for _, p := range targets {
		chName := p.name
		lossCb := func(err error) { s.onSubscriptionLoss(chName, err) }
		err := s.Subscribe(ctx, &s.wg, p.name, defaultChannelQuery, p.c, lossCb)

		s.mu.Lock()
		sub := s.subscriptions[p.name]
		if sub == nil {
			s.mu.Unlock()
			continue
		}
		if err == nil {
			sub.state = StateSubscribed
			sub.attempts = 0
			sub.lastErr = nil
			s.mu.Unlock()
			slog.Info("", "evtspike", "retry_success", "channel", p.name)
			statusDirty = true
			continue
		}
		sub.attempts++
		sub.lastErr = err
		if sub.attempts >= subRetryMaxTries {
			sub.state = StateFailed
			s.mu.Unlock()
			slog.Warn("", "evtspike", "subscription_failed", "channel", p.name, "attempts", sub.attempts, "error", err.Error())
			statusDirty = true
			continue
		}
		s.mu.Unlock()
	}
	if statusDirty && s.OnStatusChange != nil {
		s.OnStatusChange(s.Status())
	}
}

// Stop cancels subscriptions, waits for goroutines to exit, and writes the
// in-memory baseline to disk one final time. Safe to call after a failed
// Start.
func (s *Subsystem) Stop() {
	s.quiesce()
	slog.Info("", "evtspike", "stop", "host", s.host)
}

// quiesce cancels the run context, waits for scoring + persistence +
// subscription goroutines (all joined to s.wg via A1), and writes the final
// baseline. Factored out so Reload's Enabled=true→false branch can tear the
// running subsystem down without logging a misleading "stop" line or losing
// the startCtx (which Stop's caller does not own).
func (s *Subsystem) quiesce() {
	s.runMu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.runMu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
	s.writeBaseline()

	s.mu.Lock()
	s.detectors = make(map[string]*Detector)
	s.counters = make(map[string]*atomic.Int64)
	s.channels = nil
	s.subscriptions = make(map[string]*channelSubscription)
	s.startupErr = nil
	s.lastSpikeAt = time.Time{}
	s.mu.Unlock()
}

// Reload applies newCfg to a running subsystem per the live-reload matrix
// in contracts/evtspike-config.md. newCfg is assumed clamped by ClampEvtSpike.
// Existing channels' GammaState is never rewritten by scalar-prior changes —
// their Alpha/Beta have already been shaped by observations (data-model.md §1).
func (s *Subsystem) Reload(newCfg dc.EvtSpikeConfig) error {
	s.mu.Lock()
	oldCfg := s.cfg
	s.mu.Unlock()

	// Enabled diff takes precedence: it dictates whether we are running at
	// all, which gates every other field. true→false tears down subscriptions
	// and loops but keeps the subsystem object (and its startCtx) alive for a
	// later re-enable. false→true hydrates a fresh run from the stored
	// startCtx without re-entering Start (which would double-capture).
	if oldCfg.Enabled != newCfg.Enabled {
		s.mu.Lock()
		s.cfg = newCfg
		s.mu.Unlock()

		if !newCfg.Enabled {
			s.quiesce()
			// A full disable also drops the Security privilege if it was ever
			// enabled — otherwise the token keeps it across the off-window.
			if oldCfg.SecurityChannelEnabled {
				s.tryDisablePrivilege("full_disable")
			}
			slog.Info("", "evtspike", "reload_disabled", "host", s.host)
			if s.OnStatusChange != nil {
				s.OnStatusChange(s.Status())
			}
			return nil
		}

		s.runMu.Lock()
		hasParent := s.startCtx != nil
		s.runMu.Unlock()
		if !hasParent {
			return fmt.Errorf("evtspike: Reload(Enabled=true) before Start — no stored parent context")
		}
		s.baselinePath = s.resolveBaselinePath()
		slog.Info("", "evtspike", "reload_enabled", "host", s.host)
		return s.startEnabled()
	}

	// If both sides are disabled there's nothing to reload; just persist the
	// new config so the next enable sees the latest scalar tunables.
	if !newCfg.Enabled {
		s.mu.Lock()
		s.cfg = newCfg
		s.mu.Unlock()
		return nil
	}

	if channelSetChanged(oldCfg, newCfg) {
		slog.Info("", "evtspike", "channel_list_changed", "host", s.host)
		return s.restartWithConfig(newCfg)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	newRho := 1.0
	if newCfg.HalfLifeBuckets > 0 {
		newRho = math.Exp(-math.Ln2 / float64(newCfg.HalfLifeBuckets))
	}
	newCooldown := time.Duration(newCfg.CooldownMinutes) * time.Minute

	for _, d := range s.detectors {
		d.Cfg.MinCount = newCfg.MinCount
		d.Cfg.Threshold = newCfg.Threshold
		d.Cfg.Cooldown = newCooldown
		d.Cfg.SlotMaturityObservations = newCfg.SlotMaturityObservations
		d.Cfg.Rho = newRho
	}

	s.cfg.MinCount = newCfg.MinCount
	s.cfg.Threshold = newCfg.Threshold
	s.cfg.CooldownMinutes = newCfg.CooldownMinutes
	s.cfg.SlotMaturityObservations = newCfg.SlotMaturityObservations
	s.cfg.PersistIntervalSeconds = newCfg.PersistIntervalSeconds
	s.cfg.HalfLifeBuckets = newCfg.HalfLifeBuckets
	s.cfg.PriorStrength = newCfg.PriorStrength
	s.cfg.MeanPerBucketPrior = newCfg.MeanPerBucketPrior

	return nil
}

// channelSetChanged reports whether newCfg would resolve to a different
// subscription set or baseline-file path than oldCfg. Uses ResolveChannels so
// the three channel-affecting fields (DisabledChannels, AddedChannels,
// SecurityChannelEnabled) are all covered by one equality check that matches
// the production merge semantics (case-insensitive dedup, disabled-then-added
// ordering).
func channelSetChanged(oldCfg, newCfg dc.EvtSpikeConfig) bool {
	if oldCfg.BaselinePath != newCfg.BaselinePath {
		return true
	}
	return !reflect.DeepEqual(ResolveChannels(oldCfg), ResolveChannels(newCfg))
}

// restartWithConfig performs the Stop+Start cycle required when the channel
// set or baseline-file path changes. Stop flushes in-memory state to disk;
// Start then hydrates detectors from that same baseline, so mature channels
// retain their learned state across the restart and no alert storm follows.
// The parent context stored by Start is reused so the caller (the file-watch
// path in svc.go) need not pass a context through Reload.
func (s *Subsystem) restartWithConfig(newCfg dc.EvtSpikeConfig) error {
	s.runMu.Lock()
	parent := s.startCtx
	s.runMu.Unlock()
	if parent == nil {
		return fmt.Errorf("evtspike: Reload called before Start — no stored parent context")
	}

	s.mu.Lock()
	oldSecurity := s.cfg.SecurityChannelEnabled
	s.mu.Unlock()

	s.Stop()

	// If the Security opt-in is being turned off, disable the privilege the
	// prior Start enabled so the token no longer holds it across the new run.
	// startEnabled re-enables it when the new cfg still wants Security.
	if oldSecurity && !newCfg.SecurityChannelEnabled {
		s.tryDisablePrivilege("security_optout")
	}

	s.mu.Lock()
	s.cfg = newCfg
	s.baselinePath = s.resolveBaselinePath()
	s.mu.Unlock()

	return s.startEnabled()
}

// tryDisablePrivilege calls DisablePrivilege if set and logs outcome at WARN
// on failure. Never fails Reload — privilege restoration is best-effort.
func (s *Subsystem) tryDisablePrivilege(reason string) {
	if s.DisablePrivilege == nil {
		return
	}
	if err := s.DisablePrivilege(); err != nil {
		slog.Warn("", "evtspike", "privilege_disable_failed", "reason", reason, "error", err.Error())
		return
	}
	slog.Info("", "evtspike", "privilege_disabled", "reason", reason)
}

// initChannels resolves, subscribes, and primes detectors for every channel
// in the current config. Split out from Start so tests can run it without
// firing scoring/persistence goroutines.
func (s *Subsystem) initChannels(ctx context.Context, bf *BaselineFile) {
	effective := s.cfg
	if effective.SecurityChannelEnabled {
		if err := s.EnablePrivilege(); err != nil {
			reason := err.Error()
			if errors.Is(err, ErrPrivilegeNotAssigned) {
				reason = "SeSecurityPrivilege not assigned to process token"
			}
			slog.Warn("", "evtspike", "skipped", "channel", SecurityChannel, "reason", reason)
			effective.SecurityChannelEnabled = false
		}
	}

	wanted := ResolveChannels(effective)
	subscribed := make([]string, 0, len(wanted))

	for _, ch := range wanted {
		d := s.buildDetector(bf, ch)
		c := new(atomic.Int64)
		chName := ch
		lossCb := func(err error) { s.onSubscriptionLoss(chName, err) }
		err := s.Subscribe(ctx, &s.wg, ch, defaultChannelQuery, c, lossCb)
		s.mu.Lock()
		s.detectors[ch] = d
		s.counters[ch] = c
		if err != nil {
			// Detector stays live so baseline state survives; subscription
			// enters retrying. supervisor will retry every subRetryInterval.
			s.subscriptions[ch] = &channelSubscription{name: ch, state: StateRetrying, attempts: 1, lastErr: err}
			s.mu.Unlock()
			slog.Warn("", "evtspike", "subscribe_failed", "channel", ch, "state", "retrying", "reason", err.Error())
			continue
		}
		s.subscriptions[ch] = &channelSubscription{name: ch, state: StateSubscribed}
		s.mu.Unlock()
		subscribed = append(subscribed, ch)
		slog.Info("", "evtspike", "subscribed", "channel", ch)
	}

	s.mu.Lock()
	s.channels = wanted // track all requested channels; Status filters by state
	if len(subscribed) == 0 && len(wanted) > 0 {
		s.startupErr = fmt.Errorf("no channels subscribed of %d requested", len(wanted))
	}
	s.mu.Unlock()

	slog.Info("", "evtspike", "start", "channels", len(subscribed), "requested", len(wanted), "host", s.host)
}

// onSubscriptionLoss is the callback handed to Subscribe's loss-signal param.
// Invoked asynchronously when the drain goroutine detects its subscription
// has gone bad mid-run. Moves the channel to StateRetrying, leaving the
// detector + baseline state untouched so a later successful retry hydrates
// the same learned distribution.
func (s *Subsystem) onSubscriptionLoss(channel string, err error) {
	s.mu.Lock()
	sub, ok := s.subscriptions[channel]
	if !ok {
		// Channel already removed by Stop/Reload; nothing to transition.
		s.mu.Unlock()
		return
	}
	if sub.state == StateFailed {
		s.mu.Unlock()
		return
	}
	sub.state = StateRetrying
	sub.attempts = 0
	sub.lastErr = err
	s.mu.Unlock()
	slog.Warn("", "evtspike", "subscription_lost", "channel", channel, "state", "retrying", "error", err.Error())
	if s.OnStatusChange != nil {
		s.OnStatusChange(s.Status())
	}
}

func (s *Subsystem) buildDetector(bf *BaselineFile, channel string) *Detector {
	cfg := DetectorConfig{
		MinCount:                 s.cfg.MinCount,
		Threshold:                s.cfg.Threshold,
		Cooldown:                 time.Duration(s.cfg.CooldownMinutes) * time.Minute,
		SlotMaturityObservations: s.cfg.SlotMaturityObservations,
	}
	d := NewDetector(s.cfg.MeanPerBucketPrior, s.cfg.PriorStrength, float64(s.cfg.HalfLifeBuckets), cfg)
	if bf != nil {
		if cs, ok := bf.Channels[channel]; ok {
			d.Slots = cs.Slots
			d.Global = cs.Global
			d.LastAlert = cs.LastAlert
		}
	}
	return d
}

// scoringLoop drives scoreOnce every 10 seconds until ctx is cancelled. Ticks
// are wall-clock aligned by Go's ticker, which is sufficient for the POC —
// exact slot-rollover alignment is a property of PersistIntervalSeconds
// (data-model.md §1), not the scoring cadence.
func (s *Subsystem) scoringLoop(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(scoringIntervalSeconds * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			s.scoreOnce(t)
		}
	}
}

// persistenceLoop writes the baseline on a PersistIntervalSeconds cadence
// until ctx is cancelled. Stop performs the final crash-less write.
func (s *Subsystem) persistenceLoop(ctx context.Context) {
	defer s.wg.Done()
	s.mu.Lock()
	interval := time.Duration(s.cfg.PersistIntervalSeconds) * time.Second
	s.mu.Unlock()
	if interval <= 0 {
		interval = time.Duration(dc.DefaultEvtSpikePersistIntervalSeconds) * time.Second
	}
	src := s.PersistTickSource
	if src == nil {
		src = defaultPersistTickSource
	}
	ch, stop := src(interval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			s.writeBaseline()
		}
	}
}

func defaultPersistTickSource(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// scoreOnce drains every subscribed channel's counter, feeds the count
// through its detector, and on a confirmed alert builds a SpikePayload and
// invokes OnSpike. Exposed at package scope so tests can drive scoring
// deterministically without relying on the 10-second ticker.
//
// The per-channel mutation of Detector state via ObserveBucket runs under
// s.mu so writeBaseline / Status never observe a torn snapshot. OnSpike is
// invoked outside the lock so a callback that blocks or synchronously
// re-enters the subsystem cannot deadlock the scoring loop.
func (s *Subsystem) scoreOnce(now time.Time) {
	s.mu.Lock()
	chans := append([]string(nil), s.channels...)
	s.mu.Unlock()

	for _, ch := range chans {
		s.mu.Lock()
		d := s.detectors[ch]
		c := s.counters[ch]
		if d == nil || c == nil {
			s.mu.Unlock()
			continue
		}

		count := int(c.Swap(0))
		r := d.ObserveBucket(now, count)

		var payload *SpikePayload
		if r.Alert {
			offset := firstSeenOffsetBuckets(d.RecentFlags)
			payload = &SpikePayload{
				Host:              s.host,
				Channel:           ch,
				WindowStart:       now,
				WindowEnd:         now.Add(scoringIntervalSeconds * time.Second),
				Observed:          r.Count,
				Expected:          r.Mean,
				TailProbability:   r.TailProb,
				ConfirmationCount: confirmationCount(d.RecentFlags),
				FirstSeenAt:       now.Add(-time.Duration(offset) * scoringIntervalSeconds * time.Second),
			}
			s.lastSpikeAt = now
		}
		s.mu.Unlock()

		if payload == nil {
			continue
		}

		slog.Info("", "evtspike", "confirmed_spike",
			"host", s.host,
			"channel", ch,
			"observed", r.Count,
			"expected", r.Mean,
			"tail", r.TailProb,
		)

		s.OnSpike(*payload)
	}
}

func (s *Subsystem) writeBaseline() {
	path := s.baselinePath
	if path == "" {
		return
	}

	s.mu.Lock()
	bf := &BaselineFile{
		SchemaVersion: SchemaVersion,
		WrittenAt:     s.Now(),
		Host:          s.host,
		Channels:      make(map[string]ChannelState, len(s.detectors)),
	}
	for name, d := range s.detectors {
		bf.Channels[name] = ChannelState{
			Slots:     d.Slots,
			Global:    d.Global,
			LastAlert: d.LastAlert,
		}
	}
	s.mu.Unlock()

	if err := WriteBaseline(path, bf); err != nil {
		slog.Warn("", "evtspike", "baseline_write_failed", "error", err.Error())
		return
	}
	if fi, err := os.Stat(path); err == nil {
		slog.Debug("", "evtspike", "baseline_written", "path", path, "bytes", fi.Size(), "channels", len(bf.Channels))
	}
}

func (s *Subsystem) resolveBaselinePath() string {
	if s.cfg.BaselinePath != "" {
		return s.cfg.BaselinePath
	}
	pd := os.Getenv("ProgramData")
	if pd == "" {
		return ""
	}
	return filepath.Join(pd, "LISS Technologies", "LISSTech DrainCtl", defaultBaselineFilename)
}

// Status returns a snapshot of the subsystem's state for the dashboard.
// EnabledChannels and MatureChannels count only channels currently in
// StateSubscribed — channels in retrying or failed are excluded so the
// dashboard pill reflects true operational capacity (plan Phase B1).
func (s *Subsystem) Status() DetectorStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	enabled := 0
	mature := 0
	for name, sub := range s.subscriptions {
		if sub.state != StateSubscribed {
			continue
		}
		enabled++
		if d, ok := s.detectors[name]; ok && detectorMature(d, s.cfg.SlotMaturityObservations) {
			mature++
		}
	}

	var lastSpike *time.Time
	if !s.lastSpikeAt.IsZero() {
		t := s.lastSpikeAt
		lastSpike = &t
	}

	status := DetectorStatus{
		Host:            s.host,
		State:           DeriveState(s.cfg.Enabled, enabled, mature, s.startupErr),
		EnabledChannels: enabled,
		MatureChannels:  mature,
		LastSpikeAt:     lastSpike,
	}
	if s.startupErr != nil {
		status.ErrorReason = s.startupErr.Error()
	}
	return status
}

func detectorMature(d *Detector, thresh int) bool {
	if thresh <= 0 {
		thresh = defaultSlotMaturityObservations
	}
	for i := range d.Slots {
		if d.Slots[i].N >= thresh {
			return true
		}
	}
	return false
}

// confirmationCount counts set bits in the rolling 3-bit confirmation window.
// Returns 0..3.
func confirmationCount(flags uint8) int {
	n := 0
	for i := 0; i < confirmM; i++ {
		if flags&1 == 1 {
			n++
		}
		flags >>= 1
	}
	return n
}

// firstSeenOffsetBuckets returns how many scoring buckets back the oldest set
// bit in the confirmation window sits. Bit 0 is this bucket, bit confirmM-1
// is the oldest. Returns 0 if no bit is set.
func firstSeenOffsetBuckets(flags uint8) int {
	for pos := confirmM - 1; pos >= 0; pos-- {
		if (flags>>pos)&1 == 1 {
			return pos
		}
	}
	return 0
}
