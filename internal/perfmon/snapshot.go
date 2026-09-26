//go:build windows

package perfmon

import (
	"math"
	"sort"
)

// Percentile computes the p-th percentile (0-100) from a sorted slice of
// float64 values using linear interpolation. Returns 0 for empty input.
func Percentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return sorted[0]
	}
	// Rank = p/100 * (n-1)
	rank := (p / 100.0) * float64(n-1)
	lo := int(math.Floor(rank))
	hi := lo + 1
	if hi >= n {
		return sorted[n-1]
	}
	frac := rank - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

// AggregateValues computes P50, P95, and Max from a slice of values.
// The input slice is sorted in place.
func AggregateValues(values []float64) (p50, p95, max float64) {
	if len(values) == 0 {
		return 0, 0, 0
	}
	sort.Float64s(values)
	p50 = Percentile(values, 50)
	p95 = Percentile(values, 95)
	max = values[len(values)-1]
	return p50, p95, max
}

// AggregateServicePercentiles returns the median and the service-level P95.
// For lower-is-better metrics, P95 is the numeric 95th percentile. For
// higher-is-better metrics (FPS and frame quality), it is the numeric 5th
// percentile: 95% of sessions are at or above that floor.
// The input slice is sorted in place.
func AggregateServicePercentiles(values []float64, higherIsBetter bool) (p50, p95 float64) {
	if len(values) == 0 {
		return 0, 0
	}
	sort.Float64s(values)
	p50 = Percentile(values, 50)
	tail := 95.0
	if higherIsBetter {
		tail = 5
	}
	return p50, Percentile(values, tail)
}

// filterRemoteFXValues removes invalid PDH instance values in place. A zero
// FPS or quality value denotes an inactive stream; zero remains valid for
// lower-is-better loss and skipped-frame metrics.
func filterRemoteFXValues(values []float64, min, max float64, higherIsBetter bool) []float64 {
	n := 0
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < min || value > max ||
			(higherIsBetter && value == 0) {
			continue
		}
		values[n] = value
		n++
	}
	return values[:n]
}

// RoundTo rounds a float64 to n decimal places.
func RoundTo(val float64, places int) float64 {
	pow := math.Pow(10, float64(places))
	return math.Round(val*pow) / pow
}
