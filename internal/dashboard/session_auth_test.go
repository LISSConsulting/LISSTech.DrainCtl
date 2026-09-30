//go:build windows

package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

func TestSessionsRead_AdminBoundaryAndFleetProjection(t *testing.T) {
	cfg := dc.SessionsConfig{Enabled: true, AllowActions: true, IdentityVisibility: dc.SessionVisibilityFull, ClientVisibility: dc.SessionVisibilityFull, ProcessVisibility: dc.SessionVisibilityFull}
	ds, _ := newSessionHandlerTestServer(t, cfg)
	for _, host := range []string{"alpha.example.test", "bravo.example.test", "unknown.example.test"} {
		ds.state.Register(host)
		if _, err := ds.state.Update(host, &dc.CheckResult{Host: host, Status: "Healthy"}); err != nil {
			t.Fatalf("update %s: %v", host, err)
		}
	}
	for i, host := range []string{"bravo.example.test", "alpha.example.test"} {
		snapshot := testSessionSnapshot(host, sessiondata.DecimalUint64(i+1))
		if _, err := ds.ingestSessionSnapshot(context.Background(), snapshot); err != nil {
			t.Fatalf("ingest %s: %v", host, err)
		}
	}

	store := newAdminTestStore(t)
	nonAdmin, err := store.Create(&AuthInfo{Username: "viewer"})
	if err != nil {
		t.Fatalf("create viewer session: %v", err)
	}
	admin, err := store.CreateWithAdmin(&AuthInfo{Username: "administrator"}, true)
	if err != nil {
		t.Fatalf("create admin session: %v", err)
	}

	fleet := requireAdmin(store)(http.HandlerFunc(ds.handleSessionsFleet))
	denied := httptest.NewRecorder()
	deniedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	deniedRequest.AddCookie(&http.Cookie{Name: "drainctl_session", Value: nonAdmin})
	fleet.ServeHTTP(denied, deniedRequest)
	assertJSONError(t, denied, http.StatusForbidden, "admin_required")

	ok := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?sort=host&dir=asc&page=1&page_size=15", nil)
	request.AddCookie(&http.Cookie{Name: "drainctl_session", Value: admin})
	fleet.ServeHTTP(ok, request)
	if ok.Code != http.StatusOK {
		t.Fatalf("fleet status = %d: %s", ok.Code, ok.Body.String())
	}
	if got := ok.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var body struct {
		Total int `json:"total"`
		Items []struct {
			Host      string          `json:"host"`
			Freshness string          `json:"freshness"`
			User      json.RawMessage `json:"user"`
			Processes json.RawMessage `json:"processes"`
		} `json:"items"`
	}
	if err := json.NewDecoder(ok.Body).Decode(&body); err != nil {
		t.Fatalf("decode fleet response: %v", err)
	}
	if body.Total != 3 || len(body.Items) != 3 || body.Items[0].Host != "alpha.example.test" || body.Items[2].Host != "unknown.example.test" {
		t.Fatalf("fleet order/known-host projection = %+v", body)
	}
	if body.Items[2].Freshness != "unknown" {
		t.Fatalf("no-snapshot host freshness = %q, want unknown", body.Items[2].Freshness)
	}
	for _, item := range body.Items {
		if item.User != nil || item.Processes != nil {
			t.Fatalf("fleet response leaked detail fields for %s: %+v", item.Host, item)
		}
	}

	invalid := httptest.NewRecorder()
	invalidRequest := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?page_size=16", nil)
	invalidRequest.AddCookie(&http.Cookie{Name: "drainctl_session", Value: admin})
	fleet.ServeHTTP(invalid, invalidRequest)
	assertJSONError(t, invalid, http.StatusBadRequest, "invalid_sessions_query")

	paged := httptest.NewRecorder()
	pagedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?q=7&state=active&sort=host&dir=desc&page=2&page_size=15", nil)
	pagedRequest.AddCookie(&http.Cookie{Name: "drainctl_session", Value: admin})
	fleet.ServeHTTP(paged, pagedRequest)
	var pagedBody struct {
		Query struct {
			Q    string `json:"q"`
			Page int    `json:"page"`
		} `json:"query"`
		Total int               `json:"total"`
		Items []json.RawMessage `json:"items"`
	}
	if err := json.NewDecoder(paged.Body).Decode(&pagedBody); paged.Code != http.StatusOK || err != nil || pagedBody.Query.Q != "7" || pagedBody.Query.Page != 2 || pagedBody.Total != 2 || len(pagedBody.Items) != 0 {
		t.Fatalf("filtered paged fleet = %d %s, decoded=%+v err=%v", paged.Code, paged.Body.String(), pagedBody, err)
	}

	detail := requireAdmin(store)(http.HandlerFunc(ds.handleSessionDetail))
	detailRequest := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/alpha.example.test?page=1&page_size=15", nil)
	detailRequest.SetPathValue("host", "alpha.example.test")
	detailRequest.AddCookie(&http.Cookie{Name: "drainctl_session", Value: admin})
	detailResult := httptest.NewRecorder()
	detail.ServeHTTP(detailResult, detailRequest)
	if detailResult.Code != http.StatusOK || detailResult.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("detail response = %d headers=%v body=%s", detailResult.Code, detailResult.Header(), detailResult.Body.String())
	}
	var detailBody struct {
		ActionsAvailable bool   `json:"actions_available"`
		Freshness        string `json:"freshness"`
		Sessions         []struct {
			User            *string `json:"user"`
			WorkingSetBytes string  `json:"working_set_bytes"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(detailResult.Body).Decode(&detailBody); err != nil {
		t.Fatalf("decode detail response: %v", err)
	}
	if detailBody.Freshness != string(telemetry.SessionFreshnessFresh) {
		t.Fatalf("detail freshness = %q, want %q for a current successful snapshot", detailBody.Freshness, telemetry.SessionFreshnessFresh)
	}
	if !detailBody.ActionsAvailable || len(detailBody.Sessions) != 1 || detailBody.Sessions[0].User == nil || *detailBody.Sessions[0].User != "alice" || detailBody.Sessions[0].WorkingSetBytes != "42" {
		t.Fatalf("detail projection/actions = %+v", detailBody)
	}

	missing := httptest.NewRecorder()
	missingRequest := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/missing.example.test", nil)
	missingRequest.SetPathValue("host", "missing.example.test")
	missingRequest.AddCookie(&http.Cookie{Name: "drainctl_session", Value: admin})
	detail.ServeHTTP(missing, missingRequest)
	assertJSONError(t, missing, http.StatusNotFound, "session_snapshot_not_found")
}

func TestSessionsRead_UnavailableStoreReturnsServiceError(t *testing.T) {
	ds := newTestServer(t)
	ds.testLoadConfigFunc = func() (*dc.Config, error) { return &dc.Config{Sessions: dc.SessionsConfig{Enabled: true}}, nil }
	w := httptest.NewRecorder()
	ds.handleSessionsFleet(w, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))
	assertJSONError(t, w, http.StatusServiceUnavailable, "sessions_unavailable")
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("unavailable Cache-Control = %q, want no-store", got)
	}
}
