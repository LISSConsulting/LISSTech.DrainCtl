//go:build windows

package pipe

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strings"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"golang.org/x/sys/windows"
)

// mockHandler implements PipeHandler for tests.
type mockHandler struct {
	statusResult   *dc.CheckResult
	historyResult  []dc.AuditRecord
	registerResult json.RawMessage
	registerErr    error
	registerCalls  []string
}

func (m *mockHandler) HandleStatus() *dc.CheckResult {
	return m.statusResult
}

func (m *mockHandler) HandleHistory(limit int, changesOnly bool) []dc.AuditRecord {
	return m.historyResult
}

func (m *mockHandler) HandleServers() json.RawMessage {
	return nil
}

func (m *mockHandler) HandleRemoveServer(_ string) error {
	return fmt.Errorf("not implemented")
}

func (m *mockHandler) HandleRegister(url string) (json.RawMessage, error) {
	m.registerCalls = append(m.registerCalls, url)
	if m.registerErr != nil {
		return nil, m.registerErr
	}
	return m.registerResult, nil
}

// pipeCall writes req to handlePipeConn via an in-memory net.Pipe and returns
// the decoded PipeResponse.
func pipeCall(t *testing.T, req PipeRequest, handler PipeHandler) PipeResponse {
	t.Helper()
	client, server := net.Pipe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handlePipeConn(server, handler)
	}()

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := client.Write(data); err != nil {
		t.Fatalf("write request: %v", err)
	}

	buf := make([]byte, 256*1024)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	_ = client.Close()
	<-done

	var resp PipeResponse
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return resp
}

func TestHandlePipeConn_StatusOK(t *testing.T) {
	connAllowed := true
	dur := 42.0
	handler := &mockHandler{
		statusResult: &dc.CheckResult{
			Version:              "25.001.0",
			Status:               "Healthy",
			Message:              "All connections allowed.",
			ExitCode:             0,
			ConnectionsAllowed:   &connAllowed,
			StateDurationSeconds: &dur,
		},
	}

	resp := pipeCall(t, PipeRequest{Cmd: "status"}, handler)

	if !resp.OK {
		t.Fatalf("expected OK=true, got OK=false error=%q", resp.Error)
	}
	var result dc.CheckResult
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if result.Status != "Healthy" {
		t.Errorf("status: got %q, want %q", result.Status, "Healthy")
	}
	if result.ExitCode != 0 {
		t.Errorf("exit_code: got %d, want 0", result.ExitCode)
	}
}

func TestHandlePipeConn_StatusNilResult(t *testing.T) {
	// HandleStatus returns nil → service error response.
	handler := &mockHandler{statusResult: nil}

	resp := pipeCall(t, PipeRequest{Cmd: "status"}, handler)

	if resp.OK {
		t.Fatal("expected OK=false for nil status result")
	}
	if resp.Error == "" {
		t.Error("expected non-empty error message")
	}
}

func TestHandlePipeConn_HistoryOK(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	handler := &mockHandler{
		historyResult: []dc.AuditRecord{
			{Timestamp: now, Host: "srv1", DrainLabel: "AllowAll", ExitCode: 0},
			{Timestamp: now.Add(-5 * time.Minute), Host: "srv1", DrainLabel: "AllowAll", ExitCode: 0},
		},
	}

	resp := pipeCall(t, PipeRequest{Cmd: "history", Limit: 10}, handler)

	if !resp.OK {
		t.Fatalf("expected OK=true, got OK=false error=%q", resp.Error)
	}
	var records []dc.HistoryRecord
	if err := json.Unmarshal(resp.Data, &records); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if len(records) != 2 {
		t.Errorf("len(records): got %d, want 2", len(records))
	}
}

func TestHandlePipeConn_HistoryDefaultLimit(t *testing.T) {
	// Limit <= 0 should default to 50.
	var capturedLimit int
	handler := &captureHandler{}
	handler.onHistory = func(limit int, changesOnly bool) []dc.AuditRecord {
		capturedLimit = limit
		return nil
	}

	pipeCall(t, PipeRequest{Cmd: "history", Limit: 0}, handler)

	if capturedLimit != 50 {
		t.Errorf("limit: got %d, want 50", capturedLimit)
	}
}

