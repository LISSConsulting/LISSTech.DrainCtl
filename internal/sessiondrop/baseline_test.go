//go:build windows

package sessiondrop

import (
	"math"
	"testing"
	"time"
)

func TestLowerTailProbabilityStableAndMonotone(t *testing.T) {
	alpha, beta := 2500.0, 125.0
	previous := 0.0
	for observed := range 200 {
		got := LowerTailProbability(alpha, beta, observed)
		if math.IsNaN(got) || got < previous || got > 1 {
			t.Fatalf("CDF(%d) = %g after %g", observed, got, previous)
		}
		previous = got
	}
	if got := LowerTailProbability(2, 1, 0); got != 0.25 {
		t.Fatalf("geometric lower CDF at zero = %g, want 0.25", got)
	}
}

func TestBaselineDecayAndReadinessUseOnlyNormalTrainingTimes(t *testing.T) {
	start := time.Date(2026, time.September, 20, 0, 0, 0, 0, time.UTC).UnixMilli()
	settings := DefaultSettings()
	settings.BaselineHalfLifeHours = 24
	baseline := newBaseline("host-a", BaselineScopeAllHours, nil, start)
	for day := range FallbackMinimumObservations {
		baseline = trainBaseline(baseline, 10, "", start+int64(day)*time.Hour.Milliseconds(), settings)
	}
	if fallbackReady(&baseline) {
		t.Fatal("fallback ready before its normal-training timestamps span 24 hours")
	}
	last := start + int64(FallbackMinimumSpanHours)*time.Hour.Milliseconds()
	baseline = trainBaseline(baseline, 10, "", last, settings)
	if !fallbackReady(&baseline) {
		t.Fatal("fallback not ready after 20+ normal observations spanning 24 hours")
	}
	trainedAt := *baseline.LastNormalTrainedAtMS
	decayed := decayedBaseline(baseline, last+48*time.Hour.Milliseconds(), settings.BaselineHalfLifeHours)
	if *decayed.LastNormalTrainedAtMS != trainedAt || !fallbackReady(&decayed) {
		t.Fatal("decay changed normal-training readiness timestamps")
	}
	if decayed.Alpha >= baseline.Alpha || decayed.Beta >= baseline.Beta {
		t.Fatalf("decay did not pull posterior toward prior: %#v -> %#v", baseline, decayed)
	}
}

func TestSlotReadinessUsesSevenDistinctSourceLocalDates(t *testing.T) {
	settings := DefaultSettings()
	baseline := newBaseline("host-a", BaselineScopeSlot, new(7), 1)
	for day := range SlotMaturityDays {
		baseline = trainBaseline(baseline, 10, "2026-09-"+string(rune('1'+day)), int64(day+1), settings)
	}
	if !slotReady(&baseline) {
		t.Fatalf("slot maturity dates = %v, want ready", baseline.TrainedLocalDates)
	}
	if slot := SlotForObservation(time.Date(2026, 9, 27, 7, 45, 0, 0, time.UTC).UnixMilli(), -7*60); slot != 3 {
		t.Fatalf("source-local slot = %d, want 3", slot)
	}
}
