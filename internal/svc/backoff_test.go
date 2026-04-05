//go:build windows

package svc

import (
	"testing"
	"time"
)

func TestBackoffDuration(t *testing.T) {
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{-1, 5 * time.Minute}, // negative treated as zero — return base
		{0, 5 * time.Minute},
		{1, 10 * time.Minute},
		{2, 20 * time.Minute},
		{3, 40 * time.Minute},
		{4, 80 * time.Minute},
		{5, 160 * time.Minute},    // d == configFetchMax — capped via >= guard
		{6, 160 * time.Minute},    // shift capped at 5 → same as failures=5
		{9, 160 * time.Minute},    // still capped
		{1000, 160 * time.Minute}, // large value still capped at configFetchMax
	}

	for _, tc := range cases {
		got := backoffDuration(tc.failures)
		if got != tc.want {
			t.Errorf("backoffDuration(%d) = %s, want %s", tc.failures, got, tc.want)
		}
	}
}

// TestBackoffDuration_NeverExceedsMax verifies the invariant that backoffDuration
// never returns more than configFetchMax for any non-negative failure count.
func TestBackoffDuration_NeverExceedsMax(t *testing.T) {
	for failures := 0; failures <= 20; failures++ {
		got := backoffDuration(failures)
		if got > configFetchMax {
			t.Errorf("backoffDuration(%d) = %s, exceeds configFetchMax %s", failures, got, configFetchMax)
		}
	}
}
