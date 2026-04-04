//go:build windows

package dashboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
		state: NewServerState(t.TempDir(), nil),
		log:   dc.DiscardLogger(),
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
	hostname := strings.Repeat("a", 253) // exactly at limit
	body := `{"hostname":"` + hostname + `"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(body))

	ds.handleRegister(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (253-char hostname should be accepted)", w.Code, http.StatusOK)
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
	var records []dc.CheckResult
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
	var records []dc.CheckResult
	if err := json.NewDecoder(w.Body).Decode(&records); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("len(records) = %d, want 3", len(records))
	}
	// Newest first: Alert, Grace, Healthy
	if records[0].Status != "Alert" {
		t.Errorf("records[0].Status = %q, want Alert", records[0].Status)
	}
	if records[1].Status != "Grace" {
		t.Errorf("records[1].Status = %q, want Grace", records[1].Status)
	}
	if records[2].Status != "Healthy" {
		t.Errorf("records[2].Status = %q, want Healthy", records[2].Status)
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
	var records []dc.CheckResult
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

	var records []dc.CheckResult
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
		var resp struct{ Unknown int `json:"unknown"` }
		json.NewDecoder(w.Body).Decode(&resp)
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
		json.NewDecoder(w.Body).Decode(&resp)
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

	var servers []ServerInfo
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

	var servers []ServerInfo
	if err := json.NewDecoder(w.Body).Decode(&servers); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(servers) != 3 {
		t.Fatalf("len(servers) = %d, want 3", len(servers))
	}
	if servers[0].Hostname != "ALPHA" || servers[1].Hostname != "MANGO" || servers[2].Hostname != "ZETA" {
		t.Errorf("servers not sorted: got [%s, %s, %s]", servers[0].Hostname, servers[1].Hostname, servers[2].Hostname)
	}
}

func TestHandleServers_IncludesLastResult(t *testing.T) {
	ds := newTestServer(t)
	ds.state.Register("SRV01")
	ds.state.Update("SRV01", &dc.CheckResult{Host: "SRV01", Status: "Alert"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/servers", nil)
	ds.handleServers(w, r)

	var servers []ServerInfo
	if err := json.NewDecoder(w.Body).Decode(&servers); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("len(servers) = %d, want 1", len(servers))
	}
	if servers[0].LastResult == nil {
		t.Fatal("LastResult should be populated after Update")
	}
	if servers[0].LastResult.Status != "Alert" {
		t.Errorf("LastResult.Status = %q, want Alert", servers[0].LastResult.Status)
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
		Type:    "webhook",
		URL:     webhookSrv.URL,
		Triggers: dc.DefaultTriggers,
	}
	ds.testNotifyFunc = func() error {
		return dc.SendTestNotification([]dc.NotificationTarget{target}, nil)
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
		Type:    "webhook",
		URL:     webhookSrv.URL,
		Secret:  "test-secret",
		Triggers: dc.DefaultTriggers,
	}
	ds.testNotifyFunc = func() error {
		return dc.SendTestNotification([]dc.NotificationTarget{target}, nil)
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

// ── handleGetNotifyConfig ─────────────────────────────────────────────────────

func TestHandleGetNotifyConfig_Returns200WithNotifications(t *testing.T) {
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
	r := httptest.NewRequest(http.MethodGet, "/api/v1/notify-config", nil)
	ds.handleGetNotifyConfig(w, r)

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

func TestHandleGetNotifyConfig_EmptyNotificationsReturnsEmptyArray(t *testing.T) {
	ds := newTestServer(t)
	ds.testLoadConfigFunc = func() (*dc.Config, error) {
		return dc.DefaultConfig(), nil
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/notify-config", nil)
	ds.handleGetNotifyConfig(w, r)

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

func TestHandleGetNotifyConfig_LoadConfigError_Returns500(t *testing.T) {
	ds := newTestServer(t)
	ds.testLoadConfigFunc = func() (*dc.Config, error) {
		return nil, fmt.Errorf("config file not found")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/notify-config", nil)
	ds.handleGetNotifyConfig(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d (load error should yield 500)", w.Code, http.StatusInternalServerError)
	}
}

func TestHandleGetNotifyConfig_MultipleTargets(t *testing.T) {
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
	r := httptest.NewRequest(http.MethodGet, "/api/v1/notify-config", nil)
	ds.handleGetNotifyConfig(w, r)

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

// ── handlePutNotifyConfig ─────────────────────────────────────────────────────

func TestHandlePutNotifyConfig_Success_Returns200(t *testing.T) {
	ds := newTestServer(t)
	ds.testPutNotifyConfigFunc = func(_ []dc.NotificationTarget, _ *int, _ *int) error {
		return nil
	}

	body := `{"notifications":[{"type":"webhook","url":"https://hooks.example.com/","triggers":["drain_on","drain_off","alert","healthy"],"repeat_minutes":0}],"session_warning_threshold":80,"grace_period":60}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/notify-config", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	ds.handlePutNotifyConfig(w, r)

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

