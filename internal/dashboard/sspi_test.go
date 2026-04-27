//go:build windows

package dashboard

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	sspiLiveContexts.Store(0)
	sspiPendingContexts.Store(0)
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
		sspiPendingContexts.Add(1)
	}
	sspiLiveContexts.Add(1)

	// Duplicate first-leg on the same connKey before the second leg arrives.
	second := &fakeReleaser{}
	secondPC := &pendingCtx{rel: second, created: time.Now()}
	if prev, loaded := pending.Swap(key, secondPC); loaded {
		releasePendingContext(prev.(*pendingCtx))
	} else {
		sspiPendingContexts.Add(1)
	}
	sspiLiveContexts.Add(1)

	if got := first.released.Load(); got != 1 {
		t.Errorf("orphaned first context: Release called %d times, want 1", got)
	}
	if got := second.released.Load(); got != 0 {
		t.Errorf("active second context: Release called %d times, want 0", got)
	}
	if live, pending := SspiMetrics(); live != 1 || pending != 1 {
		t.Errorf("counters: live=%d pending=%d, want live=1 pending=1", live, pending)
	}

	// Drain — the active context should still be releasable normally.
	if v, ok := pending.LoadAndDelete(key); ok {
		releasePendingContext(v.(*pendingCtx))
		sspiPendingContexts.Add(-1)
	}
	if got := second.released.Load(); got != 1 {
		t.Errorf("after take: Release called %d times, want 1", got)
	}
	if live, p := SspiMetrics(); live != 0 || p != 0 {
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
		sspiPendingContexts.Add(1)
	}
	sspiLiveContexts.Add(1)

	if got := rel.released.Load(); got != 0 {
		t.Errorf("fresh insert: Release called %d times, want 0", got)
	}
	if live, p := SspiMetrics(); live != 1 || p != 1 {
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
