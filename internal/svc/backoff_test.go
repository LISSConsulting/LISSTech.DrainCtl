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
		{0, 5 * time.Minute},
		{1, 10 * time.Minute},
		{2, 20 * time.Minute},
		{3, 40 * time.Minute},
		{4, 80 * time.Minute},
		{5, 160 * time.Minute},
		{6, 160 * time.Minute}, // capped
		{9, 160 * time.Minute}, // capped
	}

	for _, tc := range cases {
		got := backoffDuration(tc.failures)
		if got != tc.want {
			t.Errorf("backoffDuration(%d) = %s, want %s", tc.failures, got, tc.want)
		}
	}
}
