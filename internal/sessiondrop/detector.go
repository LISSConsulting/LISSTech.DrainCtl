//go:build windows

package sessiondrop

import (
	"context"
	"fmt"
	"time"
)

// Detector is deliberately host-local. Its only shared ordering input is the
// durable acceptance sequence supplied by the inbox; it never pools baselines
// or confirmation windows between hosts.
type Detector struct {
	store    Store
	settings Settings
}

func NewDetector(store Store, settings Settings) *Detector {
	return &Detector{store: store, settings: settings}
}

// Settings returns the immutable detector configuration used for every
// transaction-bound processing instance.
func (d *Detector) Settings() Settings {
	return d.settings
}

// Process applies one already accepted observation. Callers drain the durable
// inbox in accepted-sequence order; this method itself remains deterministic
// when replaying the same observation after a restart.
func (d *Detector) Process(ctx context.Context, observation Observation) (*Source, error) {
	if d.store == nil {
		return nil, fmt.Errorf("sessiondrop: nil store")
	}
	state, err := d.store.DetectorState(ctx, observation.CanonicalHost)
	if err != nil {
		return nil, err
	}
	if state == nil {
		state = &DetectorState{CanonicalHost: observation.CanonicalHost}
	}
	state.CanonicalHost = observation.CanonicalHost
	state.StateUpdatedAtMS = observation.AcceptedAtMS

	if gap := validateObservation(*state, observation); gap != GapReasonNone {
		state.LastGapReason = gap
		state.Confirmation = nil
		if err := d.store.SaveDetectorState(ctx, *state); err != nil {
			return nil, err
		}
		return nil, nil
	}

	state.LastScoredReportEpochMS = observation.ReportEpochMS
	state.LastGapReason = GapReasonNone
	total := *observation.TotalSessions // present validation guarantees this.

	if observation.Drain == DrainContextOverlap {
		state.PostDrainRemaining = ConfirmationWindow
	}
	drain := observation.Drain
	if drain == DrainContextNone && state.PostDrainRemaining > 0 {
		drain = DrainContextPostHorizon
		state.PostDrainRemaining--
	}
	classification := observation.ClassificationContext
	if drain == DrainContextOverlap || drain == DrainContextPostHorizon {
		classification = ClassificationDrainAssociated
	} else if drain == DrainContextUnknown || classification != ClassificationUnexplained {
		drain = DrainContextUnknown
		classification = ClassificationUnknownContext
	}

	slot := SlotForObservation(observation.ReportEpochMS, observation.LocalOffsetMinutes)
	slotBaseline, err := d.store.Baseline(ctx, observation.CanonicalHost, BaselineScopeSlot, &slot)
	if err != nil {
		return nil, err
	}
	fallback, err := d.store.Baseline(ctx, observation.CanonicalHost, BaselineScopeAllHours, nil)
	if err != nil {
		return nil, err
	}
	selection, slotDays := selectBaseline(slotBaseline, fallback, observation.AcceptedAtMS, d.settings)

	candidate := false
	var expected, probability float64
	if selection.baseline != nil && state.LastReferenceTotal != nil && *state.LastReferenceTotal > 0 {
		expected = selection.baseline.Alpha / selection.baseline.Beta
		probability = LowerTailProbability(selection.baseline.Alpha, selection.baseline.Beta, total)
		loss := *state.LastReferenceTotal - total
		relative := float64(loss) / float64(*state.LastReferenceTotal)
		candidate = classification != ClassificationNotScored && probability < d.settings.LowerTailThreshold &&
			loss >= d.settings.MinimumDropSessions && relative >= d.settings.MinimumDropPercent/100
	}

	// A candidate, every drain/context observation, and every confirmed horizon
	// are excluded from training. This prevents a drop from lowering its own
	// baseline, including while cooldown suppresses a duplicate source.
	eligibleNormal := !candidate && drain == DrainContextNone && classification == ClassificationUnexplained
	if eligibleNormal {
		if err := d.train(ctx, observation, slot, slotBaseline, fallback); err != nil {
			return nil, err
		}
		state.LastReferenceTotal = new(total)
	}

	if candidate {
		state.Confirmation = appendWindow(state.Confirmation, ConfirmationObservation{
			ReportEpochMS: observation.ReportEpochMS, Candidate: true, DrainContext: drain, AcceptedAtMS: observation.AcceptedAtMS,
		})
	} else if len(state.Confirmation) != 0 && selection.baseline != nil {
		state.Confirmation = appendWindow(state.Confirmation, ConfirmationObservation{
			ReportEpochMS: observation.ReportEpochMS, Candidate: false, DrainContext: drain, AcceptedAtMS: observation.AcceptedAtMS,
		})
	} else if selection.baseline == nil {
		state.Confirmation = nil
	}

	var source *Source
	if confirmationCount(state.Confirmation) >= ConfirmationRequired {
		if observation.AcceptedAtMS >= state.CooldownUntilMS {
			created, err := d.confirm(ctx, observation, *state, selection, slotDays, total, expected, probability)
			if err != nil {
				return nil, err
			}
			source = created
			state.CooldownUntilMS = observation.AcceptedAtMS + int64(time.Duration(d.settings.CooldownMinutes)*time.Minute/time.Millisecond)
		}
		state.Confirmation = nil
	}
	if err := d.store.SaveDetectorState(ctx, *state); err != nil {
		return nil, err
	}
	return source, nil
}

