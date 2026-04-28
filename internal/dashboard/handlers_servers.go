//go:build windows

package dashboard

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/etwids"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
)

// hostnameRE matches RFC 1123 hostnames: labels of alphanumerics and hyphens
// (hyphen not at start/end), separated by dots.
var hostnameRE = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)*$`)

// ServerView is the flattened JSON shape returned to the Svelte dashboard
// frontend from GET /api/v1/servers and GET /api/v1/servers/{host}.
// It unwraps ServerInfo + CheckResult so the frontend never navigates nested
// last_result fields. Status is normalised to lowercase frontend tokens.
type ServerView struct {
	Host                 string           `json:"host"`
	Status               string           `json:"status"` // "ok"/"grace"/"alert"/"off"
	DrainMode            string           `json:"drain_mode"`
	Sessions             int              `json:"sessions"`              // TotalSessions; 0 when unknown
	SessionsActive       int              `json:"sessions_active"`       // connected sessions; 0 when unknown
	SessionsDisconnected int              `json:"sessions_disconnected"` // disconnected sessions; 0 when unknown
	MaxSessions          int              `json:"max_sessions"`          // server capacity; 0 when unknown
	StateDurationSeconds *float64         `json:"state_duration_seconds"`
	StateChangedAt       *time.Time       `json:"state_changed_at,omitempty"` // when the current state began
	Version              string           `json:"version"`
	RegisteredAt         time.Time        `json:"registered_at"`
	LastSeen             time.Time        `json:"last_seen,omitempty"`
	ChangedBy            string           `json:"changed_by,omitempty"`
	GraceDeadline        *time.Time       `json:"grace_deadline"`
	Perf                 *dc.PerfSnapshot `json:"perf"`
}

// statusToken converts a CheckResult.Status value ("Healthy"/"Warning"/"Grace"/"Alert")
// to the lowercase frontend token ("ok"/"warning"/"grace"/"alert"/"off").
func statusToken(status string) string {
	switch status {
	case "Healthy":
		return "ok"
	case "Warning":
		return "warning"
	case "Grace":
		return "grace"
	case "Alert":
		return "alert"
	default:
		return "off"
	}
}

// toServerView converts a stored ServerInfo into the flattened ServerView
// the dashboard frontend expects.
func toServerView(info ServerInfo) ServerView {
	v := ServerView{
		Host:         info.Hostname,
		Status:       "off",
		RegisteredAt: info.RegisteredAt,
		LastSeen:     info.LastSeen,
	}
	r := info.LastResult
	if r == nil {
		return v
	}
	if r.Host != "" {
		v.Host = r.Host
	}
	if !info.LastSeen.IsZero() && time.Since(info.LastSeen) > staleThreshold {
		v.Status = "off"
	} else {
		v.Status = statusToken(r.Status)
	}
	v.DrainMode = r.DrainModeLabel
	v.Version = r.Version
	v.ChangedBy = r.ChangedBy
	v.Perf = r.Performance
	v.StateDurationSeconds = r.StateDurationSeconds
	v.StateChangedAt = r.StateSince
	if r.Sessions != nil {
		v.Sessions = r.Sessions.TotalSessions
		v.SessionsActive = r.Sessions.ActiveSessions
		v.SessionsDisconnected = r.Sessions.DisconnectedSessions
		v.MaxSessions = r.Sessions.MaxSessions
	}
	// GraceDeadline: the moment when the grace window expires.
	// Computed from StateSince + GracePeriodSeconds so the frontend can display
	// a live countdown without knowing the raw grace period configuration.
	if r.Status == "Grace" && r.StateSince != nil && r.GracePeriodSeconds > 0 {
		deadline := r.StateSince.Add(time.Duration(r.GracePeriodSeconds) * time.Second)
		v.GraceDeadline = &deadline
	}
	return v
}

// isAuthorizedForHost checks whether the authenticated identity is allowed to
// register or report for the given hostname.
//   - Machine accounts ("DOMAIN\HOST$") can only act for their own hostname.
//   - Members of the dashboard admin group can act for any host.
//   - All other identities are rejected.
func isAuthorizedForHost(auth *AuthInfo, hostname, adminGroup string) bool {
	if auth == nil {
		return false
	}
	// Machine account: "DOMAIN\HOST$" → extract hostname, compare.
	user := auth.Username
	if strings.HasSuffix(user, "$") {
		machineHost := user[:len(user)-1] // strip trailing $
		if idx := strings.LastIndex(machineHost, `\`); idx >= 0 {
			machineHost = machineHost[idx+1:]
		}
		return strings.EqualFold(machineHost, hostname)
	}
	// Non-machine account: must be a member of the admin group.
	for _, g := range auth.Groups {
		if strings.EqualFold(g, adminGroup) {
			return true
		}
		parts := strings.SplitN(g, `\`, 2)
		if len(parts) == 2 && strings.EqualFold(parts[1], adminGroup) {
			return true
		}
	}
	return false
}

// handleRegister processes POST /api/v1/register.
// Expects JSON: {"hostname":"..."}
func (ds *DashboardServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hostname string `json:"hostname"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Hostname == "" {
		http.Error(w, "hostname required", http.StatusBadRequest)
		return
	}
	req.Hostname = strings.TrimSpace(req.Hostname)
	if req.Hostname == "" || len(req.Hostname) > 253 || !hostnameRE.MatchString(req.Hostname) {
		http.Error(w, "hostname invalid", http.StatusBadRequest)
		return
	}

	// Verify the authenticated identity matches the claimed hostname.
	// Machine accounts are "DOMAIN\HOST$" — the hostname must match.
	// Domain admins (non-machine accounts) can register any host.
	auth := GetAuthInfo(r)
	user := ""
	if auth != nil {
		user = auth.Username
		if !isAuthorizedForHost(auth, req.Hostname, ds.cfg.Group) && !isLocalSystemForHost(r, auth, req.Hostname) {
			slog.Warn("dashboard: register rejected: identity mismatch",
				slog.Int("event_id", etwids.EvtAccessDenied), "user", user, "claimed_host", req.Hostname)
			http.Error(w, "identity does not match claimed hostname", http.StatusForbidden)
			return
		}
	}

	ds.state.Register(req.Hostname)
	slog.Info("dashboard=register", slog.Int("event_id", etwids.EvtServerRegistered), "host", req.Hostname, "user", user)
	ds.broadcastServerUpdate(req.Hostname)

	resp := struct {
		OK             bool   `json:"ok"`
		TLSFingerprint string `json:"tls_fingerprint,omitempty"`
	}{OK: true, TLSFingerprint: ds.fingerprint}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// handleReport processes POST /api/v1/report.
