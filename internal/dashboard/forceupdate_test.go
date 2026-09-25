//go:build windows

package dashboard

import (
	"testing"
	"time"
)

// TestForceUpdateState_EnqueueAcceptsNewCommand pins the happy path:
// a fresh (host, command_id) pair is enqueued, returns Accepted, and is
// visible via Pending until consumed.
func TestForceUpdateState_EnqueueAcceptsNewCommand(t *testing.T) {
	s := newForceUpdateState()

	outcome, cmd := s.Enqueue("host-a", "cmd-1", "operator note", "v26.9.24")
	if outcome != forceUpdateOutcomeAccepted {
		t.Fatalf("first Enqueue outcome = %q, want %q", outcome, forceUpdateOutcomeAccepted)
	}
	if cmd == nil {
		t.Fatal("first Enqueue returned nil cmd")
	}
	if cmd.CommandID != "cmd-1" || cmd.Host != "host-a" {
		t.Errorf("cmd fields = %+v, want CommandID=cmd-1 Host=host-a", cmd)
	}

	pending := s.Pending("host-a")
	if len(pending) != 1 {
		t.Fatalf("Pending returned %d entries, want 1", len(pending))
	}
	if pending[0].CommandID != "cmd-1" {
		t.Errorf("Pending[0].CommandID = %q, want cmd-1", pending[0].CommandID)
	}
}

// TestForceUpdateState_DuplicateWithinWindowReturnsDuplicate is the
// core idempotency test: the second enqueue with the same
// (host, command_id) inside the idempotency window returns Duplicate
// without appending a second command to the slice.
func TestForceUpdateState_DuplicateWithinWindowReturnsDuplicate(t *testing.T) {
	s := newForceUpdateState()

	first, _ := s.Enqueue("host-a", "cmd-1", "first", "v26.9.24")
	if first != forceUpdateOutcomeAccepted {
		t.Fatalf("first outcome = %q, want Accepted", first)
	}

	second, secondCmd := s.Enqueue("host-a", "cmd-1", "second", "v26.9.24")
	if second != forceUpdateOutcomeDuplicate {
		t.Fatalf("second outcome = %q, want Duplicate", second)
	}
	if secondCmd == nil {
		t.Fatal("duplicate Enqueue returned nil; expected original command")
	}
	if secondCmd.CommandID != "cmd-1" {
		t.Errorf("duplicate returned cmd %q, want cmd-1", secondCmd.CommandID)
	}

	// Crucially, only ONE entry should be in the slice — duplicate
	// must not enqueue a second one.
	pending := s.Pending("host-a")
	if len(pending) != 1 {
		t.Fatalf("Pending length = %d after duplicate, want 1", len(pending))
	}
}

// TestForceUpdateState_DistinctCommandIDsAreNotDeduped guards the
// negative case: two distinct command_ids against the same host must
// each be enqueued. Easy regression if the dedupe key is accidentally
// keyed on host alone.
func TestForceUpdateState_DistinctCommandIDsAreNotDeduped(t *testing.T) {
	s := newForceUpdateState()

	out1, _ := s.Enqueue("host-a", "cmd-1", "", "v26.9.24")
	out2, _ := s.Enqueue("host-a", "cmd-2", "", "v26.9.24")
	if out1 != forceUpdateOutcomeAccepted || out2 != forceUpdateOutcomeAccepted {
		t.Fatalf("expected both Accepted, got %q / %q", out1, out2)
	}
	pending := s.Pending("host-a")
	if len(pending) != 2 {
		t.Fatalf("Pending length = %d, want 2", len(pending))
	}
}

func TestForceUpdateState_DeliveryRetriesUntilAcknowledged(t *testing.T) {
	s := newForceUpdateState()
	s.Enqueue("host-a", "cmd-1", "", "v26.9.24")

	first := s.Consume("host-a")
	second := s.Consume("host-a")
	if first == nil || second == nil || first.CommandID != second.CommandID {
		t.Fatalf("retry deliveries = %+v / %+v, want cmd-1 twice", first, second)
	}
	s.Acknowledge("host-a", "cmd-1")
	if got := s.Consume("host-a"); got != nil {
		t.Fatalf("Consume after acknowledgement = %+v, want nil", got)
	}
}

// TestForceUpdateState_ConsumeReturnsNilWhenEmpty guards the empty
// queue case. Without this guard the report handler would attach a
// nil pending_command to every report body when the queue is idle.
func TestForceUpdateState_ConsumeReturnsNilWhenEmpty(t *testing.T) {
	s := newForceUpdateState()

	if got := s.Consume("host-a"); got != nil {
		t.Fatalf("Consume on empty state = %+v, want nil", got)
	}
}

