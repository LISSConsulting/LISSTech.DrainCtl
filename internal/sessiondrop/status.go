//go:build windows

// Package sessiondrop owns central deterministic lower-tail detection types.
package sessiondrop

const (
	BaselineModelVersion        = "gamma_poisson_lower_v1"
	SourceKind                  = "session_drop"
	SlotsPerDay                 = 96
	ConfirmationRequired        = 2
	ConfirmationWindow          = 3
	SlotMaturityDays            = 7
	FallbackMinimumObservations = 20
	FallbackMinimumSpanHours    = 24
)

type SessionPresence string

const (
	SessionPresencePresent     SessionPresence = "present"
	SessionPresenceNil         SessionPresence = "nil"
	SessionPresenceInvalid     SessionPresence = "invalid"
	SessionPresenceUnavailable SessionPresence = "unavailable"
)

type FreshnessContext string

const (
	FreshnessFresh   FreshnessContext = "fresh"
	FreshnessStale   FreshnessContext = "stale"
	FreshnessUnknown FreshnessContext = "unknown"
)

type DrainContext string

const (
	DrainContextNone        DrainContext = "none"
	DrainContextOverlap     DrainContext = "overlap"
	DrainContextPostHorizon DrainContext = "post_horizon"
	DrainContextUnknown     DrainContext = "unknown"
)

type Classification string

const (
	ClassificationUnexplained     Classification = "unexplained"
	ClassificationDrainAssociated Classification = "drain_associated"
	ClassificationUnknownContext  Classification = "unknown_context"
	ClassificationNotScored       Classification = "not_scored"
)

type GapReason string

const (
	GapReasonNone                 GapReason = ""
	GapReasonNilEnumeration       GapReason = "nil_enumeration"
	GapReasonInvalidEnumeration   GapReason = "invalid_enumeration"
	GapReasonMissingReportEpoch   GapReason = "missing_report_epoch"
	GapReasonDuplicateReportEpoch GapReason = "duplicate_report_epoch"
	GapReasonOutOfOrder           GapReason = "out_of_order"
	GapReasonStale                GapReason = "stale"
	GapReasonFreshnessUnknown     GapReason = "freshness_unknown"
)

type BaselineScope string

const (
	BaselineScopeSlot     BaselineScope = "slot"
	BaselineScopeAllHours BaselineScope = "all_hours"
)

// ErrorCode is the closed safe API error vocabulary for session-drop routes.
type ErrorCode string

const (
	ErrorCodeSessionExpired               ErrorCode = "session_expired"
	ErrorCodeAccessDenied                 ErrorCode = "access_denied"
	ErrorCodeInvalidRequest               ErrorCode = "invalid_request"
	ErrorCodeSourceNotFound               ErrorCode = "source_not_found"
	ErrorCodeSessionDropListUnavailable   ErrorCode = "session_drop_list_unavailable"
	ErrorCodeSessionDropDetailUnavailable ErrorCode = "session_drop_detail_unavailable"
	ErrorCodeAttemptLimitReached          ErrorCode = "attempt_limit_reached"
)

type ErrorResponse struct {
	Error struct {
		Code ErrorCode `json:"code"`
	} `json:"error"`
}

// Settings are the five configurable central detector controls.
type Settings struct {
	LowerTailThreshold    float64
	MinimumDropSessions   int
	MinimumDropPercent    float64
	BaselineHalfLifeHours int
	CooldownMinutes       int
}

func DefaultSettings() Settings {
	return Settings{
		LowerTailThreshold:    0.0001,
		MinimumDropSessions:   3,
		MinimumDropPercent:    30,
		BaselineHalfLifeHours: 168,
		CooldownMinutes:       60,
	}
}

// BaselineReadiness is a derived host-local readiness view. All-hours span is
// computed only from normal-training timestamps.
type BaselineReadiness struct {
	SlotMatureDays                int
	SlotReady                     bool
	FallbackObservationCount      int
	FallbackFirstTrainedAtMS      *int64
	FallbackLastNormalTrainedAtMS *int64
	FallbackReady                 bool
}

// Status is the host-free central detector status used by authorized status
// projections. It contains no registered-host identity or source data.
type Status struct {
	PendingObservations int64
}

