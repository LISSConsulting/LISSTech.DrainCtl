//go:build windows

package dashboard

import (
	"context"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

func TestExportFleetCSV_Contract(t *testing.T) {
	ds, ms, closeStore := newTestServerWithStore(t)
	defer closeStore()
	const (
		hostA = "FLEET-HOST-01"
		hostB = "FLEET-HOST-02"
	)
	ds.state.Register(hostA)
	ds.state.Register(hostB)
	base := time.Date(2026, 9, 30, 14, 0, 0, 0, time.UTC)
	if err := ms.Append(context.Background(), []telemetry.Sample{
		{Ts: base, Host: hostA, Counter: "cpu_pct", Value: 42.0},
		{Ts: base, Host: hostB, Counter: "cpu_pct", Value: 44.0},
		{Ts: base.Add(time.Minute), Host: hostA, Counter: "cpu_pct", Value: 43.0},
		{Ts: base.Add(time.Minute), Host: hostB, Counter: "cpu_pct", Value: 45.0},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	series, err := ms.QueryRangeFleet(
		context.Background(),
		[]string{hostA, hostB},
		base.Add(-time.Minute),
		base.Add(2*time.Minute),
		telemetry.TierRaw,
		[]string{"cpu_pct"},
		int64(telemetry.DefaultRawFleetBucketMs),
	)
	if err != nil {
		t.Fatalf("QueryRangeFleet: %v", err)
	}
	if len(series.Data["cpu_pct"].T) == 0 {
		t.Fatal("fleet fixture produced no cpu_pct samples")
	}


	from := base.Add(-time.Minute).Format(time.RFC3339)
	to := base.Add(2 * time.Minute).Format(time.RFC3339)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/_fleet/export?from="+from+"&to="+to+"&resolution=raw&format=csv&graph=overview.load", nil)
	r.SetPathValue("host", "_fleet")
	w := httptest.NewRecorder()
	ds.handleExport(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "text/csv; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/csv; charset=utf-8", got)
	}
	if got := w.Header().Get("Content-Disposition"); got != `attachment; filename="drainctl-fleet-overview-load.csv"` {
		t.Errorf("Content-Disposition = %q", got)
	}
	records, err := csv.NewReader(w.Body).ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}
	if len(records) < 2 {
		t.Fatalf("record count = %d, want header plus data row", len(records))
	}
	if got := len(records[0]); got != 8 {
		t.Errorf("header column count = %d, want 8", got)
	}
}

