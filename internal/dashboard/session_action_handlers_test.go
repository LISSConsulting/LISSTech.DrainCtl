//go:build windows

package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
	"github.com/google/uuid"
)

func newSessionActionTestServer(t *testing.T, cfg dc.SessionsConfig) (*DashboardServer, *telemetry.SessionActionStore, string) {
	t.Helper()
	ds, db := newSessionHandlerTestServer(t, cfg)
	ds.sessionActions = telemetry.NewSessionActionStore(db)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ds.sessionStore = NewSessionStore(ctx)
	token, err := ds.sessionStore.CreateWithAdmin(&AuthInfo{Username: `CONTOSO\admin`}, true)
	if err != nil {
		t.Fatal(err)
	}
	ds.setHeartbeatInterval(time.Minute)
	return ds, ds.sessionActions.(*telemetry.SessionActionStore), token
}

func sessionActionRequest(t *testing.T, host, sessionID, token, key, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+host+"/"+sessionID+"/actions", strings.NewReader(body))
	r.SetPathValue("host", host)
	r.SetPathValue("sessionID", sessionID)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", uuid.NewSHA1(uuid.NameSpaceURL, []byte(key)).String())
	r.AddCookie(&http.Cookie{Name: "drainctl_session", Value: token})
	return r
}

func seedActionTarget(t *testing.T, ds *DashboardServer, host string) int64 {
	t.Helper()
	ds.state.Register(host)
	ds.state.Update(host, &dc.CheckResult{Host: host, Status: "Healthy"})
	snapshot := testSessionSnapshot(host, 1)
	if _, err := ds.ingestSessionSnapshot(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	return *snapshot.Sessions[0].LogonAtMS
}

func TestSessionAction_EnqueueReplayConflictDurabilityAndProtection(t *testing.T) {
	cfg := dc.SessionsConfig{Enabled: true, AllowActions: true, IdentityVisibility: dc.SessionVisibilityFull, ClientVisibility: dc.SessionVisibilityFull, ProcessVisibility: dc.SessionVisibilityFull}
	ds, actions, token := newSessionActionTestServer(t, cfg)
	host := "action.example.test"
	logon := seedActionTarget(t, ds, host)
	body := `{"type":"message","expected_logon_at_ms":` + strconv.FormatInt(logon, 10) + `,"message":"Do not expose this message"}`

	created := httptest.NewRecorder()
	ds.handleSessionAction(created, sessionActionRequest(t, host, "7", token, "replay-key", body))
	if created.Code != http.StatusAccepted || created.Header().Get("Content-Type") != "application/json" || created.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create status=%d headers=%v body=%s", created.Code, created.Header(), created.Body.String())
	}
	var createResponse struct {
		Action sessiondata.SessionActionStatus `json:"action"`
	}
	if err := json.NewDecoder(created.Body).Decode(&createResponse); err != nil || createResponse.Action.State != sessiondata.SessionActionQueued || createResponse.Action.Type != sessiondata.SessionActionMessage || createResponse.Action.CanonicalHost != host {
		t.Fatalf("created status=%+v err=%v", createResponse.Action, err)
	}
	status := createResponse.Action
	if strings.Contains(created.Body.String(), "Do not expose") || status.ActionID == "" {
		t.Fatalf("unsafe create body: %s", created.Body.String())
	}

	replay := httptest.NewRecorder()
	ds.handleSessionAction(replay, sessionActionRequest(t, host, "7", token, "replay-key", body))
	var replayResponse struct {
		Action sessiondata.SessionActionStatus `json:"action"`
	}
	_ = json.NewDecoder(replay.Body).Decode(&replayResponse)
	if replay.Code != http.StatusOK || replayResponse.Action.ActionID != status.ActionID {
		t.Fatalf("replay=%d %+v", replay.Code, replayResponse.Action)
	}
	conflict := httptest.NewRecorder()
	ds.handleSessionAction(conflict, sessionActionRequest(t, host, "7", token, "replay-key", strings.Replace(body, "Do not expose this message", "different", 1)))
	assertJSONError(t, conflict, http.StatusConflict, "idempotency_conflict")

	deliveries, err := actions.Deliver(context.Background(), host, time.Now())
	if err != nil || len(deliveries) != 1 || deliveries[0].Action.ActionID != status.ActionID || deliveries[0].MessageProtection != "dpapi" || bytes.Equal(deliveries[0].MessageCiphertext, []byte("Do not expose this message")) {
		t.Fatalf("durable protected delivery=%+v err=%v", deliveries, err)
	}
}

