//go:build windows

package telemetry

import (
	"context"
	"testing"
	"time"
)

func newEventSpikeStore(t *testing.T) (*EventSpikeStore, *DB) {
	t.Helper()
	db := openTestDB(t)
	return NewEventSpikeStore(db), db
}

func sampleSpike(host, channel string, windowStart time.Time, observed int) EventSpike {
	return EventSpike{
		Host:              host,
		Channel:           channel,
		WindowStart:       windowStart,
		WindowEnd:         windowStart.Add(5 * time.Minute),
		Observed:          observed,
		Expected:          5.0,
		TailProbability:   0.001,
		ConfirmationCount: 3,
		FirstSeenAt:       windowStart,
	}
}

func TestEventSpikes_InsertRecent(t *testing.T) {
	s, _ := newEventSpikeStore(t)
	base := time.Now().UTC().Truncate(time.Second)

	got, inserted, err := s.Insert(context.Background(), sampleSpike("SRV01", "Application", base, 42))
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if !inserted {
		t.Fatal("inserted = false on fresh insert")
	}
	if got.ID <= 0 {
		t.Errorf("ID = %d, want > 0", got.ID)
	}

	entries, err := s.Recent(context.Background(), "SRV01", 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("Recent len = %d, want 1", len(entries))
	}
	if entries[0].Observed != 42 {
		t.Errorf("Observed = %d, want 42", entries[0].Observed)
	}
	if entries[0].Channel != "Application" {
		t.Errorf("Channel = %q, want Application", entries[0].Channel)
	}
}

func TestEventSpikes_DedupOnIdentity(t *testing.T) {
	s, _ := newEventSpikeStore(t)
	base := time.Now().UTC().Truncate(time.Second)

	first, inserted, err := s.Insert(context.Background(), sampleSpike("SRV01", "Application", base, 42))
	if err != nil || !inserted {
		t.Fatalf("first Insert: err=%v inserted=%v", err, inserted)
	}

	dup, inserted2, err := s.Insert(context.Background(), sampleSpike("SRV01", "Application", base, 99))
	if err != nil {
		t.Fatalf("dup Insert: %v", err)
	}
	if inserted2 {
		t.Error("inserted = true on duplicate identity")
	}
	if dup.ID != first.ID {
		t.Errorf("dup.ID = %d, want %d (lookup should return the pre-existing row)", dup.ID, first.ID)
	}
	// The lookup returns the *original* row — the new observed count is discarded.
	if dup.Observed != 42 {
		t.Errorf("dup.Observed = %d, want 42 (existing row preserved)", dup.Observed)
	}

	entries, err := s.Recent(context.Background(), "SRV01", 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("Recent len = %d after dup Insert, want 1", len(entries))
	}
}

func TestEventSpikes_DifferentChannelsSameWindow(t *testing.T) {
	s, _ := newEventSpikeStore(t)
	base := time.Now().UTC().Truncate(time.Second)

	for _, ch := range []string{"Application", "System", "Security"} {
		if _, inserted, err := s.Insert(context.Background(), sampleSpike("SRV01", ch, base, 10)); err != nil || !inserted {
			t.Fatalf("Insert %s: err=%v inserted=%v", ch, err, inserted)
		}
	}

	entries, _ := s.Recent(context.Background(), "SRV01", 10)
	if len(entries) != 3 {
		t.Errorf("Recent len = %d, want 3 (one per channel)", len(entries))
	}
}

func TestEventSpikes_PerHostIsolation(t *testing.T) {
	s, _ := newEventSpikeStore(t)
	base := time.Now().UTC().Truncate(time.Second)

	_, _, _ = s.Insert(context.Background(), sampleSpike("SRV01", "Application", base, 10))
	_, _, _ = s.Insert(context.Background(), sampleSpike("SRV02", "Application", base, 20))

	srv1, _ := s.Recent(context.Background(), "SRV01", 10)
	srv2, _ := s.Recent(context.Background(), "SRV02", 10)

	if len(srv1) != 1 || srv1[0].Observed != 10 {
		t.Errorf("SRV01 entries = %+v, want 1 entry with Observed=10", srv1)
	}
	if len(srv2) != 1 || srv2[0].Observed != 20 {
		t.Errorf("SRV02 entries = %+v, want 1 entry with Observed=20", srv2)
	}
}

func TestEventSpikes_RecentNewestFirst(t *testing.T) {
	s, _ := newEventSpikeStore(t)
	base := time.Now().UTC().Truncate(time.Second)

	for i := 0; i < 5; i++ {
		_, _, err := s.Insert(context.Background(),
			sampleSpike("SRV01", "Application", base.Add(time.Duration(i)*time.Minute), i))
		if err != nil {
			t.Fatalf("Insert[%d]: %v", i, err)
		}
	}

	entries, _ := s.Recent(context.Background(), "SRV01", 10)
	if len(entries) != 5 {
		t.Fatalf("Recent len = %d, want 5", len(entries))
	}
	// Newest first: Observed = 4, 3, 2, 1, 0 in order.
	for i, want := range []int{4, 3, 2, 1, 0} {
		if entries[i].Observed != want {
			t.Errorf("entries[%d].Observed = %d, want %d (newest-first ordering)", i, entries[i].Observed, want)
		}
	}
}

func TestEventSpikes_RangeFiltersByWindow(t *testing.T) {
	s, _ := newEventSpikeStore(t)
	base := time.Now().UTC().Truncate(time.Minute)

	// Insert 10 spikes at minute 0..9.
	for i := 0; i < 10; i++ {
		_, _, err := s.Insert(context.Background(),
			sampleSpike("SRV01", "Application", base.Add(time.Duration(i)*time.Minute), i))
		if err != nil {
			t.Fatalf("Insert[%d]: %v", i, err)
		}
	}

	// Range [base+3min, base+7min) should return spikes at 3, 4, 5, 6.
	entries, err := s.Range(context.Background(), "SRV01",
		base.Add(3*time.Minute), base.Add(7*time.Minute), 100)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("Range len = %d, want 4", len(entries))
	}
	// Newest first: 6, 5, 4, 3.
	for i, want := range []int{6, 5, 4, 3} {
		if entries[i].Observed != want {
			t.Errorf("entries[%d].Observed = %d, want %d", i, entries[i].Observed, want)
		}
	}
}

func TestEventSpikes_EmptyHostReturnsEmptySlice(t *testing.T) {
	s, _ := newEventSpikeStore(t)

	entries, err := s.Recent(context.Background(), "GHOST", 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if entries == nil {
		t.Error("Recent returned nil slice — must return non-nil for JSON `[]` rendering")
	}
	if len(entries) != 0 {
		t.Errorf("len = %d, want 0", len(entries))
	}

	rng, err := s.Range(context.Background(), "GHOST",
		time.Now().Add(-time.Hour), time.Now(), 10)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if rng == nil {
		t.Error("Range returned nil slice")
	}
	if len(rng) != 0 {
		t.Errorf("Range len = %d, want 0", len(rng))
	}
}

func TestEventSpikes_IDsMonotonic(t *testing.T) {
	s, _ := newEventSpikeStore(t)
	base := time.Now().UTC().Truncate(time.Second)

	var ids []int64
	for i := 0; i < 5; i++ {
		got, _, err := s.Insert(context.Background(),
			sampleSpike("SRV01", "Application", base.Add(time.Duration(i)*time.Minute), i))
		if err != nil {
			t.Fatalf("Insert[%d]: %v", i, err)
		}
		ids = append(ids, got.ID)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Errorf("id[%d]=%d not > id[%d]=%d", i, ids[i], i-1, ids[i-1])
		}
	}
}
