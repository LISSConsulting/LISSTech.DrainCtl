//go:build windows

package dashboard

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
)

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
	ms           metricsReader
	as           auditReader
	mnt          maintenanceReader

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
	spikes eventSpikeReader

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
func StartDashboard(ctx context.Context, cfg dc.DashboardConfig, dataDir string, ms metricsReader, as auditReader, mnt maintenanceReader, srv serverReader, sps eventSpikeReader) (*ServerState, error) {
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

	ds.wireServerStateCallbacks()

	mux := http.NewServeMux()
	registerRoutes(ctx, ds, mux)

	tlsCfg, fingerprint, err := setupTLS(cfg, dataDir)
	if err != nil {
		return nil, err
	}
	ds.fingerprint = fingerprint

	addr := fmt.Sprintf(":%d", cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dashboard listen %s: %w", addr, err)
	}
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

// wireServerStateCallbacks installs the SSE/metrics/spike callbacks that bridge
// the ServerState event surface to DashboardServer's broker and stores.
func (ds *DashboardServer) wireServerStateCallbacks() {
	// Snapshot fields into locals so closures below capture fixed pointers,
	// matching the original parameter-capture semantics of the free-function form.
	state := ds.state
	ms := ds.ms

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
}

// setupTLS resolves the dashboard's TLS configuration and returns the
// SHA-256 fingerprint of the leaf cert (empty when TLS is not configured).
func setupTLS(cfg dc.DashboardConfig, dataDir string) (*tls.Config, string, error) {
	tlsCfg, err := loadOrGenerateTLS(cfg.TLSCert, cfg.TLSKey, dataDir)
	if err != nil {
		return nil, "", fmt.Errorf("dashboard TLS setup failed: %w", err)
	}
	var fp string
	if tlsCfg != nil && len(tlsCfg.Certificates) > 0 {
		if leaf, parseErr := x509.ParseCertificate(tlsCfg.Certificates[0].Certificate[0]); parseErr == nil {
			fp = certFingerprint(leaf)
		}
	}
	return tlsCfg, fp, nil
}

// registerRoutes wires every HTTP route on mux. Public, agent (SSPI machine
// account), auth, session-protected UI, and static-asset routes are grouped
// in that order to mirror the auth model.
func registerRoutes(ctx context.Context, ds *DashboardServer, mux *http.ServeMux) {
	cfg := ds.cfg

	// Per-IP rate limiter: 10 req/s sustained, burst 60.
	// Generous enough for normal agent reporting and browser use;
	// prevents runaway scripts from hammering the public health endpoint.
	rl := newIPRateLimiter(10, 60)

	// Stricter per-IP rate limiter for credential endpoints: 5 req/min sustained,
	// burst 3. Limits brute-force to ~300 attempts/hour per IP while allowing
	// normal use (a human retrying bad credentials a handful of times).
	authRL := newIPRateLimiter(float64(5)/60, 3)

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
