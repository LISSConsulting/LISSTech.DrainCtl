//go:build windows

package perfmon

import (
	"math"
	"syscall"
	"testing"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

func TestParseSessionInstanceID(t *testing.T) {
	tests := []struct {
		instance string
		wantID   uint32
		wantOK   bool
	}{
		{instance: "0", wantID: 0, wantOK: true},
		{instance: "12", wantID: 12, wantOK: true},
		{instance: "4294967295", wantID: ^uint32(0), wantOK: true},
		{instance: "012"},
		{instance: ""},
		{instance: "4294967296"},
		{instance: "12#1"},
		{instance: "rdp-tcp#12"},
		{instance: "_Total"},
		{instance: "12 "},
	}
	for _, test := range tests {
		gotID, gotOK := parseSessionInstanceID(test.instance)
		if gotID != test.wantID || gotOK != test.wantOK {
			t.Errorf("parseSessionInstanceID(%q) = (%d, %t), want (%d, %t)", test.instance, gotID, gotOK, test.wantID, test.wantOK)
		}
	}
}

func TestApplySessionFloatCorrelatesOnlyExactCanonicalInstance(t *testing.T) {
	out := map[uint32]SessionPDHMetrics{12: {}, 112: {}}
	ids := map[uint32]struct{}{12: {}, 112: {}}
	applySessionFloat(out, ids, []pdhInstanceValue{
		{Instance: "12", Value: 7.25},
		{Instance: "112", Value: 9.5},
		{Instance: "012", Value: 44},
		{Instance: "session-12", Value: 55},
		{Instance: "12#1", Value: 66},
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
			ids := map[uint32]struct{}{12: {}}
			(&Collector{logicalCPUCount: test.logicalCPUCount}).applySessionCPU(out, ids, []pdhInstanceValue{{Instance: "12", Value: test.value}})

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
	ids := map[uint32]struct{}{12: {}}
	if collectRemoteFX(out, ids, nil, false, 0, 240, false, func(metrics *SessionPDHMetrics, value float64) {
		metrics.RemoteFX = &SessionRemoteFXMetrics{FPS: new(value)}
	}) {
		t.Fatal("unavailable RemoteFX counter reported success")
	}
	if out[12].RemoteFX != nil {
		t.Fatal("unavailable RemoteFX counter fabricated a value")
	}
}

func TestCollectSessionPDHRejectsOversizedSessionSet(t *testing.T) {
	ids := make([]uint32, maxSessionPDHEntries+1)
	for i := range ids {
		ids[i] = uint32(i + 1)
	}
	metrics, capabilities := (&Collector{}).CollectSessionPDH(ids)
	if len(metrics) != 0 || capabilities.InputDelay || capabilities.RemoteFX {
		t.Fatalf("oversized collection = (%v, %+v), want no metrics and no capabilities", metrics, capabilities)
	}
}

func TestAggregateKeepsSuccessfulNamedValuePercentiles(t *testing.T) {
	first := dc.PerfSnapshot{SessionCPUP50: 11, SessionCPUP95: 24, P50Present: dc.PerfP50SessionCPU}
	second := dc.PerfSnapshot{SessionCPUP50: 18, SessionCPUP95: 31, P50Present: dc.PerfP50SessionCPU}
	aggregated := aggregate([]dc.PerfSnapshot{first, second})
	if aggregated.SessionCPUP50 != 18 || aggregated.SessionCPUP95 != 31 || !aggregated.HasP50(dc.PerfP50SessionCPU, aggregated.SessionCPUP50) {
		t.Fatalf("aggregate = %+v, want retained P50/P95 from successful values", aggregated)
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
