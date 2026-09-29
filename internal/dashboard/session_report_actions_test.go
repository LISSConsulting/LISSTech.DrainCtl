//go:build windows

package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
	"github.com/google/uuid"
)

type sessionActionReportResponse struct {
	OK                           bool                               `json:"ok"`
	PendingSessionActions        []sessiondata.PendingSessionAction `json:"pending_session_actions"`
	AcknowledgedSessionActionIDs []string                           `json:"acknowledged_session_action_ids"`
}

type blockingDeliveryStore struct {
	sessionActionStore
	entered chan<- struct{}
	release <-chan struct{}
}

func (s blockingDeliveryStore) Deliver(ctx context.Context, host string, now time.Time) ([]telemetry.SessionActionDelivery, error) {
	select {
	case s.entered <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.sessionActionStore.Deliver(ctx, host, now)
}

func reportSessionActions(t *testing.T, ds *DashboardServer, body string) sessionActionReportResponse {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/report", strings.NewReader(body))
	ds.handleReport(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("report status=%d body=%s", w.Code, w.Body.String())
	}
	var response sessionActionReportResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestReportSessionActions_DeliveryRedeliveryCompletionAndAcknowledgement(t *testing.T) {
	cfg := dc.SessionsConfig{Enabled: true, AllowActions: true, IdentityVisibility: dc.SessionVisibilityFull, ClientVisibility: dc.SessionVisibilityFull, ProcessVisibility: dc.SessionVisibilityFull}
	ds, actions, _ := newSessionActionTestServer(t, cfg)
	host := "report-actions.example.test"
	logon := seedActionTarget(t, ds, host)
	now := ds.clock()
	actionID := uuid.NewString()
	_, _, err := actions.Enqueue(context.Background(), telemetry.SessionActionEnqueue{Action: sessiondata.SessionAction{
		ActionID: actionID, CanonicalHost: host, SessionID: 7, ExpectedLogonAtMS: logon,
		Type: sessiondata.SessionActionLogoff, State: sessiondata.SessionActionQueued,
		CreatedAtMS: now.UnixMilli(), ExpiresAtMS: now.Add(5 * time.Minute).UnixMilli(),
		RequestedBy: "test", IdempotencyKey: actionID, RequestFingerprint: []byte(actionID),
	}, IdempotencyEndpoint: "POST /test"})
	if err != nil {
		t.Fatal(err)
	}

	report := `{"host":"` + host + `","version":"test"}`
	first := reportSessionActions(t, ds, report)
	if !first.OK || len(first.PendingSessionActions) != 1 || first.PendingSessionActions[0].ActionID != actionID || first.PendingSessionActions[0].Message != nil || len(first.AcknowledgedSessionActionIDs) != 0 {
		status, statusErr := actions.Status(context.Background(), actionID)
		t.Fatalf("first report=%+v status=%+v err=%v", first, status, statusErr)
	}
	second := reportSessionActions(t, ds, report)
	if len(second.PendingSessionActions) != 1 || second.PendingSessionActions[0].ActionID != actionID {
		t.Fatalf("redelivery=%+v", second)
	}

	wrongHost := "wrong-report-actions.example.test"
	ds.state.Register(wrongHost)
	wrong := reportSessionActions(t, ds, `{"host":"`+wrongHost+`","version":"test","completed_session_actions":[{"action_id":"`+actionID+`","outcome":"completed"}]}`)
	if len(wrong.AcknowledgedSessionActionIDs) != 0 {
		t.Fatalf("wrong-host completion acknowledged: %+v", wrong)
	}
	unknown := reportSessionActions(t, ds, `{"host":"`+host+`","version":"test","completed_session_actions":[{"action_id":"`+uuid.NewString()+`","outcome":"completed"}]}`)
	if len(unknown.AcknowledgedSessionActionIDs) != 0 {
		t.Fatalf("unknown completion acknowledged: %+v", unknown)
	}

	completed := reportSessionActions(t, ds, `{"host":"`+host+`","version":"test","completed_session_actions":[{"action_id":"`+actionID+`","outcome":"completed"}]}`)
	if len(completed.AcknowledgedSessionActionIDs) != 1 || completed.AcknowledgedSessionActionIDs[0] != actionID || len(completed.PendingSessionActions) != 0 {
		t.Fatalf("completion response=%+v", completed)
	}
	status, err := actions.Status(context.Background(), actionID)
	if err != nil || status.State != sessiondata.SessionActionCompleted {
		t.Fatalf("completed status=%+v err=%v", status, err)
	}
	replayed := reportSessionActions(t, ds, `{"host":"`+host+`","version":"test","completed_session_actions":[{"action_id":"`+actionID+`","outcome":"completed"}]}`)
	if len(replayed.AcknowledgedSessionActionIDs) != 1 || replayed.AcknowledgedSessionActionIDs[0] != actionID || len(replayed.PendingSessionActions) != 0 {
		t.Fatalf("replayed completion response=%+v", replayed)
	}
	duplicate := reportSessionActions(t, ds, `{"host":"`+host+`","version":"test","completed_session_actions":[{"action_id":"`+actionID+`","outcome":"duplicate"}]}`)
	if len(duplicate.AcknowledgedSessionActionIDs) != 1 || duplicate.AcknowledgedSessionActionIDs[0] != actionID || len(duplicate.PendingSessionActions) != 0 {
		t.Fatalf("duplicate completion response=%+v", duplicate)
	}

	ds.wireServerStateCallbacks()
	localActionID := uuid.NewString()
	_, _, err = actions.Enqueue(context.Background(), telemetry.SessionActionEnqueue{Action: sessiondata.SessionAction{
		ActionID: localActionID, CanonicalHost: host, SessionID: 7, ExpectedLogonAtMS: logon,
		Type: sessiondata.SessionActionLogoff, State: sessiondata.SessionActionQueued,
		CreatedAtMS: now.UnixMilli(), ExpiresAtMS: now.Add(5 * time.Minute).UnixMilli(),
		RequestedBy: "test", IdempotencyKey: localActionID, RequestFingerprint: []byte(localActionID),
	}, IdempotencyEndpoint: "POST /test"})
	if err != nil {
		t.Fatal(err)
	}
	handled, localPending, err := ds.state.DeliverPendingSessionActions(host)
	if err != nil || !handled || len(localPending) != 1 || localPending[0].ActionID != localActionID {
		t.Fatalf("local delivery handled=%t pending=%+v err=%v", handled, localPending, err)
	}
	handled, acknowledged, err := ds.state.CompletePendingSessionActions(host, []sessiondata.CompletedSessionAction{{ActionID: localActionID, Outcome: sessiondata.SessionActionOutcomeCompleted}})
	if err != nil || !handled || len(acknowledged) != 1 || acknowledged[0] != localActionID {
		t.Fatalf("local completion handled=%t acknowledged=%v err=%v", handled, acknowledged, err)
	}

	legacy := reportSessionActions(t, ds, report)
	if legacy.PendingSessionActions == nil || legacy.AcknowledgedSessionActionIDs == nil || len(legacy.PendingSessionActions) != 0 || len(legacy.AcknowledgedSessionActionIDs) != 0 {
		t.Fatalf("legacy report response=%+v", legacy)
	}

}

func TestReportSessionActions_DisabledPolicyNeverDeliversRetainedAction(t *testing.T) {
	cfg := dc.SessionsConfig{Enabled: true, AllowActions: true, IdentityVisibility: dc.SessionVisibilityFull, ClientVisibility: dc.SessionVisibilityFull, ProcessVisibility: dc.SessionVisibilityFull}
	ds, actions, _ := newSessionActionTestServer(t, cfg)
	current := cfg
	ds.testLoadConfigFunc = func() (*dc.Config, error) { return &dc.Config{Sessions: current}, nil }
	host := "disabled-report-actions.example.test"
	logon := seedActionTarget(t, ds, host)
	actionID := uuid.NewString()
	if _, _, err := actions.Enqueue(context.Background(), telemetry.SessionActionEnqueue{Action: sessiondata.SessionAction{
		ActionID: actionID, CanonicalHost: host, SessionID: 7, ExpectedLogonAtMS: logon,
		Type: sessiondata.SessionActionLogoff, State: sessiondata.SessionActionQueued,
		CreatedAtMS: ds.clock().UnixMilli(), ExpiresAtMS: ds.clock().Add(sessionActionTTL).UnixMilli(),
		RequestedBy: "test", IdempotencyKey: actionID, RequestFingerprint: []byte(actionID),
	}, IdempotencyEndpoint: "POST /test"}); err != nil {
		t.Fatal(err)
	}

	// This models process restart with an already-persisted active command:
	// retained rows remain, but the current policy must prevent selection.
	current.Enabled = false
	response := reportSessionActions(t, ds, `{"host":"`+host+`","version":"test"}`)
	if len(response.PendingSessionActions) != 0 {
		t.Fatalf("disabled policy delivered retained actions: %+v", response.PendingSessionActions)
	}
	status, err := actions.Status(context.Background(), actionID)
	if err != nil || status.State != sessiondata.SessionActionQueued {
		t.Fatalf("disabled delivery changed retained action: status=%+v err=%v", status, err)
	}

	current.Enabled = true
	current.AllowActions = false
	response = reportSessionActions(t, ds, `{"host":"`+host+`","version":"test"}`)
	if len(response.PendingSessionActions) != 0 {
		t.Fatalf("actions-disabled policy delivered retained actions: %+v", response.PendingSessionActions)
	}
	status, err = actions.Status(context.Background(), actionID)
	if err != nil || status.State != sessiondata.SessionActionQueued {
		t.Fatalf("actions-disabled delivery changed retained action: status=%+v err=%v", status, err)
	}
}

func TestReportSessionActions_DeliverySerializesWithPolicyTransition(t *testing.T) {
	cfg := dc.SessionsConfig{Enabled: true, AllowActions: true, IdentityVisibility: dc.SessionVisibilityFull, ClientVisibility: dc.SessionVisibilityFull, ProcessVisibility: dc.SessionVisibilityFull}
	ds, actions, _ := newSessionActionTestServer(t, cfg)
	host := "serialized-report-actions.example.test"
	logon := seedActionTarget(t, ds, host)
	actionID := uuid.NewString()
	if _, _, err := actions.Enqueue(context.Background(), telemetry.SessionActionEnqueue{Action: sessiondata.SessionAction{
		ActionID: actionID, CanonicalHost: host, SessionID: 7, ExpectedLogonAtMS: logon,
		Type: sessiondata.SessionActionLogoff, State: sessiondata.SessionActionQueued,
		CreatedAtMS: ds.clock().UnixMilli(), ExpiresAtMS: ds.clock().Add(sessionActionTTL).UnixMilli(),
		RequestedBy: "test", IdempotencyKey: actionID, RequestFingerprint: []byte(actionID),
	}, IdempotencyEndpoint: "POST /test"}); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	ds.sessionActions = blockingDeliveryStore{sessionActionStore: actions, entered: entered, release: release}
	deliveryDone := make(chan error, 1)
	go func() {
		_, err := ds.deliverSessionActions(context.Background(), host)
		deliveryDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("delivery did not reach selection")
	}

	transitionDone := make(chan error, 1)
	go func() {
		transitionDone <- ds.CancelSessionActionsForPolicy(context.Background())
	}()
	select {
	case err := <-transitionDone:
		t.Fatalf("policy transition ran during action delivery: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if err := <-deliveryDone; err != nil {
		t.Fatalf("delivery error: %v", err)
	}
	if err := <-transitionDone; err != nil {
		t.Fatalf("policy transition error: %v", err)
	}
	status, err := actions.Status(context.Background(), actionID)
	if err != nil || status.State != sessiondata.SessionActionExpired {
		t.Fatalf("transition did not cancel delivered action: status=%+v err=%v", status, err)
	}
}
