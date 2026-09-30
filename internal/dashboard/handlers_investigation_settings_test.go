//go:build windows

package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondrop"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

const validInvestigationSettingsJSON = `{"provider":{"access_enabled":false,"automatic_enabled":false,"credential":{"operation":"preserve"}},"session_drop":{"lower_tail_threshold":0.0001,"minimum_drop_sessions":3,"minimum_drop_percent":30,"baseline_half_life_hours":168,"cooldown_minutes":60}}`

func TestDecodeInvestigationSettingsRequiresClosedFullReplacement(t *testing.T) {
	r := httptest.NewRequest(http.MethodPut, "/api/v1/investigation/settings", strings.NewReader(validInvestigationSettingsJSON))
	request, err := decodeInvestigationSettings(r)
	if err != nil {
		t.Fatalf("decode valid request: %v", err)
	}
	if request.Provider.Credential.Operation != "preserve" || request.SessionDrop.CooldownMinutes != 60 {
		t.Fatalf("decoded request = %#v", request)
	}
	for _, body := range []string{
		`{}`,
		`{"provider":{"access_enabled":false,"automatic_enabled":false,"credential":{"operation":"preserve"},"unexpected":true},"session_drop":{"lower_tail_threshold":0.0001,"minimum_drop_sessions":3,"minimum_drop_percent":30,"baseline_half_life_hours":168,"cooldown_minutes":60}}`,
		`{"provider":{"access_enabled":false,"automatic_enabled":false,"credential":{"operation":"preserve"}},"session_drop":{"lower_tail_threshold":0.0001,"minimum_drop_sessions":3,"minimum_drop_percent":30,"baseline_half_life_hours":168}}`,
		`{"provider":{"access_enabled":false,"privacy_acknowledgement":null,"automatic_enabled":false,"credential":{"operation":"preserve"}},"session_drop":{"lower_tail_threshold":0.0001,"minimum_drop_sessions":3,"minimum_drop_percent":30,"baseline_half_life_hours":168,"cooldown_minutes":60}}`,
	} {
		r := httptest.NewRequest(http.MethodPut, "/api/v1/investigation/settings", strings.NewReader(body))
		if _, err := decodeInvestigationSettings(r); err == nil {
			t.Fatalf("accepted invalid closed replacement: %s", body)
		}
	}
}

func TestInvestigationSettingsRoutesAreRegisteredAndSessionProtected(t *testing.T) {
	ds := newTestServer(t)
	sessionContext, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ds.sessionStore = NewSessionStore(sessionContext)
	mux := http.NewServeMux()
	registerRoutes(context.Background(), ds, mux)

	for _, method := range []string{http.MethodGet, http.MethodPut} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(method, "/api/v1/investigation/settings", nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s settings route status = %d, want registered session protection (%d)", method, response.Code, http.StatusUnauthorized)
		}
	}
}

func TestInvestigationSettingsViewDerivesReadyStatusFromExactAuditReference(t *testing.T) {
	ctx := context.Background()
	db, err := telemetry.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open telemetry database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := dc.DefaultConfig()
	runtime, ok := NewFeatureRuntime(db, sessiondrop.Settings{
		LowerTailThreshold:    cfg.SessionDrop.LowerTailThreshold,
		MinimumDropSessions:   cfg.SessionDrop.MinimumDropSessions,
		MinimumDropPercent:    cfg.SessionDrop.MinimumDropPercent,
		BaselineHalfLifeHours: cfg.SessionDrop.BaselineHalfLifeHours,
		CooldownMinutes:       cfg.SessionDrop.CooldownMinutes,
	}).(*productionFeatureRuntime)
	if !ok {
		t.Fatal("feature runtime does not expose production implementation")
	}
	ds := &DashboardServer{featureRuntime: runtime}
	acks := telemetry.NewPrivacyAcknowledgementStore(db)
	ack, err := acks.Append(ctx, "operator", time.Unix(1, 0).UTC(), telemetry.PrivacyAcknowledgementClauseSetHash())
	if err != nil {
		t.Fatalf("append acknowledgement: %v", err)
	}
	cfg.InvestigationProvider = dc.InvestigationProviderConfig{
		AccessEnabled: true, AutomaticEnabled: true, CredentialCiphertext: "protected-ciphertext",
		PrivacyAcknowledgementVersion: dc.PrivacyAcknowledgementVersion, PrivacyAcknowledgementAuditID: ack.ID,
	}

	view, err := ds.investigationSettingsView(ctx, cfg)
	if err != nil {
		t.Fatalf("build settings view: %v", err)
	}
	status := providerStatus(view.Provider)
	if !status.AccessEnabled || !status.Acknowledged || !status.AutomaticEnabled || !status.HasCredential {
		t.Fatalf("provider status from current audit/config = %#v", status)
	}
	body, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal safe view: %v", err)
	}
	if strings.Contains(string(body), "protected-ciphertext") || strings.Contains(string(body), "privacy_acknowledgement_audit_id") {
		t.Fatalf("settings view disclosed private provider configuration: %s", body)
	}

	cfg.InvestigationProvider.PrivacyAcknowledgementAuditID++
	staleView, err := ds.investigationSettingsView(ctx, cfg)
	if err != nil {
		t.Fatalf("build stale-audit settings view: %v", err)
	}
	staleStatus := providerStatus(staleView.Provider)
	if staleStatus.Acknowledged || staleStatus.AutomaticEnabled {
		t.Fatalf("provider status accepted a non-current audit reference: %#v", staleStatus)
	}
}
