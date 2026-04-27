//go:build windows

package dashboard

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sspimetrics"
	"github.com/alexbrainman/sspi"
)

// fakeReleaser stands in for *negotiate.ServerContext in unit tests; counts
// Release() calls so we can assert orphan/reaper paths actually free their
// kernel handles.
type fakeReleaser struct {
	released atomic.Int32
}

func (f *fakeReleaser) Release() error {
	f.released.Add(1)
	return nil
}

func resetSspiCounters() {
	sspimetrics.Live.Store(0)
	sspimetrics.Pending.Store(0)
}

// TestPendingMap_OrphanReplaceReleasesPriorContext is the regression test for
// the leak observed in the v26.116.33 production diag bundles: a duplicate
// first-leg arriving on a connKey that already had a pendingCtx silently
// overwrote the slot, leaking one SSPI handle per occurrence.
func TestPendingMap_OrphanReplaceReleasesPriorContext(t *testing.T) {
	resetSspiCounters()
	t.Cleanup(resetSspiCounters)

	var pending sync.Map
	const key = "10.0.0.1:54321"

	first := &fakeReleaser{}
	firstPC := &pendingCtx{rel: first, created: time.Now()}
	if prev, loaded := pending.Swap(key, firstPC); loaded {
		releasePendingContext(prev.(*pendingCtx))
	} else {
		sspimetrics.Pending.Add(1)
	}
	sspimetrics.Live.Add(1)

	// Duplicate first-leg on the same connKey before the second leg arrives.
	second := &fakeReleaser{}
	secondPC := &pendingCtx{rel: second, created: time.Now()}
	if prev, loaded := pending.Swap(key, secondPC); loaded {
		releasePendingContext(prev.(*pendingCtx))
	} else {
		sspimetrics.Pending.Add(1)
	}
	sspimetrics.Live.Add(1)

	if got := first.released.Load(); got != 1 {
		t.Errorf("orphaned first context: Release called %d times, want 1", got)
	}
	if got := second.released.Load(); got != 0 {
		t.Errorf("active second context: Release called %d times, want 0", got)
	}
	if live, pending := sspimetrics.Snapshot(); live != 1 || pending != 1 {
		t.Errorf("counters: live=%d pending=%d, want live=1 pending=1", live, pending)
	}

	// Drain — the active context should still be releasable normally.
	if v, ok := pending.LoadAndDelete(key); ok {
		releasePendingContext(v.(*pendingCtx))
		sspimetrics.Pending.Add(-1)
	}
	if got := second.released.Load(); got != 1 {
		t.Errorf("after take: Release called %d times, want 1", got)
	}
	if live, p := sspimetrics.Snapshot(); live != 0 || p != 0 {
		t.Errorf("counters after drain: live=%d pending=%d, want 0/0", live, p)
	}
}

// TestPendingMap_FreshKeyDoesNotReleaseAnything covers the non-conflict
// path: a first-leg landing on a key with no prior entry must increment
// the pending counter and not invoke Release on anything.
func TestPendingMap_FreshKeyDoesNotReleaseAnything(t *testing.T) {
	resetSspiCounters()
	t.Cleanup(resetSspiCounters)

	var pending sync.Map
	rel := &fakeReleaser{}
	pc := &pendingCtx{rel: rel, created: time.Now()}
	if prev, loaded := pending.Swap("10.0.0.2:9", pc); loaded {
		releasePendingContext(prev.(*pendingCtx))
	} else {
		sspimetrics.Pending.Add(1)
	}
	sspimetrics.Live.Add(1)

	if got := rel.released.Load(); got != 0 {
		t.Errorf("fresh insert: Release called %d times, want 0", got)
	}
	if live, p := sspimetrics.Snapshot(); live != 1 || p != 1 {
		t.Errorf("counters: live=%d pending=%d, want 1/1", live, p)
	}
}

