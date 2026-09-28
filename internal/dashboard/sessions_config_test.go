//go:build windows

package dashboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

type failingSessionActionCanceller struct {
	sessionActionStore
	err error
}

func (s failingSessionActionCanceller) CancelForPrivacy(context.Context, time.Time) ([]sessiondata.SessionActionStatus, error) {
	return nil, s.err
}

type recordingSessionSnapshotPurger struct {
	sessionSnapshotWriter
	calls int
}

func (s *recordingSessionSnapshotPurger) PurgeSnapshots(ctx context.Context, hours int, now time.Time) (int64, error) {
	s.calls++
	return s.sessionSnapshotWriter.PurgeSnapshots(ctx, hours, now)
}

const sessionPrivacyTestAgentInstanceID = "01890f9d-5c00-7000-8000-000000000001"

func sessionSettingsRequest(t *testing.T, body string, auth *AuthInfo) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
	if auth != nil {
		r = r.WithContext(context.WithValue(r.Context(), authInfoKey, auth))
	}
	return r
}

func TestSessionSettings_AdminMutationAndInvalidInput(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	if err := dc.SaveConfig(dc.DefaultConfig()); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	ds := newTestServer(t)
	admin := &AuthInfo{Username: "admin", Groups: []string{ds.cfg.Group}}

	body := `{"sessions":{"enabled":false,"collect_processes":false,"top_processes":5,"retention_hours":48,"allow_actions":true,"identity_visibility":"masked","client_visibility":"hidden","process_visibility":"masked"}}`
	w := httptest.NewRecorder()
	ds.handlePutSettings(w, sessionSettingsRequest(t, body, admin))
	if w.Code != http.StatusOK {
		t.Fatalf("admin update status = %d: %s", w.Code, w.Body.String())
	}
	got, err := dc.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	want := dc.SessionsConfig{Enabled: false, CollectProcesses: false, TopProcesses: 5, RetentionHours: 48, AllowActions: true, IdentityVisibility: dc.SessionVisibilityMasked, ClientVisibility: dc.SessionVisibilityHidden, ProcessVisibility: dc.SessionVisibilityMasked}
	if !reflect.DeepEqual(got.Sessions, want) {
		t.Fatalf("sessions = %#v, want %#v", got.Sessions, want)
	}

	invalid := `{"sessions":{"enabled":true,"collect_processes":true,"top_processes":6,"retention_hours":24,"allow_actions":false,"identity_visibility":"full","client_visibility":"full","process_visibility":"full"}}`
	w = httptest.NewRecorder()
	ds.handlePutSettings(w, sessionSettingsRequest(t, invalid, admin))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid update status = %d: %s", w.Code, w.Body.String())
	}
	afterInvalid, err := dc.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !reflect.DeepEqual(afterInvalid.Sessions, want) {
		t.Errorf("invalid input changed sessions: %#v", afterInvalid.Sessions)
	}
}

func TestSessionSettings_NonAdminCannotMutate(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	if err := dc.SaveConfig(dc.DefaultConfig()); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	ds := newTestServer(t)
	w := httptest.NewRecorder()
	ds.handlePutSettings(w, sessionSettingsRequest(t, `{"sessions":{"enabled":false,"collect_processes":true,"top_processes":3,"retention_hours":24,"allow_actions":false,"identity_visibility":"full","client_visibility":"full","process_visibility":"full"}}`, &AuthInfo{Username: "user"}))
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-admin update status = %d: %s", w.Code, w.Body.String())
	}
	cfg, err := dc.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.Sessions.Enabled {
		t.Error("non-admin mutation changed persisted sessions config")
	}
}

