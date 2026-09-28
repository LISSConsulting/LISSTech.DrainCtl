//go:build windows

package investigation

import (
	"context"
	"errors"
	"sync"
	"time"
)

// SendClient isolates the fixed provider boundary for worker tests and production wiring.
type SendClient interface {
	Send(context.Context, SendConfiguration, []byte, []string, []string, SendAuthorization) (ProviderOutcome, error)
}

type finalizationCandidate struct {
	attemptID  int64
	report     Report
	provenance Provenance
	expiresAt  time.Time
}

// Worker serializes durable claims and provider egress. A candidate is retained
// only after a local HTTP return, solely to retry its SQLite finalization.
type Worker struct {
	Store          Storage
	Client         SendClient
	Configuration  SendConfiguration
	CanaryProvider func() []string
	Now            func() time.Time
	CutoffMS       func() int64

	mu        sync.Mutex
	sent      []time.Time
	candidate *finalizationCandidate
}

func (w *Worker) Status() WorkerStatus {
	return WorkerStatus{Workers: 1, MaxNonterminalAttempts: MaxNonterminalAttempts, RequestsPerMinute: 10, Burst: 2, RequestTimeoutSeconds: 30}
}

func (w *Worker) Recover(ctx context.Context) (int64, error) {
	if w.Store == nil {
		return 0, errors.New("incomplete worker")
	}
	return w.Store.RecoverRunning(ctx, w.now().UnixMilli())
}

// RunOne performs at most one durable finalization retry or one queue claim.
// It never reconstructs a request or repeats a transmission after authorization.
func (w *Worker) RunOne(ctx context.Context) (bool, error) {
	if w.Store == nil || w.Client == nil {
		return false, errors.New("incomplete worker")
	}
	if handled, err := w.finalizeCandidate(ctx); handled {
		return true, err
	}

	cutoff := int64(0)
	if w.CutoffMS != nil {
		cutoff = w.CutoffMS()
	}
	attempt, found, err := w.Store.ClaimNext(ctx, cutoff, w.now().UnixMilli())
	if err != nil || !found {
		return found, err
	}
	evidence, err := w.Store.LoadEvidence(ctx, attempt.ID)
	if err != nil {
		return true, w.fail(ctx, attempt.ID, TerminalReasonStorageUnavailable)
	}
	if evidence.Kind == SnapshotKindUnavailable {
		return true, w.Store.FinalizeUnavailable(ctx, attempt.ID, w.now().UnixMilli())
	}
	// Read the atomically current configuration before reserving egress. ProviderClient
	// validates it once more immediately before durable authorization.
	if err := w.validateConfiguration(ctx); err != nil {
		return true, w.fail(ctx, attempt.ID, TerminalReasonConfigurationDisabled)
	}
	if err := w.allowSend(); err != nil {
		return true, w.fail(ctx, attempt.ID, TerminalReasonProviderRateLimited)
	}

	authorized := false
	authorize := func(ctx context.Context) error {
		if err := w.Store.AuthorizeSend(ctx, attempt.ID, w.now().UnixMilli()); err != nil {
			return err
		}
		authorized = true
		return nil
	}
	outcome, sendErr := w.Client.Send(ctx, w.Configuration, evidence.CanonicalJSON, evidence.FactIDs, w.canaries(), authorize)
	if !authorized {
		w.releaseSendReservation()
		if sendErr != nil {
			return true, sendErr
		}
		if outcome.Reason == TerminalReasonNone {
			return true, errors.New("provider returned without send authorization")
		}
		return true, w.fail(ctx, attempt.ID, outcome.Reason)
	}

	// Durable authorization succeeded, so every return from the provider boundary
	// completes that lifecycle before any result or failure finalization.
	completedAt := w.now()
	if err := w.Store.CompleteSend(ctx, attempt.ID, completedAt.UnixMilli()); err != nil {
		// The provider exchange has returned. Do not call Send again; restart
		// recovery will safely close this authorized row if persistence remains unavailable.
		return true, err
	}
	if sendErr != nil {
		return true, w.fail(ctx, attempt.ID, TransportReason(sendErr))
	}
	if outcome.Reason != TerminalReasonNone {
		return true, w.fail(ctx, attempt.ID, outcome.Reason)
	}
	if outcome.Report == nil {
		return true, w.fail(ctx, attempt.ID, TerminalReasonResponseInvalid)
	}

	p, err := w.provenance(ctx, attempt.ID, outcome)
	if err != nil {
		return true, err
	}
	candidate := &finalizationCandidate{
		attemptID: attempt.ID, report: *outcome.Report, provenance: p,
		expiresAt: completedAt.Add(time.Duration(FinalizationLeaseMilliseconds) * time.Millisecond),
	}
	if err := w.Store.FinalizeResult(ctx, attempt.ID, candidate.report, candidate.provenance, w.now().UnixMilli()); err != nil {
		w.mu.Lock()
		w.candidate = candidate
		w.mu.Unlock()
		return true, err
	}
	return true, nil
}

