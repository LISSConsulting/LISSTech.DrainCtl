//go:build windows

package drainctl

import (
	"errors"
	"testing"
	"time"
)

type fakeProcessSnapshot struct {
	entries []processEntry
	index   int
	nextErr error
	closed  int
}

func (s *fakeProcessSnapshot) Next() (processEntry, bool, error) {
	if s.nextErr != nil {
		err := s.nextErr
		s.nextErr = nil
		return processEntry{}, false, err
	}
	if s.index == len(s.entries) {
		return processEntry{}, false, nil
	}
	entry := s.entries[s.index]
	s.index++
	return entry, true, nil
}
func (s *fakeProcessSnapshot) Close() error { s.closed++; return nil }

type fakeProcessHandle struct {
	creation   uint64
	cpu        uint64
	timesErr   error
	workingSet uint64
	memoryErr  error
	closed     int
}

func (h *fakeProcessHandle) Times() (uint64, uint64, error) { return h.creation, h.cpu, h.timesErr }
func (h *fakeProcessHandle) WorkingSet() (uint64, error)    { return h.workingSet, h.memoryErr }
func (h *fakeProcessHandle) Close() error                   { h.closed++; return nil }

type fakeProcessAPI struct {
	snapshots     []*fakeProcessSnapshot
	sessions      map[uint32]uint32
	sessionErr    map[uint32]error
	handles       map[uint32][]*fakeProcessHandle
	openErr       map[uint32]error
	now           []time.Time
	snapshotCalls int
}

func (a *fakeProcessAPI) Snapshot() (processSnapshot, error) {
	a.snapshotCalls++
	if len(a.snapshots) == 0 {
		return nil, errors.New("unexpected snapshot")
	}
	snapshot := a.snapshots[0]
	a.snapshots = a.snapshots[1:]
	return snapshot, nil
}
func (a *fakeProcessAPI) SessionID(pid uint32) (uint32, error) {
	if err := a.sessionErr[pid]; err != nil {
		return 0, err
	}
	return a.sessions[pid], nil
}
func (a *fakeProcessAPI) Open(pid uint32) (processHandle, error) {
	if err := a.openErr[pid]; err != nil {
		return nil, err
	}
	handles := a.handles[pid]
	if len(handles) == 0 {
		return nil, errors.New("unexpected open")
	}
	handle := handles[0]
	a.handles[pid] = handles[1:]
	return handle, nil
}
func (a *fakeProcessAPI) Now() time.Time {
	now := a.now[0]
	a.now = a.now[1:]
	return now
}

func processTicks(duration time.Duration) uint64 { return uint64(duration / 100) }

func TestProcessCollectorAttributesOnlyRequestedSessionAndClosesHandles(t *testing.T) {
	kept := &fakeProcessHandle{creation: 1, cpu: 10, workingSet: 4096}
	other := &fakeProcessHandle{creation: 1, cpu: 10, workingSet: 8192}
	snapshot := &fakeProcessSnapshot{entries: []processEntry{{pid: 11, imageName: `C:\Windows\kept.exe`}, {pid: 12, imageName: "other.exe"}}}
	api := &fakeProcessAPI{snapshots: []*fakeProcessSnapshot{snapshot}, sessions: map[uint32]uint32{11: 7, 12: 8}, handles: map[uint32][]*fakeProcessHandle{11: {kept}, 12: {other}}, now: []time.Time{time.Unix(1, 0)}}
	collector := newProcessCollector(api, 1)

	got, available := collector.CollectTopProcesses([]uint32{7}, 5)
	if !available {
		t.Fatal("process collection marked unavailable")
	}
	if len(got[7]) != 1 || got[7][0].PID != 11 || got[7][0].ImageName != "kept.exe" {
		t.Fatalf("session 7 processes = %#v", got[7])
	}
	if _, exists := got[8]; exists {
		t.Fatalf("unrequested session was returned: %#v", got[8])
	}
	if kept.closed != 1 || other.closed != 0 || snapshot.closed != 1 {
		t.Fatalf("closure counts kept=%d other=%d snapshot=%d", kept.closed, other.closed, snapshot.closed)
	}
}