// Observation is the durable, host-keyed inbox input. AcceptedSequence is the
// sole ordering key; AcceptedAtMS is evidence/freshness time, never ordering.
type Observation struct {
	AcceptedSequence      int64
	CanonicalHost         string
	ReportEpochMS         int64
	AcceptedAtMS          int64
	LocalOffsetMinutes    int
	LocalDate             string
	SessionPresence       SessionPresence
	ActiveSessions        *int
	DisconnectedSessions  *int
	TotalSessions         *int
	Freshness             FreshnessContext
	Drain                 DrainContext
	ClassificationContext Classification
}

// Baseline is persisted separately for every host slot and all-hours fallback.
type Baseline struct {
	ID                    int64
	CanonicalHost         string
	Scope                 BaselineScope
	SlotIndex             *int
	ModelVersion          string
	Alpha                 float64
	Beta                  float64
	ObservationCount      int
	FirstTrainedAtMS      *int64
	LastNormalTrainedAtMS *int64
	LastUpdatedAtMS       int64
	TrainedLocalDates     []string
}

// DetectorState is the host-keyed persisted state. Confirmation positions are
// chronological and empty only after a gap or completed confirmation.
type DetectorState struct {
	CanonicalHost           string
	LastScoredReportEpochMS int64
	LastReferenceTotal      *int
	CooldownUntilMS         int64
	PostDrainRemaining      int
	Confirmation            []ConfirmationObservation
	LastGapReason           GapReason
	StateUpdatedAtMS        int64
}

type ConfirmationObservation struct {
	ReportEpochMS int64
	Candidate     bool
	DrainContext  DrainContext
	AcceptedAtMS  int64
}

// Source is the durable internal session-drop anomaly form. It contains the
// registered host only because it is a deterministic source row, never an
// investigation/provider/SSE projection.
type Source struct {
	ID                               int64
	RegisteredHost                   string
	ReportEpochMS                    int64
	AcceptedAtMS                     int64
	LocalOffsetMinutes               int
	LocalDate                        string
	DetectedAtMS                     int64
	ConfirmationStartedReportEpochMS int64
	ConfirmationEndedReportEpochMS   int64
	ConfirmationStartedAtMS          int64
	ConfirmationEndedAtMS            int64
	ObservedTotalSessions            int
	ReferenceTotalSessions           int
	ExpectedTotalSessions            float64
	AbsoluteLossSessions             int
	RelativeLoss                     float64
	TailProbability                  float64
	BaselineModelVersion             string
	BaselineScope                    BaselineScope
	SlotIndex                        *int
	SlotMatureDays                   *int
	ConfirmationFlags                []bool
	ConfirmationCount                int
	FreshnessContext                 FreshnessContext
	DrainContext                     DrainContext
	Classification                   Classification
	InvestigationEligible            bool
}

// SourceSummary is the authorized REST list shape and deliberately has no attempts.
type SourceSummary struct {
	ID                     string         `json:"id"`
	SourceKind             string         `json:"source_kind"`
	RegisteredHost         string         `json:"registered_host"`
	ConfirmedAt            string         `json:"confirmed_at"`
	Classification         Classification `json:"classification"`
	InvestigationEligible  bool           `json:"investigation_eligible"`
	ObservedTotalSessions  int            `json:"observed_total_sessions"`
	ReferenceTotalSessions int            `json:"reference_total_sessions"`
	ExpectedTotalSessions  float64        `json:"expected_total_sessions"`
	AbsoluteLossSessions   int            `json:"absolute_loss_sessions"`
	RelativeLoss           float64        `json:"relative_loss"`
	TailProbability        float64        `json:"tail_probability"`
	BaselineModelVersion   string         `json:"baseline_model_version"`
	BaselineScope          BaselineScope  `json:"baseline_scope"`
	SlotIndex              *int           `json:"slot_index"`
	SlotMatureDays         *int           `json:"slot_mature_days"`
	ConfirmationWindowSize int            `json:"confirmation_window_size"`
	ConfirmationCount      int            `json:"confirmation_count"`
}

// SourceDetail exposes the two persisted central acceptance timestamps as
// RFC3339 values; report-epoch values remain explicit decimal strings.
type SourceDetail struct {
	SourceSummary
	ConfirmationStartedAt          string           `json:"confirmation_started_at"`
	ConfirmationEndedAt            string           `json:"confirmation_ended_at"`
	ConfirmationFlags              []bool           `json:"confirmation_flags"`
	FreshnessContext               FreshnessContext `json:"freshness_context"`
	DrainContext                   DrainContext     `json:"drain_context"`
	ConfirmationEndedReportEpochMS string           `json:"confirmation_ended_report_epoch_ms"`
}

type AttemptInitiation string

