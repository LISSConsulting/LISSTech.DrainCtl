//go:build windows

package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/investigation"
)

func TestInvestigationControllerErrorMapsProviderNotReady(t *testing.T) {
	writer := httptest.NewRecorder()
	investigationControllerError(writer, investigation.ErrProviderNotReady)

	if writer.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", writer.Code, http.StatusConflict)
	}
	var response investigation.ErrorResponse
	if err := json.NewDecoder(writer.Body).Decode(&response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if response.Error.Code != investigation.ErrorCodeProviderNotReady {
		t.Fatalf("error code = %q, want %q", response.Error.Code, investigation.ErrorCodeProviderNotReady)
	}
}

func TestInvestigationRequestValidationIsClosed(t *testing.T) {
	for _, rawQuery := range []string{
		"limit=01", "limit=+1", "limit=101", "after_attempt_number=0",
		"after_attempt_number=01", "limit=1&limit=2", "unknown=1",
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/investigation/sources/event_spike/1/attempts?"+rawQuery, nil)
		if _, _, ok := investigationHistoryQuery(request); ok {
			t.Fatalf("query %q accepted", rawQuery)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/investigation/sources/event_spike/1/attempts?limit=2&after_attempt_number=7", nil)
	limit, after, ok := investigationHistoryQuery(request)
	if !ok || limit != 2 || after != 7 {
		t.Fatalf("valid history query = (%d, %d, %v)", limit, after, ok)
	}
}

func TestInvestigationCreateRejectsNonJSONBeforeControllerLookup(t *testing.T) {
	ds := &DashboardServer{}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/investigation/sources/event_spike/1/attempts", nil)
	writer := httptest.NewRecorder()
	ds.handleCreateInvestigation(writer, request)
	if writer.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want %d", writer.Code, http.StatusUnsupportedMediaType)
	}
	var response investigation.ErrorResponse
	if err := json.NewDecoder(writer.Body).Decode(&response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if response.Error.Code != investigation.ErrorCodeInvalidContentType {
		t.Fatalf("error code = %q, want %q", response.Error.Code, investigation.ErrorCodeInvalidContentType)
	}
}

func TestFeatureTimestampUsesExactMilliseconds(t *testing.T) {
	if got, want := formatFeatureTimestamp(1), "1970-01-01T00:00:00.001Z"; got != want {
		t.Fatalf("timestamp = %q, want %q", got, want)
	}
}

func TestInvestigationStatusReflectsLatestFailure(t *testing.T) {
	ready := dc.InvestigationProviderSafeView{AccessEnabled: true, Acknowledged: true, HasCredential: true}
	if got := operationalInvestigationStateForFailure(ready, &investigation.LatestFailure{Reason: investigation.TerminalReasonAuthenticationFailed}); got != investigation.OperationalStateDegraded {
		t.Fatalf("authentication state = %q, want %q", got, investigation.OperationalStateDegraded)
	}
	if got := operationalInvestigationStateForFailure(ready, &investigation.LatestFailure{Reason: investigation.TerminalReasonTimeout}); got != investigation.OperationalStateFailing {
		t.Fatalf("timeout state = %q, want %q", got, investigation.OperationalStateFailing)
	}
	if got := operationalInvestigationStateForFailure(dc.InvestigationProviderSafeView{}, &investigation.LatestFailure{Reason: investigation.TerminalReasonTimeout}); got != investigation.OperationalStateDisabled {
		t.Fatalf("disabled state = %q, want %q", got, investigation.OperationalStateDisabled)
	}
}

func TestInvestigationHistoryPaginationIsStableAndAscending(t *testing.T) {
	attempts := []investigation.Attempt{{Number: 1}, {Number: 3}, {Number: 4}}
	page, next := investigationAttemptPage(attempts, 2, 0)
	if len(page) != 2 || page[0].Number != 1 || page[1].Number != 3 || next == nil || *next != 3 {
		t.Fatalf("first page = %#v, next=%v", page, next)
	}
	page, next = investigationAttemptPage(attempts, 2, 3)
	if len(page) != 1 || page[0].Number != 4 || next != nil {
		t.Fatalf("second page = %#v, next=%v", page, next)
	}
}