func TestProcessCollectorFirstSecondAndPIDReuse(t *testing.T) {
	start := time.Unix(100, 0)
	first := &fakeProcessHandle{creation: 10, cpu: processTicks(time.Second), workingSet: 10}
	reused := &fakeProcessHandle{creation: 20, cpu: processTicks(8 * time.Second), workingSet: 20}
	second := &fakeProcessHandle{creation: 20, cpu: processTicks(12 * time.Second), workingSet: 30}
	api := &fakeProcessAPI{
		snapshots: []*fakeProcessSnapshot{{entries: []processEntry{{pid: 44, imageName: "app.exe"}}}, {entries: []processEntry{{pid: 44, imageName: "app.exe"}}}, {entries: []processEntry{{pid: 44, imageName: "app.exe"}}}},
		sessions:  map[uint32]uint32{44: 3}, handles: map[uint32][]*fakeProcessHandle{44: {first, reused, second}},
		now: []time.Time{start, start.Add(10 * time.Second), start.Add(20 * time.Second)},
	}
	collector := newProcessCollector(api, 2)
	for pass := range 2 {
		got, available := collector.CollectTopProcesses([]uint32{3}, 1)
		if !available || len(got[3]) != 1 || got[3][0].CPUPercent != nil {
			t.Fatalf("pass %d = %#v, available=%t", pass, got[3], available)
		}
	}
	got, available := collector.CollectTopProcesses([]uint32{3}, 1)
	if !available || len(got[3]) != 1 || got[3][0].CPUPercent == nil {
		t.Fatalf("second sample = %#v, available=%t", got[3], available)
	}
	if cpu := *got[3][0].CPUPercent; cpu != 20 {
		t.Errorf("normalized CPU = %v, want 20", cpu)
	}
}

func TestProcessCollectorInaccessibleAndExitedProcesses(t *testing.T) {
	noCPU := &fakeProcessHandle{workingSet: 100, timesErr: errors.New("access denied")}
	noMemory := &fakeProcessHandle{workingSet: 100, memoryErr: errors.New("exited")}
	snapshot := &fakeProcessSnapshot{entries: []processEntry{{pid: 1, imageName: "cpu-null.exe"}, {pid: 2, imageName: "omitted.exe"}, {pid: 3, imageName: "gone.exe"}}}
	api := &fakeProcessAPI{snapshots: []*fakeProcessSnapshot{snapshot}, sessions: map[uint32]uint32{1: 9, 2: 9, 3: 9}, handles: map[uint32][]*fakeProcessHandle{1: {noCPU}, 2: {noMemory}}, openErr: map[uint32]error{3: errors.New("exited")}, now: []time.Time{time.Unix(1, 0)}}
	collector := newProcessCollector(api, 1)
	got, available := collector.CollectTopProcesses([]uint32{9}, 5)
	if !available || len(got[9]) != 1 || got[9][0].CPUPercent != nil {
		t.Fatalf("inaccessible result = %#v, available=%t", got[9], available)
	}
	if noCPU.closed != 1 || noMemory.closed != 1 || snapshot.closed != 1 {
		t.Fatalf("closure counts cpu=%d memory=%d snapshot=%d", noCPU.closed, noMemory.closed, snapshot.closed)
	}
}

func TestProcessCollectorTopZeroSkipsToolhelp(t *testing.T) {
	api := &fakeProcessAPI{now: []time.Time{time.Unix(1, 0)}}
	collector := newProcessCollector(api, 1)
	got, available := collector.CollectTopProcesses([]uint32{6}, 0)
	if !available || len(got[6]) != 0 || api.snapshotCalls != 0 {
		t.Fatalf("top zero result=%#v available=%t snapshots=%d", got, available, api.snapshotCalls)
	}
}

func TestProcessCollectorClosesSnapshotOnEnumerationError(t *testing.T) {
	snapshot := &fakeProcessSnapshot{nextErr: errors.New("toolhelp failure")}
	api := &fakeProcessAPI{snapshots: []*fakeProcessSnapshot{snapshot}, now: []time.Time{time.Unix(1, 0)}}
	collector := newProcessCollector(api, 1)
	_, available := collector.CollectTopProcesses([]uint32{1}, 1)
	if available || snapshot.closed != 1 {
		t.Fatalf("available=%t snapshot closed=%d", available, snapshot.closed)
	}
}
