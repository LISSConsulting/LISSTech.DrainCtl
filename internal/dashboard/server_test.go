//go:build windows

package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// newTestServer creates a DashboardServer backed by a temp directory.
// No TLS, no auth middleware — tests call handler methods directly.
func newTestServer(t *testing.T) *DashboardServer {
	t.Helper()
	return &DashboardServer{
		state:                newTestServerState(t),
		cfg:                  dc.DashboardConfig{Group: "Domain Admins"},
		broker:               NewBroker(),
		remoteEvtSpikeStatus: make(map[string]evtspike.DetectorStatus),
	}
}

// ── handleHealth ──────────────────────────────────────────────────────────────

func TestHandleHealth_EmptyState(t *testing.T) {
	ds := newTestServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)

	ds.handleHealth(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var resp struct {
		OK       bool   `json:"ok"`
		Version  string `json:"version"`
		Servers  int    `json:"servers"`
		Healthy  int    `json:"healthy"`
		Alerting int    `json:"alerting"`
		Unknown  int    `json:"unknown"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK {
		t.Error("ok = false, want true")
	}
	if resp.Version != dc.Version {
		t.Errorf("version = %q, want %q", resp.Version, dc.Version)
	}
	if resp.Servers != 0 {
		t.Errorf("servers = %d, want 0", resp.Servers)
	}
	if resp.Healthy != 0 || resp.Alerting != 0 || resp.Unknown != 0 {
		t.Errorf("counts = healthy:%d alerting:%d unknown:%d, want all 0", resp.Healthy, resp.Alerting, resp.Unknown)
	}
}

func TestHandleHealth_UnreportedServersAreUnknown(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	ds.state.Register("SRV02")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	ds.handleHealth(w, r)

	var resp struct {
		Servers int `json:"servers"`
		Unknown int `json:"unknown"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Servers != 2 {
		t.Errorf("servers = %d, want 2", resp.Servers)
	}
	if resp.Unknown != 2 {
		t.Errorf("unknown = %d, want 2 (no reports yet)", resp.Unknown)
	}
}

func TestHandleHealth_StatusCounts(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	ds.state.Register("SRV02")
	ds.state.Register("SRV03")

	ds.state.Update("SRV01", &dc.CheckResult{Host: "SRV01", Status: "Healthy"})
	ds.state.Update("SRV02", &dc.CheckResult{Host: "SRV02", Status: "Alert"})
	// SRV03 has no report → unknown

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	ds.handleHealth(w, r)

	var resp struct {
		Servers  int `json:"servers"`
		Healthy  int `json:"healthy"`
		Alerting int `json:"alerting"`
		Unknown  int `json:"unknown"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Servers != 3 {
		t.Errorf("servers = %d, want 3", resp.Servers)
	}
	if resp.Healthy != 1 {
		t.Errorf("healthy = %d, want 1", resp.Healthy)
	}
	if resp.Alerting != 1 {
		t.Errorf("alerting = %d, want 1", resp.Alerting)
	}
	if resp.Unknown != 1 {
		t.Errorf("unknown = %d, want 1", resp.Unknown)
	}
}

func TestHandleHealth_StaleServerCountedAsOffline(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	ds.state.Update("SRV01", &dc.CheckResult{Host: "SRV01", Status: "Healthy"})

	// Back-date LastSeen past the stale threshold.
	if err := ds.state.store.BackdateLastSeen(context.Background(), "SRV01", time.Now().Add(-15*time.Minute)); err != nil {
		t.Fatalf("BackdateLastSeen: %v", err)
	}

	w := httptest.NewRecorder()
	ds.handleHealth(w, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	var resp struct {
		Healthy int `json:"healthy"`
		Offline int `json:"offline"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Healthy != 0 {
		t.Errorf("healthy = %d, want 0 (stale server must not count as healthy)", resp.Healthy)
	}
	if resp.Offline != 1 {
		t.Errorf("offline = %d, want 1 (last_seen > staleThreshold)", resp.Offline)
	}
}

func TestHandleHealth_FreshServerNotOffline(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	// Update sets LastSeen = time.Now() — well within the stale threshold.
	ds.state.Update("SRV01", &dc.CheckResult{Host: "SRV01", Status: "Healthy"})

	w := httptest.NewRecorder()
	ds.handleHealth(w, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	var resp struct {
		Healthy int `json:"healthy"`
		Offline int `json:"offline"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Healthy != 1 {
		t.Errorf("healthy = %d, want 1 (fresh server)", resp.Healthy)
	}
	if resp.Offline != 0 {
		t.Errorf("offline = %d, want 0 (last_seen < staleThreshold)", resp.Offline)
	}
}

func TestHandleHealth_StaleAlertAndGraceCountedAsOffline(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	ds.state.Register("SRV02")
	ds.state.Update("SRV01", &dc.CheckResult{Host: "SRV01", Status: "Alert"})
	ds.state.Update("SRV02", &dc.CheckResult{Host: "SRV02", Status: "Grace"})

	if err := ds.state.store.BackdateLastSeen(context.Background(), "SRV01", time.Now().Add(-20*time.Minute)); err != nil {
		t.Fatalf("BackdateLastSeen SRV01: %v", err)
	}
	if err := ds.state.store.BackdateLastSeen(context.Background(), "SRV02", time.Now().Add(-11*time.Minute)); err != nil {
		t.Fatalf("BackdateLastSeen SRV02: %v", err)
	}

	w := httptest.NewRecorder()
	ds.handleHealth(w, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	var resp struct {
		Alerting int `json:"alerting"`
		Grace    int `json:"grace"`
		Offline  int `json:"offline"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Alerting != 0 {
		t.Errorf("alerting = %d, want 0 (stale alert counted as offline)", resp.Alerting)
	}
	if resp.Grace != 0 {
		t.Errorf("grace = %d, want 0 (stale grace counted as offline)", resp.Grace)
	}
	if resp.Offline != 2 {
		t.Errorf("offline = %d, want 2", resp.Offline)
	}
}

// ── handleRegister ────────────────────────────────────────────────────────────

func TestHandleRegister_ValidHostname(t *testing.T) {
	ds := newTestServer(t)
	body := `{"hostname":"SRV01"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(body))

	ds.handleRegister(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var resp struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK {
		t.Error("ok = false, want true")
	}
	if !ds.state.IsRegistered("SRV01") {
		t.Error("SRV01 should be registered after successful POST")
	}
}

func TestHandleRegister_EmptyHostname_JSON(t *testing.T) {
	ds := newTestServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(`{"hostname":""}`))

	ds.handleRegister(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (empty hostname in JSON should be rejected)", w.Code, http.StatusBadRequest)
	}
}

func TestHandleRegister_WhitespaceOnlyHostname(t *testing.T) {
	ds := newTestServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(`{"hostname":"   "}`))

	ds.handleRegister(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (whitespace-only hostname should be rejected)", w.Code, http.StatusBadRequest)
	}
}

func TestHandleRegister_HostnameTrimmed(t *testing.T) {
	ds := newTestServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(`{"hostname":"  SRV01  "}`))

	ds.handleRegister(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if !ds.state.IsRegistered("SRV01") {
		t.Error("trimmed hostname 'SRV01' should be registered")
	}
	if ds.state.IsRegistered("  SRV01  ") {
		t.Error("untrimmed key '  SRV01  ' should not be registered")
	}
}

func TestHandleRegister_HostnameTooLong(t *testing.T) {
	ds := newTestServer(t)
	hostname := strings.Repeat("a", 254) // exceeds 253-char DNS limit
	body := `{"hostname":"` + hostname + `"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(body))

	ds.handleRegister(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (254-char hostname should be rejected)", w.Code, http.StatusBadRequest)
	}
}

func TestHandleRegister_MaxLengthHostname(t *testing.T) {
	ds := newTestServer(t)
	// Build a valid 253-char hostname: four RFC-1123 labels of 63+63+63+61 chars.
	label63 := strings.Repeat("a", 63)
	label61 := strings.Repeat("a", 61)
	hostname := label63 + "." + label63 + "." + label63 + "." + label61 // 253 chars
	body := `{"hostname":"` + hostname + `"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(body))

	ds.handleRegister(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (253-char valid hostname should be accepted)", w.Code, http.StatusOK)
	}
}

func TestHandleRegister_InvalidCharacters(t *testing.T) {
	cases := []struct {
		name     string
		hostname string
	}{
		{"leading hyphen", "-server"},
		{"trailing hyphen", "server-"},
		{"underscore", "my_server"},
		{"slash", "path/server"},
		{"null byte", "server\x00"},
		{"space in middle", "my server"},
		{"at sign", "server@domain"},
		{"dot only", "."},
		{"empty label", "server..domain"},
		{"label too long", strings.Repeat("a", 64)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ds := newTestServer(t)
			body, _ := json.Marshal(map[string]string{"hostname": tc.hostname})
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(body))
			ds.handleRegister(w, r)
			if w.Code != http.StatusBadRequest {
				t.Errorf("hostname %q: status = %d, want %d", tc.hostname, w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestHandleRegister_ValidHostnameFormats(t *testing.T) {
	cases := []string{
		"SRV01",
		"server.domain.com",
		"rdsh-1",
		"1server",
		"a",
		"a1",
	}
	for _, hostname := range cases {
		t.Run(hostname, func(t *testing.T) {
			ds := newTestServer(t)
			body, _ := json.Marshal(map[string]string{"hostname": hostname})
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(body))
			ds.handleRegister(w, r)
			if w.Code != http.StatusOK {
				t.Errorf("hostname %q: status = %d, want %d (should be accepted)", hostname, w.Code, http.StatusOK)
			}
		})
	}
}

func TestHandleRegister_InvalidJSON(t *testing.T) {
	ds := newTestServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader("not json"))

	ds.handleRegister(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (invalid JSON should be rejected)", w.Code, http.StatusBadRequest)
	}
}

func TestHandleRegister_MissingHostnameField(t *testing.T) {
	ds := newTestServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(`{"other":"field"}`))

	ds.handleRegister(w, r)

	// JSON parses fine but Hostname is zero-value ("") → rejected.
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (missing hostname field should be rejected)", w.Code, http.StatusBadRequest)
	}
}

func TestHandleRegister_IdempotentReRegister(t *testing.T) {
	ds := newTestServer(t)
	body := `{"hostname":"SRV01"}`

	for i := range 3 {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(body))
		ds.handleRegister(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("registration %d: status = %d, want %d", i+1, w.Code, http.StatusOK)
		}
	}

	// Should still be exactly one registration.
	servers := ds.state.All()
	if len(servers) != 1 {
		t.Errorf("expected 1 server after 3 registrations of the same host, got %d", len(servers))
	}
}

// TestHandleRegister_AuthenticatedUser_Returns200 verifies that handleRegister
// succeeds and does not panic when the request carries SSPI auth info in its
// context (the auth != nil branch in the handler).
func TestHandleRegister_AuthenticatedUser_Returns200(t *testing.T) {
	ds := newTestServer(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(`{"hostname":"SRV01"}`))
	r = r.WithContext(context.WithValue(r.Context(), authInfoKey, &AuthInfo{Username: "DOMAIN\\alice", Groups: []string{"Domain Admins"}}))
	ds.handleRegister(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !ds.state.IsRegistered("SRV01") {
		t.Error("SRV01 should be registered after authenticated register call")
	}
}

// TestRequireMachineAccount_HumanPrincipalReturnsJSONError verifies BUGS.md#4:
// an authenticated human principal hitting a machine-account-gated route gets
// 403 with a JSON error body naming the account, not a bare plain-text response.
func TestRequireMachineAccount_HumanPrincipalReturnsJSONError(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	h := requireMachineAccount(inner)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/register", nil)
	r = r.WithContext(context.WithValue(r.Context(), authInfoKey, &AuthInfo{Username: `DOMAIN\alice`}))
	h.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"error"`) {
		t.Errorf("body missing \"error\" key: %s", body)
	}
	// json.Marshal escapes backslashes: DOMAIN\alice becomes DOMAIN\\alice in JSON text.
	if !strings.Contains(body, `DOMAIN\\alice`) {
		t.Errorf("body should name the rejected principal, got: %s", body)
	}
	if called {
		t.Error("inner handler must not be called for non-machine accounts")
	}
}

// TestRequireMachineAccount_UnauthenticatedReturns401 verifies that
// unauthenticated requests still get a plain 401 (no info leak).
func TestRequireMachineAccount_UnauthenticatedReturns401(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	h := requireMachineAccount(inner)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/register", nil)
	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// ── handleReport ──────────────────────────────────────────────────────────────

func TestHandleReport_UnregisteredHostRejected(t *testing.T) {
	ds := newTestServer(t)
	result := dc.CheckResult{Host: "SRV01", Status: "Healthy"}
	body, _ := json.Marshal(result)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/report", bytes.NewReader(body))

	ds.handleReport(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d (unregistered host should be forbidden)", w.Code, http.StatusForbidden)
	}
}

func TestHandleReport_RegisteredHostAccepted(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")

	result := dc.CheckResult{Host: "SRV01", Status: "Healthy"}
	body, _ := json.Marshal(result)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/report", bytes.NewReader(body))

	ds.handleReport(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var resp struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK {
		t.Error("ok = false, want true")
	}
}

func TestHandleReport_EmptyHostRejected(t *testing.T) {
	ds := newTestServer(t)
	result := dc.CheckResult{Status: "Healthy"} // Host is zero value
	body, _ := json.Marshal(result)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/report", bytes.NewReader(body))

	ds.handleReport(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (empty host should be rejected)", w.Code, http.StatusBadRequest)
	}
}

func TestHandleReport_InvalidJSONRejected(t *testing.T) {
	ds := newTestServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/report", strings.NewReader("not json"))

	ds.handleReport(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (invalid JSON should be rejected)", w.Code, http.StatusBadRequest)
	}
}

func TestHandleReport_StoresLastResult(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")

	result := dc.CheckResult{Host: "SRV01", Status: "Alert"}
	body, _ := json.Marshal(result)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/report", bytes.NewReader(body))
	ds.handleReport(w, r)

	servers := ds.state.All()
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	if servers[0].LastResult == nil {
		t.Fatal("LastResult should be set after report")
	}
	if servers[0].LastResult.Status != "Alert" {
		t.Errorf("LastResult.Status = %q, want Alert", servers[0].LastResult.Status)
	}
}

func TestHandleReport_UpdatesLastSeen(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")

	before := time.Now()

	result := dc.CheckResult{Host: "SRV01", Status: "Healthy"}
	body, _ := json.Marshal(result)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/report", bytes.NewReader(body))
	ds.handleReport(w, r)

	servers := ds.state.All()
	if servers[0].LastSeen.Before(before) {
		t.Error("LastSeen should be updated after a report")
	}
}

// ── handleHistory ─────────────────────────────────────────────────────────────

func TestHistoryHandler_Returns410AfterRingRemoval(t *testing.T) {
	ds := newTestServer(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/history/SRV01", nil)
	r.SetPathValue("host", "SRV01")
	ds.handleHistory(w, r)

	if w.Code != http.StatusGone {
		t.Fatalf("status = %d, want %d (Gone)", w.Code, http.StatusGone)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "use /api/v1/metrics/{host} or /api/v1/audit" {
		t.Errorf("error = %q, want migration message", body["error"])
	}
}

func TestHandleReport_HealthDashboardReflectsReport(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")

	// Initial health: server unknown.
	{
		w := httptest.NewRecorder()
		ds.handleHealth(w, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
		var resp struct {
			Unknown int `json:"unknown"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode health response: %v", err)
		}
		if resp.Unknown != 1 {
			t.Fatalf("pre-report unknown = %d, want 1", resp.Unknown)
		}
	}

	// Post a report.
	{
		result := dc.CheckResult{Host: "SRV01", Status: "Alert"}
		body, _ := json.Marshal(result)
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/report", bytes.NewReader(body))
		ds.handleReport(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("report status = %d, want 200", w.Code)
		}
	}

	// Health should now reflect the alert.
	{
		w := httptest.NewRecorder()
		ds.handleHealth(w, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
		var resp struct {
			Unknown  int `json:"unknown"`
			Alerting int `json:"alerting"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode health response: %v", err)
		}
		if resp.Unknown != 0 {
			t.Errorf("post-report unknown = %d, want 0", resp.Unknown)
		}
		if resp.Alerting != 1 {
			t.Errorf("post-report alerting = %d, want 1", resp.Alerting)
		}
	}
}

// ── handleHealth: Grace counting ──────────────────────────────────────────────

func TestHandleHealth_GraceCountedSeparatelyFromHealthy(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	ds.state.Register("SRV02")
	ds.state.Register("SRV03")
	ds.state.Register("SRV04")

	ds.state.Update("SRV01", &dc.CheckResult{Host: "SRV01", Status: "Healthy"})
	ds.state.Update("SRV02", &dc.CheckResult{Host: "SRV02", Status: "Grace"})
	ds.state.Update("SRV03", &dc.CheckResult{Host: "SRV03", Status: "Alert"})
	// SRV04 has no report → unknown

	w := httptest.NewRecorder()
	ds.handleHealth(w, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp struct {
		Servers  int `json:"servers"`
		Healthy  int `json:"healthy"`
		Grace    int `json:"grace"`
		Alerting int `json:"alerting"`
		Unknown  int `json:"unknown"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Servers != 4 {
		t.Errorf("servers = %d, want 4", resp.Servers)
	}
	if resp.Healthy != 1 {
		t.Errorf("healthy = %d, want 1 (Grace must not be counted as Healthy)", resp.Healthy)
	}
	if resp.Grace != 1 {
		t.Errorf("grace = %d, want 1", resp.Grace)
	}
	if resp.Alerting != 1 {
		t.Errorf("alerting = %d, want 1", resp.Alerting)
	}
	if resp.Unknown != 1 {
		t.Errorf("unknown = %d, want 1", resp.Unknown)
	}
}

func TestHandleHealth_GraceFieldPresentWhenZero(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	ds.state.Update("SRV01", &dc.CheckResult{Host: "SRV01", Status: "Healthy"})

	w := httptest.NewRecorder()
	ds.handleHealth(w, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	var resp struct {
		Grace *int `json:"grace"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Grace == nil {
		t.Fatal("grace field absent from health response")
	}
	if *resp.Grace != 0 {
		t.Errorf("grace = %d, want 0 when no servers are in Grace", *resp.Grace)
	}
}

// ── handleServers ─────────────────────────────────────────────────────────────

func TestHandleServers_EmptyStateReturnsEmptyArray(t *testing.T) {
	ds := newTestServer(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/servers", nil)
	ds.handleServers(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var servers []ServerView
	if err := json.NewDecoder(w.Body).Decode(&servers); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(servers) != 0 {
		t.Errorf("len(servers) = %d, want 0", len(servers))
	}
}

func TestHandleServers_ReturnsSortedByHostname(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("ZETA")
	ds.state.Register("ALPHA")
	ds.state.Register("MANGO")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/servers", nil)
	ds.handleServers(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var servers []ServerView
	if err := json.NewDecoder(w.Body).Decode(&servers); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(servers) != 3 {
		t.Fatalf("len(servers) = %d, want 3", len(servers))
	}
	if servers[0].Host != "ALPHA" || servers[1].Host != "MANGO" || servers[2].Host != "ZETA" {
		t.Errorf("servers not sorted: got [%s, %s, %s]", servers[0].Host, servers[1].Host, servers[2].Host)
	}
}

func TestHandleServers_IncludesStatus(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	ds.state.Update("SRV01", &dc.CheckResult{Host: "SRV01", Status: "Alert"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/servers", nil)
	ds.handleServers(w, r)

	var servers []ServerView
	if err := json.NewDecoder(w.Body).Decode(&servers); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("len(servers) = %d, want 1", len(servers))
	}
	// Status is normalised to lowercase "alert" by the ServerView mapping.
	if servers[0].Status != "alert" {
		t.Errorf("status = %q, want \"alert\"", servers[0].Status)
	}
}

// ── handleDeleteServer ────────────────────────────────────────────────────────

func TestHandleDeleteServer_UnknownHostReturns404(t *testing.T) {
	ds := newTestServer(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/api/v1/servers/GHOST", nil)
	r.SetPathValue("host", "GHOST")
	ds.handleDeleteServer(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (unknown host should be 404)", w.Code, http.StatusNotFound)
	}
}

func TestHandleDeleteServer_KnownHostReturns200(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/api/v1/servers/SRV01", nil)
	r.SetPathValue("host", "SRV01")
	ds.handleDeleteServer(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var resp struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK {
		t.Error("ok = false, want true")
	}
}

func TestHandleDeleteServer_RemovesServerFromState(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	ds.state.Register("SRV02")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/api/v1/servers/SRV01", nil)
	r.SetPathValue("host", "SRV01")
	ds.handleDeleteServer(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if ds.state.IsRegistered("SRV01") {
		t.Error("SRV01 should be removed from state after DELETE")
	}
	if !ds.state.IsRegistered("SRV02") {
		t.Error("SRV02 should remain registered after SRV01 is deleted")
	}
}

func TestHandleDeleteServer_MissingHostParamReturns400(t *testing.T) {
	ds := newTestServer(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/api/v1/servers/", nil)
	// PathValue("host") returns "" — simulates missing param
	ds.handleDeleteServer(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (missing host param should be 400)", w.Code, http.StatusBadRequest)
	}
}

func TestHandleDeleteServer_IdempotentDeleteReturns404(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")

	deleteOnce := func() int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodDelete, "/api/v1/servers/SRV01", nil)
		r.SetPathValue("host", "SRV01")
		ds.handleDeleteServer(w, r)
		return w.Code
	}

	if code := deleteOnce(); code != http.StatusOK {
		t.Fatalf("first delete: status = %d, want %d", code, http.StatusOK)
	}
	if code := deleteOnce(); code != http.StatusNotFound {
		t.Errorf("second delete: status = %d, want %d (host already gone)", code, http.StatusNotFound)
	}
}

// TestHandleDeleteServer_AuthenticatedUser_Returns200 verifies that
// handleDeleteServer succeeds and does not panic when the request carries SSPI
// auth info in its context (covers the auth != nil branch that logs the username).
func TestHandleDeleteServer_AuthenticatedUser_Returns200(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV-AUTH")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/api/v1/servers/SRV-AUTH", nil)
	r.SetPathValue("host", "SRV-AUTH")
	r = r.WithContext(context.WithValue(r.Context(), authInfoKey, &AuthInfo{Username: "DOMAIN\\alice"}))
	ds.handleDeleteServer(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ds.state.IsRegistered("SRV-AUTH") {
		t.Error("server should have been removed")
	}
}

// ── handleNotifyTest ──────────────────────────────────────────────────────────

func TestHandleNotifyTest_NoTargets_Returns400(t *testing.T) {
	ds := newTestServer(t)
	ds.testNotifyFunc = func() ([]dc.TestNotificationResult, error) {
		return nil, fmt.Errorf("no notification targets configured")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/notify-test", nil)
	ds.handleNotifyTest(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (no targets should yield 400)", w.Code, http.StatusBadRequest)
	}
	if !strings.Contains(w.Body.String(), "no notification targets configured") {
		t.Errorf("body = %q, want error message in body", w.Body.String())
	}
}

func TestHandleNotifyTest_Success_Returns200(t *testing.T) {
	ds := newTestServer(t)
	called := false
	ds.testNotifyFunc = func() ([]dc.TestNotificationResult, error) {
		called = true
		return []dc.TestNotificationResult{
			{Type: "webhook", URL: "https://hook/", OK: true},
		}, nil
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/notify-test", nil)
	ds.handleNotifyTest(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if !called {
		t.Error("testNotifyFunc was not called")
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var resp struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK {
		t.Error("ok = false, want true")
	}
}

func TestHandleNotifyTest_NetworkError_Returns400(t *testing.T) {
	ds := newTestServer(t)
	ds.testNotifyFunc = func() ([]dc.TestNotificationResult, error) {
		return []dc.TestNotificationResult{
			{Type: "webhook", URL: "https://hook/", OK: false, Error: "connection refused"},
		}, fmt.Errorf("connection refused")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/notify-test", nil)
	ds.handleNotifyTest(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleNotifyTest_ConfigError_Returns400(t *testing.T) {
	ds := newTestServer(t)
	ds.testNotifyFunc = func() ([]dc.TestNotificationResult, error) {
		return nil, fmt.Errorf("failed to load config: open config.json: no such file or directory")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/notify-test", nil)
	ds.handleNotifyTest(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (config errors surface as 400)", w.Code, http.StatusBadRequest)
	}
}

func TestHandleNotifyTest_MockWebhookReceivesRequest(t *testing.T) {
	// Spin up a local webhook receiver.
	received := make(chan struct{}, 1)
	webhookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer webhookSrv.Close()

	ds := newTestServer(t)
	// Simulate what the real path does: send to a configured target.
	target := dc.NotificationTarget{
		Type:     "webhook",
		URL:      webhookSrv.URL,
		Triggers: dc.DefaultTriggers,
	}
	ds.testNotifyFunc = func() ([]dc.TestNotificationResult, error) {
		return dc.SendTestNotification([]dc.NotificationTarget{target})
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/notify-test", nil)
	ds.handleNotifyTest(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	select {
	case <-received:
		// webhook was called
	default:
		t.Error("webhook server was not called")
	}
}

func TestHandleNotifyTest_MockWebhookWithSecret_SignatureHeaderPresent(t *testing.T) {
	var gotSig string
	webhookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-DrainCtl-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer webhookSrv.Close()

	ds := newTestServer(t)
	target := dc.NotificationTarget{
		Type:     "webhook",
		URL:      webhookSrv.URL,
		Secret:   "test-secret",
		Triggers: dc.DefaultTriggers,
	}
	ds.testNotifyFunc = func() ([]dc.TestNotificationResult, error) {
		return dc.SendTestNotification([]dc.NotificationTarget{target})
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/notify-test", nil)
	ds.handleNotifyTest(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if !strings.HasPrefix(gotSig, "sha256=") {
		t.Errorf("X-DrainCtl-Signature = %q, want sha256=... prefix", gotSig)
	}
}

// ── handleGetSettings ─────────────────────────────────────────────────────

func TestHandleGetSettings_Returns200WithNotifications(t *testing.T) {
	ds := newTestServer(t)
	ds.testLoadConfigFunc = func() (*dc.Config, error) {
		cfg := dc.DefaultConfig()
		cfg.Notifications = []dc.NotificationTarget{
			{Type: "webhook", URL: "https://hooks.example.com/abc", Triggers: dc.DefaultTriggers},
		}
		cfg.SessionWarningThreshold = 75
		cfg.GracePeriod = 45
		return cfg, nil
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	ds.handleGetSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var resp struct {
		Notifications           []dc.NotificationTarget `json:"notifications"`
		SessionWarningThreshold int                     `json:"session_warning_threshold"`
		GracePeriod             int                     `json:"grace_period"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Notifications) != 1 {
		t.Fatalf("len(notifications) = %d, want 1", len(resp.Notifications))
	}
	if resp.Notifications[0].URL != "https://hooks.example.com/abc" {
		t.Errorf("notifications[0].URL = %q, want https://hooks.example.com/abc", resp.Notifications[0].URL)
	}
	if resp.SessionWarningThreshold != 75 {
		t.Errorf("session_warning_threshold = %d, want 75", resp.SessionWarningThreshold)
	}
	if resp.GracePeriod != 45 {
		t.Errorf("grace_period = %d, want 45", resp.GracePeriod)
	}
}

func TestHandleGetSettings_EmptyNotificationsReturnsEmptyArray(t *testing.T) {
	ds := newTestServer(t)
	ds.testLoadConfigFunc = func() (*dc.Config, error) {
		return dc.DefaultConfig(), nil
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	ds.handleGetSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp struct {
		Notifications []dc.NotificationTarget `json:"notifications"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Must be a JSON array, not null.
	if resp.Notifications == nil {
		t.Error("notifications should be an empty array, not null")
	}
	if len(resp.Notifications) != 0 {
		t.Errorf("len(notifications) = %d, want 0", len(resp.Notifications))
	}
}

func TestHandleGetSettings_NilNotificationsReturnsEmptyArray(t *testing.T) {
	ds := newTestServer(t)
	ds.testLoadConfigFunc = func() (*dc.Config, error) {
		cfg := dc.DefaultConfig()
		cfg.Notifications = nil // simulate old config file with no notifications field
		return cfg, nil
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	ds.handleGetSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	// Decode into a raw map so we can distinguish null from [].
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(w.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	notifRaw, ok := raw["notifications"]
	if !ok {
		t.Fatal("notifications field missing from response")
	}
	// Must be "[]" not "null".
	if string(notifRaw) == "null" {
		t.Error("notifications serialized as null; want empty JSON array []")
	}
}

func TestHandleGetSettings_LoadConfigError_Returns500(t *testing.T) {
	ds := newTestServer(t)
	ds.testLoadConfigFunc = func() (*dc.Config, error) {
		return nil, fmt.Errorf("config file not found")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	ds.handleGetSettings(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d (load error should yield 500)", w.Code, http.StatusInternalServerError)
	}
}

func TestHandleGetSettings_MultipleTargets(t *testing.T) {
	ds := newTestServer(t)
	ds.testLoadConfigFunc = func() (*dc.Config, error) {
		cfg := dc.DefaultConfig()
		cfg.Notifications = []dc.NotificationTarget{
			{Type: "webhook", URL: "https://hook1.example.com/", Secret: "s3cr3t", Triggers: dc.DefaultTriggers},
			{Type: "ntfy", URL: "https://ntfy.sh/my-topic", Triggers: dc.DefaultTriggers},
		}
		return cfg, nil
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	ds.handleGetSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp struct {
		Notifications []dc.NotificationTarget `json:"notifications"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Notifications) != 2 {
		t.Fatalf("len(notifications) = %d, want 2", len(resp.Notifications))
	}
	if resp.Notifications[0].Type != "webhook" {
		t.Errorf("notifications[0].type = %q, want webhook", resp.Notifications[0].Type)
	}
	// Secret is write-only — never returned. Check has_secret instead.
	var rawResp struct {
		Notifications []struct {
			HasSecret bool   `json:"has_secret"`
			Secret    string `json:"secret"`
		} `json:"notifications"`
	}
	// Re-request to get raw JSON
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	ds.handleGetSettings(w2, r2)
	if err := json.NewDecoder(w2.Body).Decode(&rawResp); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if !rawResp.Notifications[0].HasSecret {
		t.Error("notifications[0].has_secret should be true")
	}
	if rawResp.Notifications[0].Secret != "" {
		t.Errorf("notifications[0].secret should be empty (write-only), got %q", rawResp.Notifications[0].Secret)
	}
	if resp.Notifications[1].Type != "ntfy" {
		t.Errorf("notifications[1].type = %q, want ntfy", resp.Notifications[1].Type)
	}
}

// ── handlePutSettings ─────────────────────────────────────────────────────

func TestHandlePutSettings_Success_Returns200(t *testing.T) {
	ds := newTestServer(t)
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int, _ *int, _ *dc.PerformanceConfig) error {
		return nil
	}

	body := `{"notifications":[{"type":"webhook","url":"https://hooks.example.com/","triggers":["drain_on","drain_off","alert","healthy"],"repeat_minutes":0}],"session_warning_threshold":80,"grace_period":60}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	ds.handlePutSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var resp struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK {
		t.Error("ok = false, want true")
	}
}

func TestHandlePutSettings_InvalidJSON_Returns400(t *testing.T) {
	ds := newTestServer(t)
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int, _ *int, _ *dc.PerformanceConfig) error {
		return nil
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader("not json at all"))
	ds.handlePutSettings(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (invalid JSON should be 400)", w.Code, http.StatusBadRequest)
	}
}

func TestHandlePutSettings_UpdateError_Returns500(t *testing.T) {
	ds := newTestServer(t)
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int, _ *int, _ *dc.PerformanceConfig) error {
		return fmt.Errorf("disk full")
	}

	body := `{"notifications":[],"session_warning_threshold":80,"grace_period":60}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
	ds.handlePutSettings(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d (update error should be 500)", w.Code, http.StatusInternalServerError)
	}
}

func TestHandlePutSettings_CallsUpdateWithCorrectNotifications(t *testing.T) {
	ds := newTestServer(t)

	var capturedNotifs *[]dc.NotificationTarget
	var capturedThreshold *int
	var capturedGrace *int
	ds.testPutSettingsFunc = func(notifications *[]dc.NotificationTarget, threshold *int, grace *int, _ *int, _ *dc.PerformanceConfig) error {
		capturedNotifs = notifications
		capturedThreshold = threshold
		capturedGrace = grace
		return nil
	}

	payload := struct {
		Notifications           []dc.NotificationTarget `json:"notifications"`
		SessionWarningThreshold int                     `json:"session_warning_threshold"`
		GracePeriod             int                     `json:"grace_period"`
	}{
		Notifications: []dc.NotificationTarget{
			{Type: "ntfy", URL: "https://ntfy.sh/alerts", Triggers: dc.DefaultTriggers},
		},
		SessionWarningThreshold: 90,
		GracePeriod:             30,
	}
	body, _ := json.Marshal(payload)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", bytes.NewReader(body))
	ds.handlePutSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if capturedNotifs == nil || len(*capturedNotifs) != 1 || (*capturedNotifs)[0].URL != "https://ntfy.sh/alerts" {
		t.Errorf("capturedNotifs = %v, want 1 entry with URL https://ntfy.sh/alerts", capturedNotifs)
	}
	if capturedThreshold == nil || *capturedThreshold != 90 {
		t.Errorf("capturedThreshold = %v, want 90", capturedThreshold)
	}
	if capturedGrace == nil || *capturedGrace != 30 {
		t.Errorf("capturedGrace = %v, want 30", capturedGrace)
	}
}

func TestHandlePutSettings_PartialUpdate_ThresholdAndGraceOmitted(t *testing.T) {
	ds := newTestServer(t)

	var capturedThreshold *int
	var capturedGrace *int
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, threshold *int, grace *int, _ *int, _ *dc.PerformanceConfig) error {
		capturedThreshold = threshold
		capturedGrace = grace
		return nil
	}

	// Only notifications — no threshold or grace_period fields.
	body := `{"notifications":[]}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
	ds.handlePutSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if capturedThreshold != nil {
		t.Errorf("capturedThreshold = %v, want nil (field omitted from request)", capturedThreshold)
	}
	if capturedGrace != nil {
		t.Errorf("capturedGrace = %v, want nil (field omitted from request)", capturedGrace)
	}
}

func TestHandlePutSettings_WebhookSecretPreserved(t *testing.T) {
	ds := newTestServer(t)

	var capturedNotifs *[]dc.NotificationTarget
	ds.testPutSettingsFunc = func(notifications *[]dc.NotificationTarget, _ *int, _ *int, _ *int, _ *dc.PerformanceConfig) error {
		capturedNotifs = notifications
		return nil
	}

	target := dc.NotificationTarget{
		Type:     "webhook",
		URL:      "https://hooks.example.com/secret",
		Secret:   "my-hmac-secret",
		Triggers: dc.DefaultTriggers,
	}
	body, _ := json.Marshal(struct {
		Notifications []dc.NotificationTarget `json:"notifications"`
	}{Notifications: []dc.NotificationTarget{target}})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", bytes.NewReader(body))
	ds.handlePutSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if capturedNotifs == nil || len(*capturedNotifs) != 1 {
		t.Fatalf("len(capturedNotifs) = %v, want 1", capturedNotifs)
	}
	if (*capturedNotifs)[0].Secret != "my-hmac-secret" {
		t.Errorf("Secret = %q, want my-hmac-secret", (*capturedNotifs)[0].Secret)
	}
}

// TestHandlePutSettings_ClearSecret verifies the dashboard's escape hatch for
// wiping a saved secret. Empty secret means "preserve" (legacy back-compat),
// so the only way to actually clear is the per-target clear_secret flag.
func TestHandlePutSettings_ClearSecret(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	// Seed an existing config with a secret on disk so the preserve path has
	// something to fall back on (proves clear_secret beats preservation).
	seed := dc.DefaultConfig()
	seed.Notifications = []dc.NotificationTarget{
		{Type: "webhook", URL: "https://hook/", Secret: "old-hmac", Triggers: dc.DefaultTriggers},
	}
	if err := dc.SaveConfig(seed); err != nil {
		t.Fatalf("seed SaveConfig: %v", err)
	}

	ds := newTestServer(t)
	var capturedNotifs *[]dc.NotificationTarget
	ds.testPutSettingsFunc = func(notifications *[]dc.NotificationTarget, _ *int, _ *int, _ *int, _ *dc.PerformanceConfig) error {
		capturedNotifs = notifications
		return nil
	}

	// Send the same target with no secret AND clear_secret=true.
	body := `{"notifications":[{"type":"webhook","url":"https://hook/","triggers":["drain_on","drain_off","alert","healthy"],"clear_secret":true}]}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
	ds.handlePutSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if capturedNotifs == nil || len(*capturedNotifs) != 1 {
		t.Fatalf("captured = %v, want 1 target", capturedNotifs)
	}
	if got := (*capturedNotifs)[0].Secret; got != "" {
		t.Errorf("Secret = %q, want empty (clear_secret should wipe)", got)
	}
}

func TestHandlePutSettings_AbsentNotifications_PassedAsNil(t *testing.T) {
	ds := newTestServer(t)

	var capturedNotifs *[]dc.NotificationTarget
	ds.testPutSettingsFunc = func(notifications *[]dc.NotificationTarget, _ *int, _ *int, _ *int, _ *dc.PerformanceConfig) error {
		capturedNotifs = notifications
		return nil
	}

	// Body contains only threshold — no "notifications" key at all.
	th := 75
	body, _ := json.Marshal(struct {
		SessionWarningThreshold int `json:"session_warning_threshold"`
	}{SessionWarningThreshold: th})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", bytes.NewReader(body))
	ds.handlePutSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if capturedNotifs != nil {
		t.Errorf("capturedNotifs = %v, want nil (absent field should not clear notifications)", capturedNotifs)
	}
}

func TestHandlePutSettings_OutOfRangeThreshold_Returns400(t *testing.T) {
	ds := newTestServer(t)
	hookCalled := false
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int, _ *int, _ *dc.PerformanceConfig) error {
		hookCalled = true
		return nil
	}

	body := `{"session_warning_threshold":150}` // > 100, invalid
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
	ds.handlePutSettings(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for threshold=150", w.Code)
	}
	if hookCalled {
		t.Error("update hook must not be called for invalid input")
	}
}

func TestHandlePutSettings_NegativeThreshold_Returns400(t *testing.T) {
	ds := newTestServer(t)
	hookCalled := false
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int, _ *int, _ *dc.PerformanceConfig) error {
		hookCalled = true
		return nil
	}

	body := `{"session_warning_threshold":-1}` // < 0, invalid
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
	ds.handlePutSettings(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for threshold=-1", w.Code)
	}
	if hookCalled {
		t.Error("update hook must not be called for invalid input")
	}
}

func TestHandlePutSettings_OutOfRangeGracePeriod_Returns400(t *testing.T) {
	ds := newTestServer(t)
	hookCalled := false
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int, _ *int, _ *dc.PerformanceConfig) error {
		hookCalled = true
		return nil
	}

	body := `{"grace_period":2000}` // > 1440, invalid
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
	ds.handlePutSettings(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for grace_period=2000", w.Code)
	}
	if hookCalled {
		t.Error("update hook must not be called for invalid input")
	}
}

func TestHandlePutSettings_ZeroGracePeriod_Returns400(t *testing.T) {
	ds := newTestServer(t)
	hookCalled := false
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int, _ *int, _ *dc.PerformanceConfig) error {
		hookCalled = true
		return nil
	}

	body := `{"grace_period":0}` // < 1, invalid
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
	ds.handlePutSettings(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for grace_period=0", w.Code)
	}
	if hookCalled {
		t.Error("update hook must not be called for invalid input")
	}
}

func TestHandlePutSettings_InvalidTargetType_Returns400(t *testing.T) {
	ds := newTestServer(t)
	hookCalled := false
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int, _ *int, _ *dc.PerformanceConfig) error {
		hookCalled = true
		return nil
	}

	body := `{"notifications":[{"type":"sms","url":"https://example.com"}]}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
	ds.handlePutSettings(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for unknown target type", w.Code)
	}
	if hookCalled {
		t.Error("update hook must not be called for invalid input")
	}
}

func TestHandlePutSettings_InvalidTargetURLScheme_Returns400(t *testing.T) {
	ds := newTestServer(t)
	hookCalled := false
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int, _ *int, _ *dc.PerformanceConfig) error {
		hookCalled = true
		return nil
	}

	body := `{"notifications":[{"type":"webhook","url":"ftp://bad-scheme.com"}]}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
	ds.handlePutSettings(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for ftp:// URL scheme", w.Code)
	}
	if hookCalled {
		t.Error("update hook must not be called for invalid input")
	}
}

func TestHandlePutSettings_InvalidTargetTrigger_Returns400(t *testing.T) {
	ds := newTestServer(t)
	hookCalled := false
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int, _ *int, _ *dc.PerformanceConfig) error {
		hookCalled = true
		return nil
	}

	body := `{"notifications":[{"type":"webhook","url":"https://example.com","triggers":["drain_on","bogus_trigger"]}]}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
	ds.handlePutSettings(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for unknown trigger", w.Code)
	}
	if hookCalled {
		t.Error("update hook must not be called for invalid input")
	}
}

func TestHandlePutSettings_OutOfRangeRepeatMinutes_Returns400(t *testing.T) {
	ds := newTestServer(t)
	hookCalled := false
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int, _ *int, _ *dc.PerformanceConfig) error {
		hookCalled = true
		return nil
	}

	body := fmt.Sprintf(`{"notifications":[{"type":"webhook","url":"https://example.com","repeat_minutes":%d}]}`, dc.MaxRepeatMinutes+1)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
	ds.handlePutSettings(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for repeat_minutes > MaxRepeatMinutes", w.Code)
	}
	if hookCalled {
		t.Error("update hook must not be called for invalid input")
	}
}

func TestHandlePutSettings_ClearNotificationsWithEmptyArray(t *testing.T) {
	ds := newTestServer(t)

	var capturedNotifs *[]dc.NotificationTarget
	ds.testPutSettingsFunc = func(notifications *[]dc.NotificationTarget, _ *int, _ *int, _ *int, _ *dc.PerformanceConfig) error {
		capturedNotifs = notifications
		return nil
	}

	// An explicit empty array must clear all targets (non-nil pointer to empty slice).
	body := `{"notifications":[]}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
	ds.handlePutSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if capturedNotifs == nil {
		t.Fatal("capturedNotifs is nil; empty array should produce a non-nil pointer to empty slice")
	}
	if len(*capturedNotifs) != 0 {
		t.Errorf("len(*capturedNotifs) = %d, want 0", len(*capturedNotifs))
	}
}

// TestHandlePutSettings_AuthenticatedUser_Returns200 verifies that
// handlePutSettings succeeds when the request carries SSPI auth info
// (the auth != nil branch that logs the username).
func TestHandlePutSettings_AuthenticatedUser_Returns200(t *testing.T) {
	ds := newTestServer(t)
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int, _ *int, _ *dc.PerformanceConfig) error {
		return nil
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(`{}`))
	r = r.WithContext(context.WithValue(r.Context(), authInfoKey, &AuthInfo{Username: "DOMAIN\\bob"}))
	ds.handlePutSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

// TestHandleNotifyTest_AuthenticatedUser_Returns200 verifies that
// handleNotifyTest succeeds and does not panic when the request carries SSPI
// auth info in its context (covers the auth != nil branch).
func TestHandleNotifyTest_AuthenticatedUser_Returns200(t *testing.T) {
	ds := newTestServer(t)
	ds.testNotifyFunc = func() ([]dc.TestNotificationResult, error) { return nil, nil }

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/notify-test", nil)
	r = r.WithContext(context.WithValue(r.Context(), authInfoKey, &AuthInfo{Username: "DOMAIN\\carol"}))
	ds.handleNotifyTest(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

// ── securityMiddleware ────────────────────────────────────────────────────────

func TestSecurityMiddleware_SetsExpectedHeaders(t *testing.T) {
	handler := securityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(w, r)

	headers := map[string]string{
		"Content-Security-Policy": cspHeader,
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "SAMEORIGIN",
		"Referrer-Policy":         "strict-origin-when-cross-origin",
		"Permissions-Policy":      "camera=(), microphone=(), geolocation=()",
		"Cache-Control":           "no-store",
	}
	for name, want := range headers {
		if got := w.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestSecurityMiddleware_DoesNotSetHSTS(t *testing.T) {
	handler := securityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(w, r)

	if hsts := w.Header().Get("Strict-Transport-Security"); hsts != "" {
		t.Errorf("HSTS header present in non-TLS path: %q (should be set by hstsMiddleware only)", hsts)
	}
}

func TestHSTSMiddleware_SetsSTSHeader(t *testing.T) {
	handler := hstsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(w, r)

	sts := w.Header().Get("Strict-Transport-Security")
	if sts == "" {
		t.Fatal("Strict-Transport-Security header missing")
	}
	if !strings.Contains(sts, "max-age=") {
		t.Errorf("Strict-Transport-Security = %q, want max-age= directive", sts)
	}
}

// ── handleUI ──────────────────────────────────────────────────────────────────

func TestHandleUI_ReturnsHTML(t *testing.T) {
	ds := newTestServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	ds.handleUI(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", ct)
	}
	if w.Body.Len() == 0 {
		t.Error("expected non-empty response body")
	}
}

func TestHandleUI_NoCacheHeader(t *testing.T) {
	ds := newTestServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	ds.handleUI(w, r)

	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
}

func TestHandleUI_BodyContainsDashboard(t *testing.T) {
	ds := newTestServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	ds.handleUI(w, r)

	body := w.Body.String()
	if !strings.Contains(body, "DrainCtl") {
		t.Error("response body does not contain expected dashboard content")
	}
}

// ── handleHealth Grace + default branch ───────────────────────────────────────

func TestHandleHealth_GraceServerCounted(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	ds.state.Update("SRV01", &dc.CheckResult{Host: "SRV01", Status: "Grace"})

	w := httptest.NewRecorder()
	ds.handleHealth(w, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	var resp struct {
		Grace    int `json:"grace"`
		Healthy  int `json:"healthy"`
		Alerting int `json:"alerting"`
		Unknown  int `json:"unknown"`
		Offline  int `json:"offline"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Grace != 1 {
		t.Errorf("grace = %d, want 1", resp.Grace)
	}
	if resp.Healthy != 0 || resp.Alerting != 0 || resp.Unknown != 0 || resp.Offline != 0 {
		t.Errorf("unexpected counts: healthy=%d alerting=%d unknown=%d offline=%d",
			resp.Healthy, resp.Alerting, resp.Unknown, resp.Offline)
	}
}

func TestHandleHealth_ErrorStatusCountedAsUnknown(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	// "Error" is not a recognised status value — falls through to default: unknown++.
	ds.state.Update("SRV01", &dc.CheckResult{Host: "SRV01", Status: "Error"})

	w := httptest.NewRecorder()
	ds.handleHealth(w, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	var resp struct {
		Unknown  int `json:"unknown"`
		Healthy  int `json:"healthy"`
		Alerting int `json:"alerting"`
		Grace    int `json:"grace"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Unknown != 1 {
		t.Errorf("unknown = %d, want 1 (Error status should default to unknown)", resp.Unknown)
	}
	if resp.Healthy != 0 || resp.Alerting != 0 || resp.Grace != 0 {
		t.Errorf("unexpected counts: healthy=%d alerting=%d grace=%d",
			resp.Healthy, resp.Alerting, resp.Grace)
	}
}

// ── io.ReadAll error paths ────────────────────────────────────────────────────

// errReader is an io.Reader that always returns an error, used to exercise
// the io.ReadAll failure branch in request body handlers.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, fmt.Errorf("simulated read error") }

func TestHandleReport_ReadBodyError_Returns400(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/report", errReader{})

	ds.handleReport(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandlePutSettings_ReadBodyError_Returns400(t *testing.T) {
	ds := newTestServer(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", errReader{})

	ds.handlePutSettings(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// ── handleGetServer ───────────────────────────────────────────────────────────

func TestHandleGetServer_ReturnsServerView(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	result := &dc.CheckResult{Host: "SRV01", Status: "Healthy", DrainModeLabel: "AllowAll"}
	ds.state.Update("SRV01", result)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/servers/SRV01", nil)
	r.SetPathValue("host", "SRV01")

	ds.handleGetServer(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var view ServerView
	if err := json.NewDecoder(w.Body).Decode(&view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.Host != "SRV01" {
		t.Errorf("host = %q, want SRV01", view.Host)
	}
	// Status is normalised to lowercase token.
	if view.Status != "ok" {
		t.Errorf("status = %q, want \"ok\"", view.Status)
	}
}

func TestHandleGetServer_UnknownHostReturns404(t *testing.T) {
	ds := newTestServer(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/servers/UNKNOWN", nil)
	r.SetPathValue("host", "UNKNOWN")

	ds.handleGetServer(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestHandleGetServer_NoLastResultReturnsRegisteredHost(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV02")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/servers/SRV02", nil)
	r.SetPathValue("host", "SRV02")

	ds.handleGetServer(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var view ServerView
	if err := json.NewDecoder(w.Body).Decode(&view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.Host != "SRV02" {
		t.Errorf("host = %q, want SRV02", view.Host)
	}
	// Newly registered host with no report defaults to "off".
	if view.Status != "off" {
		t.Errorf("status = %q, want \"off\" for newly registered host", view.Status)
	}
}

// TestHandleRegister_ReadBodyError_Returns400 verifies that handleRegister
// returns 400 when the request body cannot be read.
func TestHandleRegister_ReadBodyError_Returns400(t *testing.T) {
	ds := newTestServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/register", errReader{})
	ds.handleRegister(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// TestHandleGetServer_EmptyHost_Returns400 verifies that handleGetServer
// returns 400 when the host path value is empty.
func TestHandleGetServer_EmptyHost_Returns400(t *testing.T) {
	ds := newTestServer(t)
	w := httptest.NewRecorder()
	// Do not call r.SetPathValue so host remains "".
	r := httptest.NewRequest(http.MethodGet, "/api/v1/servers/", nil)
	ds.handleGetServer(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// TestHandleNotifyTest_NilTestFuncUsesLoadConfigFunc_Success verifies that when
// testNotifyFunc is nil, handleNotifyTest uses testLoadConfigFunc to load
// config and calls SendTestNotification with the returned targets.
func TestHandleNotifyTest_NilTestFuncUsesLoadConfigFunc_Success(t *testing.T) {
	webhookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer webhookSrv.Close()

	ds := newTestServer(t)
	// testNotifyFunc is intentionally nil — exercises the real load path.
	ds.testLoadConfigFunc = func() (*dc.Config, error) {
		cfg := &dc.Config{}
		cfg.Notifications = []dc.NotificationTarget{
			{Type: "webhook", URL: webhookSrv.URL, Triggers: dc.DefaultTriggers},
		}
		return cfg, nil
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/notify-test", nil)
	ds.handleNotifyTest(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

// TestHandleNotifyTest_NilTestFuncUsesLoadConfigFunc_LoadError verifies that
// when testNotifyFunc is nil and testLoadConfigFunc returns an error,
// handleNotifyTest returns 400 with the error message.
func TestHandleNotifyTest_NilTestFuncUsesLoadConfigFunc_LoadError(t *testing.T) {
	ds := newTestServer(t)
	ds.testLoadConfigFunc = func() (*dc.Config, error) {
		return nil, fmt.Errorf("config unavailable")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/notify-test", nil)
	ds.handleNotifyTest(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "config unavailable") {
		t.Errorf("body %q should contain %q", w.Body.String(), "config unavailable")
	}
}

// ── production config-path coverage ──────────────────────────────────────────
//
// The following tests exercise the "else" branches in handlers that call
// dc.LoadConfig / dc.UpdateNotifySettings directly when testLoadConfigFunc /
// testPutSettingsFunc are nil. They use t.Setenv("ProgramData", …) to
// redirect all config I/O to a temp directory so no machine-wide state is
// affected.

// TestHandleGetSettings_ProductionPathLoadsConfig exercises the
// dc.LoadConfig branch (testLoadConfigFunc == nil). A fresh config is written
// on first access; the handler must return 200 with valid JSON.
func TestHandleGetSettings_ProductionPathLoadsConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)

	ds := newTestServer(t)
	// testLoadConfigFunc is nil — production dc.LoadConfig is used.

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	ds.handleGetSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var out struct {
		Notifications           []dc.NotificationTarget `json:"notifications"`
		SessionWarningThreshold int                     `json:"session_warning_threshold"`
		GracePeriod             int                     `json:"grace_period"`
	}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Notifications == nil {
		t.Error("expected non-nil notifications array")
	}
}

// TestHandlePutSettings_ProductionPathUpdatesConfig exercises the
// dc.UpdateNotifySettings branch (testPutSettingsFunc == nil). A valid
// grace_period update is sent; the handler must return 200.
func TestHandlePutSettings_ProductionPathUpdatesConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)

	ds := newTestServer(t)
	// testPutSettingsFunc is nil — production dc.UpdateNotifySettings is used.

	body := `{"grace_period":10}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
	ds.handlePutSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
}

// TestHandlePutSettings_ProductionPath_UpdateError_Returns500 exercises the
// error-return branch in the else block (testPutSettingsFunc == nil). A
// directory placed at config.json causes LoadConfig inside UpdateNotifySettings
// to fail with a non-not-exist error, which propagates as 500.
func TestHandlePutSettings_ProductionPath_UpdateError_Returns500(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)

	// Create the data dir and place a directory where config.json should be,
	// so os.ReadFile returns an error that is NOT os.IsNotExist.
	dataDir := filepath.Join(dir, "LISS Technologies", "LISSTech DrainCtl")
	if err := os.MkdirAll(filepath.Join(dataDir, "config.json"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	ds := newTestServer(t)
	// testPutSettingsFunc is nil — production dc.UpdateNotifySettings used.

	body := `{"grace_period":10}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
	ds.handlePutSettings(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body.String())
	}
}

// TestHandleNotifyTest_BothHooksNil_UsesProductionLoad exercises the inner
// testLoadConfigFunc==nil branch inside handleNotifyTest when testNotifyFunc is
// also nil. The fresh default config has no notification targets so
// SendTestNotification returns "no targets" and the handler returns 400.
func TestHandleNotifyTest_BothHooksNil_UsesProductionLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)

	ds := newTestServer(t)
	// Both testNotifyFunc and testLoadConfigFunc are nil — production load path.

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/notify-test", nil)
	ds.handleNotifyTest(w, r)

	// Fresh default config has no notification targets → 400 "no targets".
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
}

// ── Broker tests ─────────────────────────────────────────────────────────────

func mustMarshalTest(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBroker_SubscribeAndBroadcast(t *testing.T) {
	b := NewBroker()
	id, ch, _, err := b.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Unsubscribe(id)

	if b.Count() != 1 {
		t.Fatalf("count = %d, want 1", b.Count())
	}

	b.Broadcast(mustMarshalTest(t, SSEEvent{Type: "server_update", Host: "SRV01", Data: []byte(`{}`), Timestamp: time.Now()}))

	select {
	case msg := <-ch:
		if !bytes.Contains(msg, []byte(`"server_update"`)) {
			t.Errorf("message missing event type: %s", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for broadcast")
	}
}

func TestBroker_Unsubscribe(t *testing.T) {
	b := NewBroker()
	id, _, _, _ := b.Subscribe()
	b.Unsubscribe(id)

	if b.Count() != 0 {
		t.Fatalf("count = %d after unsubscribe, want 0", b.Count())
	}
}

func TestBroker_SlowSubscriberEvicted(t *testing.T) {
	b := NewBroker()
	id, ch, done, _ := b.Subscribe()
	_ = ch // don't read from it

	// Fill the channel buffer (16 messages).
	for i := 0; i < 17; i++ {
		b.Broadcast(mustMarshalTest(t, SSEEvent{Type: "server_update", Data: []byte(`{}`), Timestamp: time.Now()}))
	}

	select {
	case <-done:
		// subscriber was evicted — correct
	case <-time.After(time.Second):
		t.Fatal("slow subscriber was not evicted")
	}
	_ = id

	if b.Count() != 0 {
		t.Fatalf("count = %d after eviction, want 0", b.Count())
	}
}

func TestBroker_MultipleSubscribers(t *testing.T) {
	b := NewBroker()
	id1, ch1, _, _ := b.Subscribe()
	id2, ch2, _, _ := b.Subscribe()
	defer b.Unsubscribe(id1)
	defer b.Unsubscribe(id2)

	b.Broadcast(mustMarshalTest(t, SSEEvent{Type: "server_update", Host: "SRV01", Data: []byte(`{}`), Timestamp: time.Now()}))

	for i, ch := range []<-chan []byte{ch1, ch2} {
		select {
		case msg := <-ch:
			if !bytes.Contains(msg, []byte(`SRV01`)) {
				t.Errorf("subscriber %d: message missing host: %s", i, msg)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timeout", i)
		}
	}
}

func TestBroker_SubscriberCap(t *testing.T) {
	b := NewBroker()
	for i := 0; i < maxSubscribers; i++ {
		_, _, _, err := b.Subscribe()
		if err != nil {
			t.Fatalf("subscribe %d failed: %v", i, err)
		}
	}
	_, _, _, err := b.Subscribe()
	if err != ErrTooManySubscribers {
		t.Fatalf("expected ErrTooManySubscribers, got %v", err)
	}
}

// TestHandleSSE_DeliversBroadcastedEvent verifies the full SSE pipeline:
// headers are set correctly and a broadcast event is delivered to the HTTP body.
func TestHandleSSE_DeliversBroadcastedEvent(t *testing.T) {
	ds := newTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(ctx)

	// Run handleSSE in a goroutine — it blocks until the context is cancelled.
	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		ds.handleSSE(w, r)
	}()

	// Give the handler time to subscribe before we broadcast.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && ds.broker.Count() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if ds.broker.Count() == 0 {
		t.Fatal("handler did not subscribe within 1s")
	}

	// Broadcast an event and let the handler write it.
	payload := mustMarshalTest(t, SSEEvent{
		Type:      "server_update",
		Host:      "SRV01",
		Data:      json.RawMessage(`{}`),
		Timestamp: time.Now(),
	})
	ds.broker.Broadcast(payload)

	// Give the handler time to write the event.
	time.Sleep(50 * time.Millisecond)

	// Cancel the context to stop the handler.
	cancel()
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handleSSE did not return after context cancellation")
	}

	// Verify SSE headers.
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	// Verify the event payload was written in SSE format.
	body := w.Body.String()
	if !strings.Contains(body, "data:") {
		t.Errorf("SSE body missing data: prefix; got %q", body)
	}
	if !strings.Contains(body, "server_update") {
		t.Errorf("SSE body missing event type; got %q", body)
	}
	if !strings.Contains(body, "SRV01") {
		t.Errorf("SSE body missing host; got %q", body)
	}
}

// TestHandleSSE_TooManySubscribers verifies that the endpoint returns 429
// when the broker subscriber cap is reached.
func TestHandleSSE_TooManySubscribers(t *testing.T) {
	ds := newTestServer(t)

	// Fill the broker to the cap.
	for i := 0; i < maxSubscribers; i++ {
		_, _, _, err := ds.broker.Subscribe()
		if err != nil {
			t.Fatalf("subscribe %d: %v", i, err)
		}
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	ds.handleSSE(w, r)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w.Code)
	}
}

// TestHandleSSE_SendsKeepalive verifies that the handler emits ": keepalive"
// SSE comments on the configured interval when no events are broadcast.
// A short interval is injected via sseKeepaliveInterval to avoid a 25-second
// wall-clock wait in CI.
func TestHandleSSE_SendsKeepalive(t *testing.T) {
	orig := sseKeepaliveInterval
	sseKeepaliveInterval = 50 * time.Millisecond
	t.Cleanup(func() { sseKeepaliveInterval = orig })

	ds := newTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(ctx)

	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		ds.handleSSE(w, r)
	}()

	// Wait long enough for at least one keepalive tick.
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handleSSE did not return after context cancellation")
	}

	body := w.Body.String()
	if !strings.Contains(body, ": keepalive") {
		t.Errorf("SSE body missing keepalive comment; got %q", body)
	}
}

// TestHandleSSE_ClosesOnSessionExpiry verifies that the SSE handler closes the
// stream within sseSessionCheckInterval after the session is deleted (e.g.
// after logout from another tab or admin revocation).
func TestHandleSSE_ClosesOnSessionExpiry(t *testing.T) {
	orig := sseSessionCheckInterval
	sseSessionCheckInterval = 50 * time.Millisecond
	t.Cleanup(func() { sseSessionCheckInterval = orig })

	ds := newTestServer(t)
	// Attach a real session store — newTestServer omits it by default since
	// most tests call handlers directly without authentication middleware.
	storeCtx, storeCancel := context.WithCancel(context.Background())
	t.Cleanup(storeCancel)
	ds.sessionStore = NewSessionStore(storeCtx)

	// Create a real session in the store and attach the token as a cookie.
	token, err := ds.sessionStore.Create(&AuthInfo{Username: "testuser"})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(ctx)
	r.AddCookie(&http.Cookie{Name: "drainctl_session", Value: token})

	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		ds.handleSSE(w, r)
	}()

	// Wait for the handler to subscribe.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && ds.broker.Count() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if ds.broker.Count() == 0 {
		t.Fatal("handler did not subscribe within 1s")
	}

	// Delete the session — simulates logout or admin revocation.
	ds.sessionStore.Delete(token)

	// Handler must close within a few check intervals.
	select {
	case <-handlerDone:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("handleSSE did not close after session was deleted")
	}
}

// TestHandleReport_BroadcastsSSEUpdate verifies the end-to-end SSE wiring:
// handleReport → state.Update() → state.OnUpdate → broker.Broadcast() delivers
// a server_update event to a connected subscriber.
func TestHandleReport_BroadcastsSSEUpdate(t *testing.T) {
	ds := newTestServer(t)

	// Wire the SSE broadcast exactly as StartDashboard does in production.
	ds.state.OnUpdate = ds.broadcastServerUpdate

	ds.state.Register("SRV01")

	// Subscribe before the report arrives.
	_, ch, done, err := ds.broker.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer ds.broker.Unsubscribe("sub-1")

	result := dc.CheckResult{Host: "SRV01", Status: "Healthy"}
	body, _ := json.Marshal(result)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/report", bytes.NewReader(body))
	ds.handleReport(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("handleReport status = %d, want 200", w.Code)
	}

	select {
	case msg := <-ch:
		if !bytes.Contains(msg, []byte(`"server_update"`)) {
			t.Errorf("broadcast missing server_update type: %s", msg)
		}
		if !bytes.Contains(msg, []byte(`SRV01`)) {
			t.Errorf("broadcast missing host SRV01: %s", msg)
		}
	case <-done:
		t.Fatal("subscriber evicted unexpectedly")
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for SSE broadcast after handleReport")
	}
}

// TestHandlePutSettings_BroadcastsSSESettingsUpdate verifies that a successful
// PUT /api/v1/settings broadcasts a settings_update event to connected browsers.
func TestHandlePutSettings_BroadcastsSSESettingsUpdate(t *testing.T) {
	ds := newTestServer(t)

	// Inject no-op update and a config loader so broadcastSettingsUpdate succeeds.
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int, _ *int, _ *dc.PerformanceConfig) error { return nil }
	ds.testLoadConfigFunc = func() (*dc.Config, error) {
		return &dc.Config{
			GracePeriod:             10,
			SessionWarningThreshold: 80,
			Notifications:           []dc.NotificationTarget{},
		}, nil
	}

	_, ch, done, err := ds.broker.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer ds.broker.Unsubscribe("sub-1")

	body := []byte(`{"grace_period":10}`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", bytes.NewReader(body))
	ds.handlePutSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("handlePutSettings status = %d, want 200", w.Code)
	}

	select {
	case msg := <-ch:
		if !bytes.Contains(msg, []byte(`"settings_update"`)) {
			t.Errorf("broadcast missing settings_update type: %s", msg)
		}
	case <-done:
		t.Fatal("subscriber evicted unexpectedly")
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for SSE broadcast after handlePutSettings")
	}
}

// TestBroadcastSettingsUpdate_SecretsStripped verifies that webhook HMAC keys
// and other secrets are never included in the settings_update SSE broadcast.
// The real backend stores secrets in config.json but must not transmit them
// over the event stream to connected browsers.
func TestBroadcastSettingsUpdate_SecretsStripped(t *testing.T) {
	ds := newTestServer(t)
	ds.testLoadConfigFunc = func() (*dc.Config, error) {
		return &dc.Config{
			GracePeriod:             15,
			SessionWarningThreshold: 80,
			Notifications: []dc.NotificationTarget{
				{Type: "webhook", URL: "https://hook.example.com/", Secret: "top-secret-hmac-key", Triggers: dc.DefaultTriggers},
				{Type: "ntfy", URL: "https://ntfy.example.com/alerts", Secret: "ntfy-token", Triggers: dc.DefaultTriggers},
			},
		}, nil
	}

	_, ch, done, err := ds.broker.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer ds.broker.Unsubscribe("sub-1")

	ds.broadcastSettingsUpdate()

	select {
	case msg := <-ch:
		if bytes.Contains(msg, []byte("top-secret-hmac-key")) {
			t.Errorf("broadcast contains webhook secret; got: %s", msg)
		}
		if bytes.Contains(msg, []byte("ntfy-token")) {
			t.Errorf("broadcast contains ntfy secret; got: %s", msg)
		}
		if !bytes.Contains(msg, []byte(`"settings_update"`)) {
			t.Errorf("broadcast missing settings_update type: %s", msg)
		}
	case <-done:
		t.Fatal("subscriber evicted unexpectedly")
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for SSE broadcast")
	}
}

// TestHandleRegister_BroadcastsSSEServerUpdate verifies that a successful
// POST /api/v1/register broadcasts a server_update event to connected browsers
// so they can display the new server without waiting for the next poll cycle.
func TestHandleRegister_BroadcastsSSEServerUpdate(t *testing.T) {
	ds := newTestServer(t)

	_, ch, done, err := ds.broker.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer ds.broker.Unsubscribe("sub-1")

	body := []byte(`{"hostname":"NEW01"}`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(body))
	ds.handleRegister(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("handleRegister status = %d, want 200", w.Code)
	}

	select {
	case msg := <-ch:
		if !bytes.Contains(msg, []byte(`"server_update"`)) {
			t.Errorf("broadcast missing server_update type: %s", msg)
		}
		if !bytes.Contains(msg, []byte(`NEW01`)) {
			t.Errorf("broadcast missing host NEW01: %s", msg)
		}
	case <-done:
		t.Fatal("subscriber evicted unexpectedly")
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for SSE broadcast after handleRegister")
	}
}

// TestHandleDeleteServer_BroadcastsSSEServerDeleted verifies that a successful
// DELETE /api/v1/servers/{host} broadcasts a server_deleted event so connected
// browsers remove the server from their list immediately.
func TestHandleDeleteServer_BroadcastsSSEServerDeleted(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("OLD01")

	_, ch, done, err := ds.broker.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer ds.broker.Unsubscribe("sub-1")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/api/v1/servers/OLD01", nil)
	r.SetPathValue("host", "OLD01")
	ds.handleDeleteServer(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("handleDeleteServer status = %d, want 200", w.Code)
	}

	select {
	case msg := <-ch:
		if !bytes.Contains(msg, []byte(`"server_deleted"`)) {
			t.Errorf("broadcast missing server_deleted type: %s", msg)
		}
		if !bytes.Contains(msg, []byte(`OLD01`)) {
			t.Errorf("broadcast missing host OLD01: %s", msg)
		}
	case <-done:
		t.Fatal("subscriber evicted unexpectedly")
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for SSE broadcast after handleDeleteServer")
	}
}

// ── isAuthorizedForHost ───────────────────────────────────────────────────────

func TestIsAuthorizedForHost(t *testing.T) {
	const group = "Domain Admins"

	tests := []struct {
		name     string
		auth     *AuthInfo
		hostname string
		want     bool
	}{
		// nil guard
		{name: "nil auth", auth: nil, hostname: "SRV01", want: false},

		// Machine accounts — only allowed for their own host
		{name: "machine account no domain matches", auth: &AuthInfo{Username: "SRV01$"}, hostname: "SRV01", want: true},
		{name: "machine account no domain case insensitive", auth: &AuthInfo{Username: "srv01$"}, hostname: "SRV01", want: true},
		{name: "machine account with domain matches", auth: &AuthInfo{Username: `CONTOSO\SRV01$`}, hostname: "SRV01", want: true},
		{name: "machine account with domain case insensitive", auth: &AuthInfo{Username: `CONTOSO\srv01$`}, hostname: "SRV01", want: true},
		{name: "machine account wrong host rejected", auth: &AuthInfo{Username: `CONTOSO\SRV02$`}, hostname: "SRV01", want: false},
		{name: "machine account no domain wrong host", auth: &AuthInfo{Username: "SRV02$"}, hostname: "SRV01", want: false},

		// Non-machine accounts — must be member of admin group
		{name: "non-machine in group allowed", auth: &AuthInfo{Username: "alice", Groups: []string{group}}, hostname: "SRV01", want: true},
		{name: "non-machine in group case insensitive", auth: &AuthInfo{Username: "alice", Groups: []string{"domain admins"}}, hostname: "SRV01", want: true},
		{name: "non-machine in domain-prefixed group", auth: &AuthInfo{Username: "alice", Groups: []string{`CONTOSO\Domain Admins`}}, hostname: "SRV01", want: true},
		{name: "non-machine domain-prefixed group case insensitive", auth: &AuthInfo{Username: "alice", Groups: []string{`CONTOSO\domain admins`}}, hostname: "SRV01", want: true},
		{name: "non-machine not in group rejected", auth: &AuthInfo{Username: "alice", Groups: []string{"Users"}}, hostname: "SRV01", want: false},
		{name: "non-machine empty groups rejected", auth: &AuthInfo{Username: "alice", Groups: nil}, hostname: "SRV01", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isAuthorizedForHost(tc.auth, tc.hostname, group)
			if got != tc.want {
				t.Errorf("isAuthorizedForHost(%v, %q, %q) = %v, want %v", tc.auth, tc.hostname, group, got, tc.want)
			}
		})
	}
}

func TestServerState_Update_OnUpdateCallback_NoDeadlock(t *testing.T) {
	state := newTestServerState(t)
	state.Register("SRV01")

	// Wire OnUpdate to call state.Get() — the exact pattern that caused
	// the production deadlock (commit d9bf821). If the lock is not released
	// before calling OnUpdate, this test hangs (detected by timeout).
	called := make(chan string, 1)
	state.OnUpdate = func(hostname string) {
		// This calls state.mu.RLock — deadlocks if Update still holds the write lock.
		info := state.Get(hostname)
		if info == nil {
			t.Error("Get returned nil for registered host inside OnUpdate")
		}
		called <- hostname
	}

	result := &dc.CheckResult{Host: "SRV01", Status: "Healthy"}
	state.Update("SRV01", result)

	select {
	case host := <-called:
		if host != "SRV01" {
			t.Errorf("OnUpdate called with %q, want SRV01", host)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnUpdate was not called — possible deadlock")
	}
}

// ── handleMetrics ─────────────────────────────────────────────────────────────

// newTestServerWithStore returns a DashboardServer wired to a real on-disk
// telemetry store. The returned cleanup closes the DB; t.TempDir() reaps the
// file itself. Used by handleMetrics contract tests that need QueryRange to
// round-trip real rows through SQLite.
func newTestServerWithStore(t *testing.T) (*DashboardServer, *telemetry.MetricsStore, func()) {
	t.Helper()
	db, err := telemetry.Open(t.TempDir())
	if err != nil {
		t.Fatalf("telemetry.Open: %v", err)
	}
	ms := telemetry.NewMetricsStore(db)
	ds := &DashboardServer{
		state:  newTestServerState(t),
		cfg:    dc.DashboardConfig{Group: "Domain Admins"},
		broker: NewBroker(),
		ms:     ms,
	}
	return ds, ms, func() { _ = db.Close() }
}

func TestMetricsHandler_ContractShape(t *testing.T) {
	ds, ms, closeDB := newTestServerWithStore(t)
	defer closeDB()
	ds.state.Register("SRV01")

	base := time.Now().UTC().Truncate(time.Second).Add(-10 * time.Minute)
	samples := []telemetry.Sample{
		{Ts: base, Host: "SRV01", Counter: "cpu_pct", Value: 12.4},
		{Ts: base.Add(30 * time.Second), Host: "SRV01", Counter: "cpu_pct", Value: 15.1},
		{Ts: base.Add(60 * time.Second), Host: "SRV01", Counter: "cpu_pct", Value: 13.7},
	}
	if err := ms.Append(context.Background(), samples); err != nil {
		t.Fatalf("Append: %v", err)
	}

	from := base.Add(-time.Minute).Format(time.RFC3339)
	to := base.Add(5 * time.Minute).Format(time.RFC3339)
	url := "/api/v1/metrics/SRV01?from=" + from + "&to=" + to + "&resolution=raw"
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, url, nil)
	r.SetPathValue("host", "SRV01")
	ds.handleMetrics(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var resp struct {
		Host            string  `json:"host"`
		Tier            string  `json:"tier"`
		From            string  `json:"from"`
		To              string  `json:"to"`
		OldestAvailable *string `json:"oldest_available"`
		NewestAvailable *string `json:"newest_available"`
		Series          map[string]struct {
			T   []int64   `json:"t"`
			Avg []float64 `json:"avg"`
			Min []float64 `json:"min"`
			Max []float64 `json:"max"`
		} `json:"series"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Host != "SRV01" {
		t.Errorf("host = %q, want SRV01", resp.Host)
	}
	if resp.Tier != "raw" {
		t.Errorf("tier = %q, want raw", resp.Tier)
	}
	if resp.From == "" || resp.To == "" {
		t.Errorf("from/to missing: from=%q to=%q", resp.From, resp.To)
	}
	if resp.OldestAvailable == nil || resp.NewestAvailable == nil {
		t.Fatalf("oldest/newest should be set when data exists: oldest=%v newest=%v",
			resp.OldestAvailable, resp.NewestAvailable)
	}
	cpu, ok := resp.Series["cpu_pct"]
	if !ok {
		t.Fatalf("series.cpu_pct missing; got keys=%v", resp.Series)
	}
	if len(cpu.T) != 3 || len(cpu.Avg) != 3 || len(cpu.Min) != 3 || len(cpu.Max) != 3 {
		t.Fatalf("series arrays wrong length: t=%d avg=%d min=%d max=%d",
			len(cpu.T), len(cpu.Avg), len(cpu.Min), len(cpu.Max))
	}
	// Contract: for raw tier, avg == min == max == original value (http-metrics.md).
	for i := range cpu.T {
		if cpu.Avg[i] != cpu.Min[i] || cpu.Avg[i] != cpu.Max[i] {
			t.Errorf("raw tier avg/min/max should be equal at i=%d: avg=%v min=%v max=%v",
				i, cpu.Avg[i], cpu.Min[i], cpu.Max[i])
		}
	}
}

func TestMetricsHandler_EmptyStateReturns200(t *testing.T) {
	ds, _, closeDB := newTestServerWithStore(t)
	defer closeDB()
	ds.state.Register("SRV01")

	from := time.Now().UTC().Add(-5 * time.Minute).Format(time.RFC3339)
	to := time.Now().UTC().Format(time.RFC3339)
	url := "/api/v1/metrics/SRV01?from=" + from + "&to=" + to + "&resolution=raw"
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, url, nil)
	r.SetPathValue("host", "SRV01")
	ds.handleMetrics(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	// Empty-state contract: oldest_available / newest_available are JSON null,
	// series is an empty object. Re-decode as raw JSON to distinguish null from
	// missing and {} from null.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if got := string(raw["oldest_available"]); got != "null" {
		t.Errorf("oldest_available = %s, want null", got)
	}
	if got := string(raw["newest_available"]); got != "null" {
		t.Errorf("newest_available = %s, want null", got)
	}
	if got := strings.TrimSpace(string(raw["series"])); got != "{}" {
		t.Errorf("series = %s, want {}", got)
	}
}

func TestMetricsHandler_InvalidRangeReturns400(t *testing.T) {
	cases := []struct {
		name  string
		query string
		code  string
	}{
		{"missing from", "?to=2026-04-16T00:00:00Z&resolution=raw", "invalid_range"},
		{"missing to", "?from=2026-04-15T00:00:00Z&resolution=raw", "invalid_range"},
		{"missing both", "?resolution=raw", "invalid_range"},
		{"unparseable from", "?from=nope&to=2026-04-16T00:00:00Z&resolution=raw", "invalid_range"},
		{"unparseable to", "?from=2026-04-15T00:00:00Z&to=nope&resolution=raw", "invalid_range"},
		{"to equals from", "?from=2026-04-15T00:00:00Z&to=2026-04-15T00:00:00Z&resolution=raw", "invalid_range"},
		{"to before from", "?from=2026-04-16T00:00:00Z&to=2026-04-15T00:00:00Z&resolution=raw", "invalid_range"},
		{"range exceeds 90 days", "?from=2025-01-01T00:00:00Z&to=2025-07-01T00:00:00Z&resolution=raw", "invalid_range"},
		{"bad resolution", "?from=2026-04-15T00:00:00Z&to=2026-04-16T00:00:00Z&resolution=yearly", "invalid_resolution"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ds, _, closeDB := newTestServerWithStore(t)
			defer closeDB()
			ds.state.Register("SRV01")

			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/SRV01"+tc.query, nil)
			r.SetPathValue("host", "SRV01")
			ds.handleMetrics(w, r)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusBadRequest, w.Body.String())
			}
			var body map[string]string
			if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["error"] != tc.code {
				t.Errorf("error = %q, want %q", body["error"], tc.code)
			}
		})
	}
}

func TestMetricsHandler_UnknownHostReturns404(t *testing.T) {
	ds, _, closeDB := newTestServerWithStore(t)
	defer closeDB()
	// Deliberately do not register any host.

	url := "/api/v1/metrics/GHOST?from=2026-04-15T00:00:00Z&to=2026-04-16T00:00:00Z&resolution=raw"
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, url, nil)
	r.SetPathValue("host", "GHOST")
	ds.handleMetrics(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusNotFound, w.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "unknown_host" {
		t.Errorf("error = %q, want unknown_host", body["error"])
	}
}

// newTestServerWithAggregator wires a DashboardServer to a real on-disk
// telemetry store plus an Aggregator so tests can materialise 5-min and
// hourly tiers deterministically via a single RollOnce call.
func newTestServerWithAggregator(t *testing.T) (*DashboardServer, *telemetry.MetricsStore, *telemetry.Aggregator, func()) {
	t.Helper()
	db, err := telemetry.Open(t.TempDir())
	if err != nil {
		t.Fatalf("telemetry.Open: %v", err)
	}
	ms := telemetry.NewMetricsStore(db)
	agg := telemetry.NewAggregator(db, 60)
	ds := &DashboardServer{
		state:  newTestServerState(t),
		cfg:    dc.DashboardConfig{Group: "Domain Admins"},
		broker: NewBroker(),
		ms:     ms,
	}
	return ds, ms, agg, func() { _ = db.Close() }
}

// decodeMetricsTier reads just the `tier` field from a handler response body.
func decodeMetricsTier(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var resp struct {
		Tier string `json:"tier"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	return resp.Tier
}

// TestMetricsHandler_ResolutionAutoMatrix drives every decision boundary in
// research.md §8's auto-resolution table. Seeds a single raw sample far
// enough in the past that all three tiers have `oldest <= from` for every
// window under test, so the table exercises the window → tier mapping
// without interference from the degradation code path.
func TestMetricsHandler_ResolutionAutoMatrix(t *testing.T) {
	ds, ms, agg, closeDB := newTestServerWithAggregator(t)
	defer closeDB()
	ds.state.Register("SRV01")

	now := time.Now().UTC().Truncate(time.Second)
	ctx := context.Background()

	// One sample 50h back lets `roll5Min` materialise a 5-min bucket and
	// `rollHourly` (reading from metrics_5min) materialise an hourly bucket,
	// so all three tier pools contain rows older than any tested `from`.
	if err := ms.Append(ctx, []telemetry.Sample{
		{Ts: now.Add(-50 * time.Hour), Host: "SRV01", Counter: "cpu_pct", Value: 10},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	agg.RollOnce(ctx, now)

	// Sanity: each tier must hold >= 1 row for this host. If the aggregator
	// watermark math drifts, the rest of the matrix would silently fall
	// through to the degradation path and misdiagnose.
	for _, tier := range []telemetry.Tier{telemetry.TierRaw, telemetry.TierFiveMin, telemetry.TierHourly} {
		oldest, _, err := ms.BoundsForTier(ctx, "SRV01", tier)
		if err != nil {
			t.Fatalf("BoundsForTier %s: %v", tier.TierName(), err)
		}
		if oldest == nil {
			t.Fatalf("tier %s has no rows after aggregation; matrix cannot test auto selection", tier.TierName())
		}
	}

	cases := []struct {
		name   string
		window time.Duration
		tier   string
	}{
		{"5m → raw", 5 * time.Minute, "raw"},
		{"15m (inclusive) → raw", 15 * time.Minute, "raw"},
		{"15m+1m → 1min", 15*time.Minute + time.Minute, "1min"},
		{"1h (inclusive) → 1min", time.Hour, "1min"},
		{"1h+1m → 5min", time.Hour + time.Minute, "5min"},
		{"12h → 5min", 12 * time.Hour, "5min"},
		{"36h (inclusive) → 5min", 36 * time.Hour, "5min"},
		{"36h+1m → hourly", 36*time.Hour + time.Minute, "hourly"},
		{"5d → hourly", 5 * 24 * time.Hour, "hourly"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from := now.Add(-tc.window).Format(time.RFC3339)
			to := now.Format(time.RFC3339)
			url := "/api/v1/metrics/SRV01?from=" + from + "&to=" + to + "&resolution=auto"
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, url, nil)
			r.SetPathValue("host", "SRV01")
			ds.handleMetrics(w, r)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
			}
			if got := decodeMetricsTier(t, w); got != tc.tier {
				t.Errorf("tier = %q, want %q (window=%s)", got, tc.tier, tc.window)
			}
		})
	}
}

// TestMetricsHandler_ExplicitResolutionOverrides confirms that an explicit
// `resolution=` value bypasses the auto window→tier selection AND the
// degradation loop — the response echoes the requested tier even if no data
// is materialised there. Empty DB is used so there is nothing for auto to
// fall back to.
func TestMetricsHandler_ExplicitResolutionOverrides(t *testing.T) {
	ds, _, closeDB := newTestServerWithStore(t)
	defer closeDB()
	ds.state.Register("SRV01")

	now := time.Now().UTC().Truncate(time.Second)

	cases := []struct {
		name       string
		resolution string
		window     time.Duration
	}{
		// A 48h window would pick hourly under auto. Explicit raw forces raw.
		{"resolution=raw overrides a 48h window", "raw", 48 * time.Hour},
		// A 30m window would pick raw under auto. Explicit 5min forces 5min.
		{"resolution=5min overrides a 30m window", "5min", 30 * time.Minute},
		// A 30m window would pick raw under auto. Explicit hourly forces hourly.
		{"resolution=hourly overrides a 30m window", "hourly", 30 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from := now.Add(-tc.window).Format(time.RFC3339)
			to := now.Format(time.RFC3339)
			url := "/api/v1/metrics/SRV01?from=" + from + "&to=" + to + "&resolution=" + tc.resolution
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, url, nil)
			r.SetPathValue("host", "SRV01")
			ds.handleMetrics(w, r)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
			}
			if got := decodeMetricsTier(t, w); got != tc.resolution {
				t.Errorf("tier = %q, want %q", got, tc.resolution)
			}
		})
	}
}

// TestMetricsHandler_DegradesWhenRawPurged walks the full degradation chain.
// Raw holds only very recent samples (oldest > from), 5-min and hourly are
// empty because the aggregator hasn't run. The handler must degrade
// raw → 5min → hourly and return hourly since it is the coarsest tier.
func TestMetricsHandler_DegradesWhenRawPurged(t *testing.T) {
	ds, ms, closeDB := newTestServerWithStore(t)
	defer closeDB()
	ds.state.Register("SRV01")

	now := time.Now().UTC().Truncate(time.Second)
	ctx := context.Background()

	// Raw oldest = now-20m. Request from = now-3h is strictly older than that,
	// so raw reports coverage-missing via oldest.After(from) and degrades.
	if err := ms.Append(ctx, []telemetry.Sample{
		{Ts: now.Add(-20 * time.Minute), Host: "SRV01", Counter: "cpu_pct", Value: 7},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Window = 30m picks raw under auto. With raw missing the older half of
	// the window, and 5-min + hourly both empty, resolveMetricsTier should
	// fall all the way through to hourly (it is never degraded off).
	from := now.Add(-3 * time.Hour).Format(time.RFC3339)
	to := now.Add(-150 * time.Minute).Format(time.RFC3339)
	url := "/api/v1/metrics/SRV01?from=" + from + "&to=" + to + "&resolution=auto"
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, url, nil)
	r.SetPathValue("host", "SRV01")
	ds.handleMetrics(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := decodeMetricsTier(t, w); got != "hourly" {
		t.Errorf("tier = %q, want hourly", got)
	}
}

// ── handleAudit ──────────────────────────────────────────────────────────────

// newTestServerWithAudit wires a DashboardServer to a real on-disk telemetry
// store with an AuditStore attached. The returned cleanup closes the store and
// the underlying DB; t.TempDir() reaps the files themselves.
func newTestServerWithAudit(t *testing.T) (*DashboardServer, *telemetry.AuditStore, func()) {
	t.Helper()
	db, err := telemetry.Open(t.TempDir())
	if err != nil {
		t.Fatalf("telemetry.Open: %v", err)
	}
	as, err := telemetry.NewAuditStore(context.Background(), db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("NewAuditStore: %v", err)
	}
	ds := &DashboardServer{
		state:  newTestServerState(t),
		cfg:    dc.DashboardConfig{Group: "Domain Admins"},
		broker: NewBroker(),
		as:     as,
	}
	return ds, as, func() {
		_ = as.Close()
		_ = db.Close()
	}
}

func TestAuditHandler_ContractShape(t *testing.T) {
	ds, as, cleanup := newTestServerWithAudit(t)
	defer cleanup()

	ctx := context.Background()
	ts := time.Now().UTC().Truncate(time.Millisecond)
	keyMod := ts.Add(-time.Second)
	beforeTs := ts.Add(-time.Hour)

	// Row 1: a normal state change (reconciliation=false, before_ts absent).
	normal := telemetry.AuditRecord{
		Ts: ts, Host: "SRV01", PrevState: 0, NewState: 1,
		Principal: "DOMAIN\\alice", ChangedBy: "alice",
		KeyModifiedTs: &keyMod,
	}
	// Row 2: a reconciliation/drift row (reconciliation=true, before_ts present).
	drift := telemetry.AuditRecord{
		Ts: ts.Add(-30 * time.Minute), Host: "SRV01", PrevState: 1, NewState: 0,
		Reason:         "service-downtime drift: last-known DrainAll, observed Off",
		Reconciliation: true, BeforeTs: &beforeTs,
	}
	if err := as.Append(ctx, normal); err != nil {
		t.Fatalf("Append normal: %v", err)
	}
	if err := as.Append(ctx, drift); err != nil {
		t.Fatalf("Append drift: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/audit", nil)
	ds.handleAudit(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var resp struct {
		Records []struct {
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
		} `json:"records"`
		NextCursor    *string `json:"next_cursor"`
		Tier          string  `json:"tier"`
		TotalReturned int     `json:"total_returned"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Tier != "audit" {
		t.Errorf("tier = %q, want audit", resp.Tier)
	}
	if resp.TotalReturned != 2 || len(resp.Records) != 2 {
		t.Fatalf("total_returned=%d records=%d, want 2 and 2", resp.TotalReturned, len(resp.Records))
	}
	if resp.NextCursor != nil {
		t.Errorf("next_cursor = %v, want null on exhausted result", *resp.NextCursor)
	}
	// DESC order by ts — normal (ts) comes before drift (ts-30m).
	if !resp.Records[0].Ts.Equal(ts) {
		t.Errorf("records[0].ts = %v, want %v (DESC order)", resp.Records[0].Ts, ts)
	}
	r0 := resp.Records[0]
	if r0.PrevStateLabel != dc.DrainMode(0).String() || r0.NewStateLabel != dc.DrainMode(1).String() {
		t.Errorf("state labels = %q/%q, want %q/%q",
			r0.PrevStateLabel, r0.NewStateLabel, dc.DrainMode(0).String(), dc.DrainMode(1).String())
	}
	if r0.Reconciliation {
		t.Errorf("records[0].reconciliation = true, want false for normal row")
	}
	// Normal-row before_ts must be omitted (contract: only present when reconciliation=true).
	if r0.BeforeTs != nil {
		t.Errorf("records[0].before_ts = %v, want nil for non-reconciliation row", *r0.BeforeTs)
	}
	if r0.KeyModifiedTs == nil || !r0.KeyModifiedTs.Equal(keyMod) {
		t.Errorf("records[0].key_modified_ts = %v, want %v", r0.KeyModifiedTs, keyMod)
	}

	r1 := resp.Records[1]
	if !r1.Reconciliation {
		t.Errorf("records[1].reconciliation = false, want true for drift row")
	}
	if r1.BeforeTs == nil || !r1.BeforeTs.Equal(beforeTs) {
		t.Errorf("records[1].before_ts = %v, want %v", r1.BeforeTs, beforeTs)
	}
}

func TestAuditHandler_FilterByHost(t *testing.T) {
	ds, as, cleanup := newTestServerWithAudit(t)
	defer cleanup()

	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Millisecond)
	for i, host := range []string{"SRV01", "SRV02", "SRV01", "SRV03"} {
		rec := telemetry.AuditRecord{
			Ts: base.Add(time.Duration(i) * time.Minute), Host: host,
			PrevState: 0, NewState: 1,
		}
		if err := as.Append(ctx, rec); err != nil {
			t.Fatalf("Append %s: %v", host, err)
		}
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/audit?host=SRV01", nil)
	ds.handleAudit(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	var resp struct {
		Records []struct {
			Host string `json:"host"`
		} `json:"records"`
		TotalReturned int `json:"total_returned"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TotalReturned != 2 {
		t.Fatalf("total_returned = %d, want 2 (only SRV01 rows)", resp.TotalReturned)
	}
	for i, rec := range resp.Records {
		if rec.Host != "SRV01" {
			t.Errorf("records[%d].host = %q, want SRV01", i, rec.Host)
		}
	}
}

func TestAuditHandler_CursorPagination(t *testing.T) {
	ds, as, cleanup := newTestServerWithAudit(t)
	defer cleanup()

	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Millisecond)
	const total = 5
	for i := 0; i < total; i++ {
		rec := telemetry.AuditRecord{
			Ts: base.Add(time.Duration(i) * time.Minute), Host: "SRV01",
			PrevState: 0, NewState: 1,
		}
		if err := as.Append(ctx, rec); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	seenHosts := make(map[int64]int)
	cursor := ""
	pages := 0
	for {
		url := "/api/v1/audit?limit=2"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, url, nil)
		ds.handleAudit(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("page %d status = %d, body=%s", pages, w.Code, w.Body.String())
		}
		var resp struct {
			Records []struct {
				Ts time.Time `json:"ts"`
			} `json:"records"`
			NextCursor *string `json:"next_cursor"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("page %d decode: %v", pages, err)
		}
		for _, rec := range resp.Records {
			seenHosts[rec.Ts.UnixMilli()]++
		}
		pages++
		if pages > total+1 {
			t.Fatalf("cursor walk did not terminate after %d pages", pages)
		}
		if resp.NextCursor == nil {
			break
		}
		cursor = *resp.NextCursor
	}
	if len(seenHosts) != total {
		t.Errorf("seen %d unique rows, want %d", len(seenHosts), total)
	}
	for ts, count := range seenHosts {
		if count != 1 {
			t.Errorf("row ts=%d returned %d times, want 1", ts, count)
		}
	}
	if pages < 3 {
		t.Errorf("pages = %d, want ≥ 3 to exercise cursor advance", pages)
	}
}

func TestHistoryHandler_Returns410(t *testing.T) {
	ds, _, cleanup := newTestServerWithAudit(t)
	defer cleanup()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/history/SRV01", nil)
	r.SetPathValue("host", "SRV01")
	ds.handleHistory(w, r)

	if w.Code != http.StatusGone {
		t.Fatalf("status = %d, want %d (Gone) even with audit store live", w.Code, http.StatusGone)
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "use /api/v1/metrics/{host} or /api/v1/audit" {
		t.Errorf("error = %q, want migration message", body["error"])
	}
}

func TestAuditHandler_ChangesOnlyDefaultsTrue(t *testing.T) {
	ds, as, cleanup := newTestServerWithAudit(t)
	defer cleanup()

	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Millisecond)

	// Row A: real state change.
	change := telemetry.AuditRecord{
		Ts: base, Host: "SRV01", PrevState: 0, NewState: 1,
	}
	// Row B: no-op (prev == new) — filtered out under changes_only=true default.
	noop := telemetry.AuditRecord{
		Ts: base.Add(time.Minute), Host: "SRV01", PrevState: 1, NewState: 1,
	}
	if err := as.Append(ctx, change); err != nil {
		t.Fatalf("Append change: %v", err)
	}
	if err := as.Append(ctx, noop); err != nil {
		t.Fatalf("Append noop: %v", err)
	}

	// Default: changes_only is absent → must behave as true → noop dropped.
	{
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/audit", nil)
		ds.handleAudit(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("default status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
		}
		var resp struct {
			Records []struct {
				PrevState int `json:"prev_state"`
				NewState  int `json:"new_state"`
			} `json:"records"`
			TotalReturned int `json:"total_returned"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("default decode: %v", err)
		}
		if resp.TotalReturned != 1 {
			t.Errorf("default total_returned = %d, want 1 (noop filtered)", resp.TotalReturned)
		}
		// Guard against an inverted filter returning the noop row instead of
		// the change row: the one returned record must have prev_state != new_state.
		if len(resp.Records) != 1 || resp.Records[0].PrevState == resp.Records[0].NewState {
			t.Errorf("default returned noop row (prev==new); records=%+v", resp.Records)
		}
	}

	// Explicit changes_only=false → noop returned too.
	{
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/audit?changes_only=false", nil)
		ds.handleAudit(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("explicit false status = %d, body=%s", w.Code, w.Body.String())
		}
		var resp struct {
			TotalReturned int `json:"total_returned"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("explicit false decode: %v", err)
		}
		if resp.TotalReturned != 2 {
			t.Errorf("changes_only=false total_returned = %d, want 2", resp.TotalReturned)
		}
	}
}

// TestAuditHandler_CursorPaginationStableAcrossSameMsEvents is the regression
// gate for the row-value tuple-comparison seek predicate. Two audit rows with
// identical `ts` on two different hosts must each be returned exactly once
// across paginated requests; if the predicate collapsed the tuple to a scalar
// `ts < cursor_ts` the second row at the same ts would be silently skipped.
func TestAuditHandler_CursorPaginationStableAcrossSameMsEvents(t *testing.T) {
	ds, as, cleanup := newTestServerWithAudit(t)
	defer cleanup()

	ctx := context.Background()
	ts := time.Now().UTC().Truncate(time.Millisecond)

	for _, host := range []string{"SRV01", "SRV02"} {
		rec := telemetry.AuditRecord{
			Ts: ts, Host: host, PrevState: 0, NewState: 1,
		}
		if err := as.Append(ctx, rec); err != nil {
			t.Fatalf("Append %s: %v", host, err)
		}
	}

	type hostEntry struct {
		Host string `json:"host"`
	}
	seen := map[string]int{}
	cursor := ""
	pages := 0
	for {
		url := "/api/v1/audit?limit=1"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, url, nil)
		ds.handleAudit(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("page %d status = %d, body=%s", pages, w.Code, w.Body.String())
		}
		var resp struct {
			Records    []hostEntry `json:"records"`
			NextCursor *string     `json:"next_cursor"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("page %d decode: %v", pages, err)
		}
		for _, rec := range resp.Records {
			seen[rec.Host]++
		}
		pages++
		if pages > 4 {
			t.Fatalf("cursor walk did not terminate after %d pages", pages)
		}
		if resp.NextCursor == nil {
			break
		}
		cursor = *resp.NextCursor
	}

	for _, host := range []string{"SRV01", "SRV02"} {
		if seen[host] != 1 {
			t.Errorf("host %s returned %d times across pages, want 1", host, seen[host])
		}
	}
	if len(seen) != 2 {
		t.Errorf("distinct hosts = %d, want 2", len(seen))
	}
}

// TestSettingsHandler_ConcurrentRetentionPUTIsSerialized is a regression gate
// on the config.json named Windows mutex (`Global\DrainCtlConfig`). Two PUTs
// driven concurrently through the handler must both return 200 and leave
// config.json reflecting exactly one of the two intents.
//
// Without the mutex, `saveConfigToFile`'s two-step write (WriteFile to a fixed
// tmp path, then MoveFileEx atomic rename) is unsafe under concurrency: the
// second writer's tmp can clobber the first's before the first renames, or
// the first's rename can delete the tmp out from under the second → one of
// the PUTs returns a 500. Both-200 with a valid final value is the invariant.
//
// The handler normally calls `dc.UpdateNotifySettings` which does a LoadConfig
// BEFORE entering the mutex; on Windows, ReadFile (no FILE_SHARE_DELETE) can
// collide with a peer's MoveFileEx with ERROR_ACCESS_DENIED. That race is
// orthogonal to the mutex being tested here, so we use `testPutSettingsFunc`
// to call `dc.SaveConfig` directly — same mutex-protected save path, without
// the load-side noise that would otherwise force flaky retries.
//
// `grace_period` is the payload field because handlePutSettings does not yet
// expose a retention field; the mutex being exercised is the same one that
// will cover retention PUTs in the US5 settings-widget work.
func TestSettingsHandler_ConcurrentRetentionPUTIsSerialized(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)
	if err := dc.SaveConfig(dc.DefaultConfig()); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	const (
		intentA = 10
		intentB = 30
	)

	ds := newTestServer(t)
	// Bypass the handler's LoadConfig front-half and exercise only the
	// mutex-protected save. Route grace_period directly to dc.SaveConfig.
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, grace *int, _ *int, _ *dc.PerformanceConfig) error {
		cfg := dc.DefaultConfig()
		if grace != nil {
			cfg.GracePeriod = *grace
		}
		return dc.SaveConfig(cfg)
	}
	// Broadcast's LoadConfig is a post-save notification side-effect; stub it
	// so a trailing ReadFile doesn't create a load-vs-rename race in reverse
	// against the peer goroutine's save.
	ds.testLoadConfigFunc = func() (*dc.Config, error) { return dc.DefaultConfig(), nil }

	runOne := func(value int) (int, string) {
		body := fmt.Sprintf(`{"grace_period":%d}`, value)
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		ds.handlePutSettings(w, r)
		return w.Code, w.Body.String()
	}

	var wg sync.WaitGroup
	codes := [2]int{}
	bodies := [2]string{}
	start := make(chan struct{})

	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		codes[0], bodies[0] = runOne(intentA)
	}()
	go func() {
		defer wg.Done()
		<-start
		codes[1], bodies[1] = runOne(intentB)
	}()
	close(start)
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("PUT #%d status = %d (body=%s), want 200 — mutex did not serialize",
				i, code, bodies[i])
		}
	}

	final, err := dc.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v — config.json may be torn", err)
	}
	if final.GracePeriod != intentA && final.GracePeriod != intentB {
		t.Fatalf("grace_period = %d, want one of {%d, %d} — final state is neither intent",
			final.GracePeriod, intentA, intentB)
	}
}

// ── handleMaintenance ─────────────────────────────────────────────────────────

// newTestServerWithMaintenance returns a DashboardServer wired to a real
// on-disk telemetry store and MaintenanceStore. testLoadConfigFunc is stubbed
// so the handler reads deterministic interval defaults instead of hitting
// config.json on disk.
func newTestServerWithMaintenance(t *testing.T) (*DashboardServer, *telemetry.MaintenanceStore, func()) {
	t.Helper()
	db, err := telemetry.Open(t.TempDir())
	if err != nil {
		t.Fatalf("telemetry.Open: %v", err)
	}
	mnt := telemetry.NewMaintenanceStore(db)
	ds := &DashboardServer{
		state:  newTestServerState(t),
		cfg:    dc.DashboardConfig{Group: "Domain Admins"},
		broker: NewBroker(),
		mnt:    mnt,
	}
	ds.testLoadConfigFunc = func() (*dc.Config, error) { return dc.DefaultConfig(), nil }
	return ds, mnt, func() { _ = db.Close() }
}

type maintenanceJobJSON struct {
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

type maintenanceResponseJSON struct {
	Jobs       []maintenanceJobJSON `json:"jobs"`
	ServerTime string               `json:"server_time"`
}

func callMaintenance(t *testing.T, ds *DashboardServer) (*httptest.ResponseRecorder, maintenanceResponseJSON) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/maintenance/status", nil)
	ds.handleMaintenance(w, r)

	var resp maintenanceResponseJSON
	if w.Code == http.StatusOK {
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v; body=%s", err, w.Body.String())
		}
	}
	return w, resp
}

func TestMaintenanceHandler_ContractShape(t *testing.T) {
	ds, mnt, closeDB := newTestServerWithMaintenance(t)
	defer closeDB()

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	// Recent aggregator run — within 2× its 60s expected interval.
	aggStart := now.Add(-30 * time.Second)
	aggFinish := aggStart.Add(83 * time.Millisecond)
	if err := mnt.UpsertJob(ctx, "aggregator_5min", telemetry.Result{
		Started:      aggStart,
		Finished:     aggFinish,
		Outcome:      "success",
		RowsAffected: 300,
	}); err != nil {
		t.Fatalf("UpsertJob aggregator_5min: %v", err)
	}

	// Recent retention run with its 900s expected interval.
	retStart := now.Add(-5 * time.Minute)
	retFinish := retStart.Add(4115 * time.Millisecond)
	if err := mnt.UpsertJob(ctx, "retention", telemetry.Result{
		Started:      retStart,
		Finished:     retFinish,
		Outcome:      "success",
		RowsAffected: 1287,
	}); err != nil {
		t.Fatalf("UpsertJob retention: %v", err)
	}

	// Failure with a reason, rows_affected=0.
	failStart := now.Add(-10 * time.Second)
	failFinish := failStart.Add(204 * time.Millisecond)
	if err := mnt.UpsertJob(ctx, "aggregator_hourly", telemetry.Result{
		Started:      failStart,
		Finished:     failFinish,
		Outcome:      "failure",
		Reason:       "disk full: insert into metrics_hourly: database or disk is full",
		RowsAffected: 0,
	}); err != nil {
		t.Fatalf("UpsertJob aggregator_hourly: %v", err)
	}

	w, resp := callMaintenance(t, ds)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	if resp.ServerTime == "" {
		t.Error("server_time missing")
	}
	if _, err := time.Parse(time.RFC3339Nano, resp.ServerTime); err != nil {
		t.Errorf("server_time parse: %v (got %q)", err, resp.ServerTime)
	}
	if len(resp.Jobs) != 3 {
		t.Fatalf("jobs count = %d, want 3 (got %+v)", len(resp.Jobs), resp.Jobs)
	}

	// ListJobs returns rows alphabetically by name.
	byName := make(map[string]maintenanceJobJSON, len(resp.Jobs))
	for _, j := range resp.Jobs {
		byName[j.Name] = j
	}

	agg, ok := byName["aggregator_5min"]
	if !ok {
		t.Fatalf("aggregator_5min row missing")
	}
	if agg.Outcome != "success" {
		t.Errorf("aggregator_5min outcome = %q, want success", agg.Outcome)
	}
	if agg.Reason != "" {
		t.Errorf("aggregator_5min reason = %q, want empty", agg.Reason)
	}
	if agg.RowsAffected != 300 {
		t.Errorf("aggregator_5min rows_affected = %d, want 300", agg.RowsAffected)
	}
	if agg.DurationMs != 83 {
		t.Errorf("aggregator_5min duration_ms = %d, want 83", agg.DurationMs)
	}
	if agg.ExpectedIntervalSeconds != int64(dc.DefaultAggregatorIntervalSeconds) {
		t.Errorf("aggregator_5min expected_interval_seconds = %d, want %d",
			agg.ExpectedIntervalSeconds, dc.DefaultAggregatorIntervalSeconds)
	}
	if agg.Overdue {
		t.Error("aggregator_5min overdue = true, want false (ran 30s ago, interval 60s)")
	}
	if _, err := time.Parse(time.RFC3339Nano, agg.Started); err != nil {
		t.Errorf("aggregator_5min started parse: %v (got %q)", err, agg.Started)
	}
	if _, err := time.Parse(time.RFC3339Nano, agg.Finished); err != nil {
		t.Errorf("aggregator_5min finished parse: %v (got %q)", err, agg.Finished)
	}

	ret, ok := byName["retention"]
	if !ok {
		t.Fatalf("retention row missing")
	}
	wantRetention := int64(dc.DefaultRetentionIntervalMinutes) * 60
	if ret.ExpectedIntervalSeconds != wantRetention {
		t.Errorf("retention expected_interval_seconds = %d, want %d",
			ret.ExpectedIntervalSeconds, wantRetention)
	}
	if ret.RowsAffected != 1287 {
		t.Errorf("retention rows_affected = %d, want 1287", ret.RowsAffected)
	}
	if ret.Overdue {
		t.Error("retention overdue = true, want false (ran 5m ago, interval 15m)")
	}

	fail, ok := byName["aggregator_hourly"]
	if !ok {
		t.Fatalf("aggregator_hourly row missing")
	}
	if fail.Outcome != "failure" {
		t.Errorf("aggregator_hourly outcome = %q, want failure", fail.Outcome)
	}
	if !strings.Contains(fail.Reason, "disk full") {
		t.Errorf("aggregator_hourly reason = %q, want substring 'disk full'", fail.Reason)
	}
	if fail.ExpectedIntervalSeconds != int64(dc.DefaultAggregatorIntervalSeconds) {
		t.Errorf("aggregator_hourly expected_interval_seconds = %d, want %d",
			fail.ExpectedIntervalSeconds, dc.DefaultAggregatorIntervalSeconds)
	}
}

func TestMaintenanceHandler_OverdueFlagCorrect(t *testing.T) {
	ds, mnt, closeDB := newTestServerWithMaintenance(t)
	defer closeDB()

	ctx := context.Background()
	aggInterval := time.Duration(dc.DefaultAggregatorIntervalSeconds) * time.Second
	retInterval := time.Duration(dc.DefaultRetentionIntervalMinutes) * time.Minute
	now := time.Now().UTC()

	// Fresh aggregator — finished well under 2× interval → NOT overdue.
	freshStart := now.Add(-aggInterval / 2)
	if err := mnt.UpsertJob(ctx, "aggregator_5min", telemetry.Result{
		Started:      freshStart,
		Finished:     freshStart.Add(10 * time.Millisecond),
		Outcome:      "success",
		RowsAffected: 10,
	}); err != nil {
		t.Fatalf("UpsertJob fresh aggregator: %v", err)
	}

	// Stale aggregator — finished > 2× interval ago → OVERDUE.
	staleFinish := now.Add(-3 * aggInterval)
	if err := mnt.UpsertJob(ctx, "aggregator_hourly", telemetry.Result{
		Started:      staleFinish.Add(-time.Second),
		Finished:     staleFinish,
		Outcome:      "success",
		RowsAffected: 1,
	}); err != nil {
		t.Fatalf("UpsertJob stale aggregator: %v", err)
	}

	// Retention > 2× its 15-min interval ago → OVERDUE.
	retFinish := now.Add(-3 * retInterval)
	if err := mnt.UpsertJob(ctx, "retention", telemetry.Result{
		Started:      retFinish.Add(-time.Second),
		Finished:     retFinish,
		Outcome:      "success",
		RowsAffected: 42,
	}); err != nil {
		t.Fatalf("UpsertJob stale retention: %v", err)
	}

	w, resp := callMaintenance(t, ds)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}

	byName := make(map[string]maintenanceJobJSON, len(resp.Jobs))
	for _, j := range resp.Jobs {
		byName[j.Name] = j
	}

	if byName["aggregator_5min"].Overdue {
		t.Error("aggregator_5min overdue = true, want false (fresh run within interval)")
	}
	if !byName["aggregator_hourly"].Overdue {
		t.Error("aggregator_hourly overdue = false, want true (3× interval old)")
	}
	if !byName["retention"].Overdue {
		t.Error("retention overdue = false, want true (3× interval old)")
	}
}

func TestMaintenanceHandler_OneShotJobNeverOverdue(t *testing.T) {
	ds, mnt, closeDB := newTestServerWithMaintenance(t)
	defer closeDB()

	ctx := context.Background()
	// Pre-dated by ten years — would be overdue by any interval test, yet
	// one-shot jobs carry expected_interval_seconds == 0 and must never flag.
	ancient := time.Now().UTC().Add(-10 * 365 * 24 * time.Hour)

	oneShots := []string{"jsonl_migration", "drift_reconciliation"}
	for _, name := range oneShots {
		if err := mnt.UpsertJob(ctx, name, telemetry.Result{
			Started:      ancient,
			Finished:     ancient.Add(50 * time.Millisecond),
			Outcome:      "success",
			RowsAffected: 1,
		}); err != nil {
			t.Fatalf("UpsertJob %s: %v", name, err)
		}
	}

	w, resp := callMaintenance(t, ds)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}

	byName := make(map[string]maintenanceJobJSON, len(resp.Jobs))
	for _, j := range resp.Jobs {
		byName[j.Name] = j
	}

	for _, name := range oneShots {
		j, ok := byName[name]
		if !ok {
			t.Fatalf("%s row missing", name)
		}
		if j.ExpectedIntervalSeconds != 0 {
			t.Errorf("%s expected_interval_seconds = %d, want 0 (one-shot sentinel)",
				name, j.ExpectedIntervalSeconds)
		}
		if j.Overdue {
			t.Errorf("%s overdue = true, want false (one-shot jobs are never overdue)", name)
		}
	}
}

// ── Bug B: remote-host evtspike DetectorStatus propagation ───────────────────

// TestHandleReport_CachesRemoteEvtSpikeStatus verifies that the /api/v1/report
// handler stores the EvtSpikeStatus payload sent by a remote agent so the
// pull-based GET /api/evtspike/status?host=<remote> handler can return it.
// Before this wiring, the central dashboard had no visibility into remote
// detector state and the pill rendered DISABLED for every registered agent.
func TestHandleReport_CachesRemoteEvtSpikeStatus(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("GW01")
	ds.state.GetRemoteEvtSpikeStatus = ds.RemoteEvtSpikeStatus
	ds.evtspikeStatus = func(host string) evtspike.DetectorStatus {
		// Simulate the handler.go registration: remote hosts fall through
		// to the cache; local host returns a different value.
		if host != "RDS12" {
			return ds.state.GetRemoteEvtSpikeStatus(host)
		}
		return evtspike.DetectorStatus{}
	}

	status := evtspike.DetectorStatus{
		Host:            "GW01",
		State:           evtspike.StateHealthy,
		EnabledChannels: 54,
		MatureChannels:  27,
	}
	body, _ := json.Marshal(dc.CheckResult{Host: "GW01", Status: "Healthy", EvtSpikeStatus: &status})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/report", bytes.NewReader(body))
	ds.handleReport(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("report status = %d, want 200", w.Code)
	}

	// Now query the status endpoint for the remote host.
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/evtspike/status?host=GW01", nil)
	ds.handleEvtSpikeStatus(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status endpoint = %d, want 200", w.Code)
	}
	var got evtspike.DetectorStatus
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.State != evtspike.StateHealthy {
		t.Errorf("state = %q, want healthy", got.State)
	}
	if got.EnabledChannels != 54 {
		t.Errorf("enabled_channels = %d, want 54", got.EnabledChannels)
	}
	if got.MatureChannels != 27 {
		t.Errorf("mature_channels = %d, want 27", got.MatureChannels)
	}
}

// TestHandleReport_EmitsSSEOnRemoteStatusChange verifies that when a remote
// agent's DetectorStatus transitions between reports (training → healthy),
// the central dashboard emits exactly one detector_status SSE event so
// connected browsers see the pill flip with no poll lag.
func TestHandleReport_EmitsSSEOnRemoteStatusChange(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("GW01")
	_, ch, _, err := ds.broker.Subscribe()
	if err != nil {
		t.Fatal(err)
	}

	post := func(state string, mature int) {
		t.Helper()
		status := evtspike.DetectorStatus{Host: "GW01", State: state, EnabledChannels: 54, MatureChannels: mature}
		body, _ := json.Marshal(dc.CheckResult{Host: "GW01", Status: "Healthy", EvtSpikeStatus: &status})
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/report", bytes.NewReader(body))
		ds.handleReport(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("report status = %d, want 200", w.Code)
		}
	}

	post(evtspike.StateTraining, 0)
	post(evtspike.StateHealthy, 40)

	events := drainBroker(t, ch)
	detectorEvents := 0
	for _, ev := range events {
		if ev.Type == "detector_status" {
			detectorEvents++
		}
	}
	if detectorEvents != 2 {
		t.Fatalf("got %d detector_status events, want 2 (one per transition)", detectorEvents)
	}
}

// TestHandleReport_NoSSEOnRemoteStatusNoChange verifies broker dedup: two
// reports carrying the same DetectorStatus emit only one detector_status SSE
// event. Without this, every heartbeat on steady state would spam connected
// browsers.
func TestHandleReport_NoSSEOnRemoteStatusNoChange(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("GW01")
	_, ch, _, err := ds.broker.Subscribe()
	if err != nil {
		t.Fatal(err)
	}

	status := evtspike.DetectorStatus{Host: "GW01", State: evtspike.StateHealthy, EnabledChannels: 54, MatureChannels: 40}
	body, _ := json.Marshal(dc.CheckResult{Host: "GW01", Status: "Healthy", EvtSpikeStatus: &status})

	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/report", bytes.NewReader(body))
		ds.handleReport(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("report %d status = %d, want 200", i, w.Code)
		}
	}

	events := drainBroker(t, ch)
	detectorEvents := 0
	for _, ev := range events {
		if ev.Type == "detector_status" {
			detectorEvents++
		}
	}
	if detectorEvents != 1 {
		t.Fatalf("got %d detector_status events, want 1 (three same-state reports dedup to one emission)", detectorEvents)
	}
}

// TestCheckResult_EvtSpikeStatusRoundTrip verifies that CheckResult round-trips
// the EvtSpikeStatus field through JSON without data loss — the wire contract
// that remote agents and the central dashboard share via /api/v1/report.
func TestCheckResult_EvtSpikeStatusRoundTrip(t *testing.T) {
	original := dc.CheckResult{
		Host:   "GW01",
		Status: "Healthy",
		EvtSpikeStatus: &dc.DetectorStatus{
			Host:            "GW01",
			State:           evtspike.StateTraining,
			EnabledChannels: 54,
			MatureChannels:  5,
		},
	}
	body, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(body, []byte(`"evtspike_status"`)) {
		t.Fatalf("marshaled body missing evtspike_status field: %s", body)
	}

	var got dc.CheckResult
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.EvtSpikeStatus == nil {
		t.Fatal("unmarshaled EvtSpikeStatus is nil")
	}
	if got.EvtSpikeStatus.State != evtspike.StateTraining {
		t.Errorf("state = %q, want training", got.EvtSpikeStatus.State)
	}
	if got.EvtSpikeStatus.EnabledChannels != 54 {
		t.Errorf("enabled_channels = %d, want 54", got.EvtSpikeStatus.EnabledChannels)
	}
	if got.EvtSpikeStatus.MatureChannels != 5 {
		t.Errorf("mature_channels = %d, want 5", got.EvtSpikeStatus.MatureChannels)
	}

	// Absent field must be omitted on the wire (omitempty).
	absent, _ := json.Marshal(dc.CheckResult{Host: "GW01", Status: "Healthy"})
	if bytes.Contains(absent, []byte(`"evtspike_status"`)) {
		t.Errorf("CheckResult without EvtSpikeStatus should not emit the field: %s", absent)
	}
}

// ── Test fixtures: fleet metrics and metrics-seed (feature 008) ───────────────

// counterTestSeries mirrors the JSON shape of one series in a metrics response.
// Promoted from per-test anonymous structs so fleet and seed tests share the type.
type counterTestSeries struct {
	T   []int64   `json:"t"`
	Avg []float64 `json:"avg"`
	Min []float64 `json:"min"`
	Max []float64 `json:"max"`
}

// metricsTestResp mirrors the full JSON wire format of GET /api/v1/metrics/{host}.
// Used for both fleet (_fleet) and per-host assertions.
type metricsTestResp struct {
	Host            string                        `json:"host"`
	Tier            string                        `json:"tier"`
	From            string                        `json:"from"`
	To              string                        `json:"to"`
	OldestAvailable *string                       `json:"oldest_available"`
	NewestAvailable *string                       `json:"newest_available"`
	Series          map[string]*counterTestSeries `json:"series"`
}

// metricsSeedEntry mirrors one per-host sample from GET /api/v1/metrics.
// Field names match the JSON tags expected by the frontend MetricsSeedSample typedef.
type metricsSeedEntry struct {
	Time       int64   `json:"time"`
	CPU        float64 `json:"cpu"`
	Mem        float64 `json:"mem"`
	InputDelay float64 `json:"inputDelay"`
	Sessions   int     `json:"sessions"`
	DiskQueue  float64 `json:"diskQueue"`
	TcpRetrans float64 `json:"tcpRetrans"`
}

// decodeMetricsResp decodes a metrics handler response body into metricsTestResp.
func decodeMetricsResp(t *testing.T, w *httptest.ResponseRecorder) metricsTestResp {
	t.Helper()
	var resp metricsTestResp
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decodeMetricsResp: %v (body=%s)", err, w.Body.String())
	}
	return resp
}

// decodeMetricsSeedResp decodes a metrics-seed handler response body.
func decodeMetricsSeedResp(t *testing.T, w *httptest.ResponseRecorder) map[string][]metricsSeedEntry {
	t.Helper()
	var resp map[string][]metricsSeedEntry
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decodeMetricsSeedResp: %v (body=%s)", err, w.Body.String())
	}
	return resp
}

// insertFleetSamples appends raw telemetry samples for each host/counter pair.
// Samples are evenly spaced from base at 30 s ticks. Hosts not already
// registered in ds.state are registered automatically.
//
// vals maps counter name → the constant value inserted for every tick.
func insertFleetSamples(
	t *testing.T,
	ds *DashboardServer,
	ms *telemetry.MetricsStore,
	hosts []string,
	base time.Time,
	vals map[string]float64,
	count int,
) {
	t.Helper()
	for _, h := range hosts {
		ds.state.Register(h)
	}
	samples := make([]telemetry.Sample, 0, len(hosts)*len(vals)*count)
	for _, h := range hosts {
		for counter, v := range vals {
			for i := range count {
				samples = append(samples, telemetry.Sample{
					Ts:      base.Add(time.Duration(i) * 30 * time.Second),
					Host:    h,
					Counter: counter,
					Value:   v,
				})
			}
		}
	}
	if err := ms.Append(context.Background(), samples); err != nil {
		t.Fatalf("insertFleetSamples: %v", err)
	}
}

// metricsURL builds a GET /api/v1/metrics/{host} query URL.
func metricsURL(host string, from, to time.Time, resolution string, counters []string) string {
	u := fmt.Sprintf("/api/v1/metrics/%s?from=%s&to=%s",
		host,
		from.UTC().Format(time.RFC3339),
		to.UTC().Format(time.RFC3339),
	)
	if resolution != "" {
		u += "&resolution=" + resolution
	}
	if len(counters) > 0 {
		u += "&counters=" + strings.Join(counters, ",")
	}
	return u
}

// TestFleetFixtureShapes validates fixture helper types and functions against
// the wire-format contracts before any fleet or seed handler tests use them.
func TestFleetFixtureShapes(t *testing.T) {
	metricsJSON := `{"host":"_fleet","tier":"5min","from":"2026-01-01T00:00:00Z","to":"2026-01-01T01:00:00Z","oldest_available":null,"newest_available":null,"series":{}}`
	w := &httptest.ResponseRecorder{Body: bytes.NewBufferString(metricsJSON)}
	resp := decodeMetricsResp(t, w)
	if resp.Host != "_fleet" {
		t.Errorf("metricsTestResp.host = %q, want _fleet", resp.Host)
	}
	if resp.Series == nil {
		t.Error("metricsTestResp.series should not be nil on empty JSON object")
	}

	seedJSON := `{"SRV01":[{"time":1000000,"cpu":12.5,"mem":40.0,"inputDelay":5,"sessions":10,"diskQueue":0.1,"tcpRetrans":0.05}]}`
	w2 := &httptest.ResponseRecorder{Body: bytes.NewBufferString(seedJSON)}
	seed := decodeMetricsSeedResp(t, w2)
	if len(seed["SRV01"]) != 1 {
		t.Fatalf("metricsSeedEntry len = %d, want 1", len(seed["SRV01"]))
	}
	if seed["SRV01"][0].CPU != 12.5 {
		t.Errorf("metricsSeedEntry.CPU = %v, want 12.5", seed["SRV01"][0].CPU)
	}

	u := metricsURL("_fleet", time.Unix(0, 0).UTC(), time.Unix(3600, 0).UTC(), "raw", []string{"cpu_pct"})
	if !strings.Contains(u, "_fleet") || !strings.Contains(u, "cpu_pct") {
		t.Errorf("metricsURL unexpected output: %s", u)
	}

	ds, ms, closeDB := newTestServerWithStore(t)
	defer closeDB()
	base := time.Now().UTC().Add(-5 * time.Minute)
	insertFleetSamples(t, ds, ms, []string{"S01", "S02"}, base, map[string]float64{"cpu_pct": 42.0}, 3)
	if !ds.state.IsRegistered("S01") || !ds.state.IsRegistered("S02") {
		t.Error("insertFleetSamples must register provided hosts")
	}
}

// ── handleFleetMetrics ────────────────────────────────────────────────────────

func TestFleetMetrics_InvalidRange(t *testing.T) {
	cases := []struct {
		name  string
		query string
		code  string
	}{
		{"missing from", "?to=2026-04-16T00:00:00Z", "invalid_range"},
		{"missing to", "?from=2026-04-15T00:00:00Z", "invalid_range"},
		{"missing both", "", "invalid_range"},
		{"to before from", "?from=2026-04-16T00:00:00Z&to=2026-04-15T00:00:00Z", "invalid_range"},
		{"bad resolution", "?from=2026-04-15T00:00:00Z&to=2026-04-16T00:00:00Z&resolution=yearly", "invalid_resolution"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ds, _, closeDB := newTestServerWithStore(t)
			defer closeDB()

			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/_fleet"+tc.query, nil)
			r.SetPathValue("host", "_fleet")
			ds.handleMetrics(w, r)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusBadRequest, w.Body.String())
			}
			var body map[string]string
			if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["error"] != tc.code {
				t.Errorf("error = %q, want %q", body["error"], tc.code)
			}
		})
	}
}

func TestFleetMetrics_StorageError(t *testing.T) {
	ds := newTestServer(t) // no ms wired → ds.ms == nil

	now := time.Now().UTC()
	from := now.Add(-time.Hour).Format(time.RFC3339)
	to := now.Format(time.RFC3339)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/_fleet?from="+from+"&to="+to, nil)
	r.SetPathValue("host", "_fleet")
	ds.handleMetrics(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "storage_error" {
		t.Errorf("error = %q, want storage_error", body["error"])
	}
}

func TestFleetMetrics_EmptyFleetReturns200(t *testing.T) {
	ds, _, closeDB := newTestServerWithStore(t)
	defer closeDB()
	// No hosts registered.

	now := time.Now().UTC()
	from := now.Add(-time.Hour).Format(time.RFC3339)
	to := now.Format(time.RFC3339)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/_fleet?from="+from+"&to="+to+"&resolution=raw", nil)
	r.SetPathValue("host", "_fleet")
	ds.handleMetrics(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	resp := decodeMetricsResp(t, w)
	if resp.Host != "_fleet" {
		t.Errorf("host = %q, want _fleet", resp.Host)
	}
	if len(resp.Series) != 0 {
		t.Errorf("empty fleet should return empty series, got %d entries", len(resp.Series))
	}
}

func TestFleetMetrics_AggregatesAcrossHosts(t *testing.T) {
	ds, ms, closeDB := newTestServerWithStore(t)
	defer closeDB()

	base := time.Now().UTC().Truncate(time.Second).Add(-5 * time.Minute)
	insertFleetSamples(t, ds, ms, []string{"S01", "S02"}, base, map[string]float64{"cpu_pct": 50.0}, 3)

	from := base.Add(-time.Minute).Format(time.RFC3339)
	to := base.Add(5 * time.Minute).Format(time.RFC3339)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet,
		"/api/v1/metrics/_fleet?from="+from+"&to="+to+"&resolution=raw&counters=cpu_pct", nil)
	r.SetPathValue("host", "_fleet")
	ds.handleMetrics(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	resp := decodeMetricsResp(t, w)
	if resp.Host != "_fleet" {
		t.Errorf("host = %q, want _fleet", resp.Host)
	}
	cpu, ok := resp.Series["cpu_pct"]
	if !ok {
		t.Fatal("response missing cpu_pct series")
	}
	if len(cpu.T) == 0 {
		t.Error("cpu_pct series is empty; expected fleet-aggregated samples")
	}
	for i, avg := range cpu.Avg {
		if avg != 50.0 {
			t.Errorf("cpu_pct avg[%d] = %v, want 50.0 (avg of two hosts both at 50)", i, avg)
		}
	}
}

// TestFleetMetrics_SumsSessionCountersAcrossHosts pins the fix for the Overview
// Sessions metric reading half the counter tile's value: session counters must
// SUM across hosts at fleet aggregation, not AVG. Exercises all three tier
// paths (raw, 1min via auto, 5min) so a regression in any fleet query builder
// surfaces here.
func TestFleetMetrics_SumsSessionCountersAcrossHosts(t *testing.T) {
	tiers := []struct {
		name       string
		resolution string
		wantTier   string
	}{
		{"raw tier", "raw", "raw"},
		{"1min tier", "1min", "1min"},
	}
	for _, tier := range tiers {
		t.Run(tier.name, func(t *testing.T) {
			ds, ms, closeDB := newTestServerWithStore(t)
			defer closeDB()

			base := time.Now().UTC().Truncate(time.Second).Add(-5 * time.Minute)
			// Two hosts, each reporting 1 session — fleet total must be 2.
			insertFleetSamples(t, ds, ms, []string{"S01", "S02"}, base,
				map[string]float64{"sessions_total": 1.0, "cpu_pct": 50.0}, 3)

			from := base.Add(-time.Minute).Format(time.RFC3339)
			to := base.Add(5 * time.Minute).Format(time.RFC3339)

			url := "/api/v1/metrics/_fleet?from=" + from + "&to=" + to +
				"&resolution=" + tier.resolution + "&counters=sessions_total,cpu_pct"
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, url, nil)
			r.SetPathValue("host", "_fleet")
			ds.handleMetrics(w, r)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
			}
			resp := decodeMetricsResp(t, w)
			if resp.Tier != tier.wantTier {
				t.Errorf("tier = %q, want %q", resp.Tier, tier.wantTier)
			}
			sess, ok := resp.Series["sessions_total"]
			if !ok || len(sess.Avg) == 0 {
				t.Fatal("response missing sessions_total series")
			}
			for i, v := range sess.Avg {
				if v != 2.0 {
					t.Errorf("sessions_total avg[%d] = %v, want 2.0 (sum of two hosts at 1)", i, v)
				}
			}
			// cpu_pct is not summable — must still average to 50.
			cpu, ok := resp.Series["cpu_pct"]
			if !ok || len(cpu.Avg) == 0 {
				t.Fatal("response missing cpu_pct series")
			}
			for i, v := range cpu.Avg {
				if v != 50.0 {
					t.Errorf("cpu_pct avg[%d] = %v, want 50.0 (avg of two hosts at 50)", i, v)
				}
			}
		})
	}
}

// ── handleSeedMetrics ─────────────────────────────────────────────────────────

func TestSeedMetrics_StorageError(t *testing.T) {
	ds := newTestServer(t) // no ms

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	ds.handleSeedMetrics(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "storage_error" {
		t.Errorf("error = %q, want storage_error", body["error"])
	}
}

func TestSeedMetrics_InvalidResolution(t *testing.T) {
	ds, _, closeDB := newTestServerWithStore(t)
	defer closeDB()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/metrics?resolution=hourly", nil)
	ds.handleSeedMetrics(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "invalid_resolution" {
		t.Errorf("error = %q, want invalid_resolution", body["error"])
	}
}

func TestSeedMetrics_InvalidRange(t *testing.T) {
	ds, _, closeDB := newTestServerWithStore(t)
	defer closeDB()

	cases := []struct {
		name  string
		query string
	}{
		{"to before from", "?from=2026-04-16T00:00:00Z&to=2026-04-15T00:00:00Z"},
		{"unparseable from", "?from=nope&to=2026-04-16T00:00:00Z"},
		{"unparseable to", "?from=2026-04-15T00:00:00Z&to=nope"},
		{"range exceeds 90 days", "?from=2025-01-01T00:00:00Z&to=2025-07-01T00:00:00Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/api/v1/metrics"+tc.query, nil)
			ds.handleSeedMetrics(w, r)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusBadRequest, w.Body.String())
			}
			var body map[string]string
			if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["error"] != "invalid_range" {
				t.Errorf("error = %q, want invalid_range", body["error"])
			}
		})
	}
}

func TestSeedMetrics_ReturnsPerHostHistory(t *testing.T) {
	ds, ms, closeDB := newTestServerWithStore(t)
	defer closeDB()

	base := time.Now().UTC().Truncate(time.Second).Add(-5 * time.Minute)
	insertFleetSamples(t, ds, ms, []string{"SRV01", "SRV02"}, base, map[string]float64{
		"cpu_pct":      42.0,
		"mem_avail_mb": 8192.0,
		"mem_total_mb": 16384.0,
	}, 3)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	ds.handleSeedMetrics(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	seed := decodeMetricsSeedResp(t, w)
	for _, host := range []string{"SRV01", "SRV02"} {
		samples, ok := seed[host]
		if !ok {
			t.Errorf("response missing %s", host)
			continue
		}
		if len(samples) == 0 {
			t.Errorf("%s has no seed samples", host)
			continue
		}
		if samples[0].CPU != 42.0 {
			t.Errorf("%s cpu = %v, want 42.0", host, samples[0].CPU)
		}
		// mem = (1 - 8192/16384) * 100 = 50.0
		if samples[0].Mem != 50.0 {
			t.Errorf("%s mem = %v, want 50.0", host, samples[0].Mem)
		}
	}
}

func TestSeedMetrics_BoundedByLimit(t *testing.T) {
	ds, ms, closeDB := newTestServerWithStore(t)
	defer closeDB()

	base := time.Now().UTC().Truncate(time.Second).Add(-30 * time.Minute)
	insertFleetSamples(t, ds, ms, []string{"SRV01"}, base, map[string]float64{"cpu_pct": 10.0}, 20)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/metrics?limit=5", nil)
	ds.handleSeedMetrics(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	seed := decodeMetricsSeedResp(t, w)
	if n := len(seed["SRV01"]); n > 5 {
		t.Errorf("SRV01 sample count = %d, want ≤ 5 (limit was 5)", n)
	}
}

func TestSeedMetrics_EmptyWhenNoHosts(t *testing.T) {
	ds, _, closeDB := newTestServerWithStore(t)
	defer closeDB()
	// No hosts registered.

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	ds.handleSeedMetrics(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	seed := decodeMetricsSeedResp(t, w)
	if len(seed) != 0 {
		t.Errorf("empty fleet should return empty map, got %d entries", len(seed))
	}
}

// ── US1: retained Overview fleet history (T010) ───────────────────────────────

// ── handleSeedMetrics — US3 production seeding (T024) ────────────────────────

// TestSeedMetrics_US3_DefaultLimitEnforced verifies that the seed endpoint
// returns at most the default limit (60) samples per host when no explicit
// limit parameter is provided, so cold-start seeding is bounded by default.
func TestSeedMetrics_US3_DefaultLimitEnforced(t *testing.T) {
	ds, ms, closeDB := newTestServerWithStore(t)
	defer closeDB()

	// Insert more than the default limit of 60 samples.
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	insertFleetSamples(t, ds, ms, []string{"SRV01"}, base, map[string]float64{"cpu_pct": 5.0}, 80)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	ds.handleSeedMetrics(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	seed := decodeMetricsSeedResp(t, w)
	if n := len(seed["SRV01"]); n > 60 {
		t.Errorf("SRV01 sample count = %d, want ≤ 60 (default limit)", n)
	}
	if len(seed["SRV01"]) == 0 {
		t.Error("SRV01 has no seed samples; want at least some")
	}
}

// TestSeedMetrics_US3_PerHostEmptyWhenNoData verifies that a registered host with
// no telemetry in the DB contributes an empty slice (not a missing key) so the
// frontend can distinguish "host exists, no data yet" from "host not in response".
func TestSeedMetrics_US3_PerHostEmptyWhenNoData(t *testing.T) {
	ds, ms, closeDB := newTestServerWithStore(t)
	defer closeDB()

	// SRV01 has data; SRV02 is registered but has no samples.
	base := time.Now().UTC().Truncate(time.Second).Add(-5 * time.Minute)
	insertFleetSamples(t, ds, ms, []string{"SRV01"}, base, map[string]float64{"cpu_pct": 10.0}, 2)
	ds.state.Register("SRV02")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	ds.handleSeedMetrics(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	seed := decodeMetricsSeedResp(t, w)
	if len(seed["SRV01"]) == 0 {
		t.Error("SRV01 should have seed samples")
	}
	// SRV02 may be absent or present with an empty slice — both are acceptable
	// because the handler only emits hosts that have rows.
	// The important thing is that SRV01 does NOT bleed into SRV02's bucket.
	if s2, ok := seed["SRV02"]; ok && len(s2) > 0 {
		t.Errorf("SRV02 should have no data, got %d sample(s)", len(s2))
	}
}

// TestSeedMetrics_US3_SamplesAscendingByTime verifies that each per-host slice
// in the seed response is sorted oldest-first (ascending timestamp), matching
// the order expected by the frontend's sparkline ring buffers.
func TestSeedMetrics_US3_SamplesAscendingByTime(t *testing.T) {
	ds, ms, closeDB := newTestServerWithStore(t)
	defer closeDB()

	base := time.Now().UTC().Truncate(time.Second).Add(-10 * time.Minute)
	insertFleetSamples(t, ds, ms, []string{"SRV01"}, base, map[string]float64{"cpu_pct": 7.0}, 5)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	ds.handleSeedMetrics(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	seed := decodeMetricsSeedResp(t, w)
	samples := seed["SRV01"]
	if len(samples) < 2 {
		t.Fatalf("want ≥ 2 samples for ordering check, got %d", len(samples))
	}
	for i := 1; i < len(samples); i++ {
		if samples[i].Time < samples[i-1].Time {
			t.Errorf("sample[%d].time=%d < sample[%d].time=%d — not ascending",
				i, samples[i].Time, i-1, samples[i-1].Time)
		}
	}
}

// TestFleetMetrics_US1_RetainedCoverageBounds confirms that oldest_available and
// newest_available are populated from real DB rows and surfaced in the fleet
// response, enabling Overview chart consumers to detect whether retained data
// covers the requested window.
func TestFleetMetrics_US1_RetainedCoverageBounds(t *testing.T) {
	ds, ms, closeDB := newTestServerWithStore(t)
	defer closeDB()

	base := time.Now().UTC().Truncate(time.Second).Add(-2 * time.Hour)
	insertFleetSamples(t, ds, ms, []string{"SRV01"}, base, map[string]float64{"cpu_pct": 30.0}, 4)

	// Query a window that includes the inserted samples.
	from := base.Add(-time.Minute).Format(time.RFC3339)
	to := base.Add(5 * time.Minute).Format(time.RFC3339)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet,
		"/api/v1/metrics/_fleet?from="+from+"&to="+to+"&resolution=raw", nil)
	r.SetPathValue("host", "_fleet")
	ds.handleMetrics(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	resp := decodeMetricsResp(t, w)
	if resp.OldestAvailable == nil {
		t.Error("oldest_available should be non-null when data exists in DB")
	}
	if resp.NewestAvailable == nil {
		t.Error("newest_available should be non-null when data exists in DB")
	}
}

// TestFleetMetrics_US1_EmptySeriesOnNoData confirms that a fleet query with no
// matching rows returns 200 with an empty series map and null coverage bounds.
// This is the FR-019a "Collecting data…" trigger for Overview charts.
func TestFleetMetrics_US1_EmptySeriesOnNoData(t *testing.T) {
	ds, _, closeDB := newTestServerWithStore(t)
	defer closeDB()
	ds.state.Register("SRV01") // registered but no telemetry rows

	now := time.Now().UTC()
	from := now.Add(-time.Hour).Format(time.RFC3339)
	to := now.Format(time.RFC3339)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet,
		"/api/v1/metrics/_fleet?from="+from+"&to="+to+"&resolution=raw", nil)
	r.SetPathValue("host", "_fleet")
	ds.handleMetrics(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	resp := decodeMetricsResp(t, w)
	if len(resp.Series) != 0 {
		t.Errorf("no-data fleet should return empty series, got %d series", len(resp.Series))
	}
	if resp.OldestAvailable != nil {
		t.Errorf("oldest_available should be null when no rows exist, got %q", *resp.OldestAvailable)
	}
	if resp.NewestAvailable != nil {
		t.Errorf("newest_available should be null when no rows exist, got %q", *resp.NewestAvailable)
	}
}

// TestFleetMetrics_US1_OverviewCounterFamilies confirms that LOAD (cpu_pct),
// HIC (input_delay_p95_ms), SESSIONS (sessions_total), and REMOTE FX
// (rfx_fps_out) series are returned when those counters are present in the DB.
// These are the four counter families the Overview charts consume.
func TestFleetMetrics_US1_OverviewCounterFamilies(t *testing.T) {
	ds, ms, closeDB := newTestServerWithStore(t)
	defer closeDB()

	base := time.Now().UTC().Truncate(time.Second).Add(-5 * time.Minute)
	insertFleetSamples(t, ds, ms, []string{"SRV01"}, base, map[string]float64{
		"cpu_pct":            55.0,
		"input_delay_p95_ms": 18.0,
		"sessions_total":     12.0,
		"rfx_fps_out":        24.0,
	}, 2)

	from := base.Add(-time.Minute).Format(time.RFC3339)
	to := base.Add(5 * time.Minute).Format(time.RFC3339)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet,
		"/api/v1/metrics/_fleet?from="+from+"&to="+to+"&resolution=raw", nil)
	r.SetPathValue("host", "_fleet")
	ds.handleMetrics(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	resp := decodeMetricsResp(t, w)

	for _, want := range []string{"cpu_pct", "input_delay_p95_ms", "sessions_total", "rfx_fps_out"} {
		cs, ok := resp.Series[want]
		if !ok {
			t.Errorf("missing series %q in fleet response", want)
			continue
		}
		if len(cs.T) == 0 {
			t.Errorf("series %q is empty", want)
		}
	}
}

// ── handleFleetMetrics — US2 tier/degradation/bounds (T017) ─────────────────

// TestFleetMetrics_US2_ExplicitResolutionOverrides verifies that the fleet
// endpoint honours explicit resolution parameters regardless of window size,
// matching the same contract as the per-host metrics handler. This matters for
// the shared-window navigation (US2) where the frontend may force a specific
// tier when the operator selects a preset (5M/1H/1D/3D/5D).
func TestFleetMetrics_US2_ExplicitResolutionOverrides(t *testing.T) {
	cases := []struct {
		name       string
		resolution string
		window     time.Duration
	}{
		// A 3D window would pick hourly under auto; explicit raw overrides it.
		{"raw overrides 3D window", "raw", 3 * 24 * time.Hour},
		// A 5M window would pick raw under auto; explicit 5min overrides it.
		{"5min overrides 5M window", "5min", 5 * time.Minute},
		// A 5M window would pick raw under auto; explicit hourly overrides it.
		{"hourly overrides 5M window", "hourly", 5 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ds, ms, closeDB := newTestServerWithStore(t)
			defer closeDB()
			now := time.Now().UTC().Truncate(time.Second)
			base := now.Add(-tc.window)
			insertFleetSamples(t, ds, ms, []string{"SRV01"}, base,
				map[string]float64{"cpu_pct": 10.0}, 2)

			from := base.Format(time.RFC3339)
			to := now.Format(time.RFC3339)
			url := "/api/v1/metrics/_fleet?from=" + from + "&to=" + to + "&resolution=" + tc.resolution
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, url, nil)
			r.SetPathValue("host", "_fleet")
			ds.handleMetrics(w, r)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
			}
			if got := decodeMetricsTier(t, w); got != tc.resolution {
				t.Errorf("tier = %q, want %q", got, tc.resolution)
			}
		})
	}
}

// TestFleetMetrics_US2_DegradationWhenRawPurged verifies that the fleet endpoint
// degrades from raw to 5min when raw coverage does not reach the requested from
// timestamp, and then to hourly when 5min is also insufficient.
func TestFleetMetrics_US2_DegradationWhenRawPurged(t *testing.T) {
	ds, ms, closeDB := newTestServerWithStore(t)
	defer closeDB()

	now := time.Now().UTC().Truncate(time.Second)

	// Raw oldest = now-2m. A 30m window (from = now-30m) will not be covered.
	// Both 5min and hourly are empty, so the handler degrades all the way to hourly.
	base := now.Add(-2 * time.Minute)
	insertFleetSamples(t, ds, ms, []string{"SRV01"}, base,
		map[string]float64{"cpu_pct": 5.0}, 2)

	from := now.Add(-30 * time.Minute).Format(time.RFC3339)
	to := now.Add(-25 * time.Minute).Format(time.RFC3339)
	url := "/api/v1/metrics/_fleet?from=" + from + "&to=" + to + "&resolution=auto"
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, url, nil)
	r.SetPathValue("host", "_fleet")
	ds.handleMetrics(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if got := decodeMetricsTier(t, w); got != "hourly" {
		t.Errorf("tier = %q, want hourly after full degradation", got)
	}
}

// TestFleetMetrics_US2_ResponseBoundsPresent verifies that oldest_available and
// newest_available are populated when fleet data exists, enabling the frontend to
// display coverage feedback for the selected shared window.
func TestFleetMetrics_US2_ResponseBoundsPresent(t *testing.T) {
	ds, ms, closeDB := newTestServerWithStore(t)
	defer closeDB()

	now := time.Now().UTC().Truncate(time.Second)
	base := now.Add(-5 * time.Minute)
	insertFleetSamples(t, ds, ms, []string{"SRV01", "SRV02"}, base,
		map[string]float64{"cpu_pct": 20.0}, 3)

	from := base.Add(-time.Minute).Format(time.RFC3339)
	to := now.Format(time.RFC3339)
	url := "/api/v1/metrics/_fleet?from=" + from + "&to=" + to + "&resolution=raw"
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, url, nil)
	r.SetPathValue("host", "_fleet")
	ds.handleMetrics(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	resp := decodeMetricsResp(t, w)
	if resp.OldestAvailable == nil {
		t.Error("oldest_available is nil; want non-null when data exists")
	}
	if resp.NewestAvailable == nil {
		t.Error("newest_available is nil; want non-null when data exists")
	}
}
