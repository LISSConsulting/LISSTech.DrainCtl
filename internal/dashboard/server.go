//go:build windows

package dashboard

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
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

//go:embed dashboard.html
var dashboardHTML []byte

//go:embed favicon.png
var faviconPNG []byte

// DashboardServer holds the dashboard HTTP server state.
type DashboardServer struct {
	state       *ServerState
	cfg         dc.DashboardConfig
	server      *http.Server
	fingerprint string // SHA-256 fingerprint of the TLS certificate

	// testNotifyFunc, if non-nil, is called by handleNotifyTest instead of
	// LoadConfig+SendTestNotification. Used in tests to avoid filesystem access.
	testNotifyFunc func() error

	// testLoadConfigFunc, if non-nil, is called by handleGetNotifyConfig instead
	// of dc.LoadConfig. Used in tests to avoid filesystem access.
	testLoadConfigFunc func() (*dc.Config, error)

	// testPutNotifyConfigFunc, if non-nil, is called by handlePutNotifyConfig
	// instead of dc.UpdateNotifySettings. Receives the parsed request values;
	// nil notifications means the field was absent from the request body
	// (no-op for that field). nil threshold/gracePeriod mean the fields were absent.
	testPutNotifyConfigFunc func(notifications *[]dc.NotificationTarget, sessionThreshold *int, gracePeriod *int) error
}

// StartDashboard creates the server state, sets up routes, and starts the
// HTTP listener. It returns the ServerState so the service main loop can
// call state.Update() after each check cycle. The server shuts down
// gracefully when ctx is cancelled.
func StartDashboard(ctx context.Context, cfg dc.DashboardConfig, dataDir string) (*ServerState, error) {
	state := NewServerState(dataDir)

	ds := &DashboardServer{
		state: state,
		cfg:   cfg,
	}

	// Per-IP rate limiter: 10 req/s sustained, burst 60.
	// Generous enough for normal agent reporting and browser use;
	// prevents runaway scripts from hammering the public health endpoint.
	rl := newIPRateLimiter(10, 60)

	mux := http.NewServeMux()

	// wrapAuth/wrapGroup are determined at compile time via build tags.
	// Production builds (default) use SSPI Negotiate middleware.
	// Dev builds (-tags devmode) bypass auth entirely.
	wa := func(h http.Handler) http.Handler { return wrapAuth(ctx, h, cfg.Group) }
	wg := func(h http.Handler) http.Handler { return wrapGroup(ctx, h, cfg.Group) }

	rlw := func(h http.Handler) http.Handler { return rateLimitMiddleware(rl, h) }

	// Public routes — no authentication required.
	mux.Handle("GET /api/v1/health", rlw(http.HandlerFunc(ds.handleHealth)))

	// Agent routes — any authenticated domain identity.
	mux.Handle("POST /api/v1/register", rlw(wa(http.HandlerFunc(ds.handleRegister))))
	mux.Handle("POST /api/v1/report", rlw(wa(http.HandlerFunc(ds.handleReport))))

	// Management / UI routes — require group membership.
	mux.Handle("GET /api/v1/history/{host}", rlw(wg(http.HandlerFunc(ds.handleHistory))))
	mux.Handle("GET /api/v1/servers", rlw(wg(http.HandlerFunc(ds.handleServers))))
	mux.Handle("GET /api/v1/servers/{host}", rlw(wg(http.HandlerFunc(ds.handleGetServer))))
	mux.Handle("DELETE /api/v1/servers/{host}", rlw(wg(http.HandlerFunc(ds.handleDeleteServer))))
	mux.Handle("GET /api/v1/notify-config", rlw(wa(http.HandlerFunc(ds.handleGetNotifyConfig))))
	mux.Handle("PUT /api/v1/notify-config", rlw(wg(http.HandlerFunc(ds.handlePutNotifyConfig))))
	mux.Handle("POST /api/v1/notify-test", rlw(wg(http.HandlerFunc(ds.handleNotifyTest))))
	mux.Handle("GET /favicon.ico", rlw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(faviconPNG)
	})))
	registerMockRoute(mux) // no-op in production; serves /mock.js in devmode builds
	mux.Handle("GET /", rlw(wg(http.HandlerFunc(ds.handleUI))))

	addr := fmt.Sprintf(":%d", cfg.Port)

	// Resolve TLS configuration.
	var tlsCfg *tls.Config
	tlsCfg, err := loadOrGenerateTLS(cfg.TLSCert, cfg.TLSKey, dataDir)
	if err != nil {
		slog.Warn("dashboard TLS setup failed, falling back to HTTP", "error", err)
		tlsCfg = nil
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
				"user", user, "claimed_host", req.Hostname)
			http.Error(w, "identity does not match claimed hostname", http.StatusForbidden)
			return
		}
	}

	ds.state.Register(req.Hostname)
	slog.Info("dashboard=register", "host", req.Hostname, "user", user)

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
			"user", auth.Username, "claimed_host", result.Host)
		http.Error(w, "identity does not match claimed hostname", http.StatusForbidden)
		return
	}

	ds.state.Update(result.Host, &result)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// handleServers returns GET /api/v1/servers as a JSON array.
