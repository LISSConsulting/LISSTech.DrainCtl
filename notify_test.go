//go:build windows

package drainctl

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ── webhookSignature ──────────────────────────────────────────────────────────

func TestWebhookSignature_Format(t *testing.T) {
	sig := webhookSignature("secret", []byte(`{"test":true}`))
	if !strings.HasPrefix(sig, "sha256=") {
		t.Errorf("signature %q does not start with 'sha256='", sig)
	}
	hex := sig[len("sha256="):]
	if len(hex) != 64 {
		t.Errorf("hex part of signature has length %d, want 64", len(hex))
	}
}

func TestWebhookSignature_Correctness(t *testing.T) {
	secret := "test-secret"
	body := []byte(`{"event":"drain_on"}`)

	got := webhookSignature(secret, body)

	// Compute expected independently.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if got != expected {
		t.Errorf("webhookSignature = %q, want %q", got, expected)
	}
}

func TestWebhookSignature_DifferentSecrets(t *testing.T) {
	body := []byte(`{"event":"test"}`)
	sig1 := webhookSignature("secret-a", body)
	sig2 := webhookSignature("secret-b", body)
	if sig1 == sig2 {
		t.Error("different secrets produced identical signatures")
	}
}

func TestWebhookSignature_DifferentBodies(t *testing.T) {
	secret := "same-secret"
	sig1 := webhookSignature(secret, []byte(`{"event":"drain_on"}`))
	sig2 := webhookSignature(secret, []byte(`{"event":"drain_off"}`))
	if sig1 == sig2 {
		t.Error("different bodies produced identical signatures")
	}
}

// ── sendWebhook ───────────────────────────────────────────────────────────────

func TestSendWebhook_NoSecret(t *testing.T) {
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	payload := map[string]any{"event": "test", "host": "SRV01"}
	if err := sendWebhook(srv.URL, "", payload); err != nil {
		t.Fatalf("sendWebhook error: %v", err)
	}

	if captured.Header.Get("X-DrainCtl-Signature") != "" {
		t.Error("expected no X-DrainCtl-Signature header when secret is empty")
	}
	if ct := captured.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestSendWebhook_WithSecret(t *testing.T) {
	var capturedSig string
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedSig = r.Header.Get("X-DrainCtl-Signature")
		var buf [4096]byte
		n, _ := r.Body.Read(buf[:])
		capturedBody = buf[:n]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	secret := "my-signing-secret"
	payload := map[string]any{"event": "drain_on"}
	if err := sendWebhook(srv.URL, secret, payload); err != nil {
		t.Fatalf("sendWebhook error: %v", err)
	}

	if !strings.HasPrefix(capturedSig, "sha256=") {
		t.Errorf("X-DrainCtl-Signature = %q, expected sha256=... prefix", capturedSig)
	}

	// Verify the signature matches the body.
	expected := webhookSignature(secret, capturedBody)
	if capturedSig != expected {
		t.Errorf("signature mismatch:\n  got  %s\n  want %s", capturedSig, expected)
	}
}

func TestSendWebhook_NonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := sendWebhook(srv.URL, "", map[string]any{"event": "test"})
	if err == nil {
		t.Error("expected error for 500 response, got nil")
	}
}

// ── SendNotification ──────────────────────────────────────────────────────────

func newTestResult(host, status string) *CheckResult {
	dur := 0.0
	connAllowed := status == "Healthy"
	return &CheckResult{
		Host:                 host,
		Status:               status,
		DrainModeLabel:       "AllowAll",
		Message:              status + " message",
		ConnectionsAllowed:   &connAllowed,
		StateDurationSeconds: &dur,
		Timestamp:            time.Now(),
	}
}

func TestSendNotification_CallsWebhook(t *testing.T) {
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&count, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	targets := []NotificationTarget{
		{Type: "webhook", URL: srv.URL, Triggers: []Trigger{TriggerDrainOn}},
	}
	state := &NotifyState{}
	result := newTestResult("SRV01", "Healthy")

	SendNotification(targets, state, result, TriggerDrainOn, "", nil)

	if atomic.LoadInt32(&count) != 1 {
		t.Errorf("webhook called %d times, want 1", atomic.LoadInt32(&count))
	}
}

func TestSendNotification_SkipsWrongTrigger(t *testing.T) {
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&count, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	targets := []NotificationTarget{
		{Type: "webhook", URL: srv.URL, Triggers: []Trigger{TriggerDrainOn}},
	}
	state := &NotifyState{}
	result := newTestResult("SRV01", "Healthy")

	// Send with TriggerDrainOff — target only listens for TriggerDrainOn.
	SendNotification(targets, state, result, TriggerDrainOff, "", nil)

	if atomic.LoadInt32(&count) != 0 {
		t.Errorf("webhook called %d times, want 0 for mismatched trigger", atomic.LoadInt32(&count))
	}
}

