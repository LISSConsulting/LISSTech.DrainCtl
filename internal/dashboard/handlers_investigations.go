//go:build windows

package dashboard

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/investigation"
)

type investigationFeatureReader interface {
	investigationController() *investigation.Controller
}

func (ds *DashboardServer) investigationReader() investigationFeatureReader {
	reader, _ := ds.featureRuntime.(investigationFeatureReader)
	return reader
}

func attemptSummary(attempt investigation.Attempt) investigation.AttemptSummary {
	result := investigation.AttemptSummary{AttemptID: strconv.FormatInt(attempt.ID, 10), AttemptNumber: attempt.Number, Initiation: attempt.Initiation, State: attempt.State, CreatedAt: formatFeatureTimestamp(attempt.CreatedAtMS), TerminalReason: attempt.TerminalReason, EvidenceVersion: investigation.EvidenceVersion, OmissionCodes: []investigation.OmissionCode{}}
	if attempt.RetryOfAttemptID != nil {
		value := strconv.FormatInt(*attempt.RetryOfAttemptID, 10)
		result.RetryOfAttemptID = &value
	}
	result.StartedAt = featureTimestamp(attempt.StartedAtMS)
	result.SendAuthorizedAt = featureTimestamp(attempt.SendAuthorizedAtMS)
	result.SendCompletedAt = featureTimestamp(attempt.SendCompletedAtMS)
	result.CompletedAt = featureTimestamp(attempt.CompletedAtMS)
	return result
}
func formatFeatureTimestamp(value int64) string {
	return time.UnixMilli(value).UTC().Format("2006-01-02T15:04:05.000Z")
}
func featureTimestamp(value *int64) *string {
	if value == nil {
		return nil
	}
	text := formatFeatureTimestamp(*value)
	return &text
}

func featureSource(r *http.Request) (investigation.SourceRef, bool) {
	id, ok := featurePositiveID(r.PathValue("source_id"))
	if !ok {
		return investigation.SourceRef{}, false
	}
	kind := investigation.SourceKind(r.PathValue("source_kind"))
	if kind != investigation.SourceKindEventSpike && kind != investigation.SourceKindSessionDrop {
		return investigation.SourceRef{}, false
	}
	return investigation.SourceRef{Kind: kind, ID: id}, true
}

func investigationError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code}})
}
func (ds *DashboardServer) controller() *investigation.Controller {
	if r := ds.investigationReader(); r != nil {
		return r.investigationController()
	}
	return nil
}

func (ds *DashboardServer) handleInvestigationHistory(w http.ResponseWriter, r *http.Request) {
	controller := ds.controller()
	if controller == nil {
		investigationError(w, 503, "attempt_store_unavailable")
		return
	}
	source, ok := featureSource(r)
	if !ok {
		investigationError(w, 400, "invalid_request")
		return
	}
	limit, after, ok := investigationHistoryQuery(r)
	if !ok {
		investigationError(w, 400, "invalid_request")
		return
	}
	attempts, err := controller.History(r.Context(), source)
	if err != nil {
		investigationControllerError(w, err)
		return
	}
	page, next := investigationAttemptPage(attempts, limit, after)
	items := make([]investigation.AttemptSummary, len(page))
	for i := range page {
		items[i] = attemptSummary(page[i])
		if _, evidence, _, _, err := controller.Detail(r.Context(), page[i].ID); err == nil {
			items[i].EvidenceVersion = evidence.Version
			items[i].OmissionCodes = evidence.OmissionCodes
			if items[i].OmissionCodes == nil {
				items[i].OmissionCodes = []investigation.OmissionCode{}
			}
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"source": investigation.SourceLink{SourceKind: source.Kind, SourceID: strconv.FormatInt(source.ID, 10)}, "attempts": items, "next_after_attempt_number": next})
}

func investigationAttemptPage(attempts []investigation.Attempt, limit, after int) ([]investigation.Attempt, *int) {
	start := 0
	for start < len(attempts) && attempts[start].Number <= after {
		start++
	}
	hasMore := len(attempts)-start > limit
	end := start + limit
	if end > len(attempts) {
		end = len(attempts)
	}
	page := attempts[start:end]
	if !hasMore {
		return page, nil
	}
	next := page[len(page)-1].Number
	return page, &next
}

