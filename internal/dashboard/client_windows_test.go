//go:build windows

package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// TestReportSpikeContextCancel proves that the ctx threaded through
// ReportSpike → negotiateRequest → http.NewRequestWithContext actually
// cancels in-flight HTTP requests. Without that plumb-through the call would
// hang on the dashboard client's 10-second internal Timeout instead of
// returning when ctx is cancelled. This is the load-bearing assertion that
// catches a future regression where someone reverts to http.NewRequest.
func TestReportSpikeContextCancel(t *testing.T) {
	// Server handler blocks until the request context is cancelled OR the
	// test ends (testDone). We watch testDone too because httptest.Server
	// Close blocks on any in-flight handler — without that escape hatch the
	// test would hang during server cleanup if the client's ctx cancel raced
	// the connection teardown.
	testDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-testDone:
		}
		w.WriteHeader(http.StatusOK)
	}))
	// Defers run LIFO: the close runs first, unblocking any handler still in
	// the select; only then does server.Close return. Without this ordering
	// httptest.Server.Close blocks 5s on the lingering connection.
	defer server.Close()
	defer close(testDone)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	done := make(chan struct{})
	start := time.Now()
	go func() {
		ReportSpike(ctx, server.URL, &dc.SpikePayload{Host: "test", Channel: "X"})
		close(done)
	}()

	select {
	case <-done:
		elapsed := time.Since(start)
		if elapsed > 200*time.Millisecond {
			t.Fatalf("ReportSpike returned after ctx cancel but too slowly: %v (want <200ms)", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("ReportSpike did not return within 2s after ctx cancel — ctx is not threaded into HTTP request")
	}
}
