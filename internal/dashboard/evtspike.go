//go:build windows

package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/etwids"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// spikeQueryTimeout bounds individual /api/evtspike/spikes calls.
const spikeQueryTimeout = 5 * time.Second

const (
	// spikeDefaultLimit is the default ?limit= when neither from/to nor an
	// explicit limit is supplied. Preserves the pre-009 contract.
	spikeDefaultLimit = 20

	// spikeListMaxLimit caps ?limit= on the recent-list mode (no from/to).
	spikeListMaxLimit = 50

	// spikeRangeMaxLimit caps the row count when ?from / ?to are supplied.
	// Big enough to cover a 5-day window even under an active detector;
	// small enough that a pathological query can't exhaust the reader pool.
	spikeRangeMaxLimit = 500
)

// EvtSpikeStatusFunc returns the detector status for a registered host. The
// subsystem installs this hook at Start; when the feature is off (or the
// subsystem has not wired itself yet) the field on DashboardServer is nil and
// the handler presents a synthetic state="disabled" response per
// contracts/dashboard-sse-events.md.
type EvtSpikeStatusFunc func(host string) evtspike.DetectorStatus

// handleEvtSpikeStatus serves GET /api/evtspike/status?host=<hostname>.
// Returns 200 with a DetectorStatus body for registered hosts, 404 otherwise.
// A disabled feature returns 200 with state="disabled" — not 503 — because the
// disabled state is a permanent configuration choice, not a transient failure.
func (ds *DashboardServer) handleEvtSpikeStatus(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Query().Get("host")
	if host == "" || !ds.state.IsRegistered(host) {
		writeJSONError(w, "unknown_host", http.StatusNotFound)
		return
	}

	var status evtspike.DetectorStatus
	if ds.evtspikeStatus != nil {
		status = ds.evtspikeStatus(host)
	}
	status.Host = host
	if status.State == "" {
		status.State = evtspike.StateDisabled
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(status)
}

// handleEvtSpikeSpikes serves GET /api/evtspike/spikes?host=<hostname>&limit=<1..50>
// or GET /api/evtspike/spikes?host=<hostname>&from=<iso>&to=<iso>.
//
// Mode A (recent list, pre-009 contract): no from/to → returns newest-first
// entries capped by limit (default 20, clamped [1,50]).
//
// Mode B (range query, 009+): both from and to supplied → returns newest-first
// entries whose window_start falls in [from, to), capped at spikeRangeMaxLimit.
// Backs the SpikeSwimlane chart.
//
// Returns 200 with a (possibly empty) JSON array of RecentSpikeEntry. 404 for
// unknown hosts. 400 if from/to are malformed or one is supplied without the
// other. Empty result for a registered host with no spikes is 200 [], not 404.
func (ds *DashboardServer) handleEvtSpikeSpikes(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Query().Get("host")
	if host == "" || !ds.state.IsRegistered(host) {
		writeJSONError(w, "unknown_host", http.StatusNotFound)
		return
	}

	fromRaw := r.URL.Query().Get("from")
	toRaw := r.URL.Query().Get("to")

	entries := []evtspike.RecentSpikeEntry{}

	if fromRaw != "" || toRaw != "" {
		// Mode B: range query.
		if fromRaw == "" || toRaw == "" {
			writeJSONError(w, "from_and_to_both_required", http.StatusBadRequest)
			return
		}
		from, err := time.Parse(time.RFC3339Nano, fromRaw)
		if err != nil {
			writeJSONError(w, "invalid_from", http.StatusBadRequest)
			return
		}
		to, err := time.Parse(time.RFC3339Nano, toRaw)
		if err != nil {
			writeJSONError(w, "invalid_to", http.StatusBadRequest)
			return
		}
		if !to.After(from) {
			writeJSONError(w, "to_must_be_after_from", http.StatusBadRequest)
			return
		}

		if ds.spikes != nil {
			ctx, cancel := context.WithTimeout(r.Context(), spikeQueryTimeout)
			defer cancel()
			rows, err := ds.spikes.Range(ctx, host, from, to, spikeRangeMaxLimit)
			if err != nil {
				slog.Warn("dashboard: event_spikes range failed", "host", host, "error", err) //nolint:gosec // host is validated against registered server list
				writeJSONError(w, "storage_error", http.StatusInternalServerError)
				return
			}
			entries = toRecentSpikeEntries(rows)
		}
	} else {
		// Mode A: recent list.
		limit := spikeDefaultLimit
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil {
				if n < 1 {
					n = 1
				}
				if n > spikeListMaxLimit {
					n = spikeListMaxLimit
				}
				limit = n
			}
		}
		if ds.spikes != nil {
			ctx, cancel := context.WithTimeout(r.Context(), spikeQueryTimeout)
			defer cancel()
			rows, err := ds.spikes.Recent(ctx, host, limit)
			if err != nil {
				slog.Warn("dashboard: event_spikes recent failed", "host", host, "error", err) //nolint:gosec // host is validated against registered server list
				writeJSONError(w, "storage_error", http.StatusInternalServerError)
				return
			}
			entries = toRecentSpikeEntries(rows)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(entries)
}

// handleReportSpike serves POST /api/v1/spike. Accepts a JSON SpikePayload
// pushed by a remote agent when its evtspike subsystem confirms a spike and
// the agent isn't the local-dashboard host (the local path appends via
// OnEvtSpikeIngest already). The body's Host must match the authenticated
// machine account. Duplicate (Channel, WindowStart) posts are silently
// ignored so a retrying agent can't double-insert.
func (ds *DashboardServer) handleReportSpike(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 16384))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var spike dc.SpikePayload
	if err := json.Unmarshal(body, &spike); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if spike.Host == "" {
		http.Error(w, "host field required", http.StatusBadRequest)
		return
	}
	if !ds.state.IsRegistered(spike.Host) {
		http.Error(w, "host not registered", http.StatusForbidden)
		return
	}
	auth := GetAuthInfo(r)
	if auth != nil && !isAuthorizedForHost(auth, spike.Host, ds.cfg.Group) {
		slog.Warn("dashboard: spike rejected: identity mismatch",
			slog.Int("event_id", etwids.EvtAccessDenied), "user", auth.Username, "claimed_host", spike.Host)
		http.Error(w, "identity does not match claimed hostname", http.StatusForbidden)
		return
	}

	if ds.spikes == nil {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
		return
	}
	entry, inserted := ds.insertSpike(spike)
	if inserted {
		ds.broker.PublishRecentSpike(entry)
		slog.Info("dashboard=spike_ingested",
			"host", spike.Host, "channel", spike.Channel,
			"observed", spike.Observed, "expected", spike.Expected)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// toRecentSpikeEntries adapts telemetry.EventSpike rows into the wire type
// returned by /api/evtspike/spikes and the `recent_spike` SSE event. The JSON
// field names on RecentSpikeEntry (via embedded SpikePayload) match the
// pre-009 contract verbatim.
func toRecentSpikeEntries(rows []telemetry.EventSpike) []evtspike.RecentSpikeEntry {
	out := make([]evtspike.RecentSpikeEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, evtspike.RecentSpikeEntry{
			ID: row.ID,
			SpikePayload: dc.SpikePayload{
				Host:              row.Host,
				Channel:           row.Channel,
				WindowStart:       row.WindowStart,
				WindowEnd:         row.WindowEnd,
				Observed:          row.Observed,
				Expected:          row.Expected,
				TailProbability:   row.TailProbability,
				ConfirmationCount: row.ConfirmationCount,
				FirstSeenAt:       row.FirstSeenAt,
			},
		})
	}
	return out
}
