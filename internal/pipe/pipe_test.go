//go:build windows

package pipe

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// mockHandler implements PipeHandler for tests.
type mockHandler struct {
	statusResult  *dc.CheckResult
	historyResult []dc.AuditRecord
}

func (m *mockHandler) HandleStatus() *dc.CheckResult {
	return m.statusResult
}

func (m *mockHandler) HandleHistory(limit int, changesOnly bool) []dc.AuditRecord {
	return m.historyResult
}

// pipeCall writes req to handlePipeConn via an in-memory net.Pipe and returns
// the decoded PipeResponse.
func pipeCall(t *testing.T, req PipeRequest, handler PipeHandler) PipeResponse {
	t.Helper()
	client, server := net.Pipe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handlePipeConn(server, handler, nil)
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
		handlePipeConn(server, &mockHandler{}, nil)
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

// captureHandler lets tests inject callbacks for HandleHistory.
type captureHandler struct {
	onHistory func(limit int, changesOnly bool) []dc.AuditRecord
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
