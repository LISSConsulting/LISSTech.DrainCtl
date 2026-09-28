//go:build windows

package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHandleSSE_NamedSessionEventsUseSingleRecordFrames(t *testing.T) {
	for _, tc := range []struct {
		name     string
		typeName string
		data     string
	}{
		{name: "snapshot", typeName: "session_snapshot", data: `{"host":"rdsh-01.example.test","session_count":1}`},
		{name: "action", typeName: "session_action", data: `{"action_id":"0195a584-5b25-7a00-91a5-7cbb4dac92d9","state":"completed","completed_at_ms":1,"result_code":"completed"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ds := newTestServer(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(ctx)
			done := make(chan struct{})
			go func() {
				defer close(done)
				ds.handleSSE(w, r)
			}()

			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) && ds.broker.Count() == 0 {
				time.Sleep(5 * time.Millisecond)
			}
			if ds.broker.Count() == 0 {
				t.Fatal("handler did not subscribe")
			}
			ds.broker.Broadcast(mustMarshalTest(t, SSEEvent{Type: tc.typeName, Host: "hidden.example.test", Data: json.RawMessage(tc.data)}))
			time.Sleep(50 * time.Millisecond)
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("handler did not stop")
			}

			want := "event: " + tc.typeName + "\ndata: " + tc.data + "\n\n"
			if got := w.Body.String(); got != want {
				t.Fatalf("SSE frame = %q, want %q", got, want)
			}
		})
	}
}
