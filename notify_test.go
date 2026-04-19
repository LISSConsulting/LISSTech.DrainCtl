//go:build windows

package drainctl

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

	SendNotification(targets, state, result, TriggerDrainOn, "")

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
	SendNotification(targets, state, result, TriggerDrainOff, "")

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

	SendNotification(targets, state, result, TriggerAlert, "")
	SendNotification(targets, state, result, TriggerAlert, "")

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
	SendNotification(targets, state, result, TriggerAlert, "")
	// Immediate second call is suppressed (within 60-minute window).
	SendNotification(targets, state, result, TriggerAlert, "")

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
	SendNotification(targets, state, newTestResult("SRV01", "Alert"), TriggerAlert, "")
	// Return to healthy — clears alert tracking.
	SendNotification(targets, state, newTestResult("SRV01", "Healthy"), TriggerHealthy, "")
	// Fire alert again — should fire again since tracking was reset.
	SendNotification(targets, state, newTestResult("SRV01", "Alert"), TriggerAlert, "")

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

	SendNotification(targets, &NotifyState{}, result, TriggerDrainOn, "DOMAIN\\admin")

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
	SendNotification(nil, &NotifyState{}, newTestResult("SRV01", "Healthy"), TriggerDrainOn, "")
	SendNotification([]NotificationTarget{}, &NotifyState{}, newTestResult("SRV01", "Healthy"), TriggerDrainOn, "")
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
	SendNotification(targets, state, newTestResult("SRV01", "Healthy"), TriggerSessionWarning, "")
	if atomic.LoadInt32(&count) != 1 {
		t.Fatalf("expected 1 call after session_warning, got %d", atomic.LoadInt32(&count))
	}

	// Transition to healthy — alert tracking is cleared but session_warning entry must survive.
	SendNotification(targets, state, newTestResult("SRV01", "Healthy"), TriggerHealthy, "")
	if atomic.LoadInt32(&count) != 2 {
		t.Fatalf("expected 2 calls after healthy, got %d", atomic.LoadInt32(&count))
	}

	// Session_warning fires again — would fire if tracking was cleared, should still be suppressed.
	SendNotification(targets, state, newTestResult("SRV01", "Healthy"), TriggerSessionWarning, "")
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
	SendNotification(targets, state, result, TriggerAlert, "")
	if atomic.LoadInt32(&count1) != 1 || atomic.LoadInt32(&count2) != 1 {
		t.Fatalf("expected 1+1 after first call, got %d+%d", count1, count2)
	}

	// Second immediate call: srv1 is suppressed (once-only), srv2 is suppressed (within 60 min).
	SendNotification(targets, state, result, TriggerAlert, "")
	if atomic.LoadInt32(&count1) != 1 || atomic.LoadInt32(&count2) != 1 {
		t.Errorf("expected both suppressed on second call, got count1=%d count2=%d", count1, count2)
	}

	// Manually backdate srv1's last-sent time so it looks like it was cleared (simulate reset).
	// srv2 keeps its own timestamp, independent of srv1.
	delete(state.LastAlertNotify, srv1.URL)

	// Third call: srv1 fires again (tracking deleted), srv2 still suppressed.
	SendNotification(targets, state, result, TriggerAlert, "")
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

	if err := sendNtfy(srv.URL, "DrainCtl: Alert on SRV01", "Drain active.", "high", ""); err != nil {
		t.Fatalf("sendNtfy error: %v", err)
	}
}

func TestSendNtfy_NonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := sendNtfy(srv.URL, "title", "msg", "default", "")
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

	if err := sendNtfy(srv.URL, "My Title", "My Message", "high", ""); err != nil {
		t.Fatalf("sendNtfy error: %v", err)
	}

	checks := map[string]string{
		"Title":    "My Title",
		"Priority": "high",
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

	SendNotification(targets, state, result, TriggerAlert, "")

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

	SendNotification(targets, &NotifyState{}, result, TriggerSessionWarning, "")

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

	SendNotification(targets, &NotifyState{}, result, TriggerDrainOn, "")

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
	_, err := SendTestNotification(nil)
	if err == nil {
		t.Error("expected error for nil targets, got nil")
	}
	_, err2 := SendTestNotification([]NotificationTarget{})
	if err2 == nil {
		t.Error("expected error for empty targets, got nil")
	}
}

