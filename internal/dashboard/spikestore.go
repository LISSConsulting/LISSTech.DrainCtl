//go:build windows

package dashboard

import (
	"sync"
	"sync/atomic"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
)

const (
	// spikeStoreHostCapacity is the per-host ring-buffer size. Matches
	// data-model.md §7 "bounded in-memory ring buffer per host, capacity 20".
	spikeStoreHostCapacity = 20

	// spikeStoreDefaultLimit is the default value for the ?limit= query param
	// on GET /api/evtspike/spikes.
	spikeStoreDefaultLimit = 20

	// spikeStoreMaxLimit is the handler-side upper clamp on ?limit=, per
	// contracts/dashboard-sse-events.md (limit ∈ [1..50]).
	spikeStoreMaxLimit = 50
)

// SpikeStore is a bounded, per-host in-memory ring buffer of confirmed spike
// events. Assigns monotonic int64 IDs across the service lifetime so the UI
// has stable list keys. Not persisted (FR-028) — cleared on service restart.
type SpikeStore struct {
	mu     sync.RWMutex
	hosts  map[string]*hostRing
	nextID atomic.Int64
}

type hostRing struct {
	buf  []evtspike.RecentSpikeEntry
	head int // index where the next appended entry will be written
	size int // number of valid entries currently in buf (≤ cap(buf))
}

// NewSpikeStore returns an empty store ready for concurrent use.
func NewSpikeStore() *SpikeStore {
	return &SpikeStore{hosts: make(map[string]*hostRing)}
}

// Append records a spike for host and returns the resulting entry with its
// assigned ID. Drops the oldest entry when the per-host ring is full.
func (s *SpikeStore) Append(host string, spike evtspike.SpikePayload) evtspike.RecentSpikeEntry {
	entry := evtspike.RecentSpikeEntry{
		ID:           s.nextID.Add(1),
		SpikePayload: spike,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ring, ok := s.hosts[host]
	if !ok {
		ring = &hostRing{buf: make([]evtspike.RecentSpikeEntry, spikeStoreHostCapacity)}
		s.hosts[host] = ring
	}
	ring.buf[ring.head] = entry
	ring.head = (ring.head + 1) % spikeStoreHostCapacity
	if ring.size < spikeStoreHostCapacity {
		ring.size++
	}
	return entry
}

// AppendDedup is like Append but drops payloads whose (Channel, WindowStart)
// matches the ring's newest entry. Guards against an occasional double-POST
// from a remote agent (e.g., a retry after a transient network blip); the
// identity is strong enough because the detector confirms at most one spike
// per (channel, window). Returns (entry, true) on insert, zero-value+false on
// dedup.
func (s *SpikeStore) AppendDedup(host string, spike evtspike.SpikePayload) (evtspike.RecentSpikeEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ring, ok := s.hosts[host]
	if ok && ring.size > 0 {
		prevIdx := ring.head - 1
		if prevIdx < 0 {
			prevIdx += spikeStoreHostCapacity
		}
		prev := ring.buf[prevIdx].SpikePayload
		if prev.Channel == spike.Channel && prev.WindowStart.Equal(spike.WindowStart) {
			return evtspike.RecentSpikeEntry{}, false
		}
	}
	entry := evtspike.RecentSpikeEntry{
		ID:           s.nextID.Add(1),
		SpikePayload: spike,
	}
	if !ok {
		ring = &hostRing{buf: make([]evtspike.RecentSpikeEntry, spikeStoreHostCapacity)}
		s.hosts[host] = ring
	}
	ring.buf[ring.head] = entry
	ring.head = (ring.head + 1) % spikeStoreHostCapacity
	if ring.size < spikeStoreHostCapacity {
		ring.size++
	}
	return entry, true
}

// Recent returns up to limit newest-first entries for host. Returns an empty
// (non-nil) slice when the host has no recorded spikes so JSON renders `[]`.
func (s *SpikeStore) Recent(host string, limit int) []evtspike.RecentSpikeEntry {
	if limit <= 0 {
		return []evtspike.RecentSpikeEntry{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ring, ok := s.hosts[host]
	if !ok || ring.size == 0 {
		return []evtspike.RecentSpikeEntry{}
	}
	n := limit
	if n > ring.size {
		n = ring.size
	}
	out := make([]evtspike.RecentSpikeEntry, 0, n)
	idx := ring.head - 1
	if idx < 0 {
		idx += spikeStoreHostCapacity
	}
	for i := 0; i < n; i++ {
		out = append(out, ring.buf[idx])
		idx--
		if idx < 0 {
			idx += spikeStoreHostCapacity
		}
	}
	return out
}
