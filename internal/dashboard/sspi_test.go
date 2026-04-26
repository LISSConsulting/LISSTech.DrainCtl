//go:build windows

package dashboard

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/alexbrainman/sspi"
)

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
