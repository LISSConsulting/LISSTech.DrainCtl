//go:build windows

// Package investigation owns the central, host-free investigation lifecycle.
package investigation

type ProviderProfileName string

type ResponseFormatName string

const (
	ProviderProfile               ProviderProfileName = "openai_responses"
	ProviderEndpoint                                  = "https://api.openai.com/v1/responses"
	ProviderModel                                     = "gpt-6-astra"
	ResponseFormat                ResponseFormatName  = "anomaly_investigation_v1"
	EvidenceVersion                                   = 1
	ResultVersion                                     = 1
	MaxNonterminalAttempts                            = 100
	MaxAttemptsPerSource                              = 100
	SendLeaseMilliseconds         int64               = 30_000
	FinalizationLeaseMilliseconds int64               = 120_000
)

// SourceKind identifies the deterministic source without copying its host.
type SourceKind string

const (
	SourceKindEventSpike  SourceKind = "event_spike"
	SourceKindSessionDrop SourceKind = "session_drop"
)

// SourceRef is the internal integer-backed source link used in durable rows.
type SourceRef struct {
	Kind SourceKind
	ID   int64
}

// SourceLink is the REST/SSE source link. IDs are decimal strings by contract.
type SourceLink struct {
	SourceKind SourceKind `json:"source_kind"`
	SourceID   string     `json:"source_id"`
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

// TerminalReason is intentionally a closed local enum. It never carries provider text.
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

type SnapshotKind string

const (
	SnapshotKindAvailable   SnapshotKind = "available"
	SnapshotKindUnavailable SnapshotKind = "unavailable"
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

// ErrorCode is the closed safe API error vocabulary for investigation routes.
// It is never populated from storage, parser, transport, or provider text.
type ErrorCode string

const (
	ErrorCodeSessionExpired          ErrorCode = "session_expired"
	ErrorCodeAccessDenied            ErrorCode = "access_denied"
	ErrorCodeInvalidContentType      ErrorCode = "invalid_content_type"
	ErrorCodeInvalidRequest          ErrorCode = "invalid_request"
	ErrorCodeMethodNotAllowed        ErrorCode = "method_not_allowed"
	ErrorCodeRateLimited             ErrorCode = "rate_limited"
	ErrorCodeInvalidSettings         ErrorCode = "invalid_settings"
	ErrorCodeSettingsUnavailable     ErrorCode = "settings_unavailable"
	ErrorCodeStatusUnavailable       ErrorCode = "status_unavailable"
	ErrorCodeSourceNotFound          ErrorCode = "source_not_found"
	ErrorCodeSourceIneligible        ErrorCode = "source_ineligible"
	ErrorCodeProviderNotReady        ErrorCode = "provider_not_ready"
	ErrorCodeSourceCompleted         ErrorCode = "source_completed"
	ErrorCodeRetryRequired           ErrorCode = "retry_required"
	ErrorCodeAttemptLimitReached     ErrorCode = "attempt_limit_reached"
	ErrorCodeQueueFull               ErrorCode = "queue_full"
	ErrorCodeAttemptStoreUnavailable ErrorCode = "attempt_store_unavailable"
	ErrorCodeAttemptNotFound         ErrorCode = "attempt_not_found"
	ErrorCodeRetryNotAllowed         ErrorCode = "retry_not_allowed"
)

type ErrorResponse struct {
	Error struct {
		Code ErrorCode `json:"code"`
	} `json:"error"`
}

// Attempt is the durable internal lifecycle form. All timestamps are UTC Unix milliseconds.
type Attempt struct {
	ID                           int64
	Source                       SourceRef
	Number                       int
	Initiation                   AttemptInitiation
	RetryOfAttemptID             *int64
	State                        AttemptState
	CreatedAtMS                  int64
	QueuedAtMS                   int64
	StartedAtMS                  *int64
	SendAuthorizedAtMS           *int64
	SendCompletedAtMS            *int64
	SendLeaseExpiresAtMS         *int64
	FinalizationLeaseExpiresAtMS *int64
	CompletedAtMS                *int64
	TerminalReason               TerminalReason
	EvidenceHash                 [32]byte
	Existing                     bool
}

// AttemptSummary is the host-free REST projection. Timestamp strings are RFC3339 UTC.
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

// EvidenceSnapshot is the persisted internal form. CanonicalJSON is either validated
// EvidenceV1 or exactly {} for a local unavailable snapshot.
type EvidenceSnapshot struct {
	AttemptID     int64
	Version       int
	Kind          SnapshotKind
	SourceTimeMS  int64
	SnapshotAtMS  int64
	FromMS        int64
	ToMS          int64
	CanonicalJSON []byte
	FactIDs       []string
	OmissionCodes []OmissionCode
}

// EvidenceSummary intentionally exposes no evidence content.
type EvidenceSummary struct {
	Version       int            `json:"version"`
	SnapshotKind  SnapshotKind   `json:"snapshot_kind"`
	SnapshotAt    string         `json:"snapshot_at"`
	WindowStart   string         `json:"window_start"`
	WindowEnd     string         `json:"window_end"`
	FactIDs       []string       `json:"fact_ids"`
	OmissionCodes []OmissionCode `json:"omission_codes"`
}

type OverallAssessment string

const (
	OverallAssessmentInsufficientEvidence         OverallAssessment = "insufficient_evidence"
	OverallAssessmentIndeterminate                OverallAssessment = "indeterminate"
	OverallAssessmentLocalizedOperationalIssue    OverallAssessment = "likely_localized_operational_issue"
	OverallAssessmentFleetWideOperationalIssue    OverallAssessment = "likely_fleet_wide_operational_issue"
	OverallAssessmentExpectedOrMaintenanceRelated OverallAssessment = "likely_expected_or_maintenance_related"
)

type EvidenceSufficiency string

const (
	EvidenceSufficiencyInsufficient EvidenceSufficiency = "insufficient"
	EvidenceSufficiencyPartial      EvidenceSufficiency = "partial"
	EvidenceSufficiencySufficient   EvidenceSufficiency = "sufficient"
)

type Confidence string

const (
	ConfidenceLow    Confidence = "low"
	ConfidenceMedium Confidence = "medium"
	ConfidenceHigh   Confidence = "high"
)

type MissingEvidenceCategory string

const (
	MissingEvidenceAdditionalTimeSeries     MissingEvidenceCategory = "additional_time_series"
	MissingEvidenceHostHealthDetail         MissingEvidenceCategory = "host_health_detail"
	MissingEvidenceServiceState             MissingEvidenceCategory = "service_state"
	MissingEvidenceAuthenticationDetail     MissingEvidenceCategory = "authentication_detail"
	MissingEvidenceNetworkDependencyDetail  MissingEvidenceCategory = "network_dependency_detail"
	MissingEvidenceChangeMaintenanceContext MissingEvidenceCategory = "change_or_maintenance_context"
	MissingEvidenceFleetComparison          MissingEvidenceCategory = "fleet_comparison"
	MissingEvidenceOther                    MissingEvidenceCategory = "other"
)

type DiagnosticCheckType string

const (
	DiagnosticCheckInspectRetainedMetrics         DiagnosticCheckType = "inspect_retained_metrics"
	DiagnosticCheckVerifyServiceState             DiagnosticCheckType = "verify_service_state"
	DiagnosticCheckVerifyAuthenticationState      DiagnosticCheckType = "verify_authentication_state"
	DiagnosticCheckVerifyNetworkOrDependency      DiagnosticCheckType = "verify_network_or_dependency"
	DiagnosticCheckVerifyChangeMaintenanceContext DiagnosticCheckType = "verify_change_or_maintenance_context"
	DiagnosticCheckCompareFleet                   DiagnosticCheckType = "compare_fleet"
	DiagnosticCheckCollectAdditionalObservation   DiagnosticCheckType = "collect_additional_observation"
)

type TextKind string

const (
	TextKindSummary         TextKind = "untrusted_summary"
	TextKindHypothesis      TextKind = "untrusted_hypothesis"
	TextKindMissingEvidence TextKind = "untrusted_missing_evidence"
	TextKindDiagnosticCheck TextKind = "untrusted_diagnostic_check"
)

type ReportText struct {
	TextKind TextKind `json:"text_kind"`
	Text     string   `json:"text"`
	FactIDs  []string `json:"fact_ids"`
}

type Hypothesis struct {
	Rank                 int        `json:"rank"`
	Confidence           Confidence `json:"confidence"`
	TextKind             TextKind   `json:"text_kind"`
	Text                 string     `json:"text"`
	SupportingFactIDs    []string   `json:"supporting_fact_ids"`
	ContradictingFactIDs []string   `json:"contradicting_fact_ids"`
}

type MissingEvidence struct {
	Category       MissingEvidenceCategory `json:"category"`
	TextKind       TextKind                `json:"text_kind"`
	Text           string                  `json:"text"`
	RelatedFactIDs []string                `json:"related_fact_ids"`
}

type RecommendedDiagnosticCheck struct {
	Rank                   int                 `json:"rank"`
	CheckType              DiagnosticCheckType `json:"check_type"`
	TextKind               TextKind            `json:"text_kind"`
	Text                   string              `json:"text"`
	FactIDs                []string            `json:"fact_ids"`
	RelatedHypothesisRanks []int               `json:"related_hypothesis_ranks"`
}

// Report is the complete normalized anomaly_investigation_v1 projection.
type Report struct {
	ResultVersion               int                          `json:"result_version"`
	Summary                     ReportText                   `json:"summary"`
	OverallAssessment           OverallAssessment            `json:"overall_assessment"`
	EvidenceSufficiency         EvidenceSufficiency          `json:"evidence_sufficiency"`
	HumanReviewRequired         bool                         `json:"human_review_required"`
	Hypotheses                  []Hypothesis                 `json:"hypotheses"`
	MissingEvidence             []MissingEvidence            `json:"missing_evidence"`
	RecommendedDiagnosticChecks []RecommendedDiagnosticCheck `json:"recommended_diagnostic_checks"`
}

type ValidationOutcome string

const (
	ValidationOutcomeAccepted             ValidationOutcome = "accepted"
	ValidationOutcomeInsufficientEvidence ValidationOutcome = "insufficient_evidence"
)

// Provenance is the durable closed local form. It exposes no provider material.
type Provenance struct {
	AttemptID           int64
	ProviderProfile     ProviderProfileName
	ProviderEndpoint    string
	RequestedModel      string
	ResponseFormat      ResponseFormatName
	Store               bool
	SendAuthorizedAtMS  int64
	SendCompletedAtMS   int64
	RequestHeaderBytes  int
	RequestBodyBytes    int
	ResponseHeaderBytes int
	ResponseBodyBytes   int
	ValidationOutcome   ValidationOutcome
}

// ProvenanceDTO is the REST form. Only these canonical RFC3339 timestamp names exist.
type ProvenanceDTO struct {
	ProviderProfile     ProviderProfileName `json:"provider_profile"`
	ProviderEndpoint    string              `json:"provider_endpoint"`
	RequestedModel      string              `json:"requested_model"`
	ResponseFormat      ResponseFormatName  `json:"response_format"`
	Store               bool                `json:"store"`
	SendAuthorizedAt    string              `json:"send_authorized_at"`
	SendCompletedAt     string              `json:"send_completed_at"`
	RequestHeaderBytes  int                 `json:"request_header_bytes"`
	RequestBodyBytes    int                 `json:"request_body_bytes"`
	ResponseHeaderBytes int                 `json:"response_header_bytes"`
	ResponseBodyBytes   int                 `json:"response_body_bytes"`
	ValidationOutcome   ValidationOutcome   `json:"validation_outcome"`
}

type AttemptDetailSummary struct {
	AttemptSummary
	Source SourceLink `json:"source"`
}

type AttemptDetail struct {
	Attempt    AttemptDetailSummary `json:"attempt"`
	Evidence   EvidenceSummary      `json:"evidence"`
	Result     *Report              `json:"result"`
	Provenance *ProvenanceDTO       `json:"provenance"`
}

type AttemptCounts struct {
	Queued               int `json:"queued"`
	Running              int `json:"running"`
	Completed            int `json:"completed"`
	InsufficientEvidence int `json:"insufficient_evidence"`
	Failed               int `json:"failed"`
}

type OperationalState string

const (
	OperationalStateDisabled         OperationalState = "disabled"
	OperationalStateConfigured       OperationalState = "configured"
	OperationalStateReady            OperationalState = "ready"
	OperationalStateAutomaticEnabled OperationalState = "automatic_enabled"
	OperationalStateDegraded         OperationalState = "degraded"
	OperationalStateFailing          OperationalState = "failing"
)

type LatestFailure struct {
	Reason TerminalReason `json:"reason"`
	At     string         `json:"at"`
}

type WorkerStatus struct {
	Workers                int `json:"workers"`
	MaxNonterminalAttempts int `json:"max_nonterminal_attempts"`
	RequestsPerMinute      int `json:"requests_per_minute"`
	Burst                  int `json:"burst"`
	RequestTimeoutSeconds  int `json:"request_timeout_seconds"`
}

type ProviderStatus struct {
	Profile                       string `json:"profile"`
	Endpoint                      string `json:"endpoint"`
	Model                         string `json:"model"`
	AccessEnabled                 bool   `json:"access_enabled"`
	Acknowledged                  bool   `json:"acknowledged"`
	PrivacyAcknowledgementVersion string `json:"privacy_acknowledgement_version"`
	AutomaticEnabled              bool   `json:"automatic_enabled"`
	HasCredential                 bool   `json:"has_credential"`
}

type Status struct {
	OperationalState OperationalState `json:"operational_state"`
	Provider         ProviderStatus   `json:"provider"`
	AttemptCounts    AttemptCounts    `json:"attempt_counts"`
	Worker           WorkerStatus     `json:"worker"`
	LatestFailure    *LatestFailure   `json:"latest_failure"`
}

type AttemptUpdate struct {
	AttemptID        string            `json:"attempt_id"`
	Source           SourceLink        `json:"source"`
	AttemptNumber    int               `json:"attempt_number"`
	Initiation       AttemptInitiation `json:"initiation"`
	RetryOfAttemptID *string           `json:"retry_of_attempt_id"`
	State            AttemptState      `json:"state"`
	TerminalReason   TerminalReason    `json:"terminal_reason"`
	UpdatedAt        string            `json:"updated_at"`
}

type Update struct {
	Kind    string         `json:"kind"`
	Attempt *AttemptUpdate `json:"attempt,omitempty"`
	Status  *Status        `json:"status,omitempty"`
}
