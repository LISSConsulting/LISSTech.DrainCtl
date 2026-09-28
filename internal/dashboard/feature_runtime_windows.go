//go:build windows

package dashboard

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/investigation"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondrop"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

type productionFeatureRuntime struct {
	db                   *telemetry.DB
	investigation        *investigation.Worker
	controller           *investigation.Controller
	attempts             investigation.Storage
	attemptStore         *telemetry.InvestigationAttemptStore
	detector             *sessiondrop.Detector
	inbox                *sessiondrop.TelemetryStore
	spikes               *telemetry.EventSpikeStore
	configuration        *investigation.ConfigAccessor
	publishInvestigation func(investigation.Update)
	publishSessionDrop   func(sessiondrop.SSEEvent)
	wake                 chan struct{}
	stop                 chan struct{}
	once                 sync.Once
	wg                   sync.WaitGroup
}

type productionEvidenceResolver struct {
	spikes  *telemetry.EventSpikeStore
	sources *sessiondrop.TelemetryStore
}

func (r productionEvidenceResolver) Build(ctx context.Context, source investigation.SourceRef) (investigation.EvidenceSnapshot, error) {
	now := time.Now().UTC().UnixMilli()
	omissions := []investigation.OmissionCode{investigation.OmissionFreshnessUnavailable, investigation.OmissionDrainUnavailable, investigation.OmissionLocalPoints, investigation.OmissionFleetPoints, investigation.OmissionLocalAggregate, investigation.OmissionFleetAggregate}
	input := investigation.EvidenceInput{SnapshotAtMS: now, Source: source, Freshness: investigation.FreshnessUnknown, Drain: investigation.DrainUnknown, Omissions: omissions}
	switch source.Kind {
	case investigation.SourceKindEventSpike:
		if r.spikes == nil {
			return investigation.EvidenceSnapshot{}, investigation.ErrSourceNotFound
		}
		spike, found, err := r.spikes.LookupByID(ctx, source.ID)
		if err != nil {
			return investigation.EvidenceSnapshot{}, err
		}
		if !found {
			return investigation.EvidenceSnapshot{}, investigation.ErrSourceNotFound
		}
		input.SourceTimeMS = spike.WindowEnd.UTC().UnixMilli()
		input.Detector = investigation.DetectorConfirmed
		input.EventSpike = &investigation.EventSpikeEvidence{Channel: investigation.Channel("unknown"), WindowFromMS: spike.WindowStart.UTC().UnixMilli(), WindowToMS: spike.WindowEnd.UTC().UnixMilli(), ObservedCount: int64(spike.Observed), ExpectedCountMilli: int64(spike.Expected * 1000), TailProbabilityPPB: int64(spike.TailProbability * 1_000_000_000)}
	case investigation.SourceKindSessionDrop:
		if r.sources == nil {
			return investigation.EvidenceSnapshot{}, investigation.ErrSourceNotFound
		}
		drop, err := r.sources.Source(ctx, source.ID)
		if err != nil {
			return investigation.EvidenceSnapshot{}, err
		}
		if drop == nil {
			return investigation.EvidenceSnapshot{}, investigation.ErrSourceNotFound
		}
		input.SourceTimeMS = drop.ConfirmationEndedAtMS
		input.Detector = investigation.DetectorState(drop.Classification)
		input.SessionDrop = &investigation.SessionDropEvidence{ObservedCount: int64(drop.ObservedTotalSessions), ReferenceCount: int64(drop.ReferenceTotalSessions), ExpectedCountMilli: int64(drop.ExpectedTotalSessions * 1000), AbsoluteLoss: int64(drop.AbsoluteLossSessions), RelativeLossBPS: int64(drop.RelativeLoss * 10000), TailProbabilityPPB: int64(drop.TailProbability * 1_000_000_000), Classification: investigation.DetectorState(drop.Classification)}
	default:
		return investigation.EvidenceSnapshot{}, investigation.ErrSourceNotFound
	}
	return investigation.BuildEvidence(input, nil)
}

