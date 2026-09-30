//go:build windows

package dashboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

type countingSessionQueries struct {
	fleetCalls  int
	detailCalls int
}

func (q *countingSessionQueries) Fleet(context.Context, telemetry.FleetSessionQuery) (telemetry.FleetSessionPage, error) {
	q.fleetCalls++
	return telemetry.FleetSessionPage{}, nil
}

func (q *countingSessionQueries) Detail(context.Context, string, telemetry.SessionDetailQuery) (telemetry.SessionDetail, error) {
	q.detailCalls++
	return telemetry.SessionDetail{}, nil
}

func newSessionHandlerTestServer(t *testing.T, cfg dc.SessionsConfig) (*DashboardServer, *telemetry.DB) {
	t.Helper()
	db, err := telemetry.Open(t.TempDir())
	if err != nil {
		t.Fatalf("telemetry.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	snapshots, err := telemetry.NewSessionSnapshotStore(db)
	if err != nil {
		t.Fatalf("NewSessionSnapshotStore: %v", err)
	}
	queries, err := telemetry.NewSessionQueryStore(db)
	if err != nil {
		t.Fatalf("NewSessionQueryStore: %v", err)
	}
	ds := newTestServer(t)
	ds.sessionSnapshots = snapshots
	ds.sessionQueries = queries
	ds.cfg.Group = "Dashboard Admins"
	ds.testLoadConfigFunc = func() (*dc.Config, error) { return &dc.Config{Sessions: cfg}, nil }
	ds.setHeartbeatInterval(time.Minute)
	return ds, db
}

func testSessionSnapshot(host string, sequence sessiondata.DecimalUint64) sessiondata.SessionSnapshot {
	user, domain, station, client, address := "alice", "CONTOSO", "rdp-tcp#1", "WS-01", "192.0.2.1"
	logon := time.Now().Add(-time.Hour).UnixMilli()
	workingSet := sessiondata.DecimalUint64(42)
	return sessiondata.SessionSnapshot{
		Schema: sessiondata.SnapshotSchema, Host: host, AgentInstanceID: "01890f9d-5c00-7000-8000-000000000001", Sequence: sequence,
		ObservedAtMS: time.Now().UnixMilli(), CollectorVersion: "test", LogicalCPUCount: 1,
		Capabilities: sessiondata.SessionCapabilities{SessionActions: true, Processes: true, InputDelay: true, RemoteFX: true},
		Sessions:     []sessiondata.SessionRecord{{SessionID: 7, LogonAtMS: &logon, User: &user, Domain: &domain, State: sessiondata.SessionActive, Station: &station, ClientName: &client, ClientAddress: &address, WorkingSetBytes: &workingSet, Processes: []sessiondata.SessionProcess{}}},
	}
}

func snapshotRequest(t *testing.T, snapshot sessiondata.SessionSnapshot, auth *AuthInfo) *http.Request {
	t.Helper()
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/session-snapshot", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	if auth != nil {
		r = r.WithContext(context.WithValue(r.Context(), authInfoKey, auth))
	}
	return r
}

func assertJSONError(t *testing.T, result *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if result.Code != status {
		t.Fatalf("status = %d, want %d: %s", result.Code, status, result.Body.String())
	}
	if got := result.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := strings.TrimSpace(result.Body.String()); got != `{"error":"`+code+`"}` {
		t.Fatalf("body = %s, want error %q", got, code)
	}
}

func TestHandleSessionSnapshot_StrictMachineIngestAndReplacement(t *testing.T) {
	cfg := dc.SessionsConfig{Enabled: true, AllowActions: true, IdentityVisibility: dc.SessionVisibilityFull, ClientVisibility: dc.SessionVisibilityFull, ProcessVisibility: dc.SessionVisibilityFull}
	ds, _ := newSessionHandlerTestServer(t, cfg)
	host := "rdsh-01.example.test"
	auth := &AuthInfo{Username: `CONTOSO\rdsh-01.example.test$`}
	handler := requireSnapshotMachineAccount(http.HandlerFunc(ds.handleSessionSnapshot))

	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, snapshotRequest(t, testSessionSnapshot(host, 1), nil))
	assertJSONError(t, unauthenticated, http.StatusUnauthorized, "unauthorized_machine")

	human := httptest.NewRecorder()
	handler.ServeHTTP(human, snapshotRequest(t, testSessionSnapshot(host, 1), &AuthInfo{Username: `CONTOSO\alice`}))
	assertJSONError(t, human, http.StatusForbidden, "host_identity_mismatch")

	mismatch := httptest.NewRecorder()
	handler.ServeHTTP(mismatch, snapshotRequest(t, testSessionSnapshot(host, 1), &AuthInfo{Username: `CONTOSO\other$`}))
	assertJSONError(t, mismatch, http.StatusForbidden, "host_identity_mismatch")

	invalid := httptest.NewRecorder()
	invalidRequest := httptest.NewRequest(http.MethodPost, "/api/v1/session-snapshot", strings.NewReader(`{"schema":"drainctl.session-snapshot.v1","unknown":true}`))
	invalidRequest.Header.Set("Content-Type", "application/json")
	invalidRequest = invalidRequest.WithContext(context.WithValue(invalidRequest.Context(), authInfoKey, auth))
	handler.ServeHTTP(invalid, invalidRequest)
	assertJSONError(t, invalid, http.StatusBadRequest, "invalid_snapshot")

	encoded := httptest.NewRecorder()
	encodedRequest := snapshotRequest(t, testSessionSnapshot(host, 1), auth)
	encodedRequest.Header.Set("Content-Encoding", "gzip")
	handler.ServeHTTP(encoded, encodedRequest)
	assertJSONError(t, encoded, http.StatusUnsupportedMediaType, "unsupported_media_type")

	validBody, err := json.Marshal(testSessionSnapshot(host, 1))
	if err != nil {
		t.Fatalf("marshal duplicate-key snapshot: %v", err)
	}
	duplicate := httptest.NewRecorder()
	duplicateRequest := httptest.NewRequest(http.MethodPost, "/api/v1/session-snapshot", strings.NewReader(string(validBody[:len(validBody)-1])+`,"host":"`+host+`"}`))
	duplicateRequest.Header.Set("Content-Type", "application/json")
	duplicateRequest = duplicateRequest.WithContext(context.WithValue(duplicateRequest.Context(), authInfoKey, auth))
	handler.ServeHTTP(duplicate, duplicateRequest)
	assertJSONError(t, duplicate, http.StatusBadRequest, "invalid_snapshot")

	tooLarge := httptest.NewRecorder()
	largeRequest := httptest.NewRequest(http.MethodPost, "/api/v1/session-snapshot", strings.NewReader(strings.Repeat("x", sessiondata.MaxSnapshotBytes+1)))
	largeRequest.Header.Set("Content-Type", "application/json")
	largeRequest = largeRequest.WithContext(context.WithValue(largeRequest.Context(), authInfoKey, auth))
	handler.ServeHTTP(tooLarge, largeRequest)
	assertJSONError(t, tooLarge, http.StatusRequestEntityTooLarge, "snapshot_too_large")

	accepted := httptest.NewRecorder()
	handler.ServeHTTP(accepted, snapshotRequest(t, testSessionSnapshot(host, 2), auth))
	var acceptedBody struct {
		Accepted     bool  `json:"accepted"`
		ReceivedAtMS int64 `json:"received_at_ms"`
		SessionCount *int  `json:"session_count"`
	}
	if err := json.Unmarshal(accepted.Body.Bytes(), &acceptedBody); accepted.Code != http.StatusAccepted || err != nil || !acceptedBody.Accepted || acceptedBody.ReceivedAtMS == 0 || acceptedBody.SessionCount == nil || *acceptedBody.SessionCount != 1 {
		t.Fatalf("accepted response = %d %s, decoded=%+v err=%v", accepted.Code, accepted.Body.String(), acceptedBody, err)
	}

	staleNow := time.Now().Add(4 * time.Minute)
	freshness, err := ds.sessionQueries.Detail(context.Background(), host, telemetry.SessionDetailQuery{Page: 1, PageSize: 30, Now: staleNow, HeartbeatInterval: time.Minute, IsHostOnline: func(string) bool { return true }})
	if err != nil || freshness.Freshness != telemetry.SessionFreshnessStale {
		var received int64
		if freshness.LastSuccessReceivedAtMS != nil {
			received = *freshness.LastSuccessReceivedAtMS
		}
		t.Fatalf("receipt-time freshness = %q last_success=%d now=%d err=%v, want stale after four heartbeats", freshness.Freshness, received, staleNow.UnixMilli(), err)
	}

	stale := httptest.NewRecorder()
	handler.ServeHTTP(stale, snapshotRequest(t, testSessionSnapshot(host, 2), auth))
	var staleBody struct {
		Accepted bool   `json:"accepted"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(stale.Body.Bytes(), &staleBody); stale.Code != http.StatusAccepted || err != nil || staleBody.Accepted || staleBody.Reason != "stale_snapshot" {
		t.Fatalf("stale response = %d %s, decoded=%+v err=%v", stale.Code, stale.Body.String(), staleBody, err)
	}

	fatalSnapshot := testSessionSnapshot(host, 3)
	fatalSnapshot.CollectionError = &sessiondata.CollectionError{Code: sessiondata.CollectionErrorWTSEnumerationFailed}
	fatalSnapshot.Sessions = []sessiondata.SessionRecord{}
	fatalSnapshot.Capabilities = sessiondata.SessionCapabilities{}
	fatalWire, err := json.Marshal(fatalSnapshot)
	if err != nil {
		t.Fatalf("marshal fatal snapshot: %v", err)
	}
	var fatalEnvelope map[string]json.RawMessage
	if err := json.Unmarshal(fatalWire, &fatalEnvelope); err != nil {
		t.Fatalf("decode fatal snapshot: %v", err)
	}
	if got := string(fatalEnvelope["sessions"]); got != "[]" {
		t.Fatalf("fatal sessions wire value = %s, want []", got)
	}
	fatal := httptest.NewRecorder()
	handler.ServeHTTP(fatal, snapshotRequest(t, fatalSnapshot, auth))
	var fatalBody struct {
		Accepted        bool   `json:"accepted"`
		CollectionError string `json:"collection_error"`
		SessionCount    *int   `json:"session_count"`
	}
	if err := json.Unmarshal(fatal.Body.Bytes(), &fatalBody); fatal.Code != http.StatusAccepted || err != nil || !fatalBody.Accepted || fatalBody.CollectionError != sessiondata.CollectionErrorWTSEnumerationFailed || fatalBody.SessionCount != nil {
		t.Fatalf("fatal response = %d %s, decoded=%+v err=%v", fatal.Code, fatal.Body.String(), fatalBody, err)
	}
	detail, err := ds.sessionQueries.Detail(context.Background(), host, telemetry.SessionDetailQuery{Page: 1, PageSize: 30, Now: time.Now(), HeartbeatInterval: time.Minute})
	if err != nil || detail.Total != 1 || detail.CollectionStatus != telemetry.SessionCollectionError {
		t.Fatalf("fatal attempt did not preserve latest success: detail=%+v err=%v", detail, err)
	}

	empty := testSessionSnapshot(host, 4)
	empty.Sessions = []sessiondata.SessionRecord{}
	emptyResult := httptest.NewRecorder()
	handler.ServeHTTP(emptyResult, snapshotRequest(t, empty, auth))
	var emptyBody struct {
		Accepted     bool `json:"accepted"`
		SessionCount *int `json:"session_count"`
	}
	if err := json.Unmarshal(emptyResult.Body.Bytes(), &emptyBody); emptyResult.Code != http.StatusAccepted || err != nil || !emptyBody.Accepted || emptyBody.SessionCount == nil || *emptyBody.SessionCount != 0 {
		t.Fatalf("empty response = %d %s, decoded=%+v err=%v", emptyResult.Code, emptyResult.Body.String(), emptyBody, err)
	}
	detail, err = ds.sessionQueries.Detail(context.Background(), host, telemetry.SessionDetailQuery{Page: 1, PageSize: 30, Now: time.Now(), HeartbeatInterval: time.Minute})
	if err != nil || detail.Total != 0 || detail.SessionCount == nil || *detail.SessionCount != 0 {
		t.Fatalf("empty snapshot did not clear sessions: detail=%+v err=%v", detail, err)
	}
}

func TestSessionSnapshotAuth_UsesContractErrorsWithoutChangingReportAuth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	called := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	snapshotAuth := snapshotNegotiateMiddleware(ctx, requireSnapshotMachineAccount(next))

	missing := httptest.NewRecorder()
	snapshotAuth.ServeHTTP(missing, httptest.NewRequest(http.MethodPost, "/api/v1/session-snapshot", nil))
	assertJSONError(t, missing, http.StatusUnauthorized, "unauthorized_machine")
	if got := missing.Header().Get("WWW-Authenticate"); got != "Negotiate" {
		t.Fatalf("snapshot missing-auth challenge = %q, want Negotiate", got)
	}

	invalid := httptest.NewRecorder()
	invalidRequest := httptest.NewRequest(http.MethodPost, "/api/v1/session-snapshot", nil)
	invalidRequest.Header.Set("Authorization", "Negotiate %%%")
	snapshotAuth.ServeHTTP(invalid, invalidRequest)
	assertJSONError(t, invalid, http.StatusUnauthorized, "unauthorized_machine")
	if called {
		t.Fatal("snapshot handler called after authentication failure")
	}

	reportAuth := NegotiateMiddleware(ctx, next)
	report := httptest.NewRecorder()
	reportAuth.ServeHTTP(report, httptest.NewRequest(http.MethodPost, "/api/v1/report", nil))
	if report.Code != http.StatusUnauthorized {
		t.Fatalf("report missing-auth status = %d, want 401", report.Code)
	}
	if got := report.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("report missing-auth Content-Type = %q, want existing text/plain response", got)
	}
	if got := report.Body.String(); got != "authentication required\n" {
		t.Fatalf("report missing-auth body = %q, want existing response", got)
	}
}

func TestHandleSessionSnapshot_StorageFailureReturnsNoSuccess(t *testing.T) {
	cfg := dc.SessionsConfig{Enabled: true, IdentityVisibility: dc.SessionVisibilityFull, ClientVisibility: dc.SessionVisibilityFull, ProcessVisibility: dc.SessionVisibilityFull}
	ds, db := newSessionHandlerTestServer(t, cfg)
	if err := db.Close(); err != nil {
		t.Fatalf("close telemetry DB: %v", err)
	}
	host := "rdsh-02.example.test"
	w := httptest.NewRecorder()
	ds.handleSessionSnapshot(w, snapshotRequest(t, testSessionSnapshot(host, 1), &AuthInfo{Username: `CONTOSO\rdsh-02.example.test$`}))
	assertJSONError(t, w, http.StatusServiceUnavailable, "storage_error")
}

func TestSessionSnapshotIngest_PersistsPrivacyProjection(t *testing.T) {
	cfg := dc.SessionsConfig{Enabled: true, IdentityVisibility: dc.SessionVisibilityHidden, ClientVisibility: dc.SessionVisibilityHidden, ProcessVisibility: dc.SessionVisibilityHidden}
	ds, _ := newSessionHandlerTestServer(t, cfg)
	host := "private.example.test"
	if _, err := ds.ingestSessionSnapshot(context.Background(), testSessionSnapshot(host, 1)); err != nil {
		t.Fatalf("ingest hidden snapshot: %v", err)
	}
	detail, err := ds.sessionQueries.Detail(context.Background(), host, telemetry.SessionDetailQuery{Page: 1, PageSize: 30, Now: time.Now(), HeartbeatInterval: time.Minute})
	if err != nil || len(detail.Sessions) != 1 {
		t.Fatalf("read hidden snapshot: detail=%+v err=%v", detail, err)
	}
	session := detail.Sessions[0]
	if session.User != nil || session.Domain != nil || session.Station != nil || session.ClientName != nil || session.ClientAddress != nil || len(session.Processes) != 0 {
		t.Fatalf("stored session retains hidden data: %+v", session)
	}
}

func TestHandleSessionSnapshot_RejectsStrictJSONAtEveryLevel(t *testing.T) {
	cfg := dc.SessionsConfig{Enabled: true, IdentityVisibility: dc.SessionVisibilityFull, ClientVisibility: dc.SessionVisibilityFull, ProcessVisibility: dc.SessionVisibilityFull}
	ds, _ := newSessionHandlerTestServer(t, cfg)

	type strictCase struct {
		name    string
		prepare func(*sessiondata.SessionSnapshot)
		old     string
		new     string
	}
	cases := []strictCase{
		{name: "duplicate top-level", old: `{"schema":"drainctl.session-snapshot.v1",`, new: `{"schema":"drainctl.session-snapshot.v1","schema":"drainctl.session-snapshot.v1",`},
		{name: "duplicate session", old: `{"session_id":7,`, new: `{"session_id":7,"session_id":7,`},
		{name: "duplicate process", prepare: func(snapshot *sessiondata.SessionSnapshot) {
			snapshot.Sessions[0].Processes = []sessiondata.SessionProcess{{PID: 42, ImageName: "app.exe"}}
		}, old: `{"pid":42,`, new: `{"pid":42,"pid":42,`},
		{name: "duplicate RemoteFX", prepare: func(snapshot *sessiondata.SessionSnapshot) {
			fps := 1.0
			snapshot.Sessions[0].RemoteFX = &sessiondata.RemoteFXMetrics{FPS: &fps}
		}, old: `{"fps":1,`, new: `{"fps":1,"fps":1,`},
		{name: "unknown top-level", old: `{"schema":`, new: `{"unknown":true,"schema":`},
		{name: "unknown session", old: `{"session_id":7,`, new: `{"unknown":true,"session_id":7,`},
		{name: "unknown process", prepare: func(snapshot *sessiondata.SessionSnapshot) {
			snapshot.Sessions[0].Processes = []sessiondata.SessionProcess{{PID: 42, ImageName: "app.exe"}}
		}, old: `{"pid":42,`, new: `{"unknown":true,"pid":42,`},
		{name: "unknown RemoteFX", prepare: func(snapshot *sessiondata.SessionSnapshot) {
			fps := 1.0
			snapshot.Sessions[0].RemoteFX = &sessiondata.RemoteFXMetrics{FPS: &fps}
		}, old: `{"fps":1,`, new: `{"unknown":true,"fps":1,`},
	}

	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host := "strict-" + strconv.Itoa(index) + ".example.test"
			snapshot := testSessionSnapshot(host, 1)
			if tc.prepare != nil {
				tc.prepare(&snapshot)
			}
			body, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatalf("marshal snapshot: %v", err)
			}
			payload := strings.Replace(string(body), tc.old, tc.new, 1)
			if payload == string(body) {
				t.Fatalf("fixture did not contain %q", tc.old)
			}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/session-snapshot", strings.NewReader(payload))
			request.Header.Set("Content-Type", "application/json")
			request = request.WithContext(context.WithValue(request.Context(), authInfoKey, &AuthInfo{Username: `CONTOSO\` + host + `$`}))
			result := httptest.NewRecorder()
			ds.handleSessionSnapshot(result, request)
			assertJSONError(t, result, http.StatusBadRequest, "invalid_snapshot")

			_, err = ds.sessionQueries.Detail(context.Background(), host, telemetry.SessionDetailQuery{Page: 1, PageSize: 30, Now: time.Now(), HeartbeatInterval: time.Minute})
			if !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("invalid snapshot was retained: err=%v, want sql.ErrNoRows", err)
			}
		})
	}

	t.Run("trailing value", func(t *testing.T) {
		host := "strict-trailing.example.test"
		body, err := json.Marshal(testSessionSnapshot(host, 1))
		if err != nil {
			t.Fatalf("marshal snapshot: %v", err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/session-snapshot", strings.NewReader(string(body)+` true`))
		request.Header.Set("Content-Type", "application/json")
		request = request.WithContext(context.WithValue(request.Context(), authInfoKey, &AuthInfo{Username: `CONTOSO\` + host + `$`}))
		result := httptest.NewRecorder()
		ds.handleSessionSnapshot(result, request)
		assertJSONError(t, result, http.StatusBadRequest, "invalid_snapshot")
		_, err = ds.sessionQueries.Detail(context.Background(), host, telemetry.SessionDetailQuery{Page: 1, PageSize: 30, Now: time.Now(), HeartbeatInterval: time.Minute})
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("trailing snapshot was retained: err=%v, want sql.ErrNoRows", err)
		}
	})
}

func TestHandleSessionDetail_MapsNoRowsAndStorageFailures(t *testing.T) {
	cfg := dc.SessionsConfig{Enabled: true, IdentityVisibility: dc.SessionVisibilityFull, ClientVisibility: dc.SessionVisibilityFull, ProcessVisibility: dc.SessionVisibilityFull}
	ds, db := newSessionHandlerTestServer(t, cfg)

	missing := httptest.NewRecorder()
	missingRequest := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/missing.example.test", nil)
	missingRequest.SetPathValue("host", "missing.example.test")
	ds.handleSessionDetail(missing, missingRequest)
	assertJSONError(t, missing, http.StatusNotFound, "session_snapshot_not_found")

	if err := db.Close(); err != nil {
		t.Fatalf("close telemetry DB: %v", err)
	}
	unavailable := httptest.NewRecorder()
	unavailableRequest := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/unavailable.example.test", nil)
	unavailableRequest.SetPathValue("host", "unavailable.example.test")
	ds.handleSessionDetail(unavailable, unavailableRequest)
	assertJSONError(t, unavailable, http.StatusServiceUnavailable, "sessions_unavailable")
}

func TestSessionReadRoutes_DisabledBeforeQuerying(t *testing.T) {
	cfg := dc.SessionsConfig{Enabled: false}
	ds, _ := newSessionHandlerTestServer(t, cfg)
	queries := &countingSessionQueries{}
	ds.sessionQueries = queries

	fleet := httptest.NewRecorder()
	ds.handleSessionsFleet(fleet, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))
	assertJSONError(t, fleet, http.StatusConflict, "sessions_disabled")

	detail := httptest.NewRecorder()
	detailRequest := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/retained.example.test", nil)
	detailRequest.SetPathValue("host", "retained.example.test")
	ds.handleSessionDetail(detail, detailRequest)
	assertJSONError(t, detail, http.StatusConflict, "sessions_disabled")

	if queries.fleetCalls != 0 || queries.detailCalls != 0 {
		t.Fatalf("disabled routes queried retained snapshots: fleet=%d detail=%d", queries.fleetCalls, queries.detailCalls)
	}
}
