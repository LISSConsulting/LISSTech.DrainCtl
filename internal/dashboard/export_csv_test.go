//go:build windows

package dashboard

import (
	"bytes"
	"encoding/csv"
	"reflect"
	"testing"
	"time"
)

func TestExportCSV_WritesDataRows(t *testing.T) {
	t0 := time.Date(2026, 9, 30, 14, 0, 0, 0, time.UTC)
	v42 := 42.0
	v8192 := 8192.0
	vMillis := 0.001
	snap := Snapshot{Rows: []Observation{
		{Timestamp: t0.Add(time.Minute), HostName: "z-host", SeriesID: "z_series", SeriesLabel: "=literal", Unit: "%", Value: &v42, Aggregation: "avg", Resolution: "1m"},
		{Timestamp: t0, HostName: "csv-go-host,1", SeriesID: "b_series", SeriesLabel: "+literal", Unit: "MB", Value: &v8192, Aggregation: "avg", Resolution: "1m"},
		{Timestamp: t0, HostName: "name\"quoted\"", SeriesID: "a_series", SeriesLabel: "-literal", Unit: "ms", Value: &vMillis, Aggregation: "p95", Resolution: "1m"},
		{Timestamp: t0, HostName: "multi\nline", SeriesID: "a_series", SeriesLabel: "@literal\t\r", Unit: "", Value: nil, Aggregation: "raw", Resolution: "raw"},
	}}

	var body bytes.Buffer
	if err := WriteCSV(&body, snap); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	records, err := csv.NewReader(&body).ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}

	wantHeader := []string{"timestamp_utc", "host_name", "series_id", "series_label", "unit", "value", "aggregation", "resolution"}
	if len(records) != 5 {
		t.Fatalf("record count = %d, want header plus 4 rows: %q", len(records), records)
	}
	if !reflect.DeepEqual(records[0], wantHeader) {
		t.Errorf("header = %q, want %q", records[0], wantHeader)
	}

	// Rows must sort by timestamp, series ID, then host name.
	wantOrder := []struct {
		seriesID string
		host     string
	}{
		{"a_series", "multi\nline"},
		{"a_series", "name\"quoted\""},
		{"b_series", "csv-go-host,1"},
		{"z_series", "z-host"},
	}
	for i, want := range wantOrder {
		row := records[i+1]
		if row[2] != want.seriesID || row[1] != want.host {
			t.Errorf("row %d order = (%q, %q), want (%q, %q)", i+1, row[2], row[1], want.seriesID, want.host)
		}
	}

	if got := records[1][5]; got != "" {
		t.Errorf("missing value = %q, want empty", got)
	}
	if got := records[2][5]; got != "0.001" {
		t.Errorf("precision value = %q, want 0.001", got)
	}
	if got := records[3][5]; got != "8192" {
		t.Errorf("integer value = %q, want 8192", got)
	}
	if got := records[4][5]; got != "42" {
		t.Errorf("whole-number value = %q, want 42", got)
	}

	if got := records[1][3]; got != "@literal\t\r" {
		t.Errorf("formula-triggering label = %q, want unchanged", got)
	}
	if got := records[2][3]; got != "-literal" {
		t.Errorf("formula-triggering label = %q, want unchanged", got)
	}
	if got := records[3][3]; got != "+literal" {
		t.Errorf("formula-triggering label = %q, want unchanged", got)
	}
	if got := records[4][3]; got != "=literal" {
		t.Errorf("formula-triggering label = %q, want unchanged", got)
	}
}

func TestExportCSV_FleetPlaceholderIsEmptyHost(t *testing.T) {
	value := 1.0
	var body bytes.Buffer
	if err := WriteCSV(&body, Snapshot{Rows: []Observation{{
		Timestamp: time.Date(2026, 9, 30, 14, 0, 0, 0, time.UTC),
		HostName:  hostFleetPlaceholder,
		SeriesID:  "cpu_pct",
		Value:     &value,
	}}}); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	records, err := csv.NewReader(&body).ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("record count = %d, want header plus data row", len(records))
	}
	if got := records[1][1]; got != "" {
		t.Errorf("fleet host = %q, want empty", got)
	}
}