func TestSessionAction_DisconnectQueuesWithoutMessage(t *testing.T) {
	cfg := dc.SessionsConfig{Enabled: true, AllowActions: true, IdentityVisibility: dc.SessionVisibilityFull, ClientVisibility: dc.SessionVisibilityFull, ProcessVisibility: dc.SessionVisibilityFull}
	ds, actions, token := newSessionActionTestServer(t, cfg)
	host := "disconnect.example.test"
	logon := seedActionTarget(t, ds, host)
	body := `{"type":"disconnect","expected_logon_at_ms":` + strconv.FormatInt(logon, 10) + `}`
	w := httptest.NewRecorder()
	ds.handleSessionAction(w, sessionActionRequest(t, host, "7", token, "disconnect", body))
	if w.Code != http.StatusAccepted || strings.Contains(w.Body.String(), "message") {
		t.Fatalf("enqueue status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Action sessiondata.SessionActionStatus `json:"action"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil || response.Action.Type != sessiondata.SessionActionDisconnect {
		t.Fatalf("disconnect status=%+v err=%v", response.Action, err)
	}
	deliveries, err := actions.Deliver(context.Background(), host, time.Now())
	if err != nil || len(deliveries) != 1 || deliveries[0].Action.Type != sessiondata.SessionActionDisconnect || deliveries[0].MessageCiphertext != nil || deliveries[0].MessageProtection != "" {
		t.Fatalf("disconnect delivery=%+v err=%v", deliveries, err)
	}
	withMessage := httptest.NewRecorder()
	ds.handleSessionAction(withMessage, sessionActionRequest(t, host, "7", token, "disconnect-message", `{"type":"disconnect","expected_logon_at_ms":`+strconv.FormatInt(logon, 10)+`,"message":"forbidden"}`))
	assertJSONError(t, withMessage, http.StatusBadRequest, "invalid_action_request")
}

func TestSessionAction_ReplaySurvivesSnapshotPurgeAndPolicyDisable(t *testing.T) {
	cfg := dc.SessionsConfig{Enabled: true, AllowActions: true, IdentityVisibility: dc.SessionVisibilityFull, ClientVisibility: dc.SessionVisibilityFull, ProcessVisibility: dc.SessionVisibilityFull}
	ds, _, token := newSessionActionTestServer(t, cfg)
	host := "replay-after-purge.example.test"
	logon := seedActionTarget(t, ds, host)
	body := `{"type":"logoff","expected_logon_at_ms":` + strconv.FormatInt(logon, 10) + `}`

	created := httptest.NewRecorder()
	ds.handleSessionAction(created, sessionActionRequest(t, host, "7", token, "replay-after-purge", body))
	if created.Code != http.StatusAccepted {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	if err := ds.PurgeSessionSnapshotsForPrivacy(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = false
	ds.testLoadConfigFunc = func() (*dc.Config, error) {
		result := dc.DefaultConfig()
		result.Sessions = cfg
		return result, nil
	}
	replayed := httptest.NewRecorder()
	ds.handleSessionAction(replayed, sessionActionRequest(t, host, "7", token, "replay-after-purge", body))
	if replayed.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replayed.Code, replayed.Body.String())
	}
}

func TestSessionAction_RejectsInvalidRequestAndNamesMissingResources(t *testing.T) {
	cfg := dc.SessionsConfig{Enabled: true, AllowActions: true, IdentityVisibility: dc.SessionVisibilityFull, ClientVisibility: dc.SessionVisibilityFull, ProcessVisibility: dc.SessionVisibilityFull}
	ds, _, token := newSessionActionTestServer(t, cfg)
	host := "request-errors.example.test"
	logon := seedActionTarget(t, ds, host)
	body := `{"type":"logoff","expected_logon_at_ms":` + strconv.FormatInt(logon, 10) + `}`

	invalidKey := httptest.NewRecorder()
	request := sessionActionRequest(t, host, "7", token, "invalid-key", body)
	request.Header.Set("Idempotency-Key", "not-a-uuid")
	ds.handleSessionAction(invalidKey, request)
	assertJSONError(t, invalidKey, http.StatusBadRequest, "invalid_action_request")

	missingSession := httptest.NewRecorder()
	ds.handleSessionAction(missingSession, sessionActionRequest(t, host, "8", token, "missing-session", body))
	assertJSONError(t, missingSession, http.StatusNotFound, "session_not_found")

	missingStatus := httptest.NewRecorder()
	actionID := uuid.NewString()
	statusRequest := httptest.NewRequest(http.MethodGet, "/api/v1/session-actions/"+actionID, nil)
	statusRequest.SetPathValue("actionID", actionID)
	ds.handleSessionActionStatus(missingStatus, statusRequest)
	assertJSONError(t, missingStatus, http.StatusNotFound, "session_action_not_found")
}

func TestSessionAction_GatesStatusAndShadowSafety(t *testing.T) {
	base := dc.SessionsConfig{Enabled: true, AllowActions: true, IdentityVisibility: dc.SessionVisibilityFull, ClientVisibility: dc.SessionVisibilityFull, ProcessVisibility: dc.SessionVisibilityFull}
	ds, _, token := newSessionActionTestServer(t, base)
	host := "gates.example.test"
	logon := seedActionTarget(t, ds, host)
	body := `{"type":"logoff","expected_logon_at_ms":` + strconv.FormatInt(logon, 10) + `}`
	for _, tc := range []struct {
		name   string
		mutate func()
	}{
		{"config", func() {
			ds.testLoadConfigFunc = func() (*dc.Config, error) { return &dc.Config{Sessions: dc.SessionsConfig{Enabled: true}}, nil }
		}},
		{"capability", func() {
			ds.testLoadConfigFunc = func() (*dc.Config, error) { return &dc.Config{Sessions: base}, nil }
			s := testSessionSnapshot(host, 2)
			s.Capabilities.SessionActions = false
			if _, err := ds.ingestSessionSnapshot(context.Background(), s); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.mutate()
			w := httptest.NewRecorder()
			ds.handleSessionAction(w, sessionActionRequest(t, host, "7", token, tc.name, body))
			assertJSONError(t, w, http.StatusConflict, map[string]string{"config": "sessions_disabled", "capability": "actions_unsupported"}[tc.name])
		})
	}
	changed := httptest.NewRecorder()
	valid := testSessionSnapshot(host, 3)
	if _, err := ds.ingestSessionSnapshot(context.Background(), valid); err != nil {
		t.Fatal(err)
	}
	ds.testLoadConfigFunc = func() (*dc.Config, error) { return &dc.Config{Sessions: base}, nil }
	ds.handleSessionAction(changed, sessionActionRequest(t, host, "7", token, "changed", `{"type":"logoff","expected_logon_at_ms":0}`))
	assertJSONError(t, changed, http.StatusConflict, "session_identity_changed")
	fatal := testSessionSnapshot(host, 4)
	fatal.CollectionError = &sessiondata.CollectionError{Code: sessiondata.CollectionErrorWTSEnumerationFailed}
	fatal.Sessions = nil
	fatal.Capabilities = sessiondata.SessionCapabilities{}
	if _, err := ds.ingestSessionSnapshot(context.Background(), fatal); err != nil {
		t.Fatal(err)
	}
	collectionError := httptest.NewRecorder()
	ds.handleSessionAction(collectionError, sessionActionRequest(t, host, "7", token, "collection-error", body))
	assertJSONError(t, collectionError, http.StatusConflict, "host_not_fresh")

	fresh := testSessionSnapshot(host, 5)
	if _, err := ds.ingestSessionSnapshot(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
	ds.now = func() time.Time { return time.Now().Add(4 * time.Minute) }
	stale := httptest.NewRecorder()
	ds.handleSessionAction(stale, sessionActionRequest(t, host, "7", token, "stale", body))
	assertJSONError(t, stale, http.StatusConflict, "host_not_fresh")
	staleShadow := httptest.NewRecorder()
	staleRequest := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+host+"/7/shadow", nil)
	staleRequest.SetPathValue("host", host)
	staleRequest.SetPathValue("sessionID", "7")
	staleRequest.AddCookie(&http.Cookie{Name: "drainctl_session", Value: token})
	ds.handleSessionShadow(staleShadow, staleRequest)
	assertJSONError(t, staleShadow, http.StatusConflict, "sessions_unavailable")

	ds.now = time.Now
	ds.testLoadConfigFunc = func() (*dc.Config, error) {
		return &dc.Config{Sessions: dc.SessionsConfig{Enabled: true, IdentityVisibility: dc.SessionVisibilityFull, ClientVisibility: dc.SessionVisibilityFull, ProcessVisibility: dc.SessionVisibilityFull}}, nil
	}
	shadow := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+host+"/7/shadow", nil)
	r.SetPathValue("host", host)
	r.SetPathValue("sessionID", "7")
	r.AddCookie(&http.Cookie{Name: "drainctl_session", Value: token})
	ds.handleSessionShadow(shadow, r)
	var response struct {
		Command     string `json:"command"`
		ProtocolURI string `json:"protocol_uri"`
	}
	if shadow.Code != http.StatusOK || shadow.Header().Get("Cache-Control") != "no-store" || json.NewDecoder(shadow.Body).Decode(&response) != nil || response.Command != `mstsc.exe /v:`+host+` /shadow:7 /control` || response.ProtocolURI != `drainctl-shadow://shadow?host=`+host+`&session=7` {
		t.Fatalf("shadow=%d %#v %s", shadow.Code, response, shadow.Body.String())
	}

	unauthenticated := httptest.NewRecorder()
	unauthenticatedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+host+"/7/shadow", nil)
	unauthenticatedRequest.SetPathValue("host", host)
	unauthenticatedRequest.SetPathValue("sessionID", "7")
	ds.handleSessionShadow(unauthenticated, unauthenticatedRequest)
	assertJSONError(t, unauthenticated, http.StatusUnauthorized, "authentication_required")

	missing := httptest.NewRecorder()
	missingRequest := r.Clone(context.Background())
	missingRequest.SetPathValue("sessionID", "8")
	invalid := httptest.NewRecorder()
	invalidRequest := r.Clone(context.Background())
	invalidRequest.SetPathValue("host", "bad host")
	invalidRequest.SetPathValue("sessionID", "not-a-number")
	ds.handleSessionShadow(invalid, invalidRequest)
	assertJSONError(t, invalid, http.StatusBadRequest, "invalid_session_target")
	ds.handleSessionShadow(missing, missingRequest)
	assertJSONError(t, missing, http.StatusConflict, "session_changed")

	fatalShadow := testSessionSnapshot(host, 6)
	fatalShadow.CollectionError = &sessiondata.CollectionError{Code: sessiondata.CollectionErrorWTSEnumerationFailed}
	fatalShadow.Capabilities = sessiondata.SessionCapabilities{}
	fatalShadow.Sessions = nil
	if _, err := ds.ingestSessionSnapshot(context.Background(), fatalShadow); err != nil {
		t.Fatal(err)
	}
	fatalResult := httptest.NewRecorder()
	ds.handleSessionShadow(fatalResult, r.Clone(context.Background()))
	assertJSONError(t, fatalResult, http.StatusConflict, "sessions_unavailable")

	disabled := httptest.NewRecorder()
	ds.testLoadConfigFunc = func() (*dc.Config, error) { return &dc.Config{Sessions: dc.SessionsConfig{}}, nil }
	ds.handleSessionShadow(disabled, r.Clone(context.Background()))
	assertJSONError(t, disabled, http.StatusConflict, "sessions_unavailable")
}

