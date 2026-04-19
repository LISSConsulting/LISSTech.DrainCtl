//go:build windows

package evtspike

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
// be driven by direct counter manipulation.
type SubscribeFunc func(ctx context.Context, channel, query string, counter *atomic.Int64) error

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

	// Now lets tests inject a deterministic clock for the persistence write
	// timestamp. The scoring path always takes time from the caller.
	Now func() time.Time

	// PersistTickSource returns the channel that drives persistenceLoop and a
	// stop closure invoked on loop exit. Production uses time.NewTicker; tests
	// supply a channel they send on to trigger WriteBaseline deterministically.
	PersistTickSource func(time.Duration) (<-chan time.Time, func())

	baselinePath string

	mu          sync.Mutex
	detectors   map[string]*Detector
	counters    map[string]*atomic.Int64
	channels    []string
	startupErr  error
	lastSpikeAt time.Time

	runMu  sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
	ctx    context.Context
}

// New returns an unstarted Subsystem. cfg is assumed already clamped by
// ClampEvtSpike on the caller.
func New(cfg dc.EvtSpikeConfig, host string) *Subsystem {
	s := &Subsystem{
		cfg:               cfg,
		host:              host,
		Subscribe:         Subscribe,
		EnablePrivilege:   EnableSecurityPrivilege,
		Now:               time.Now,
		PersistTickSource: defaultPersistTickSource,
		detectors:         make(map[string]*Detector),
		counters:          make(map[string]*atomic.Int64),
	}
	s.baselinePath = s.resolveBaselinePath()
	return s
}

// Start loads the baseline, resolves channels, subscribes each one, and
// launches the scoring and persistence goroutines. Per-channel subscribe
// errors are logged and the channel is skipped (FR-009); other channels
// continue. Returns an error only if OnSpike was not set.
func (s *Subsystem) Start(ctx context.Context) error {
	if s.OnSpike == nil {
		return fmt.Errorf("evtspike: Subsystem.OnSpike must be set before Start")
	}

	bf, _ := LoadBaseline(s.baselinePath)

	s.runMu.Lock()
	runCtx, cancel := context.WithCancel(ctx)
	s.ctx = runCtx
	s.cancel = cancel
	s.runMu.Unlock()

	s.initChannels(runCtx, bf)

	if s.OnStatusChange != nil {
		s.OnStatusChange(s.Status())
	}

	s.wg.Add(2)
	go s.scoringLoop(runCtx)
	go s.persistenceLoop(runCtx)

	return nil
}

// Stop cancels subscriptions, waits for goroutines to exit, and writes the
// in-memory baseline to disk one final time. Safe to call after a failed
// Start.
func (s *Subsystem) Stop() {
	s.runMu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.runMu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
	s.writeBaseline()
	slog.Info("", "evtspike", "stop", "host", s.host)
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
		if err := s.Subscribe(ctx, ch, defaultChannelQuery, c); err != nil {
			slog.Warn("", "evtspike", "skipped", "channel", ch, "reason", err.Error())
			continue
		}
		s.mu.Lock()
		s.detectors[ch] = d
		s.counters[ch] = c
		s.mu.Unlock()
		subscribed = append(subscribed, ch)
		slog.Info("", "evtspike", "subscribed", "channel", ch)
	}

	s.mu.Lock()
	s.channels = subscribed
	if len(subscribed) == 0 && len(wanted) > 0 {
		s.startupErr = fmt.Errorf("no channels subscribed of %d requested", len(wanted))
	}
	s.mu.Unlock()

	slog.Info("", "evtspike", "start", "channels", len(subscribed), "host", s.host)
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
	interval := time.Duration(s.cfg.PersistIntervalSeconds) * time.Second
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
func (s *Subsystem) Status() DetectorStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	mature := 0
	for _, d := range s.detectors {
		if detectorMature(d, s.cfg.SlotMaturityObservations) {
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
		State:           DeriveState(s.cfg.Enabled, len(s.channels), mature, s.startupErr),
		EnabledChannels: len(s.channels),
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
