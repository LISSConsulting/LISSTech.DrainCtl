//go:build windows

package dashboard

import (
	"context"
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
	state  *ServerState
	cfg    dc.DashboardConfig
	log    dc.LogFunc
	server *http.Server
}

// StartDashboard creates the server state, sets up routes, and starts the
// HTTP listener. It returns the ServerState so the service main loop can
// call state.Update() after each check cycle. The server shuts down
// gracefully when ctx is cancelled.
func StartDashboard(ctx context.Context, cfg dc.DashboardConfig, dataDir string, log dc.LogFunc) (*ServerState, error) {
	if log == nil {
		log = dc.DiscardLogger()
	}

	state := NewServerState(dataDir)

	ds := &DashboardServer{
		state: state,
		cfg:   cfg,
		log:   log,
	}

	mux := http.NewServeMux()

	// Agent routes — any authenticated domain identity.
	mux.Handle("POST /api/v1/register", NegotiateMiddleware(
		http.HandlerFunc(ds.handleRegister), log))
	mux.Handle("POST /api/v1/report", NegotiateMiddleware(
		http.HandlerFunc(ds.handleReport), log))

	// Management / UI routes — require group membership.
	mux.Handle("GET /api/v1/servers", NegotiateMiddleware(
		RequireGroup(cfg.Group, http.HandlerFunc(ds.handleServers), log), log))
	mux.Handle("DELETE /api/v1/servers/{host}", NegotiateMiddleware(
		RequireGroup(cfg.Group, http.HandlerFunc(ds.handleDeleteServer), log), log))
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(faviconPNG)
	})
	mux.Handle("GET /", NegotiateMiddleware(
		RequireGroup(cfg.Group, http.HandlerFunc(ds.handleUI), log), log))

	addr := fmt.Sprintf(":%d", cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dashboard listen %s: %w", addr, err)
	}

	ds.server = &http.Server{
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start serving in background.
	go func() {
		log(dc.LvlINF, "dashboard=listening", fmt.Sprintf("addr=%s", addr))
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

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
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

// handleUI serves the embedded SPA.
func (ds *DashboardServer) handleUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(dashboardHTML)
}
