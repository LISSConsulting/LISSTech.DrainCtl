//go:build windows

package dashboard

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/etwids"
)

// forceUpdateMinAgentVersion is the first release that shipped the
// pending-command report protocol. Agents below this floor are reported as
// unsupported so the UI never claims an update was queued to an agent that
// cannot consume it.
//
// Format: CalVer YY.MM.N (matches the agent report version). The comparison is
// numeric by component, not lexicographic.
const forceUpdateMinAgentVersion = "26.9.17"

// forceUpdateResponse is the per-host JSON body returned from
// POST /api/v1/servers/{host}/force-update.
type forceUpdateResponse struct {
	Host       string             `json:"host"`
	CommandID  string             `json:"command_id"`
	Outcome    forceUpdateOutcome `json:"outcome"`
	AcceptedAt string             `json:"accepted_at,omitempty"`
	Version    string             `json:"version,omitempty"`
	Reason     string             `json:"reason,omitempty"`
	Error      string             `json:"error,omitempty"`
}

type forceUpdateRequest struct {
	CommandID string `json:"command_id"`
	Reason    string `json:"reason,omitempty"`
}

// handleForceUpdate serves POST /api/v1/servers/{host}/force-update.
// Authenticates the operator, validates the host is registered, and
// enqueues a pending command that will be attached to the agent's next
// /api/v1/report response.
//
// Auth: the same isAuthorizedForHost gate that DELETE /servers/{host}
// uses. Authenticated operators may force-update any host in the
// configured group; unauthenticated requests get 401; identity-host
// mismatches get 403.
//
// Idempotency: client-supplied command_id is required and must match
// the documented regex. Replays within the idempotency window return
// outcome=duplicate without re-enqueueing.
//
// Offline / unsupported: outcome in the body, status 200. The caller
// switches on outcome for the user-facing state. We intentionally do
// NOT use 4xx for offline/unsupported so the dashboard's batch toolbar
// can iterate the response uniformly — every 200 has an outcome to
// switch on, no need to inspect the status code first.
func (ds *DashboardServer) handleForceUpdate(w http.ResponseWriter, r *http.Request) {
	rawHost := strings.TrimSpace(r.PathValue("host"))
	if rawHost == "" {
		http.Error(w, "host parameter required", http.StatusBadRequest)
		return
	}
	if !hostnameRE.MatchString(rawHost) {
		http.Error(w, "invalid hostname", http.StatusBadRequest)
		return
	}
	host := strings.ToLower(rawHost)

	// Auth + authz mirror DELETE /servers/{host}. Same gate, same
	// reasoning: force-update is a "modify" operation on the host's
	// management state.
	auth := GetAuthInfo(r)
	if auth == nil {
		writeJSONError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !isAuthorizedForHost(auth, rawHost, ds.cfg.Group) {
		slog.Warn("dashboard: force-update rejected: identity mismatch",
			slog.Int("event_id", etwids.EvtAccessDenied), "user", auth.Username, "host", rawHost)
		writeJSONError(w, "identity does not match host", http.StatusForbidden)
		return
	}
	if localHost, err := os.Hostname(); err == nil && strings.EqualFold(host, localHost) && !ds.localForceUpdateSupported.Load() {
		ds.writeForceUpdateResponse(w, host, "", "", "", forceUpdateOutcomeUnsupported, time.Time{}, nil)
		return
	}

	if !ds.state.IsRegistered(rawHost) {
		writeJSONError(w, "host not registered", http.StatusNotFound)
		return
	}

	// command_id is required: callers retain it across retries so delivery is
	// idempotent. A missing body is therefore invalid rather than a request to
	// mint a non-retryable identifier.
	if r.ContentLength <= 0 {
		writeJSONError(w, "request body required", http.StatusBadRequest)
		return
	}
	var req forceUpdateRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		writeJSONError(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Reason must fit under the documented cap. Empty is allowed.
	if len(req.Reason) > forceUpdateMaxReasonLen {
		writeJSONError(w, fmt.Sprintf("reason exceeds %d chars", forceUpdateMaxReasonLen), http.StatusUnprocessableEntity)
		return
	}

	if req.CommandID == "" {
		writeJSONError(w, "command_id required", http.StatusUnprocessableEntity)
		return
	}
	if !forceUpdateCommandIDRE.MatchString(req.CommandID) {
		writeJSONError(w, "command_id must match "+forceUpdateCommandIDRE.String(), http.StatusUnprocessableEntity)
		return
	}

	// Read the agent's most recent report so we can decide offline vs
	// unsupported vs accepted. GetCached avoids the DB hit on the hot
	// path; Get is the fallback when the cache is cold (e.g. first
	// request after dashboard restart). Either way, we tolerate an
	// empty result — that just means the host has never reported, which
	// is reported as offline below.
	info := ds.state.GetCached(rawHost)
	if info == nil {
		info = ds.state.Get(rawHost)
	}
	var agentVersion string
	if info != nil && info.LastResult != nil {
		agentVersion = info.LastResult.Version
	}

	// Offline check first: a stale host should never be told
	// "accepted" because the command would sit in the pending queue
	// until the agent's next heartbeat (which may never come). The
	// dashboard operator's UX is "host is offline — try again later",
	// not a quietly-pending queue.
	if info == nil || info.LastSeen.IsZero() || time.Since(info.LastSeen) >= ds.staleAfter() {
		ds.writeForceUpdateResponse(w, host, req.CommandID, req.Reason, agentVersion, forceUpdateOutcomeOffline, time.Time{}, nil)
		return
	}

	// A missing or malformed version is also unsupported. Treating it as
	// current would acknowledge a command to an agent that may silently ignore
	// the pending_command field during a rolling upgrade.
	if agentVersion == "" || agentVersionLessThan(agentVersion, forceUpdateMinAgentVersion) {
		ds.writeForceUpdateResponse(w, host, req.CommandID, req.Reason, agentVersion, forceUpdateOutcomeUnsupported, time.Time{}, nil)
		return
	}

	// Enqueue. The registry handles dedup (within the idempotency
	// window) and expiry (entries older than 24h are GC'd lazily).
	outcome, cmd := ds.forceUpdates.Enqueue(host, req.CommandID, req.Reason, agentVersion)
	var acceptedAt time.Time
	if cmd != nil {
		acceptedAt = cmd.AcceptedAt
	}
	ds.writeForceUpdateResponse(w, host, req.CommandID, req.Reason, agentVersion, outcome, acceptedAt, cmd)

	// Server-side log: keep slog noise low for the happy path but
	// capture enough context to reconstruct what happened from a
	// dashboard session replay. slog.Info for accepted/duplicate;
	// duplicate is normal-replay traffic so it stays at Info.
	switch outcome {
	case forceUpdateOutcomeAccepted:
		slog.Info("dashboard=force_update_accepted",
			"host", host,
			"command_id", req.CommandID,
			"version", agentVersion,
			slog.Int("event_id", etwids.EvtForceUpdateAccepted))
	case forceUpdateOutcomeDuplicate:
		slog.Info("dashboard=force_update_duplicate",
			"host", host,
			"command_id", req.CommandID)
	default:
		slog.Warn("dashboard=force_update_unexpected_outcome",
			"host", host, "command_id", req.CommandID, "outcome", string(outcome))
	}
}

// writeForceUpdateResponse serialises the per-host response.
func (ds *DashboardServer) writeForceUpdateResponse(w http.ResponseWriter, host, commandID, reason, agentVersion string, outcome forceUpdateOutcome, acceptedAt time.Time, _ *forceUpdateCommand) {
	resp := forceUpdateResponse{
		Host: host, CommandID: commandID,
		Outcome: outcome, Reason: reason,
		Version: agentVersion,
	}
	if !acceptedAt.IsZero() {
		resp.AcceptedAt = acceptedAt.UTC().Format(time.RFC3339Nano)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// agentVersionLessThan compares two CalVer YY.MM.N strings. Returns
// true when a < b. Empty or unparseable strings sort below everything
// parseable (callers already gate on emptiness via the offline check).
//
// Intentionally tolerant of a leading 'v' so version strings stored
// with or without the prefix compare identically.
func agentVersionLessThan(a, b string) bool {
	ap, ok := parseCalVer(a)
	if !ok {
		return true
	}
	bp, ok := parseCalVer(b)
	if !ok {
		return false
	}
	if ap[0] != bp[0] {
		return ap[0] < bp[0]
	}
	if ap[1] != bp[1] {
		return ap[1] < bp[1]
	}
	return ap[2] < bp[2]
}

// parseCalVer parses "YY.MM.N" or "vYY.MM.N" into [year, month, n].
// Returns ok=false for malformed input; the caller falls back to a
// safe default (offline vs unsupported) per its own logic.
func parseCalVer(s string) ([3]int, bool) {
	var out [3]int
	s = strings.TrimPrefix(s, "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	if out[1] > 12 {
		return out, false
	}
	return out, true
}

// consumePendingCommand returns the next pending force-update command
// for host, if any. Called by the report handler to attach the command
// to the agent's next heartbeat response. Wraps forceUpdateState.Consume
// so callers don't need to know about the registry type.
func (ds *DashboardServer) consumePendingCommand(host string) *forceUpdateCommand {
	return ds.forceUpdates.Consume(host)
}

// broadcastForceUpdateCompletion publishes the terminal SSE event for
// a host. Called by the report handler when an agent posts a
// ForceUpdateReport field describing its updater's decision.
//
// The data payload mirrors the contract documented in
// openapi.yaml components.ForceUpdateEvent: command_id, outcome,
// reason (the updater's decision string), and the version transition
// when applicable. Outcomes are: completed, failed, duplicate,
// refused. The frontend's toast lifecycle keys off command_id so the
// caller can age out per-host toasts at 2 * poll_interval with no
// completion event.
func (ds *DashboardServer) broadcastForceUpdateCompletion(host string, payload ForceUpdateCompletionPayload) {
	if payload.CommandID == "" {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("dashboard: force-update event marshal failed", "host", host, "error", err)
		return
	}
	wrapped, err := json.Marshal(SSEEvent{
		Type:      "force_update",
		Host:      host,
		Data:      raw,
		Timestamp: time.Now(),
	})
	if err != nil {
		slog.Warn("dashboard: force-update event wrap failed", "host", host, "error", err)
		return
	}
	ds.broker.Broadcast(wrapped)
}

// ForceUpdateCompletionPayload is the data field of the force_update
// SSE event. Mirrors openapi.yaml components.ForceUpdateEventData.
// Host is the agent that reported the completion; it is captured at
// report time so the OnLocalForceUpdateCompletion callback (which has
// no explicit host argument) can route the SSE event correctly without
// re-deriving the host from a separate channel.
//
// Exported so the service loop (which lives in a different package)
// can construct one for the ReportLocal path without re-declaring the
// shape.
type ForceUpdateCompletionPayload struct {
	Host       string `json:"host,omitempty"`
	CommandID  string `json:"command_id"`
	Outcome    string `json:"outcome"` // completed | failed | duplicate | refused
	Reason     string `json:"reason,omitempty"`
	OldVersion string `json:"old_version,omitempty"`
	NewVersion string `json:"new_version,omitempty"`
	DecidedAt  string `json:"decided_at,omitempty"`
}
