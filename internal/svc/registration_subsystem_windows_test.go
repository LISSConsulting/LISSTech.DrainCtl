//go:build windows

package svc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/dashboard"
)

func TestRegistrationSubsystem_RegisterRoutesToWorker(t *testing.T) {
	s := newRegistrationSubsystem(func(ctx context.Context, gotURL string) (*dashboard.RegisterResult, error) {
		if gotURL != "https://dash.example" {
			t.Fatalf("url = %q, want https://dash.example", gotURL)
		}
		return &dashboard.RegisterResult{TLSFingerprint: "abc123"}, nil
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(s.Stop)

	got, err := s.Register(context.Background(), "https://dash.example")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got == nil || got.TLSFingerprint != "abc123" {
		t.Fatalf("Register result = %#v, want fingerprint abc123", got)
	}
}

func TestRegistrationSubsystem_StopCancelsInFlightRegister(t *testing.T) {
	started := make(chan struct{})
	s := newRegistrationSubsystem(func(ctx context.Context, _ string) (*dashboard.RegisterResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := s.Register(context.Background(), "https://dash.example")
		done <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Register did not reach worker")
	}
	s.Stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Register err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Register did not unblock after Stop")
	}
}

func TestServiceHandler_HandleRegisterUsesSubsystem(t *testing.T) {
	h, _, cleanup := newHandlerStore(t)
	defer cleanup()

	s := newRegistrationSubsystem(func(ctx context.Context, gotURL string) (*dashboard.RegisterResult, error) {
		if gotURL != "https://dash.example" {
			t.Fatalf("url = %q, want https://dash.example", gotURL)
		}
		return &dashboard.RegisterResult{TLSFingerprint: "def456"}, nil
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(s.Stop)
	h.registration = s

	raw, err := h.HandleRegister("https://dash.example")
	if err != nil {
		t.Fatalf("HandleRegister: %v", err)
	}
	var got dashboard.RegisterResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.TLSFingerprint != "def456" {
		t.Fatalf("fingerprint = %q, want def456", got.TLSFingerprint)
	}
}