func TestSendTestNotification_EmptyURLTargets_ReturnsError(t *testing.T) {
	// Targets with empty URLs count as "no targets".
	targets := []NotificationTarget{
		{Type: "webhook", URL: ""},
	}
	if _, err := SendTestNotification(targets); err == nil {
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
	if _, err := SendTestNotification(targets); err != nil {
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
	if _, err := SendTestNotification(targets); err == nil {
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
	if _, err := SendTestNotification(targets); err != nil {
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
	if _, err := SendTestNotification(targets); err != nil {
		t.Fatalf("SendTestNotification error: %v", err)
	}
	if atomic.LoadInt32(&count1) != 1 || atomic.LoadInt32(&count2) != 1 {
		t.Errorf("calls: srv1=%d srv2=%d, want 1+1", count1, count2)
	}
}

func TestSendTestNotification_MultipleErrors_ReturnsAll(t *testing.T) {
	// Both targets return 500 — both errors must be present in the returned error.
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv1.Close()
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv2.Close()

	targets := []NotificationTarget{
		{Type: "webhook", URL: srv1.URL},
		{Type: "webhook", URL: srv2.URL},
	}
	_, err := SendTestNotification(targets)
	if err == nil {
		t.Fatal("expected error when both targets fail, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, srv1.URL) {
		t.Errorf("error message missing first target URL (%s): %s", srv1.URL, msg)
	}
	if !strings.Contains(msg, srv2.URL) {
		t.Errorf("error message missing second target URL (%s): %s", srv2.URL, msg)
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

	SendNotification(targets, &NotifyState{}, result, TriggerAlert, "")

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

	SendNotification(targets, &NotifyState{}, result, TriggerDrainOff, "")

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

	SendNotification(targets, &NotifyState{}, result, TriggerSessionWarning, "")

	if !strings.Contains(capturedBody, "90%") {
		t.Errorf("ntfy body %q should contain utilization percentage", capturedBody)
	}
	if !strings.Contains(capturedBody, "9/10") {
		t.Errorf("ntfy body %q should contain session counts", capturedBody)
	}
}

// ntfyTitle is now an internal implementation detail; its behaviour is verified
// indirectly via TestSendNotification_NtfyTitleIsReadable.

// TestSendNotification_NtfyTitleIsReadable verifies that the Title header sent
// to an ntfy endpoint contains a human-readable label rather than a raw
// trigger name like "drain_on".
func TestSendNotification_NtfyTitleIsReadable(t *testing.T) {
	cases := []struct {
		trigger     Trigger
		status      string
		wantInTitle string
	}{
		{TriggerDrainOn, "Grace", "remote connections disabled"},
		{TriggerDrainOff, "Healthy", "re-enabled"},
		{TriggerGraceEntered, "Grace", "grace period"},
		{TriggerAlert, "Alert", "exceeds"},
		{TriggerHealthy, "Healthy", "healthy"},
	}
	for _, tc := range cases {
		t.Run(string(tc.trigger), func(t *testing.T) {
			var capturedTitle string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedTitle = r.Header.Get("Title")
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			targets := []NotificationTarget{
				{Type: "ntfy", URL: srv.URL, Triggers: []Trigger{tc.trigger}},
			}
			result := newTestResult("SRV01", tc.status)
			SendNotification(targets, &NotifyState{}, result, tc.trigger, "")

			if !strings.Contains(strings.ToLower(capturedTitle), strings.ToLower(tc.wantInTitle)) {
				t.Errorf("Title = %q, want it to contain %q", capturedTitle, tc.wantInTitle)
			}
			// Must contain the host name.
			if !strings.Contains(capturedTitle, "SRV01") {
				t.Errorf("Title = %q, should contain host name", capturedTitle)
			}
		})
	}
}

// TestSendNotification_ResetsOnDrainOff verifies that drain_off clears
// LastAlertNotify the same way TriggerHealthy does, allowing the alert to
// re-fire immediately on the next poll cycle.
func TestSendNotification_ResetsOnDrainOff(t *testing.T) {
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&count, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	targets := []NotificationTarget{
		{Type: "webhook", URL: srv.URL, Triggers: []Trigger{TriggerAlert, TriggerDrainOff}, RepeatMinutes: 0},
	}
	state := &NotifyState{}

	// Fire alert (once-only).
	SendNotification(targets, state, newTestResult("SRV01", "Alert"), TriggerAlert, "")
	// Drain off — clears alert tracking.
	SendNotification(targets, state, newTestResult("SRV01", "Healthy"), TriggerDrainOff, "")
	// Fire alert again — should fire again since tracking was reset.
	SendNotification(targets, state, newTestResult("SRV01", "Alert"), TriggerAlert, "")

	if atomic.LoadInt32(&count) != 3 {
		t.Errorf("webhook called %d times, want 3 (alert, drain_off, alert-after-reset)", atomic.LoadInt32(&count))
	}
}

// TestSendTestNotification_UnknownTypeSkipped verifies that a target with an
// unrecognised type is silently skipped — no error, no panic — when the URL is
// non-empty. This is an intentional no-op to allow forward-compatibility.
func TestSendTestNotification_UnknownTypeSkipped(t *testing.T) {
	targets := []NotificationTarget{
		{Type: "webhook", URL: "http://any", Triggers: DefaultTriggers}, // anchor so hasTargets is true
		{Type: "future-backend", URL: "http://example.com/future", Triggers: DefaultTriggers},
	}

	// Replace the real HTTP call with a server that accepts all requests so
	// we only care about whether the unknown type causes an error, not the
	// network result.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	targets[0].URL = srv.URL

	if _, err := SendTestNotification(targets); err != nil {
		t.Errorf("SendTestNotification with unknown type returned error: %v", err)
	}
}

// TestSendTestNotification_WebhookPayloadSchemaComplete verifies that the test
// notification payload contains the same top-level fields as a real notification
// so webhook consumers can validate against a consistent schema.
func TestSendTestNotification_WebhookPayloadSchemaComplete(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 65536)
		n, _ := r.Body.Read(buf)
		body = buf[:n]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	targets := []NotificationTarget{
		{Type: "webhook", URL: srv.URL, Triggers: DefaultTriggers},
	}
	if _, err := SendTestNotification(targets); err != nil {
		t.Fatalf("SendTestNotification error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("parse payload: %v", err)
	}

	// Fields required in every real notification payload.
	requiredFields := []string{
		"event", "host", "drain_mode", "status", "message",
		"changed_by", "state_duration_seconds",
		"grace_period_seconds", "connections_allowed", "version", "timestamp",
	}
	for _, field := range requiredFields {
		if _, ok := payload[field]; !ok {
			t.Errorf("test payload missing required field %q", field)
		}
	}

	if got := payload["event"]; got != "test" {
		t.Errorf("event = %v, want 'test'", got)
	}
	if got := payload["connections_allowed"]; got != true {
		t.Errorf("connections_allowed = %v, want true (test is not a drain state)", got)
	}
	if got, ok := payload["version"].(string); !ok || got == "" {
		t.Errorf("version = %v, want non-empty string", payload["version"])
	}
	if got := payload["grace_period_seconds"]; got != float64(0) {
		t.Errorf("grace_period_seconds = %v, want 0", got)
	}
}

// TestSendNotification_PreviousModeInPayload verifies that the webhook payload
// includes a "previous_mode" field when the CheckResult carries a transition
// (result.Transition = true, result.TransitionFrom non-empty).
func TestSendNotification_PreviousModeInPayload(t *testing.T) {
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
	connAllowed := false
	result := &CheckResult{
		Host:               "SRV-PROD",
		Status:             "Grace",
		DrainModeLabel:     "DrainAllSessions",
		Message:            "Drain mode active.",
		ConnectionsAllowed: &connAllowed,
		Transition:         true,
		TransitionFrom:     "AllowAll",
		Timestamp:          time.Now(),
	}

	SendNotification(targets, &NotifyState{}, result, TriggerDrainOn, "DOMAIN\\admin")

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	if got, ok := payload["previous_mode"]; !ok {
		t.Error("payload missing 'previous_mode' field when Transition=true")
	} else if got != "AllowAll" {
		t.Errorf("previous_mode = %v, want AllowAll", got)
	}
}

// TestSendNotification_NoPreviousModeWhenNotTransition verifies that
// "previous_mode" is absent from the payload when result.Transition is false.
func TestSendNotification_NoPreviousModeWhenNotTransition(t *testing.T) {
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
		Host:               "SRV-PROD",
		Status:             "Alert",
		DrainModeLabel:     "DrainAllSessions",
		Message:            "Drain mode active.",
		ConnectionsAllowed: &connAllowed,
		Transition:         false, // not a transition
		Timestamp:          time.Now(),
	}

	SendNotification(targets, &NotifyState{}, result, TriggerAlert, "")

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	if _, ok := payload["previous_mode"]; ok {
		t.Error("payload must not include 'previous_mode' when Transition=false")
	}
}

// TestSendWebhook_InvalidURL_ReturnsError verifies that sendWebhook returns an
// error when the target URL is not a valid HTTP URL.
func TestSendWebhook_InvalidURL_ReturnsError(t *testing.T) {
	err := sendWebhook("\x00invalid-url", "", map[string]any{"event": "test"})
	if err == nil {
		t.Error("expected error for invalid URL, got nil")
	}
}

// TestSendNtfy_InvalidURL_ReturnsError verifies that sendNtfy returns an error
// when the target URL is not valid.
func TestSendNtfy_InvalidURL_ReturnsError(t *testing.T) {
	err := sendNtfy("\x00invalid-url", "DrainCtl Test", "msg", "default", "")
	if err == nil {
		t.Error("expected error for invalid URL, got nil")
	}
}

// TestSendWebhook_MarshalError_ReturnsError verifies that sendWebhook returns
// an error when the payload contains an unmarshalable value (e.g. NaN float).
// json.Marshal rejects IEEE 754 NaN/Inf because JSON has no representation for
// them.
func TestSendWebhook_MarshalError_ReturnsError(t *testing.T) {
	payload := map[string]any{
		"event": math.NaN(), // json.Marshal returns UnsupportedValueError for NaN
	}
	err := sendWebhook("http://example.com/hook", "", payload)
	if err == nil {
		t.Fatal("expected error for NaN payload, got nil")
	}
	if !strings.Contains(err.Error(), "marshal payload") {
		t.Errorf("error = %q, want prefix 'marshal payload'", err)
	}
}

// TestSendTestNotification_NtfyError_ReturnsError verifies that
// SendTestNotification returns an error when the ntfy server responds with
// a non-2xx status.
func TestSendTestNotification_NtfyError_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	targets := []NotificationTarget{
		{Type: "ntfy", URL: srv.URL},
	}
	_, err := SendTestNotification(targets)
	if err == nil {
		t.Error("expected error when ntfy returns 503, got nil")
	}
	if !strings.Contains(err.Error(), srv.URL) {
		t.Errorf("error message missing target URL (%s): %s", srv.URL, err)
	}
}

// TestSendNotification_WebhookErrorIsLogged verifies that when sendWebhook
// returns an error (non-2xx status), SendNotification does not panic and
// does not propagate the error to the caller (fire-and-forget semantics).
// Errors are now logged internally via slog.
func TestSendNotification_WebhookErrorIsLogged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	targets := []NotificationTarget{
		{Type: "webhook", URL: srv.URL, Triggers: []Trigger{TriggerDrainOn}},
	}
	state := &NotifyState{}
	result := newTestResult("HOST", "Healthy")
	// Must not panic; error is logged via slog internally.
	SendNotification(targets, state, result, TriggerDrainOn, "")
}

// TestSendNotification_NtfyErrorIsLogged verifies that when sendNtfy returns
// an error (non-2xx status), SendNotification does not panic and does not
// propagate the error to the caller. Errors are logged internally via slog.
func TestSendNotification_NtfyErrorIsLogged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	targets := []NotificationTarget{
		{Type: "ntfy", URL: srv.URL, Triggers: []Trigger{TriggerDrainOn}},
	}
	state := &NotifyState{}
	result := newTestResult("HOST", "Healthy")
	// Must not panic; error is logged via slog internally.
	SendNotification(targets, state, result, TriggerDrainOn, "")
}

// TestSendTestNotification_SkipsEmptyURLTarget verifies that targets with an
// empty URL are skipped via the continue branch, while targets with a real URL
// are still dispatched.
func TestSendTestNotification_SkipsEmptyURLTarget(t *testing.T) {
	var called int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&called, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// First target has empty URL (should be skipped); second has a real URL.
	targets := []NotificationTarget{
		{Type: "webhook", URL: ""},
		{Type: "webhook", URL: srv.URL},
	}
	_, err := SendTestNotification(targets)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if atomic.LoadInt32(&called) != 1 {
		t.Errorf("expected 1 webhook call, got %d", called)
	}
}

// TestSendWebhook_DoFails_ReturnsError verifies that sendWebhook returns an
// error wrapping "http post" when the underlying HTTP Do call fails (e.g.
// because the server has been closed and the connection is refused).
func TestSendWebhook_DoFails_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close() // close before use — Do will get connection refused

	err := sendWebhook(url, "", map[string]any{"event": "test"})
	if err == nil {
		t.Fatal("expected error when connection refused, got nil")
	}
	if !strings.Contains(err.Error(), "http post") {
		t.Errorf("error = %q, want prefix 'http post'", err)
	}
}

// TestSendNtfy_DoFails_ReturnsError verifies that sendNtfy returns an error
// wrapping "http post" when the HTTP Do call fails.
func TestSendNtfy_DoFails_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close() // close before use — Do will get connection refused

	err := sendNtfy(url, "Test", "msg", "default", "")
	if err == nil {
		t.Fatal("expected error when connection refused, got nil")
	}
	if !strings.Contains(err.Error(), "http post") {
		t.Errorf("error = %q, want prefix 'http post'", err)
	}
}

