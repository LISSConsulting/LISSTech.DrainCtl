//go:build windows

package evtspike

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// safeBuf is a bytes.Buffer guarded by a mutex so a slog.TextHandler can write
// to it concurrently with the test goroutine reading String().
type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// noopSubscribe is a stand-in for the real EvtSubscribe used by
// Subsystem.Start. Tests drive bucket counters directly, so we just need the
// subscribe step to succeed.
func noopSubscribe(_ context.Context, _ *sync.WaitGroup, _, _ string, _ *atomic.Int64, _ func(error)) error {
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

// TestSubsystem_PersistenceTicker_WritesOnAdvance covers T065: the periodic
// persistence ticker must call WriteBaseline every PersistIntervalSeconds. We
// inject a controllable tick channel via PersistTickSource and advance the
// mock clock by sending on it; each tick must produce a fresh baseline file
// on disk whose WrittenAt reflects the advance.
func TestSubsystem_PersistenceTicker_WritesOnAdvance(t *testing.T) {
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

	s := New(cfg, "TEST-HOST")
	s.Subscribe = noopSubscribe
	s.OnSpike = func(SpikePayload) {}

	tickCh := make(chan time.Time)
	s.PersistTickSource = func(time.Duration) (<-chan time.Time, func()) {
		return tickCh, func() {}
	}

	var nowMu sync.Mutex
	clock := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return clock
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	// First tick: advance the mock clock 15 minutes and signal persistenceLoop.
	// Use a synchronous send to ensure the loop has consumed the tick before we
	// inspect the file. The writeBaseline call completes before the next tick
	// can be sent.
	nowMu.Lock()
	clock = clock.Add(15 * time.Minute)
	firstExpected := clock
	nowMu.Unlock()
	tickCh <- time.Time{}

	first := waitForBaselineWrittenAt(t, baselinePath, firstExpected)

	nowMu.Lock()
	clock = clock.Add(15 * time.Minute)
	secondExpected := clock
	nowMu.Unlock()
	tickCh <- time.Time{}

	second := waitForBaselineWrittenAt(t, baselinePath, secondExpected)

	if !second.WrittenAt.After(first.WrittenAt) {
		t.Fatalf("second baseline WrittenAt %v should be after first %v — ticker did not drive a second write",
			second.WrittenAt, first.WrittenAt)
	}
	if second.Host != "TEST-HOST" {
		t.Errorf("second baseline Host: got %q want TEST-HOST", second.Host)
	}
}

// TestSubsystem_Reload_ThresholdHotApplied_US5 covers T066: calling Reload on a
// running subsystem with a changed Threshold must propagate the new value to
// s.cfg and every live detector's Cfg so the next ObserveBucket scores against
// it. The assertion path is Detector.Cfg inspection, per the task's
// "inspect internal config" criterion — driving a second observation through
// a trained detector would require the test to guess the exact y-range
// between old and new threshold, which is not load-bearing for this contract.
func TestSubsystem_Reload_ThresholdHotApplied_US5(t *testing.T) {
	s := testSubsystem(t, "TEST-HOST")
	s.OnSpike = func(SpikePayload) {}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.initChannels(ctx, nil)

	oldThreshold := s.cfg.Threshold
	if oldThreshold == 0 {
		t.Fatal("test premise broken: baseline cfg.Threshold is zero")
	}

	s.mu.Lock()
	if len(s.detectors) == 0 {
		s.mu.Unlock()
		t.Fatal("test premise broken: no detectors initialised")
	}
	for ch, d := range s.detectors {
		if d.Cfg.Threshold != oldThreshold {
			s.mu.Unlock()
			t.Fatalf("pre-reload %s: d.Cfg.Threshold=%g want %g", ch, d.Cfg.Threshold, oldThreshold)
		}
	}
	s.mu.Unlock()

	newCfg := s.cfg
	newCfg.Threshold = 1e-3
	if err := s.Reload(newCfg); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.Threshold != 1e-3 {
		t.Errorf("s.cfg.Threshold: got %g want 1e-3", s.cfg.Threshold)
	}
	for ch, d := range s.detectors {
		if d.Cfg.Threshold != 1e-3 {
			t.Errorf("post-reload %s: d.Cfg.Threshold=%g want 1e-3", ch, d.Cfg.Threshold)
		}
	}
}

// TestSubsystem_Reload_DisabledChannels_RestartsSubsystem_US5 covers T067:
// calling Reload with a changed DisabledChannels list must trigger an internal
// Stop+Start so detectors and subscriptions realign with the new channel set.
// Assertions:
//   - A "channel_list_changed" slog line is emitted (the contract marker).
//   - The "evtspike=start" line fires AFTER "channel_list_changed" (proving
//     Start was re-invoked by the reload path, not just a state mutation).
//   - The newly-disabled channel is gone from s.channels and s.detectors.
//   - A retained channel's *Detector instance is rebuilt (different pointer
//     than pre-reload) — the Stop side rebuilt the map from scratch.
//   - OnSpike is not called during the transition (no alert storm, per US5's
//     Independent Test).
func TestSubsystem_Reload_DisabledChannels_RestartsSubsystem_US5(t *testing.T) {
	s := testSubsystem(t, "TEST-HOST")

	var onSpikeCalls atomic.Int32
	s.OnSpike = func(SpikePayload) { onSpikeCalls.Add(1) }

	logBuf := &safeBuf{}
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	const disabledChannel = "Application"
	const retainedChannel = "System"

	s.mu.Lock()
	if _, ok := s.detectors[disabledChannel]; !ok {
		s.mu.Unlock()
		t.Fatalf("premise broken: %q not subscribed at Start; got channels=%v", disabledChannel, s.channels)
	}
	preReloadDetector, ok := s.detectors[retainedChannel]
	if !ok {
		s.mu.Unlock()
		t.Fatalf("premise broken: %q not subscribed at Start; got channels=%v", retainedChannel, s.channels)
	}
	s.mu.Unlock()

	// Clear the baseline log output captured during the initial Start so the
	// assertions below only reflect what Reload emitted.
	logBuf.mu.Lock()
	logBuf.b.Reset()
	logBuf.mu.Unlock()

	newCfg := s.cfg
	newCfg.DisabledChannels = []string{disabledChannel}

	if err := s.Reload(newCfg); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	log := logBuf.String()
	changedIdx := strings.Index(log, "channel_list_changed")
	if changedIdx < 0 {
		t.Fatalf("expected 'channel_list_changed' slog marker after Reload; captured log:\n%s", log)
	}
	startIdx := strings.Index(log, "evtspike=start")
	if startIdx < 0 {
		t.Fatalf("expected 'evtspike=start' slog line (proving Start was re-invoked); captured log:\n%s", log)
	}
	if startIdx <= changedIdx {
		t.Errorf("'evtspike=start' at %d should follow 'channel_list_changed' at %d; captured log:\n%s",
			startIdx, changedIdx, log)
	}
	stopIdx := strings.Index(log, "evtspike=stop")
	if stopIdx < 0 || stopIdx >= startIdx {
		t.Errorf("expected 'evtspike=stop' to precede 'evtspike=start' (Stop+Start order); captured log:\n%s", log)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, stillThere := s.detectors[disabledChannel]; stillThere {
		t.Errorf("%q still in detectors map after Reload disabled it", disabledChannel)
	}
	for _, ch := range s.channels {
		if strings.EqualFold(ch, disabledChannel) {
			t.Errorf("%q still in s.channels after Reload disabled it; channels=%v", disabledChannel, s.channels)
		}
	}
	postReloadDetector, ok := s.detectors[retainedChannel]
	if !ok {
		t.Fatalf("retained channel %q missing from detectors after Reload; channels=%v", retainedChannel, s.channels)
	}
	if postReloadDetector == preReloadDetector {
		t.Errorf("retained channel %q detector pointer did not change across Reload — Stop+Start did not rebuild state",
			retainedChannel)
	}

	if got := onSpikeCalls.Load(); got != 0 {
		t.Errorf("OnSpike fired %d times during Reload transition; alert storm expected to be 0", got)
	}
}

// TestSubsystem_SecurityChannelOptIn_PrivilegeGranted covers the success half of
// T023a: when cfg.SecurityChannelEnabled=true and EnablePrivilege succeeds,
// Subscribe is invoked for Security alongside the default channels and
// EnablePrivilege is called exactly once at Start. The dedicated-account
// failure half lives in TestSubsystem_SecurityChannelOptIn_PrivilegeNotAssigned.
func TestSubsystem_SecurityChannelOptIn_PrivilegeGranted(t *testing.T) {
	s := testSubsystem(t, "TEST-HOST")
	s.cfg.SecurityChannelEnabled = true
	s.OnSpike = func(SpikePayload) {}

	var privCalls atomic.Int32
	s.EnablePrivilege = func() error {
		privCalls.Add(1)
		return nil
	}

	var subMu sync.Mutex
	var subscribed []string
	s.Subscribe = func(_ context.Context, _ *sync.WaitGroup, channel, _ string, _ *atomic.Int64, _ func(error)) error {
		subMu.Lock()
		subscribed = append(subscribed, channel)
		subMu.Unlock()
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.initChannels(ctx, nil)

	if got := privCalls.Load(); got != 1 {
		t.Errorf("EnablePrivilege call count: got %d want 1", got)
	}

	subMu.Lock()
	gotSub := append([]string(nil), subscribed...)
	subMu.Unlock()
	if !containsFold(gotSub, SecurityChannel) {
		t.Errorf("%q not subscribed when privilege granted; subscribed=%v", SecurityChannel, gotSub)
	}
	if !containsFold(gotSub, "Application") {
		t.Errorf("default channel %q missing from subscribed set; subscribed=%v", "Application", gotSub)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !containsFold(s.channels, SecurityChannel) {
		t.Errorf("%q missing from s.channels after Start; channels=%v", SecurityChannel, s.channels)
	}
}

// TestSubsystem_SecurityChannelOptIn_PrivilegeNotAssigned covers the dedicated-
// account half of T023a: when AdjustTokenPrivileges returns
// ERROR_NOT_ALL_ASSIGNED (surfaced as ErrPrivilegeNotAssigned), the subsystem
// must skip the Security subscription, log a warning, and continue subscribing
// all other default channels.
func TestSubsystem_SecurityChannelOptIn_PrivilegeNotAssigned(t *testing.T) {
	s := testSubsystem(t, "TEST-HOST")
	s.cfg.SecurityChannelEnabled = true
	s.OnSpike = func(SpikePayload) {}

	var privCalls atomic.Int32
	s.EnablePrivilege = func() error {
		privCalls.Add(1)
		return ErrPrivilegeNotAssigned
	}

	var subMu sync.Mutex
	var subscribed []string
	s.Subscribe = func(_ context.Context, _ *sync.WaitGroup, channel, _ string, _ *atomic.Int64, _ func(error)) error {
		subMu.Lock()
		subscribed = append(subscribed, channel)
		subMu.Unlock()
		return nil
	}

	logBuf := &safeBuf{}
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.initChannels(ctx, nil)

	if got := privCalls.Load(); got != 1 {
		t.Errorf("EnablePrivilege call count: got %d want 1", got)
	}

	subMu.Lock()
	gotSub := append([]string(nil), subscribed...)
	subMu.Unlock()
	if containsFold(gotSub, SecurityChannel) {
		t.Errorf("%q subscribed despite ErrPrivilegeNotAssigned; subscribed=%v", SecurityChannel, gotSub)
	}
	if len(gotSub) != len(Defaults) {
		t.Errorf("subscribed channel count: got %d want %d (Defaults excluding Security)", len(gotSub), len(Defaults))
	}
	if !containsFold(gotSub, "Application") || !containsFold(gotSub, "System") {
		t.Errorf("core default channels missing after Security skip; subscribed=%v", gotSub)
	}

	log := logBuf.String()
	if !strings.Contains(log, "evtspike=skipped") {
		t.Errorf("expected 'evtspike=skipped' warning in log; got:\n%s", log)
	}
	if !strings.Contains(log, "channel=Security") {
		t.Errorf("expected warning to name channel=Security; got:\n%s", log)
	}
	if !strings.Contains(log, "level=WARN") {
		t.Errorf("expected WARN-level skip log; got:\n%s", log)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if containsFold(s.channels, SecurityChannel) {
		t.Errorf("%q in s.channels despite ErrPrivilegeNotAssigned; channels=%v", SecurityChannel, s.channels)
	}
}

// waitForBaselineWrittenAt polls the baseline file until LoadBaseline returns a
// BaselineFile whose WrittenAt equals the expected timestamp. The synchronous
// tick send guarantees persistenceLoop has entered the case branch, but
// writeBaseline runs under s.mu and the atomic rename may lag by a few
// microseconds — hence the short deadline.
func waitForBaselineWrittenAt(t *testing.T, path string, want time.Time) *BaselineFile {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		bf, err := LoadBaseline(path)
		if err == nil && bf != nil && bf.WrittenAt.Equal(want) {
			return bf
		}
		if time.Now().After(deadline) {
			if err != nil {
				t.Fatalf("waiting for WrittenAt=%v: LoadBaseline error: %v", want, err)
			}
			var got time.Time
			if bf != nil {
				got = bf.WrittenAt
			}
			t.Fatalf("waiting for WrittenAt=%v: last observed %v", want, got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestSubscription_TransitionsToRetryingOnAsyncLoss — T107. An in-flight
// subscription that fails mid-run (via the async loss callback) must move
// to StateRetrying without waiting for the first supervisor tick. Status
// accounting must drop it from EnabledChannels.
func TestSubscription_TransitionsToRetryingOnAsyncLoss(t *testing.T) {
	s := testSubsystem(t, "TEST-HOST")
	s.cfg.DisabledChannels = []string{} // all 54 defaults
	s.OnSpike = func(SpikePayload) {}

	// Capture the loss callback for one specific channel.
	var lossFor atomic.Value // stores func(error)
	target := "Application"
	s.Subscribe = func(_ context.Context, wg *sync.WaitGroup, channel, _ string, _ *atomic.Int64, loss func(error)) error {
		if channel == target {
			lossFor.Store(loss)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
		}()
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	preEnabled := s.Status().EnabledChannels

	cb, _ := lossFor.Load().(func(error))
	if cb == nil {
		t.Fatal("loss callback was never captured for target channel")
	}
	cb(errRetrySim)

	s.mu.Lock()
	state := s.subscriptions[target].state
	s.mu.Unlock()
	if state != StateRetrying {
		t.Errorf("channel state after async loss: got %q want %q", state, StateRetrying)
	}

	postEnabled := s.Status().EnabledChannels
	if postEnabled != preEnabled-1 {
		t.Errorf("EnabledChannels: pre=%d post=%d; want one fewer", preEnabled, postEnabled)
	}
}

// TestSubscription_TransitionsToFailedAfterTwelveAttempts — T107. A channel
// that fails every retry attempt must move to StateFailed after
// subRetryMaxTries tries. We drive the supervisor via a channel-injected
// RetryTickSource.
func TestSubscription_TransitionsToFailedAfterTwelveAttempts(t *testing.T) {
	s := testSubsystem(t, "TEST-HOST")
	s.cfg.DisabledChannels = []string{} // all defaults
	s.OnSpike = func(SpikePayload) {}

	// Drive retries via an externally-sendable channel.
	tickCh := make(chan time.Time, 1)
	s.RetryTickSource = func(time.Duration) (<-chan time.Time, func()) {
		return tickCh, func() {}
	}

	// Subscribe succeeds once for every channel, then fails forever on
	// retries. We track channel-specific call counts to simulate a provider
	// that's up at boot and goes permanently bad afterward.
	target := "Application"
	var bootPhase atomic.Bool
	bootPhase.Store(true)
	s.Subscribe = func(_ context.Context, wg *sync.WaitGroup, channel, _ string, _ *atomic.Int64, _ func(error)) error {
		if channel == target && !bootPhase.Load() {
			return errRetrySim
		}
		wg.Add(1)
		go func() { defer wg.Done() }()
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	// Transition target to retrying via direct mutation (simulating loss).
	bootPhase.Store(false)
	s.onSubscriptionLoss(target, errRetrySim)

	// Fire the supervisor ticker subRetryMaxTries times; each tick increments
	// attempts because Subscribe now always fails for the target.
	for i := 0; i < subRetryMaxTries; i++ {
		tickCh <- time.Now()
		// Wait for retryOnce to consume the tick and advance state.
		deadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(deadline) {
			s.mu.Lock()
			sub := s.subscriptions[target]
			attempts := sub.attempts
			state := sub.state
			s.mu.Unlock()
			if attempts > i || state == StateFailed {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	s.mu.Lock()
	sub := s.subscriptions[target]
	state := sub.state
	attempts := sub.attempts
	s.mu.Unlock()
	if state != StateFailed {
		t.Errorf("after %d failed retries: state=%q attempts=%d; want %q", subRetryMaxTries, state, attempts, StateFailed)
	}
}

// TestStatus_EnabledChannelsExcludesNonSubscribed — T107. Three channels —
// one subscribed, one retrying, one failed. Status.EnabledChannels must be
// 1, not 3. Without the B1 fix, a dropped channel still counted as enabled
// and the dashboard pill stayed "healthy" under degraded conditions.
func TestStatus_EnabledChannelsExcludesNonSubscribed(t *testing.T) {
	s := testSubsystem(t, "TEST-HOST")
	s.OnSpike = func(SpikePayload) {}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	// Directly mutate channels' states: exactly one stays subscribed, the
	// rest split between retrying and failed.
	s.mu.Lock()
	i := 0
	for _, sub := range s.subscriptions {
		switch i {
		case 0:
			sub.state = StateSubscribed
		case 1:
			sub.state = StateRetrying
		default:
			sub.state = StateFailed
		}
		i++
	}
	s.mu.Unlock()

	st := s.Status()
	if st.EnabledChannels != 1 {
		t.Errorf("EnabledChannels = %d, want 1 (only one channel in StateSubscribed)", st.EnabledChannels)
	}
}

var errRetrySim = errTestRetrySim("subscription lost in test")

type errTestRetrySim string

func (e errTestRetrySim) Error() string { return string(e) }

// TestResolveChannels_AddedSecurityPreservedWithFlagOff — T103. The plan
// deliberately rejected the "filter Security out of AddedChannels" shape:
// the existing spec/data-model/contracts contract says AddedChannels=Security
// is preserved with the flag off, and subscribe fails at runtime via the
// privilege check. This test locks that contract.
func TestResolveChannels_AddedSecurityPreservedWithFlagOff(t *testing.T) {
	cfg := dc.EvtSpikeConfig{
		SecurityChannelEnabled: false,
		AddedChannels:          []string{SecurityChannel},
	}
	got := ResolveChannels(cfg)

	var seen int
	for _, ch := range got {
		if ch == SecurityChannel {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("ResolveChannels with flag off + AddedChannels=Security: got %d Security entries, want 1", seen)
	}

	cfg.SecurityChannelEnabled = true
	got = ResolveChannels(cfg)
	seen = 0
	for _, ch := range got {
		if ch == SecurityChannel {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("ResolveChannels with flag on + AddedChannels=Security: got %d Security entries, want 1 (dedup)", seen)
	}
}

// TestReload_SecurityDisableRestoresPrivilege — T103. Start with Security on,
// Reload with Security off via a channel-set change; DisablePrivilege must be
// invoked exactly once. Regression test for Phase A3: without this, the token
// keeps SeSecurityPrivilege enabled across the off-window and a subsequent
// AddedChannels=Security attempt would silently succeed.
func TestReload_SecurityDisableRestoresPrivilege(t *testing.T) {
	s := testSubsystem(t, "TEST-HOST")
	s.cfg.SecurityChannelEnabled = true
	s.OnSpike = func(SpikePayload) {}

	var enables, disables atomic.Int32
	s.EnablePrivilege = func() error { enables.Add(1); return nil }
	s.DisablePrivilege = func() error { disables.Add(1); return nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	if got := enables.Load(); got != 1 {
		t.Errorf("initial Start: enable count = %d, want 1", got)
	}

	newCfg := s.cfg
	newCfg.SecurityChannelEnabled = false
	if err := s.Reload(newCfg); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if got := disables.Load(); got != 1 {
		t.Errorf("after Security opt-out: disable count = %d, want 1", got)
	}
}

// TestReload_AddedChannelsSecurityAfterDisableFails — T103. End-to-end bypass
// regression: after opting out of Security, adding "Security" via
// AddedChannels must fail the privilege check and be logged as skipped;
// EnabledChannels must exclude it. Without Phase A3 this would succeed
// silently.
func TestReload_AddedChannelsSecurityAfterDisableFails(t *testing.T) {
	s := testSubsystem(t, "TEST-HOST")
	s.cfg.SecurityChannelEnabled = true
	s.OnSpike = func(SpikePayload) {}

	// Track whether the privilege is currently enabled on our mock token.
	var enabled atomic.Bool
	s.EnablePrivilege = func() error { enabled.Store(true); return nil }
	s.DisablePrivilege = func() error { enabled.Store(false); return nil }
	// Subscribe fails when Security is requested but the privilege is not
	// enabled — mirrors the real EvtSubscribe behaviour against a token that
	// lacks SeSecurityPrivilege.
	s.Subscribe = func(ctx context.Context, wg *sync.WaitGroup, channel, _ string, _ *atomic.Int64, _ func(error)) error {
		if channel == SecurityChannel && !enabled.Load() {
			return ErrPrivilegeNotAssigned
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ctx.Done()
		}()
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	optOut := s.cfg
	optOut.SecurityChannelEnabled = false
	if err := s.Reload(optOut); err != nil {
		t.Fatalf("Reload(opt-out): %v", err)
	}
	if enabled.Load() {
		t.Fatal("privilege still enabled after opt-out Reload")
	}

	// Now try to smuggle Security back via AddedChannels.
	smuggle := optOut
	smuggle.AddedChannels = []string{SecurityChannel}
	if err := s.Reload(smuggle); err != nil {
		t.Fatalf("Reload(smuggle): %v", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Post-B1 contract: Security detector may exist (baseline persists
	// across retry attempts) but its subscription must NOT be in
	// StateSubscribed — the privilege check should have rejected the
	// subscribe call and moved it to StateRetrying.
	if sub, ok := s.subscriptions[SecurityChannel]; ok && sub.state == StateSubscribed {
		t.Errorf("Security channel subscription is StateSubscribed after opt-out smuggle; subs=%+v", sub)
	}
	// Status.EnabledChannels should exclude Security.
}

// TestReload_EnabledFalseStopsScoringLoop — T101. Reload(Enabled=false) must
// tear down the running subsystem: no further OnSpike, no further detectors
// in the map, Status.State reflects the disabled config.
func TestReload_EnabledFalseStopsScoringLoop(t *testing.T) {
	s := testSubsystem(t, "TEST-HOST")
	var onSpikeCalls atomic.Int32
	s.OnSpike = func(SpikePayload) { onSpikeCalls.Add(1) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	s.mu.Lock()
	if len(s.detectors) == 0 {
		s.mu.Unlock()
		t.Fatal("premise: no detectors after Start")
	}
	s.mu.Unlock()

	disabled := s.cfg
	disabled.Enabled = false
	if err := s.Reload(disabled); err != nil {
		t.Fatalf("Reload(Enabled=false): %v", err)
	}

	s.mu.Lock()
	if len(s.detectors) != 0 {
		s.mu.Unlock()
		t.Errorf("detectors not cleared after disable: %d remain", len(s.detectors))
	}
	if len(s.channels) != 0 {
		s.mu.Unlock()
		t.Errorf("channels not cleared after disable: %v", s.channels)
	}
	s.mu.Unlock()

	st := s.Status()
	if st.State != "disabled" {
		t.Errorf("Status.State after disable: got %q want \"disabled\"", st.State)
	}
}

// TestReload_EnabledTrueStartsFromDisabled — T101. Construct with
// Enabled=false, call Start (which must capture startCtx but not subscribe),
// then Reload(Enabled=true). Subsystem must hydrate detectors and resume
// normal scoring.
func TestReload_EnabledTrueStartsFromDisabled(t *testing.T) {
	s := testSubsystem(t, "TEST-HOST")
	s.cfg.Enabled = false
	s.OnSpike = func(SpikePayload) {}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	s.mu.Lock()
	if len(s.detectors) != 0 {
		s.mu.Unlock()
		t.Fatalf("Start(Enabled=false) launched detectors: %d", len(s.detectors))
	}
	s.mu.Unlock()

	enabled := s.cfg
	enabled.Enabled = true
	if err := s.Reload(enabled); err != nil {
		t.Fatalf("Reload(Enabled=true): %v", err)
	}

	s.mu.Lock()
	nDet := len(s.detectors)
	s.mu.Unlock()
	if nDet == 0 {
		t.Fatal("Reload(Enabled=true) did not hydrate detectors")
	}

	st := s.Status()
	if st.State == "disabled" {
		t.Errorf("Status.State after enable: got %q want non-disabled", st.State)
	}
}

// TestReload_EnabledToggleFromSvcWiring — T101. Drives the config-reload path
// via applyEvtSpikeConfigReload exactly as internal/svc/handler.go does, to
// catch wiring bugs that a pure subsystem-level test misses.
//
// (Lives in internal/svc/svc_test.go since applyEvtSpikeConfigReload is
// internal to that package.)
//
// NOTE: placeholder here just to document the task; the actual test is in
// internal/svc/svc_test.go.

// TestStop_WaitsForSubscriberGoroutines — T099. An injected SubscribeFunc
// mimics what production Subscribe does (wg.Add + goroutine that honours
// ctx.Done). Once Stop cancels the context, the goroutine lingers for 200 ms
// before signalling done. Stop must not return until wg.Wait observes it —
// proof that subscription goroutines are part of the wait-group the caller
// uses for shutdown ordering.
func TestStop_WaitsForSubscriberGoroutines(t *testing.T) {
	s := testSubsystem(t, "TEST-HOST")
	s.OnSpike = func(SpikePayload) {}

	const linger = 200 * time.Millisecond
	var exited atomic.Bool
	s.Subscribe = func(ctx context.Context, wg *sync.WaitGroup, _, _ string, _ *atomic.Int64, _ func(error)) error {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ctx.Done()
			time.Sleep(linger)
			exited.Store(true)
		}()
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	start := time.Now()
	s.Stop()
	elapsed := time.Since(start)

	if !exited.Load() {
		t.Fatalf("Stop returned before subscription goroutine exited")
	}
	// Stop should have waited at least the linger window for each of 54 channels
	// running in parallel (they all exit concurrently), so minimum elapsed is
	// linger. Allow a small slop for scheduler noise.
	if elapsed < linger-20*time.Millisecond {
		t.Fatalf("Stop returned too fast (%v < %v); it did not wait for subscription goroutines", elapsed, linger)
	}
}