func (ds *DashboardServer) handleServers(w http.ResponseWriter, r *http.Request) {
	servers := ds.state.All()
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(servers)
}

// handleGetServer returns GET /api/v1/servers/{host} as a JSON object.
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
	_ = enc.Encode(info)
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
	slog.Info("dashboard=removed", "host", host, "user", user) //nolint:gosec // host is validated by the router pattern

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// handleGetNotifyConfig returns the current notification config as JSON.
func (ds *DashboardServer) handleGetNotifyConfig(w http.ResponseWriter, _ *http.Request) {
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

// handlePutNotifyConfig accepts JSON and updates notification config atomically.
// All provided fields are written in a single config load+save cycle.
// Absent fields (not present in the JSON body) are left unchanged; to clear
// notifications send "notifications": [].
func (ds *DashboardServer) handlePutNotifyConfig(w http.ResponseWriter, r *http.Request) {
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

	if ds.testPutNotifyConfigFunc != nil {
		if err := ds.testPutNotifyConfigFunc(in.Notifications, in.SessionWarningThreshold, in.GracePeriod); err != nil {
			slog.Error("update config failed (test hook)", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		if err := dc.UpdateNotifySettings(in.Notifications, in.SessionWarningThreshold, in.GracePeriod); err != nil {
			slog.Error("update notify settings failed", "error", err)
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
	slog.Info("dashboard=notify-config-updated", "user", user)

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
	var singleTarget *dc.NotificationTarget
	if r.Body != nil && r.ContentLength > 0 {
		var t dc.NotificationTarget
		if err := json.NewDecoder(r.Body).Decode(&t); err == nil && t.URL != "" {
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

// handleUI serves the embedded SPA.
func (ds *DashboardServer) handleUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(dashboardHTML)
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

	healthy, grace, alerting, unknown, offline := 0, 0, 0, 0, 0
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
		Grace    int    `json:"grace"`
		Alerting int    `json:"alerting"`
		Offline  int    `json:"offline"`
		Unknown  int    `json:"unknown"`
	}{
		OK:       true,
		Version:  dc.Version,
		Servers:  len(servers),
		Healthy:  healthy,
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

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(records)
}

// cspHeader is the Content-Security-Policy value applied to all responses.
// The dashboard SPA uses inline scripts and styles (uPlot + app JS) and loads
// fonts from Google Fonts CDN, so 'unsafe-inline' is required for script-src
// and style-src. All other sources are restricted to 'self'.
const cspHeader = "default-src 'none'; " +
	"script-src 'unsafe-inline'; " +
	"style-src 'unsafe-inline' https://fonts.googleapis.com; " +
	"font-src https://fonts.gstatic.com; " +
	"img-src 'self' data:; " +
	"connect-src 'self'; " +
	"frame-ancestors 'self'; " +
	"base-uri 'self'; " +
	"form-action 'self'"

// securityMiddleware adds defensive HTTP security headers to all responses.
// Cache-Control is set to no-store by default so that authenticated API
// responses (server list, health data, notify config) are never stored in
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
