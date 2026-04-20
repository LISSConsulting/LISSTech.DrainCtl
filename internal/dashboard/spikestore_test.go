//go:build windows

package dashboard

import (
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

func makeDCSpike(channel string, windowStart time.Time) dc.SpikePayload {
	return dc.SpikePayload{
		Host:              "SRV01",
		Channel:           channel,
		WindowStart:       windowStart,
		WindowEnd:         windowStart.Add(10 * time.Second),
		Observed:          20,
		Expected:          2.0,
		TailProbability:   1e-5,
		ConfirmationCount: 2,
		FirstSeenAt:       windowStart,
	}
}

// TestAppendDedup_InsertsFreshEntry verifies that a spike inserted into an
// empty ring (no prior entries for this host) is always accepted.
func TestAppendDedup_InsertsFreshEntry(t *testing.T) {
	s := NewSpikeStore()
	base := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
	entry, inserted := s.AppendDedup("SRV01", makeDCSpike("Application", base))
	if !inserted {
		t.Fatal("expected inserted=true for first entry")
	}
	if entry.ID == 0 {
		t.Error("entry ID should be non-zero")
	}
	if entry.Channel != "Application" {
		t.Errorf("channel = %q, want Application", entry.Channel)
	}
}

// TestAppendDedup_DedupsSameChannelAndWindow verifies that a second POST with
// the same (Channel, WindowStart) as the ring's newest entry is silently
// dropped — the remote agent may retry on network blips without double-inserting.
func TestAppendDedup_DedupsSameChannelAndWindow(t *testing.T) {
	s := NewSpikeStore()
	base := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
	spike := makeDCSpike("Application", base)
	first, inserted := s.AppendDedup("SRV01", spike)
	if !inserted {
		t.Fatal("first insert failed")
	}
	_, inserted2 := s.AppendDedup("SRV01", spike)
	if inserted2 {
		t.Error("duplicate (Channel, WindowStart) should be deduplicated")
	}
	// Verify the ring still has exactly one entry.
	entries := s.Recent("SRV01", 10)
	if len(entries) != 1 {
		t.Fatalf("ring size = %d, want 1", len(entries))
	}
	if entries[0].ID != first.ID {
		t.Errorf("surviving entry ID = %d, want %d", entries[0].ID, first.ID)
	}
}

// TestAppendDedup_InsertsOnDifferentChannel verifies that a second spike on a
// different channel is NOT treated as a duplicate even when WindowStart matches.
func TestAppendDedup_InsertsOnDifferentChannel(t *testing.T) {
	s := NewSpikeStore()
	base := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
	s.AppendDedup("SRV01", makeDCSpike("Application", base))
	_, inserted := s.AppendDedup("SRV01", makeDCSpike("System", base))
	if !inserted {
		t.Error("different channel should not be deduplicated")
	}
	if n := len(s.Recent("SRV01", 10)); n != 2 {
		t.Errorf("ring size = %d, want 2", n)
	}
}

// TestAppendDedup_InsertsOnDifferentWindow verifies that a second spike on the
// same channel but a different WindowStart is NOT deduplicated.
func TestAppendDedup_InsertsOnDifferentWindow(t *testing.T) {
	s := NewSpikeStore()
	base := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
	s.AppendDedup("SRV01", makeDCSpike("Application", base))
	_, inserted := s.AppendDedup("SRV01", makeDCSpike("Application", base.Add(10*time.Second)))
	if !inserted {
		t.Error("different WindowStart should not be deduplicated")
	}
	if n := len(s.Recent("SRV01", 10)); n != 2 {
		t.Errorf("ring size = %d, want 2", n)
	}
}

// TestAppendDedup_IndependentPerHost verifies host-level isolation: a dedup
// match on SRV01 does not suppress an identical spike reported for SRV02.
func TestAppendDedup_IndependentPerHost(t *testing.T) {
	s := NewSpikeStore()
	base := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
	s.AppendDedup("SRV01", dc.SpikePayload{
		Host: "SRV01", Channel: "Application", WindowStart: base, WindowEnd: base.Add(10 * time.Second),
	})
	_, inserted := s.AppendDedup("SRV02", dc.SpikePayload{
		Host: "SRV02", Channel: "Application", WindowStart: base, WindowEnd: base.Add(10 * time.Second),
	})
	if !inserted {
		t.Error("identical (Channel, WindowStart) on a different host should insert")
	}
}