// ── event_spike contract tests ───────────────────────────────────────────────

// newSpikeResult constructs a CheckResult with a populated Spike payload and
// the deterministic timestamps the contract examples use, so contract tests
// can compare exact wire shape without hitting time.Now() drift.
func newSpikeResult() (*CheckResult, *SpikePayload) {
	winStart := time.Date(2026, 4, 16, 14, 22, 42, 0, time.UTC)
	winEnd := winStart.Add(10 * time.Second)
	firstSeen := winStart.Add(-10 * time.Second)
	connAllowed := true
	spike := &SpikePayload{
		Host:              "RDSH-04",
		Channel:           "Microsoft-Windows-Winlogon/Operational",
		WindowStart:       winStart,
		WindowEnd:         winEnd,
		Observed:          47,
		Expected:          3.2,
		TailProbability:   2.1e-7,
		ConfirmationCount: 3,
		FirstSeenAt:       firstSeen,
	}
	res := &CheckResult{
		Host:               spike.Host,
		Status:             "alert",
		DrainModeLabel:     "AllowAll",
		ConnectionsAllowed: &connAllowed,
		Version:            "26.106.12",
		Timestamp:          winEnd,
		Message:            "Event count 47 in 10s bucket; expected ~3.2 at this time-of-day; tail probability 2.1e-7.",
		Spike:              spike,
	}
	return res, spike
}

