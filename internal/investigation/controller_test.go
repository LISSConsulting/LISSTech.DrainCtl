//go:build windows

package investigation

import (
	"context"
	"errors"
	"testing"
)

type controllerTestConfiguration struct {
	validateErr   error
	validateCalls int
	decryptCalls  int
}

func (c *controllerTestConfiguration) ValidateForSend(context.Context) error {
	c.validateCalls++
	return c.validateErr
}

func (c *controllerTestConfiguration) DecryptCredential(context.Context) ([]byte, error) {
	c.decryptCalls++
	return nil, errors.New("decrypt must not run during admission")
}

type controllerTestEvidence struct{ calls int }

func (e *controllerTestEvidence) Build(context.Context, SourceRef) (EvidenceSnapshot, error) {
	e.calls++
	return EvidenceSnapshot{}, nil
}

type controllerTestStorage struct {
	history      []Attempt
	previous     Attempt
	sourceExists bool
	creates      int
}

func (s *controllerTestStorage) Create(context.Context, CreateAttempt) (Attempt, error) {
	s.creates++
	return Attempt{ID: 1, State: AttemptStateQueued}, nil
}
func (*controllerTestStorage) ClaimNext(context.Context, int64, int64) (Attempt, bool, error) {
	return Attempt{}, false, nil
}
func (*controllerTestStorage) LoadEvidence(context.Context, int64) (EvidenceSnapshot, error) {
	return EvidenceSnapshot{}, nil
}
func (*controllerTestStorage) FinalizeResult(context.Context, int64, Report, Provenance, int64) error {
	return nil
}
func (*controllerTestStorage) AuthorizeSend(context.Context, int64, int64) error { return nil }
func (*controllerTestStorage) CompleteSend(context.Context, int64, int64) error  { return nil }
func (*controllerTestStorage) FinalizeFailure(context.Context, int64, TerminalReason, int64) error {
	return nil
}
func (*controllerTestStorage) FinalizeUnavailable(context.Context, int64, int64) error { return nil }
func (*controllerTestStorage) RecoverRunning(context.Context, int64) (int64, error)    { return 0, nil }
func (*controllerTestStorage) Counts(context.Context) (AttemptCounts, error) {
	return AttemptCounts{}, nil
}
func (s *controllerTestStorage) History(context.Context, SourceRef) ([]Attempt, error) {
	return s.history, nil
}
func (s *controllerTestStorage) SourceExists(context.Context, SourceRef) (bool, error) {
	return s.sourceExists, nil
}
func (*controllerTestStorage) LatestFailure(context.Context) (*LatestFailure, error) { return nil, nil }
func (s *controllerTestStorage) Get(context.Context, int64) (Attempt, error)         { return s.previous, nil }
func (*controllerTestStorage) LoadResult(context.Context, int64) (*Report, *Provenance, error) {
	return nil, nil, nil
}

func TestControllerHistoryRejectsUnknownSource(t *testing.T) {
	controller := &Controller{Store: &controllerTestStorage{}}
	_, err := controller.History(context.Background(), SourceRef{Kind: SourceKindEventSpike, ID: 1})
	if !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("history error = %v, want ErrSourceNotFound", err)
	}
}

func TestControllerRejectsUnreadyManualAndRetryBeforeEvidenceOrCreate(t *testing.T) {
	for _, test := range []struct {
		name  string
		retry bool
	}{
		{name: "manual"},
		{name: "retry", retry: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			configuration := &controllerTestConfiguration{validateErr: errors.New("provider disabled")}
			evidence := &controllerTestEvidence{}
			store := &controllerTestStorage{previous: Attempt{ID: 1, Source: SourceRef{Kind: SourceKindSessionDrop, ID: 7}, State: AttemptStateFailed}}
			controller := &Controller{Store: store, Evidence: evidence, Readiness: configuration}

			var err error
			if test.retry {
				_, err = controller.Retry(context.Background(), 1)
			} else {
				_, err = controller.CreateManual(context.Background(), SourceRef{Kind: SourceKindSessionDrop, ID: 7})
			}
			if !errors.Is(err, ErrProviderNotReady) {
				t.Fatalf("error = %v, want ErrProviderNotReady", err)
			}
			if configuration.validateCalls != 1 || configuration.decryptCalls != 0 {
				t.Fatalf("configuration calls = validate:%d decrypt:%d, want validate:1 decrypt:0", configuration.validateCalls, configuration.decryptCalls)
			}
			if evidence.calls != 0 || store.creates != 0 {
				t.Fatalf("unready admission created work: evidence:%d attempts:%d", evidence.calls, store.creates)
			}
		})
	}
}

func TestControllerRejectsDefaultDisabledManualWithoutDecrypting(t *testing.T) {
	decryptCalls := 0
	configuration := NewConfigAccessor(func(context.Context) (ConfigSnapshot, error) {
		return ConfigSnapshot{}, nil
	}, func(string) ([]byte, error) {
		decryptCalls++
		return []byte("secret"), nil
	})
	evidence := &controllerTestEvidence{}
	store := &controllerTestStorage{}
	controller := &Controller{Store: store, Evidence: evidence, Readiness: configuration}

	_, err := controller.CreateManual(context.Background(), SourceRef{Kind: SourceKindSessionDrop, ID: 7})
	if !errors.Is(err, ErrProviderNotReady) {
		t.Fatalf("error = %v, want ErrProviderNotReady", err)
	}
	if decryptCalls != 0 || evidence.calls != 0 || store.creates != 0 {
		t.Fatalf("default-disabled admission created work: decrypt:%d evidence:%d attempts:%d", decryptCalls, evidence.calls, store.creates)
	}
}

func TestControllerPreservesQueuedManualDuplicateWithoutReadinessCheck(t *testing.T) {
	configuration := &controllerTestConfiguration{validateErr: errors.New("provider disabled")}
	evidence := &controllerTestEvidence{}
	queued := Attempt{ID: 1, Source: SourceRef{Kind: SourceKindSessionDrop, ID: 7}, State: AttemptStateQueued}
	controller := &Controller{Store: &controllerTestStorage{history: []Attempt{queued}}, Evidence: evidence, Readiness: configuration}

	attempt, err := controller.CreateManual(context.Background(), queued.Source)
	if err != nil {
		t.Fatalf("create duplicate: %v", err)
	}
	if !attempt.Existing || attempt.ID != queued.ID {
		t.Fatalf("duplicate = %#v, want existing queued attempt", attempt)
	}
	if configuration.validateCalls != 0 || evidence.calls != 0 {
		t.Fatalf("duplicate performed admission work: validate:%d evidence:%d", configuration.validateCalls, evidence.calls)
	}
}

func TestControllerAdmitsReadyManualFlow(t *testing.T) {
	configuration := &controllerTestConfiguration{}
	evidence := &controllerTestEvidence{}
	store := &controllerTestStorage{}
	controller := &Controller{Store: store, Evidence: evidence, Readiness: configuration}

	attempt, err := controller.CreateManual(context.Background(), SourceRef{Kind: SourceKindSessionDrop, ID: 7})
	if err != nil {
		t.Fatalf("create ready manual attempt: %v", err)
	}
	if attempt.ID != 1 || evidence.calls != 1 || store.creates != 1 {
		t.Fatalf("ready admission = attempt:%#v evidence:%d attempts:%d", attempt, evidence.calls, store.creates)
	}
	if configuration.validateCalls != 1 || configuration.decryptCalls != 0 {
		t.Fatalf("configuration calls = validate:%d decrypt:%d, want validate:1 decrypt:0", configuration.validateCalls, configuration.decryptCalls)
	}
}
