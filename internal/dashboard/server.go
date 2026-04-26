//go:build windows

package dashboard

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/etwids"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// hostnameRE matches RFC 1123 hostnames: labels of alphanumerics and hyphens
// (hyphen not at start/end), separated by dots.
var hostnameRE = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)*$`)

// sseKeepaliveInterval is the period between SSE keepalive comments.
// Exported as a package-level var so tests can shorten it without rebuilding.
var sseKeepaliveInterval = 25 * time.Second

// sseSessionCheckInterval is how often handleSSE revalidates the session cookie.
// When a session is deleted (logout from another tab, admin revocation) the SSE
// stream is terminated within this window so the browser reconnects and receives
// a 401 instead of continuing to receive events for a dead session.
// Exported as a package-level var so tests can shorten it without rebuilding.
var sseSessionCheckInterval = 5 * time.Minute

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

//go:embed all:dist
var distFS embed.FS

//go:embed favicon.png
var faviconPNG []byte

//go:embed openapi.yaml
var openapiSpec []byte

// DashboardServer holds the dashboard HTTP server state.
type DashboardServer struct {
	state        *ServerState
	cfg          dc.DashboardConfig
	server       *http.Server
	fingerprint  string        // SHA-256 fingerprint of the TLS certificate
	sessionStore *SessionStore // in-memory dashboard session store
	broker       *Broker       // SSE event broker for real-time updates
	ms           *telemetry.MetricsStore
	as           *telemetry.AuditStore
	mnt          *telemetry.MaintenanceStore

	// testNotifyFunc, if non-nil, is called by handleNotifyTest instead of
	// LoadConfig+SendTestNotification. Used in tests to avoid filesystem access.
	testNotifyFunc func() ([]dc.TestNotificationResult, error)

	// testLoadConfigFunc, if non-nil, is called by handleGetSettings instead
	// of dc.LoadConfig. Used in tests to avoid filesystem access.
	testLoadConfigFunc func() (*dc.Config, error)

	// testPutSettingsFunc, if non-nil, is called by handlePutSettings
	// instead of dc.UpdateNotifySettings. Receives the parsed request values;
	// nil notifications means the field was absent from the request body
	// (no-op for that field). nil threshold/gracePeriod/pollInterval mean the fields were absent.
	testPutSettingsFunc func(notifications *[]dc.NotificationTarget, sessionThreshold *int, gracePeriod *int, pollInterval *int, performance *dc.PerformanceConfig) error

	// testPutEvtSpikeEnabledFunc, if non-nil, is called by handlePutSettings
	// instead of dc.UpdateEvtSpikeEnabled for the evtspike.enabled flag. Nil
	// enabled means the field was absent from the request body (no-op).
	testPutEvtSpikeEnabledFunc func(enabled *bool) error

	// evtspikeStatus returns the current detector status for a host. Set by
	// the evtspike subsystem at Start; nil when the feature is off or not yet
	// wired. See evtspike.go handleEvtSpikeStatus for nil semantics.
	evtspikeStatus EvtSpikeStatusFunc

	// spikes backs GET /api/evtspike/spikes and the remote-agent POST /api/v1/spike
	// path. SQLite-backed (telemetry.EventSpikeStore) since feature 009 — previously
	// a per-host in-memory ring buffer. Always non-nil once StartDashboard returns;
	// the handler still tolerates nil so zero-telemetry bring-up paths (e.g. tests)
	// keep working.
	spikes *telemetry.EventSpikeStore

	// remoteEvtSpikeStatus caches the DetectorStatus most recently reported by
	// each registered agent via /api/v1/report. Remote hosts don't run a local
	// broker, so the central dashboard relies on the heartbeat payload to know
	// their detector state; the pull function for non-local hosts consults
	// this map so GET /api/evtspike/status?host=<remote> reflects reality.
	remoteEvtSpikeStatusMu sync.RWMutex
	remoteEvtSpikeStatus   map[string]evtspike.DetectorStatus
}

// StartDashboard creates the server state, sets up routes, and starts the
// HTTP listener. It returns the ServerState so the service main loop can
// call state.Update() after each check cycle. The server shuts down
// gracefully when ctx is cancelled.
// ms may be nil; when nil, metrics ingest is skipped (degraded mode).
// as may be nil; when nil, /api/v1/audit returns storage_error.
// mnt may be nil; when nil, /api/v1/maintenance/status returns storage_error.
// srv and sps are required — the dashboard depends on the SQLite-backed
// server roster and spike store since feature 009.
func StartDashboard(ctx context.Context, cfg dc.DashboardConfig, dataDir string, ms *telemetry.MetricsStore, as *telemetry.AuditStore, mnt *telemetry.MaintenanceStore, srv *telemetry.ServerStore, sps *telemetry.EventSpikeStore) (*ServerState, error) {
	// One-shot import of any legacy servers.json produced by a pre-009 binary.
	// Renames the file so the import is idempotent across reboots.
	if err := MigrateLegacyServersJSON(ctx, dataDir, srv); err != nil {
		slog.Warn("dashboard: legacy servers.json migration failed", "error", err)
	}

	state := NewServerState(srv)

	ds := &DashboardServer{
		state:                state,
		cfg:                  cfg,
		sessionStore:         NewSessionStore(ctx),
		broker:               NewBroker(),
		ms:                   ms,
		as:                   as,
		mnt:                  mnt,
		spikes:               sps,
		remoteEvtSpikeStatus: make(map[string]evtspike.DetectorStatus),
	}

	// Wire SSE broadcast: any state update (from handleReport or local ReportLocal)
	// triggers a server_update event to all connected browsers.
	state.OnUpdate = ds.broadcastServerUpdate

	// Wire evtspike ingestion: the service's Subsystem.OnSpike callback calls
	// OnEvtSpikeIngest to persist the spike to event_spikes and emit a
	// recent_spike SSE event on first-time insert. OnEvtSpikeStatus emits
	// detector_status on transitions; the broker dedups so callers may emit
	// liberally. RegisterEvtSpikeStatusFunc installs the pull-based status
	// lookup that backs GET /api/evtspike/status.
	state.OnEvtSpikeIngest = func(spike evtspike.SpikePayload) evtspike.RecentSpikeEntry {
		entry, inserted := ds.insertSpike(spike)
		if inserted {
			ds.broker.PublishRecentSpike(entry)
		}
		return entry
	}
	state.OnEvtSpikeStatus = func(status evtspike.DetectorStatus) {
		ds.broker.PublishDetectorStatus(status)
	}
	state.RegisterEvtSpikeStatusFunc = func(f EvtSpikeStatusFunc) {
		ds.evtspikeStatus = f
	}
	state.GetRemoteEvtSpikeStatus = ds.RemoteEvtSpikeStatus

	// Wire metrics ingest for both HTTP and local-report paths.
	if ms != nil {
		state.OnMetrics = func(r dc.CheckResult) {
			samples := checkResultSamples(r)
			if len(samples) == 0 {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := ms.Append(ctx, samples); err != nil {
				slog.Warn("telemetry: metrics append failed", "host", r.Host, "error", err)
			}
		}
	}

	// Per-IP rate limiter: 10 req/s sustained, burst 60.
	// Generous enough for normal agent reporting and browser use;
	// prevents runaway scripts from hammering the public health endpoint.
	rl := newIPRateLimiter(10, 60)

	// Stricter per-IP rate limiter for credential endpoints: 5 req/min sustained,
	// burst 3. Limits brute-force to ~300 attempts/hour per IP while allowing
	// normal use (a human retrying bad credentials a handful of times).
	authRL := newIPRateLimiter(float64(5)/60, 3)

	mux := http.NewServeMux()

	// wrapAuth is determined at compile time via build tags.
	// Production builds (default) use SSPI Negotiate middleware.
	// SSPI Negotiate auth for agent routes and session-based auth for UI routes.
	wa := func(h http.Handler) http.Handler { return wrapAuth(ctx, h, cfg.Group) }

	// requireSession validates the drainctl_session cookie for dashboard routes.
	// Dev builds treat this as a no-op.
	rs := requireSession(ds.sessionStore)

	rlw := func(h http.Handler) http.Handler { return rateLimitMiddleware(rl, h) }

	// Public routes — no authentication required.
	mux.Handle("GET /api/v1/health", rlw(http.HandlerFunc(ds.handleHealth)))

	// Agent routes — SSPI Negotiate authentication restricted to machine accounts.
	// Human domain accounts are rejected; only COMPUTERNAME$ principals may call these.
	rma := requireMachineAccount
	mux.Handle("POST /api/v1/register", rlw(wa(rma(http.HandlerFunc(ds.handleRegister)))))
	mux.Handle("POST /api/v1/report", rlw(wa(rma(http.HandlerFunc(ds.handleReport)))))
	mux.Handle("POST /api/v1/spike", rlw(wa(rma(http.HandlerFunc(ds.handleReportSpike)))))

	// Auth routes — create and invalidate dashboard sessions.
	// POST /api/v1/auth/negotiate: short-circuits to 200 for existing valid sessions;
	// otherwise runs SSPI Negotiate to create a new session.
	//
	// NegotiateMiddleware is instantiated once here so its pending-context map
	// survives across the NTLM multi-leg handshake (Type 1 → Type 2 → Type 3).
	// Instantiating it inside the handler would create a fresh map per request,
	// causing the Type 3 lookup to fail with SEC_E_INVALID_TOKEN.
	negotiateHandler := NegotiateMiddleware(ctx, handleNegotiate(ds.sessionStore, cfg.Group))
	mux.Handle("POST /api/v1/auth/negotiate", rlw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie("drainctl_session"); err == nil && cookie.Value != "" {
			if sess := ds.sessionStore.Get(cookie.Value); sess != nil {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]string{"username": sess.Username})
				return
			}
		}
		negotiateHandler.ServeHTTP(w, r)
	})))
	authRLW := func(h http.Handler) http.Handler { return rateLimitMiddleware(authRL, h) }
	mux.Handle("POST /api/v1/auth/login", authRLW(handleLogin(ds.sessionStore, cfg.Group)))
	mux.Handle("POST /api/v1/auth/logout", rlw(handleLogout(ds.sessionStore)))

	// GET /api/v1/me: lightweight session probe used on page load to restore an
	// existing authenticated session without triggering a new Negotiate handshake.
	// Returns {"user":"…"} with a valid session cookie; plain 401 otherwise.
	// Must NOT set WWW-Authenticate — this endpoint is plain-fetch only.
	mux.Handle("GET /api/v1/me", rlw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("drainctl_session")
		if err != nil || cookie.Value == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		sess := ds.sessionStore.Get(cookie.Value)
		if sess == nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"user": sess.Username})
	})))

	// Management / UI routes — require a valid dashboard session cookie.
	mux.Handle("GET /api/v1/metrics", rlw(rs(http.HandlerFunc(ds.handleSeedMetrics))))
	mux.Handle("GET /api/v1/metrics/{host}", rlw(rs(http.HandlerFunc(ds.handleMetrics))))
	mux.Handle("GET /api/v1/audit", rlw(rs(http.HandlerFunc(ds.handleAudit))))
	mux.Handle("GET /api/v1/history/{host}", rlw(rs(http.HandlerFunc(ds.handleHistory))))
	mux.Handle("GET /api/v1/servers", rlw(rs(http.HandlerFunc(ds.handleServers))))
	mux.Handle("GET /api/v1/servers/{host}", rlw(rs(http.HandlerFunc(ds.handleGetServer))))
	mux.Handle("DELETE /api/v1/servers/{host}", rlw(rs(http.HandlerFunc(ds.handleDeleteServer))))
	mux.Handle("GET /api/v1/settings", rlw(rs(http.HandlerFunc(ds.handleGetSettings))))
	mux.Handle("PUT /api/v1/settings", rlw(rs(http.HandlerFunc(ds.handlePutSettings))))
	// Per-target CRUD — atomic add/edit/delete that bypasses the bulk PUT and
	// returns the updated targets list (with has_secret) in one round-trip.
	mux.Handle("POST /api/v1/settings/notifications", rlw(rs(http.HandlerFunc(ds.handleAddNotificationTarget))))
	mux.Handle("PUT /api/v1/settings/notifications/{idx}", rlw(rs(http.HandlerFunc(ds.handleUpdateNotificationTarget))))
	mux.Handle("DELETE /api/v1/settings/notifications/{idx}", rlw(rs(http.HandlerFunc(ds.handleDeleteNotificationTarget))))
	mux.Handle("POST /api/v1/notify-test", rlw(rs(http.HandlerFunc(ds.handleNotifyTest))))
	mux.Handle("GET /api/v1/maintenance/status", rlw(rs(http.HandlerFunc(ds.handleMaintenance))))
	mux.Handle("GET /api/evtspike/status", rlw(rs(http.HandlerFunc(ds.handleEvtSpikeStatus))))
	mux.Handle("GET /api/evtspike/spikes", rlw(rs(http.HandlerFunc(ds.handleEvtSpikeSpikes))))

	// SSE event stream — session auth, no rate limit (long-lived connection).
	mux.Handle("GET /api/v1/events", rs(http.HandlerFunc(ds.handleSSE)))

	// Agent config pull — machine accounts only. Same data as /settings but
	// separate route with its own auth model (SSPI machine account, not session).
	mux.Handle("GET /api/v1/config", rlw(wa(rma(http.HandlerFunc(ds.handleGetSettings)))))
	mux.Handle("GET /favicon.ico", rlw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(faviconPNG)
	})))

	// OpenAPI spec and Swagger UI.
	mux.Handle("GET /api/v1/openapi.yaml", rlw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(openapiSpec)
	})))
	mux.Handle("GET /api/docs", rlw(http.HandlerFunc(handleSwaggerUI)))

	// Hashed static assets from Vite build — immutable caching.
	assets, _ := fs.Sub(distFS, "dist/assets")
	mux.Handle("GET /assets/", rlw(http.StripPrefix("/assets/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.FileServerFS(assets).ServeHTTP(w, r)
	}))))

	// Unhashed static files from Vite public/ (logo, images, etc.).
	// Serves any file with a static extension from the dist root;
	// everything else falls through to the SPA handler.
	distRoot, _ := fs.Sub(distFS, "dist")
	staticFS := http.FileServerFS(distRoot)
	mux.Handle("GET /{file}", rlw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("file")
		if dot := strings.LastIndex(name, "."); dot >= 0 {
			switch strings.ToLower(name[dot:]) {
			case ".png", ".jpg", ".jpeg", ".svg", ".ico", ".webp", ".gif", ".webmanifest":
				w.Header().Set("Cache-Control", "public, max-age=3600")
				staticFS.ServeHTTP(w, r)
				return
			}
		}
		ds.handleUI(w, r)
	})))

	// SPA HTML is served without authentication — the Svelte app handles the
	// auth flow client-side via POST /api/v1/auth/negotiate and /auth/login.
	mux.Handle("GET /", rlw(http.HandlerFunc(ds.handleUI)))

	addr := fmt.Sprintf(":%d", cfg.Port)

	// Resolve TLS configuration.
	var tlsCfg *tls.Config
	tlsCfg, err := loadOrGenerateTLS(cfg.TLSCert, cfg.TLSKey, dataDir)
	if err != nil {
		return nil, fmt.Errorf("dashboard TLS setup failed: %w", err)
	}

	// Extract certificate fingerprint for the register response.
	if tlsCfg != nil && len(tlsCfg.Certificates) > 0 {
		leaf, parseErr := x509.ParseCertificate(tlsCfg.Certificates[0].Certificate[0])
		if parseErr == nil {
			ds.fingerprint = certFingerprint(leaf)
		}
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dashboard listen %s: %w", addr, err)
	}

	// Wrap listener with TLS if configured.
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}

	// Apply security headers to all responses.
	// HSTS is additionally applied when TLS is active.
	handler := securityMiddleware(mux)
	if tlsCfg != nil {
		handler = hstsMiddleware(handler)
	}

	ds.server = &http.Server{
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
		// Route http.Server internal errors (TLS handshake failures, etc.) through
		// slog at DEBUG so they land in the DBG ETW channel (off by default) rather
		// than flowing via log.Default() → slog.LevelInfo → Operational channel.
		ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
	}

	// Start serving in background.
	scheme := "http"
	if tlsCfg != nil {
		scheme = "https"
	}
	go func() {
		slog.Info("dashboard=listening", "addr", addr, "scheme", scheme)
		if err := ds.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("dashboard server error", "error", err)
		}
	}()

	// Graceful shutdown on context cancellation.
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := ds.server.Shutdown(shutCtx); err != nil {
			slog.Warn("dashboard shutdown error", "error", err)
		}
		slog.Info("dashboard=stopped")
	}()

	return state, nil
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

// notifyTargetView is the write-only view of a notification target returned to
// the dashboard. Secrets are stripped — only a has_secret boolean is exposed
// so the UI can tell whether a saved credential exists.
type notifyTargetView struct {
	Type          string       `json:"type"`
	URL           string       `json:"url"`
	Triggers      []dc.Trigger `json:"triggers"`
	RepeatMinutes int          `json:"repeat_minutes,omitempty"`
	HasSecret     bool         `json:"has_secret"`
	To            []string     `json:"to,omitempty"`
	From          string       `json:"from,omitempty"`
	Enabled       *bool        `json:"enabled,omitempty"`
}

// notifyTargetWire is the inbound shape used by both the bulk PUT /settings and
// the per-target CRUD endpoints. ClearSecret is wire-only: when true, the
// existing on-disk secret is wiped regardless of the Secret field. We can't put
// it on dc.NotificationTarget because that struct is also the on-disk format.
type notifyTargetWire struct {
	dc.NotificationTarget
	ClearSecret bool `json:"clear_secret,omitempty"`
}

// makeNotifyTargetView projects an on-disk target into the redacted view.
func makeNotifyTargetView(t dc.NotificationTarget) notifyTargetView {
	return notifyTargetView{
		Type:          t.Type,
		URL:           t.URL,
		Triggers:      t.Triggers,
		RepeatMinutes: t.RepeatMinutes,
		HasSecret:     t.Secret != "",
		To:            t.To,
		From:          t.From,
		Enabled:       t.Enabled,
	}
}

// makeNotifyTargetViews projects a slice with a non-nil empty fallback so JSON
// encoding produces [] rather than null.
func makeNotifyTargetViews(targets []dc.NotificationTarget) []notifyTargetView {
	views := make([]notifyTargetView, len(targets))
	for i, t := range targets {
		views[i] = makeNotifyTargetView(t)
	}
	return views
}

// validateNotificationTarget returns a 400-class error describing why the
// target is rejected, or nil if it passes structural validation. The idx is
// included only in error messages from the bulk PUT path; per-target endpoints
// pass -1 to suppress the prefix.
func validateNotificationTarget(t dc.NotificationTarget, idx int) error {
	prefix := ""
	if idx >= 0 {
		prefix = fmt.Sprintf("notifications[%d]: ", idx)
	}
	if t.Type != "webhook" && t.Type != "ntfy" && t.Type != "email" {
		return fmt.Errorf("%sunknown type %q (want \"webhook\", \"ntfy\", or \"email\")", prefix, t.Type)
	}
	if t.URL != "" {
		lower := strings.ToLower(t.URL)
		validScheme := strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") ||
			strings.HasPrefix(lower, "smtp://") || strings.HasPrefix(lower, "smtps://")
		if !validScheme {
			return fmt.Errorf("%sURL must use http, https, smtp, or smtps scheme", prefix)
		}
	}
	for _, tr := range t.Triggers {
		if !dc.ValidTriggers[tr] {
			return fmt.Errorf("%sunknown trigger %q", prefix, tr)
		}
	}
	if t.RepeatMinutes < 0 || t.RepeatMinutes > dc.MaxRepeatMinutes {
		return fmt.Errorf("%srepeat_minutes must be 0–%d", prefix, dc.MaxRepeatMinutes)
	}
	return nil
}

// applySecretPolicy implements the three-mode secret semantics shared between
// the bulk PUT and the per-target endpoints:
//
//	clearSecret=true  → wipe the saved secret (regardless of new.Secret)
//	new.Secret == ""  → preserve the existing secret
//	new.Secret != ""  → replace with the new value (DPAPI-encrypted by Validate)
//
// existingSecret is the secret loaded from disk for this target slot. Per-target
// endpoints pass it in by index (robust to URL renames). The bulk PUT path
// looks it up by Type+URL.
func applySecretPolicy(newTarget *dc.NotificationTarget, existingSecret string, clearSecret bool) {
	switch {
	case clearSecret:
		newTarget.Secret = ""
	case newTarget.Secret == "":
		newTarget.Secret = existingSecret
	}
}

// handleGetSettings returns the current dashboard settings as JSON.
func (ds *DashboardServer) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	var cfg *dc.Config
	var err error
	if ds.testLoadConfigFunc != nil {
		cfg, err = ds.testLoadConfigFunc()
	} else {
		cfg, err = dc.LoadConfig()
	}
	if err != nil {
		slog.Error("load config failed", "error", err)
		http.Error(w, "failed to load config", http.StatusInternalServerError)
		return
	}

	views := makeNotifyTargetViews(cfg.Notifications)
	// evtspike surface is intentionally narrow: the modal only exposes the
	// enabled toggle per FR-028. Other evtspike fields (thresholds, channel
	// lists, baseline_path, security-channel gate) stay admin-only in
	// config.json and are not leaked here.
	type evtspikeView struct {
		Enabled bool `json:"enabled"`
	}
	out := struct {
		Notifications           []notifyTargetView   `json:"notifications"`
		SessionWarningThreshold int                  `json:"session_warning_threshold"`
		GracePeriod             int                  `json:"grace_period"`
		PollInterval            int                  `json:"poll_interval"`
		Performance             dc.PerformanceConfig `json:"performance"`
		EvtSpike                evtspikeView         `json:"evtspike"`
	}{
		Notifications:           views,
		SessionWarningThreshold: cfg.SessionWarningThreshold,
		GracePeriod:             cfg.GracePeriod,
		PollInterval:            cfg.PollInterval,
		Performance:             cfg.Performance,
		EvtSpike:                evtspikeView{Enabled: cfg.EvtSpike.Enabled},
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

// handlePutSettings accepts JSON and updates dashboard settings atomically.
// All provided fields are written in a single config load+save cycle.
// Absent fields (not present in the JSON body) are left unchanged; to clear
// notifications send "notifications": [].
func (ds *DashboardServer) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8192))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// evtspikeInput narrows the modal's write surface to just the enabled
	// flag (FR-028). Admin-only evtspike fields are not accepted here; those
	// belong in config.json.
	type evtspikeInput struct {
		Enabled *bool `json:"enabled,omitempty"`
	}
	// Use pointer-to-slice so we can distinguish absent ("don't change") from
	// explicit empty array ("clear all notifications").
	var in struct {
		Notifications           *[]notifyTargetWire   `json:"notifications"`
		SessionWarningThreshold *int                  `json:"session_warning_threshold,omitempty"`
		GracePeriod             *int                  `json:"grace_period,omitempty"`
		PollInterval            *int                  `json:"poll_interval,omitempty"`
		Performance             *dc.PerformanceConfig `json:"performance,omitempty"`
		EvtSpike                *evtspikeInput        `json:"evtspike,omitempty"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Project the wire targets back into the on-disk type once we've consumed
	// the wire-only flags.
	var notifications *[]dc.NotificationTarget
	if in.Notifications != nil {
		flat := make([]dc.NotificationTarget, len(*in.Notifications))
		for i, w := range *in.Notifications {
			flat[i] = w.NotificationTarget
		}
		notifications = &flat
	}

	// Validate numeric fields before persisting; return 400 (client error) not 500.
	if in.SessionWarningThreshold != nil && (*in.SessionWarningThreshold < 0 || *in.SessionWarningThreshold > 100) {
		http.Error(w, "session_warning_threshold must be 0–100", http.StatusBadRequest)
		return
	}
	if in.GracePeriod != nil && (*in.GracePeriod < 1 || *in.GracePeriod > 1440) {
		http.Error(w, "grace_period must be 1–1440", http.StatusBadRequest)
		return
	}
	if in.PollInterval != nil && (*in.PollInterval < 10 || *in.PollInterval > dc.MaxPollInterval) {
		http.Error(w, fmt.Sprintf("poll_interval must be 10–%d", dc.MaxPollInterval), http.StatusBadRequest)
		return
	}

	// Validate notification targets so invalid entries are rejected with a clear
	// 400 instead of being silently stripped by Config.Validate() after save.
	if notifications != nil {
		for i, t := range *notifications {
			if err := validateNotificationTarget(t, i); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
	}

	if in.Performance != nil {
		if in.Performance.SampleIntervalSec != 0 && (in.Performance.SampleIntervalSec < 10 || in.Performance.SampleIntervalSec > 300) {
			http.Error(w, "sample_interval_sec must be 10–300", http.StatusBadRequest)
			return
		}
		if in.Performance.LoadAlertDelaySec < 0 {
			http.Error(w, "load_alert_delay_sec must be non-negative", http.StatusBadRequest)
			return
		}
		if in.Performance.InputDelayAlertDelaySec < 0 {
			http.Error(w, "input_delay_alert_delay_sec must be non-negative", http.StatusBadRequest)
			return
		}
	}

	// Apply the three-mode secret policy. Bulk PUT keys lookups by Type+URL,
	// which is fragile across renames — the per-target endpoints below address
	// this by index instead.
	if notifications != nil {
		existing, err := dc.LoadConfig()
		secretMap := map[string]string{}
		if err == nil {
			for _, t := range existing.Notifications {
				secretMap[t.Type+"\x00"+t.URL] = t.Secret
			}
		}
		for i := range *notifications {
			t := &(*notifications)[i]
			wire := (*in.Notifications)[i] // sibling slot in the wire-only slice
			applySecretPolicy(t, secretMap[t.Type+"\x00"+t.URL], wire.ClearSecret)
		}
	}

	if ds.testPutSettingsFunc != nil {
		if err := ds.testPutSettingsFunc(notifications, in.SessionWarningThreshold, in.GracePeriod, in.PollInterval, in.Performance); err != nil {
			slog.Error("update config failed (test hook)", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		if err := dc.UpdateNotifySettings(notifications, in.SessionWarningThreshold, in.GracePeriod, in.PollInterval, in.Performance); err != nil {
			slog.Error("update settings failed", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	var evtspikeEnabled *bool
	if in.EvtSpike != nil {
		evtspikeEnabled = in.EvtSpike.Enabled
	}
	if ds.testPutEvtSpikeEnabledFunc != nil {
		if err := ds.testPutEvtSpikeEnabledFunc(evtspikeEnabled); err != nil {
			slog.Error("update evtspike enabled failed (test hook)", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		if err := dc.UpdateEvtSpikeEnabled(evtspikeEnabled); err != nil {
			slog.Error("update evtspike enabled failed", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	auth := GetAuthInfo(r)
	user := ""
	if auth != nil {
		user = auth.Username
	}
	slog.Info("dashboard=settings-updated", slog.Int("event_id", etwids.EvtDashboardConfigChange), "user", user)

	// Broadcast settings change to connected browsers.
	ds.broadcastSettingsUpdate()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// notifyTargetsResponse is the body returned by the per-target CRUD endpoints.
// It carries the full updated targets list (with has_secret) so the frontend
// can replace its local copy in one round-trip — no follow-up GET required.
type notifyTargetsResponse struct {
	Notifications []notifyTargetView `json:"notifications"`
}

// updateTargetsAndRespond runs `mutate` under the config lock, writes the
// resulting notifications list, broadcasts the change, and renders the new
// view to the client. mutate may return an error to abort with a 4xx (the
// caller picks the status code by inspecting the error).
func (ds *DashboardServer) updateTargetsAndRespond(w http.ResponseWriter, r *http.Request, mutate func(targets []dc.NotificationTarget) ([]dc.NotificationTarget, error)) {
	var newTargets []dc.NotificationTarget
	err := dc.ReadModifyWriteNotifications(func(targets []dc.NotificationTarget) ([]dc.NotificationTarget, error) {
		out, mErr := mutate(targets)
		if mErr != nil {
			return nil, mErr
		}
		newTargets = out
		return out, nil
	})
	if err != nil {
		// Handler errors carry a wrapped HTTP status via httpStatusError.
		var httpErr *httpStatusError
		if errors.As(err, &httpErr) {
			http.Error(w, httpErr.msg, httpErr.status)
			return
		}
		slog.Error("update notification targets failed", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	auth := GetAuthInfo(r)
	user := ""
	if auth != nil {
		user = auth.Username
	}
	slog.Info("dashboard=settings-updated", slog.Int("event_id", etwids.EvtDashboardConfigChange), "user", user, "scope", "notifications")

	ds.broadcastSettingsUpdate()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(notifyTargetsResponse{Notifications: makeNotifyTargetViews(newTargets)})
}

// httpStatusError lets the per-target CRUD handlers signal a desired HTTP
// status from inside the readModifyWrite callback.
type httpStatusError struct {
	status int
	msg    string
}

func (e *httpStatusError) Error() string { return e.msg }

func badRequest(msg string) error { return &httpStatusError{status: http.StatusBadRequest, msg: msg} }
func notFound(msg string) error   { return &httpStatusError{status: http.StatusNotFound, msg: msg} }

// decodeNotifyTargetWire reads a single notifyTargetWire from the request body,
// rejecting payloads larger than 4 KB. Returns 400-class errors for the caller
// to surface to the client.
func decodeNotifyTargetWire(r *http.Request) (notifyTargetWire, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		return notifyTargetWire{}, badRequest("bad request")
	}
	var w notifyTargetWire
	if err := json.Unmarshal(body, &w); err != nil {
		return notifyTargetWire{}, badRequest("invalid JSON")
	}
	return w, nil
}

// handleAddNotificationTarget appends a new notification target to the saved
// list. The wire-only clear_secret flag is ignored on add — there is no
// existing secret to clear. Returns the full updated targets list.
func (ds *DashboardServer) handleAddNotificationTarget(w http.ResponseWriter, r *http.Request) {
	wire, err := decodeNotifyTargetWire(r)
	if err != nil {
		var httpErr *httpStatusError
		if errors.As(err, &httpErr) {
			http.Error(w, httpErr.msg, httpErr.status)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if vErr := validateNotificationTarget(wire.NotificationTarget, -1); vErr != nil {
		http.Error(w, vErr.Error(), http.StatusBadRequest)
		return
	}

	ds.updateTargetsAndRespond(w, r, func(targets []dc.NotificationTarget) ([]dc.NotificationTarget, error) {
		// clear_secret on add is a no-op (nothing to clear). New secret is taken
		// as-is; empty means no credential.
		return append(targets, wire.NotificationTarget), nil
	})
}

// handleUpdateNotificationTarget replaces the target at the given index with
// the request body. Secrets follow the standard three-mode policy, except the
// existing secret is looked up by INDEX (not Type+URL), so URL renames preserve
// the credential.
func (ds *DashboardServer) handleUpdateNotificationTarget(w http.ResponseWriter, r *http.Request) {
	idx, ok := parseTargetIndex(w, r)
	if !ok {
		return
	}
	wire, err := decodeNotifyTargetWire(r)
	if err != nil {
		var httpErr *httpStatusError
		if errors.As(err, &httpErr) {
			http.Error(w, httpErr.msg, httpErr.status)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if vErr := validateNotificationTarget(wire.NotificationTarget, -1); vErr != nil {
		http.Error(w, vErr.Error(), http.StatusBadRequest)
		return
	}

	ds.updateTargetsAndRespond(w, r, func(targets []dc.NotificationTarget) ([]dc.NotificationTarget, error) {
		if idx < 0 || idx >= len(targets) {
			return nil, notFound(fmt.Sprintf("notification target index %d not found (have %d)", idx, len(targets)))
		}
		updated := wire.NotificationTarget
		applySecretPolicy(&updated, targets[idx].Secret, wire.ClearSecret)
		out := make([]dc.NotificationTarget, len(targets))
		copy(out, targets)
		out[idx] = updated
		return out, nil
	})
}

// handleDeleteNotificationTarget removes the target at the given index.
func (ds *DashboardServer) handleDeleteNotificationTarget(w http.ResponseWriter, r *http.Request) {
	idx, ok := parseTargetIndex(w, r)
	if !ok {
		return
	}

	ds.updateTargetsAndRespond(w, r, func(targets []dc.NotificationTarget) ([]dc.NotificationTarget, error) {
		if idx < 0 || idx >= len(targets) {
			return nil, notFound(fmt.Sprintf("notification target index %d not found (have %d)", idx, len(targets)))
		}
		out := make([]dc.NotificationTarget, 0, len(targets)-1)
		out = append(out, targets[:idx]...)
		out = append(out, targets[idx+1:]...)
		return out, nil
	})
}

// parseTargetIndex extracts and validates the {idx} path parameter. Writes a
// 400 response and returns ok=false on failure.
func parseTargetIndex(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.PathValue("idx")
	idx, err := strconv.Atoi(raw)
	if err != nil || idx < 0 {
		http.Error(w, "idx must be a non-negative integer", http.StatusBadRequest)
		return 0, false
	}
	return idx, true
}

// handleNotifyTest sends a test notification.
// If the request body contains a JSON-encoded NotificationTarget, only that
// single target is tested (used by the target edit modal). Otherwise, all
// currently saved targets are tested (used by the config modal "Send Test").
func (ds *DashboardServer) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	auth := GetAuthInfo(r)
	user := ""
	if auth != nil {
		user = auth.Username
	}

	// Try to decode a single target from the body.
	// Webhook/ntfy targets are identified by a non-empty URL; email targets
	// have no URL (they use From/To instead) so we check Type == "email".
	var singleTarget *dc.NotificationTarget
	if r.Body != nil && r.ContentLength > 0 {
		var t dc.NotificationTarget
		if err := json.NewDecoder(r.Body).Decode(&t); err == nil && t.Type != "" && (t.URL != "" || t.Type == "email") {
			singleTarget = &t
		}
	}

	// Secrets are write-only — the browser never has them. If the secret
	// field is empty, look up the saved secret so the test uses real credentials.
	if singleTarget != nil && singleTarget.Secret == "" {
		if cfg, loadErr := dc.LoadConfig(); loadErr == nil {
			for _, saved := range cfg.Notifications {
				if saved.Type == singleTarget.Type && saved.URL == singleTarget.URL {
					singleTarget.Secret = saved.Secret
					break
				}
			}
		}
	}

	var (
		results []dc.TestNotificationResult
		err     error
	)
	if singleTarget != nil {
		slog.Info("dashboard=notify-test-target", "user", user, "type", singleTarget.Type, "url", singleTarget.URL)
		results, err = dc.SendTestNotification([]dc.NotificationTarget{*singleTarget})
	} else {
		slog.Info("dashboard=notify-test", "user", user)
		// testNotifyFunc can be injected in tests to avoid real config/network I/O.
		fn := ds.testNotifyFunc
		if fn == nil {
			fn = func() ([]dc.TestNotificationResult, error) {
				loadFn := ds.testLoadConfigFunc
				if loadFn == nil {
					loadFn = func() (*dc.Config, error) { return dc.LoadConfig() }
				}
				cfg, loadErr := loadFn()
				if loadErr != nil {
					return nil, fmt.Errorf("failed to load config: %w", loadErr)
				}
				return dc.SendTestNotification(cfg.Notifications)
			}
		}
		results, err = fn()
	}

	// Always return the per-target results (UI uses them to show which target
	// failed). HTTP status is 200 if everything succeeded, 207 (Multi-Status)
	// if some targets failed but at least one succeeded, 400 if all failed or
	// the request was malformed.
	w.Header().Set("Content-Type", "application/json")
	successes := 0
	for _, r := range results {
		if r.OK {
			successes++
		}
	}

	body := map[string]any{
		"ok":      err == nil,
		"results": results,
	}
	if err != nil {
		body["error"] = err.Error()
	}
	status := http.StatusOK
	if err != nil {
		if successes == 0 {
			status = http.StatusBadRequest
		} else {
			status = http.StatusMultiStatus
		}
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// handleUI serves the embedded SPA index.html.
// It generates a per-request nonce and injects it into both the Content-Security-Policy
// header and the theme flash-prevention inline <script> tag in index.html.
// This removes the need for 'unsafe-inline' in script-src while still allowing
// the one inline script that prevents a light-flash before the theme JS runs.
func (ds *DashboardServer) handleUI(w http.ResponseWriter, _ *http.Request) {
	data, err := distFS.ReadFile("dist/index.html")
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}

	// Generate a cryptographically-random per-request nonce (128 bits).
	var nb [16]byte
	if _, err := rand.Read(nb[:]); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	nonce := base64.StdEncoding.EncodeToString(nb[:])

	// Extend the CSP with the nonce, overriding the header set by securityMiddleware.
	w.Header().Set("Content-Security-Policy",
		strings.Replace(cspBase, "script-src 'self'", "script-src 'self' 'nonce-"+nonce+"'", 1))

	// Inject the nonce attribute into the theme inline script.
	html := strings.Replace(string(data), "<script>", "<script nonce=\""+nonce+"\">", 1)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(html))
}

// staleThreshold is the maximum time since a server's last report before it is
// considered offline. Matches the STALE constant (600 000 ms) in the dashboard UI.
const staleThreshold = 10 * time.Minute

// handleHealth serves GET /api/v1/health without authentication.
// Returns version, registered server count, and per-status counts.
// Useful for load-balancer health checks and external monitoring.
//
// A server is counted as "offline" when its last report is older than
// staleThreshold, regardless of the last-reported status. This matches the
// dashboard UI's staleness check so the API and UI always agree.
func (ds *DashboardServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	servers := ds.state.All()
	now := time.Now()

	healthy, warning, grace, alerting, unknown, offline := 0, 0, 0, 0, 0, 0
	for _, s := range servers {
		if s.LastResult == nil {
			unknown++
			continue
		}
		if !s.LastSeen.IsZero() && now.Sub(s.LastSeen) > staleThreshold {
			offline++
			continue
		}
		switch s.LastResult.Status {
		case "Healthy":
			healthy++
		case "Warning":
			warning++
		case "Alert":
			alerting++
		case "Grace":
			grace++
		default:
			unknown++
		}
	}

	resp := struct {
		OK       bool   `json:"ok"`
		Version  string `json:"version"`
		Servers  int    `json:"servers"`
		Healthy  int    `json:"healthy"`
		Warning  int    `json:"warning"`
		Grace    int    `json:"grace"`
		Alerting int    `json:"alerting"`
		Offline  int    `json:"offline"`
		Unknown  int    `json:"unknown"`
	}{
		OK:       true,
		Version:  dc.Version,
		Servers:  len(servers),
		Healthy:  healthy,
		Warning:  warning,
		Grace:    grace,
		Alerting: alerting,
		Offline:  offline,
		Unknown:  unknown,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleHistory serves GET /api/v1/history/{host}.
// The in-memory ring was removed in favour of the SQLite telemetry store.
// This route is retained for one release to give callers time to migrate.
func (ds *DashboardServer) handleHistory(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusGone)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": "use /api/v1/metrics/{host} or /api/v1/audit",
	})
}

// handleMaintenance serves GET /api/v1/maintenance/status per
// contracts/http-maintenance.md. One row per background job, with
// expected_interval_seconds echoed from the current telemetry config and
// overdue computed server-side per FR-031.
func (ds *DashboardServer) handleMaintenance(w http.ResponseWriter, r *http.Request) {
	if ds.mnt == nil {
		writeJSONError(w, "storage_error", http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	jobs, err := ds.mnt.ListJobs(ctx)
	if err != nil {
		slog.Error("maintenance: list jobs failed", "error", err)
		writeJSONError(w, "storage_error", http.StatusInternalServerError)
		return
	}

	var cfg *dc.Config
	if ds.testLoadConfigFunc != nil {
		cfg, err = ds.testLoadConfigFunc()
	} else {
		cfg, err = dc.LoadConfig()
	}
	if err != nil {
		slog.Error("maintenance: load config failed", "error", err)
		writeJSONError(w, "storage_error", http.StatusInternalServerError)
		return
	}

	aggregatorInterval := int64(cfg.Telemetry.AggregatorIntervalSeconds)
	retentionInterval := int64(cfg.Telemetry.RetentionIntervalMinutes) * 60
	serverTime := time.Now().UTC()

	type jobJSON struct {
		Name                    string `json:"name"`
		Started                 string `json:"started"`
		Finished                string `json:"finished"`
		DurationMs              int64  `json:"duration_ms"`
		Outcome                 string `json:"outcome"`
		Reason                  string `json:"reason"`
		RowsAffected            int64  `json:"rows_affected"`
		Overdue                 bool   `json:"overdue"`
		ExpectedIntervalSeconds int64  `json:"expected_interval_seconds"`
	}

	out := struct {
		Jobs       []jobJSON `json:"jobs"`
		ServerTime string    `json:"server_time"`
	}{
		Jobs:       make([]jobJSON, 0, len(jobs)),
		ServerTime: serverTime.Format(time.RFC3339Nano),
	}

	for _, j := range jobs {
		var expected int64
		switch j.Name {
		case "aggregator_5min", "aggregator_hourly":
			expected = aggregatorInterval
		case "retention":
			expected = retentionInterval
		default:
			expected = 0
		}
		overdue := j.IsOverdue(expected, serverTime)
		out.Jobs = append(out.Jobs, jobJSON{
			Name:                    j.Name,
			Started:                 j.Started.Format(time.RFC3339Nano),
			Finished:                j.Finished.Format(time.RFC3339Nano),
			DurationMs:              j.DurationMs,
			Outcome:                 j.Outcome,
			Reason:                  j.Reason,
			RowsAffected:            j.RowsAffected,
			Overdue:                 overdue,
			ExpectedIntervalSeconds: expected,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// writeJSONError writes a JSON body {"error":"<code>"} with the given HTTP status.
func writeJSONError(w http.ResponseWriter, code string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

// resolveMetricsTier maps a request's resolution parameter to a concrete tier.
// Explicit "raw"/"1min"/"5min"/"hourly" pass through. "auto" picks by window
// size (≤15m → raw, ≤1h → 1min, ≤36h → 5min, >36h → hourly) then degrades to
// the next coarser tier if the chosen tier's oldest row does not cover `from`.
// Hourly is never degraded (it is the coarsest tier).
//
// The 1-minute tier is a virtual GROUP BY on metrics_raw — the 1H dashboard
// pill maps to 60 buckets. Short windows (15M pill) render raw so the chart
// shows per-second detail before the minute aggregation kicks in.
func (ds *DashboardServer) resolveMetricsTier(
	ctx context.Context,
	host string,
	from, to time.Time,
	resolution string,
) (telemetry.Tier, error) {
	switch resolution {
	case "raw":
		return telemetry.TierRaw, nil
	case "1min":
		return telemetry.TierOneMin, nil
	case "5min":
		return telemetry.TierFiveMin, nil
	case "hourly":
		return telemetry.TierHourly, nil
	}

	window := to.Sub(from)
	var tier telemetry.Tier
	switch {
	case window <= 15*time.Minute:
		tier = telemetry.TierRaw
	case window <= time.Hour:
		tier = telemetry.TierOneMin
	case window <= 36*time.Hour:
		tier = telemetry.TierFiveMin
	default:
		tier = telemetry.TierHourly
	}

	for tier != telemetry.TierHourly {
		oldest, _, err := ds.ms.BoundsForTier(ctx, host, tier)
		if err != nil {
			return 0, err
		}
		if oldest != nil && !oldest.After(from) {
			return tier, nil
		}
		switch tier {
		case telemetry.TierRaw, telemetry.TierOneMin:
			// Both share metrics_raw's bounds; falling through either goes to 5min.
			tier = telemetry.TierFiveMin
		case telemetry.TierFiveMin:
			tier = telemetry.TierHourly
		}
	}
	return tier, nil
}

// handleMetrics serves GET /api/v1/metrics/{host} per contracts/http-metrics.md.
// When host is "_fleet" the request is routed to handleFleetMetrics.
func (ds *DashboardServer) handleMetrics(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	if host == "" {
		writeJSONError(w, "unknown_host", http.StatusNotFound)
		return
	}
	if host == "_fleet" {
		ds.handleFleetMetrics(w, r)
		return
	}
	if !ds.state.IsRegistered(host) {
		writeJSONError(w, "unknown_host", http.StatusNotFound)
		return
	}

	q := r.URL.Query()
	fromStr := q.Get("from")
	toStr := q.Get("to")
	if fromStr == "" || toStr == "" {
		writeJSONError(w, "invalid_range", http.StatusBadRequest)
		return
	}
	from, err := time.Parse(time.RFC3339, fromStr)
	if err != nil {
		writeJSONError(w, "invalid_range", http.StatusBadRequest)
		return
	}
	to, err := time.Parse(time.RFC3339, toStr)
	if err != nil {
		writeJSONError(w, "invalid_range", http.StatusBadRequest)
		return
	}
	if !to.After(from) || to.Sub(from) > 90*24*time.Hour {
		writeJSONError(w, "invalid_range", http.StatusBadRequest)
		return
	}

	resStr := q.Get("resolution")
	if resStr == "" {
		resStr = "auto"
	}
	if resStr != "auto" && resStr != "raw" && resStr != "1min" && resStr != "5min" && resStr != "hourly" {
		writeJSONError(w, "invalid_resolution", http.StatusBadRequest)
		return
	}

	var counters []string
	if csv := q.Get("counters"); csv != "" {
		for _, c := range strings.Split(csv, ",") {
			c = strings.TrimSpace(c)
			if c != "" {
				counters = append(counters, c)
			}
		}
	}

	if ds.ms == nil {
		writeJSONError(w, "storage_error", http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	tier, err := ds.resolveMetricsTier(ctx, host, from, to, resStr)
	if err != nil {
		slog.Error("metrics: tier resolution failed", "host", host, "error", err) //nolint:gosec // host is validated against registered server list
		writeJSONError(w, "storage_error", http.StatusInternalServerError)
		return
	}

	sr, err := ds.ms.QueryRange(ctx, host, from, to, tier, counters)
	if err != nil {
		slog.Error("metrics: query failed", "host", host, "error", err) //nolint:gosec // host is validated against registered server list
		writeJSONError(w, "storage_error", http.StatusInternalServerError)
		return
	}

	type counterJSON struct {
		T   []int64   `json:"t"`
		Avg []float64 `json:"avg"`
		Min []float64 `json:"min"`
		Max []float64 `json:"max"`
	}
	type metricsResp struct {
		Host            string                  `json:"host"`
		Tier            string                  `json:"tier"`
		From            string                  `json:"from"`
		To              string                  `json:"to"`
		OldestAvailable *string                 `json:"oldest_available"`
		NewestAvailable *string                 `json:"newest_available"`
		Series          map[string]*counterJSON `json:"series"`
	}

	resp := metricsResp{
		Host:   host,
		Tier:   sr.Tier.TierName(),
		From:   from.UTC().Format(time.RFC3339),
		To:     to.UTC().Format(time.RFC3339),
		Series: make(map[string]*counterJSON, len(sr.Data)),
	}
	if sr.OldestAvailable != nil {
		s := sr.OldestAvailable.UTC().Format(time.RFC3339)
		resp.OldestAvailable = &s
	}
	if sr.NewestAvailable != nil {
		s := sr.NewestAvailable.UTC().Format(time.RFC3339)
		resp.NewestAvailable = &s
	}
	for name, cs := range sr.Data {
		t := cs.T
		if t == nil {
			t = []int64{}
		}
		avg := cs.Avg
		if avg == nil {
			avg = []float64{}
		}
		min := cs.Min
		if min == nil {
			min = []float64{}
		}
		max := cs.Max
		if max == nil {
			max = []float64{}
		}
		resp.Series[name] = &counterJSON{T: t, Avg: avg, Min: min, Max: max}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// resolveFleetMetricsTier picks the concrete tier for a fleet query using the
// same window-size heuristic as resolveMetricsTier, then degrades if the
// fleet-wide oldest timestamp does not cover from.
func (ds *DashboardServer) resolveFleetMetricsTier(
	ctx context.Context,
	hosts []string,
	from, to time.Time,
	resolution string,
) (telemetry.Tier, error) {
	switch resolution {
	case "raw":
		return telemetry.TierRaw, nil
	case "1min":
		return telemetry.TierOneMin, nil
	case "5min":
		return telemetry.TierFiveMin, nil
	case "hourly":
		return telemetry.TierHourly, nil
	}
	window := to.Sub(from)
	var tier telemetry.Tier
	switch {
	case window <= 15*time.Minute:
		tier = telemetry.TierRaw
	case window <= time.Hour:
		tier = telemetry.TierOneMin
	case window <= 36*time.Hour:
		tier = telemetry.TierFiveMin
	default:
		tier = telemetry.TierHourly
	}
	for tier != telemetry.TierHourly {
		oldest, _, err := ds.ms.BoundsForTierFleet(ctx, hosts, tier)
		if err != nil {
			return 0, err
		}
		if oldest != nil && !oldest.After(from) {
			return tier, nil
		}
		switch tier {
		case telemetry.TierRaw, telemetry.TierOneMin:
			tier = telemetry.TierFiveMin
		case telemetry.TierFiveMin:
			tier = telemetry.TierHourly
		}
	}
	return tier, nil
}

// handleFleetMetrics serves GET /api/v1/metrics/_fleet per
// contracts/http-fleet-metrics.md, returning retained history aggregated
// across all registered hosts.
func (ds *DashboardServer) handleFleetMetrics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fromStr := q.Get("from")
	toStr := q.Get("to")
	if fromStr == "" || toStr == "" {
		writeJSONError(w, "invalid_range", http.StatusBadRequest)
		return
	}
	from, err := time.Parse(time.RFC3339, fromStr)
	if err != nil {
		writeJSONError(w, "invalid_range", http.StatusBadRequest)
		return
	}
	to, err := time.Parse(time.RFC3339, toStr)
	if err != nil {
		writeJSONError(w, "invalid_range", http.StatusBadRequest)
		return
	}
	if !to.After(from) || to.Sub(from) > 90*24*time.Hour {
		writeJSONError(w, "invalid_range", http.StatusBadRequest)
		return
	}
	resStr := q.Get("resolution")
	if resStr == "" {
		resStr = "auto"
	}
	if resStr != "auto" && resStr != "raw" && resStr != "1min" && resStr != "5min" && resStr != "hourly" {
		writeJSONError(w, "invalid_resolution", http.StatusBadRequest)
		return
	}
	var counters []string
	if csv := q.Get("counters"); csv != "" {
		for _, c := range strings.Split(csv, ",") {
			c = strings.TrimSpace(c)
			if c != "" {
				counters = append(counters, c)
			}
		}
	}
	if ds.ms == nil {
		writeJSONError(w, "storage_error", http.StatusInternalServerError)
		return
	}

	infos := ds.state.All()
	hosts := make([]string, len(infos))
	for i, info := range infos {
		hosts[i] = info.Hostname
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	tier, err := ds.resolveFleetMetricsTier(ctx, hosts, from, to, resStr)
	if err != nil {
		slog.Error("fleet metrics: tier resolution failed", "error", err)
		writeJSONError(w, "storage_error", http.StatusInternalServerError)
		return
	}

	// Raw-tier bucketing must be wide enough to absorb every agent's poll
	// jitter — clocks drift, perfmon collection takes variable time, and
	// pull-config propagation lag means hosts may briefly run different
	// sample_interval_sec values. A bucket equal to sample_interval still
	// puts hosts on opposite sides of a 30-s boundary when their phases
	// differ by ~T, producing the alternating-host zigzag we already fixed
	// once. 2× sample_interval guarantees every host has at least one
	// sample per bucket regardless of phase. Falls back to the store's
	// default when config isn't available (test harness, hot-swap gap).
	rawBucketMs := int64(telemetry.DefaultRawFleetBucketMs)
	var cfg *dc.Config
	var cerr error
	if ds.testLoadConfigFunc != nil {
		cfg, cerr = ds.testLoadConfigFunc()
	} else {
		cfg, cerr = dc.LoadConfig()
	}
	if cerr == nil && cfg.Performance.SampleIntervalSec > 0 {
		rawBucketMs = int64(cfg.Performance.SampleIntervalSec) * 2 * 1000
	}

	sr, err := ds.ms.QueryRangeFleet(ctx, hosts, from, to, tier, counters, rawBucketMs)
	if err != nil {
		slog.Error("fleet metrics: query failed", "error", err)
		writeJSONError(w, "storage_error", http.StatusInternalServerError)
		return
	}

	type counterJSON struct {
		T   []int64   `json:"t"`
		Avg []float64 `json:"avg"`
		Min []float64 `json:"min"`
		Max []float64 `json:"max"`
	}
	type metricsResp struct {
		Host            string                  `json:"host"`
		Tier            string                  `json:"tier"`
		From            string                  `json:"from"`
		To              string                  `json:"to"`
		OldestAvailable *string                 `json:"oldest_available"`
		NewestAvailable *string                 `json:"newest_available"`
		Series          map[string]*counterJSON `json:"series"`
	}
	resp := metricsResp{
		Host:   "_fleet",
		Tier:   sr.Tier.TierName(),
		From:   from.UTC().Format(time.RFC3339),
		To:     to.UTC().Format(time.RFC3339),
		Series: make(map[string]*counterJSON, len(sr.Data)),
	}
	if sr.OldestAvailable != nil {
		s := sr.OldestAvailable.UTC().Format(time.RFC3339)
		resp.OldestAvailable = &s
	}
	if sr.NewestAvailable != nil {
		s := sr.NewestAvailable.UTC().Format(time.RFC3339)
		resp.NewestAvailable = &s
	}
	for name, cs := range sr.Data {
		t := cs.T
		if t == nil {
			t = []int64{}
		}
		avg := cs.Avg
		if avg == nil {
			avg = []float64{}
		}
		min := cs.Min
		if min == nil {
			min = []float64{}
		}
		max := cs.Max
		if max == nil {
			max = []float64{}
		}
		resp.Series[name] = &counterJSON{T: t, Avg: avg, Min: min, Max: max}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// seedMetricsCounters are the raw-tier counter names queried for the metrics seed route.
var seedMetricsCounters = []string{
	"cpu_pct",
	"mem_avail_mb",
	"mem_total_mb",
	"input_delay_p95_ms",
	"sessions_total",
	"disk_queue",
	"tcp_retrans_sec",
}

// handleSeedMetrics serves GET /api/v1/metrics (no {host}) per
// contracts/http-metrics-seed.md.  Returns bounded recent raw-tier history
// per registered host for sparkline cold-start seeding.
func (ds *DashboardServer) handleSeedMetrics(w http.ResponseWriter, r *http.Request) {
	if ds.ms == nil {
		writeJSONError(w, "storage_error", http.StatusInternalServerError)
		return
	}

	q := r.URL.Query()

	if res := q.Get("resolution"); res != "" && res != "raw" {
		writeJSONError(w, "invalid_resolution", http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	from := now.Add(-30 * time.Minute)
	to := now

	if s := q.Get("from"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeJSONError(w, "invalid_range", http.StatusBadRequest)
			return
		}
		from = t
	}
	if s := q.Get("to"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeJSONError(w, "invalid_range", http.StatusBadRequest)
			return
		}
		to = t
	}
	if !to.After(from) || to.Sub(from) > 90*24*time.Hour {
		writeJSONError(w, "invalid_range", http.StatusBadRequest)
		return
	}

	limit := 60
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			writeJSONError(w, "invalid_range", http.StatusBadRequest)
			return
		}
		if n > 200 {
			n = 200
		}
		limit = n
	}

	type seedSampleJSON struct {
		Time       int64   `json:"time"`
		CPU        float64 `json:"cpu"`
		Mem        float64 `json:"mem"`
		InputDelay float64 `json:"inputDelay"`
		Sessions   int     `json:"sessions"`
		DiskQueue  float64 `json:"diskQueue"`
		TcpRetrans float64 `json:"tcpRetrans"`
	}

	infos := ds.state.All()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	result := make(map[string][]seedSampleJSON, len(infos))
	for _, info := range infos {
		host := info.Hostname
		sr, err := ds.ms.QueryRange(ctx, host, from, to, telemetry.TierRaw, seedMetricsCounters)
		if err != nil {
			slog.Error("seed metrics: query failed", "host", host, "error", err) //nolint:gosec
			writeJSONError(w, "storage_error", http.StatusInternalServerError)
			return
		}

		byTs := make(map[int64]*seedSampleJSON)
		availByTs := make(map[int64]float64)
		totalByTs := make(map[int64]float64)

		for name, cs := range sr.Data {
			for i, ts := range cs.T {
				s, ok := byTs[ts]
				if !ok {
					s = &seedSampleJSON{Time: ts}
					byTs[ts] = s
				}
				v := cs.Avg[i]
				switch name {
				case "cpu_pct":
					s.CPU = v
				case "mem_avail_mb":
					availByTs[ts] = v
				case "mem_total_mb":
					totalByTs[ts] = v
				case "input_delay_p95_ms":
					s.InputDelay = v
				case "sessions_total":
					s.Sessions = int(v)
				case "disk_queue":
					s.DiskQueue = v
				case "tcp_retrans_sec":
					s.TcpRetrans = v
				}
			}
		}

		for ts, avail := range availByTs {
			if total, ok := totalByTs[ts]; ok && total > 0 {
				if s, ok := byTs[ts]; ok {
					s.Mem = (1 - avail/total) * 100
				}
			}
		}

		tss := make([]int64, 0, len(byTs))
		for ts := range byTs {
			tss = append(tss, ts)
		}
		sort.Slice(tss, func(i, j int) bool { return tss[i] < tss[j] })
		if len(tss) > limit {
			tss = tss[len(tss)-limit:]
		}

		samples := make([]seedSampleJSON, 0, len(tss))
		for _, ts := range tss {
			samples = append(samples, *byTs[ts])
		}
		result[host] = samples
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// handleAudit serves GET /api/v1/audit per contracts/http-audit.md.
// Time-range query over the audit table with cursor pagination.
func (ds *DashboardServer) handleAudit(w http.ResponseWriter, r *http.Request) {
	if ds.as == nil {
		writeJSONError(w, "storage_error", http.StatusInternalServerError)
		return
	}

	q := r.URL.Query()

	filter := telemetry.QueryFilter{
		Host:        q.Get("host"),
		Actor:       q.Get("actor"),
		ChangesOnly: true,
	}

	if s := q.Get("from"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeJSONError(w, "invalid_range", http.StatusBadRequest)
			return
		}
		tu := t.UTC()
		filter.From = &tu
	}
	if s := q.Get("to"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeJSONError(w, "invalid_range", http.StatusBadRequest)
			return
		}
		tu := t.UTC()
		filter.To = &tu
	}
	if filter.From != nil && filter.To != nil && !filter.To.After(*filter.From) {
		writeJSONError(w, "invalid_range", http.StatusBadRequest)
		return
	}

	// changes_only defaults to true (CLI parity — matches http-audit.md).
	if s := q.Get("changes_only"); s != "" {
		switch strings.ToLower(s) {
		case "true", "1":
			filter.ChangesOnly = true
		case "false", "0":
			filter.ChangesOnly = false
		default:
			writeJSONError(w, "invalid_range", http.StatusBadRequest)
			return
		}
	}

	limit := 500
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			writeJSONError(w, "invalid_range", http.StatusBadRequest)
			return
		}
		if n > 5000 {
			n = 5000
		}
		limit = n
	}
	filter.Limit = limit

	// Cursors are bound to the filter set (host, actor, from, to, changes_only).
	// Reusing a cursor from a different filter set is rejected per the contract
	// to prevent silently-skipped pages when a client varies filters mid-walk.
	filterSig := auditFilterSignature(filter)
	if rawCursor := q.Get("cursor"); rawCursor != "" {
		inner, err := unwrapAuditCursor(rawCursor, filterSig)
		if err != nil {
			writeJSONError(w, "invalid_cursor", http.StatusBadRequest)
			return
		}
		filter.Cursor = inner
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	records, nextCursor, err := ds.as.QueryRange(ctx, filter)
	if err != nil {
		if errors.Is(err, telemetry.ErrInvalidCursor) {
			writeJSONError(w, "invalid_cursor", http.StatusBadRequest)
			return
		}
		slog.Error("audit: query failed", "error", err)
		writeJSONError(w, "storage_error", http.StatusInternalServerError)
		return
	}

	type auditJSON struct {
		Ts             time.Time  `json:"ts"`
		Host           string     `json:"host"`
		PrevState      int        `json:"prev_state"`
		PrevStateLabel string     `json:"prev_state_label"`
		NewState       int        `json:"new_state"`
		NewStateLabel  string     `json:"new_state_label"`
		Principal      string     `json:"principal"`
		ChangedBy      string     `json:"changed_by"`
		Reason         string     `json:"reason"`
		KeyModifiedTs  *time.Time `json:"key_modified_ts"`
		Reconciliation bool       `json:"reconciliation"`
		BeforeTs       *time.Time `json:"before_ts,omitempty"`
	}
	out := make([]auditJSON, len(records))
	for i, rec := range records {
		row := auditJSON{
			Ts:             rec.Ts.UTC(),
			Host:           rec.Host,
			PrevState:      rec.PrevState,
			PrevStateLabel: dc.DrainMode(rec.PrevState).String(), //nolint:gosec // PrevState is 0..3 from a trusted source
			NewState:       rec.NewState,
			NewStateLabel:  dc.DrainMode(rec.NewState).String(), //nolint:gosec // NewState is 0..3 from a trusted source
			Principal:      rec.Principal,
			ChangedBy:      rec.ChangedBy,
			Reason:         rec.Reason,
			KeyModifiedTs:  rec.KeyModifiedTs,
			Reconciliation: rec.Reconciliation,
		}
		if rec.Reconciliation {
			row.BeforeTs = rec.BeforeTs
		}
		out[i] = row
	}

	resp := struct {
		Records       []auditJSON `json:"records"`
		NextCursor    *string     `json:"next_cursor"`
		Tier          string      `json:"tier"`
		TotalReturned int         `json:"total_returned"`
	}{
		Records:       out,
		Tier:          "audit",
		TotalReturned: len(out),
	}
	if nextCursor != "" {
		wrapped := wrapAuditCursor(nextCursor, filterSig)
		resp.NextCursor = &wrapped
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// auditCursorEnvelope carries the inner telemetry-store cursor alongside a
// short SHA-256 signature of the filter fields the cursor was produced under.
// Clients that change filters (host, actor, from, to, changes_only) mid-walk
// get `invalid_cursor` instead of silently-skipped pages (contracts/http-audit.md).
type auditCursorEnvelope struct {
	Sig   string `json:"s"` // hex-encoded sha256(filter canonical form)
	Inner string `json:"c"` // opaque cursor from telemetry.AuditStore
}

// auditFilterSignature produces a stable hex digest over the filter dimensions
// that define "the same walk". Limit is intentionally excluded so a client can
// shrink/grow page size without invalidating its cursor.
func auditFilterSignature(f telemetry.QueryFilter) string {
	var from, to int64 = -1, -1
	if f.From != nil {
		from = f.From.UTC().UnixMilli()
	}
	if f.To != nil {
		to = f.To.UTC().UnixMilli()
	}
	canonical := fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%t",
		f.Host, f.Actor, from, to, f.ChangesOnly)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:8]) // 8 bytes is plenty vs collisions by a hostile client
}

// wrapAuditCursor binds a telemetry-store cursor to the current filter signature.
func wrapAuditCursor(inner, sig string) string {
	b, _ := json.Marshal(auditCursorEnvelope{Sig: sig, Inner: inner})
	return base64.URLEncoding.EncodeToString(b)
}

// unwrapAuditCursor decodes a client-supplied cursor and rejects it if the
// signature does not match the current request's filter set.
func unwrapAuditCursor(raw, wantSig string) (string, error) {
	decoded, err := base64.URLEncoding.DecodeString(raw)
	if err != nil {
		return "", fmt.Errorf("audit cursor: decode: %w", err)
	}
	var env auditCursorEnvelope
	if err := json.Unmarshal(decoded, &env); err != nil {
		return "", fmt.Errorf("audit cursor: unmarshal: %w", err)
	}
	if env.Sig != wantSig {
		return "", fmt.Errorf("audit cursor: filter mismatch")
	}
	return env.Inner, nil
}

// mustMarshal marshals v to JSON, returning nil on error (caller checks SSEEvent marshal).
func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// broadcastServerUpdate builds a ServerView for the named host and broadcasts
// it as a server_update SSE event.
func (ds *DashboardServer) broadcastServerUpdate(host string) {
	info := ds.state.Get(host)
	if info == nil {
		return
	}
	view := toServerView(*info)
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
	}{
		Notifications:           redacted,
		SessionWarningThreshold: cfg.SessionWarningThreshold,
		GracePeriod:             cfg.GracePeriod,
		Performance:             cfg.Performance,
		EvtSpike:                evtspikeView{Enabled: cfg.EvtSpike.Enabled},
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

	// Session revalidation: check the session store periodically so that a
	// deleted or expired session terminates the stream within
	// sseSessionCheckInterval rather than persisting until the TCP connection
	// drops. The browser then reconnects and receives a 401 immediately.
	// Skipped when sessionToken is empty (test requests, dev mode).
	sessionCheck := time.NewTicker(sseSessionCheckInterval)
	defer sessionCheck.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-done:
			return
		case <-sessionCheck.C:
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

// handleSwaggerUI serves a minimal HTML page that loads Swagger UI from CDN.
func handleSwaggerUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	// Relaxed CSP for Swagger UI CDN resources.
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; "+
			"script-src 'self' https://unpkg.com 'unsafe-inline'; "+
			"style-src 'self' https://unpkg.com 'unsafe-inline'; "+
			"img-src 'self' data: https://unpkg.com; "+
			"connect-src 'self'; "+
			"font-src https://unpkg.com; "+
			"frame-ancestors 'self'; "+
			"base-uri 'self'")
	_, _ = io.WriteString(w, `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>DrainCtl API — Swagger UI</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    SwaggerUIBundle({
      url: "/api/v1/openapi.yaml",
      dom_id: "#swagger-ui",
      deepLinking: true,
      presets: [SwaggerUIBundle.presets.apis, SwaggerUIBundle.SwaggerUIStandalonePreset],
      layout: "BaseLayout"
    });
  </script>
</body>
</html>`)
}

// checkResultSamples converts the numeric fields of a CheckResult into telemetry
// samples for storage. Only Performance and Sessions fields are extracted; Status,
// DrainMode, and other categorical fields are not stored as metrics.
func checkResultSamples(r dc.CheckResult) []telemetry.Sample {
	ts := r.Timestamp
	host := r.Host

	out := make([]telemetry.Sample, 0, 32)
	add := func(counter string, v float64) {
		out = append(out, telemetry.Sample{Ts: ts, Host: host, Counter: counter, Value: v})
	}

	if p := r.Performance; p != nil {
		add("cpu_pct", p.CPUPct)
		add("cpu_p95_pct", p.CPUP95)
		add("mem_avail_mb", p.MemAvailMB)
		add("mem_total_mb", p.MemTotalMB)
		add("pages_sec", p.PagesSec)
		add("disk_queue", p.DiskQueue)
		add("tcp_retrans_sec", p.TCPRetrans)
		add("input_delay_p50_ms", p.InputDelayP50)
		add("input_delay_p95_ms", p.InputDelayP95)
		add("input_delay_max_ms", p.InputDelayMax)
		if p.SessionCPUP95 != 0 {
			add("session_cpu_p95_pct", p.SessionCPUP95)
		}
		if p.SessionCPUP50 != 0 {
			add("session_cpu_p50_pct", p.SessionCPUP50)
		}
		if p.SessionMemP95 != 0 {
			add("session_mem_p95_bytes", p.SessionMemP95)
		}
		if p.SessionMemP50 != 0 {
			add("session_mem_p50_bytes", p.SessionMemP50)
		}
		if p.RFXAvailable {
			add("rfx_fps_out", p.RFXFPSOut)
			add("rfx_fps_out_p50", p.RFXFPSOutP50)
			add("rfx_skip_server_sec", p.RFXSkipServer)
			add("rfx_skip_net_sec", p.RFXSkipNet)
			add("rfx_encode_ms", p.RFXEncodeMS)
			add("rfx_quality_pct", p.RFXQuality)
			add("rfx_rtt_ms", p.RFXRTT)
			add("rfx_loss_pct", p.RFXLoss)
		}
	}

	if s := r.Sessions; s != nil {
		add("sessions_total", float64(s.TotalSessions))
		add("sessions_active", float64(s.ActiveSessions))
		add("sessions_disconnected", float64(s.DisconnectedSessions))
		add("sessions_max", float64(s.MaxSessions))
	}

	return out
}

// cspBase is the Content-Security-Policy value applied to all responses.
// The Vite-bundled SPA uses only 'self' for scripts — no 'unsafe-inline'.
// handleUI extends cspBase with a per-request nonce for the theme
// flash-prevention inline script in index.html (see handleUI).
// style-src keeps 'unsafe-inline' because Svelte components use inline style=
// attributes for reactive styling (e.g. flex widths, dirty-state tints).
const cspBase = "default-src 'none'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
	"font-src https://fonts.gstatic.com; " +
	"img-src 'self' data:; " +
	"connect-src 'self'; " +
	"frame-ancestors 'self'; " +
	"base-uri 'self'; " +
	"form-action 'self'"

// cspHeader is the CSP applied to every response via securityMiddleware.
// API responses and asset responses use this directly (no inline script).
// handleUI overrides this header with a nonce-augmented version.
const cspHeader = cspBase

// securityMiddleware adds defensive HTTP security headers to all responses.
// Cache-Control is set to no-store by default so that authenticated API
// responses (server list, health data, settings) are never stored in
// browser or intermediate caches. The UI handler overrides this to no-cache,
// and the favicon handler overrides it to public, max-age=86400.
func securityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", cspHeader)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// hstsMiddleware adds Strict-Transport-Security headers to all responses.
func hstsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=63072000") // 2 years
		next.ServeHTTP(w, r)
	})
}