// TestEventSpikePayload_Shape verifies the full contract shape of an
// event_spike webhook body: top-level envelope fields plus the spike
// sub-object schema from contracts/event_spike-payload.md.
func TestEventSpikePayload_Shape(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		capturedBody = body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	targets := []NotificationTarget{
		{Type: "webhook", URL: srv.URL, Triggers: []Trigger{TriggerEventSpike}},
	}
	result, spike := newSpikeResult()

	SendNotification(targets, &NotifyState{}, result, TriggerEventSpike, "")

	if len(capturedBody) == 0 {
		t.Fatal("webhook captured no body — SendNotification did not dispatch event_spike")
	}
	var payload map[string]any
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v\nbody: %s", err, capturedBody)
	}

	envelope := map[string]any{
		"event":   string(TriggerEventSpike),
		"host":    spike.Host,
		"version": "26.106.12",
		"status":  "alert",
		"message": result.Message,
	}
	for key, want := range envelope {
		got, ok := payload[key]
		if !ok {
			t.Errorf("payload missing top-level field %q", key)
			continue
		}
		if got != want {
			t.Errorf("payload[%q] = %v, want %v", key, got, want)
		}
	}
	if _, ok := payload["timestamp"].(string); !ok {
		t.Errorf("payload missing string field 'timestamp'; got %T", payload["timestamp"])
	}
	if _, ok := payload["subject"].(string); !ok {
		t.Errorf("payload missing string field 'subject'; got %T", payload["subject"])
	}

	subRaw, ok := payload["spike"]
	if !ok {
		t.Fatal("payload missing 'spike' sub-object")
	}
	sub, ok := subRaw.(map[string]any)
	if !ok {
		t.Fatalf("'spike' is %T, want map[string]any", subRaw)
	}
	if got, _ := sub["channel"].(string); got != spike.Channel {
		t.Errorf("spike.channel = %v, want %q", sub["channel"], spike.Channel)
	}
	if got, _ := sub["window_start"].(string); got != spike.WindowStart.Format(time.RFC3339) {
		t.Errorf("spike.window_start = %v, want %q", sub["window_start"], spike.WindowStart.Format(time.RFC3339))
	}
	if got, _ := sub["window_end"].(string); got != spike.WindowEnd.Format(time.RFC3339) {
		t.Errorf("spike.window_end = %v, want %q", sub["window_end"], spike.WindowEnd.Format(time.RFC3339))
	}
	if got, _ := sub["observed"].(float64); int(got) != spike.Observed {
		t.Errorf("spike.observed = %v, want %d", sub["observed"], spike.Observed)
	}
	if got, _ := sub["expected"].(float64); got != spike.Expected {
		t.Errorf("spike.expected = %v, want %v", sub["expected"], spike.Expected)
	}
	if got, _ := sub["tail_probability"].(float64); got != spike.TailProbability {
		t.Errorf("spike.tail_probability = %v, want %v", sub["tail_probability"], spike.TailProbability)
	}
	if got, _ := sub["confirmation_count"].(float64); int(got) != spike.ConfirmationCount {
		t.Errorf("spike.confirmation_count = %v, want %d", sub["confirmation_count"], spike.ConfirmationCount)
	}
	if got, _ := sub["first_seen_at"].(string); got != spike.FirstSeenAt.Format(time.RFC3339) {
		t.Errorf("spike.first_seen_at = %v, want %q", sub["first_seen_at"], spike.FirstSeenAt.Format(time.RFC3339))
	}
}

