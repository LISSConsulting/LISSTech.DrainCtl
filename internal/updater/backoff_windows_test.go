//go:build windows

package updater

import (
	"testing"
	"time"
)

func TestBackoff_FailureSchedule(t *testing.T) {
	want := []time.Duration{
		5 * time.Minute,
		15 * time.Minute,
		45 * time.Minute,
		2 * time.Hour,
		6 * time.Hour,
		24 * time.Hour,
	}
	var b backoff
	for i, w := range want {
		got := b.recordFailure()
		if got != w {
			t.Errorf("failure %d: got %v, want %v", i+1, got, w)
		}
	}
}

func TestBackoff_CapsAtMax(t *testing.T) {
	var b backoff
	// Push past the schedule.
	for range len(backoffSchedule) + 5 {
		b.recordFailure()
	}
	got := b.recordFailure()
	if got != 24*time.Hour {
		t.Errorf("post-cap delay = %v, want 24h", got)
	}
}

func TestBackoff_SuccessResets(t *testing.T) {
	var b backoff
	b.recordFailure()
	b.recordFailure()
	b.recordFailure()
	if b.failureCount() != 3 {
		t.Fatalf("failureCount after 3 fails = %d, want 3", b.failureCount())
	}
	b.recordSuccess()
	if b.failureCount() != 0 {
		t.Errorf("failureCount after success = %d, want 0", b.failureCount())
	}
	// Next failure starts back at 5m, not where we left off.
	got := b.recordFailure()
	if got != 5*time.Minute {
		t.Errorf("post-reset first failure = %v, want 5m", got)
	}
}
