//go:build windows

package dashboard

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// staleThreshold is the maximum time since a server's last report before it is
// considered offline. Matches the STALE constant (600 000 ms) in the dashboard UI.
const staleThreshold = 10 * time.Minute

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