func TestRemoteSettings_RedactsDashboardOnlySessionFields(t *testing.T) {
	ds := newTestServer(t)
	ds.testLoadConfigFunc = func() (*dc.Config, error) {
		cfg := dc.DefaultConfig()
		cfg.Sessions = dc.SessionsConfig{Enabled: true, CollectProcesses: false, TopProcesses: 2, RetentionHours: 72, AllowActions: true, IdentityVisibility: dc.SessionVisibilityMasked, ClientVisibility: dc.SessionVisibilityHidden, ProcessVisibility: dc.SessionVisibilityFull}
		return cfg, nil
	}
	w := httptest.NewRecorder()
	ds.handleGetSettings(w, httptest.NewRequest(http.MethodGet, "/api/v1/config", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("remote config status = %d: %s", w.Code, w.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(w.Body).Decode(&raw); err != nil {
		t.Fatalf("decode remote config: %v", err)
	}
	var sessions map[string]json.RawMessage
	if err := json.Unmarshal(raw["sessions"], &sessions); err != nil {
		t.Fatalf("decode sessions: %v", err)
	}
	if _, ok := sessions["allow_actions"]; ok {
		t.Error("remote sessions config disclosed allow_actions")
	}
	if _, ok := sessions["retention_hours"]; ok {
		t.Error("remote sessions config disclosed retention_hours")
	}
	var remote RemoteSessionsConfig
	if err := json.Unmarshal(raw["sessions"], &remote); err != nil {
		t.Fatalf("decode projected sessions: %v", err)
	}
	if !remote.Enabled || remote.CollectProcesses || remote.TopProcesses != 2 || remote.IdentityVisibility != dc.SessionVisibilityMasked || remote.ClientVisibility != dc.SessionVisibilityHidden || remote.ProcessVisibility != dc.SessionVisibilityFull {
		t.Errorf("remote sessions = %#v", remote)
	}
}

func TestSettingsGet_ExposesFullSessionsConfig(t *testing.T) {
	ds := newTestServer(t)
	ds.testLoadConfigFunc = func() (*dc.Config, error) {
		cfg := dc.DefaultConfig()
		cfg.Sessions.AllowActions = true
		cfg.Sessions.RetentionHours = 48
		return cfg, nil
	}
	w := httptest.NewRecorder()
	ds.handleGetSettings(w, httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("settings status = %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Sessions dc.SessionsConfig `json:"sessions"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if !response.Sessions.AllowActions || response.Sessions.RetentionHours != 48 {
		t.Errorf("sessions GET projection = %#v", response.Sessions)
	}
}

func sessionSettingsJSON(t *testing.T, sessions dc.SessionsConfig) string {
	t.Helper()
	body, err := json.Marshal(struct {
		Sessions dc.SessionsConfig `json:"sessions"`
	}{Sessions: sessions})
	if err != nil {
		t.Fatalf("marshal sessions settings: %v", err)
	}
	return string(body)
}

func newSessionPrivacySettingsServer(t *testing.T, current dc.SessionsConfig) (*DashboardServer, *telemetry.DB, *telemetry.SessionSnapshotStore, *telemetry.SessionQueryStore) {
	t.Helper()
	t.Setenv("ProgramData", t.TempDir())
	cfg := dc.DefaultConfig()
	cfg.Sessions = current
	if err := dc.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
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
	return ds, db, snapshots, queries
}

func enqueueSessionPolicyAction(t *testing.T, ds *DashboardServer, db *telemetry.DB, actionID string) *telemetry.SessionActionStore {
	t.Helper()
	actions := telemetry.NewSessionActionStore(db)
	ds.sessionActions = actions
	now := time.Now().UTC()
	_, _, err := actions.Enqueue(context.Background(), telemetry.SessionActionEnqueue{
		Action: sessiondata.SessionAction{
			ActionID: actionID, CanonicalHost: "policy.example.test", SessionID: 7, ExpectedLogonAtMS: 1,
			Type: sessiondata.SessionActionLogoff, State: sessiondata.SessionActionQueued,
			CreatedAtMS: now.UnixMilli(), ExpiresAtMS: now.Add(sessionActionTTL).UnixMilli(),
			RequestedBy: "admin", IdempotencyKey: actionID, RequestFingerprint: []byte(actionID),
		},
		IdempotencyEndpoint: "POST /sessions/policy.example.test/7/actions",
	})
	if err != nil {
		t.Fatalf("enqueue session action: %v", err)
	}
	return actions
}

func seedRawSessionSnapshot(t *testing.T, snapshots *telemetry.SessionSnapshotStore, host string) {
	t.Helper()
	snapshot := testSessionSnapshot(host, 1)
	snapshot.AgentInstanceID = sessionPrivacyTestAgentInstanceID
	_, err := snapshots.Apply(context.Background(), snapshot, sessiondata.PrivacyPolicy{
		Identity: sessiondata.Visibility(dc.SessionVisibilityFull),
		Client:   sessiondata.Visibility(dc.SessionVisibilityFull),
		Process:  sessiondata.Visibility(dc.SessionVisibilityFull),
	})
	if err != nil {
		t.Fatalf("seed raw session snapshot: %v", err)
	}
}

func sessionConfigDetailQuery() telemetry.SessionDetailQuery {
	return telemetry.SessionDetailQuery{Page: 1, PageSize: 30, Now: time.Now(), HeartbeatInterval: time.Minute}
}

func TestSessionSettings_VisibilityChangesPurgeRetainedSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name string
		next func(dc.SessionsConfig) dc.SessionsConfig
	}{
		{"identity", func(s dc.SessionsConfig) dc.SessionsConfig {
			s.IdentityVisibility = dc.SessionVisibilityMasked
			return s
		}},
		{"client", func(s dc.SessionsConfig) dc.SessionsConfig { s.ClientVisibility = dc.SessionVisibilityMasked; return s }},
		{"process", func(s dc.SessionsConfig) dc.SessionsConfig {
			s.ProcessVisibility = dc.SessionVisibilityMasked
			return s
		}},
		{"loosening", func(s dc.SessionsConfig) dc.SessionsConfig { s.IdentityVisibility = dc.SessionVisibilityFull; return s }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := dc.DefaultConfig().Sessions
			if tc.name == "loosening" {
				current.IdentityVisibility = dc.SessionVisibilityHidden
			}
			next := tc.next(current)
			ds, _, snapshots, queries := newSessionPrivacySettingsServer(t, current)
			host := "privacy-" + tc.name + ".example.test"
			seedRawSessionSnapshot(t, snapshots, host)

			w := httptest.NewRecorder()
			ds.handlePutSettings(w, sessionSettingsRequest(t, sessionSettingsJSON(t, next), &AuthInfo{Username: "admin", Groups: []string{ds.cfg.Group}}))
			if w.Code != http.StatusOK {
				t.Fatalf("update status = %d: %s", w.Code, w.Body.String())
			}
			persisted, err := dc.LoadConfig()
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if !reflect.DeepEqual(persisted.Sessions, next) {
				t.Fatalf("persisted sessions = %#v, want %#v", persisted.Sessions, next)
			}
			if _, err := queries.Detail(context.Background(), host, sessionConfigDetailQuery()); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("raw snapshot remained after policy change: %v", err)
			}
		})
	}
}