// Expects JSON CheckResult. Rejects unregistered hosts with 403.
func (ds *DashboardServer) handleReport(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 65536))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	var result dc.CheckResult
	if err := json.Unmarshal(body, &result); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if result.Host == "" {
		http.Error(w, "host field required", http.StatusBadRequest)
		return
	}

	if !ds.state.IsRegistered(result.Host) {
		http.Error(w, "host not registered", http.StatusForbidden)
		return
	}

	auth := GetAuthInfo(r)
	if auth != nil && !isAuthorizedForHost(auth, result.Host, ds.cfg.Group) && !isLocalSystemForHost(r, auth, result.Host) {
		slog.Warn("dashboard: report rejected: identity mismatch",
			slog.Int("event_id", etwids.EvtAccessDenied), "user", auth.Username, "claimed_host", result.Host)
		http.Error(w, "identity does not match claimed hostname", http.StatusForbidden)
		return
	}

	ds.state.Update(result.Host, &result)

	// Propagate the remote agent's evtspike detector status. The broker dedups
	// by (host, state) so calling unconditionally every heartbeat is safe and
	// keeps the wire quiet on steady state. The cache backs the pull function
	// registered for non-local hosts in internal/svc/handler.go.
	if result.EvtSpikeStatus != nil {
		status := *result.EvtSpikeStatus
		status.Host = result.Host
		ds.remoteEvtSpikeStatusMu.Lock()
		ds.remoteEvtSpikeStatus[result.Host] = status
		ds.remoteEvtSpikeStatusMu.Unlock()
		ds.broker.PublishDetectorStatus(status)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// RemoteEvtSpikeStatus returns the DetectorStatus most recently reported for
// the given host via /api/v1/report. Zero value (State="") for unknown hosts;
// callers translate that into the disabled state. Safe for concurrent use.
func (ds *DashboardServer) RemoteEvtSpikeStatus(host string) evtspike.DetectorStatus {
	ds.remoteEvtSpikeStatusMu.RLock()
	defer ds.remoteEvtSpikeStatusMu.RUnlock()
	return ds.remoteEvtSpikeStatus[host]
}

// handleServers returns GET /api/v1/servers as a JSON array of ServerView.
func (ds *DashboardServer) handleServers(w http.ResponseWriter, r *http.Request) {
	infos := ds.state.All()
	views := make([]ServerView, len(infos))
	for i, info := range infos {
		views[i] = toServerView(info)
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(views)
}

// handleGetServer returns GET /api/v1/servers/{host} as a JSON ServerView.
// Returns 404 if the host is not registered.
func (ds *DashboardServer) handleGetServer(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	if host == "" {
		http.Error(w, "host parameter required", http.StatusBadRequest)
		return
	}

	info := ds.state.Get(host)
	if info == nil {
		http.Error(w, "host not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(toServerView(*info))
}

// handleDeleteServer processes DELETE /api/v1/servers/{host}.
func (ds *DashboardServer) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	if host == "" {
		http.Error(w, "host parameter required", http.StatusBadRequest)
		return
	}

	if !ds.state.Remove(host) {
		http.Error(w, "host not found", http.StatusNotFound)
		return
	}

	auth := GetAuthInfo(r)
	user := ""
	if auth != nil {
		user = auth.Username
	}
	slog.Info("dashboard=removed", slog.Int("event_id", etwids.EvtServerRemoved), "host", host, "user", user) //nolint:gosec // host is validated by the router pattern
	ds.broadcastServerDeleted(host, user)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}
