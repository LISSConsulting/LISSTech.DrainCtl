//go:build windows

package dashboard

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// SSEEvent is a single event broadcast to all connected browsers.
type SSEEvent struct {
	Type      string          `json:"type"` // "server_update" or "settings_update"
	Host      string          `json:"host,omitempty"`
	Data      json.RawMessage `json:"data"`
	Timestamp time.Time       `json:"timestamp"`
}

// subscriber is a connected browser session consuming the event stream.
type subscriber struct {
	ch   chan []byte
	id   string
	done chan struct{} // closed when subscriber is removed
}

// Broker manages SSE subscribers and broadcasts events to all of them.
type Broker struct {
	mu          sync.RWMutex
	subscribers map[string]*subscriber
	nextID      int
}

// NewBroker creates an empty broker.
func NewBroker() *Broker {
	return &Broker{
		subscribers: make(map[string]*subscriber),
	}
}

// Subscribe registers a new subscriber and returns its channel and done signal.
// The caller must call Unsubscribe when finished.
func (b *Broker) Subscribe() (id string, ch <-chan []byte, done <-chan struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.nextID++
	sub := &subscriber{
		ch:   make(chan []byte, 16), // buffered to absorb brief bursts
		id:   fmt.Sprintf("sub-%d", b.nextID),
		done: make(chan struct{}),
	}
	b.subscribers[sub.id] = sub
	slog.Debug("sse: subscriber added", "id", sub.id, "total", len(b.subscribers))
	return sub.id, sub.ch, sub.done
}

// Unsubscribe removes a subscriber and closes its done channel.
func (b *Broker) Unsubscribe(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if sub, ok := b.subscribers[id]; ok {
		close(sub.done)
		delete(b.subscribers, id)
		slog.Debug("sse: subscriber removed", "id", id, "total", len(b.subscribers))
	}
}

// Broadcast sends a message to all subscribers. Slow subscribers whose
// channel buffer is full are evicted to prevent blocking.
func (b *Broker) Broadcast(event SSEEvent) {
	payload, err := json.Marshal(event)
	if err != nil {
		slog.Warn("sse: marshal event failed", "error", err)
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	for id, sub := range b.subscribers {
		select {
		case sub.ch <- payload:
			// delivered
		default:
			// channel full — evict slow subscriber
			slog.Warn("sse: evicting slow subscriber", "id", id)
			close(sub.done)
			delete(b.subscribers, id)
		}
	}
}

// Count returns the number of active subscribers.
func (b *Broker) Count() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}