func (ds *DashboardServer) handleCreateInvestigation(w http.ResponseWriter, r *http.Request) {
	if !isJSONContentType(r) {
		investigationError(w, http.StatusUnsupportedMediaType, "invalid_content_type")
		return
	}
	controller := ds.controller()
	if controller == nil {
		investigationError(w, 503, "attempt_store_unavailable")
		return
	}
	source, ok := featureSource(r)
	if !ok || !emptyJSONObject(r) {
		investigationError(w, 400, "invalid_request")
		return
	}
	attempt, err := controller.CreateManual(r.Context(), source)
	if err != nil {
		investigationControllerError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if attempt.Existing {
		_ = json.NewEncoder(w).Encode(attemptSummary(attempt))
		return
	}
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(attemptSummary(attempt))
}

func (ds *DashboardServer) handleRetryInvestigation(w http.ResponseWriter, r *http.Request) {
	if !isJSONContentType(r) {
		investigationError(w, http.StatusUnsupportedMediaType, "invalid_content_type")
		return
	}
	controller := ds.controller()
	if controller == nil {
		investigationError(w, 503, "attempt_store_unavailable")
		return
	}
	id, ok := featurePositiveID(r.PathValue("attempt_id"))
	if !ok || !emptyJSONObject(r) {
		investigationError(w, 400, "invalid_request")
		return
	}
	attempt, err := controller.Retry(r.Context(), id)
	if err != nil {
		investigationControllerError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(attemptSummary(attempt))
}

func isJSONContentType(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}
func emptyJSONObject(r *http.Request) bool {
	reader := io.LimitReader(r.Body, investigationSettingsMaxBytes+1)
	decoder := json.NewDecoder(reader)
	var raw json.RawMessage
	if decoder.Decode(&raw) != nil || len(raw) == 0 || decoder.Decode(&struct{}{}) != io.EOF {
		return false
	}
	var body map[string]json.RawMessage
	return json.Unmarshal(raw, &body) == nil && body != nil && len(body) == 0
}

func investigationHistoryQuery(r *http.Request) (int, int, bool) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0, 0, false
	}
	for key, items := range values {
		if (key != "limit" && key != "after_attempt_number") || len(items) != 1 {
			return 0, 0, false
		}
	}
	limit := 100
	if items, present := values["limit"]; present {
		value, ok := parseFeatureLimit(items[0], 100, 100)
		if !ok {
			return 0, 0, false
		}
		limit = value
	}
	after := 0
	if items, present := values["after_attempt_number"]; present {
		value, ok := featurePositiveID(items[0])
		if !ok || value > int64(^uint(0)>>1) {
			return 0, 0, false
		}
		after = int(value)
	}
	return limit, after, true
}
func investigationControllerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, investigation.ErrProviderNotReady):
		investigationError(w, http.StatusConflict, string(investigation.ErrorCodeProviderNotReady))
	case errors.Is(err, investigation.ErrQueueFull):
		investigationError(w, http.StatusTooManyRequests, string(investigation.ErrorCodeQueueFull))
	case errors.Is(err, investigation.ErrAttemptLimitReached):
		investigationError(w, http.StatusConflict, string(investigation.ErrorCodeAttemptLimitReached))
	case errors.Is(err, investigation.ErrSourceIneligible):
		investigationError(w, http.StatusConflict, string(investigation.ErrorCodeSourceIneligible))
	case errors.Is(err, investigation.ErrSourceCompleted):
		investigationError(w, http.StatusConflict, string(investigation.ErrorCodeSourceCompleted))
	case errors.Is(err, investigation.ErrRetryRequired):
		investigationError(w, http.StatusConflict, string(investigation.ErrorCodeRetryRequired))
	case errors.Is(err, investigation.ErrRetryNotAllowed):
		investigationError(w, http.StatusConflict, string(investigation.ErrorCodeRetryNotAllowed))
	case errors.Is(err, investigation.ErrAttemptNotFound):
		investigationError(w, http.StatusNotFound, string(investigation.ErrorCodeAttemptNotFound))
	case errors.Is(err, investigation.ErrSourceNotFound):
		investigationError(w, http.StatusNotFound, string(investigation.ErrorCodeSourceNotFound))
	default:
		investigationError(w, http.StatusServiceUnavailable, string(investigation.ErrorCodeAttemptStoreUnavailable))
	}
}

