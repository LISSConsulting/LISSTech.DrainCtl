//go:build windows

package investigation

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

type workerTestStorage struct {
	attempt      Attempt
	evidence     EvidenceSnapshot
	claimed      bool
	authorizeErr error
	authorizes   int
	completes    int
	failures     []TerminalReason
}

func (*workerTestStorage) Create(context.Context, CreateAttempt) (Attempt, error) {
	return Attempt{}, nil
}
func (s *workerTestStorage) ClaimNext(context.Context, int64, int64) (Attempt, bool, error) {
	if s.claimed {
		return Attempt{}, false, nil
	}
	s.claimed = true
	return s.attempt, true, nil
}
func (s *workerTestStorage) LoadEvidence(context.Context, int64) (EvidenceSnapshot, error) {
	return s.evidence, nil
}
func (*workerTestStorage) FinalizeResult(context.Context, int64, Report, Provenance, int64) error {
	return nil
}
func (s *workerTestStorage) AuthorizeSend(context.Context, int64, int64) error {
	s.authorizes++
	return s.authorizeErr
}
func (s *workerTestStorage) CompleteSend(context.Context, int64, int64) error {
	s.completes++
	return nil
}
func (s *workerTestStorage) FinalizeFailure(_ context.Context, _ int64, reason TerminalReason, _ int64) error {
	s.failures = append(s.failures, reason)
	return nil
}
func (*workerTestStorage) FinalizeUnavailable(context.Context, int64, int64) error { return nil }
func (*workerTestStorage) RecoverRunning(context.Context, int64) (int64, error)    { return 0, nil }
func (*workerTestStorage) Counts(context.Context) (AttemptCounts, error)           { return AttemptCounts{}, nil }
func (*workerTestStorage) History(context.Context, SourceRef) ([]Attempt, error)   { return nil, nil }
func (*workerTestStorage) SourceExists(context.Context, SourceRef) (bool, error)   { return false, nil }
func (*workerTestStorage) LatestFailure(context.Context) (*LatestFailure, error)   { return nil, nil }
func (s *workerTestStorage) Get(context.Context, int64) (Attempt, error)           { return s.attempt, nil }
func (*workerTestStorage) LoadResult(context.Context, int64) (*Report, *Provenance, error) {
	return nil, nil, nil
}

type workerTestClient struct {
	calls      int
	invokeAuth bool
	outcome    ProviderOutcome
	err        error
}

func (c *workerTestClient) Send(ctx context.Context, _ SendConfiguration, _ []byte, _ []string, _ []string, authorize SendAuthorization) (ProviderOutcome, error) {
	c.calls++
	if c.invokeAuth {
		if err := authorize(ctx); err != nil {
			return ProviderOutcome{}, err
		}
	}
	return c.outcome, c.err
}

type workerTestConfiguration struct {
	validate    func(int) error
	credential  []byte
	validations int
	decrypts    int
}

func (c *workerTestConfiguration) ValidateForSend(context.Context) error {
	c.validations++
	if c.validate != nil {
		return c.validate(c.validations)
	}
	return nil
}
func (c *workerTestConfiguration) DecryptCredential(context.Context) ([]byte, error) {
	c.decrypts++
	return c.credential, nil
}

func workerTestAttempt() Attempt { return Attempt{ID: 41, State: AttemptStateRunning} }
func workerTestEvidence() EvidenceSnapshot {
	return EvidenceSnapshot{Kind: SnapshotKindAvailable, CanonicalJSON: []byte(`{"v":1}`), FactIDs: validFacts()}
}

func TestWorkerFinalConfigurationRejectionHasNoSendLifecycle(t *testing.T) {
	store := &workerTestStorage{attempt: workerTestAttempt(), evidence: workerTestEvidence()}
	config := &workerTestConfiguration{
		credential: []byte("temporary-secret"),
		validate: func(call int) error {
			if call == 3 { // worker pre-check, provider initial check, provider final check
				return errors.New("configuration changed")
			}
			return nil
		},
	}
	provider := NewProviderClient()
	provider.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("provider sent after final configuration rejection")
		return nil, nil
	})}
	worker := &Worker{Store: store, Client: provider, Configuration: config, Now: func() time.Time { return time.UnixMilli(1).UTC() }}

	handled, err := worker.RunOne(context.Background())
	if err != nil || !handled {
		t.Fatalf("RunOne = (%v, %v)", handled, err)
	}
	if store.authorizes != 0 || store.completes != 0 || len(store.failures) != 1 || store.failures[0] != TerminalReasonConfigurationDisabled {
		t.Fatalf("send lifecycle after final config rejection: authorizes=%d completes=%d failures=%v", store.authorizes, store.completes, store.failures)
	}
}

func TestWorkerCompletesAuthorizedSendAndDoesNotResend(t *testing.T) {
	store := &workerTestStorage{attempt: workerTestAttempt(), evidence: workerTestEvidence()}
	client := &workerTestClient{invokeAuth: true, err: errors.New("network unavailable")}
	worker := &Worker{Store: store, Client: client, Configuration: &workerTestConfiguration{}, Now: func() time.Time { return time.UnixMilli(1).UTC() }}

	if handled, err := worker.RunOne(context.Background()); err != nil || !handled {
		t.Fatalf("first RunOne = (%v, %v)", handled, err)
	}
	if handled, err := worker.RunOne(context.Background()); err != nil || handled {
		t.Fatalf("second RunOne = (%v, %v)", handled, err)
	}
	if client.calls != 1 || store.authorizes != 1 || store.completes != 1 || len(store.failures) != 1 || store.failures[0] != TerminalReasonNetworkError {
		t.Fatalf("authorized send lifecycle: calls=%d authorizes=%d completes=%d failures=%v", client.calls, store.authorizes, store.completes, store.failures)
	}
}

func TestWorkerDoesNotCompleteWhenAuthorizationCallbackFails(t *testing.T) {
	authorizationErr := errors.New("durable authorization unavailable")
	store := &workerTestStorage{attempt: workerTestAttempt(), evidence: workerTestEvidence(), authorizeErr: authorizationErr}
	client := &workerTestClient{invokeAuth: true}
	worker := &Worker{Store: store, Client: client, Configuration: &workerTestConfiguration{}, Now: func() time.Time { return time.UnixMilli(1).UTC() }}

	_, err := worker.RunOne(context.Background())
	if !errors.Is(err, authorizationErr) {
		t.Fatalf("RunOne error = %v, want %v", err, authorizationErr)
	}
	if client.calls != 1 || store.authorizes != 1 || store.completes != 0 || len(store.failures) != 0 {
		t.Fatalf("authorization failure lifecycle: calls=%d authorizes=%d completes=%d failures=%v", client.calls, store.authorizes, store.completes, store.failures)
	}
}