type baselineSelection struct {
	baseline *Baseline
	scope    BaselineScope
	slot     *int
}

func selectBaseline(slot, fallback *Baseline, atMS int64, settings Settings) (baselineSelection, int) {
	if slotReady(slot) {
		decayed := decayedBaseline(*slot, atMS, settings.BaselineHalfLifeHours)
		return baselineSelection{baseline: &decayed, scope: BaselineScopeSlot, slot: slot.SlotIndex}, len(slot.TrainedLocalDates)
	}
	if fallbackReady(fallback) {
		decayed := decayedBaseline(*fallback, atMS, settings.BaselineHalfLifeHours)
		return baselineSelection{baseline: &decayed, scope: BaselineScopeAllHours}, 0
	}
	return baselineSelection{}, 0
}

func (d *Detector) train(ctx context.Context, observation Observation, slot int, slotBaseline, fallback *Baseline) error {
	if slotBaseline == nil {
		slotBaseline = new(newBaseline(observation.CanonicalHost, BaselineScopeSlot, new(slot), observation.AcceptedAtMS))
	}
	if fallback == nil {
		fallback = new(newBaseline(observation.CanonicalHost, BaselineScopeAllHours, nil, observation.AcceptedAtMS))
	}
	total := *observation.TotalSessions
	if err := d.store.UpsertBaseline(ctx, trainBaseline(*slotBaseline, total, observation.LocalDate, observation.AcceptedAtMS, d.settings)); err != nil {
		return err
	}
	return d.store.UpsertBaseline(ctx, trainBaseline(*fallback, total, "", observation.AcceptedAtMS, d.settings))
}