func TestHandlePipeConn_HistoryChangesOnly(t *testing.T) {
	var capturedChangesOnly bool
	handler := &captureHandler{}
	handler.onHistory = func(limit int, changesOnly bool) []dc.AuditRecord {
		capturedChangesOnly = changesOnly
		return nil
	}

	pipeCall(t, PipeRequest{Cmd: "history", Limit: 5, ChangesOnly: true}, handler)

	if !capturedChangesOnly {
		t.Error("changesOnly: got false, want true")
	}
}

func TestHandlePipeConn_UnknownCommand(t *testing.T) {
	handler := &mockHandler{}

	resp := pipeCall(t, PipeRequest{Cmd: "bogus"}, handler)

	if resp.OK {
		t.Fatal("expected OK=false for unknown command")
	}
	if resp.Error == "" {
		t.Error("expected non-empty error message")
	}
}

func TestHandlePipeConn_InvalidJSON(t *testing.T) {
	client, server := net.Pipe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handlePipeConn(server, &mockHandler{})
	}()

	_, _ = client.Write([]byte("not json {{"))

	buf := make([]byte, 4096)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	_ = client.Close()
	<-done

	var resp PipeResponse
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.OK {
		t.Fatal("expected OK=false for invalid JSON")
	}
	if resp.Error == "" {
		t.Error("expected non-empty error message")
	}
}

// TestHandlePipeConn_ConnectionClosed verifies that handlePipeConn returns
// cleanly when the client closes the connection before sending any data.
// This exercises the `err != nil || n == 0` early-return path at the top of
// handlePipeConn.
func TestHandlePipeConn_ConnectionClosed(t *testing.T) {
	client, server := net.Pipe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handlePipeConn(server, &mockHandler{})
	}()

	// Close the client immediately — no data written.
	_ = client.Close()
	<-done // handlePipeConn must return without hanging.
}

// TestHandlePipeConn_StatusMarshalError verifies that handlePipeConn returns
// {ok:false, error:"marshal result: ..."} when json.Marshal fails on the
// status result.  A NaN in StateDurationSeconds (*float64) triggers
// json.UnsupportedValueError, which is the only reliable way to force a
// CheckResult marshal failure.
func TestHandlePipeConn_StatusMarshalError(t *testing.T) {
	nan := math.NaN()
	handler := &mockHandler{
		statusResult: &dc.CheckResult{
			Status:               "Healthy",
			StateDurationSeconds: &nan,
		},
	}

	resp := pipeCall(t, PipeRequest{Cmd: "status"}, handler)

	if resp.OK {
		t.Fatal("expected OK=false when marshal fails")
	}
	if !strings.Contains(resp.Error, "marshal result") {
		t.Errorf("error = %q, want 'marshal result' prefix", resp.Error)
	}
}

