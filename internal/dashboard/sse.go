//go:build windows

package dashboard

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// sseKeepaliveInterval is the period between SSE keepalive comments.
// Exported as a package-level var so tests can shorten it without rebuilding.
var sseKeepaliveInterval = 25 * time.Second

// sseSessionCheckInterval is how often handleSSE revalidates the session cookie.
// When a session is deleted (logout from another tab, admin revocation) the SSE
// stream is terminated within this window so the browser reconnects and receives
// a 401 instead of continuing to receive events for a dead session.
// Exported as a package-level var so tests can shorten it without rebuilding.
var sseSessionCheckInterval = 5 * time.Minute

// mustMarshal marshals v to JSON, returning nil on error (caller checks SSEEvent marshal).
func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// broadcastServerUpdate builds a ServerView for the named host and broadcasts
// it as a server_update SSE event. Reads the cached *ServerInfo populated by
// the most recent ServerState.Update; falls back to a DB read only when the
// cache is cold (e.g. broadcast triggered by Register before any heartbeat,
// or process restart). The cache hit path skips the per-heartbeat DB read +
// JSON unmarshal that previously fired for every registered host.
func (ds *DashboardServer) broadcastServerUpdate(host string) {
	info := ds.state.GetCached(host)
	if info == nil {
		info = ds.state.Get(host)
	}
	if info == nil {
		return
	}
	view := toServerView(*info, ds.staleAfter())
	payload, err := json.Marshal(SSEEvent{
		Type:      "server_update",
		Host:      host,
		Data:      mustMarshal(view),
		Timestamp: time.Now(),
	})
	if err != nil {
		slog.Warn("sse: broadcastServerUpdate: marshal failed", "host", host, "error", err)
		return
	}
	ds.broker.Broadcast(payload)
}

// BroadcastServerUpdate is the exported variant for use by the service handler
// when reporting local check results.
func (ds *DashboardServer) BroadcastServerUpdate(host string) {
	ds.broadcastServerUpdate(host)
}

// broadcastServerDeleted broadcasts a server_deleted SSE event so connected
// browsers remove the host from their list immediately instead of waiting for
// the next 30-second poll cycle. changedBy is the dashboard user who performed
// the deletion; it is embedded in the event data so the event log can show
// attribution without a separate API call.
func (ds *DashboardServer) broadcastServerDeleted(host, changedBy string) {
	type deletedData struct {
		ChangedBy string `json:"changed_by,omitempty"`
	}
	payload, err := json.Marshal(SSEEvent{
		Type:      "server_deleted",
		Host:      host,
		Data:      mustMarshal(deletedData{ChangedBy: changedBy}),
		Timestamp: time.Now(),
	})
	if err != nil {
		slog.Warn("sse: broadcastServerDeleted: marshal failed", "host", host, "error", err) //nolint:gosec // host is validated by the router pattern
		return
	}
	ds.broker.Broadcast(payload)
}

// broadcastServerPermanentlyRemoved emits the durable
// server_permanently_removed SSE event after its tombstone has been written.
// Callers also emit the legacy server_deleted event first, preserving existing
// clients while updated clients consume the durable removal metadata.
func (ds *DashboardServer) broadcastServerPermanentlyRemoved(host, removedBy, reason string, removedAt time.Time) {
	type removedData struct {
		RemovedBy string    `json:"removed_by,omitempty"`
		Reason    string    `json:"reason,omitempty"`
		RemovedAt time.Time `json:"removed_at"`
		Permanent bool      `json:"permanent"`
	}
	payload, err := json.Marshal(SSEEvent{
		Type:      "server_permanently_removed",
		Host:      host,
		Data:      mustMarshal(removedData{RemovedBy: removedBy, Reason: reason, RemovedAt: removedAt, Permanent: true}),
		Timestamp: time.Now(),
	})
	if err != nil {
		slog.Warn("sse: broadcastServerPermanentlyRemoved: marshal failed", "host", host, "error", err) //nolint:gosec
		return
	}
	ds.broker.Broadcast(payload)
}

// broadcastServerRestored broadcasts a server_restored SSE event after the
// tombstone for the host has been deleted. The live roster does NOT
// automatically re-add the host — the agent's next /api/v1/register call
// creates a fresh row. The dashboard's "removed servers" panel removes the
// entry. UI state holding a selected row that was just restored can
// reconcile by listening for this event and re-issuing /api/v1/servers.
func (ds *DashboardServer) broadcastServerRestored(host, restoredBy string, restoredAt time.Time) {
	type restoredData struct {
		RestoredBy string    `json:"restored_by,omitempty"`
		RestoredAt time.Time `json:"restored_at"`
	}
	payload, err := json.Marshal(SSEEvent{
		Type:      "server_restored",
		Host:      host,
		Data:      mustMarshal(restoredData{RestoredBy: restoredBy, RestoredAt: restoredAt}),
		Timestamp: time.Now(),
	})
	if err != nil {
		slog.Warn("sse: broadcastServerRestored: marshal failed", "host", host, "error", err) //nolint:gosec
		return
	}
	ds.broker.Broadcast(payload)
}

