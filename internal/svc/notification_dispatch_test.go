//go:build windows

package svc

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

func TestNotificationDispatchDoesNotWaitForSlowEndpoint(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusOK)
		close(done)
	}))
	defer srv.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	result := &dc.CheckResult{Host: "SRV01", Status: "Healthy", Timestamp: time.Now()}
	targets := []dc.NotificationTarget{{Type: "webhook", URL: srv.URL, Triggers: []dc.Trigger{dc.TriggerDrainOn}}}
	returned := make(chan struct{})
	go func() {
		sendAsyncNotification(targets, nil, &dc.NotifyState{}, result, dc.TriggerDrainOn, "")
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("notification dispatch blocked on delivery")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("notification was not delivered")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("notification did not finish")
	}
}