func TestSendNotification_RepeatOnce(t *testing.T) {
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&count, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// RepeatMinutes == 0 means fire once only.
	targets := []NotificationTarget{
		{Type: "webhook", URL: srv.URL, Triggers: []Trigger{TriggerAlert}, RepeatMinutes: 0},
	}
	state := &NotifyState{}
	result := newTestResult("SRV01", "Alert")

	SendNotification(targets, state, result, TriggerAlert, "", nil)
	SendNotification(targets, state, result, TriggerAlert, "", nil)

	if atomic.LoadInt32(&count) != 1 {
		t.Errorf("webhook called %d times, want 1 (fire-once)", atomic.LoadInt32(&count))
	}
}

func TestSendNotification_RepeatInterval(t *testing.T) {
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&count, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// RepeatMinutes == 60 means suppress if sent within the last 60 minutes.
	targets := []NotificationTarget{
		{Type: "webhook", URL: srv.URL, Triggers: []Trigger{TriggerAlert}, RepeatMinutes: 60},
	}
	state := &NotifyState{}
	result := newTestResult("SRV01", "Alert")

	// First call fires.
	SendNotification(targets, state, result, TriggerAlert, "", nil)
	// Immediate second call is suppressed (within 60-minute window).
	SendNotification(targets, state, result, TriggerAlert, "", nil)

	if atomic.LoadInt32(&count) != 1 {
		t.Errorf("webhook called %d times, want 1 (repeat suppressed)", atomic.LoadInt32(&count))
	}
}

func TestSendNotification_ResetsOnHealthy(t *testing.T) {
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&count, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	targets := []NotificationTarget{
		{Type: "webhook", URL: srv.URL, Triggers: []Trigger{TriggerAlert, TriggerHealthy}, RepeatMinutes: 0},
	}
	state := &NotifyState{}

	// Fire alert (once-only).
	SendNotification(targets, state, newTestResult("SRV01", "Alert"), TriggerAlert, "", nil)
	// Return to healthy — clears alert tracking.
	SendNotification(targets, state, newTestResult("SRV01", "Healthy"), TriggerHealthy, "", nil)
	// Fire alert again — should fire again since tracking was reset.
	SendNotification(targets, state, newTestResult("SRV01", "Alert"), TriggerAlert, "", nil)

	if atomic.LoadInt32(&count) != 3 {
		t.Errorf("webhook called %d times, want 3 (alert, healthy, alert-after-reset)", atomic.LoadInt32(&count))
	}
}

func TestSendNotification_PayloadFields(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 65536)
		n, _ := r.Body.Read(buf)
		body = buf[:n]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	targets := []NotificationTarget{
		{Type: "webhook", URL: srv.URL, Triggers: []Trigger{TriggerDrainOn}},
	}
	result := newTestResult("SRV-PROD", "Healthy")

	SendNotification(targets, &NotifyState{}, result, TriggerDrainOn, "DOMAIN\\admin", nil)

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("parse webhook payload: %v", err)
	}

	checks := map[string]string{
		"event":      string(TriggerDrainOn),
		"host":       "SRV-PROD",
		"changed_by": "DOMAIN\\admin",
	}
	for key, want := range checks {
		got, ok := payload[key].(string)
		if !ok || got != want {
			t.Errorf("payload[%q] = %v, want %q", key, payload[key], want)
		}
	}
}

func TestSendNotification_EmptyTargets(t *testing.T) {
	// Must not panic.
	SendNotification(nil, &NotifyState{}, newTestResult("SRV01", "Healthy"), TriggerDrainOn, "", nil)
	SendNotification([]NotificationTarget{}, &NotifyState{}, newTestResult("SRV01", "Healthy"), TriggerDrainOn, "", nil)
}

