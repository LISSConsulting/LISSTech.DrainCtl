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
