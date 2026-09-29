//go:build windows

package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/investigation"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

const investigationSettingsMaxBytes = 16 << 10

type featureDatabaseProvider interface{ featureDatabase() *telemetry.DB }

func (ds *DashboardServer) investigationDatabase() *telemetry.DB {
	provider, _ := ds.featureRuntime.(featureDatabaseProvider)
	if provider == nil {
		return nil
	}
	return provider.featureDatabase()
}

func (ds *DashboardServer) investigationAcknowledgements() *telemetry.PrivacyAcknowledgementStore {
	db := ds.investigationDatabase()
	if db == nil {
		return nil
	}
	return telemetry.NewPrivacyAcknowledgementStore(db)
}

type investigationSessionDropView struct {
	dc.SessionDropConfig
	Detector investigationDetectorConstants `json:"detector"`
}
type investigationDetectorConstants struct {
	SlotsPerDay                 int `json:"slots_per_day"`
	ConfirmationRequired        int `json:"confirmation_required"`
	ConfirmationWindow          int `json:"confirmation_window"`
	SlotMaturityEligibleDays    int `json:"slot_maturity_eligible_days"`
	FallbackMinimumObservations int `json:"fallback_minimum_observations"`
	FallbackMinimumSpanHours    int `json:"fallback_minimum_span_hours"`
}
type investigationSettingsView struct {
	Provider    dc.InvestigationProviderSafeView `json:"provider"`
	SessionDrop investigationSessionDropView     `json:"session_drop"`
}

func fixedInvestigationDetector() investigationDetectorConstants {
	return investigationDetectorConstants{SlotsPerDay: 96, ConfirmationRequired: 2, ConfirmationWindow: 3, SlotMaturityEligibleDays: 7, FallbackMinimumObservations: 20, FallbackMinimumSpanHours: 24}
}

func (ds *DashboardServer) investigationSettingsView(ctx context.Context, cfg *dc.Config) (investigationSettingsView, error) {
	acks := ds.investigationAcknowledgements()
	if acks == nil {
		return investigationSettingsView{}, errors.New("missing acknowledgement store")
	}
	provider := cfg.InvestigationProvider
	current, err := acks.IsCurrentReference(ctx, cfg.CurrentInvestigationPrivacyAcknowledgementAuditID(), provider.PrivacyAcknowledgementVersion, telemetry.PrivacyAcknowledgementClauseSetHash())
	if err != nil {
		return investigationSettingsView{}, err
	}
	return investigationSettingsView{Provider: cfg.InvestigationProviderSafeView(current), SessionDrop: investigationSessionDropView{SessionDropConfig: cfg.SessionDrop, Detector: fixedInvestigationDetector()}}, nil
}

func providerStatus(view dc.InvestigationProviderSafeView) investigation.ProviderStatus {
	return investigation.ProviderStatus{
		Profile: view.Profile, Endpoint: view.Endpoint, Model: view.Model,
		AccessEnabled: view.AccessEnabled, Acknowledged: view.Acknowledged,
		PrivacyAcknowledgementVersion: view.PrivacyAcknowledgementVersion,
		AutomaticEnabled:              view.AutomaticEnabled, HasCredential: view.HasCredential,
	}
}

func operationalInvestigationState(provider dc.InvestigationProviderSafeView) investigation.OperationalState {
	if !provider.AccessEnabled {
		return investigation.OperationalStateDisabled
	}
	if !provider.Acknowledged || !provider.HasCredential {
		return investigation.OperationalStateConfigured
	}
	if provider.AutomaticEnabled {
		return investigation.OperationalStateAutomaticEnabled
	}
	return investigation.OperationalStateReady
}

func (ds *DashboardServer) handleGetInvestigationSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := dc.LoadConfig()
	if err != nil {
		investigationError(w, http.StatusServiceUnavailable, "settings_unavailable")
		return
	}
	view, err := ds.investigationSettingsView(r.Context(), cfg)
	if err != nil {
		investigationError(w, http.StatusServiceUnavailable, "settings_unavailable")
		return
	}
	writeInvestigationJSON(w, http.StatusOK, view)
}

type investigationSettingsRequest struct {
	Provider    investigationProviderRequest `json:"provider"`
	SessionDrop dc.SessionDropConfig         `json:"session_drop"`
}
type investigationProviderRequest struct {
	AccessEnabled          bool                              `json:"access_enabled"`
	PrivacyAcknowledgement *dc.PrivacyAcknowledgement        `json:"privacy_acknowledgement,omitempty"`
	AutomaticEnabled       bool                              `json:"automatic_enabled"`
	Credential             dc.InvestigationCredentialCommand `json:"credential"`
}

