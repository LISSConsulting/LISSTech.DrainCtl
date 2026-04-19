//go:build windows

package evtspike

import (
	"math"
	"testing"
	"time"
)

func testCfg() DetectorConfig {
	return DetectorConfig{
		MinCount:                 10,
		Threshold:                1e-4,
		Cooldown:                 10 * time.Minute,
		SlotMaturityObservations: 5,
	}
}

func TestNegBinUpperTail_ZeroCount(t *testing.T) {
	p := negBinUpperTail(0, 3.0, 60.0)
	if p != 1.0 {
		t.Errorf("P(Y>=0) should be 1.0, got %f", p)
	}
}

func TestNegBinUpperTail_LargeCount(t *testing.T) {
	p := negBinUpperTail(50, 3.0, 60.0)
	if p > 1e-10 {
		t.Errorf("P(Y>=50) should be tiny, got %e", p)
	}
}

func TestNegBinQuantile(t *testing.T) {
	q99 := negBinQuantile(0.99, 6.0, 60.0)
	mean := 6.0 / 60.0
	if float64(q99) < mean {
		t.Errorf("99th percentile %d should be >= mean %.2f", q99, mean)
	}
}

func TestColdStart_NoFalseAlarm(t *testing.T) {
	d := NewDetector(0.1, 60, 360, testCfg())
	now := time.Date(2026, 4, 15, 10, 0, 0, 0, time.Local)

	r := d.ObserveBucket(now, 15)
	if r.Alert {
		t.Error("should not alert on first bucket (cold start)")
	}
}

func TestSteadyState_SpikeDetected(t *testing.T) {
	d := NewDetector(0.1, 60, 360, testCfg())
	now := time.Date(2026, 4, 15, 10, 0, 0, 0, time.Local)

	for i := 0; i < 200; i++ {
		d.ObserveBucket(now.Add(time.Duration(i)*10*time.Second), 1)
	}

	r := d.ObserveBucket(now.Add(2000*time.Second), 100)
	if !r.Anomalous {
		t.Errorf("count=100 after training on 1s should be anomalous, p=%e mean=%.2f", r.TailProb, r.Mean)
	}
}

func TestPersistence_SingleSpikeNoAlert(t *testing.T) {
	d := NewDetector(0.1, 60, 360, testCfg())
	now := time.Date(2026, 4, 15, 10, 0, 0, 0, time.Local)

	for i := 0; i < 200; i++ {
		d.ObserveBucket(now.Add(time.Duration(i)*10*time.Second), 1)
	}

	r1 := d.ObserveBucket(now.Add(2000*time.Second), 100)
	r2 := d.ObserveBucket(now.Add(2010*time.Second), 1)
	r3 := d.ObserveBucket(now.Add(2020*time.Second), 1)

	if r1.Alert || r2.Alert || r3.Alert {
		t.Error("single spike followed by normal should not trigger alert")
	}
}

func TestPersistence_SustainedSpikeAlerts(t *testing.T) {
	d := NewDetector(0.1, 60, 360, testCfg())
	now := time.Date(2026, 4, 15, 10, 0, 0, 0, time.Local)

	for i := 0; i < 200; i++ {
		d.ObserveBucket(now.Add(time.Duration(i)*10*time.Second), 1)
	}

	base := 2000
	var gotAlert bool
	for i := 0; i < 3; i++ {
		r := d.ObserveBucket(now.Add(time.Duration(base+i*10)*time.Second), 100)
		if r.Alert {
			gotAlert = true
		}
	}
	if !gotAlert {
		t.Error("sustained spike over 3 buckets should trigger alert")
	}
}

func TestCooldown_SuppressesRepeat(t *testing.T) {
	d := NewDetector(0.1, 60, 360, testCfg())
	now := time.Date(2026, 4, 15, 10, 0, 0, 0, time.Local)

	for i := 0; i < 200; i++ {
		d.ObserveBucket(now.Add(time.Duration(i)*10*time.Second), 1)
	}

	base := now.Add(2000 * time.Second)
	for i := 0; i < 3; i++ {
		d.ObserveBucket(base.Add(time.Duration(i*10)*time.Second), 100)
	}

	r := d.ObserveBucket(base.Add(30*time.Second), 100)
	if r.Alert {
		t.Error("should be suppressed by cooldown")
	}
}

func TestRobustUpdate_BaselineNotPoisoned(t *testing.T) {
	d := NewDetector(0.1, 60, 360, testCfg())
	now := time.Date(2026, 4, 15, 10, 0, 0, 0, time.Local)

	for i := 0; i < 200; i++ {
		d.ObserveBucket(now.Add(time.Duration(i)*10*time.Second), 1)
	}

	meanBefore := d.Global.Alpha / d.Global.Beta

	d.ObserveBucket(now.Add(2000*time.Second), 10000)

	meanAfter := d.Global.Alpha / d.Global.Beta

	ratio := meanAfter / meanBefore
	if ratio > 5.0 {
		t.Errorf("baseline jumped too much: before=%.2f after=%.2f ratio=%.1f", meanBefore, meanAfter, ratio)
	}
}

