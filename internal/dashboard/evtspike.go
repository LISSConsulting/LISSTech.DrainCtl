//go:build windows

package dashboard

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
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

// handleEvtSpikeSpikes serves GET /api/evtspike/spikes?host=<hostname>&limit=<1..50>.
// Returns 200 with a (possibly empty) JSON array of RecentSpikeEntry, newest
// first, for a registered host. 404 for unknown hosts. The limit param defaults
// to 20 and is clamped to [1, 50] per contracts/dashboard-sse-events.md. Empty
// result for a registered host with no spikes is 200 [], not 404.
func (ds *DashboardServer) handleEvtSpikeSpikes(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Query().Get("host")
	if host == "" || !ds.state.IsRegistered(host) {
		writeJSONError(w, "unknown_host", http.StatusNotFound)
		return
	}

	limit := spikeStoreDefaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			if n < 1 {
				n = 1
			}
			if n > spikeStoreMaxLimit {
				n = spikeStoreMaxLimit
			}
			limit = n
		}
	}

	entries := []evtspike.RecentSpikeEntry{}
	if ds.spikestore != nil {
		entries = ds.spikestore.Recent(host, limit)
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(entries)
}