func TestHandlePipeConn_RegisterOK(t *testing.T) {
	handler := &mockHandler{
		registerResult: json.RawMessage(`{"tls_fingerprint":"abc123"}`),
	}

	resp := pipeCall(t, PipeRequest{Cmd: "register", URL: "https://dash.example:8443"}, handler)

	if !resp.OK {
		t.Fatalf("expected OK=true, got OK=false error=%q", resp.Error)
	}
	var result struct {
		TLSFingerprint string `json:"tls_fingerprint"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if result.TLSFingerprint != "abc123" {
		t.Errorf("tls_fingerprint: got %q, want %q", result.TLSFingerprint, "abc123")
	}
	if len(handler.registerCalls) != 1 || handler.registerCalls[0] != "https://dash.example:8443" {
		t.Errorf("registerCalls: got %v, want [https://dash.example:8443]", handler.registerCalls)
	}
}

func TestHandlePipeConn_RegisterMissingURL(t *testing.T) {
	handler := &mockHandler{}

	resp := pipeCall(t, PipeRequest{Cmd: "register"}, handler)

	if resp.OK {
		t.Fatal("expected OK=false when URL is empty")
	}
	if !strings.Contains(resp.Error, "url required") {
		t.Errorf("error = %q, want 'url required'", resp.Error)
	}
	if len(handler.registerCalls) != 0 {
		t.Errorf("registerCalls: got %d, want 0 (handler must not be invoked)", len(handler.registerCalls))
	}
}

func TestHandlePipeConn_RegisterHandlerError(t *testing.T) {
	handler := &mockHandler{registerErr: fmt.Errorf("register: status 401")}

	resp := pipeCall(t, PipeRequest{Cmd: "register", URL: "https://dash.example:8443"}, handler)

	if resp.OK {
		t.Fatal("expected OK=false when handler returns error")
	}
	if !strings.Contains(resp.Error, "status 401") {
		t.Errorf("error = %q, want substring 'status 401'", resp.Error)
	}
}

func TestReadPipeResponse_ContinuesOnMoreData(t *testing.T) {
	r := &scriptedReader{steps: []readStep{
		{data: []byte(`{"ok":true,"data":"`), err: windows.ERROR_MORE_DATA},
		{data: []byte(`chunked"}`), err: io.EOF},
	}}

	got, err := readPipeResponse(r)
	if err != nil {
		t.Fatalf("readPipeResponse: %v", err)
	}
	if string(got) != `{"ok":true,"data":"chunked"}` {
		t.Fatalf("response = %q", string(got))
	}
}

func TestReadPipeResponse_PropagatesUnexpectedError(t *testing.T) {
	want := errors.New("boom")
	r := &scriptedReader{steps: []readStep{{data: []byte("oops"), err: want}}}

	_, err := readPipeResponse(r)
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

// captureHandler lets tests inject callbacks for HandleHistory.
type captureHandler struct {
	onHistory func(limit int, changesOnly bool) []dc.AuditRecord
}

type readStep struct {
	data []byte
	err  error
}

type scriptedReader struct {
	steps []readStep
	idx   int
}

func (r *scriptedReader) Read(p []byte) (int, error) {
	if r.idx >= len(r.steps) {
		return 0, io.EOF
	}
	step := r.steps[r.idx]
	r.idx++
	n := copy(p, step.data)
	return n, step.err
}

func (c *captureHandler) HandleStatus() *dc.CheckResult {
	return &dc.CheckResult{Status: "Healthy"}
}

func (c *captureHandler) HandleHistory(limit int, changesOnly bool) []dc.AuditRecord {
	if c.onHistory != nil {
		return c.onHistory(limit, changesOnly)
	}
	return nil
}

func (c *captureHandler) HandleServers() json.RawMessage { return nil }

func (c *captureHandler) HandleRemoveServer(_ string) error {
	return fmt.Errorf("not implemented")
}

func (c *captureHandler) HandleRegister(_ string) (json.RawMessage, error) {
	return nil, fmt.Errorf("not implemented")
}

// TestRegisterViaPipe_WrapsDialFailureWithErrPipeUnavailable guards the
// discrimination the CLI register-command fallback relies on: when the
// service's pipe is unreachable the error must be detectable via
// errors.Is(err, ErrPipeUnavailable) so we only bootstrap via direct HTTP in
// that one case. If this wrap is ever removed a human register call would
// silently retry under the operator's token on every service-side failure.
func TestRegisterViaPipe_WrapsDialFailureWithErrPipeUnavailable(t *testing.T) {
	// No service / pipe listener is running in the test process, so dialPipe
	// fails with ERROR_FILE_NOT_FOUND. That must come back wrapped.
	_, err := RegisterViaPipe("https://dash.invalid")
	if err == nil {
		t.Fatal("expected error when no pipe listener is running")
	}
	if !errors.Is(err, ErrPipeUnavailable) {
		t.Errorf("errors.Is(err, ErrPipeUnavailable) = false; err = %v", err)
	}
}