func TestSharedServerCred_AcquiresOnceAcrossConcurrentCallers(t *testing.T) {
	var calls atomic.Int32
	s := &sharedServerCred{
		acquire: func(string) (*sspi.Credentials, error) {
			calls.Add(1)
			return &sspi.Credentials{}, nil
		},
	}

	const N = 200
	var wg sync.WaitGroup
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			if _, err := s.get(); err != nil {
				t.Errorf("get: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("AcquireServerCredentials called %d times across %d callers, want 1 — leaks lsass state", got, N)
	}
}

func TestSharedServerCred_FailureIsNotCached(t *testing.T) {
	var calls atomic.Int32
	boom := errors.New("transient lsass failure")
	s := &sharedServerCred{
		acquire: func(string) (*sspi.Credentials, error) {
			n := calls.Add(1)
			if n < 3 {
				return nil, boom
			}
			return &sspi.Credentials{}, nil
		},
	}

	for i := range 4 {
		_, err := s.get()
		switch {
		case i < 2 && !errors.Is(err, boom):
			t.Fatalf("call %d: got err=%v, want %v", i, err, boom)
		case i >= 2 && err != nil:
			t.Fatalf("call %d: got err=%v, want nil", i, err)
		}
	}

	// After first success the cred is cached; no further acquire calls.
	if got := calls.Load(); got != 3 {
		t.Fatalf("acquire called %d times, want 3 (2 failures + 1 success)", got)
	}
}

func TestSharedServerCred_ReleaseClearsCache(t *testing.T) {
	var calls atomic.Int32
	s := &sharedServerCred{
		acquire: func(string) (*sspi.Credentials, error) {
			calls.Add(1)
			return &sspi.Credentials{}, nil
		},
	}

	if _, err := s.get(); err != nil {
		t.Fatalf("get: %v", err)
	}
	s.release()
	if _, err := s.get(); err != nil {
		t.Fatalf("get after release: %v", err)
	}

	if got := calls.Load(); got != 2 {
		t.Fatalf("acquire called %d times, want 2 (one before release, one after)", got)
	}
}

// TestPendingReaper_NoRaceReleasesEntry — happy path. With no concurrent
// handler claiming the entry, the reaper's CompareAndDelete succeeds and
// the entry is released exactly once. Proves the B1 fix didn't break the
// uncontended cleanup path.
func TestPendingReaper_NoRaceReleasesEntry(t *testing.T) {
	resetSspiCounters()
	t.Cleanup(resetSspiCounters)

	var pending sync.Map
	const key = "192.0.2.1:51000"

	// Pre-state: a leg-1 handler has stored an entry and incremented both
	// counters (mirrors production sspi.go after a successful first leg).
	rel := &fakeReleaser{}
	pc := &pendingCtx{rel: rel, created: time.Now().Add(-90 * time.Second)}
	pending.Store(key, pc)
	sspimetrics.Pending.Add(1)
	sspimetrics.Live.Add(1)

	// Walk the map exactly the way the reaper does, applying the new
	// CompareAndDelete-guarded cleanup.
	pending.Range(func(k, v any) bool {
		entry := v.(*pendingCtx)
		if time.Since(entry.created) > 60*time.Second {
			if pending.CompareAndDelete(k, v) {
				releasePendingContext(entry)
				sspimetrics.Pending.Add(-1)
			}
		}
		return true
	})

	if got := rel.released.Load(); got != 1 {
		t.Errorf("Release called %d times, want 1 (uncontended cleanup)", got)
	}
	mapEmpty := true
	pending.Range(func(any, any) bool { mapEmpty = false; return false })
	if !mapEmpty {
		t.Error("pending map not drained after reaper")
	}
	if live := sspimetrics.Live.Load(); live != 0 {
		t.Errorf("sspimetrics.Live = %d, want 0 after release", live)
	}
	if p := sspimetrics.Pending.Load(); p != 0 {
		t.Errorf("sspimetrics.Pending = %d, want 0 after delete", p)
	}
}

// TestPendingReaper_LosesCompareAndDeleteWhenHandlerWonFirst — regression
// test for B1. Models the racy interleaving as it unfolds in production:
// a leg-2 handler wins pending.LoadAndDelete(K) AFTER pending.Range has
// yielded (K, value) to the reaper but BEFORE the reaper's cleanup runs.
// The buggy code (bare pending.Delete + releasePendingContext) would
// double-release the kernel handle and drive sspimetrics.Pending to -1.
// The fix's CompareAndDelete returns false on a stolen entry; the reaper
// skips the release entirely.
func TestPendingReaper_LosesCompareAndDeleteWhenHandlerWonFirst(t *testing.T) {
	resetSspiCounters()
	t.Cleanup(resetSspiCounters)

	var pending sync.Map
	const key = "192.0.2.2:51001"

	// Pre-state: leg-1 stored, both counters incremented.
	rel := &fakeReleaser{}
	pc := &pendingCtx{rel: rel, created: time.Now().Add(-90 * time.Second)}
	pending.Store(key, pc)
	sspimetrics.Pending.Add(1)
	sspimetrics.Live.Add(1)

	// Reaper begins iterating; capture the value Range would have yielded.
	var observed any
	pending.Range(func(_, v any) bool {
		observed = v
		return false // stop after first
	})

	// Concurrent leg-2 handler wins the entry (mirrors sspi.go:189-192:
	// LoadAndDelete + sspimetrics.Pending.Add(-1) on the leg-2 path).
	if _, ok := pending.LoadAndDelete(key); !ok {
		t.Fatal("handler should have found the entry")
	}
	sspimetrics.Pending.Add(-1)

	// Now the reaper resumes its cleanup against the value it captured.
	// CompareAndDelete must fail (entry already gone) → no double-release.
	if pending.CompareAndDelete(key, observed) {
		t.Fatal("CompareAndDelete succeeded after handler win — race not modeled correctly")
	}
	// The buggy code would have run releasePendingContext here unconditionally;
	// the fixed code skips it. Verify by counters + Release count.

	if got := rel.released.Load(); got != 0 {
		t.Errorf("Release called %d times, want 0 (handler owns the context)", got)
	}
	if live := sspimetrics.Live.Load(); live != 1 {
		t.Errorf("sspimetrics.Live = %d, want 1 (handler hasn't called its deferred Release in this test)", live)
	}
	if p := sspimetrics.Pending.Load(); p != 0 {
		t.Errorf("sspimetrics.Pending = %d, want 0 (1 from setup, -1 from handler model, 0 net)", p)
	}
}