func (ds *DashboardServer) handleInvestigationDetail(w http.ResponseWriter, r *http.Request) {
	controller := ds.controller()
	if controller == nil {
		investigationError(w, 503, "attempt_store_unavailable")
		return
	}
	id, ok := featurePositiveID(r.PathValue("attempt_id"))
	if !ok {
		investigationError(w, 400, "invalid_request")
		return
	}
	attempt, evidence, result, provenance, err := controller.Detail(r.Context(), id)
	if err != nil {
		investigationControllerError(w, err)
		return
	}
	detail := struct {
		investigation.AttemptDetailSummary
		Evidence   investigation.EvidenceSummary `json:"evidence"`
		Result     *investigation.Report         `json:"result"`
		Provenance *investigation.ProvenanceDTO  `json:"provenance"`
	}{AttemptDetailSummary: investigation.AttemptDetailSummary{AttemptSummary: attemptSummary(attempt), Source: investigation.SourceLink{SourceKind: attempt.Source.Kind, SourceID: strconv.FormatInt(attempt.Source.ID, 10)}}, Evidence: investigation.EvidenceSummary{Version: evidence.Version, SnapshotKind: evidence.Kind, SnapshotAt: formatFeatureTimestamp(evidence.SnapshotAtMS), WindowStart: formatFeatureTimestamp(evidence.FromMS), WindowEnd: formatFeatureTimestamp(evidence.ToMS), FactIDs: evidence.FactIDs, OmissionCodes: evidence.OmissionCodes}, Result: result}
	if provenance != nil {
		detail.Provenance = &investigation.ProvenanceDTO{ProviderProfile: provenance.ProviderProfile, ProviderEndpoint: provenance.ProviderEndpoint, RequestedModel: provenance.RequestedModel, ResponseFormat: provenance.ResponseFormat, Store: provenance.Store, SendAuthorizedAt: formatFeatureTimestamp(provenance.SendAuthorizedAtMS), SendCompletedAt: formatFeatureTimestamp(provenance.SendCompletedAtMS), RequestHeaderBytes: provenance.RequestHeaderBytes, RequestBodyBytes: provenance.RequestBodyBytes, ResponseHeaderBytes: provenance.ResponseHeaderBytes, ResponseBodyBytes: provenance.ResponseBodyBytes, ValidationOutcome: provenance.ValidationOutcome}
	}
	if detail.Evidence.FactIDs == nil {
		detail.Evidence.FactIDs = []string{}
	}
	if detail.Evidence.OmissionCodes == nil {
		detail.Evidence.OmissionCodes = []investigation.OmissionCode{}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"attempt": detail})
}

func (ds *DashboardServer) handleInvestigationStatus(w http.ResponseWriter, r *http.Request) {
	controller := ds.controller()
	if controller == nil {
		investigationError(w, 503, "status_unavailable")
		return
	}
	counts, worker, err := controller.Status(r.Context())
	if err != nil {
		investigationError(w, 503, "status_unavailable")
		return
	}
	latestFailure, err := controller.LatestFailure(r.Context())
	if err != nil {
		investigationError(w, 503, "status_unavailable")
		return
	}
	cfg, err := dc.LoadConfig()
	if err != nil {
		investigationError(w, 503, "status_unavailable")
		return
	}
	settings, err := ds.investigationSettingsView(r.Context(), cfg)
	if err != nil {
		investigationError(w, 503, "status_unavailable")
		return
	}
	state := operationalInvestigationStateForFailure(settings.Provider, latestFailure)
	writeInvestigationJSON(w, http.StatusOK, investigation.Status{OperationalState: state, Provider: providerStatus(settings.Provider), AttemptCounts: counts, Worker: worker, LatestFailure: latestFailure})
}

func operationalInvestigationStateForFailure(provider dc.InvestigationProviderSafeView, latestFailure *investigation.LatestFailure) investigation.OperationalState {
	state := operationalInvestigationState(provider)
	if latestFailure == nil || state == investigation.OperationalStateDisabled || state == investigation.OperationalStateConfigured {
		return state
	}
	switch latestFailure.Reason {
	case investigation.TerminalReasonAuthenticationFailed, investigation.TerminalReasonConfigurationDisabled, investigation.TerminalReasonConfigurationInvalid:
		return investigation.OperationalStateDegraded
	default:
		return investigation.OperationalStateFailing
	}
}