func (w *Worker) provenance(ctx context.Context, id int64, outcome ProviderOutcome) (Provenance, error) {
	attempt, err := w.Store.Get(ctx, id)
	if err != nil {
		return Provenance{}, err
	}
	if attempt.SendAuthorizedAtMS == nil || attempt.SendCompletedAtMS == nil {
		return Provenance{}, errors.New("missing durable send lifecycle")
	}
	validation := ValidationOutcomeAccepted
	if outcome.Report.EvidenceSufficiency == EvidenceSufficiencyInsufficient {
		validation = ValidationOutcomeInsufficientEvidence
	}
	return Provenance{
		AttemptID: id, ProviderProfile: ProviderProfile, ProviderEndpoint: ProviderEndpoint,
		RequestedModel: ProviderModel, ResponseFormat: ResponseFormat, Store: false,
		SendAuthorizedAtMS: *attempt.SendAuthorizedAtMS, SendCompletedAtMS: *attempt.SendCompletedAtMS,
		RequestHeaderBytes: outcome.RequestHeaderBytes, RequestBodyBytes: outcome.RequestBodyBytes,
		ResponseHeaderBytes: outcome.ResponseHeaderBytes, ResponseBodyBytes: outcome.ResponseBodyBytes,
		ValidationOutcome: validation,
	}, nil
}

func (w *Worker) finalizeCandidate(ctx context.Context) (bool, error) {
	w.mu.Lock()
	candidate := w.candidate
	w.mu.Unlock()
	if candidate == nil {
		return false, nil
	}
	if !w.now().Before(candidate.expiresAt) {
		w.mu.Lock()
		if w.candidate == candidate {
			w.candidate = nil
		}
		w.mu.Unlock()
		return true, w.fail(ctx, candidate.attemptID, TerminalReasonStorageUnavailable)
	}
	if err := w.Store.FinalizeResult(ctx, candidate.attemptID, candidate.report, candidate.provenance, w.now().UnixMilli()); err != nil {
		return true, err
	}
	w.mu.Lock()
	if w.candidate == candidate {
		w.candidate = nil
	}
	w.mu.Unlock()
	return true, nil
}

func (w *Worker) fail(ctx context.Context, id int64, reason TerminalReason) error {
	return w.Store.FinalizeFailure(ctx, id, reason, w.now().UnixMilli())
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

func (w *Worker) canaries() []string {
	if w.CanaryProvider == nil {
		return nil
	}
	return w.CanaryProvider()
}

func (w *Worker) validateConfiguration(ctx context.Context) error {
	if w.Configuration == nil {
		return errors.New("missing send configuration")
	}
	return w.Configuration.ValidateForSend(ctx)
}

func (w *Worker) allowSend() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	cut := now.Add(-time.Minute)
	kept := w.sent[:0]
	for _, at := range w.sent {
		if at.After(cut) {
			kept = append(kept, at)
		}
	}
	w.sent = kept
	if len(w.sent) >= 10 {
		return errors.New("rate limited")
	}
	// Burst two means the first two requests may start together; afterward the
	// ten-per-minute rate refills one send token every six seconds.
	if len(w.sent) >= 2 && now.Sub(w.sent[len(w.sent)-1]) < 6*time.Second {
		return errors.New("burst limited")
	}
	w.sent = append(w.sent, now)
	return nil
}

// releaseSendReservation undoes a rate reservation when durable authorization
// failed before transmission could possibly begin.
func (w *Worker) releaseSendReservation() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.sent) != 0 {
		w.sent = w.sent[:len(w.sent)-1]
	}
}