// TestEventSpikePayload_HMAC verifies that an event_spike webhook body is
// signed with HMAC-SHA256 over the raw request body when a secret is
// configured on the target.
func TestEventSpikePayload_HMAC(t *testing.T) {
	const secret = "spike-test-secret"
	var capturedBody []byte
	var capturedSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		capturedBody = body
		capturedSig = r.Header.Get("X-DrainCtl-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	targets := []NotificationTarget{
		{Type: "webhook", URL: srv.URL, Secret: secret, Triggers: []Trigger{TriggerEventSpike}},
	}
	result, _ := newSpikeResult()

	SendNotification(targets, &NotifyState{}, result, TriggerEventSpike, "")

	if len(capturedBody) == 0 {
		t.Fatal("webhook captured no body — SendNotification did not dispatch event_spike")
	}
	// The body MUST contain the spike sub-object: HMAC over a body without
	// it would be the wrong contract.
	if !strings.Contains(string(capturedBody), `"spike"`) {
		t.Fatalf("webhook body missing 'spike' sub-object:\n%s", capturedBody)
	}
	wantSig := webhookSignature(secret, capturedBody)
	if capturedSig != wantSig {
		t.Errorf("X-DrainCtl-Signature = %q, want %q", capturedSig, wantSig)
	}
}

