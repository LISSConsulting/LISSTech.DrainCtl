//go:build windows

package sessiondata

import (
	"math"
	"testing"
)

func TestCalculateSessionWorkloadNormalizesEligibilityAndMissingValues(t *testing.T) {
	snapshot := SessionSnapshot{LogicalCPUCount: 8, Sessions: []SessionRecord{
		{State: SessionActive, CPUPercent: new(100.0), WorkingSetBytes: new(DecimalUint64(0))},
		{State: SessionDisconnected, CPUPercent: new(160.0), WorkingSetBytes: new(DecimalUint64(1024))},
		{State: SessionIdle, CPUPercent: new(0.0)},
		{State: SessionListen, CPUPercent: new(800.0), WorkingSetBytes: new(DecimalUint64(1 << 30))},
		{State: SessionConnected, CPUPercent: new(math.NaN()), WorkingSetBytes: new(DecimalUint64(2048))},
	}}
	got := CalculateSessionWorkload(snapshot)
	if got.CPUCount != 3 || got.CPUSum != 32.5 || got.CPUGE5 != 2 || got.CPUGE20 != 1 {
		t.Fatalf("CPU aggregate = count %d sum %.2f ge5 %d ge20 %d", got.CPUCount, got.CPUSum, got.CPUGE5, got.CPUGE20)
	}
	if got.MemoryCount != 3 || got.MemorySumBytes != 3072 || got.MemoryZeroCount != 1 {
		t.Fatalf("memory aggregate = count %d sum %.0f zero %d", got.MemoryCount, got.MemorySumBytes, got.MemoryZeroCount)
	}
	if p95, ok := got.CPUPercentile(.95); !ok || math.Abs(p95-20) > .5 {
		t.Fatalf("CPU P95 = %v, %v", p95, ok)
	}
}

func TestSessionWorkloadHistogramRoundTripAndErrorBounds(t *testing.T) {
	var aggregate SessionWorkloadAggregate
	for _, cpu := range []float64{0, 4.99, 5, 12.34, 99.9, 100} {
		snapshot := SessionSnapshot{LogicalCPUCount: 1, Sessions: []SessionRecord{{State: SessionActive, CPUPercent: new(cpu)}}}
		aggregate.Merge(CalculateSessionWorkload(snapshot))
	}
	for _, memory := range []uint64{0, 1, 4096, 1 << 20, 987654321, 64 << 30} {
		snapshot := SessionSnapshot{LogicalCPUCount: 1, Sessions: []SessionRecord{{State: SessionIdle, WorkingSetBytes: new(DecimalUint64(memory))}}}
		aggregate.Merge(CalculateSessionWorkload(snapshot))
	}
	cpu, err := DecodeCPUHistogram(aggregate.EncodeCPUHistogram())
	if err != nil || cpu != aggregate.CPUHistogram {
		t.Fatalf("CPU round trip: %v", err)
	}
	zero, memory, err := DecodeMemoryHistogram(aggregate.EncodeMemoryHistogram())
	if err != nil || zero != aggregate.MemoryZeroCount || len(memory) != len(aggregate.MemoryHistogram) {
		t.Fatalf("memory round trip: %v", err)
	}
	for bin, count := range aggregate.MemoryHistogram {
		if memory[bin] != count {
			t.Fatalf("memory bin %d = %d, want %d", bin, memory[bin], count)
		}
	}
	for _, value := range []uint64{1, 4096, 1 << 20, 987654321, 64 << 30} {
		representative := memoryBinValue(memoryBin(value))
		if relative := math.Abs(representative-float64(value)) / float64(value); relative > .02 {
			t.Fatalf("memory %d relative error %.6f", value, relative)
		}
	}
}
