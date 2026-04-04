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
