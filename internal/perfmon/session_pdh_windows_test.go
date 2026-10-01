//go:build windows

package perfmon

import (
	"math"
	"syscall"
	"testing"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
)

func TestSessionInstanceKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "RDP-Tcp 6", want: "rdp-tcp6"},
		{input: "rdp-tcp#12", want: "rdp-tcp12"},
		{input: "Console", want: "console"},
		{input: "  Services ", want: "services"},
		{input: "", want: ""},
	}
	for _, test := range tests {
		if got := sessionInstanceKey(test.input); got != test.want {
			t.Errorf("sessionInstanceKey(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestApplySessionFloatCorrelatesOnlyActiveStationInstance(t *testing.T) {
	out := map[uint32]SessionPDHMetrics{12: {}, 112: {}}
	stations := map[string]uint32{
		"rdp-tcp6":   12,
		"rdp-tcp112": 112,
	}
	applySessionFloat(out, stations, []pdhInstanceValue{
		{Instance: "RDP-Tcp 6", Value: 7.25},
		{Instance: "RDP-Tcp 112", Value: 9.5},
		{Instance: "Console", Value: 44},
		{Instance: "session-12", Value: 55},
		{Instance: "Services", Value: 66},
		{Instance: "RDP-Tcp 9999", Value: 88},
	}, 0, 100, false, func(metrics *SessionPDHMetrics, value float64) {
		metrics.CPUPercent = new(value)
	})

	if got := out[12].CPUPercent; got == nil || *got != 7.25 {
		t.Fatalf("session 12 CPU = %v, want 7.25", got)
	}
	if got := out[112].CPUPercent; got == nil || *got != 9.5 {
		t.Fatalf("session 112 CPU = %v, want 9.5", got)
	}
}

func TestApplySessionCPUHonorsLogicalCPUWireBounds(t *testing.T) {
	tests := []struct {
		name            string
		logicalCPUCount uint16
		value           float64
		wantNil         bool
	}{
		{name: "multi-core aggregate", logicalCPUCount: 4, value: 250},
		{name: "exact host maximum", logicalCPUCount: 4, value: 400},
		{name: "over host maximum", logicalCPUCount: 4, value: 400.1, wantNil: true},
		{name: "non-finite", logicalCPUCount: 4, value: math.NaN(), wantNil: true},
		{name: "single-core maximum", logicalCPUCount: 1, value: 100},
		{name: "single-core overflow", logicalCPUCount: 1, value: 100.1, wantNil: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out := map[uint32]SessionPDHMetrics{12: {}}
			stations := map[string]uint32{"rdp-tcp6": 12}
			(&Collector{logicalCPUCount: test.logicalCPUCount}).applySessionCPU(out, stations, []pdhInstanceValue{{Instance: "RDP-Tcp 6", Value: test.value}})

			got := out[12].CPUPercent
			if test.wantNil {
				if got != nil {
					t.Fatalf("session CPU = %v, want nil", *got)
				}
				return
			}
			if got == nil || *got != test.value {
				t.Fatalf("session CPU = %v, want %v", got, test.value)
			}
		})
	}
}

func TestBoundedLogicalCPUCount(t *testing.T) {
	for _, test := range []struct {
		input int
		want  uint16
	}{
		{input: 0, want: 1},
		{input: 1, want: 1},
		{input: 1024, want: 1024},
		{input: 1025, want: 1024},
	} {
		if got := boundedLogicalCPUCount(test.input); got != test.want {
			t.Errorf("boundedLogicalCPUCount(%d) = %d, want %d", test.input, got, test.want)
		}
	}
}

func TestOptionalCounterFailureLeavesValuesNil(t *testing.T) {
	out := map[uint32]SessionPDHMetrics{12: {}}
	stations := map[string]uint32{"rdptcp6": 12}
	if collectRemoteFX(out, stations, nil, false, 0, 240, false, func(metrics *SessionPDHMetrics, value float64) {
		metrics.RemoteFX = &SessionRemoteFXMetrics{FPS: new(value)}
	}) {
		t.Fatal("unavailable RemoteFX counter reported success")
	}
	if out[12].RemoteFX != nil {
		t.Fatal("unavailable RemoteFX counter fabricated a value")
	}
}

func TestCollectSessionPDHRejectsOversizedSessionSet(t *testing.T) {
	records := make([]sessiondata.SessionRecord, maxSessionPDHEntries+1)
	for i := range records {
		records[i] = sessiondata.SessionRecord{SessionID: uint32(i + 1)}
	}
	metrics, capabilities := (&Collector{}).CollectSessionPDH(records)
	if len(metrics) != 0 || capabilities.InputDelay || capabilities.RemoteFX {
		t.Fatalf("oversized collection = (%v, %+v), want no metrics and no capabilities", metrics, capabilities)
	}
}

func TestCollectSessionPDHNoSessionQueryReturnsEmpty(t *testing.T) {
	station := "RDP-Tcp 6"
	records := []sessiondata.SessionRecord{{SessionID: 12, Station: &station}}
	metrics, capabilities := (&Collector{}).CollectSessionPDH(records)
	if len(metrics) != 1 || capabilities.InputDelay || capabilities.RemoteFX {
		t.Fatalf("missing-query collection = (%v, %+v), want one empty entry and no capabilities", metrics, capabilities)
	}
}

func TestCollectorCloseQueriesReleasesBothHandles(t *testing.T) {
	collector := &Collector{hostQuery: 1, sessionQuery: 2}
	var closed []syscall.Handle
	collector.closeQueries(func(handle syscall.Handle) {
		if handle != 0 {
			closed = append(closed, handle)
		}
	})
	if len(closed) != 2 || closed[0] != 1 || closed[1] != 2 {
		t.Fatalf("closed handles = %v, want [1 2]", closed)
	}
	if collector.hostQuery != 0 || collector.sessionQuery != 0 {
		t.Fatalf("query handles remain host=%d session=%d", collector.hostQuery, collector.sessionQuery)
	}
}
