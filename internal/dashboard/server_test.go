//go:build windows

package dashboard

import (
	"bytes"
	"encoding/json"
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