// TestEventSpikePayload_NtfyPriority verifies the contract from
// contracts/event_spike-payload.md: ntfy Priority is derived from the spike
// severity (3/default for warning, 4/high for alert — ntfy accepts either the
// numeric or named form) and the Tags header carries ["evtspike", host,
// channel-basename] so operators can filter their ntfy feeds by host and
// channel without parsing the message body.
func TestEventSpikePayload_NtfyPriority(t *testing.T) {
	cases := []struct {
		name     string
		status   string
		priority []string // ntfy accepts either the numeric or named encoding
	}{
		{"warning maps to priority 3/default", "warning", []string{"3", "default"}},
		{"alert maps to priority 4/high", "alert", []string{"4", "high"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var captured http.Header
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured = r.Header.Clone()
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			result, spike := newSpikeResult()
			result.Status = tc.status

			targets := []NotificationTarget{
				{Type: "ntfy", URL: srv.URL, Triggers: []Trigger{TriggerEventSpike}},
			}
			SendNotification(targets, &NotifyState{}, result, TriggerEventSpike, "")

			if captured == nil {
				t.Fatal("ntfy server received no request — SendNotification did not dispatch event_spike")
			}

			gotPriority := captured.Get("Priority")
			matched := false
			for _, p := range tc.priority {
				if gotPriority == p {
					matched = true
					break
				}
			}
			if !matched {
				t.Errorf("Priority = %q, want one of %v for status %q", gotPriority, tc.priority, tc.status)
			}

			gotTags := captured.Get("Tags")
			// ntfy's Tags header is a comma-separated list; basename of a
			// "Provider/Channel" path is the portion after the final slash.
			channelBase := spike.Channel
			if idx := strings.LastIndex(channelBase, "/"); idx >= 0 {
				channelBase = channelBase[idx+1:]
			}
			wantTagParts := []string{"evtspike", spike.Host, channelBase}
			for _, tag := range wantTagParts {
				if !strings.Contains(gotTags, tag) {
					t.Errorf("Tags header %q missing %q", gotTags, tag)
				}
			}
		})
	}
}