func NewFeatureRuntime(db *telemetry.DB, settings sessiondrop.Settings) FeatureRuntime {
	if db == nil {
		return &productionFeatureRuntime{}
	}
	attemptStore := telemetry.NewInvestigationAttemptStore(db)
	attempts := investigation.NewTelemetryStorage(attemptStore)
	inbox := sessiondrop.NewTelemetryStore(telemetry.NewSessionDropStore(db))
	configuration := newFeatureConfigAccessor(db)
	worker := &investigation.Worker{
		Store:         attempts,
		Client:        investigation.NewProviderClient(),
		Configuration: configuration,
		CutoffMS:      func() int64 { return featureAuditCutoff() },
	}
	runtime := &productionFeatureRuntime{db: db, investigation: worker, attempts: attempts, attemptStore: attemptStore, detector: sessiondrop.NewDetector(inbox, settings), inbox: inbox, spikes: telemetry.NewEventSpikeStore(db), configuration: configuration, wake: make(chan struct{}, 1), stop: make(chan struct{})}
	runtime.controller = &investigation.Controller{Store: attempts, Evidence: productionEvidenceResolver{spikes: runtime.spikes, sources: inbox}, Readiness: configuration, Wake: runtime.WakeSessionDropInbox, Worker: worker}
	return runtime
}

func newFeatureConfigAccessor(db *telemetry.DB) *investigation.ConfigAccessor {
	return investigation.NewConfigAccessor(func(ctx context.Context) (investigation.ConfigSnapshot, error) {
		cfg, err := dc.LoadConfig()
		if err != nil {
			return investigation.ConfigSnapshot{}, err
		}
		provider := cfg.InvestigationProvider
		acknowledged := provider.PrivacyAcknowledgementVersion == dc.PrivacyAcknowledgementVersion && provider.PrivacyAcknowledgementAuditID > 0
		if acknowledged && db != nil {
			acknowledgements := telemetry.NewPrivacyAcknowledgementStore(db)
			acknowledged, err = acknowledgements.IsCurrentReference(ctx, provider.PrivacyAcknowledgementAuditID, dc.PrivacyAcknowledgementVersion, telemetry.PrivacyAcknowledgementClauseSetHash())
			if err != nil {
				return investigation.ConfigSnapshot{}, err
			}
		}
		return investigation.ConfigSnapshot{AccessEnabled: provider.AccessEnabled, AutomaticEnabled: provider.AutomaticEnabled, PrivacyAcknowledgementVersion: provider.PrivacyAcknowledgementVersion, AcknowledgementReferenceCurrent: acknowledged, CredentialCiphertext: provider.CredentialCiphertext, AuditDays: cfg.Retention.AuditDays, CurrentAcknowledgementAuditID: cfg.CurrentInvestigationPrivacyAcknowledgementAuditID()}, nil
	}, dc.DecryptInvestigationCredential)
}

func featureAuditCutoff() int64 {
	cfg, err := dc.LoadConfig()
	if err != nil || cfg.Retention.AuditDays <= 0 {
		return time.Now().UTC().UnixMilli()
	}
	return time.Now().UTC().Add(-time.Duration(cfg.Retention.AuditDays) * 24 * time.Hour).UnixMilli()
}

func (r *productionFeatureRuntime) Recover(ctx context.Context) error {
	if r.investigation == nil {
		return errors.New("dashboard: feature runtime requires telemetry")
	}
	_, err := r.investigation.Recover(ctx)
	return err
}

func (r *productionFeatureRuntime) DrainInbox(ctx context.Context) error {
	if r.inbox == nil || r.detector == nil {
		return nil
	}
	for {
		var created *sessiondrop.Source
		drained, err := r.inbox.ConsumeNextTransactional(ctx, func(store sessiondrop.Store, observation sessiondrop.Observation) error {
			detector := sessiondrop.NewDetector(store, r.detector.Settings())
			source, err := detector.Process(ctx, observation)
			if err != nil {
				return err
			}
			created = source
			return nil
		})
		if err != nil || !drained {
			return err
		}
		if created == nil {
			continue
		}
		if r.publishSessionDrop != nil {
			r.publishSessionDrop(sessiondrop.SSEEvent{SchemaVersion: 1, SourceKind: sessiondrop.SourceKind, SourceID: strconv.FormatInt(created.ID, 10), ConfirmedAt: sessiondrop.FormatRFC3339Milliseconds(created.ConfirmationEndedAtMS), Classification: created.Classification, InvestigationEligible: created.InvestigationEligible, ConfirmationCount: created.ConfirmationCount})
		}
		if !created.InvestigationEligible || r.controller == nil || r.configuration == nil {
			continue
		}
		ready, err := r.configuration.AutomaticReady(ctx)
		if err != nil || !ready {
			continue
		}
		attempt, admitted, err := r.controller.CreateAutomatic(ctx, investigation.SourceRef{Kind: investigation.SourceKindSessionDrop, ID: created.ID})
		if err != nil {
			return err
		}
		if admitted && r.publishInvestigation != nil {
			r.publishInvestigation(investigation.Update{Kind: "attempt", Attempt: investigationAttemptUpdate(attempt)})
		}
	}
}

