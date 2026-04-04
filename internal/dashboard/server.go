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
	"net"
	"net/http"
	"strings"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

//go:embed dashboard.html
var dashboardHTML []byte

//go:embed favicon.png
var faviconPNG []byte

// DashboardServer holds the dashboard HTTP server state.
type DashboardServer struct {
	state       *ServerState
	cfg         dc.DashboardConfig
	log         dc.LogFunc
	server      *http.Server
	fingerprint string // SHA-256 fingerprint of the TLS certificate
}

// StartDashboard creates the server state, sets up routes, and starts the
// HTTP listener. It returns the ServerState so the service main loop can
// call state.Update() after each check cycle. The server shuts down
// gracefully when ctx is cancelled.
func StartDashboard(ctx context.Context, cfg dc.DashboardConfig, dataDir string, log dc.LogFunc) (*ServerState, error) {
	if log == nil {
		log = dc.DiscardLogger()
	}

	state := NewServerState(dataDir, log)

	ds := &DashboardServer{
		state: state,
		cfg:   cfg,
		log:   log,
	}

	mux := http.NewServeMux()

	// wrapAuth/wrapGroup are determined at compile time via build tags.
	// Production builds (default) use SSPI Negotiate middleware.
	// Dev builds (-tags devmode) bypass auth entirely.
	wa := func(h http.Handler) http.Handler { return wrapAuth(h, cfg.Group, log) }
	wg := func(h http.Handler) http.Handler { return wrapGroup(h, cfg.Group, log) }

	// Agent routes — any authenticated domain identity.
	mux.Handle("POST /api/v1/register", wa(http.HandlerFunc(ds.handleRegister)))
	mux.Handle("POST /api/v1/report", wa(http.HandlerFunc(ds.handleReport)))

	// Management / UI routes — require group membership.
	mux.Handle("GET /api/v1/servers", wg(http.HandlerFunc(ds.handleServers)))
	mux.Handle("DELETE /api/v1/servers/{host}", wg(http.HandlerFunc(ds.handleDeleteServer)))
	mux.Handle("GET /api/v1/notify-config", wg(http.HandlerFunc(ds.handleGetNotifyConfig)))
	mux.Handle("PUT /api/v1/notify-config", wg(http.HandlerFunc(ds.handlePutNotifyConfig)))
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(faviconPNG)
	})
	mux.Handle("GET /", wg(http.HandlerFunc(ds.handleUI)))

	addr := fmt.Sprintf(":%d", cfg.Port)

	// Resolve TLS configuration.
	var tlsCfg *tls.Config
	tlsCfg, err := loadOrGenerateTLS(cfg.TLSCert, cfg.TLSKey, dataDir, log)
	if err != nil {
		dc.LogMsg(log, dc.LvlWRN, "dashboard TLS setup failed, falling back to HTTP", fmt.Sprintf("error=%q", err))
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

	// Wrap handler with HSTS header when TLS is active.
	var handler http.Handler = mux
	if tlsCfg != nil {
		handler = hstsMiddleware(mux)
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
		log(dc.LvlINF, "dashboard=listening", fmt.Sprintf("addr=%s scheme=%s", addr, scheme))
		if err := ds.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			dc.LogMsg(log, dc.LvlERR, "dashboard server error", fmt.Sprintf("error=%q", err))
		}
	}()

	// Graceful shutdown on context cancellation.
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := ds.server.Shutdown(shutCtx); err != nil {
			dc.LogMsg(log, dc.LvlWRN, "dashboard shutdown error", fmt.Sprintf("error=%q", err))
		}
		log(dc.LvlINF, "dashboard=stopped")
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

	ds.state.Register(req.Hostname)

	auth := GetAuthInfo(r)
	user := ""
	if auth != nil {
		user = auth.Username
	}
	ds.log(dc.LvlINF, "dashboard=register",
		fmt.Sprintf("host=%s user=%s", req.Hostname, user))

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

// handleDeleteServer processes DELETE /api/v1/servers/{host}.
func (ds *DashboardServer) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	if host == "" {
		// Fallback: parse from URL path for compatibility.
		parts := strings.Split(strings.TrimSuffix(r.URL.Path, "/"), "/")
		if len(parts) > 0 {
			host = parts[len(parts)-1]
		}
	}
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
	ds.log(dc.LvlINF, "dashboard=removed",
		fmt.Sprintf("host=%s user=%s", host, user))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// handleGetNotifyConfig returns the current notification config as JSON.
func (ds *DashboardServer) handleGetNotifyConfig(w http.ResponseWriter, _ *http.Request) {
	cfg, err := dc.LoadConfig(ds.log)
	if err != nil {
		dc.LogMsg(ds.log, dc.LvlERR, "load config failed", fmt.Sprintf("error=%q", err))
		http.Error(w, "failed to load config", http.StatusInternalServerError)
		return
	}

	out := struct {
		Notifications           []dc.NotificationTarget `json:"notifications"`
		SessionWarningThreshold int                     `json:"session_warning_threshold"`
		GracePeriod             int                     `json:"grace_period"`
	}{
		Notifications:           cfg.Notifications,
		SessionWarningThreshold: cfg.SessionWarningThreshold,
		GracePeriod:             cfg.GracePeriod,
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

// handlePutNotifyConfig accepts JSON and updates notification config via scoped updaters.
func (ds *DashboardServer) handlePutNotifyConfig(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8192))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	var in struct {
		Notifications           []dc.NotificationTarget `json:"notifications"`
		SessionWarningThreshold *int                    `json:"session_warning_threshold,omitempty"`
		GracePeriod             *int                    `json:"grace_period,omitempty"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if err := dc.UpdateNotifications(in.Notifications, ds.log); err != nil {
		dc.LogMsg(ds.log, dc.LvlERR, "update notifications failed", fmt.Sprintf("error=%q", err))
		http.Error(w, "failed to update notifications", http.StatusInternalServerError)
		return
	}

	if in.SessionWarningThreshold != nil {
		if err := dc.UpdateSessionThreshold(*in.SessionWarningThreshold, ds.log); err != nil {
			dc.LogMsg(ds.log, dc.LvlERR, "update session threshold failed", fmt.Sprintf("error=%q", err))
			http.Error(w, "failed to update session threshold", http.StatusInternalServerError)
			return
		}
	}

	if in.GracePeriod != nil {
		if err := dc.UpdateGracePeriod(*in.GracePeriod, ds.log); err != nil {
			dc.LogMsg(ds.log, dc.LvlERR, "update grace period failed", fmt.Sprintf("error=%q", err))
			http.Error(w, "failed to update grace period", http.StatusInternalServerError)
			return
		}
	}

	auth := GetAuthInfo(r)
	user := ""
	if auth != nil {
		user = auth.Username
	}
	ds.log(dc.LvlINF, "dashboard=notify-config-updated", fmt.Sprintf("user=%s", user))

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

// hstsMiddleware adds Strict-Transport-Security headers to all responses.
func hstsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=63072000") // 2 years
		next.ServeHTTP(w, r)
	})
}