// TestEventSpikePayload_PerTargetSeverity proves FR-011a: severity for an
// event_spike notification is wired on the target, not derived from the
// detector. Two webhook targets on the same host/channel receive the same
// spike payload but each with its own configured severity.
func TestEventSpikePayload_PerTargetSeverity(t *testing.T) {
	type capture struct {
		mu     sync.Mutex
		bodies [][]byte
	}
	newCapturer := func() (*httptest.Server, *capture) {
		cap := &capture{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			cap.mu.Lock()
			cap.bodies = append(cap.bodies, body)
			cap.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		}))
		return srv, cap
	}

	warnSrv, warnCap := newCapturer()
	defer warnSrv.Close()
	alertSrv, alertCap := newCapturer()
	defer alertSrv.Close()

	targets := []NotificationTarget{
		{Type: "webhook", URL: warnSrv.URL, Severity: "warning", Triggers: []Trigger{TriggerEventSpike}},
		{Type: "webhook", URL: alertSrv.URL, Severity: "alert", Triggers: []Trigger{TriggerEventSpike}},
	}
	result, _ := newSpikeResult()
	result.Status = "" // detector does not assign severity — comes from target wiring

	SendNotification(targets, &NotifyState{}, result, TriggerEventSpike, "")

	check := func(label string, cap *capture, want string) {
		cap.mu.Lock()
		defer cap.mu.Unlock()
		if len(cap.bodies) != 1 {
			t.Fatalf("%s target: got %d bodies, want 1", label, len(cap.bodies))
		}
		var p map[string]any
		if err := json.Unmarshal(cap.bodies[0], &p); err != nil {
			t.Fatalf("%s target: unmarshal: %v", label, err)
		}
		if got := p["status"]; got != want {
			t.Errorf("%s target: payload.status = %v, want %q", label, got, want)
		}
		subj, _ := p["subject"].(string)
		if want == "alert" && !strings.Contains(subj, "\U0001F6A8") {
			t.Errorf("%s target: subject %q missing alert emoji", label, subj)
		}
		if want == "warning" && !strings.Contains(subj, "\u26A0") {
			t.Errorf("%s target: subject %q missing warning emoji", label, subj)
		}
	}
	check("warning", warnCap, "warning")
	check("alert", alertCap, "alert")
}

// TestEventSpikePayload_DefaultSeverity proves that when neither the target
// nor the result carries a severity, the payload defaults to "warning" — no
// auto-escalation from intensity (FR-011b).
func TestEventSpikePayload_DefaultSeverity(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	targets := []NotificationTarget{
		{Type: "webhook", URL: srv.URL, Triggers: []Trigger{TriggerEventSpike}},
	}
	result, _ := newSpikeResult()
	result.Status = ""

	SendNotification(targets, &NotifyState{}, result, TriggerEventSpike, "")

	if len(body) == 0 {
		t.Fatal("webhook captured no body")
	}
	var p map[string]any
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := p["status"]; got != "warning" {
		t.Errorf("payload.status = %v, want %q", got, "warning")
	}
}

// TestSendNotification_SpikeCooldownRolledBackOnWebhookFailure — T109. A
// webhook that returns 500 must leave LastSpikeNotify empty for that
// (target, host|channel) pair so the next confirmation fires through. Pre-
// patch, the cooldown timestamp was written before dispatch and never rolled
// back, so a transient failure consumed the entire repeat interval.
func TestSendNotification_SpikeCooldownRolledBackOnWebhookFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "simulated failure", http.StatusInternalServerError)
	}))
	defer srv.Close()

	targets := []NotificationTarget{
		{Type: "webhook", URL: srv.URL, Triggers: []Trigger{TriggerEventSpike}, RepeatMinutes: 10},
	}
	result, _ := newSpikeResult()
	state := &NotifyState{}

	SendNotification(targets, state, result, TriggerEventSpike, "")

	state.SpikeMu.Lock()
	defer state.SpikeMu.Unlock()
	m := state.LastSpikeNotify[srv.URL]
	key := result.Spike.Host + "|" + result.Spike.Channel
	if m != nil {
		if ts, ok := m[key]; ok && !ts.IsZero() {
			t.Errorf("LastSpikeNotify[%s][%s] = %v after send failure; want zero (rolled back)",
				srv.URL, key, ts)
		}
	}
}

// TestSendNotification_SpikeCooldownConsumedOnSuccess — T109. A webhook that
// returns 200 must leave the timestamp set so the repeat interval actually
// holds.
func TestSendNotification_SpikeCooldownConsumedOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	targets := []NotificationTarget{
		{Type: "webhook", URL: srv.URL, Triggers: []Trigger{TriggerEventSpike}, RepeatMinutes: 10},
	}
	result, _ := newSpikeResult()
	state := &NotifyState{}

	SendNotification(targets, state, result, TriggerEventSpike, "")

	state.SpikeMu.Lock()
	defer state.SpikeMu.Unlock()
	m := state.LastSpikeNotify[srv.URL]
	key := result.Spike.Host + "|" + result.Spike.Channel
	if m == nil {
		t.Fatalf("LastSpikeNotify[%s] not set after successful dispatch", srv.URL)
	}
	if ts := m[key]; ts.IsZero() {
		t.Errorf("LastSpikeNotify[%s][%s] = zero after successful dispatch", srv.URL, key)
	}
}