func TestTimeSlot(t *testing.T) {
	ts := time.Date(2026, 4, 15, 9, 7, 0, 0, time.Local)
	if s := timeSlot(ts); s != 36 {
		t.Errorf("09:07 should be slot 36, got %d", s)
	}
	ts2 := time.Date(2026, 4, 15, 23, 59, 0, 0, time.Local)
	if s := timeSlot(ts2); s != 95 {
		t.Errorf("23:59 should be slot 95, got %d", s)
	}
}

func TestNegBinUpperTail_Monotonic(t *testing.T) {
	alpha, beta := 6.0, 60.0
	prev := 1.0
	for y := 0; y <= 20; y++ {
		p := negBinUpperTail(y, alpha, beta)
		if p > prev+1e-12 {
			t.Errorf("tail not monotonically decreasing at y=%d: %f > %f", y, p, prev)
		}
		if math.IsNaN(p) {
			t.Errorf("NaN at y=%d", y)
		}
		prev = p
	}
}

func TestSlotMaturityObservations_UsesConfigValue(t *testing.T) {
	cfg := DetectorConfig{
		MinCount:                 10,
		Threshold:                1e-4,
		Cooldown:                 10 * time.Minute,
		SlotMaturityObservations: 3,
	}
	d := NewDetector(0.1, 60, 360, cfg)
	now := time.Date(2026, 4, 15, 10, 0, 0, 0, time.Local)

	slot := timeSlot(now)
	for i := 0; i < 3; i++ {
		d.ObserveBucket(now.Add(time.Duration(i)*10*time.Second), 1)
	}
	if d.Slots[slot].N != 3 {
		t.Fatalf("expected slot N=3, got %d", d.Slots[slot].N)
	}
	if d.Cfg.SlotMaturityObservations != 3 {
		t.Errorf("expected SlotMaturityObservations=3 to be preserved, got %d", d.Cfg.SlotMaturityObservations)
	}
}

// TestRobustUpdate_SlotNotPoisoned_30Consecutive exercises US2 SC-004: under a
// 30-bucket sustained flood (y=100 against an expected rate of ~1), the slot's
// posterior mean must grow by ≤2× — i.e., the robust cap is clamping update y
// at the 99th percentile of the NegBin posterior rather than letting the raw
// count train the baseline into the flood regime.
func TestRobustUpdate_SlotNotPoisoned_30Consecutive(t *testing.T) {
	d := NewDetector(0.1, 60, 360, testCfg())
	base := time.Date(2026, 4, 15, 10, 0, 0, 0, time.Local)

	// Converge slot 40 near its steady-state mean of ~1 by replaying 10 days
	// of 15-minute morning windows (900 y=1 observations into the same slot).
	// A single-session 90-observation window leaves the slot far from
	// steady-state given a 360-bucket EWMA half-life, so the flood assertion
	// would be dominated by convergence rather than the robust cap.
	const trainingDays = 10
	const bucketsPerSlotVisit = 90
	for day := 0; day < trainingDays; day++ {
		dayStart := base.Add(time.Duration(day) * 24 * time.Hour)
		for i := 0; i < bucketsPerSlotVisit; i++ {
			d.ObserveBucket(dayStart.Add(time.Duration(i)*10*time.Second), 1)
		}
	}

	slot := timeSlot(base)
	if d.Slots[slot].N < bucketsPerSlotVisit*trainingDays {
		t.Fatalf("slot %d undertrained: N=%d", slot, d.Slots[slot].N)
	}

	meanBefore := d.Slots[slot].Alpha / d.Slots[slot].Beta

	floodStart := base.Add(time.Duration(trainingDays) * 24 * time.Hour)
	for i := 0; i < 30; i++ {
		r := d.ObserveBucket(floodStart.Add(time.Duration(i)*10*time.Second), 100)
		if !r.Anomalous {
			t.Fatalf("flood observation %d should be anomalous: count=%d mean=%.4f tail=%e",
				i, r.Count, r.Mean, r.TailProb)
		}
	}

	if d.Slots[slot].N != bucketsPerSlotVisit*trainingDays+30 {
		t.Fatalf("expected %d total observations in slot %d, got %d",
			bucketsPerSlotVisit*trainingDays+30, slot, d.Slots[slot].N)
	}

	meanAfter := d.Slots[slot].Alpha / d.Slots[slot].Beta
	ratio := meanAfter / meanBefore
	if ratio > 2.0 {
		t.Errorf("slot %d mean grew by %.2fx under 30-bucket y=100 flood (before=%.4f after=%.4f); robust cap ineffective",
			slot, ratio, meanBefore, meanAfter)
	}
}

func TestSlotMaturityObservations_ZeroPromotedToDefault(t *testing.T) {
	cfg := DetectorConfig{
		MinCount:  10,
		Threshold: 1e-4,
		Cooldown:  10 * time.Minute,
	}
	d := NewDetector(0.1, 60, 360, cfg)
	if d.Cfg.SlotMaturityObservations != defaultSlotMaturityObservations {
		t.Errorf("zero SlotMaturityObservations should promote to default %d, got %d",
			defaultSlotMaturityObservations, d.Cfg.SlotMaturityObservations)
	}
}
