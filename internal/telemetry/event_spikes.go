//go:build windows

package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// EventSpike is the telemetry-layer representation of a confirmed event-log
// spike. Kept as a plain-data struct (no drainctl/evtspike imports) so the
// telemetry package stays free of upward dependencies. The dashboard layer is
// responsible for marshalling to/from drainctl.SpikePayload at the boundary.
type EventSpike struct {
	ID                int64
	Host              string
	Channel           string
	WindowStart       time.Time
	WindowEnd         time.Time
	Observed          int
	Expected          float64
	TailProbability   float64
	ConfirmationCount int
	FirstSeenAt       time.Time
}

// EventSpikeStore persists confirmed event-log spike payloads to drainctl.db.
// Retention is driven by the retention worker using AuditDays — same TTL as
// the drain-mode audit table — so an operator who raises audit retention to
// keep a longer drain-mode trail automatically keeps a matching spike trail.
//
// Replaces the in-memory per-host ring buffer in internal/dashboard/spikestore.go.
// IDs are monotonic across service restarts (INTEGER PRIMARY KEY AUTOINCREMENT)
// so the frontend's SSE-vs-REST dedup by id remains correct after a bounce.
type EventSpikeStore struct {
	db *DB
}

// NewEventSpikeStore wraps an open DB.
func NewEventSpikeStore(db *DB) *EventSpikeStore {
	return &EventSpikeStore{db: db}
}

// Insert records a confirmed spike. ON CONFLICT on (host, channel, window_start_ms)
// silently ignores duplicates — protects against remote-agent POST retries after
// transient network blips and against the detector re-emitting a confirmed
// (channel, window) pair. Returns (spike, inserted): inserted=false means a row
// for that identity already existed (the returned struct has the existing row's
// ID so the caller can still attach it to SSE with a stable key, but should
// skip broadcasting).
func (s *EventSpikeStore) Insert(ctx context.Context, spike EventSpike) (EventSpike, bool, error) {
	now := time.Now().UTC().UnixMilli()
	windowStartMs := spike.WindowStart.UTC().UnixMilli()
	windowEndMs := spike.WindowEnd.UTC().UnixMilli()
	firstSeenMs := spike.FirstSeenAt.UTC().UnixMilli()

	res, err := s.db.writer.ExecContext(ctx,
		`INSERT INTO event_spikes
		    (host, channel, window_start_ms, window_end_ms, observed, expected,
		     tail_probability, confirmation_count, first_seen_at_ms, created_at_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(host, channel, window_start_ms) DO NOTHING`,
		spike.Host, spike.Channel, windowStartMs, windowEndMs,
		spike.Observed, spike.Expected, spike.TailProbability,
		spike.ConfirmationCount, firstSeenMs, now,
	)
	if err != nil {
		return EventSpike{}, false, fmt.Errorf("telemetry: event_spikes insert: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return EventSpike{}, false, fmt.Errorf("telemetry: event_spikes rows_affected: %w", err)
	}

	if affected == 1 {
		id, err := res.LastInsertId()
		if err != nil {
			return EventSpike{}, false, fmt.Errorf("telemetry: event_spikes last_insert_id: %w", err)
		}
		spike.ID = id
		return spike, true, nil
	}

	existing, err := s.lookup(ctx, spike.Host, spike.Channel, windowStartMs)
	if err != nil {
		return EventSpike{}, false, err
	}
	return existing, false, nil
}

// Recent returns up to limit newest-first entries for host.
func (s *EventSpikeStore) Recent(ctx context.Context, host string, limit int) ([]EventSpike, error) {
	if limit <= 0 {
		return []EventSpike{}, nil
	}
	rows, err := s.db.reader.QueryContext(ctx,
		`SELECT id, channel, window_start_ms, window_end_ms, observed, expected,
		        tail_probability, confirmation_count, first_seen_at_ms
		 FROM event_spikes
		 WHERE host = ?
		 ORDER BY window_start_ms DESC, id DESC
		 LIMIT ?`, host, limit)
	if err != nil {
		return nil, fmt.Errorf("telemetry: event_spikes recent: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanSpikes(rows, host)
}

// Range returns newest-first entries for host whose window_start falls in
// [from, to). Clamped to maxLimit rows so a pathological window can't evict
// the query budget.
func (s *EventSpikeStore) Range(ctx context.Context, host string, from, to time.Time, maxLimit int) ([]EventSpike, error) {
	if maxLimit <= 0 {
		return []EventSpike{}, nil
	}
	fromMs := from.UTC().UnixMilli()
	toMs := to.UTC().UnixMilli()
	rows, err := s.db.reader.QueryContext(ctx,
		`SELECT id, channel, window_start_ms, window_end_ms, observed, expected,
		        tail_probability, confirmation_count, first_seen_at_ms
		 FROM event_spikes
		 WHERE host = ? AND window_start_ms >= ? AND window_start_ms < ?
		 ORDER BY window_start_ms DESC, id DESC
		 LIMIT ?`, host, fromMs, toMs, maxLimit)
	if err != nil {
		return nil, fmt.Errorf("telemetry: event_spikes range: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanSpikes(rows, host)
}

func (s *EventSpikeStore) lookup(ctx context.Context, host, channel string, windowStartMs int64) (EventSpike, error) {
	var (
		id, winEndMs, firstMs int64
		observed, confCount   int
		expected, tailProb    float64
	)
	err := s.db.reader.QueryRowContext(ctx,
		`SELECT id, window_end_ms, observed, expected, tail_probability,
		        confirmation_count, first_seen_at_ms
		 FROM event_spikes
		 WHERE host = ? AND channel = ? AND window_start_ms = ?`,
		host, channel, windowStartMs,
	).Scan(&id, &winEndMs, &observed, &expected, &tailProb, &confCount, &firstMs)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return EventSpike{}, fmt.Errorf("telemetry: event_spikes lookup: row vanished after dedup")
		}
		return EventSpike{}, fmt.Errorf("telemetry: event_spikes lookup: %w", err)
	}
	return EventSpike{
		ID:                id,
		Host:              host,
		Channel:           channel,
		WindowStart:       time.UnixMilli(windowStartMs).UTC(),
		WindowEnd:         time.UnixMilli(winEndMs).UTC(),
		Observed:          observed,
		Expected:          expected,
		TailProbability:   tailProb,
		ConfirmationCount: confCount,
		FirstSeenAt:       time.UnixMilli(firstMs).UTC(),
	}, nil
}

func scanSpikes(rows *sql.Rows, host string) ([]EventSpike, error) {
	out := []EventSpike{}
	for rows.Next() {
		var (
			id, winStartMs, winEndMs, firstMs int64
			observed, confCount               int
			expected, tailProb                float64
			channel                           string
		)
		if err := rows.Scan(&id, &channel, &winStartMs, &winEndMs, &observed,
			&expected, &tailProb, &confCount, &firstMs); err != nil {
			return nil, fmt.Errorf("telemetry: event_spikes scan: %w", err)
		}
		out = append(out, EventSpike{
			ID:                id,
			Host:              host,
			Channel:           channel,
			WindowStart:       time.UnixMilli(winStartMs).UTC(),
			WindowEnd:         time.UnixMilli(winEndMs).UTC(),
			Observed:          observed,
			Expected:          expected,
			TailProbability:   tailProb,
			ConfirmationCount: confCount,
			FirstSeenAt:       time.UnixMilli(firstMs).UTC(),
		})
	}
	return out, rows.Err()
}