func TestSessionSettings_UnchangedAndNonPrivacyChangesDoNotPurgeSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name string
		next func(dc.SessionsConfig) dc.SessionsConfig
	}{
		{"unchanged", func(s dc.SessionsConfig) dc.SessionsConfig { return s }},
		{"nonprivacy", func(s dc.SessionsConfig) dc.SessionsConfig { s.Enabled = false; return s }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := dc.DefaultConfig().Sessions
			next := tc.next(current)
			ds, _, snapshots, queries := newSessionPrivacySettingsServer(t, current)
			host := "retain-" + tc.name + ".example.test"
			seedRawSessionSnapshot(t, snapshots, host)

			w := httptest.NewRecorder()
			ds.handlePutSettings(w, sessionSettingsRequest(t, sessionSettingsJSON(t, next), &AuthInfo{Username: "admin", Groups: []string{ds.cfg.Group}}))
			if w.Code != http.StatusOK {
				t.Fatalf("update status = %d: %s", w.Code, w.Body.String())
			}
			detail, err := queries.Detail(context.Background(), host, sessionConfigDetailQuery())
			if err != nil || len(detail.Sessions) != 1 || detail.Sessions[0].User == nil {
				t.Fatalf("non-privacy change purged raw snapshot: detail=%+v err=%v", detail, err)
			}
		})
	}
}

func TestSessionSettings_RetentionReductionRunsMaintenanceImmediately(t *testing.T) {
	current := dc.DefaultConfig().Sessions
	current.RetentionHours = 48
	next := current
	next.RetentionHours = 24
	ds, _, snapshots, _ := newSessionPrivacySettingsServer(t, current)
	purger := &recordingSessionSnapshotPurger{sessionSnapshotWriter: snapshots}
	ds.sessionSnapshots = purger

	w := httptest.NewRecorder()
	ds.handlePutSettings(w, sessionSettingsRequest(t, sessionSettingsJSON(t, next), &AuthInfo{Username: "admin", Groups: []string{ds.cfg.Group}}))
	if w.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", w.Code, w.Body.String())
	}
	if purger.calls != 1 {
		t.Fatalf("retention maintenance calls=%d, want 1", purger.calls)
	}
}

func TestSessionSettings_PrivacyPurgeFailurePreventsConfigMutation(t *testing.T) {
	current := dc.DefaultConfig().Sessions
	next := current
	next.IdentityVisibility = dc.SessionVisibilityHidden
	ds, db, _, _ := newSessionPrivacySettingsServer(t, current)
	if err := db.Close(); err != nil {
		t.Fatalf("close telemetry DB: %v", err)
	}

	body, err := json.Marshal(struct {
		SessionWarningThreshold int               `json:"session_warning_threshold"`
		Sessions                dc.SessionsConfig `json:"sessions"`
	}{SessionWarningThreshold: 99, Sessions: next})
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	w := httptest.NewRecorder()
	ds.handlePutSettings(w, sessionSettingsRequest(t, string(body), &AuthInfo{Username: "admin", Groups: []string{ds.cfg.Group}}))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("update status = %d: %s", w.Code, w.Body.String())
	}
	persisted, err := dc.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !reflect.DeepEqual(persisted.Sessions, current) {
		t.Fatalf("purge failure changed sessions: %#v", persisted.Sessions)
	}
	if persisted.SessionWarningThreshold != dc.DefaultConfig().SessionWarningThreshold {
		t.Fatalf("purge failure changed non-session settings: threshold=%d", persisted.SessionWarningThreshold)
	}
}

