//go:build windows

package dashboard

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
)

// SSEEvent is a single event broadcast to all connected browsers.
type SSEEvent struct {
	Type      string          `json:"type"` // "server_update", "server_deleted", or "settings_update"
	Host      string          `json:"host,omitempty"`
	Data      json.RawMessage `json:"data"`
	Timestamp time.Time       `json:"timestamp"`
}

// subscriber is a connected browser session consuming the event stream.
type subscriber struct {
	ch        chan []byte
	id        string
	done      chan struct{}
	closeOnce sync.Once // guards close(done) — safe from both Unsubscribe and Broadcast
}

// close signals the subscriber to stop. Safe to call multiple times.
func (s *subscriber) stop() {
	s.closeOnce.Do(func() { close(s.done) })
}

// maxSubscribers is the upper bound on concurrent SSE connections.
// Prevents resource exhaustion from runaway reconnect loops or abuse.
const maxSubscribers = 100

// Broker manages SSE subscribers and broadcasts events to all of them.
type Broker struct {
	mu          sync.RWMutex
	subscribers map[string]*subscriber
	nextID      int

	stateMu       sync.Mutex
	detectorState map[string]string
}

// NewBroker creates an empty broker.
func NewBroker() *Broker {
	return &Broker{
		subscribers:   make(map[string]*subscriber),
		detectorState: make(map[string]string),
	}
}

// ErrTooManySubscribers is returned when the subscriber cap is reached.
var ErrTooManySubscribers = fmt.Errorf("too many SSE subscribers")

// Subscribe registers a new subscriber and returns its channel and done signal.
// The caller must call Unsubscribe when finished.
// Returns ErrTooManySubscribers if the cap is reached.
func (b *Broker) Subscribe() (id string, ch <-chan []byte, done <-chan struct{}, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.subscribers) >= maxSubscribers {
		return "", nil, nil, ErrTooManySubscribers
	}

	b.nextID++
	sub := &subscriber{
		ch:   make(chan []byte, 16), // buffered to absorb brief bursts
		id:   fmt.Sprintf("sub-%d", b.nextID),
		done: make(chan struct{}),
	}
	b.subscribers[sub.id] = sub
	slog.Debug("sse: subscriber added", "id", sub.id, "total", len(b.subscribers))
	return sub.id, sub.ch, sub.done, nil
}

// Unsubscribe removes a subscriber and signals it to stop.
func (b *Broker) Unsubscribe(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if sub, ok := b.subscribers[id]; ok {
		sub.stop()
		delete(b.subscribers, id)
		slog.Debug("sse: subscriber removed", "id", id, "total", len(b.subscribers))
	}
}

// Broadcast sends a pre-marshaled payload to all subscribers. Slow subscribers
// whose channel buffer is full are evicted. The lock is held only to snapshot
// the subscriber list; channel sends happen outside the lock.
func (b *Broker) Broadcast(payload []byte) {
	// Snapshot subscribers under read lock.
	b.mu.RLock()
	snapshot := make([]*subscriber, 0, len(b.subscribers))
	for _, sub := range b.subscribers {
		snapshot = append(snapshot, sub)
	}
	b.mu.RUnlock()

	// Send outside the lock — no contention with Subscribe/Unsubscribe.
	var evict []*subscriber
	for _, sub := range snapshot {
		select {
		case sub.ch <- payload:
			// delivered
		default:
			evict = append(evict, sub)
		}
	}

	// Evict slow subscribers under write lock.
	if len(evict) > 0 {
		b.mu.Lock()
		for _, sub := range evict {
			if _, ok := b.subscribers[sub.id]; ok {
				slog.Warn("sse: evicting slow subscriber", "id", sub.id)
				sub.stop() // sync.Once — safe even if Unsubscribe races
				delete(b.subscribers, sub.id)
			}
		}
		b.mu.Unlock()
	}
}

// Count returns the number of active subscribers.
func (b *Broker) Count() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}

// PublishDetectorStatus broadcasts a detector_status SSE event only when the
// host's reported state differs from the last one broadcast. Subsystems may
// call this every evaluation cycle; only transitions reach subscribers, so
// the stream stays quiet on steady state.
//
// Returns true when an event was emitted, false when the call was suppressed.
func (b *Broker) PublishDetectorStatus(status evtspike.DetectorStatus) bool {
	b.stateMu.Lock()
	prev, seen := b.detectorState[status.Host]
	if seen && prev == status.State {
		b.stateMu.Unlock()
		return false
	}
	b.detectorState[status.Host] = status.State
	b.stateMu.Unlock()

	data, err := json.Marshal(status)
	if err != nil {
		slog.Warn("sse: PublishDetectorStatus: marshal failed", "host", status.Host, "error", err)
		return false
	}
	payload, err := json.Marshal(SSEEvent{
		Type:      "detector_status",
		Host:      status.Host,
		Data:      data,
		Timestamp: time.Now(),
	})
	if err != nil {
		slog.Warn("sse: PublishDetectorStatus: envelope marshal failed", "host", status.Host, "error", err)
		return false
	}
	b.Broadcast(payload)
	return true
}

// PublishRecentSpike broadcasts a recent_spike SSE event. Every call emits —
// spikes are by construction distinct events, no dedup.
func (b *Broker) PublishRecentSpike(entry evtspike.RecentSpikeEntry) {
	data, err := json.Marshal(entry)
	if err != nil {
		slog.Warn("sse: PublishRecentSpike: marshal failed", "host", entry.Host, "error", err)
		return
	}
	payload, err := json.Marshal(SSEEvent{
		Type:      "recent_spike",
		Host:      entry.Host,
		Data:      data,
		Timestamp: time.Now(),
	})
	if err != nil {
		slog.Warn("sse: PublishRecentSpike: envelope marshal failed", "host", entry.Host, "error", err)
		return
	}
	b.Broadcast(payload)
}
