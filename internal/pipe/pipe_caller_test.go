//go:build windows

package pipe

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestRegisterViaPipe_CurrentProcessPrivileged(t *testing.T) {
	if _, err := CheckViaPipe(); err == nil {
		t.Skip("service pipe already active; skipping isolated caller integration test")
	}

	handler := &mockHandler{
		registerResult: json.RawMessage(`{"tls_fingerprint":"abc123"}`),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverErr := make(chan error, 1)
	go func() {
		conn, err := acceptPipeConn(ctx)
		if err != nil {
			serverErr <- err
			return
		}
		handlePipeConn(conn, handler)
		serverErr <- nil
	}()

	isEnvAccessDenied := func(e error) bool {
		if e == nil {
			return false
		}
		low := strings.ToLower(e.Error())
		return strings.Contains(low, "access is denied") || strings.Contains(low, "access denied")
	}

	raw, err := RegisterViaPipe("https://dash.example:8443")
	if err != nil {
		if isEnvAccessDenied(err) {
			cancel()
			<-serverErr
			t.Skip("current process cannot connect as a privileged pipe caller in this environment")
		}
		t.Fatalf("RegisterViaPipe: %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server goroutine: %v", err)
	}
	if string(raw) != `{"tls_fingerprint":"abc123"}` {
		t.Fatalf("RegisterViaPipe raw = %s, want %s", string(raw), `{"tls_fingerprint":"abc123"}`)
	}
	if len(handler.registerCalls) != 1 || handler.registerCalls[0] != "https://dash.example:8443" {
		t.Fatalf("registerCalls = %v, want [https://dash.example:8443]", handler.registerCalls)
	}
}

func TestHandlePipeConn_PrivilegedDeny(t *testing.T) {
	oldCheck := callerIsPrivilegedFunc
	callerIsPrivilegedFunc = func(net.Conn) (bool, string, error) {
		return false, "S-1-5-21-1-2-3-1002", nil
	}
	t.Cleanup(func() { callerIsPrivilegedFunc = oldCheck })

	logBuf := &syncBuffer{}
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	resp := pipeCall(t, PipeRequest{Cmd: "register", URL: "https://dash.example:8443"}, &mockHandler{})
	if resp.OK {
		t.Fatal("expected OK=false for denied privileged verb")
	}
	if resp.Error != "access denied" {
		t.Fatalf("resp.Error = %q, want %q", resp.Error, "access denied")
	}

	logText := logBuf.String()
	if !strings.Contains(logText, "pipe=access_denied") {
		t.Fatalf("deny log missing access_denied marker: %s", logText)
	}
	if !strings.Contains(logText, "sid=S-1-5-21-1-2-3-1002") {
		t.Fatalf("deny log missing sid: %s", logText)
	}
}
