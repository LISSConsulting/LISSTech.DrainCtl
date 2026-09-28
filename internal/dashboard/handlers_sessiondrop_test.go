//go:build windows

package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondrop"
)

type sessionDropListTestRuntime struct{ sources []sessiondrop.Source }

func (r sessionDropListTestRuntime) featureSessionDrops(context.Context, int, int64) ([]sessiondrop.Source, error) {
	return r.sources, nil
}
func (sessionDropListTestRuntime) featureSessionDrop(context.Context, int64) (*sessiondrop.Source, error) {
	return nil, nil
}
func (sessionDropListTestRuntime) Recover(context.Context) error            { return nil }
func (sessionDropListTestRuntime) DrainInbox(context.Context) error         { return nil }
func (sessionDropListTestRuntime) PruneExpiredQueued(context.Context) error { return nil }
func (sessionDropListTestRuntime) StartWorker(context.Context) error        { return nil }
func (sessionDropListTestRuntime) Stop()                                    {}
func (sessionDropListTestRuntime) WakeSessionDropInbox()                    {}

func TestSessionDropListQueryIsClosedAndCanonical(t *testing.T) {
	for _, rawQuery := range []string{
		"limit=01", "limit=+1", "limit=201", "before=0", "before=01",
		"before=+1", "limit=1&limit=2", "unknown=1",
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/session-drops?"+rawQuery, nil)
		if _, _, ok := sessionDropListQuery(request); ok {
			t.Fatalf("query %q accepted", rawQuery)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/session-drops?limit=2&before=7", nil)
	limit, before, ok := sessionDropListQuery(request)
	if !ok || limit != 2 || before != 7 {
		t.Fatalf("valid list query = (%d, %d, %v)", limit, before, ok)
	}
}

func TestSessionDropListUsesFinalReturnedItemAsCursor(t *testing.T) {
	ds := &DashboardServer{featureRuntime: sessionDropListTestRuntime{sources: []sessiondrop.Source{{ID: 9}, {ID: 8}, {ID: 7}}}}
	writer := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/session-drops?limit=2", nil)
	ds.handleSessionDropList(writer, request)
	if got := writer.Body.String(); !strings.Contains(got, `"next_before":"8"`) {
		t.Fatalf("response %s does not contain final returned cursor", got)
	}
}
