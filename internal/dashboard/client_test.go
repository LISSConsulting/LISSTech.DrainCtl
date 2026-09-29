//go:build windows

package dashboard

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
	"io"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"
)

// generateTestCertDER creates a minimal self-signed certificate and returns
// the DER-encoded bytes. Used to exercise VerifyPeerCertificate callbacks.
func generateTestCertDER(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "drainctl-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return der
}

// fingerprintOf computes the SHA-256 hex fingerprint of a DER certificate,
// matching the format used by certFingerprint and newDashClient.
func fingerprintOf(der []byte) string {
	h := sha256.Sum256(der)
	return hex.EncodeToString(h[:])
}

// extractVerify extracts the VerifyPeerCertificate callback from a client
// built by newDashClient. Returns nil if the client has no callback.
func extractVerify(c *http.Client) func([][]byte, [][]*x509.Certificate) error {
	transport, ok := c.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil {
		return nil
	}
	return transport.TLSClientConfig.VerifyPeerCertificate
}

// ── newDashClient — no fingerprint ───────────────────────────────────────────

// TestNewDashClient_EmptyFingerprint_NoVerifyCallback verifies that a client
// built without a fingerprint has no VerifyPeerCertificate callback —
// it relies solely on InsecureSkipVerify for first-use trust.
func TestNewDashClient_EmptyFingerprint_NoVerifyCallback(t *testing.T) {
	c := newDashClient("")
	if fn := extractVerify(c); fn != nil {
		t.Error("expected no VerifyPeerCertificate when fingerprint is empty")
	}
}

// TestNewDashClient_EmptyFingerprint_InsecureSkipVerify verifies that
// InsecureSkipVerify is true when no fingerprint is configured (TOFU model).
func TestNewDashClient_EmptyFingerprint_InsecureSkipVerify(t *testing.T) {
	c := newDashClient("")
	transport := c.Transport.(*http.Transport)
	if !transport.TLSClientConfig.InsecureSkipVerify {
		t.Error("expected InsecureSkipVerify=true for first-use trust when no fingerprint is set")
	}
}

// ── newDashClient — with fingerprint ─────────────────────────────────────────

// TestNewDashClient_WithFingerprint_SetsVerifyCallback verifies that a client
// built with a non-empty fingerprint has a VerifyPeerCertificate callback.
func TestNewDashClient_WithFingerprint_SetsVerifyCallback(t *testing.T) {
	c := newDashClient("abcdef0123456789")
	if fn := extractVerify(c); fn == nil {
		t.Error("expected VerifyPeerCertificate to be set when fingerprint is non-empty")
	}
}

// ── VerifyPeerCertificate callback ────────────────────────────────────────────

// TestNewDashClient_VerifyCallback_EmptyCertList_ReturnsError verifies that
// the callback returns an error when no certificates are presented.
func TestNewDashClient_VerifyCallback_EmptyCertList_ReturnsError(t *testing.T) {
	c := newDashClient("someexpectedfingerprint")
	verify := extractVerify(c)
	if verify == nil {
		t.Fatal("expected VerifyPeerCertificate to be set")
	}

	err := verify(nil, nil)
	if err == nil {
		t.Fatal("expected error when rawCerts is nil, got nil")
	}
	if !strings.Contains(err.Error(), "no TLS certificate") {
		t.Errorf("error = %q, want 'no TLS certificate' in message", err)
	}
}

// TestNewDashClient_VerifyCallback_EmptySlice_ReturnsError verifies the empty
// [][]byte{} case (as distinct from nil), which also has len == 0.
func TestNewDashClient_VerifyCallback_EmptySlice_ReturnsError(t *testing.T) {
	c := newDashClient("someexpectedfingerprint")
	verify := extractVerify(c)
	if verify == nil {
		t.Fatal("expected VerifyPeerCertificate to be set")
	}

	err := verify([][]byte{}, nil)
	if err == nil {
		t.Fatal("expected error when rawCerts is empty, got nil")
	}
}