func (d *Detector) confirm(ctx context.Context, observation Observation, state DetectorState, selection baselineSelection, slotDays, total int, expected, probability float64) (*Source, error) {
	window := append([]ConfirmationObservation(nil), state.Confirmation...)
	if len(window) > ConfirmationWindow {
		window = window[len(window)-ConfirmationWindow:]
	}
	flags := make([]bool, len(window))
	count := 0
	for i := range window {
		flags[i] = window[i].Candidate
		if flags[i] {
			count++
		}
	}
	if len(window) < ConfirmationRequired {
		return nil, nil
	}
	first := window[0]
	reference := 0
	if state.LastReferenceTotal != nil {
		reference = *state.LastReferenceTotal
	}
	loss := max(reference-total, 0)
	relative := 0.0
	if reference > 0 {
		relative = float64(loss) / float64(reference)
	}
	classification, drain := confirmationClassification(window)
	eligible := classification == ClassificationUnexplained
	source := Source{
		RegisteredHost: observation.CanonicalHost, ReportEpochMS: observation.ReportEpochMS, AcceptedAtMS: observation.AcceptedAtMS,
		LocalOffsetMinutes: observation.LocalOffsetMinutes, LocalDate: observation.LocalDate, DetectedAtMS: observation.AcceptedAtMS,
		ConfirmationStartedReportEpochMS: first.ReportEpochMS, ConfirmationEndedReportEpochMS: observation.ReportEpochMS,
		ConfirmationStartedAtMS: first.AcceptedAtMS, ConfirmationEndedAtMS: observation.AcceptedAtMS,
		ObservedTotalSessions: total, ReferenceTotalSessions: reference, ExpectedTotalSessions: expected,
		AbsoluteLossSessions: loss, RelativeLoss: relative, TailProbability: probability,
		BaselineModelVersion: BaselineModelVersion, BaselineScope: selection.scope, SlotIndex: selection.slot,
		ConfirmationFlags: flags, ConfirmationCount: count, FreshnessContext: FreshnessFresh, DrainContext: drain,
		Classification: classification, InvestigationEligible: eligible,
	}
	if selection.scope == BaselineScopeSlot {
		source.SlotMatureDays = new(slotDays)
	}
	inserted, created, err := d.store.InsertSource(ctx, source)
	if err != nil {
		return nil, err
	}
	if !created {
		return nil, nil
	}
	return &inserted, nil
}

func validateObservation(state DetectorState, observation Observation) GapReason {
	if observation.ReportEpochMS <= 0 {
		return GapReasonMissingReportEpoch
	}
	if observation.Freshness == FreshnessStale {
		return GapReasonStale
	}
	if observation.Freshness != FreshnessFresh {
		return GapReasonFreshnessUnknown
	}
	if observation.SessionPresence == SessionPresenceNil {
		return GapReasonNilEnumeration
	}
	if observation.SessionPresence != SessionPresencePresent || observation.ActiveSessions == nil || observation.DisconnectedSessions == nil || observation.TotalSessions == nil || *observation.ActiveSessions < 0 || *observation.DisconnectedSessions < 0 || *observation.TotalSessions != *observation.ActiveSessions+*observation.DisconnectedSessions {
		return GapReasonInvalidEnumeration
	}
	if observation.ReportEpochMS == state.LastScoredReportEpochMS && state.LastScoredReportEpochMS != 0 {
		return GapReasonDuplicateReportEpoch
	}
	if observation.ReportEpochMS < state.LastScoredReportEpochMS {
		return GapReasonOutOfOrder
	}
	return GapReasonNone
}

func appendWindow(window []ConfirmationObservation, observation ConfirmationObservation) []ConfirmationObservation {
	window = append(window, observation)
	if len(window) > ConfirmationWindow {
		copy(window, window[len(window)-ConfirmationWindow:])
		window = window[:ConfirmationWindow]
	}
	return window
}

func confirmationCount(window []ConfirmationObservation) int {
	count := 0
	for _, observation := range window {
		if observation.Candidate {
			count++
		}
	}
	return count
}

func confirmationClassification(window []ConfirmationObservation) (Classification, DrainContext) {
	drain := DrainContextNone
	unknown := false
	for _, observation := range window {
		switch observation.DrainContext {
		case DrainContextOverlap, DrainContextPostHorizon:
			drain = observation.DrainContext
		case DrainContextUnknown:
			unknown = true
		}
	}
	if drain != DrainContextNone {
		return ClassificationDrainAssociated, drain
	}
	if unknown {
		return ClassificationUnknownContext, DrainContextUnknown
	}
	return ClassificationUnexplained, DrainContextNone
}
