//go:build windows

package evtspike

import (
	"context"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// noopSubscribe is a stand-in for the real EvtSubscribe used by
// Subsystem.Start. Tests drive bucket counters directly, so we just need the
// subscribe step to succeed.
func noopSubscribe(_ context.Context, _, _ string, _ *atomic.Int64) error {
	return nil
}

func testSubsystem(t *testing.T, host string) *Subsystem {
	t.Helper()
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
	s := New(cfg, host)
	s.Subscribe = noopSubscribe
	return s
}

// trainOneChannel seeds a detector with N=200 normal buckets so the slot and
// global state mature. scoreOnce is deliberately not used here — we want to
// train without any risk of OnSpike firing mid-training.
func trainOneChannel(d *Detector, start time.Time, n int, count int) {
	for i := 0; i < n; i++ {
		d.ObserveBucket(start.Add(time.Duration(i)*10*time.Second), count)
	}
}

// TestSubsystem_SustainedBurst_FiresOnSpikeOncePerCooldown covers T021: a fake
// subscriber feeds anomalous counts across a 2-of-3 confirmation window, and
// we assert OnSpike fires exactly once, the payload satisfies data-model.md §5
// invariants, and follow-on bursts inside cooldown are suppressed.
func TestSubsystem_SustainedBurst_FiresOnSpikeOncePerCooldown(t *testing.T) {
	s := testSubsystem(t, "TEST-HOST")

	var mu sync.Mutex
	var got []SpikePayload
	s.OnSpike = func(p SpikePayload) {
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.initChannels(ctx, nil)

	channel := "Application"
	counter := s.counters[channel]
	if counter == nil {
		t.Fatalf("Application counter not initialised; subscribed=%v", s.channels)
	}
	d := s.detectors[channel]
	if d == nil {
		t.Fatal("Application detector not initialised")
	}

	base := time.Date(2026, 4, 18, 10, 0, 0, 0, time.UTC)
	trainOneChannel(d, base, 200, 1)

	// 2-of-3 anomalous pattern: bucket 0 spike, bucket 1 normal, bucket 2 spike.
	// RecentFlags after bucket 2 is 0b101 -> confirmed, alert fires.
	spikeStart := base.Add(2000 * time.Second)
	steps := []int{50, 1, 50}
	for i, c := range steps {
		counter.Store(int64(c))
		s.scoreOnce(spikeStart.Add(time.Duration(i) * 10 * time.Second))
	}

	mu.Lock()
	if len(got) != 1 {
		mu.Unlock()
		t.Fatalf("expected 1 spike after 2-of-3 burst, got %d", len(got))
	}
	p := got[0]
	mu.Unlock()

	if p.Host != "TEST-HOST" {
		t.Errorf("Host: got %q want TEST-HOST", p.Host)
	}
	if p.Channel != channel {
		t.Errorf("Channel: got %q want %q", p.Channel, channel)
	}
	if p.Observed < 0 {
		t.Errorf("Observed %d violates >=0 invariant", p.Observed)
	}
	if p.Expected < 0 {
		t.Errorf("Expected %f violates >=0 invariant", p.Expected)
	}
	if !(p.TailProbability > 0 && p.TailProbability < 1) {
		t.Errorf("TailProbability %g violates (0,1) invariant", p.TailProbability)
	}
	if p.ConfirmationCount != 2 && p.ConfirmationCount != 3 {
		t.Errorf("ConfirmationCount %d not in {2,3}", p.ConfirmationCount)
	}
	if !p.WindowEnd.Equal(p.WindowStart.Add(10 * time.Second)) {
		t.Errorf("WindowEnd %v should be WindowStart+10s (start=%v)", p.WindowEnd, p.WindowStart)
	}
	if p.FirstSeenAt.After(p.WindowStart) {
		t.Errorf("FirstSeenAt %v is after WindowStart %v", p.FirstSeenAt, p.WindowStart)
	}
	// For the 101 pattern the oldest set bit is 20 s back.
	wantFirstSeen := p.WindowStart.Add(-20 * time.Second)
	if !p.FirstSeenAt.Equal(wantFirstSeen) {
		t.Errorf("FirstSeenAt %v want %v (oldest anomalous bucket of 101)", p.FirstSeenAt, wantFirstSeen)
	}

	// More bursts inside the 10-minute cooldown: no additional OnSpike.
	for i := 0; i < 12; i++ {
		counter.Store(50)
		s.scoreOnce(spikeStart.Add(time.Duration(30+i*10) * time.Second))
	}
	mu.Lock()
	if len(got) != 1 {
		n := len(got)
		mu.Unlock()
		t.Fatalf("cooldown did not suppress: got %d spikes, want 1", n)
	}
	mu.Unlock()
}

// TestSubsystem_FloodThenSecondAnomaly_BothFire_US2 covers T044: a 30-minute
// sustained flood on one channel must not poison the posterior badly enough to
// mask a subsequent smaller anomaly. We flood Application at y=100 for 180
// consecutive 10-second buckets, pause beyond the cooldown, then inject a
// second 2-of-3 anomaly at y=30. OnSpike must fire for BOTH phases (at least
// once during the flood, at least once for the second anomaly) — proving the
// robust cap in Detector.ObserveBucket kept the baseline near its pre-flood
// mean (see TestRobustUpdate_SlotNotPoisoned_30Consecutive for the detector-
// level assertion).
func TestSubsystem_FloodThenSecondAnomaly_BothFire_US2(t *testing.T) {
	cfg := dc.EvtSpikeConfig{
		Enabled:                  true,
		MinCount:                 10,
		Threshold:                1e-4,
		CooldownMinutes:          1,
		SlotMaturityObservations: 5,
		PersistIntervalSeconds:   900,
		HalfLifeBuckets:          360,
		PriorStrength:            60,
		MeanPerBucketPrior:       0.1,
		BaselinePath:             filepath.Join(t.TempDir(), "baseline.json"),
	}
	s := New(cfg, "TEST-HOST")
	s.Subscribe = noopSubscribe

	var mu sync.Mutex
	var got []SpikePayload
	s.OnSpike = func(p SpikePayload) {
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.initChannels(ctx, nil)

	channel := "Application"
	counter := s.counters[channel]
	d := s.detectors[channel]
	if counter == nil || d == nil {
		t.Fatal("Application counter/detector not initialised")
	}

	// Train the slots the test will exercise (40, 41, 42) by replaying 10 days
	// of 1-hour morning windows with y=1. Training bypasses scoreOnce so only
	// Application's detector is primed; other channels stay at prior mean with
	// counter=0 and produce no noise.
	base := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	const trainingDays = 10
	const bucketsPerDay = 360
	for day := 0; day < trainingDays; day++ {
		dayStart := base.Add(time.Duration(day) * 24 * time.Hour)
		for i := 0; i < bucketsPerDay; i++ {
			d.ObserveBucket(dayStart.Add(time.Duration(i)*10*time.Second), 1)
		}
	}

	// Phase 1 — flood: 180 consecutive buckets of y=100 driven through
	// scoreOnce. The first confirmation fires OnSpike; robust cap clamps
	// each update so the posterior can only creep up.
	floodStart := base.Add(time.Duration(trainingDays) * 24 * time.Hour)
	const floodBuckets = 180
	for i := 0; i < floodBuckets; i++ {
		counter.Store(100)
		s.scoreOnce(floodStart.Add(time.Duration(i) * 10 * time.Second))
	}

	mu.Lock()
	floodSpikes := len(got)
	mu.Unlock()
	if floodSpikes < 1 {
		t.Fatalf("flood fired no OnSpike; expected ≥1 (first confirmation)")
	}

	// Phase 2 — pause: 12 quiet buckets (120 s) clears the 3-bucket
	// confirmation window and exceeds the 60 s cooldown, arming the detector
	// for a fresh alert.
	pauseStart := floodStart.Add(floodBuckets * 10 * time.Second)
	const pauseBuckets = 12
	for i := 0; i < pauseBuckets; i++ {
		counter.Store(1)
		s.scoreOnce(pauseStart.Add(time.Duration(i) * 10 * time.Second))
	}

	mu.Lock()
	afterPause := len(got)
	mu.Unlock()

	// Phase 3 — second smaller anomaly: 2-of-3 pattern at y=30. If the robust
	// cap poisoned the baseline toward the flood regime, y=30 would no longer
	// cross the tail threshold and OnSpike would stay silent here.
	secondStart := pauseStart.Add(pauseBuckets * 10 * time.Second)
	steps := []int{30, 1, 30}
	for i, c := range steps {
		counter.Store(int64(c))
		s.scoreOnce(secondStart.Add(time.Duration(i) * 10 * time.Second))
	}

	mu.Lock()
	total := len(got)
	mu.Unlock()

	if total <= afterPause {
		t.Fatalf("robust cap poisoned baseline: second y=30 anomaly did not "+
			"fire OnSpike (floodSpikes=%d afterPause=%d total=%d)",
			floodSpikes, afterPause, total)
	}

	mu.Lock()
	last := got[total-1]
	mu.Unlock()
	if last.Channel != channel {
		t.Errorf("second spike Channel: got %q want %q", last.Channel, channel)
	}
	if last.Observed != 30 {
		t.Errorf("second spike Observed: got %d want 30", last.Observed)
	}
}

// TestSubsystem_FloodThenRestart_SecondAnomalyStillFires_US2 covers T045: the
// robust-cap protection from US2 must survive a service restart. We flood the
// Application channel mid-day, persist the baseline through the production
// WriteBaseline path, then start a fresh Subsystem pointed at the same
// baseline file. After cooldown, a smaller second anomaly (y=30 in a 2-of-3
// pattern) must still fire OnSpike on the restarted subsystem — proving the
// capped posterior round-tripped through JSON without losing the
// poisoning-resistance property tested in TestSubsystem_FloodThenSecondAnomaly_BothFire_US2.
func TestSubsystem_FloodThenRestart_SecondAnomalyStillFires_US2(t *testing.T) {
	baselinePath := filepath.Join(t.TempDir(), "baseline.json")
	cfg := dc.EvtSpikeConfig{
		Enabled:                  true,
		MinCount:                 10,
		Threshold:                1e-4,
		CooldownMinutes:          1,
		SlotMaturityObservations: 5,
		PersistIntervalSeconds:   900,
		HalfLifeBuckets:          360,
		PriorStrength:            60,
		MeanPerBucketPrior:       0.1,
		BaselinePath:             baselinePath,
	}

	s1 := New(cfg, "TEST-HOST")
	s1.Subscribe = noopSubscribe

	var mu1 sync.Mutex
	var got1 []SpikePayload
	s1.OnSpike = func(p SpikePayload) {
		mu1.Lock()
		got1 = append(got1, p)
		mu1.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s1.initChannels(ctx, nil)

	channel := "Application"
	counter1 := s1.counters[channel]
	d1 := s1.detectors[channel]
	if counter1 == nil || d1 == nil {
		t.Fatal("Application counter/detector not initialised on s1")
	}

	base := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	const trainingDays = 10
	const bucketsPerDay = 360
	for day := 0; day < trainingDays; day++ {
		dayStart := base.Add(time.Duration(day) * 24 * time.Hour)
		for i := 0; i < bucketsPerDay; i++ {
			d1.ObserveBucket(dayStart.Add(time.Duration(i)*10*time.Second), 1)
		}
	}

	floodStart := base.Add(time.Duration(trainingDays) * 24 * time.Hour)
	const floodBuckets = 180
	for i := 0; i < floodBuckets; i++ {
		counter1.Store(100)
		s1.scoreOnce(floodStart.Add(time.Duration(i) * 10 * time.Second))
	}

	mu1.Lock()
	floodSpikes := len(got1)
	mu1.Unlock()
	if floodSpikes < 1 {
		t.Fatalf("flood fired no OnSpike on s1; expected ≥1")
	}

	s1.writeBaseline()

	bf, err := LoadBaseline(baselinePath)
	if err != nil {
		t.Fatalf("LoadBaseline after restart: %v", err)
	}
	if bf == nil || bf.Channels == nil {
		t.Fatal("LoadBaseline returned nil baseline or nil channels map")
	}
	persisted, ok := bf.Channels[channel]
	if !ok {
		t.Fatalf("baseline missing channel %q after restart; got %d channels", channel, len(bf.Channels))
	}

	s2 := New(cfg, "TEST-HOST")
	s2.Subscribe = noopSubscribe

	var mu2 sync.Mutex
	var got2 []SpikePayload
	s2.OnSpike = func(p SpikePayload) {
		mu2.Lock()
		got2 = append(got2, p)
		mu2.Unlock()
	}

	s2.initChannels(ctx, bf)

	counter2 := s2.counters[channel]
	d2 := s2.detectors[channel]
	if counter2 == nil || d2 == nil {
		t.Fatal("post-restart subsystem missing Application counter/detector")
	}

	if !reflect.DeepEqual(d2.Slots, persisted.Slots) {
		t.Fatalf("hydrated slots differ from persisted state — warm restart broken")
	}
	if !d2.LastAlert.Equal(persisted.LastAlert) {
		t.Fatalf("hydrated LastAlert %v differs from persisted %v", d2.LastAlert, persisted.LastAlert)
	}

	pauseStart := floodStart.Add(floodBuckets * 10 * time.Second)
	const pauseBuckets = 12
	for i := 0; i < pauseBuckets; i++ {
		counter2.Store(1)
		s2.scoreOnce(pauseStart.Add(time.Duration(i) * 10 * time.Second))
	}
	mu2.Lock()
	afterPause := len(got2)
	mu2.Unlock()

	secondStart := pauseStart.Add(pauseBuckets * 10 * time.Second)
	steps := []int{30, 1, 30}
	for i, c := range steps {
		counter2.Store(int64(c))
		s2.scoreOnce(secondStart.Add(time.Duration(i) * 10 * time.Second))
	}

	mu2.Lock()
	total := len(got2)
	mu2.Unlock()

	if total <= afterPause {
		t.Fatalf("post-restart y=30 anomaly did not fire OnSpike; "+
			"baseline poisoned across warm restart "+
			"(floodSpikes=%d afterPause=%d total=%d)",
			floodSpikes, afterPause, total)
	}

	mu2.Lock()
	last := got2[total-1]
	mu2.Unlock()
	if last.Channel != channel {
		t.Errorf("post-restart spike Channel: got %q want %q", last.Channel, channel)
	}
	if last.Observed != 30 {
		t.Errorf("post-restart spike Observed: got %d want 30", last.Observed)
	}
}

// TestSubsystem_MaturedSlot_WarmRestart_HydratesBitForBit_US4 covers T059: after
// maturing a slot and stopping the subsystem, a freshly started subsystem
// pointed at the same baseline file must hydrate detector GammaState bit-for-
// bit. We exercise the real Start/Stop lifecycle (not initChannels/writeBaseline
// shortcuts) so the ticker-driven scoring and persistence loops, final-flush
// path, and LoadBaseline-on-Start are all on the covered path.
//
// Race note: the scoring loop's first tick is 10 s after Start; maturing a slot
// and calling Stop completes in microseconds, so no scoringLoop tick can
// interleave. The extra on-disk comparison below guards against a future
// regression that would change that timing.
func TestSubsystem_MaturedSlot_WarmRestart_HydratesBitForBit_US4(t *testing.T) {
	baselinePath := filepath.Join(t.TempDir(), "baseline.json")
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
		BaselinePath:             baselinePath,
	}

	s1 := New(cfg, "TEST-HOST")
	s1.Subscribe = noopSubscribe
	s1.OnSpike = func(SpikePayload) {}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s1.Start(ctx); err != nil {
		t.Fatalf("s1.Start: %v", err)
	}

	channel := "Application"
	base := time.Date(2026, 4, 18, 10, 7, 0, 0, time.UTC)

	s1.mu.Lock()
	d1 := s1.detectors[channel]
	if d1 == nil {
		s1.mu.Unlock()
		s1.Stop()
		t.Fatal("s1 Application detector not initialised")
	}
	for i := 0; i < 10; i++ {
		d1.ObserveBucket(base.Add(time.Duration(i)*time.Second), 1)
	}
	d1.ObserveBucket(base.Add(45*time.Minute), 2)
	slotsBefore := d1.Slots
	globalBefore := d1.Global
	s1.mu.Unlock()

	matureSlots := 0
	for _, sl := range slotsBefore {
		if sl.N >= cfg.SlotMaturityObservations {
			matureSlots++
		}
	}
	if matureSlots == 0 {
		s1.Stop()
		t.Fatalf("no slot matured pre-Stop (SlotMaturityObservations=%d); "+
			"test would not exercise warm restart", cfg.SlotMaturityObservations)
	}

	s1.Stop()

	bf, err := LoadBaseline(baselinePath)
	if err != nil {
		t.Fatalf("LoadBaseline after Stop: %v", err)
	}
	persisted, ok := bf.Channels[channel]
	if !ok {
		t.Fatalf("baseline missing channel %q; got %d channels", channel, len(bf.Channels))
	}
	if !reflect.DeepEqual(slotsBefore, persisted.Slots) {
		t.Fatalf("baseline Slots drifted from pre-Stop snapshot "+
			"(scoringLoop tick may have raced the test)\n before: %+v\n disk:   %+v",
			slotsBefore, persisted.Slots)
	}
	if !reflect.DeepEqual(globalBefore, persisted.Global) {
		t.Fatalf("baseline Global drifted from pre-Stop snapshot\n before: %+v\n disk:   %+v",
			globalBefore, persisted.Global)
	}

	s2 := New(cfg, "TEST-HOST")
	s2.Subscribe = noopSubscribe
	s2.OnSpike = func(SpikePayload) {}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	if err := s2.Start(ctx2); err != nil {
		t.Fatalf("s2.Start: %v", err)
	}
	defer s2.Stop()

	s2.mu.Lock()
	d2 := s2.detectors[channel]
	if d2 == nil {
		s2.mu.Unlock()
		t.Fatal("s2 Application detector not initialised after warm restart")
	}
	slotsAfter := d2.Slots
	globalAfter := d2.Global
	s2.mu.Unlock()

	if !reflect.DeepEqual(slotsBefore, slotsAfter) {
		t.Fatalf("Slots not bit-for-bit after warm restart\n before: %+v\n after:  %+v",
			slotsBefore, slotsAfter)
	}
	if !reflect.DeepEqual(globalBefore, globalAfter) {
		t.Fatalf("Global not bit-for-bit after warm restart\n before: %+v\n after:  %+v",
			globalBefore, globalAfter)
	}
}

// TestSubsystem_WarmRestart_NormalNoSpike_AnomalyFires_US4 covers T060: after a
// warm restart (baseline written by s1, loaded into s2), the detector must not
// fire on a normal-rate bucket and must fire on the first eligible
// confirmation window of an anomalous pattern. "First eligible confirmation
// window" = the earliest 3-bucket window in which 2 anomalous bits can
// co-exist, which is bucket index 2 of a fresh RecentFlags=0 — RecentFlags is
// intentionally not persisted (baseline.go §ChannelState), so the rolling
// window always restarts empty.
func TestSubsystem_WarmRestart_NormalNoSpike_AnomalyFires_US4(t *testing.T) {
	baselinePath := filepath.Join(t.TempDir(), "baseline.json")
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
		BaselinePath:             baselinePath,
	}

	s1 := New(cfg, "TEST-HOST")
	s1.Subscribe = noopSubscribe
	s1.OnSpike = func(SpikePayload) {}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s1.initChannels(ctx, nil)

	channel := "Application"
	d1 := s1.detectors[channel]
	if d1 == nil {
		t.Fatal("s1 Application detector not initialised")
	}

	base := time.Date(2026, 4, 18, 10, 0, 0, 0, time.UTC)
	trainOneChannel(d1, base, 200, 1)

	s1.writeBaseline()

	bf, err := LoadBaseline(baselinePath)
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}

	s2 := New(cfg, "TEST-HOST")
	s2.Subscribe = noopSubscribe

	var mu sync.Mutex
	var got []SpikePayload
	s2.OnSpike = func(p SpikePayload) {
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
	}

	s2.initChannels(ctx, bf)

	counter := s2.counters[channel]
	d2 := s2.detectors[channel]
	if counter == nil || d2 == nil {
		t.Fatal("post-restart subsystem missing Application counter/detector")
	}
	if !detectorMature(d2, cfg.SlotMaturityObservations) {
		t.Fatalf("post-restart detector has no mature slot; test premise broken")
	}

	postStart := base.Add(2000 * time.Second)
	counter.Store(1)
	s2.scoreOnce(postStart)

	mu.Lock()
	if n := len(got); n != 0 {
		mu.Unlock()
		t.Fatalf("normal-rate bucket post-restart fired OnSpike %d times; warm-up re-entered", n)
	}
	mu.Unlock()

	anomStart := postStart.Add(10 * time.Second)
	steps := []int{50, 1, 50}
	spikesPerBucket := make([]int, len(steps))
	for i, c := range steps {
		counter.Store(int64(c))
		mu.Lock()
		before := len(got)
		mu.Unlock()
		s2.scoreOnce(anomStart.Add(time.Duration(i) * 10 * time.Second))
		mu.Lock()
		spikesPerBucket[i] = len(got) - before
		mu.Unlock()
	}

	if spikesPerBucket[0] != 0 {
		t.Errorf("bucket 0 fired prematurely (RecentFlags=0b001 cannot confirm)")
	}
	if spikesPerBucket[1] != 0 {
		t.Errorf("bucket 1 (normal) fired OnSpike unexpectedly")
	}
	if spikesPerBucket[2] != 1 {
		t.Errorf("first eligible confirmation window did not fire OnSpike exactly once (got %d)", spikesPerBucket[2])
	}
}

// TestSubsystem_SingleWindowTransient_NoSpike covers T022: a single anomalous
// bucket flanked by normal buckets does not confirm, so OnSpike must not fire.
func TestSubsystem_SingleWindowTransient_NoSpike(t *testing.T) {
	s := testSubsystem(t, "TEST-HOST")

	var count atomic.Int32
	s.OnSpike = func(SpikePayload) { count.Add(1) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.initChannels(ctx, nil)

	channel := "Application"
	counter := s.counters[channel]
	if counter == nil {
		t.Fatalf("Application counter not initialised; subscribed=%v", s.channels)
	}
	d := s.detectors[channel]
	if d == nil {
		t.Fatal("Application detector not initialised")
	}

	base := time.Date(2026, 4, 18, 10, 0, 0, 0, time.UTC)
	trainOneChannel(d, base, 200, 1)

	spikeStart := base.Add(2000 * time.Second)
	steps := []int{50, 1, 1}
	for i, c := range steps {
		counter.Store(int64(c))
		s.scoreOnce(spikeStart.Add(time.Duration(i) * 10 * time.Second))
	}

	if got := count.Load(); got != 0 {
		t.Fatalf("single-window transient fired OnSpike %d times; 2-of-3 suppression failed", got)
	}
}