// broadcastSettingsUpdate broadcasts the current settings to all connected browsers.
func (ds *DashboardServer) broadcastSettingsUpdate() {
	var cfg *dc.Config
	var err error
	if ds.testLoadConfigFunc != nil {
		cfg, err = ds.testLoadConfigFunc()
	} else {
		cfg, err = dc.LoadConfig()
	}
	if err != nil {
		slog.Warn("sse: broadcastSettingsUpdate: failed to load config", "error", err)
		return
	}
	// Strip secrets before broadcasting — webhook HMAC keys and SMTP
	// passwords must never be sent over the event stream.
	redacted := make([]dc.NotificationTarget, len(cfg.Notifications))
	copy(redacted, cfg.Notifications)
	for i := range redacted {
		redacted[i].Secret = ""
	}
	// Narrow evtspike to the enabled flag only — SSE must not leak admin-only
	// fields (thresholds, channel lists, baseline_path, security gate).
	type evtspikeView struct {
		Enabled bool `json:"enabled"`
	}
	resp := struct {
		Notifications           []dc.NotificationTarget `json:"notifications"`
		SessionWarningThreshold int                     `json:"session_warning_threshold"`
		GracePeriod             int                     `json:"grace_period"`
		Performance             dc.PerformanceConfig    `json:"performance"`
		EvtSpike                evtspikeView            `json:"evtspike"`
		Update                  dc.UpdateConfig         `json:"update"`
	}{
		Notifications:           redacted,
		SessionWarningThreshold: cfg.SessionWarningThreshold,
		GracePeriod:             cfg.GracePeriod,
		Performance:             cfg.Performance,
		EvtSpike:                evtspikeView{Enabled: cfg.EvtSpike.Enabled},
		Update:                  cfg.Update,
	}
	if resp.Notifications == nil {
		resp.Notifications = []dc.NotificationTarget{}
	}
	payload, err := json.Marshal(SSEEvent{
		Type:      "settings_update",
		Data:      mustMarshal(resp),
		Timestamp: time.Now(),
	})
	if err != nil {
		slog.Warn("sse: broadcastSettingsUpdate: marshal failed", "error", err)
		return
	}
	ds.broker.Broadcast(payload)
}

// handleSSE serves the Server-Sent Events stream for real-time dashboard updates.
func (ds *DashboardServer) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Disable the server's WriteTimeout for this long-lived connection.
	// Without this, the 15-second WriteTimeout kills the SSE stream,
	// causing rapid reconnect cycles that exhaust browser connections.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	// Capture the session token for periodic revalidation below.
	// requireSession has already validated it; we keep it so we can
	// close the stream promptly when the session is deleted or expires
	// (e.g. logout from another tab, admin session revocation).
	// Empty string when no cookie is present (tests, dev mode).
	var sessionToken string
	if cookie, err := r.Cookie("drainctl_session"); err == nil {
		sessionToken = cookie.Value
	}

	id, ch, done, err := ds.broker.Subscribe()
	if err != nil {
		http.Error(w, "too many connections", http.StatusTooManyRequests)
		return
	}
	defer ds.broker.Unsubscribe(id)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering
	flusher.Flush()

	slog.Info("sse: client connected", "id", id)
	defer slog.Info("sse: client disconnected", "id", id)

	// Keepalive: emit an SSE comment every sseKeepaliveInterval so intermediate
	// proxies and firewalls (which typically have a 30–60 s idle-connection
	// timeout) do not silently drop the stream. An SSE comment (": …\n\n") is
	// invisible to the browser's EventSource API but resets TCP idle timers.
	keepalive := time.NewTicker(sseKeepaliveInterval)
	defer keepalive.Stop()

	// Session revalidation is disabled entirely without a token (tests and
	// development mode) so those streams do not retain an unused runtime timer.
	var sessionCheck *time.Ticker
	var sessionCheckC <-chan time.Time
	if sessionToken != "" {
		sessionCheck = time.NewTicker(sseSessionCheckInterval)
		sessionCheckC = sessionCheck.C
		defer sessionCheck.Stop()
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-done:
			return
		case <-sessionCheckC:
			if sessionToken != "" && ds.sessionStore.Get(sessionToken) == nil {
				slog.Info("sse: closing stream — session expired or deleted", "id", id)
				return
			}
		case <-keepalive.C:
			_, err := fmt.Fprintf(w, ": keepalive\n\n")
			if err != nil {
				return
			}
			flusher.Flush()
		case msg := <-ch:
			_, err := fmt.Fprintf(w, "data: %s\n\n", msg)
			if err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