func TestSessionSettings_ActionPolicyDisablesCancelActiveActions(t *testing.T) {
	for _, tc := range []struct {
		name         string
		deliverFirst bool
		next         func(dc.SessionsConfig) dc.SessionsConfig
	}{
		{"sessions_disabled_queued", false, func(s dc.SessionsConfig) dc.SessionsConfig { s.Enabled = false; return s }},
		{"actions_disabled_delivered", true, func(s dc.SessionsConfig) dc.SessionsConfig { s.AllowActions = false; return s }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := dc.DefaultConfig().Sessions
			current.AllowActions = true
			ds, db, _, _ := newSessionPrivacySettingsServer(t, current)
			actions := enqueueSessionPolicyAction(t, ds, db, tc.name)
			if tc.deliverFirst {
				delivered, err := actions.Deliver(context.Background(), "policy.example.test", time.Now())
				if err != nil || len(delivered) != 1 {
					t.Fatalf("initial delivery=%+v err=%v", delivered, err)
				}
			}
			id, events, _, err := ds.broker.Subscribe()
			if err != nil {
				t.Fatal(err)
			}
			defer ds.broker.Unsubscribe(id)

			next := tc.next(current)
			w := httptest.NewRecorder()
			ds.handlePutSettings(w, sessionSettingsRequest(t, sessionSettingsJSON(t, next), &AuthInfo{Username: "admin", Groups: []string{ds.cfg.Group}}))
			if w.Code != http.StatusOK {
				t.Fatalf("update status=%d body=%s", w.Code, w.Body.String())
			}
			assertTransitionEvent(t, events, tc.name, "privacy_policy_changed")
			delivered, err := actions.Deliver(context.Background(), "policy.example.test", time.Now())
			if err != nil || len(delivered) != 0 {
				t.Fatalf("disabled policy delivered actions=%+v err=%v", delivered, err)
			}
		})
	}
}

func TestSessionSettings_VisibilityAndActionDisableCancelOnce(t *testing.T) {
	current := dc.DefaultConfig().Sessions
	current.AllowActions = true
	ds, db, _, _ := newSessionPrivacySettingsServer(t, current)
	enqueueSessionPolicyAction(t, ds, db, "combined-policy")
	id, events, _, err := ds.broker.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer ds.broker.Unsubscribe(id)

	next := current
	next.Enabled = false
	next.IdentityVisibility = dc.SessionVisibilityMasked
	w := httptest.NewRecorder()
	ds.handlePutSettings(w, sessionSettingsRequest(t, sessionSettingsJSON(t, next), &AuthInfo{Username: "admin", Groups: []string{ds.cfg.Group}}))
	if w.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", w.Code, w.Body.String())
	}
	assertTransitionEvent(t, events, "combined-policy", "privacy_policy_changed")
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case raw := <-events:
			var event SSEEvent
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatalf("decode post-transition event: %v", err)
			}
			if event.Type == "session_action" {
				t.Fatalf("duplicate cancellation event=%s", raw)
			}
		case <-timer.C:
			return
		}
	}
}

func TestSessionSettings_ActionCancellationFailurePreventsConfigMutation(t *testing.T) {
	current := dc.DefaultConfig().Sessions
	current.AllowActions = true
	ds, _, _, _ := newSessionPrivacySettingsServer(t, current)
	ds.sessionActions = failingSessionActionCanceller{sessionActionStore: ds.sessionActions, err: errors.New("cancel failed")}
	next := current
	next.AllowActions = false

	w := httptest.NewRecorder()
	ds.handlePutSettings(w, sessionSettingsRequest(t, sessionSettingsJSON(t, next), &AuthInfo{Username: "admin", Groups: []string{ds.cfg.Group}}))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("update status=%d body=%s", w.Code, w.Body.String())
	}
	persisted, err := dc.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !reflect.DeepEqual(persisted.Sessions, current) {
		t.Fatalf("cancellation failure changed sessions: %#v", persisted.Sessions)
	}
}
