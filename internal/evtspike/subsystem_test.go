//go:build windows

package evtspike

import (
	"context"
	"path/filepath"
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