func TestHandlePutNotifyConfig_InvalidJSON_Returns400(t *testing.T) {
	ds := newTestServer(t)
	ds.testPutNotifyConfigFunc = func(_ []dc.NotificationTarget, _ *int, _ *int) error {
		return nil
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/notify-config", strings.NewReader("not json at all"))
	ds.handlePutNotifyConfig(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (invalid JSON should be 400)", w.Code, http.StatusBadRequest)
	}
}

func TestHandlePutNotifyConfig_UpdateError_Returns500(t *testing.T) {
	ds := newTestServer(t)
	ds.testPutNotifyConfigFunc = func(_ []dc.NotificationTarget, _ *int, _ *int) error {
		return fmt.Errorf("disk full")
	}

	body := `{"notifications":[],"session_warning_threshold":80,"grace_period":60}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/notify-config", strings.NewReader(body))
	ds.handlePutNotifyConfig(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d (update error should be 500)", w.Code, http.StatusInternalServerError)
	}
}

func TestHandlePutNotifyConfig_CallsUpdateWithCorrectNotifications(t *testing.T) {
	ds := newTestServer(t)

	var capturedTargets []dc.NotificationTarget
	var capturedThreshold *int
	var capturedGrace *int
	ds.testPutNotifyConfigFunc = func(notifications []dc.NotificationTarget, threshold *int, grace *int) error {
		capturedTargets = notifications
		capturedThreshold = threshold
		capturedGrace = grace
		return nil
	}

	th := 90
	gp := 30
	payload := struct {
		Notifications           []dc.NotificationTarget `json:"notifications"`
		SessionWarningThreshold int                     `json:"session_warning_threshold"`
		GracePeriod             int                     `json:"grace_period"`
	}{
		Notifications: []dc.NotificationTarget{
			{Type: "ntfy", URL: "https://ntfy.sh/alerts", Triggers: dc.DefaultTriggers},
		},
		SessionWarningThreshold: th,
		GracePeriod:             gp,
	}
	body, _ := json.Marshal(payload)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/notify-config", bytes.NewReader(body))
	ds.handlePutNotifyConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if len(capturedTargets) != 1 || capturedTargets[0].URL != "https://ntfy.sh/alerts" {
		t.Errorf("capturedTargets = %v, want 1 entry with URL https://ntfy.sh/alerts", capturedTargets)
	}
	if capturedThreshold == nil || *capturedThreshold != 90 {
		t.Errorf("capturedThreshold = %v, want 90", capturedThreshold)
	}
	if capturedGrace == nil || *capturedGrace != 30 {
		t.Errorf("capturedGrace = %v, want 30", capturedGrace)
	}
}

func TestHandlePutNotifyConfig_PartialUpdate_ThresholdAndGraceOmitted(t *testing.T) {
	ds := newTestServer(t)

	var capturedThreshold *int
	var capturedGrace *int
	ds.testPutNotifyConfigFunc = func(_ []dc.NotificationTarget, threshold *int, grace *int) error {
		capturedThreshold = threshold
		capturedGrace = grace
		return nil
	}

	// Only notifications — no threshold or grace_period fields.
	body := `{"notifications":[]}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/notify-config", strings.NewReader(body))
	ds.handlePutNotifyConfig(w, r)

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

func TestHandlePutNotifyConfig_WebhookSecretPreserved(t *testing.T) {
	ds := newTestServer(t)

	var capturedTargets []dc.NotificationTarget
	ds.testPutNotifyConfigFunc = func(notifications []dc.NotificationTarget, _ *int, _ *int) error {
		capturedTargets = notifications
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
	r := httptest.NewRequest(http.MethodPut, "/api/v1/notify-config", bytes.NewReader(body))
	ds.handlePutNotifyConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if len(capturedTargets) != 1 {
		t.Fatalf("len(capturedTargets) = %d, want 1", len(capturedTargets))
	}
	if capturedTargets[0].Secret != "my-hmac-secret" {
		t.Errorf("Secret = %q, want my-hmac-secret", capturedTargets[0].Secret)
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