// TestSendNotification_SessionWarningPreservedThroughHealthy verifies that the
// session_warning repeat-tracking entry is NOT cleared when a TriggerHealthy
// fires. This matters because drain mode may turn off while sessions are still
// at high utilisation; we don't want to re-fire the session alert immediately.
func TestSendNotification_SessionWarningPreservedThroughHealthy(t *testing.T) {
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&count, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Target listens for both session_warning and healthy.
	targets := []NotificationTarget{
		{Type: "webhook", URL: srv.URL, Triggers: []Trigger{TriggerSessionWarning, TriggerHealthy}, RepeatMinutes: 0},
	}
	state := &NotifyState{}

	// Fire session_warning (once-only).
	SendNotification(targets, state, newTestResult("SRV01", "Healthy"), TriggerSessionWarning, "", nil)
	if atomic.LoadInt32(&count) != 1 {
		t.Fatalf("expected 1 call after session_warning, got %d", atomic.LoadInt32(&count))
	}

	// Transition to healthy — alert tracking is cleared but session_warning entry must survive.
	SendNotification(targets, state, newTestResult("SRV01", "Healthy"), TriggerHealthy, "", nil)
	if atomic.LoadInt32(&count) != 2 {
		t.Fatalf("expected 2 calls after healthy, got %d", atomic.LoadInt32(&count))
	}

	// Session_warning fires again — would fire if tracking was cleared, should still be suppressed.
	SendNotification(targets, state, newTestResult("SRV01", "Healthy"), TriggerSessionWarning, "", nil)
	if atomic.LoadInt32(&count) != 2 {
		t.Errorf("session_warning fired after healthy transition (tracking was cleared); got %d calls, want 2", atomic.LoadInt32(&count))
	}
}

// TestSendNotification_IndependentTargetState verifies that repeat-tracking
// state is keyed per target URL so two targets with different repeat intervals
// do not interfere with each other.
func TestSendNotification_IndependentTargetState(t *testing.T) {
	var count1, count2 int32

	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&count1, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&count2, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv2.Close()

	targets := []NotificationTarget{
		// srv1: fire once only.
		{Type: "webhook", URL: srv1.URL, Triggers: []Trigger{TriggerAlert}, RepeatMinutes: 0},
		// srv2: repeat every 60 minutes.
		{Type: "webhook", URL: srv2.URL, Triggers: []Trigger{TriggerAlert}, RepeatMinutes: 60},
	}
	state := &NotifyState{}
	result := newTestResult("SRV01", "Alert")

	// First call fires both.
	SendNotification(targets, state, result, TriggerAlert, "", nil)
	if atomic.LoadInt32(&count1) != 1 || atomic.LoadInt32(&count2) != 1 {
		t.Fatalf("expected 1+1 after first call, got %d+%d", count1, count2)
	}

	// Second immediate call: srv1 is suppressed (once-only), srv2 is suppressed (within 60 min).
	SendNotification(targets, state, result, TriggerAlert, "", nil)
	if atomic.LoadInt32(&count1) != 1 || atomic.LoadInt32(&count2) != 1 {
		t.Errorf("expected both suppressed on second call, got count1=%d count2=%d", count1, count2)
	}

	// Manually backdate srv1's last-sent time so it looks like it was cleared (simulate reset).
	// srv2 keeps its own timestamp, independent of srv1.
	delete(state.LastAlertNotify, srv1.URL)

	// Third call: srv1 fires again (tracking deleted), srv2 still suppressed.
	SendNotification(targets, state, result, TriggerAlert, "", nil)
	if atomic.LoadInt32(&count1) != 2 {
		t.Errorf("srv1 should fire after tracking reset, got count1=%d", count1)
	}
	if atomic.LoadInt32(&count2) != 1 {
		t.Errorf("srv2 should remain suppressed independently, got count2=%d", count2)
	}
}

// ── sendNtfy ──────────────────────────────────────────────────────────────────

func TestSendNtfy_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := sendNtfy(srv.URL, "DrainCtl: Alert on SRV01", "Drain active.", "high", "warning"); err != nil {
		t.Fatalf("sendNtfy error: %v", err)
	}
}

func TestSendNtfy_NonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := sendNtfy(srv.URL, "title", "msg", "default", "white_check_mark")
	if err == nil {
		t.Error("expected error for 500 response, got nil")
	}
}

func TestSendNtfy_SetsHeaders(t *testing.T) {
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := sendNtfy(srv.URL, "My Title", "My Message", "high", "warning"); err != nil {
		t.Fatalf("sendNtfy error: %v", err)
	}

	checks := map[string]string{
		"Title":    "My Title",
		"Priority": "high",
		"Tags":     "warning",
	}
	for header, want := range checks {
		if got := captured.Header.Get(header); got != want {
			t.Errorf("header %s = %q, want %q", header, got, want)
		}
	}
	if ua := captured.Header.Get("User-Agent"); !strings.HasPrefix(ua, "DrainCtl/") {
		t.Errorf("User-Agent = %q, expected DrainCtl/ prefix", ua)
	}
}