func TestSessionAction_ExpiryAndPrivacyTransitionsBroadcast(t *testing.T) {
	cfg := dc.SessionsConfig{Enabled: true, AllowActions: true, IdentityVisibility: dc.SessionVisibilityFull, ClientVisibility: dc.SessionVisibilityFull, ProcessVisibility: dc.SessionVisibilityFull}
	ds, _, token := newSessionActionTestServer(t, cfg)
	host := "broadcast.example.test"
	logon := seedActionTarget(t, ds, host)
	create := func(key string) sessiondata.SessionActionStatus {
		t.Helper()
		w := httptest.NewRecorder()
		ds.handleSessionAction(w, sessionActionRequest(t, host, "7", token, key, `{"type":"logoff","expected_logon_at_ms":`+strconv.FormatInt(logon, 10)+`}`))
		if w.Code != http.StatusAccepted {
			t.Fatalf("enqueue status=%d body=%s", w.Code, w.Body.String())
		}
		var response struct {
			Action sessiondata.SessionActionStatus `json:"action"`
		}
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatal(err)
		}
		return response.Action
	}
	expiring := create("expiring")
	id, events, _, err := ds.broker.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer ds.broker.Unsubscribe(id)
	ds.now = func() time.Time { return time.UnixMilli(expiring.ExpiresAtMS) }
	statusResult := httptest.NewRecorder()
	statusRequest := httptest.NewRequest(http.MethodGet, "/api/v1/session-actions/"+expiring.ActionID, nil)
	statusRequest.SetPathValue("actionID", expiring.ActionID)
	ds.handleSessionActionStatus(statusResult, statusRequest)
	if statusResult.Code != http.StatusOK {
		t.Fatalf("expire status=%d body=%s", statusResult.Code, statusResult.Body.String())
	}
	assertTransitionEvent(t, events, expiring.ActionID, "expired")

	ds.now = time.Now
	privacy := create("privacy")
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("missing queued action event")
	}
	if err := ds.PurgeSessionSnapshotsForPrivacy(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertTransitionEvent(t, events, privacy.ActionID, "privacy_policy_changed")
}

func assertTransitionEvent(t *testing.T, events <-chan []byte, actionID, resultCode string) {
	t.Helper()
	select {
	case raw := <-events:
		var event SSEEvent
		var status sessiondata.SessionActionStatus
		if err := json.Unmarshal(raw, &event); err != nil || json.Unmarshal(event.Data, &status) != nil {
			t.Fatalf("invalid action event=%s err=%v", raw, err)
		}
		if event.Type != "session_action" || status.ActionID != actionID || status.State != sessiondata.SessionActionExpired || status.ResultCode == nil || *status.ResultCode != resultCode {
			t.Fatalf("transition event=%+v status=%+v", event, status)
		}
	case <-time.After(time.Second):
		t.Fatalf("missing %s transition for %s", resultCode, actionID)
	}
}
