//go:build windows

package investigation

import (
	"context"
	"errors"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

var (
	ErrControllerUnavailable = errors.New("investigation: controller unavailable")
	ErrProviderNotReady      = errors.New("investigation: provider not ready")
	ErrQueueFull             = errors.New("investigation: queue full")
	ErrAttemptLimitReached   = errors.New("investigation: attempt limit reached")
	ErrAttemptNotFound       = errors.New("investigation: attempt not found")
	ErrSourceNotFound        = errors.New("investigation: source not found")
	ErrSourceIneligible      = errors.New("investigation: source ineligible")
	ErrSourceCompleted       = errors.New("investigation: source completed")
	ErrRetryRequired         = errors.New("investigation: retry required")
	ErrRetryNotAllowed       = errors.New("investigation: retry not allowed")
)

// EvidenceBuilder derives a new host-free snapshot from a durable source.
// Implementations return ErrSourceNotFound when that source has expired.
type EvidenceBuilder interface {
	Build(context.Context, SourceRef) (EvidenceSnapshot, error)
}

// ProviderReadiness validates the currently loaded provider configuration
// without exposing or decrypting the credential.
type ProviderReadiness interface {
	ValidateForSend(context.Context) error
}

// Controller is the single handler-facing orchestration seam. Storage performs
// the authoritative transactional source-eligibility, dedupe, and cap checks;
// Wake only signals durable queued work and is never a correctness handoff.
type Controller struct {
	Store     ControllerStorage
	Evidence  EvidenceBuilder
	Readiness ProviderReadiness
	Wake      func()
	Worker    *Worker
	Now       func() time.Time
}

func (c *Controller) CreateManual(ctx context.Context, source SourceRef) (Attempt, error) {
	return c.create(ctx, source, AttemptInitiationManual, nil)
}

// CreateAutomatic returns created=false when admission declined. Automatic
// callers intentionally receive no interactive queue-full error.
func (c *Controller) CreateAutomatic(ctx context.Context, source SourceRef) (attempt Attempt, created bool, err error) {
	if c == nil || c.Store == nil || c.Evidence == nil {
		return Attempt{}, false, ErrControllerUnavailable
	}
	counts, err := c.Store.Counts(ctx)
	if err != nil {
		return Attempt{}, false, mapControllerError(err)
	}
	if counts.Queued+counts.Running >= MaxNonterminalAttempts {
		return Attempt{}, false, nil
	}
	attempt, err = c.create(ctx, source, AttemptInitiationAutomatic, nil)
	if errors.Is(err, ErrQueueFull) {
		return Attempt{}, false, nil
	}
	if err != nil {
		return Attempt{}, false, err
	}
	if attempt.Existing {
		return attempt, false, nil
	}
	return attempt, true, nil
}

func (c *Controller) Retry(ctx context.Context, attemptID int64) (Attempt, error) {
	if c == nil || c.Store == nil || c.Evidence == nil || attemptID <= 0 {
		return Attempt{}, ErrControllerUnavailable
	}
	previous, err := c.Store.Get(ctx, attemptID)
	if err != nil {
		if errors.Is(err, telemetry.ErrAttemptNotFound) {
			return Attempt{}, ErrAttemptNotFound
		}
		return Attempt{}, mapControllerError(err)
	}
	if previous.State != AttemptStateFailed && previous.State != AttemptStateInsufficientEvidence {
		return Attempt{}, ErrRetryNotAllowed
	}
	attempt, err := c.create(ctx, previous.Source, AttemptInitiationRetry, &attemptID)
	if errors.Is(err, ErrSourceNotFound) {
		return Attempt{}, ErrSourceNotFound
	}
	return attempt, err
}

func (c *Controller) History(ctx context.Context, source SourceRef) ([]Attempt, error) {
	if c == nil || c.Store == nil {
		return nil, ErrControllerUnavailable
	}
	exists, err := c.Store.SourceExists(ctx, source)
	if err != nil {
		return nil, mapControllerError(err)
	}
	if !exists {
		return nil, ErrSourceNotFound
	}
	attempts, err := c.Store.History(ctx, source)
	if err != nil {
		return nil, mapControllerError(err)
	}
	return attempts, nil
}

// Detail returns immutable host-free lifecycle, evidence, result, and provenance artifacts.
func (c *Controller) Detail(ctx context.Context, attemptID int64) (Attempt, EvidenceSnapshot, *Report, *Provenance, error) {
	if c == nil || c.Store == nil || attemptID <= 0 {
		return Attempt{}, EvidenceSnapshot{}, nil, nil, ErrControllerUnavailable
	}
	attempt, err := c.Store.Get(ctx, attemptID)
	if err != nil {
		return Attempt{}, EvidenceSnapshot{}, nil, nil, mapControllerError(err)
	}
	evidence, err := c.Store.LoadEvidence(ctx, attemptID)
	if err != nil {
		return Attempt{}, EvidenceSnapshot{}, nil, nil, mapControllerError(err)
	}
	result, provenance, err := c.Store.LoadResult(ctx, attemptID)
	if err != nil {
		return Attempt{}, EvidenceSnapshot{}, nil, nil, mapControllerError(err)
	}
	return attempt, evidence, result, provenance, nil
}

func (c *Controller) Status(ctx context.Context) (AttemptCounts, WorkerStatus, error) {
	if c == nil || c.Store == nil {
		return AttemptCounts{}, WorkerStatus{}, ErrControllerUnavailable
	}
	counts, err := c.Store.Counts(ctx)
	if err != nil {
		return AttemptCounts{}, WorkerStatus{}, err
	}
	if c.Worker != nil {
		return counts, c.Worker.Status(), nil
	}
	return counts, WorkerStatus{Workers: 1, MaxNonterminalAttempts: MaxNonterminalAttempts, RequestsPerMinute: 10, Burst: 2, RequestTimeoutSeconds: 30}, nil
}

func (c *Controller) LatestFailure(ctx context.Context) (*LatestFailure, error) {
	if c == nil || c.Store == nil {
		return nil, ErrControllerUnavailable
	}
	failure, err := c.Store.LatestFailure(ctx)
	if err != nil {
		return nil, mapControllerError(err)
	}
	return failure, nil
}

func (c *Controller) create(ctx context.Context, source SourceRef, initiation AttemptInitiation, retryOf *int64) (Attempt, error) {
	if c == nil || c.Store == nil || c.Evidence == nil || c.Readiness == nil {
		return Attempt{}, ErrControllerUnavailable
	}
	// Storage repeats this lookup transactionally with the admission checks.
	// The preflight avoids rebuilding evidence for an extant root.
	if initiation != AttemptInitiationRetry {
		attempts, err := c.Store.History(ctx, source)
		if err != nil {
			return Attempt{}, mapControllerError(err)
		}
		for _, existing := range attempts {
			if existing.RetryOfAttemptID != nil {
				continue
			}
			switch existing.State {
			case AttemptStateQueued, AttemptStateRunning:
				existing.Existing = true
				return existing, nil
			case AttemptStateCompleted:
				return Attempt{}, ErrSourceCompleted
			case AttemptStateFailed, AttemptStateInsufficientEvidence:
				return Attempt{}, ErrRetryRequired
			}
		}
	}
	// Preserve duplicate responses without a new admission, then validate the
	// reload-safe current configuration before any evidence is built.
	if err := c.Readiness.ValidateForSend(ctx); err != nil {
		return Attempt{}, ErrProviderNotReady
	}
	evidence, err := c.Evidence.Build(ctx, source)
	if err != nil {
		return Attempt{}, mapControllerError(err)
	}
	attempt, err := c.Store.Create(ctx, CreateAttempt{Source: source, Initiation: initiation, RetryOfAttemptID: retryOf, CreatedAtMS: c.now().UnixMilli(), Evidence: evidence})
	if err != nil {
		mapped := mapControllerError(err)
		if errors.Is(mapped, ErrAttemptNotFound) {
			return Attempt{}, ErrSourceNotFound
		}
		return Attempt{}, mapped
	}
	if c.Wake != nil {
		c.Wake()
	}
	return attempt, nil
}

func (c *Controller) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func mapControllerError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, telemetry.ErrQueueFull):
		return ErrQueueFull
	case errors.Is(err, telemetry.ErrAttemptLimitReached):
		return ErrAttemptLimitReached
	case errors.Is(err, telemetry.ErrSourceIneligible):
		return ErrSourceIneligible
	case errors.Is(err, telemetry.ErrSourceCompleted):
		return ErrSourceCompleted
	case errors.Is(err, telemetry.ErrRetryRequired):
		return ErrRetryRequired
	case errors.Is(err, telemetry.ErrAttemptNotFound):
		return ErrAttemptNotFound
	default:
		return err
	}
}
