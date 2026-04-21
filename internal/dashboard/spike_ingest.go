//go:build windows

package dashboard

import (
	"context"
	"log/slog"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// spikeInsertTimeout bounds an individual INSERT against event_spikes so a
// contended writer cannot wedge the evtspike OnSpike callback.
const spikeInsertTimeout = 5 * time.Second

// insertSpike persists one confirmed spike into event_spikes and maps the
// result back onto the wire type. Returns (entry, inserted). inserted=false
// means the (host, channel, window_start) identity already existed — the
// entry still carries a valid ID so any caller that needs a stable key for
// SSE dedup has one, but the caller should NOT broadcast.
//
// On storage error, returns an entry synthesized from the input (ID=0) and
// inserted=false so the OnEvtSpikeIngest callback signature stays honest
// without dropping the whole event.
func (ds *DashboardServer) insertSpike(spike evtspike.SpikePayload) (evtspike.RecentSpikeEntry, bool) {
	if ds.spikes == nil {
		return evtspike.RecentSpikeEntry{SpikePayload: spike}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), spikeInsertTimeout)
	defer cancel()

	stored, inserted, err := ds.spikes.Insert(ctx, telemetry.EventSpike{
		Host:              spike.Host,
		Channel:           spike.Channel,
		WindowStart:       spike.WindowStart,
		WindowEnd:         spike.WindowEnd,
		Observed:          spike.Observed,
		Expected:          spike.Expected,
		TailProbability:   spike.TailProbability,
		ConfirmationCount: spike.ConfirmationCount,
		FirstSeenAt:       spike.FirstSeenAt,
	})
	if err != nil {
		slog.Warn("dashboard: event_spikes insert failed",
			"host", spike.Host, "channel", spike.Channel, "error", err)
		return evtspike.RecentSpikeEntry{SpikePayload: spike}, false
	}
	return evtspike.RecentSpikeEntry{
		ID:           stored.ID,
		SpikePayload: spike,
	}, inserted
}
