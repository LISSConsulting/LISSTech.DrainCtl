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
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// newTestServer creates a DashboardServer backed by a temp directory.
// No TLS, no auth middleware — tests call handler methods directly.
func newTestServer(t *testing.T) *DashboardServer {
	t.Helper()
	return &DashboardServer{
		state:  NewServerState(t.TempDir()),
		cfg:    dc.DashboardConfig{Group: "Domain Admins"},
		broker: NewBroker(),
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
	ds.state.mu.Lock()
	ds.state.servers["SRV01"].LastSeen = time.Now().Add(-15 * time.Minute)
	ds.state.mu.Unlock()

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

	ds.state.mu.Lock()
	ds.state.servers["SRV01"].LastSeen = time.Now().Add(-20 * time.Minute)
	ds.state.servers["SRV02"].LastSeen = time.Now().Add(-11 * time.Minute)
	ds.state.mu.Unlock()

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

func TestHandleHistory_UnregisteredHostReturns404(t *testing.T) {
	ds := newTestServer(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/history/GHOST", nil)
	r.SetPathValue("host", "GHOST")
	ds.handleHistory(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandleHistory_RegisteredWithNoReportsReturnsEmptyArray(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/history/SRV01", nil)
	r.SetPathValue("host", "SRV01")
	ds.handleHistory(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var records []HistoryView
	if err := json.NewDecoder(w.Body).Decode(&records); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("len(records) = %d, want 0", len(records))
	}
}

func TestHandleHistory_ReturnsReportsNewestFirst(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")

	for i, status := range []string{"Healthy", "Grace", "Alert"} {
		ds.state.Update("SRV01", &dc.CheckResult{
			Host:      "SRV01",
			Status:    status,
			Timestamp: time.Unix(int64(1000+i), 0),
		})
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/history/SRV01", nil)
	r.SetPathValue("host", "SRV01")
	ds.handleHistory(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var records []HistoryView
	if err := json.NewDecoder(w.Body).Decode(&records); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("len(records) = %d, want 3", len(records))
	}
	// Newest first: alert, grace, ok (status normalised to lowercase tokens).
	if records[0].Status != "alert" {
		t.Errorf("records[0].Status = %q, want \"alert\"", records[0].Status)
	}
	if records[1].Status != "grace" {
		t.Errorf("records[1].Status = %q, want \"grace\"", records[1].Status)
	}
	if records[2].Status != "ok" {
		t.Errorf("records[2].Status = %q, want \"ok\"", records[2].Status)
	}
}

func TestHandleHistory_LimitQueryParam(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")

	for i := range 10 {
		ds.state.Update("SRV01", &dc.CheckResult{
			Host:      "SRV01",
			Status:    "Healthy",
			Timestamp: time.Unix(int64(1000+i), 0),
		})
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/history/SRV01?limit=3", nil)
	r.SetPathValue("host", "SRV01")
	ds.handleHistory(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var records []HistoryView
	if err := json.NewDecoder(w.Body).Decode(&records); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(records) != 3 {
		t.Errorf("len(records) = %d, want 3", len(records))
	}
}

func TestHandleHistory_InvalidLimitReturns400(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")

	for _, bad := range []string{"0", "-1", "abc", "101"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/history/SRV01?limit="+bad, nil)
		r.SetPathValue("host", "SRV01")
		ds.handleHistory(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("limit=%q: status = %d, want %d", bad, w.Code, http.StatusBadRequest)
		}
	}
}

func TestHandleHistory_RingBufferCapAtHistoryMax(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")

	// Insert more records than historyMax.
	for i := range historyMax + 10 {
		ds.state.Update("SRV01", &dc.CheckResult{
			Host:      "SRV01",
			Status:    "Healthy",
			Timestamp: time.Unix(int64(1000+i), 0),
		})
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/history/SRV01", nil)
	r.SetPathValue("host", "SRV01")
	ds.handleHistory(w, r)

	var records []HistoryView
	if err := json.NewDecoder(w.Body).Decode(&records); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(records) > historyMax {
		t.Errorf("len(records) = %d, want <= %d", len(records), historyMax)
	}
}

func TestHandleHistory_MissingHostParam(t *testing.T) {
	ds := newTestServer(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/history/", nil)
	// PathValue("host") returns "" — simulates missing param
	ds.handleHistory(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
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
	ds.testNotifyFunc = func() error {
		return fmt.Errorf("no notification targets configured")
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
	ds.testNotifyFunc = func() error {
		called = true
		return nil
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
	ds.testNotifyFunc = func() error {
		return fmt.Errorf("connection refused")
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
	ds.testNotifyFunc = func() error {
		return fmt.Errorf("failed to load config: open config.json: no such file or directory")
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
	ds.testNotifyFunc = func() error {
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
	ds.testNotifyFunc = func() error {
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
	if resp.Notifications[0].Secret != "s3cr3t" {
		t.Errorf("notifications[0].secret = %q, want s3cr3t", resp.Notifications[0].Secret)
	}
	if resp.Notifications[1].Type != "ntfy" {
		t.Errorf("notifications[1].type = %q, want ntfy", resp.Notifications[1].Type)
	}
}

// ── handlePutSettings ─────────────────────────────────────────────────────

func TestHandlePutSettings_Success_Returns200(t *testing.T) {
	ds := newTestServer(t)
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int) error {
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
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int) error {
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
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int) error {
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
	ds.testPutSettingsFunc = func(notifications *[]dc.NotificationTarget, threshold *int, grace *int) error {
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
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, threshold *int, grace *int) error {
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
	ds.testPutSettingsFunc = func(notifications *[]dc.NotificationTarget, _ *int, _ *int) error {
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

func TestHandlePutSettings_AbsentNotifications_PassedAsNil(t *testing.T) {
	ds := newTestServer(t)

	var capturedNotifs *[]dc.NotificationTarget
	ds.testPutSettingsFunc = func(notifications *[]dc.NotificationTarget, _ *int, _ *int) error {
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
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int) error {
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
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int) error {
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
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int) error {
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
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int) error {
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
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int) error {
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
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int) error {
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
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int) error {
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
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int) error {
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
	ds.testPutSettingsFunc = func(notifications *[]dc.NotificationTarget, _ *int, _ *int) error {
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
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int) error {
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
	ds.testNotifyFunc = func() error { return nil }

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

func TestHandleHistory_ChangesOnly_ReturnsOnlyTransitions(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")

	// 3 non-transition records + 2 transition records
	for i := range 3 {
		ds.state.Update("SRV01", &dc.CheckResult{
			Host:       "SRV01",
			Status:     "Healthy",
			Transition: false,
			Timestamp:  time.Unix(int64(1000+i), 0),
		})
	}
	ds.state.Update("SRV01", &dc.CheckResult{
		Host:           "SRV01",
		Status:         "Grace",
		Transition:     true,
		TransitionFrom: "Healthy",
		Timestamp:      time.Unix(1010, 0),
	})
	ds.state.Update("SRV01", &dc.CheckResult{
		Host:           "SRV01",
		Status:         "Alert",
		Transition:     true,
		TransitionFrom: "Grace",
		Timestamp:      time.Unix(1020, 0),
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/history/SRV01?changes_only=1", nil)
	r.SetPathValue("host", "SRV01")
	ds.handleHistory(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var records []HistoryView
	if err := json.NewDecoder(w.Body).Decode(&records); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2 (only transitions)", len(records))
	}
	for _, rec := range records {
		if !rec.Transition {
			t.Errorf("record %q has Transition=false, want true", rec.Status)
		}
	}
}

func TestHandleHistory_ChangesOnly_EmptyWhenNoTransitions(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")

	for i := range 5 {
		ds.state.Update("SRV01", &dc.CheckResult{
			Host:       "SRV01",
			Status:     "Healthy",
			Transition: false,
			Timestamp:  time.Unix(int64(1000+i), 0),
		})
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/history/SRV01?changes_only=true", nil)
	r.SetPathValue("host", "SRV01")
	ds.handleHistory(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var records []HistoryView
	if err := json.NewDecoder(w.Body).Decode(&records); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("len(records) = %d, want 0", len(records))
	}
}

func TestHandleHistory_ChangesOnly_RespectsLimit(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")

	// Insert 5 transition records.
	for i := range 5 {
		ds.state.Update("SRV01", &dc.CheckResult{
			Host:       "SRV01",
			Status:     "Alert",
			Transition: true,
			Timestamp:  time.Unix(int64(1000+i), 0),
		})
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/history/SRV01?changes_only=1&limit=3", nil)
	r.SetPathValue("host", "SRV01")
	ds.handleHistory(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var records []HistoryView
	if err := json.NewDecoder(w.Body).Decode(&records); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(records) != 3 {
		t.Errorf("len(records) = %d, want 3 (limit applied after filter)", len(records))
	}
}

func TestHandleHistory_DefaultLimitIs20(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")

	// Insert 30 records.
	for i := range 30 {
		ds.state.Update("SRV01", &dc.CheckResult{
			Host:      "SRV01",
			Status:    "Healthy",
			Timestamp: time.Unix(int64(1000+i), 0),
		})
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/history/SRV01", nil)
	r.SetPathValue("host", "SRV01")
	ds.handleHistory(w, r)

	var records []HistoryView
	if err := json.NewDecoder(w.Body).Decode(&records); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(records) != 20 {
		t.Errorf("len(records) = %d, want 20 (default limit)", len(records))
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
	ds.testPutSettingsFunc = func(_ *[]dc.NotificationTarget, _ *int, _ *int) error { return nil }
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

func TestServerState_Update_OnUpdateCallback_NoDeadlock(t *testing.T) {
	state := NewServerState(t.TempDir())
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
