//go:build windows

package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/investigation"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondrop"
)

type sessionDropFeatureReader interface {
	featureSessionDrops(context.Context, int, int64) ([]sessiondrop.Source, error)
	featureSessionDrop(context.Context, int64) (*sessiondrop.Source, error)
}

func (ds *DashboardServer) sessionDropReader() sessionDropFeatureReader {
	reader, _ := ds.featureRuntime.(sessionDropFeatureReader)
	return reader
}
func featureError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code}})
}
func featurePositiveID(raw string) (int64, bool) {
	if len(raw) == 0 || len(raw) > 19 || raw[0] == '0' {
		return 0, false
	}
	for _, char := range raw {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	return id, err == nil && id > 0
}
func (ds *DashboardServer) handleSessionDropList(w http.ResponseWriter, r *http.Request) {
	reader := ds.sessionDropReader()
	if reader == nil {
		featureError(w, 503, "session_drop_list_unavailable")
		return
	}
	limit, before, ok := sessionDropListQuery(r)
	if !ok {
		featureError(w, 400, "invalid_request")
		return
	}
	sources, err := reader.featureSessionDrops(r.Context(), limit+1, before)
	if err != nil {
		featureError(w, 503, "session_drop_list_unavailable")
		return
	}
	var next *int64
	if len(sources) > limit {
		id := sources[limit-1].ID
		next = &id
		sources = sources[:limit]
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(sessiondrop.ListResponseFromSources(sources, next))
}

func sessionDropListQuery(r *http.Request) (int, int64, bool) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0, 0, false
	}
	for key, items := range values {
		if (key != "limit" && key != "before") || len(items) != 1 {
			return 0, 0, false
		}
	}
	limit := 50
	if items, present := values["limit"]; present {
		value, ok := parseFeatureLimit(items[0], 200, 50)
		if !ok {
			return 0, 0, false
		}
		limit = value
	}
	var before int64
	if items, present := values["before"]; present {
		value, ok := featurePositiveID(items[0])
		if !ok {
			return 0, 0, false
		}
		before = value
	}
	return limit, before, true
}
func (ds *DashboardServer) handleSessionDropDetail(w http.ResponseWriter, r *http.Request) {
	reader := ds.sessionDropReader()
	if reader == nil {
		featureError(w, 503, "session_drop_detail_unavailable")
		return
	}
	id, ok := featurePositiveID(r.PathValue("id"))
	if !ok {
		featureError(w, 400, "invalid_request")
		return
	}
	source, err := reader.featureSessionDrop(r.Context(), id)
	if err != nil {
		featureError(w, 503, "session_drop_detail_unavailable")
		return
	}
	if source == nil {
		featureError(w, 404, "source_not_found")
		return
	}
	attempts := []sessiondrop.AttemptSummary{}
	if controller := ds.controller(); controller != nil {
		history, err := controller.History(r.Context(), investigation.SourceRef{Kind: investigation.SourceKindSessionDrop, ID: id})
		if err != nil {
			featureError(w, 503, "session_drop_detail_unavailable")
			return
		}
		attempts = make([]sessiondrop.AttemptSummary, len(history))
		for i, attempt := range history {
			summary := attemptSummary(attempt)
			attempts[i] = sessiondrop.AttemptSummary{AttemptID: summary.AttemptID, AttemptNumber: summary.AttemptNumber, Initiation: sessiondrop.AttemptInitiation(summary.Initiation), RetryOfAttemptID: summary.RetryOfAttemptID, State: sessiondrop.AttemptState(summary.State), CreatedAt: summary.CreatedAt, StartedAt: summary.StartedAt, SendAuthorizedAt: summary.SendAuthorizedAt, SendCompletedAt: summary.SendCompletedAt, CompletedAt: summary.CompletedAt, TerminalReason: sessiondrop.TerminalReason(summary.TerminalReason), EvidenceVersion: summary.EvidenceVersion, OmissionCodes: convertOmissions(summary.OmissionCodes)}
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(sessiondrop.DetailResponse{Source: sessiondrop.SourceDetailFromSource(*source), Attempts: attempts})
}
func convertOmissions(items []investigation.OmissionCode) []sessiondrop.OmissionCode {
	out := make([]sessiondrop.OmissionCode, len(items))
	for i := range items {
		out[i] = sessiondrop.OmissionCode(items[i])
	}
	return out
}
func parseFeatureLimit(raw string, maximum, fallback int) (int, bool) {
	if len(raw) == 0 || raw[0] == '0' {
		return fallback, false
	}
	for _, char := range raw {
		if char < '0' || char > '9' {
			return fallback, false
		}
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > maximum {
		return fallback, false
	}
	return value, true
}