// TestSendNotification_MultiTargetOneFailsOneSucceeds — T109. Cross-target
// isolation for cooldown accounting under concurrent dispatch: a failure on
// target A must not affect the cooldown for target B, and vice versa.
func TestSendNotification_MultiTargetOneFailsOneSucceeds(t *testing.T) {
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad", http.StatusInternalServerError)
	}))
	defer failSrv.Close()
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer okSrv.Close()

	targets := []NotificationTarget{
		{Type: "webhook", URL: failSrv.URL, Triggers: []Trigger{TriggerEventSpike}, RepeatMinutes: 10},
		{Type: "webhook", URL: okSrv.URL, Triggers: []Trigger{TriggerEventSpike}, RepeatMinutes: 10},
	}
	result, _ := newSpikeResult()
	state := &NotifyState{}

	SendNotification(targets, state, result, TriggerEventSpike, "")

	key := result.Spike.Host + "|" + result.Spike.Channel
	state.SpikeMu.Lock()
	defer state.SpikeMu.Unlock()
	if m := state.LastSpikeNotify[failSrv.URL]; m != nil {
		if ts, ok := m[key]; ok && !ts.IsZero() {
			t.Errorf("failing target: cooldown not rolled back: %v", ts)
		}
	}
	m := state.LastSpikeNotify[okSrv.URL]
	if m == nil || m[key].IsZero() {
		t.Errorf("succeeding target: cooldown was not set")
	}
}

// TestTargetIsolation_TwoHealthyOneFailing — T094d / FR-025 / SC-009. A 500
// from one target must not prevent delivery to the other two healthy targets,
// and must not leave any state that blocks a subsequent spike from reaching
// the healthy targets (detection continues unaffected).
func TestTargetIsolation_TwoHealthyOneFailing(t *testing.T) {
	var ok1Count, ok2Count, failCount int32

	ok1Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&ok1Count, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ok1Srv.Close()
	ok2Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&ok2Count, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ok2Srv.Close()
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&failCount, 1)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer failSrv.Close()

	targets := []NotificationTarget{
		{Type: "webhook", URL: ok1Srv.URL, Triggers: []Trigger{TriggerEventSpike}, RepeatMinutes: 10},
		{Type: "webhook", URL: ok2Srv.URL, Triggers: []Trigger{TriggerEventSpike}, RepeatMinutes: 10},
		{Type: "webhook", URL: failSrv.URL, Triggers: []Trigger{TriggerEventSpike}, RepeatMinutes: 10},
	}
	result, _ := newSpikeResult()
	state := &NotifyState{}

	SendNotification(targets, state, result, TriggerEventSpike, "")

	if got := atomic.LoadInt32(&ok1Count); got != 1 {
		t.Errorf("healthy target 1: want 1 call, got %d", got)
	}
	if got := atomic.LoadInt32(&ok2Count); got != 1 {
		t.Errorf("healthy target 2: want 1 call, got %d", got)
	}
	if got := atomic.LoadInt32(&failCount); got != 1 {
		t.Errorf("failing target: want 1 attempt, got %d", got)
	}

	key := result.Spike.Host + "|" + result.Spike.Channel
	state.SpikeMu.Lock()
	// Healthy targets must have cooldowns set.
	if m := state.LastSpikeNotify[ok1Srv.URL]; m == nil || m[key].IsZero() {
		t.Error("healthy target 1: cooldown not set after delivery")
	}
	if m := state.LastSpikeNotify[ok2Srv.URL]; m == nil || m[key].IsZero() {
		t.Error("healthy target 2: cooldown not set after delivery")
	}
	// Failing target must have its cooldown rolled back so the next spike can retry it.
	if m := state.LastSpikeNotify[failSrv.URL]; m != nil {
		if ts, ok := m[key]; ok && !ts.IsZero() {
			t.Errorf("failing target: cooldown not rolled back after 500: %v", ts)
		}
	}
	state.SpikeMu.Unlock()

	// Verify detection continues: a second distinct spike (different channel)
	// still reaches both healthy targets and the failing target can attempt again.
	result2, _ := newSpikeResult()
	result2.Spike.Channel = "Microsoft-Windows-Security-Auditing/Operational"
	result2.Host = result2.Spike.Host

	SendNotification(targets, state, result2, TriggerEventSpike, "")

	if got := atomic.LoadInt32(&ok1Count); got != 2 {
		t.Errorf("healthy target 1 second spike: want 2 calls total, got %d", got)
	}
	if got := atomic.LoadInt32(&ok2Count); got != 2 {
		t.Errorf("healthy target 2 second spike: want 2 calls total, got %d", got)
	}
	if got := atomic.LoadInt32(&failCount); got != 2 {
		t.Errorf("failing target second spike: want 2 attempts total, got %d", got)
	}
}