// TestNewDashClient_VerifyCallback_CorrectFingerprint_ReturnsNil verifies the
// happy path: the certificate fingerprint matches the pinned value → nil error.
func TestNewDashClient_VerifyCallback_CorrectFingerprint_ReturnsNil(t *testing.T) {
	certDER := generateTestCertDER(t)
	fp := fingerprintOf(certDER)

	c := newDashClient(fp)
	verify := extractVerify(c)
	if verify == nil {
		t.Fatal("expected VerifyPeerCertificate to be set")
	}

	if err := verify([][]byte{certDER}, nil); err != nil {
		t.Errorf("expected nil for matching fingerprint, got %v", err)
	}
}

// TestNewDashClient_VerifyCallback_WrongFingerprint_ReturnsError verifies that
// a certificate whose fingerprint does not match the pinned value → error.
func TestNewDashClient_VerifyCallback_WrongFingerprint_ReturnsError(t *testing.T) {
	certDER := generateTestCertDER(t)
	// Pin a fingerprint that differs from the certificate's actual fingerprint.
	c := newDashClient(strings.Repeat("0", 64))
	verify := extractVerify(c)
	if verify == nil {
		t.Fatal("expected VerifyPeerCertificate to be set")
	}

	err := verify([][]byte{certDER}, nil)
	if err == nil {
		t.Fatal("expected error for fingerprint mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Errorf("error = %q, want 'fingerprint mismatch' in message", err)
	}
}

// TestNewDashClient_VerifyCallback_ErrorContainsExpectedFingerprint verifies
// that the mismatch error message includes both the actual and expected
// fingerprints — this aids diagnosis without requiring log access.
func TestNewDashClient_VerifyCallback_ErrorContainsExpectedFingerprint(t *testing.T) {
	certDER := generateTestCertDER(t)
	actual := fingerprintOf(certDER)
	pinned := strings.Repeat("a", 64)

	c := newDashClient(pinned)
	verify := extractVerify(c)

	err := verify([][]byte{certDER}, nil)
	if err == nil {
		t.Fatal("expected mismatch error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, actual) {
		t.Errorf("error %q does not contain actual fingerprint %q", msg, actual)
	}
	if !strings.Contains(msg, pinned) {
		t.Errorf("error %q does not contain pinned fingerprint %q", msg, pinned)
	}
}

// TestNewDashClient_Timeout verifies the HTTP client has a non-zero timeout.
func TestNewDashClient_Timeout(t *testing.T) {
	c := newDashClient("")
	if c.Timeout == 0 {
		t.Error("expected non-zero timeout on dashboard HTTP client")
	}
}

// TestNewDashClient_TLSTransport verifies the client uses an http.Transport
// with a TLS config (required for TLS communication with the dashboard).
func TestNewDashClient_TLSTransport(t *testing.T) {
	c := newDashClient("")
	transport, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", c.Transport)
	}
	if transport.TLSClientConfig == nil {
		t.Error("expected non-nil TLSClientConfig on transport")
	}
}

// TestNewDashClient_VerifyCallback_SecondCertIgnored verifies that when
// multiple raw certs are provided, only rawCerts[0] (the leaf cert) is checked.
// This matches the TLS handshake where rawCerts[0] is always the server leaf.
func TestNewDashClient_VerifyCallback_SecondCertIgnored(t *testing.T) {
	leaf := generateTestCertDER(t)
	other := generateTestCertDER(t)
	fp := fingerprintOf(leaf)

	c := newDashClient(fp)
	verify := extractVerify(c)

	// rawCerts[0] matches, rawCerts[1] does not — should still succeed.
	if err := verify([][]byte{leaf, other}, nil); err != nil {
		t.Errorf("expected nil when rawCerts[0] matches, got %v", err)
	}
}

// TestInitDashClient_UpdatesGlobalClient verifies that InitDashClient replaces
// the package-level client and that the new client has a VerifyPeerCertificate
// callback when a fingerprint is provided.
func TestInitDashClient_UpdatesGlobalClient(t *testing.T) {
	// Save and restore the original client after the test.
	original := dashClientPtr.Load()
	t.Cleanup(func() { dashClientPtr.Store(original) })

	certDER := generateTestCertDER(t)
	fp := fingerprintOf(certDER)

	InitDashClient(fp)

	stored := dashClientPtr.Load()
	if stored == nil {
		t.Fatal("expected non-nil client after InitDashClient")
	}
	// Confirm the new client has VerifyPeerCertificate set.
	verify := extractVerify(stored)
	if verify == nil {
		t.Error("expected VerifyPeerCertificate to be set after InitDashClient(fp)")
	}
	// Confirm it works correctly.
	if err := verify([][]byte{certDER}, nil); err != nil {
		t.Errorf("expected nil for matching fingerprint after InitDashClient, got %v", err)
	}
}

func TestReportSessionActionPayload_ExactTopLevelCompletionEnvelope(t *testing.T) {
	payload, err := reportSessionActionPayload(
		&dc.CheckResult{Host: "host-a", Status: "Healthy"},
		[]sessiondata.CompletedSessionAction{{
			ActionID: "0195a584-5b25-7a00-91a5-7cbb4dac92d9",
			Outcome:  sessiondata.SessionActionOutcomeCompleted,
		}},
	)
	if err != nil {
		t.Fatalf("reportSessionActionPayload() error = %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode request payload: %v", err)
	}
	completions, ok := decoded["completed_session_actions"]
	if !ok {
		t.Fatalf("request JSON missing completed_session_actions: %s", payload)
	}
	const wantCompletion = `[{"action_id":"0195a584-5b25-7a00-91a5-7cbb4dac92d9","outcome":"completed"}]`
	if string(completions) != wantCompletion {
		t.Errorf("completed_session_actions = %s, want %s", completions, wantCompletion)
	}
	if strings.Contains(string(completions), "message") || strings.Contains(string(completions), "reason") {
		t.Errorf("completion payload contains sensitive field: %s", completions)
	}
}

func TestDecodeReportSessionActionResponse_AdditiveAndCompatible(t *testing.T) {
	t.Run("pending and acknowledged", func(t *testing.T) {
		report, err := decodeReportSessionActionResponse([]byte(`{
			"ok":true,
			"pending_session_actions":[{
				"action_id":"0195a584-5b25-7a00-91a5-7cbb4dac92d9",
				"type":"message",
				"session_id":5,
				"expected_logon_at_ms":1,
				"expires_at_ms":2,
				"message":"Save work"
			}],
			"acknowledged_session_action_ids":["0195a584-5b25-7a00-91a5-7cbb4dac92d8"]
		}`))
		if err != nil {
			t.Fatalf("decodeReportSessionActionResponse() error = %v", err)
		}
		if len(report.PendingSessionActions) != 1 || report.PendingSessionActions[0].Message == nil || *report.PendingSessionActions[0].Message != "Save work" {
			t.Fatalf("pending actions = %#v", report.PendingSessionActions)
		}
		if got := report.AcknowledgedSessionActionIDs; len(got) != 1 || got[0] != "0195a584-5b25-7a00-91a5-7cbb4dac92d8" {
			t.Fatalf("acknowledged IDs = %#v", got)
		}
	})
	t.Run("old dashboard omits additive fields", func(t *testing.T) {
		report, err := decodeReportSessionActionResponse([]byte(`{"ok":true}`))
		if err != nil {
			t.Fatalf("decodeReportSessionActionResponse() error = %v", err)
		}
		if len(report.PendingSessionActions) != 0 || len(report.AcknowledgedSessionActionIDs) != 0 {
			t.Fatalf("unexpected action response = %#v", report.ReportSessionActionResult)
		}
	})
}

func TestReportSessionActionPayloadAndResponse_RejectBounds(t *testing.T) {
	completions := make([]sessiondata.CompletedSessionAction, sessiondata.MaxSessionActionCommands+1)
	for i := range completions {
		completions[i] = sessiondata.CompletedSessionAction{
			ActionID: "0195a584-5b25-7a00-91a5-7cbb4dac92d9",
			Outcome:  sessiondata.SessionActionOutcomeCompleted,
		}
	}
	if _, err := reportSessionActionPayload(&dc.CheckResult{Host: "host-a"}, completions); err == nil {
		t.Fatal("reportSessionActionPayload accepted 21 completions")
	}
	acknowledged := make([]string, sessiondata.MaxSessionActionCommands+1)
	for i := range acknowledged {
		acknowledged[i] = "0195a584-5b25-7a00-91a5-7cbb4dac92d9"
	}
	body, err := json.Marshal(map[string]any{"acknowledged_session_action_ids": acknowledged})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if _, err := decodeReportSessionActionResponse(body); err == nil {
		t.Fatal("decodeReportSessionActionResponse accepted 21 acknowledged IDs")
	}
}

func TestReportStateWithSessionActions_ReportsHTTPFailure(t *testing.T) {
	original := dashboardReportRequest
	t.Cleanup(func() { dashboardReportRequest = original })
	dashboardReportRequest = func(context.Context, string, string, []byte) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(strings.NewReader("server error")),
		}, nil
	}
	if report := ReportStateWithSessionActions(context.Background(), "https://dashboard.example", &dc.CheckResult{Host: "host-a"}, nil); report != nil {
		t.Fatalf("ReportStateWithSessionActions() = %#v, want nil on HTTP error", report)
	}
	dashboardReportRequest = func(context.Context, string, string, []byte) (*http.Response, error) {
		return nil, errors.New("network unavailable")
	}
	if report := ReportStateWithSessionActions(context.Background(), "https://dashboard.example", &dc.CheckResult{Host: "host-a"}, nil); report != nil {
		t.Fatalf("ReportStateWithSessionActions() = %#v, want nil on transport error", report)
	}
}

