//go:build windows

package dashboard

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
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
	Version              string           `json:"version"`
	RegisteredAt         time.Time        `json:"registered_at"`
	LastSeen             time.Time        `json:"last_seen,omitempty"`
	ChangedBy            string           `json:"changed_by,omitempty"`
	GraceDeadline        *time.Time       `json:"grace_deadline"`
	Perf                 *dc.PerfSnapshot `json:"perf"`
}

// HistoryView is the per-entry shape returned by GET /api/v1/history/{host}.
// It normalises CheckResult.Status to the lowercase frontend tokens so the
// Svelte component can use the value directly as a CSS class name.
type HistoryView struct {
	Timestamp            time.Time          `json:"timestamp"`
	Host                 string             `json:"host"`
	Status               string             `json:"status"` // "ok"/"grace"/"alert"/"off"
	DrainMode            string             `json:"drain_mode"`
	StateDurationSeconds *float64           `json:"state_duration_seconds"`
	Transition           bool               `json:"transition"`
	TransitionFrom       string             `json:"transition_from,omitempty"`
	ChangedBy            string             `json:"changed_by,omitempty"`
	Version              string             `json:"version"`
	Message              string             `json:"message"`
	Sessions             *dc.SessionSummary `json:"sessions,omitempty"`
	Performance          *dc.PerfSnapshot   `json:"performance,omitempty"`
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

// toHistoryView converts a CheckResult to the HistoryView with normalised status.
func toHistoryView(r dc.CheckResult) HistoryView {
	return HistoryView{
		Timestamp:            r.Timestamp,
		Host:                 r.Host,
		Status:               statusToken(r.Status),
		DrainMode:            r.DrainModeLabel,
		StateDurationSeconds: r.StateDurationSeconds,
		Transition:           r.Transition,
		TransitionFrom:       r.TransitionFrom,
		ChangedBy:            r.ChangedBy,
		Version:              r.Version,
		Message:              r.Message,
		Sessions:             r.Sessions,
		Performance:          r.Performance,
	}
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

	// testNotifyFunc, if non-nil, is called by handleNotifyTest instead of
	// LoadConfig+SendTestNotification. Used in tests to avoid filesystem access.
	testNotifyFunc func() error

	// testLoadConfigFunc, if non-nil, is called by handleGetSettings instead
	// of dc.LoadConfig. Used in tests to avoid filesystem access.
	testLoadConfigFunc func() (*dc.Config, error)

	// testPutSettingsFunc, if non-nil, is called by handlePutSettings
	// instead of dc.UpdateNotifySettings. Receives the parsed request values;
	// nil notifications means the field was absent from the request body
	// (no-op for that field). nil threshold/gracePeriod mean the fields were absent.
	testPutSettingsFunc func(notifications *[]dc.NotificationTarget, sessionThreshold *int, gracePeriod *int) error
}

// StartDashboard creates the server state, sets up routes, and starts the
// HTTP listener. It returns the ServerState so the service main loop can
// call state.Update() after each check cycle. The server shuts down
// gracefully when ctx is cancelled.
func StartDashboard(ctx context.Context, cfg dc.DashboardConfig, dataDir string) (*ServerState, error) {
	state := NewServerState(dataDir)

	ds := &DashboardServer{
		state:        state,
		cfg:          cfg,
		sessionStore: NewSessionStore(ctx),
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
	mux.Handle("GET /api/v1/history/{host}", rlw(rs(http.HandlerFunc(ds.handleHistory))))
	mux.Handle("GET /api/v1/servers", rlw(rs(http.HandlerFunc(ds.handleServers))))
	mux.Handle("GET /api/v1/servers/{host}", rlw(rs(http.HandlerFunc(ds.handleGetServer))))
	mux.Handle("DELETE /api/v1/servers/{host}", rlw(rs(http.HandlerFunc(ds.handleDeleteServer))))
	mux.Handle("GET /api/v1/settings", rlw(rs(http.HandlerFunc(ds.handleGetSettings))))
	mux.Handle("PUT /api/v1/settings", rlw(rs(http.HandlerFunc(ds.handlePutSettings))))
	mux.Handle("POST /api/v1/notify-test", rlw(rs(http.HandlerFunc(ds.handleNotifyTest))))

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
		if !isAuthorizedForHost(auth, req.Hostname, ds.cfg.Group) {
			slog.Warn("dashboard: register rejected: identity mismatch",
				slog.Int("event_id", dc.EvtAccessDenied), "user", user, "claimed_host", req.Hostname)
			http.Error(w, "identity does not match claimed hostname", http.StatusForbidden)
			return
		}
	}

	ds.state.Register(req.Hostname)
	slog.Info("dashboard=register", slog.Int("event_id", dc.EvtServerRegistered), "host", req.Hostname, "user", user)

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
	if auth != nil && !isAuthorizedForHost(auth, result.Host, ds.cfg.Group) {
		slog.Warn("dashboard: report rejected: identity mismatch",
			slog.Int("event_id", dc.EvtAccessDenied), "user", auth.Username, "claimed_host", result.Host)
		http.Error(w, "identity does not match claimed hostname", http.StatusForbidden)
		return
	}

	ds.state.Update(result.Host, &result)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
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
	slog.Info("dashboard=removed", slog.Int("event_id", dc.EvtServerRemoved), "host", host, "user", user) //nolint:gosec // host is validated by the router pattern

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
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

	notifications := cfg.Notifications
	if notifications == nil {
		notifications = []dc.NotificationTarget{}
	}
	out := struct {
		Notifications           []dc.NotificationTarget `json:"notifications"`
		SessionWarningThreshold int                     `json:"session_warning_threshold"`
		GracePeriod             int                     `json:"grace_period"`
		Performance             dc.PerformanceConfig    `json:"performance"`
	}{
		Notifications:           notifications,
		SessionWarningThreshold: cfg.SessionWarningThreshold,
		GracePeriod:             cfg.GracePeriod,
		Performance:             cfg.Performance,
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

	// Use pointer-to-slice so we can distinguish absent ("don't change") from
	// explicit empty array ("clear all notifications").
	var in struct {
		Notifications           *[]dc.NotificationTarget `json:"notifications"`
		SessionWarningThreshold *int                     `json:"session_warning_threshold,omitempty"`
		GracePeriod             *int                     `json:"grace_period,omitempty"`
		Performance             *dc.PerformanceConfig    `json:"performance,omitempty"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
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

	// Validate notification targets so invalid entries are rejected with a clear
	// 400 instead of being silently stripped by Config.Validate() after save.
	if in.Notifications != nil {
		for i, t := range *in.Notifications {
			if t.Type != "webhook" && t.Type != "ntfy" && t.Type != "email" {
				http.Error(w, fmt.Sprintf("notifications[%d]: unknown type %q (want \"webhook\", \"ntfy\", or \"email\")", i, t.Type), http.StatusBadRequest)
				return
			}
			if t.URL != "" {
				lower := strings.ToLower(t.URL)
				validScheme := strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") ||
					strings.HasPrefix(lower, "smtp://") || strings.HasPrefix(lower, "smtps://")
				if !validScheme {
					http.Error(w, fmt.Sprintf("notifications[%d]: URL must use http, https, smtp, or smtps scheme", i), http.StatusBadRequest)
					return
				}
			}
			for _, tr := range t.Triggers {
				if !dc.ValidTriggers[tr] {
					http.Error(w, fmt.Sprintf("notifications[%d]: unknown trigger %q", i, tr), http.StatusBadRequest)
					return
				}
			}
			if t.RepeatMinutes < 0 || t.RepeatMinutes > dc.MaxRepeatMinutes {
				http.Error(w, fmt.Sprintf("notifications[%d]: repeat_minutes must be 0–%d", i, dc.MaxRepeatMinutes), http.StatusBadRequest)
				return
			}
		}
	}

	if ds.testPutSettingsFunc != nil {
		if err := ds.testPutSettingsFunc(in.Notifications, in.SessionWarningThreshold, in.GracePeriod); err != nil {
			slog.Error("update config failed (test hook)", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		if err := dc.UpdateNotifySettings(in.Notifications, in.SessionWarningThreshold, in.GracePeriod); err != nil {
			slog.Error("update settings failed", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if in.Performance != nil {
			if err := dc.UpdatePerformanceConfig(*in.Performance); err != nil {
				slog.Error("update performance config failed", "error", err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}

	auth := GetAuthInfo(r)
	user := ""
	if auth != nil {
		user = auth.Username
	}
	slog.Info("dashboard=settings-updated", slog.Int("event_id", dc.EvtDashboardConfigChange), "user", user)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
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

	var err error
	if singleTarget != nil {
		slog.Info("dashboard=notify-test-target", "user", user, "type", singleTarget.Type, "url", singleTarget.URL)
		err = dc.SendTestNotification([]dc.NotificationTarget{*singleTarget})
	} else {
		slog.Info("dashboard=notify-test", "user", user)
		// testNotifyFunc can be injected in tests to avoid real config/network I/O.
		fn := ds.testNotifyFunc
		if fn == nil {
			fn = func() error {
				loadFn := ds.testLoadConfigFunc
				if loadFn == nil {
					loadFn = func() (*dc.Config, error) { return dc.LoadConfig() }
				}
				cfg, err := loadFn()
				if err != nil {
					return fmt.Errorf("failed to load config: %w", err)
				}
				return dc.SendTestNotification(cfg.Notifications)
			}
		}
		err = fn()
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
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
// Returns the last N CheckResult records for the named host, newest first.
// Optional query params:
//   - limit:        1–100, default 20
//   - changes_only: "1" or "true" — return only records where Transition=true
//
// Requires group membership. Returns 404 if the host is not registered.
func (ds *DashboardServer) handleHistory(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	if host == "" {
		http.Error(w, "host parameter required", http.StatusBadRequest)
		return
	}

	if !ds.state.IsRegistered(host) {
		http.Error(w, "host not found", http.StatusNotFound)
		return
	}

	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > historyMax {
			http.Error(w, fmt.Sprintf("limit must be 1–%d", historyMax), http.StatusBadRequest)
			return
		}
		limit = n
	}

	changesOnly := false
	if v := r.URL.Query().Get("changes_only"); v == "1" || v == "true" {
		changesOnly = true
	}

	// For changes_only, fetch the full ring so filtering has the full picture.
	// For the plain case, fetch only the requested limit — no need to allocate more.
	fetchN := limit
	if changesOnly {
		fetchN = historyMax
	}
	records := ds.state.HostHistory(host, fetchN)
	if records == nil {
		records = []dc.CheckResult{}
	}

	if changesOnly {
		filtered := records[:0]
		for _, rec := range records {
			if rec.Transition {
				filtered = append(filtered, rec)
			}
		}
		records = filtered
		if limit < len(records) {
			records = records[:limit]
		}
	}

	views := make([]HistoryView, len(records))
	for i, rec := range records {
		views[i] = toHistoryView(rec)
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(views)
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
