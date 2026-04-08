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

// RoundTo rounds a float64 to n decimal places.
func RoundTo(val float64, places int) float64 {
	pow := math.Pow(10, float64(places))
	return math.Round(val*pow) / pow
}
