//go:build windows

package drainctl

import (
	"encoding/json"
	"testing"
)

func TestPerfSnapshot_JSON_RoundTrip(t *testing.T) {
	snap := &PerfSnapshot{
		CPUPct:        45.2,
		MemAvailMB:    8192,
		MemTotalMB:    16384,
		PagesSec:      12.5,
		DiskQueue:     0.3,
		TCPRetrans:    1.0,
		InputDelayP50: 10,
		InputDelayP95: 22,
		InputDelayMax: 48,
		RFXAvailable:  true,
		RFXFPSOut:     30,
		RFXRTT:        85.5,
	}

	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got PerfSnapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.CPUPct != snap.CPUPct {
		t.Errorf("CPUPct = %v, want %v", got.CPUPct, snap.CPUPct)
	}
	if got.InputDelayP95 != snap.InputDelayP95 {
		t.Errorf("InputDelayP95 = %v, want %v", got.InputDelayP95, snap.InputDelayP95)
	}
	if got.RFXAvailable != snap.RFXAvailable {
		t.Errorf("RFXAvailable = %v, want %v", got.RFXAvailable, snap.RFXAvailable)
	}
}

func TestPerfSnapshot_JSON_OmitsEmpty(t *testing.T) {
	snap := &PerfSnapshot{
		CPUPct:     45.2,
		MemAvailMB: 8192,
		MemTotalMB: 16384,
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}

	// omitempty fields should not be present when zero
	for _, key := range []string{"rfx_fps_out", "rfx_rtt_ms", "session_cpu_p95_pct"} {
		if _, ok := m[key]; ok {
			t.Errorf("expected %q to be omitted when zero, but it was present", key)
		}
	}

	// Non-omitempty fields should always be present
	for _, key := range []string{"cpu_pct", "mem_avail_mb", "rfx_available"} {
		if _, ok := m[key]; !ok {
			t.Errorf("expected %q to be present, but it was missing", key)
		}
	}
}

func TestCheckResult_PerformanceNilByDefault(t *testing.T) {
	r := &CheckResult{
		Status:  "Healthy",
		Message: "test",
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, ok := m["performance"]; ok {
		t.Error("performance should be omitted when nil")
	}
}

func TestCheckResult_WithPerformance(t *testing.T) {
	r := &CheckResult{
		Status:  "Healthy",
		Message: "test",
		Performance: &PerfSnapshot{
			CPUPct:     45.2,
			MemAvailMB: 8192,
			MemTotalMB: 16384,
		},
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	perf, ok := m["performance"]
	if !ok {
		t.Fatal("performance should be present when set")
	}
	pm := perf.(map[string]any)
	if pm["cpu_pct"] != 45.2 {
		t.Errorf("cpu_pct = %v, want 45.2", pm["cpu_pct"])
	}
}