func (r *productionFeatureRuntime) PruneExpiredQueued(ctx context.Context) error {
	if r.attemptStore == nil {
		return nil
	}
	cfg, err := dc.LoadConfig()
	if err != nil {
		return err
	}
	_, err = r.attemptStore.PruneExpiredQueued(ctx, time.Now().UTC().Add(-time.Duration(cfg.Retention.AuditDays)*24*time.Hour))
	return err
}

func (r *productionFeatureRuntime) runWorkerPass(ctx context.Context) error {
	if err := r.Recover(ctx); err != nil {
		return err
	}
	if _, err := r.investigation.RunOne(ctx); err != nil {
		return err
	}
	return r.DrainInbox(ctx)
}

func (r *productionFeatureRuntime) StartWorker(ctx context.Context) error {
	if r.investigation == nil {
		return errors.New("dashboard: feature runtime requires telemetry")
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			// Recovery runs between, never during, RunOne calls. It only
			// terminalizes rows after their durable no-resend lease expires.
			if err := r.runWorkerPass(ctx); err != nil && ctx.Err() != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-r.stop:
				return
			case <-r.wake:
			case <-ticker.C:
			}
		}
	}()
	return nil
}

func (r *productionFeatureRuntime) WakeSessionDropInbox() {
	if r.wake != nil {
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
}
func (r *productionFeatureRuntime) Stop() {
	r.once.Do(func() {
		if r.stop != nil {
			close(r.stop)
		}
		r.wg.Wait()
	})
}
func (r *productionFeatureRuntime) investigationController() *investigation.Controller {
	return r.controller
}
func (r *productionFeatureRuntime) featureDatabase() *telemetry.DB { return r.db }
func (r *productionFeatureRuntime) featureSessionDrops(ctx context.Context, limit int, before int64) ([]sessiondrop.Source, error) {
	if r.inbox == nil {
		return nil, errors.New("session-drop store unavailable")
	}
	return r.inbox.ListSources(ctx, limit, before)
}

func (r *productionFeatureRuntime) bindFeaturePublishers(investigationPublisher func(investigation.Update), sessionDropPublisher func(sessiondrop.SSEEvent)) {
	r.publishInvestigation = investigationPublisher
	r.publishSessionDrop = sessionDropPublisher
}

func investigationAttemptUpdate(attempt investigation.Attempt) *investigation.AttemptUpdate {
	var retryOf *string
	if attempt.RetryOfAttemptID != nil {
		value := strconv.FormatInt(*attempt.RetryOfAttemptID, 10)
		retryOf = &value
	}
	return &investigation.AttemptUpdate{
		AttemptID:        strconv.FormatInt(attempt.ID, 10),
		Source:           investigation.SourceLink{SourceKind: attempt.Source.Kind, SourceID: strconv.FormatInt(attempt.Source.ID, 10)},
		AttemptNumber:    attempt.Number,
		Initiation:       attempt.Initiation,
		RetryOfAttemptID: retryOf,
		State:            attempt.State,
		TerminalReason:   attempt.TerminalReason,
		UpdatedAt:        formatFeatureTimestamp(attempt.QueuedAtMS),
	}
}
func (r *productionFeatureRuntime) featureSessionDrop(ctx context.Context, id int64) (*sessiondrop.Source, error) {
	if r.inbox == nil {
		return nil, errors.New("session-drop store unavailable")
	}
	return r.inbox.Source(ctx, id)
}