func TestForceUpdateState_DeliveredCommandRemainsDuplicate(t *testing.T) {
	s := newForceUpdateState()

	s.Enqueue("host-a", "cmd-1", "", "v26.9.24")
	if got := s.Consume("host-a"); got == nil {
		t.Fatal("Consume returned nil")
	}

	out, _ := s.Enqueue("host-a", "cmd-1", "", "v26.9.24")
	if out != forceUpdateOutcomeDuplicate {
		t.Errorf("post-completion Enqueue outcome = %q, want Duplicate", out)
	}
}

// TestForceUpdateState_ExpiredEntriesGarbageCollected verifies the
// 24h-window GC: by shrinking the window for the test, an old
// command is evicted on the next access.
func TestForceUpdateState_ExpiredEntriesGarbageCollected(t *testing.T) {
	s := newForceUpdateState()
	// Shrink the window so a 100ms-old entry is "expired" by the
	// time we run the next Enqueue.
	orig := forceUpdateIdempotencyWindow
	forceUpdateIdempotencyWindow = 50 * time.Millisecond
	t.Cleanup(func() { forceUpdateIdempotencyWindow = orig })

	s.Enqueue("host-a", "cmd-1", "", "v26.9.24")
	time.Sleep(75 * time.Millisecond)

	// New Enqueue triggers gcLocked inside Enqueue; the old entry
	// should be evicted.
	out, _ := s.Enqueue("host-a", "cmd-2", "", "v26.9.24")
	if out != forceUpdateOutcomeAccepted {
		t.Fatalf("post-expiry Enqueue outcome = %q, want Accepted", out)
	}
	pending := s.Pending("host-a")
	if len(pending) != 1 {
		t.Fatalf("Pending length = %d, want 1 (only the new entry)", len(pending))
	}
	if pending[0].CommandID != "cmd-2" {
		t.Errorf("Pending[0].CommandID = %q, want cmd-2 (old cmd-1 should have been evicted)", pending[0].CommandID)
	}
}

// TestForceUpdateState_DifferentHostsIsolated confirms that hosts
// are isolated: an enqueue for host-a must not affect host-b's queue.
func TestForceUpdateState_DifferentHostsIsolated(t *testing.T) {
	s := newForceUpdateState()

	s.Enqueue("host-a", "cmd-1", "", "v26.9.24")
	out, _ := s.Enqueue("host-b", "cmd-1", "", "v26.9.24")
	if out != forceUpdateOutcomeAccepted {
		t.Fatalf("different-host enqueue = %q, want Accepted (no dedupe across hosts)", out)
	}
	if len(s.Pending("host-a")) != 1 {
		t.Errorf("host-a pending = %d, want 1", len(s.Pending("host-a")))
	}
	if len(s.Pending("host-b")) != 1 {
		t.Errorf("host-b pending = %d, want 1", len(s.Pending("host-b")))
	}
}

// TestAgentVersionLessThan covers the floor-version check. The
// contract: a < b when a's component-wise parsed form is less than
// b's. v-prefix is tolerated.
func TestAgentVersionLessThan(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"26.9.17", "26.9.24", true},
		{"26.9.24", "26.9.17", false},
		{"26.9.17", "26.9.17", false},
		{"v26.9.17", "26.9.24", true},
		{"26.9.17", "v26.10.0", true},
		// Month digit flips correctly (10 > 9).
		{"26.9.24", "26.10.0", true},
		{"26.10.0", "26.9.24", false},
		// Garbage falls back to "less".
		{"", "26.9.24", true},
		{"garbage", "26.9.24", true},
		{"26.9.24", "garbage", false},
	}
	for _, tc := range cases {
		if got := agentVersionLessThan(tc.a, tc.b); got != tc.want {
			t.Errorf("agentVersionLessThan(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestForceUpdateCommandIDRE covers the accepted command_id alphabet.
// UUIDv4 (canonical 36-char hex-with-dashes) and operator keys with
// the documented alphabet must match; whitespace, empty strings,
// and overlong inputs must not.
func TestForceUpdateCommandIDRE(t *testing.T) {
	good := []string{
		"7f3a1b2c-1234-4abc-9def-1234567890ab", // UUIDv4
		"build-2026-09-24-001",                 // operator key
		"abcdefgh",                             // min length 8
		"a_b-c1234567890",                      // mixed separators
	}
	for _, g := range good {
		if !forceUpdateCommandIDRE.MatchString(g) {
			t.Errorf("expected %q to match", g)
		}
	}
	bad := []string{
		"",
		"short",             // < 8 chars
		"contains space",    // whitespace rejected
		"contains\nnewline", // control chars rejected
		"contains/slash",    // slash rejected (log-line safety)
		"contains\"quote",   // quote rejected
	}
	for _, b := range bad {
		if forceUpdateCommandIDRE.MatchString(b) {
			t.Errorf("expected %q NOT to match", b)
		}
	}
}
