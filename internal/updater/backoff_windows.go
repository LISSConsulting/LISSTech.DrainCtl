//go:build windows

package updater

import "time"

// backoffSchedule is the exponential delay applied after consecutive poll
// failures, per FR-012. Index = number of consecutive failures - 1.
// Indices past the end of the slice clamp to the last entry.
var backoffSchedule = []time.Duration{
	5 * time.Minute,
	15 * time.Minute,
	45 * time.Minute,
	2 * time.Hour,
	6 * time.Hour,
	24 * time.Hour, // cap
}

// backoff is the retry-delay state machine used by the updater poll loop.
// It is NOT goroutine-safe — the poll loop is single-goroutine, so adding
// a mutex would be ceremony for no benefit.
type backoff struct {
	failures int
}

// recordFailure increments the failure counter and returns the additional
// delay to apply on top of the configured poll interval. The first failure
// returns 5m; subsequent failures escalate per backoffSchedule and cap at
// 24h.
func (b *backoff) recordFailure() time.Duration {
	b.failures++
	idx := b.failures - 1
	if idx >= len(backoffSchedule) {
		idx = len(backoffSchedule) - 1
	}
	return backoffSchedule[idx]
}

// recordSuccess resets the counter so the next failure starts fresh.
func (b *backoff) recordSuccess() {
	b.failures = 0
}

// failureCount exposes the current count for logging.
func (b *backoff) failureCount() int {
	return b.failures
}