func TestReportStateWithSessionActions_DecodesMaximumBoundedUnicodeActions(t *testing.T) {
	original := dashboardReportRequest
	t.Cleanup(func() { dashboardReportRequest = original })

	message := strings.Repeat("🧪", 256)
	actions := make([]sessiondata.PendingSessionAction, sessiondata.MaxSessionActionCommands)
	for i := range actions {
		actions[i] = sessiondata.PendingSessionAction{
			ActionID:          "0195a584-5b25-7a00-91a5-7cbb4dac92d9",
			Type:              sessiondata.SessionActionMessage,
			SessionID:         uint32(i + 1),
			ExpectedLogonAtMS: 1,
			ExpiresAtMS:       2,
			Message:           &message,
		}
	}
	body, err := json.Marshal(map[string]any{
		"ok":                      true,
		"pending_session_actions": actions,
	})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if len(body) > maxReportResponseBytes {
		t.Fatalf("bounded response size = %d, limit = %d", len(body), maxReportResponseBytes)
	}
	dashboardReportRequest = func(context.Context, string, string, []byte) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	}

	report := ReportStateWithSessionActions(context.Background(), "https://dashboard.example", &dc.CheckResult{Host: "host-a"}, nil)
	if report == nil || len(report.PendingSessionActions) != sessiondata.MaxSessionActionCommands {
		t.Fatalf("pending actions = %#v", report)
	}
	if got := *report.PendingSessionActions[0].Message; got != message {
		t.Errorf("first pending message = %q, want %q", got, message)
	}
}

func TestReportStateWithSessionActions_RejectsOversizedResponse(t *testing.T) {
	original := dashboardReportRequest
	t.Cleanup(func() { dashboardReportRequest = original })

	body := `{"ok":true}` + strings.Repeat(" ", maxReportResponseBytes)
	dashboardReportRequest = func(context.Context, string, string, []byte) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	}

	report := ReportStateWithSessionActions(context.Background(), "https://dashboard.example", &dc.CheckResult{Host: "host-a"}, nil)
	if report == nil {
		t.Fatal("ReportStateWithSessionActions() = nil, want safe empty result")
	}
	if len(report.PendingSessionActions) != 0 || len(report.AcknowledgedSessionActionIDs) != 0 || report.PendingCommand != nil {
		t.Fatalf("oversized response was decoded: %#v", report)
	}
}

func TestMarshalSessionSnapshotPayload_RejectsOversizePayload(t *testing.T) {
	snapshot := sessiondata.SessionSnapshot{
		CollectorVersion: strings.Repeat("x", sessiondata.MaxSnapshotBytes),
	}
	if _, err := marshalSessionSnapshotPayload(snapshot); err == nil {
		t.Fatal("marshalSessionSnapshotPayload accepted an oversized snapshot")
	}
}
