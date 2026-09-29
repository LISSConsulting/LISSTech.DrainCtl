//go:build windows

package sessiondrop

import (
	"math"
	"sort"
	"time"
)

const (
	baselinePriorAlpha = 1.0
	baselinePriorBeta  = 1.0
)

// SlotForObservation returns the source-local quarter-hour slot. The supplied
// offset is deliberately used instead of the dashboard clock so DST dates keep
// their source-local slot identity.
func SlotForObservation(reportEpochMS int64, offsetMinutes int) int {
	local := time.UnixMilli(reportEpochMS).UTC().Add(time.Duration(offsetMinutes) * time.Minute)
	return (local.Hour()*60 + local.Minute()) / 15
}

// LowerTailProbability calculates the gamma-Poisson posterior-predictive CDF
// P(Y <= observed). It uses the negative-binomial recurrence rather than
// factorials or gamma functions, so normal detector counts remain stable.
func LowerTailProbability(alpha, beta float64, observed int) float64 {
	if observed < 0 || alpha < baselinePriorAlpha || beta < baselinePriorBeta || math.IsNaN(alpha) || math.IsNaN(beta) {
		return 0
	}
	logP := alpha * math.Log(beta/(beta+1))
	logSum := logP
	for k := range observed {
		logP += math.Log(alpha+float64(k)) - math.Log(float64(k+1)) - math.Log(beta+1)
		if logP > logSum {
			logSum += math.Log1p(math.Exp(logSum - logP))
		} else {
			logSum += math.Log1p(math.Exp(logP - logSum))
		}
	}
	if math.IsNaN(logSum) {
		return 0
	}
	return math.Min(math.Exp(logSum), 1)
}

func decayedBaseline(b Baseline, atMS int64, halfLifeHours int) Baseline {
	if b.LastUpdatedAtMS <= 0 || atMS <= b.LastUpdatedAtMS || halfLifeHours <= 0 {
		return b
	}
	factor := math.Exp2(-float64(atMS-b.LastUpdatedAtMS) / float64(time.Hour/time.Millisecond) / float64(halfLifeHours))
	b.Alpha = baselinePriorAlpha + (b.Alpha-baselinePriorAlpha)*factor
	b.Beta = baselinePriorBeta + (b.Beta-baselinePriorBeta)*factor
	return b
}

func newBaseline(host string, scope BaselineScope, slot *int, atMS int64) Baseline {
	return Baseline{CanonicalHost: host, Scope: scope, SlotIndex: slot, ModelVersion: BaselineModelVersion, Alpha: baselinePriorAlpha, Beta: baselinePriorBeta, LastUpdatedAtMS: atMS}
}

func trainBaseline(b Baseline, total int, localDate string, atMS int64, settings Settings) Baseline {
	b = decayedBaseline(b, atMS, settings.BaselineHalfLifeHours)
	b.Alpha += float64(total)
	b.Beta++
	b.ObservationCount++
	if b.FirstTrainedAtMS == nil || atMS < *b.FirstTrainedAtMS {
		value := atMS
		b.FirstTrainedAtMS = &value
	}
	if b.LastNormalTrainedAtMS == nil || atMS > *b.LastNormalTrainedAtMS {
		value := atMS
		b.LastNormalTrainedAtMS = &value
	}
	if atMS > b.LastUpdatedAtMS {
		b.LastUpdatedAtMS = atMS
	}
	if b.Scope == BaselineScopeSlot && localDate != "" {
		seen := false
		for _, date := range b.TrainedLocalDates {
			if date == localDate {
				seen = true
				break
			}
		}
		if !seen {
			b.TrainedLocalDates = append(b.TrainedLocalDates, localDate)
			sort.Strings(b.TrainedLocalDates)
		}
	}
	return b
}

func slotReady(b *Baseline) bool { return b != nil && len(b.TrainedLocalDates) >= SlotMaturityDays }

func fallbackReady(b *Baseline) bool {
	if b == nil || b.ObservationCount < FallbackMinimumObservations || b.FirstTrainedAtMS == nil || b.LastNormalTrainedAtMS == nil {
		return false
	}
	return *b.LastNormalTrainedAtMS-*b.FirstTrainedAtMS >= int64(FallbackMinimumSpanHours*time.Hour/time.Millisecond)
}

// Readiness derives selection readiness without changing any persisted
// timestamp. In particular, it does not treat a scoring/decay time as normal
// training time.
func Readiness(slot, fallback *Baseline) BaselineReadiness {
	result := BaselineReadiness{}
	if slot != nil {
		result.SlotMatureDays = len(slot.TrainedLocalDates)
		result.SlotReady = slotReady(slot)
	}
	if fallback != nil {
		result.FallbackObservationCount = fallback.ObservationCount
		result.FallbackFirstTrainedAtMS = fallback.FirstTrainedAtMS
		result.FallbackLastNormalTrainedAtMS = fallback.LastNormalTrainedAtMS
		result.FallbackReady = fallbackReady(fallback)
	}
	return result
}