func decodeInvestigationSettings(r *http.Request) (investigationSettingsRequest, error) {
	var request investigationSettingsRequest
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, investigationSettingsMaxBytes))
	if err != nil {
		return request, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return request, err
	}
	if !closedMembers(raw, "provider", "session_drop") {
		return request, errors.New("invalid settings members")
	}
	var provider map[string]json.RawMessage
	if err := json.Unmarshal(raw["provider"], &provider); err != nil || !closedMembers(provider, "access_enabled", "automatic_enabled", "credential") && !closedMembers(provider, "access_enabled", "privacy_acknowledgement", "automatic_enabled", "credential") {
		return request, errors.New("invalid provider members")
	}
	if acknowledgement, ok := provider["privacy_acknowledgement"]; ok && string(acknowledgement) == "null" {
		return request, errors.New("null acknowledgement")
	}
	if hasNull(provider) {
		return request, errors.New("null provider member")
	}
	var credential map[string]json.RawMessage
	if err := json.Unmarshal(provider["credential"], &credential); err != nil || !closedMembers(credential, "operation") && !closedMembers(credential, "operation", "value") || hasNull(credential) {
		return request, errors.New("invalid credential members")
	}
	var sessionDrop map[string]json.RawMessage
	if err := json.Unmarshal(raw["session_drop"], &sessionDrop); err != nil || !closedMembers(sessionDrop, "lower_tail_threshold", "minimum_drop_sessions", "minimum_drop_percent", "baseline_half_life_hours", "cooldown_minutes") || hasNull(sessionDrop) {
		return request, errors.New("invalid session-drop members")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, err
	}
	return request, nil
}

func closedMembers(m map[string]json.RawMessage, names ...string) bool {
	if len(m) != len(names) {
		return false
	}
	for _, name := range names {
		if _, ok := m[name]; !ok {
			return false
		}
	}
	return true
}

func hasNull(m map[string]json.RawMessage) bool {
	for _, value := range m {
		if string(value) == "null" {
			return true
		}
	}
	return false
}

func (ds *DashboardServer) handlePutInvestigationSettings(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]), "application/json") {
		investigationError(w, http.StatusUnsupportedMediaType, "invalid_content_type")
		return
	}
	request, err := decodeInvestigationSettings(r)
	if err != nil {
		investigationError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if err := dc.ValidateSessionDropConfig(request.SessionDrop); err != nil {
		investigationError(w, http.StatusUnprocessableEntity, "invalid_settings")
		return
	}
	if request.Provider.Credential.Operation != "preserve" && request.Provider.Credential.Operation != "replace" && request.Provider.Credential.Operation != "clear" {
		investigationError(w, http.StatusUnprocessableEntity, "invalid_settings")
		return
	}
	if request.Provider.Credential.Operation == "replace" {
		if request.Provider.Credential.Value == "" || len(request.Provider.Credential.Value) > 4096 {
			investigationError(w, http.StatusUnprocessableEntity, "invalid_settings")
			return
		}
	} else if request.Provider.Credential.Value != "" {
		investigationError(w, http.StatusUnprocessableEntity, "invalid_settings")
		return
	}
	if (request.Provider.AccessEnabled || request.Provider.AutomaticEnabled) && request.Provider.PrivacyAcknowledgement == nil {
		investigationError(w, http.StatusUnprocessableEntity, "invalid_settings")
		return
	}
	if request.Provider.PrivacyAcknowledgement != nil && !request.Provider.PrivacyAcknowledgement.Valid() {
		investigationError(w, http.StatusUnprocessableEntity, "invalid_settings")
		return
	}
	if request.Provider.AutomaticEnabled && !request.Provider.AccessEnabled {
		investigationError(w, http.StatusUnprocessableEntity, "invalid_settings")
		return
	}
	current, err := dc.LoadConfig()
	if err != nil {
		investigationError(w, http.StatusServiceUnavailable, "settings_unavailable")
		return
	}
	if (request.Provider.AccessEnabled || request.Provider.AutomaticEnabled) &&
		(request.Provider.Credential.Operation == "clear" ||
			request.Provider.Credential.Operation == "preserve" && current.InvestigationProvider.CredentialCiphertext == "") {
		investigationError(w, http.StatusUnprocessableEntity, "invalid_settings")
		return
	}

	var auditID int64
	if acknowledgement := request.Provider.PrivacyAcknowledgement; acknowledgement != nil {
		auth := GetAuthInfo(r)
		if auth == nil || strings.TrimSpace(auth.Username) == "" {
			investigationError(w, http.StatusServiceUnavailable, "settings_unavailable")
			return
		}
		acks := ds.investigationAcknowledgements()
		if acks == nil {
			investigationError(w, http.StatusServiceUnavailable, "settings_unavailable")
			return
		}
		ack, err := acks.Append(r.Context(), auth.Username, ds.clock().UTC(), telemetry.PrivacyAcknowledgementClauseSetHash())
		if err != nil {
			investigationError(w, http.StatusServiceUnavailable, "settings_unavailable")
			return
		}
		auditID = ack.ID
	}
	update := dc.InvestigationSettingsUpdate{AccessEnabled: request.Provider.AccessEnabled, AutomaticEnabled: request.Provider.AutomaticEnabled, PrivacyAcknowledgement: request.Provider.PrivacyAcknowledgement, PrivacyAcknowledgementAuditID: auditID, Credential: request.Provider.Credential, SessionDrop: request.SessionDrop}
	if err := dc.UpdateInvestigationSettings(update); err != nil {
		investigationError(w, http.StatusServiceUnavailable, "settings_unavailable")
		return
	}
	cfg, err := dc.LoadConfig()
	if err != nil {
		investigationError(w, http.StatusServiceUnavailable, "settings_unavailable")
		return
	}
	view, err := ds.investigationSettingsView(r.Context(), cfg)
	if err != nil {
		investigationError(w, http.StatusServiceUnavailable, "settings_unavailable")
		return
	}
	writeInvestigationJSON(w, http.StatusOK, view)
}

func writeInvestigationJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
