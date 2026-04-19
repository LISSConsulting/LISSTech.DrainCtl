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
	q99, ok := negBinQuantile(0.99, 6.0, 60.0)
	if !ok {
		t.Fatalf("negBinQuantile reported overflow on normal parameters")
	}
	mean := 6.0 / 60.0
	if float64(q99) < mean {
		t.Errorf("99th percentile %d should be >= mean %.2f", q99, mean)
	}
}

// TestNegBinQuantile_ExtremeCapSignalsOverflow — T110. When prob is
// effectively unreachable within float precision (tiny prior with huge
// tail), negBinQuantile must return ok=false rather than maxNBinIter-as-int.
// A quantile of 1.0 is unreachable because the infinite PMF sum is
// asymptotic to 1 — any implementation will hit the iteration cap first.
func TestNegBinQuantile_ExtremeCapSignalsOverflow(t *testing.T) {
	// prob=1.0 is unreachable via a partial CDF.
	if _, ok := negBinQuantile(1.0, 1.0, 0.001); ok {
		t.Error("expected ok=false for unreachable quantile (prob=1.0 with heavy-tailed prior)")
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

// TestNegBinTail_ExtremeCountDoesNotUnderflow — T110. A y far larger than the
// trained distribution's practical support must return a finite, non-negative
// tail. Without the pmf-underflow short-circuit the loop would spin to
// maxNBinIter while pmf decayed below float range; the guard keeps the
// result well-defined.
func TestNegBinTail_ExtremeCountDoesNotUnderflow(t *testing.T) {
	p := negBinUpperTail(1_000_000_000, 6.0, 60.0)
	if math.IsNaN(p) || math.IsInf(p, 0) {
		t.Fatalf("extreme count produced non-finite tail: %v", p)
	}
	if p < 0 {
		t.Errorf("extreme count produced negative tail: %v", p)
	}
	if p > 1e-10 {
		t.Errorf("P(Y >= 1e9) against alpha=6 beta=60 should be ~0, got %e", p)
	}
}

// TestDetector_FloodingAboveSoftCapStillFlags — T110. A count well beyond the
// robust-cap quantile must still mark the bucket anomalous. The cap clamps the
// baseline update, not the anomaly decision.
func TestDetector_FloodingAboveSoftCapStillFlags(t *testing.T) {
	d := NewDetector(0.1, 60, 360, testCfg())
	now := time.Date(2026, 4, 15, 10, 0, 0, 0, time.Local)

	for i := 0; i < 200; i++ {
		d.ObserveBucket(now.Add(time.Duration(i)*10*time.Second), 1)
	}

	cap99, ok := negBinQuantile(0.99, d.Global.Alpha, d.Global.Beta)
	if !ok {
		t.Fatalf("negBinQuantile overflowed on trained parameters alpha=%.3f beta=%.3f", d.Global.Alpha, d.Global.Beta)
	}

	floodCount := (cap99 + 1) * 100
	r := d.ObserveBucket(now.Add(2000*time.Second), floodCount)
	if !r.Anomalous {
		t.Errorf("flood count %d (cap99=%d) should be anomalous: tail=%e mean=%.4f", floodCount, cap99, r.TailProb, r.Mean)
	}
	if r.CapOverflow {
		t.Errorf("flood against trained state should not trigger cap overflow: CapOverflow=true")
	}
}

// TestDetector_FloodDoesNotPoisonBaseline — T110. When the robust-cap
// quantile iteration overflows (pathological heavy-tailed scoring state), the
// detector refuses the baseline update for that bucket and surfaces
// CapOverflow. A malicious or sensor-broken flood cannot poison the baseline
// into disabling future detections (FR-009 regression guard).
func TestDetector_FloodDoesNotPoisonBaseline(t *testing.T) {
	cfg := testCfg()
	cfg.Threshold = 10.0
	cfg.MinCount = 0
	d := NewDetector(0.1, 60, 360, cfg)
	now := time.Date(2026, 4, 15, 10, 0, 0, 0, time.Local)

	slot := timeSlot(now)
	d.Slots[slot] = GammaState{Alpha: 1.0, Beta: 1e-9, N: 100}
	d.Global = GammaState{Alpha: 1.0, Beta: 1e-9, N: 100}

	alphaBefore := d.Slots[slot].Alpha
	betaBefore := d.Slots[slot].Beta
	nBefore := d.Slots[slot].N
	globalAlphaBefore := d.Global.Alpha
	globalBetaBefore := d.Global.Beta

	r := d.ObserveBucket(now, 1)

	if !r.Anomalous {
		t.Fatalf("expected anomalous under relaxed threshold: tail=%e", r.TailProb)
	}
	if !r.CapOverflow {
		t.Fatalf("expected CapOverflow=true for alpha=1 beta=1e-9: quantile should saturate")
	}
	if d.Slots[slot].Alpha != alphaBefore || d.Slots[slot].Beta != betaBefore || d.Slots[slot].N != nBefore {
		t.Errorf("slot updated despite overflow: alpha %.6g→%.6g, beta %.6g→%.6g, N %d→%d",
			alphaBefore, d.Slots[slot].Alpha, betaBefore, d.Slots[slot].Beta, nBefore, d.Slots[slot].N)
	}
	if d.Global.Alpha != globalAlphaBefore || d.Global.Beta != globalBetaBefore {
		t.Errorf("global updated despite overflow: alpha %.6g→%.6g, beta %.6g→%.6g",
			globalAlphaBefore, d.Global.Alpha, globalBetaBefore, d.Global.Beta)
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
