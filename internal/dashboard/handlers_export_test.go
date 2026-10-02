//go:build windows

package dashboard

import (
	"bytes"
	"context"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
	excelize "github.com/xuri/excelize/v2"
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

func TestExportPerHostCSV_Contract(t *testing.T) {
	ds, ms, closeStore := newTestServerWithStore(t)
	defer closeStore()

	const host = "sql-prod-01"
	ds.state.Register(host)
	base := time.Date(2026, 9, 30, 14, 0, 0, 0, time.UTC)
	if err := ms.Append(context.Background(), []telemetry.Sample{
		{Ts: base, Host: host, Counter: "cpu_pct", Value: 42.0},
		{Ts: base.Add(time.Minute), Host: host, Counter: "cpu_pct", Value: 43.0},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	from := base.Add(-time.Minute).Format(time.RFC3339)
	to := base.Add(2 * time.Minute).Format(time.RFC3339)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/"+host+"/export?from="+from+"&to="+to+"&resolution=raw&format=csv&graph=host.load", nil)
	r.SetPathValue("host", host)
	w := httptest.NewRecorder()
	ds.handleExport(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Disposition"); got == "" || !strings.Contains(got, host) {
		t.Errorf("Content-Disposition = %q, want canonical host %q", got, host)
	}
	records, err := csv.NewReader(w.Body).ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}
	if len(records) < 2 {
		t.Fatalf("record count = %d, want header plus data row", len(records))
	}
	for i, record := range records[1:] {
		if got := record[1]; got != host {
			t.Errorf("row %d host_name = %q, want %q", i+1, got, host)
		}
	}
}

func TestExportFleetXLSX_Contract(t *testing.T) {
	ds, ms, closeStore := newTestServerWithStore(t)
	defer closeStore()
	const host = "sql-prod-01"
	ds.state.Register(host)
	base := time.Date(2026, 9, 30, 14, 0, 0, 0, time.UTC)
	if err := ms.Append(context.Background(), []telemetry.Sample{{Ts: base, Host: host, Counter: "cpu_pct", Value: 42}}); err != nil {
		t.Fatal(err)
	}
	from, to := base.Add(-time.Minute).Format(time.RFC3339), base.Add(time.Minute).Format(time.RFC3339)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/_fleet/export?from="+from+"&to="+to+"&resolution=raw&format=xlsx&graph=overview.load", nil)
	r.SetPathValue("host", "_fleet")
	w := httptest.NewRecorder()
	ds.handleExport(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := w.Header().Get("Content-Disposition"); !strings.HasSuffix(got, ".xlsx\"") {
		t.Errorf("Content-Disposition = %q", got)
	}
	f, err := excelize.OpenReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("invalid XLSX response: %v", err)
	}
	defer func() { _ = f.Close() }()
	if got := f.GetSheetList(); len(got) != 2 || got[0] != "Data" || got[1] != "Context" {
		t.Errorf("sheet list = %v, want [Data Context]", got)
	}
}

func TestExportPerHostXLSX_Contract(t *testing.T) {
	ds, ms, closeStore := newTestServerWithStore(t)
	defer closeStore()
	const host = "sql-prod-01"
	ds.state.Register(host)
	base := time.Date(2026, 9, 30, 14, 0, 0, 0, time.UTC)
	if err := ms.Append(context.Background(), []telemetry.Sample{{Ts: base, Host: host, Counter: "cpu_pct", Value: 42}}); err != nil {
		t.Fatal(err)
	}
	from, to := base.Add(-time.Minute).Format(time.RFC3339), base.Add(time.Minute).Format(time.RFC3339)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/"+host+"/export?from="+from+"&to="+to+"&resolution=raw&format=xlsx&graph=host.load", nil)
	r.SetPathValue("host", host)
	w := httptest.NewRecorder()
	ds.handleExport(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	f, err := excelize.OpenReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	rows, err := f.GetRows("Data")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows[1:] {
		if len(row) > 1 && row[1] != host {
			t.Errorf("host_name = %q, want %q", row[1], host)
		}
	}
	contextRows, err := f.GetRows("Context")
	if err != nil {
		t.Fatal(err)
	}
	fields := make(map[string]bool, len(contextRows))
	for _, row := range contextRows[1:] {
		if len(row) > 0 {
			fields[row[0]] = true
		}
	}
	if !fields["host_name"] || fields["host_filter"] || fields["host_filter_count"] {
		t.Errorf("Context fields = %v", fields)
	}
}
