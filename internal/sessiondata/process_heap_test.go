//go:build windows

package sessiondata

import "testing"

func TestProcessHeapLimitsAndOrder(t *testing.T) {
	processes := []SessionProcess{
		{PID: 9, ImageName: "z.exe", CPUPercent: new(10.0), WorkingSetBytes: 10},
		{PID: 8, ImageName: "a.exe", CPUPercent: new(40.0), WorkingSetBytes: 1},
		{PID: 7, ImageName: "b.exe", CPUPercent: nil, WorkingSetBytes: 1 << 20},
		{PID: 6, ImageName: "c.exe", CPUPercent: new(20.0), WorkingSetBytes: 100},
		{PID: 5, ImageName: "d.exe", CPUPercent: new(30.0), WorkingSetBytes: 100},
		{PID: 4, ImageName: "e.exe", CPUPercent: new(50.0), WorkingSetBytes: 1},
	}
	want := [][]uint32{
		nil,
		{4},
		{4, 8},
		{4, 8, 5},
		{4, 8, 5, 6},
		{4, 8, 5, 6, 9},
	}
	for limit := range want {
		heap := NewProcessHeap(limit)
		for _, process := range processes {
			heap.Push(process)
			if len(heap.items) > limit {
				t.Fatalf("limit %d retained %d candidates", limit, len(heap.items))
			}
		}
		got := heap.Processes()
		if len(got) != len(want[limit]) {
			t.Fatalf("limit %d: got %d processes, want %d", limit, len(got), len(want[limit]))
		}
		for index, pid := range want[limit] {
			if got[index].PID != pid {
				t.Errorf("limit %d result %d PID = %d, want %d", limit, index, got[index].PID, pid)
			}
		}
	}
}

func TestProcessHeapTieOrder(t *testing.T) {
	heap := NewProcessHeap(5)
	for _, process := range []SessionProcess{
		{PID: 8, ImageName: "Zulu.exe", CPUPercent: new(20.0), WorkingSetBytes: 100},
		{PID: 6, ImageName: "alpha.exe", CPUPercent: new(20.0), WorkingSetBytes: 100},
		{PID: 4, ImageName: "Alpha.EXE", CPUPercent: new(20.0), WorkingSetBytes: 100},
		{PID: 3, ImageName: "beta.exe", CPUPercent: new(20.0), WorkingSetBytes: 200},
		{PID: 2, ImageName: "ignored.exe", CPUPercent: nil, WorkingSetBytes: 999},
		{PID: 1, ImageName: "gamma.exe", CPUPercent: new(20.0), WorkingSetBytes: 200},
	} {
		heap.Push(process)
	}
	want := []uint32{3, 1, 4, 6, 8}
	got := heap.Processes()
	for index, pid := range want {
		if got[index].PID != pid {
			t.Errorf("result %d PID = %d, want %d", index, got[index].PID, pid)
		}
	}
}

func TestProcessHeapNeverRetainsUnboundedInput(t *testing.T) {
	heap := NewProcessHeap(3)
	for pid := uint32(1); pid <= 1000; pid++ {
		heap.Push(SessionProcess{PID: pid, ImageName: "process.exe", WorkingSetBytes: DecimalUint64(pid)})
		if len(heap.items) > 3 {
			t.Fatalf("retained %d candidates after PID %d", len(heap.items), pid)
		}
	}
	got := heap.Processes()
	want := []uint32{1000, 999, 998}
	for index, pid := range want {
		if got[index].PID != pid {
			t.Errorf("result %d PID = %d, want %d", index, got[index].PID, pid)
		}
	}
}
