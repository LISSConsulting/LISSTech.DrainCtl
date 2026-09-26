//go:build windows

package pipe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

	// Retry the dial briefly: the server goroutine creates the pipe inside
	// acceptPipeConn, so a too-fast dial races and gets "file not found" /
	// ErrPipeUnavailable. The pipe usually exists within a millisecond of
	// goroutine launch, but slower CI runners (GitHub Actions Windows
	// runners are ~3× slower than dev) can stretch this past 100 ms.
	var raw json.RawMessage
	var err error
	deadline := time.Now().Add(2 * time.Second)
	for {
		raw, err = RegisterViaPipe("https://dash.example:8443")
		if err == nil || !errors.Is(err, ErrPipeUnavailable) || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
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

func TestBrokerSetupViaPipe_CurrentProcessPrivileged(t *testing.T) {
	if _, err := CheckViaPipe(); err == nil {
		t.Skip("service pipe already active; skipping isolated caller integration test")
	}

	handler := &mockHandler{brokerSetupResult: &BrokerSetupResult{
		ConnectionBroker: "rdc.example.test",
		CollectionCount:  2,
		SessionHostCount: 5,
		ServiceIdentity:  `NT AUTHORITY\SYSTEM`,
	}}
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

	var result *BrokerSetupResult
	var err error
	deadline := time.Now().Add(2 * time.Second)
	for {
		result, err = BrokerSetupViaPipe("rdc.example.test")
		if err == nil || !errors.Is(err, ErrPipeUnavailable) || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		low := strings.ToLower(err.Error())
		if strings.Contains(low, "access is denied") || strings.Contains(low, "access denied") {
			cancel()
			<-serverErr
			t.Skip("current process cannot connect as a privileged pipe caller in this environment")
		}
		t.Fatalf("BrokerSetupViaPipe: %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server goroutine: %v", err)
	}
	if result == nil || *result != *handler.brokerSetupResult {
		t.Fatalf("BrokerSetupViaPipe result = %+v, want %+v", result, handler.brokerSetupResult)
	}
	if got := handler.brokerSetupCalls; len(got) != 1 || got[0] != "rdc.example.test" {
		t.Fatalf("brokerSetupCalls = %v, want [rdc.example.test]", got)
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

	handler := &mockHandler{}
	resp := pipeCall(t, PipeRequest{Cmd: "broker-setup", Broker: "rdc.example.test"}, handler)
	if resp.OK {
		t.Fatal("expected OK=false for denied privileged verb")
	}
	if resp.Error != "access denied" {
		t.Fatalf("resp.Error = %q, want %q", resp.Error, "access denied")
	}
	if got := handler.brokerSetupCalls; len(got) != 0 {
		t.Fatalf("brokerSetupCalls = %v, want no handler invocation", got)
	}

	logText := logBuf.String()
	if !strings.Contains(logText, "pipe=access_denied") {
		t.Fatalf("deny log missing access_denied marker: %s", logText)
	}
	if !strings.Contains(logText, "sid=S-1-5-21-1-2-3-1002") {
		t.Fatalf("deny log missing sid: %s", logText)
	}
}
