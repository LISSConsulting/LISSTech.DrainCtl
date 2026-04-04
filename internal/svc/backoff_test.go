//go:build windows

package svc

import "testing"

func TestBackoffTicks(t *testing.T) {
	cases := []struct {
		failures int
		want     int
	}{
		{0, 10},
		{1, 20},
		{2, 40},
		{3, 80},
		{4, 160},
		{5, 320},
		{6, 320}, // capped
		{9, 320}, // capped
	}

	for _, tc := range cases {
		got := backoffTicks(tc.failures)
		if got != tc.want {
			t.Errorf("backoffTicks(%d) = %d, want %d", tc.failures, got, tc.want)
		}
	}
}