const (
	AttemptInitiationAutomatic AttemptInitiation = "automatic"
	AttemptInitiationManual    AttemptInitiation = "manual"
	AttemptInitiationRetry     AttemptInitiation = "retry"
)

type AttemptState string

const (
	AttemptStateQueued               AttemptState = "queued"
	AttemptStateRunning              AttemptState = "running"
	AttemptStateCompleted            AttemptState = "completed"
	AttemptStateInsufficientEvidence AttemptState = "insufficient_evidence"
	AttemptStateFailed               AttemptState = "failed"
)

type TerminalReason string

const (
	TerminalReasonNone                    TerminalReason = ""
	TerminalReasonAuthenticationFailed    TerminalReason = "authentication_failed"
	TerminalReasonConfigurationDisabled   TerminalReason = "configuration_disabled"
	TerminalReasonConfigurationInvalid    TerminalReason = "configuration_invalid"
	TerminalReasonEvidenceUnavailable     TerminalReason = "evidence_unavailable"
	TerminalReasonInterrupted             TerminalReason = "interrupted"
	TerminalReasonNetworkError            TerminalReason = "network_error"
	TerminalReasonProviderRateLimited     TerminalReason = "provider_rate_limited"
	TerminalReasonProviderRequestRejected TerminalReason = "provider_request_rejected"
	TerminalReasonProviderRefused         TerminalReason = "provider_refused"
	TerminalReasonRedirectRefused         TerminalReason = "redirect_refused"
	TerminalReasonRequestLimit            TerminalReason = "request_limit"
	TerminalReasonResponseIncomplete      TerminalReason = "response_incomplete"
	TerminalReasonResponseInvalid         TerminalReason = "response_invalid"
	TerminalReasonResponseLimit           TerminalReason = "response_limit"
	TerminalReasonStorageUnavailable      TerminalReason = "storage_unavailable"
	TerminalReasonTimeout                 TerminalReason = "timeout"
	TerminalReasonUpstreamError           TerminalReason = "upstream_error"
)

type OmissionCode string

const (
	OmissionPreUpgradeContext    OmissionCode = "pre_upgrade_context"
	OmissionRetentionExpired     OmissionCode = "retention_expired"
	OmissionFreshnessUnavailable OmissionCode = "freshness_unavailable"
	OmissionDrainUnavailable     OmissionCode = "drain_unavailable"
	OmissionDetectorUnavailable  OmissionCode = "detector_unavailable"
	OmissionLocalPoints          OmissionCode = "local_points"
	OmissionFleetPoints          OmissionCode = "fleet_points"
	OmissionLocalAggregate       OmissionCode = "local_aggregate"
	OmissionFleetAggregate       OmissionCode = "fleet_aggregate"
)

// AttemptSummary is duplicated only as the session-drop detail contract's
// host-free immutable attempt projection; it prevents this package importing
// the provider-owning investigation package.
type AttemptSummary struct {
	AttemptID        string            `json:"attempt_id"`
	AttemptNumber    int               `json:"attempt_number"`
	Initiation       AttemptInitiation `json:"initiation"`
	RetryOfAttemptID *string           `json:"retry_of_attempt_id"`
	State            AttemptState      `json:"state"`
	CreatedAt        string            `json:"created_at"`
	StartedAt        *string           `json:"started_at"`
	SendAuthorizedAt *string           `json:"send_authorized_at"`
	SendCompletedAt  *string           `json:"send_completed_at"`
	CompletedAt      *string           `json:"completed_at"`
	TerminalReason   TerminalReason    `json:"terminal_reason"`
	EvidenceVersion  int               `json:"evidence_version"`
	OmissionCodes    []OmissionCode    `json:"omission_codes"`
}

type DetailResponse struct {
	Source   SourceDetail     `json:"source"`
	Attempts []AttemptSummary `json:"attempts"`
}

type ListResponse struct {
	Items      []SourceSummary `json:"items"`
	NextBefore *string         `json:"next_before"`
}

// SSEEvent is intentionally source-ID-only and contains no host, counts, or timestamps.
type SSEEvent struct {
	SchemaVersion         int            `json:"schema_version"`
	SourceKind            string         `json:"source_kind"`
	SourceID              string         `json:"source_id"`
	ConfirmedAt           string         `json:"confirmed_at"`
	Classification        Classification `json:"classification"`
	InvestigationEligible bool           `json:"investigation_eligible"`
	ConfirmationCount     int            `json:"confirmation_count"`
}