// TestSendNotification_CallsNtfy verifies that ntfy-type targets are dispatched.
func TestSendNotification_CallsNtfy(t *testing.T) {
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&count, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	targets := []NotificationTarget{
		{Type: "ntfy", URL: srv.URL, Triggers: []Trigger{TriggerAlert}},
	}
	state := &NotifyState{}
	result := newTestResult("SRV01", "Alert")

	SendNotification(targets, state, result, TriggerAlert, "", nil)

	if atomic.LoadInt32(&count) != 1 {
		t.Errorf("ntfy endpoint called %d times, want 1", atomic.LoadInt32(&count))
	}
}

// TestSendNotification_WebhookPayloadIncludesSessions verifies that session data is
// included in the webhook payload when the result contains a SessionSummary.
func TestSendNotification_WebhookPayloadIncludesSessions(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 65536)
		n, _ := r.Body.Read(buf)
		body = buf[:n]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	targets := []NotificationTarget{
		{Type: "webhook", URL: srv.URL, Triggers: []Trigger{TriggerSessionWarning}},
	}
	result := newTestResult("SRV01", "Healthy")
	result.Sessions = &SessionSummary{
		ActiveSessions:       8,
		DisconnectedSessions: 2,
		TotalSessions:        10,
		MaxSessions:          12,
		UtilizationPct:       83,
	}

	SendNotification(targets, &NotifyState{}, result, TriggerSessionWarning, "", nil)

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	sessRaw, ok := payload["sessions"]
	if !ok {
		t.Fatal("payload missing 'sessions' field")
	}
	sessMap, ok := sessRaw.(map[string]any)
	if !ok {
		t.Fatalf("payload['sessions'] is %T, want map", sessRaw)
	}
	if got := sessMap["total_sessions"]; got != float64(10) {
		t.Errorf("sessions.total_sessions = %v, want 10", got)
	}
	if got := sessMap["utilization_pct"]; got != float64(83) {
		t.Errorf("sessions.utilization_pct = %v, want 83", got)
	}
}

// TestSendNotification_WebhookPayloadOmitsSessionsWhenNil verifies that when there
// is no session data, the 'sessions' field is absent from the webhook payload.
func TestSendNotification_WebhookPayloadOmitsSessionsWhenNil(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 65536)
		n, _ := r.Body.Read(buf)
		body = buf[:n]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	targets := []NotificationTarget{
		{Type: "webhook", URL: srv.URL, Triggers: []Trigger{TriggerDrainOn}},
	}
	result := newTestResult("SRV01", "Healthy")
	// result.Sessions is nil — no session data available.

	SendNotification(targets, &NotifyState{}, result, TriggerDrainOn, "", nil)

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if _, ok := payload["sessions"]; ok {
		t.Error("payload should not contain 'sessions' when result.Sessions is nil")
	}
}

// ── SendTestNotification ──────────────────────────────────────────────────────

func TestSendTestNotification_NoTargets_ReturnsError(t *testing.T) {
	err := SendTestNotification(nil, nil)
	if err == nil {
		t.Error("expected error for nil targets, got nil")
	}
	err2 := SendTestNotification([]NotificationTarget{}, nil)
	if err2 == nil {
		t.Error("expected error for empty targets, got nil")
	}
}

func TestSendTestNotification_EmptyURLTargets_ReturnsError(t *testing.T) {
	// Targets with empty URLs count as "no targets".
	targets := []NotificationTarget{
		{Type: "webhook", URL: ""},
	}
	if err := SendTestNotification(targets, nil); err == nil {
		t.Error("expected error when all target URLs are empty, got nil")
	}
}

func TestSendTestNotification_WebhookSuccess(t *testing.T) {
	var count int32
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&count, 1)
		buf := make([]byte, 65536)
		n, _ := r.Body.Read(buf)
		capturedBody = buf[:n]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	targets := []NotificationTarget{
		{Type: "webhook", URL: srv.URL, Triggers: DefaultTriggers},
	}
	if err := SendTestNotification(targets, nil); err != nil {
		t.Fatalf("SendTestNotification error: %v", err)
	}
	if atomic.LoadInt32(&count) != 1 {
		t.Errorf("webhook called %d times, want 1", count)
	}
	var payload map[string]any
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	if got := payload["event"]; got != "test" {
		t.Errorf("event = %v, want 'test'", got)
	}
}

func TestSendTestNotification_WebhookError_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	targets := []NotificationTarget{
		{Type: "webhook", URL: srv.URL, Triggers: DefaultTriggers},
	}
	if err := SendTestNotification(targets, nil); err == nil {
		t.Error("expected error for webhook 500 response, got nil")
	}
}

func TestSendTestNotification_NtfySuccess(t *testing.T) {
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&count, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	targets := []NotificationTarget{
		{Type: "ntfy", URL: srv.URL, Triggers: DefaultTriggers},
	}
	if err := SendTestNotification(targets, nil); err != nil {
		t.Fatalf("SendTestNotification error: %v", err)
	}
	if atomic.LoadInt32(&count) != 1 {
		t.Errorf("ntfy endpoint called %d times, want 1", count)
	}
}

func TestSendTestNotification_MultipleTargets_CallsAll(t *testing.T) {
	var count1, count2 int32
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&count1, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv1.Close()
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&count2, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv2.Close()

	targets := []NotificationTarget{
		{Type: "webhook", URL: srv1.URL},
		{Type: "ntfy", URL: srv2.URL},
	}
	if err := SendTestNotification(targets, nil); err != nil {
		t.Fatalf("SendTestNotification error: %v", err)
	}
	if atomic.LoadInt32(&count1) != 1 || atomic.LoadInt32(&count2) != 1 {
		t.Errorf("calls: srv1=%d srv2=%d, want 1+1", count1, count2)
	}
}

// TestSendNotification_WebhookPayloadContextFields verifies that
// grace_period_seconds, connections_allowed, and version are always present in
// the webhook payload so consumers can derive the alerting threshold and whether
// new connections are currently being blocked without parsing status strings.
func TestSendNotification_WebhookPayloadContextFields(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 65536)
		n, _ := r.Body.Read(buf)
		body = buf[:n]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	targets := []NotificationTarget{
		{Type: "webhook", URL: srv.URL, Triggers: []Trigger{TriggerAlert}},
	}
	connAllowed := false
	result := &CheckResult{
		Host:                 "SRV-PROD",
		Status:               "Alert",
		DrainModeLabel:       "DrainAllSessions",
		Message:              "Drain active.",
		ConnectionsAllowed:   &connAllowed,
		GracePeriodSeconds:   3600,
		Version:              "26.95.0",
		StateDurationSeconds: func() *float64 { v := 0.0; return &v }(),
		Timestamp:            time.Now(),
	}

	SendNotification(targets, &NotifyState{}, result, TriggerAlert, "", nil)

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if got, ok := payload["grace_period_seconds"]; !ok {
		t.Error("payload missing 'grace_period_seconds'")
	} else if got != float64(3600) {
		t.Errorf("grace_period_seconds = %v, want 3600", got)
	}

	if got, ok := payload["connections_allowed"]; !ok {
		t.Error("payload missing 'connections_allowed'")
	} else if got != false {
		t.Errorf("connections_allowed = %v, want false (Alert state)", got)
	}

	if got, ok := payload["version"]; !ok {
		t.Error("payload missing 'version'")
	} else if got != "26.95.0" {
		t.Errorf("version = %v, want 26.95.0", got)
	}
}

// TestSendNotification_ConnectionsAllowedTrueWhenHealthy verifies that
// connections_allowed is true when the result is Healthy (nil ConnectionsAllowed
// defaults to false defensively).
func TestSendNotification_ConnectionsAllowedTrueWhenHealthy(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 65536)
		n, _ := r.Body.Read(buf)
		body = buf[:n]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	targets := []NotificationTarget{
		{Type: "webhook", URL: srv.URL, Triggers: []Trigger{TriggerDrainOff}},
	}
	result := newTestResult("SRV01", "Healthy") // ConnectionsAllowed = true

	SendNotification(targets, &NotifyState{}, result, TriggerDrainOff, "", nil)

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got, ok := payload["connections_allowed"]; !ok {
		t.Error("payload missing 'connections_allowed'")
	} else if got != true {
		t.Errorf("connections_allowed = %v, want true (Healthy state)", got)
	}
}

// TestSendNotification_NtfySessionWarningMessage verifies that the ntfy body for
// session_warning includes the utilization percentage and session counts rather
// than the generic status message.
func TestSendNotification_NtfySessionWarningMessage(t *testing.T) {
	var capturedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		capturedBody = string(buf[:n])
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	targets := []NotificationTarget{
		{Type: "ntfy", URL: srv.URL, Triggers: []Trigger{TriggerSessionWarning}},
	}
	result := newTestResult("SRV01", "Healthy")
	result.Sessions = &SessionSummary{
		ActiveSessions: 9,
		TotalSessions:  9,
		MaxSessions:    10,
		UtilizationPct: 90,
	}

	SendNotification(targets, &NotifyState{}, result, TriggerSessionWarning, "", nil)

	if !strings.Contains(capturedBody, "90%") {
		t.Errorf("ntfy body %q should contain utilization percentage", capturedBody)
	}
	if !strings.Contains(capturedBody, "9/10") {
		t.Errorf("ntfy body %q should contain session counts", capturedBody)
	}
}
