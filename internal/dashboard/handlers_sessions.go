//go:build windows

package dashboard

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
	"github.com/google/uuid"
)

const sessionStoreTimeout = 5 * time.Second

const sessionActionTTL = 5 * time.Minute

type sessionListQuery struct {
	Q        string `json:"q"`
	State    string `json:"state"`
	Sort     string `json:"sort"`
	Dir      string `json:"dir"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
}

type sessionDetailQuery struct {
	Q        string `json:"q"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
}

type sessionPageView struct {
	Number int `json:"number"`
	Size   int `json:"size"`
	Pages  int `json:"pages"`
}

type sessionShadowResponse struct {
	Command     string `json:"command"`
	ProtocolURI string `json:"protocol_uri"`
}

type sessionSummaryResponse struct {
	Host                      string                            `json:"host"`
	Mode                      string                            `json:"mode"`
	Status                    string                            `json:"status"`
	Collection                string                            `json:"collection,omitempty"`
	Freshness                 telemetry.SessionFreshness        `json:"freshness"`
	LatestAttemptInstanceID   string                            `json:"latest_attempt_instance_id"`
	LatestAttemptSequence     string                            `json:"latest_attempt_sequence"`
	LatestAttemptObservedAtMS int64                             `json:"latest_attempt_observed_at_ms"`
	LatestAttemptReceivedAtMS int64                             `json:"latest_attempt_received_at_ms"`
	LastSuccessInstanceID     *string                           `json:"last_success_instance_id"`
	LastSuccessSequence       *string                           `json:"last_success_sequence"`
	LastSuccessObservedAtMS   *int64                            `json:"last_success_observed_at_ms"`
	LastSuccessReceivedAtMS   *int64                            `json:"last_success_received_at_ms"`
	SessionCount              *int                              `json:"session_count"`
	ActiveCount               *int                              `json:"active_count"`
	IdleCount                 *int                              `json:"idle_count"`
	DisconnectedCount         *int                              `json:"disconnected_count"`
	UserCount                 *int                              `json:"user_count"`
	LastActivityAtMS          *int64                            `json:"last_activity_at_ms"`
	Capabilities              *sessiondata.SessionCapabilities  `json:"capabilities"`
	CollectionStatus          telemetry.SessionCollectionStatus `json:"collection_status"`
	CollectionErrorCode       *string                           `json:"collection_error_code"`
	DetailAvailable           bool                              `json:"detail_available"`
}

func sessionSummaryResponses(items []telemetry.FleetSessionSummary, cfg dc.SessionsConfig, withActions bool) []sessionSummaryResponse {
	result := make([]sessionSummaryResponse, len(items))
	for i, item := range items {
		result[i] = sessionSummaryResponse{Host: item.Host, Mode: item.HostMode, Status: item.HostStatus, Collection: item.Collection, Freshness: item.Freshness, LatestAttemptInstanceID: item.LatestAttemptInstanceID, LatestAttemptSequence: item.LatestAttemptSequence, LatestAttemptObservedAtMS: item.LatestAttemptObservedAtMS, LatestAttemptReceivedAtMS: item.LatestAttemptReceivedAtMS, LastSuccessInstanceID: item.LastSuccessInstanceID, LastSuccessSequence: item.LastSuccessSequence, LastSuccessObservedAtMS: item.LastSuccessObservedAtMS, LastSuccessReceivedAtMS: item.LastSuccessReceivedAtMS, SessionCount: item.SessionCount, ActiveCount: item.ActiveCount, IdleCount: item.IdleCount, DisconnectedCount: item.DisconnectedCount, UserCount: item.UserCount, LastActivityAtMS: item.LastActivityAtMS, Capabilities: item.Capabilities, CollectionStatus: item.CollectionStatus, CollectionErrorCode: item.CollectionErrorCode, DetailAvailable: item.LastSuccessInstanceID != nil}
	}
	return result
}
func sessionPrivacy(cfg dc.SessionsConfig) sessiondata.PrivacyPolicy {
	return sessiondata.PrivacyPolicy{
		Identity: sessiondata.Visibility(cfg.IdentityVisibility),
		Client:   sessiondata.Visibility(cfg.ClientVisibility),
		Process:  sessiondata.Visibility(cfg.ProcessVisibility),
	}
}

func (ds *DashboardServer) sessionsConfig() (dc.SessionsConfig, error) {
	if ds.testLoadConfigFunc != nil {
		cfg, err := ds.testLoadConfigFunc()
		if err != nil {
			return dc.SessionsConfig{}, err
		}
		return cfg.Sessions, nil
	}
	cfg, err := dc.LoadConfig()
	if err != nil {
		return dc.SessionsConfig{}, err
	}
	return cfg.Sessions, nil
}

// CancelSessionActionsForPolicy prevents active commands from surviving a
// policy transition and broadcasts each resulting safe status.
func (ds *DashboardServer) CancelSessionActionsForPolicy(ctx context.Context) error {
	ds.sessionPolicyMu.Lock()
	defer ds.sessionPolicyMu.Unlock()
	return ds.cancelSessionActionsForPolicyLocked(ctx)
}

func (ds *DashboardServer) cancelSessionActionsForPolicyLocked(ctx context.Context) error {
	if ds.sessionActions == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, sessionStoreTimeout)
	defer cancel()
	statuses, err := ds.sessionActions.CancelForPrivacy(ctx, ds.clock())
	if err != nil {
		return err
	}
	for _, status := range statuses {
		ds.broadcastSessionAction(status)
	}
	return nil
}

// PurgeSessionSnapshotsForPrivacy irreversibly removes all retained snapshots
// and cancels commands in one telemetry transaction. The policy mutex also
// serializes this transition with guarded action enqueues.
func (ds *DashboardServer) PurgeSessionSnapshotsForPrivacy(ctx context.Context) error {
	ds.sessionPolicyMu.Lock()
	defer ds.sessionPolicyMu.Unlock()
	return ds.purgeSessionSnapshotsForPrivacyLocked(ctx)
}

func (ds *DashboardServer) purgeSessionSnapshotsForPrivacyLocked(ctx context.Context) error {
	if ds.sessionSnapshots == nil && ds.sessionActions == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, sessionStoreTimeout)
	defer cancel()
	if ds.sessionActions != nil {
		statuses, err := ds.sessionActions.PurgeSnapshotsForPrivacy(ctx, ds.clock())
		if err != nil {
			return err
		}
		for _, status := range statuses {
			ds.broadcastSessionAction(status)
		}
		return nil
	}
	if err := ds.sessionSnapshots.PurgeForPrivacy(ctx); err != nil {
		return err
	}
	return nil
}

func writeSessionsError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func validSessionQueryText(value string) bool {
	return len(value) <= 128 && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

func parseSessionListQuery(r *http.Request) (sessionListQuery, error) {
	q := r.URL.Query()
	result := sessionListQuery{Q: q.Get("q"), State: q.Get("state"), Sort: q.Get("sort"), Dir: q.Get("dir")}
	if result.State == "" {
		result.State = "all"
	}
	if result.Sort == "" {
		result.Sort = "host"
	}
	if result.Dir == "" {
		result.Dir = "asc"
	}
	var err error
	result.Page, err = sessionPage(q.Get("page"), 1)
	if err != nil {
		return result, err
	}
	result.PageSize, err = sessionPage(q.Get("page_size"), 30)
	if err != nil {
		return result, err
	}
	if !validSessionQueryText(result.Q) || !oneOfSession(result.State, "all", "active", "disconnected", "idle") || !oneOfSession(result.Sort, "host", "status", "mode", "sessions", "active", "idle", "disconnected", "users", "last_activity") || !oneOfSession(result.Dir, "asc", "desc") || result.Page < 1 || result.Page > 10000 || !oneOfInt(result.PageSize, 15, 30, 50) {
		return result, errors.New("invalid sessions query")
	}
	return result, nil
}

func parseSessionDetailQuery(r *http.Request) (sessionDetailQuery, error) {
	q := r.URL.Query()
	result := sessionDetailQuery{Q: q.Get("q")}
	var err error
	result.Page, err = sessionPage(q.Get("page"), 1)
	if err != nil {
		return result, err
	}
	result.PageSize, err = sessionPage(q.Get("page_size"), 30)
	if err != nil {
		return result, err
	}
	if !validSessionQueryText(result.Q) || result.Page < 1 || result.Page > 100 || !oneOfInt(result.PageSize, 15, 30, 50) {
		return result, errors.New("invalid session detail query")
	}
	return result, nil
}

func sessionPage(value string, defaultValue int) (int, error) {
	if value == "" {
		return defaultValue, nil
	}
	return strconv.Atoi(value)
}
func oneOfSession(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}
func oneOfInt(value int, options ...int) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}

func (ds *DashboardServer) sessionOnline(host string) bool {
	for _, info := range ds.state.All() {
		if strings.EqualFold(info.Hostname, host) {
			return !info.LastSeen.IsZero() && ds.clock().Sub(info.LastSeen) < ds.staleAfter()
		}
	}
	return false
}

func (ds *DashboardServer) knownSessionHosts() []telemetry.FleetKnownHost {
	infos := ds.state.All()
	hosts := make([]telemetry.FleetKnownHost, 0, len(infos))
	for _, info := range infos {
		host := telemetry.CanonicalHostname(info.Hostname)
		entry := telemetry.FleetKnownHost{Host: host}
		if info.LastResult != nil {
			entry.Status = statusToken(info.LastResult.Status)
			entry.Mode = info.LastResult.DrainModeLabel
		}
		if ds.rdCollections != nil {
			entry.Collection = ds.rdCollections.CollectionFor(host)
		}
		hosts = append(hosts, entry)
	}
	return hosts
}

func (ds *DashboardServer) handleSessionsFleet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	cfg, err := ds.sessionsConfig()
	if err != nil {
		writeSessionsError(w, http.StatusServiceUnavailable, "sessions_unavailable")
		return
	}
	if !cfg.Enabled {
		writeSessionsError(w, http.StatusConflict, "sessions_disabled")
		return
	}
	query, err := parseSessionListQuery(r)
	if err != nil {
		writeSessionsError(w, http.StatusBadRequest, "invalid_sessions_query")
		return
	}
	if ds.sessionQueries == nil {
		writeSessionsError(w, http.StatusServiceUnavailable, "sessions_unavailable")
		return
	}
	now := ds.clock()
	ctx, cancel := context.WithTimeout(r.Context(), sessionStoreTimeout)
	defer cancel()
	page, err := ds.sessionQueries.Fleet(ctx, telemetry.FleetSessionQuery{Query: query.Q, State: query.State, Sort: query.Sort, Direction: query.Dir, Page: query.Page, PageSize: query.PageSize, IdentityVisibility: sessiondata.Visibility(cfg.IdentityVisibility), Now: now, HeartbeatInterval: time.Duration(ds.heartbeatIntervalNanos.Load()), IsHostOnline: ds.sessionOnline, KnownHosts: ds.knownSessionHosts()})
	if err != nil {
		writeSessionsError(w, http.StatusServiceUnavailable, "sessions_unavailable")
		return
	}
	pages := (page.Total + query.PageSize - 1) / query.PageSize
	if pages == 0 {
		pages = 0
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Query       sessionListQuery         `json:"query"`
		Total       int                      `json:"total"`
		Page        sessionPageView          `json:"page"`
		ServerNowMS int64                    `json:"server_now_ms"`
		Items       []sessionSummaryResponse `json:"items"`
	}{Query: query, Total: page.Total, Page: sessionPageView{Number: query.Page, Size: query.PageSize, Pages: pages}, ServerNowMS: now.UTC().UnixMilli(), Items: sessionSummaryResponses(page.Items, cfg, false)})
}

func (ds *DashboardServer) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	cfg, err := ds.sessionsConfig()
	if err != nil {
		writeSessionsError(w, http.StatusServiceUnavailable, "sessions_unavailable")
		return
	}
	if !cfg.Enabled {
		writeSessionsError(w, http.StatusConflict, "sessions_disabled")
		return
	}
	host := r.PathValue("host")
	if err := sessiondata.ValidateCanonicalHost(host); err != nil {
		writeSessionsError(w, http.StatusBadRequest, "invalid_host")
		return
	}
	query, err := parseSessionDetailQuery(r)
	if err != nil {
		writeSessionsError(w, http.StatusBadRequest, "invalid_sessions_query")
		return
	}
	if ds.sessionQueries == nil {
		writeSessionsError(w, http.StatusServiceUnavailable, "sessions_unavailable")
		return
	}
	now := ds.clock()
	ctx, cancel := context.WithTimeout(r.Context(), sessionStoreTimeout)
	defer cancel()
	detail, err := ds.sessionQueries.Detail(ctx, host, telemetry.SessionDetailQuery{Query: query.Q, Page: query.Page, PageSize: query.PageSize, Now: now, HeartbeatInterval: time.Duration(ds.heartbeatIntervalNanos.Load()), IsHostOnline: ds.sessionOnline})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeSessionsError(w, http.StatusNotFound, "session_snapshot_not_found")
		} else {
			writeSessionsError(w, http.StatusServiceUnavailable, "sessions_unavailable")
		}
		return
	}
	actionsAvailable := cfg.Enabled && cfg.AllowActions && detail.CollectionStatus == telemetry.SessionCollectionOK && detail.Freshness == telemetry.SessionFreshnessFresh && detail.Capabilities != nil && detail.Capabilities.SessionActions
	item := sessionSummaryResponses([]telemetry.FleetSessionSummary{detail.FleetSessionSummary}, cfg, false)[0]
	encoded, _ := json.Marshal(item)
	response := make(map[string]any)
	_ = json.Unmarshal(encoded, &response)
	delete(response, "detail_available")
	delete(response, "mode")
	delete(response, "status")
	delete(response, "collection")
	response["actions_available"] = actionsAvailable
	response["summary"] = map[string]any{"total": intValue(detail.SessionCount), "active": intValue(detail.ActiveCount), "idle": intValue(detail.IdleCount), "disconnected": intValue(detail.DisconnectedCount), "users": intValue(detail.UserCount), "last_activity_at_ms": detail.LastActivityAtMS}
	response["query"] = query
	response["total"] = detail.Total
	response["sessions"] = detail.Sessions
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func intValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func (ds *DashboardServer) handleSessionAction(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	host := r.PathValue("host")
	sessionID, ok := parseSessionActionID(r.PathValue("sessionID"))
	if err := sessiondata.ValidateCanonicalHost(host); err != nil || !ok {
		writeSessionsError(w, http.StatusBadRequest, "invalid_action_request")
		return
	}
	if ds.sessionActions == nil || ds.sessionQueries == nil {
		writeSessionsError(w, http.StatusServiceUnavailable, "storage_error")
		return
	}
	idempotencyKey, err := uuid.Parse(r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeSessionsError(w, http.StatusBadRequest, "invalid_action_request")
		return
	}
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		writeSessionsError(w, http.StatusBadRequest, "invalid_action_request")
		return
	}
	var request sessiondata.ActionRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(body) > 4096 || !decodeSessionActionRequest(body, &request) {
		writeSessionsError(w, http.StatusBadRequest, "invalid_action_request")
		return
	}
	normalized, err := sessiondata.NormalizeActionRequest(request)
	if err != nil {
		writeSessionsError(w, http.StatusBadRequest, "invalid_action_request")
		return
	}
	requestedBy, ok := ds.sessionRequestUser(r)
	if !ok {
		writeSessionsError(w, http.StatusUnauthorized, "authentication_required")
		return
	}

	// Replay resolution and every mutable target predicate happen under this
	// lock and the store's SQLite write transaction. In particular, a policy
	// change cannot invalidate a snapshot between the dashboard check and the
	// durable insert.
	ds.sessionPolicyMu.Lock()
	defer ds.sessionPolicyMu.Unlock()
	cfg, err := ds.sessionsConfig()
	if err != nil {
		writeSessionsError(w, http.StatusServiceUnavailable, "storage_error")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sessionStoreTimeout)
	defer cancel()
	now := ds.clock()
	actionID, err := uuid.NewV7()
	if err != nil {
		writeSessionsError(w, http.StatusServiceUnavailable, "storage_error")
		return
	}
	fingerprint := normalized.Fingerprint()
	action := sessiondata.SessionAction{ActionID: actionID.String(), CanonicalHost: host, SessionID: sessionID, ExpectedLogonAtMS: normalized.ExpectedLogonAtMS, Type: normalized.Type, State: sessiondata.SessionActionQueued, CreatedAtMS: now.UnixMilli(), ExpiresAtMS: now.Add(sessionActionTTL).UnixMilli(), RequestedBy: requestedBy, IdempotencyKey: idempotencyKey.String(), RequestFingerprint: fingerprint[:]}
	freshSince := now.Add(-3 * time.Duration(ds.heartbeatIntervalNanos.Load())).UnixMilli()
	input := telemetry.SessionActionEnqueue{
		Action: action, IdempotencyEndpoint: "POST /api/v1/sessions/" + host + "/" + strconv.FormatUint(uint64(sessionID), 10) + "/actions",
		ValidateTarget: true, Enabled: cfg.Enabled && cfg.AllowActions, FreshSinceMS: freshSince,
	}
	if normalized.Type == sessiondata.SessionActionMessage {
		ciphertext, err := dc.DPAPIEncrypt([]byte(normalized.Message))
		if err != nil {
			writeSessionsError(w, http.StatusServiceUnavailable, "storage_error")
			return
		}
		input.MessageCiphertext = ciphertext
		input.MessageProtection = "dpapi"
	}
	status, replay, err := ds.sessionActions.Enqueue(ctx, input)
	if err != nil {
		switch {
		case errors.Is(err, telemetry.ErrSessionActionIdempotencyConflict):
			writeSessionsError(w, http.StatusConflict, "idempotency_conflict")
		case errors.Is(err, telemetry.ErrSessionActionDisabled):
			writeSessionsError(w, http.StatusConflict, "sessions_disabled")
		case errors.Is(err, telemetry.ErrSessionActionSnapshotNotFound):
			writeSessionsError(w, http.StatusNotFound, "session_snapshot_not_found")
		case errors.Is(err, telemetry.ErrSessionActionHostNotFresh):
			writeSessionsError(w, http.StatusConflict, "host_not_fresh")
		case errors.Is(err, telemetry.ErrSessionActionUnsupported):
			writeSessionsError(w, http.StatusConflict, "actions_unsupported")
		case errors.Is(err, telemetry.ErrSessionActionSessionNotFound):
			writeSessionsError(w, http.StatusNotFound, "session_not_found")
		case errors.Is(err, telemetry.ErrSessionActionIdentityChanged):
			writeSessionsError(w, http.StatusConflict, "session_identity_changed")
		default:
			writeSessionsError(w, http.StatusServiceUnavailable, "storage_error")
		}
		return
	}
	if !replay {
		ds.broadcastSessionAction(status)
	}
	w.Header().Set("Content-Type", "application/json")
	if replay {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusAccepted)
	}
	_ = json.NewEncoder(w).Encode(struct {
		Action sessiondata.SessionActionStatus `json:"action"`
	}{Action: status})
}

func (ds *DashboardServer) handleSessionActionStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actionID := r.PathValue("actionID")
	if _, err := uuid.Parse(actionID); err != nil {
		writeSessionsError(w, http.StatusNotFound, "session_action_not_found")
		return
	}
	if ds.sessionActions == nil {
		writeSessionsError(w, http.StatusServiceUnavailable, "storage_error")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sessionStoreTimeout)
	defer cancel()
	statuses, err := ds.sessionActions.Expire(ctx, ds.clock())
	if err != nil {
		writeSessionsError(w, http.StatusServiceUnavailable, "storage_error")
		return
	}
	for _, transitioned := range statuses {
		ds.broadcastSessionAction(transitioned)
	}
	status, err := ds.sessionActions.Status(ctx, actionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeSessionsError(w, http.StatusNotFound, "session_action_not_found")
		} else {
			writeSessionsError(w, http.StatusServiceUnavailable, "storage_error")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}

func (ds *DashboardServer) handleSessionShadow(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	host := r.PathValue("host")
	sessionID, ok := parseSessionActionID(r.PathValue("sessionID"))
	if err := sessiondata.ValidateCanonicalHost(host); err != nil || !ok {
		writeSessionsError(w, http.StatusBadRequest, "invalid_session_target")
		return
	}
	if _, ok := ds.sessionRequestUser(r); !ok {
		writeSessionsError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	if ds.sessionQueries == nil {
		writeSessionsError(w, http.StatusServiceUnavailable, "sessions_unavailable")
		return
	}
	cfg, err := ds.sessionsConfig()
	if err != nil || !cfg.Enabled {
		writeSessionsError(w, http.StatusConflict, "sessions_unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sessionStoreTimeout)
	defer cancel()
	now := ds.clock()
	detail, err := ds.sessionQueries.Detail(ctx, host, telemetry.SessionDetailQuery{Page: 1, PageSize: 50, Now: now, HeartbeatInterval: time.Duration(ds.heartbeatIntervalNanos.Load()), IsHostOnline: ds.sessionOnline})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeSessionsError(w, http.StatusNotFound, "session_snapshot_not_found")
		} else {
			writeSessionsError(w, http.StatusServiceUnavailable, "sessions_unavailable")
		}
		return
	}
	if detail.CollectionStatus != telemetry.SessionCollectionOK || detail.Freshness != telemetry.SessionFreshnessFresh {
		writeSessionsError(w, http.StatusConflict, "sessions_unavailable")
		return
	}
	found := false
	for page := 1; page <= (detail.Total+49)/50 && !found; page++ {
		pageDetail := detail
		if page > 1 {
			pageDetail, err = ds.sessionQueries.Detail(ctx, host, telemetry.SessionDetailQuery{Page: page, PageSize: 50, Now: now, HeartbeatInterval: time.Duration(ds.heartbeatIntervalNanos.Load()), IsHostOnline: ds.sessionOnline})
			if err != nil {
				writeSessionsError(w, http.StatusServiceUnavailable, "sessions_unavailable")
				return
			}
		}
		for _, session := range pageDetail.Sessions {
			if session.SessionID == sessionID {
				found = true
				break
			}
		}
	}
	if !found {
		writeSessionsError(w, http.StatusConflict, "session_changed")
		return
	}
	command, err := sessiondata.ShadowCommand(host, sessionID)
	if err != nil {
		writeSessionsError(w, http.StatusBadRequest, "invalid_session_target")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sessionShadowResponse{
		Command:     command,
		ProtocolURI: "drainctl-shadow://shadow?host=" + host + "&session=" + strconv.FormatUint(uint64(sessionID), 10),
	})
}

func parseSessionActionID(value string) (uint32, bool) {
	id, err := strconv.ParseUint(value, 10, 32)
	return uint32(id), err == nil
}

func decodeSessionActionRequest(body []byte, request *sessiondata.ActionRequest) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	return decoder.Decode(request) == nil && decoder.Decode(&struct{}{}) == io.EOF
}

func (ds *DashboardServer) sessionRequestUser(r *http.Request) (string, bool) {
	cookie, err := r.Cookie("drainctl_session")
	if err != nil || ds.sessionStore == nil {
		return "", false
	}
	session := ds.sessionStore.Get(cookie.Value)
	if session == nil || !session.IsAdmin || session.Username == "" {
		return "", false
	}
	return session.Username, true
}

// broadcastSessionAction emits the only action metadata allowed in the
// authenticated dashboard event stream.
func (ds *DashboardServer) broadcastSessionAction(status sessiondata.SessionActionStatus) {
	if ds.broker == nil {
		return
	}
	data, err := json.Marshal(struct {
		ActionID      string                         `json:"action_id"`
		State         sessiondata.SessionActionState `json:"state"`
		CompletedAtMS *int64                         `json:"completed_at_ms"`
		ResultCode    *string                        `json:"result_code"`
	}{ActionID: status.ActionID, State: status.State, CompletedAtMS: status.CompletedAtMS, ResultCode: status.ResultCode})
	if err != nil {
		return
	}
	payload, err := json.Marshal(SSEEvent{Type: "session_action", Host: status.CanonicalHost, Data: data, Timestamp: ds.clock()})
	if err == nil {
		ds.broker.Broadcast(payload)
	}
}

func (ds *DashboardServer) handleSessionSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Encoding") != "" {
		writeSessionsError(w, http.StatusUnsupportedMediaType, "unsupported_media_type")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeSessionsError(w, http.StatusUnsupportedMediaType, "unsupported_media_type")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, sessiondata.MaxSnapshotBytes+1))
	if err != nil {
		writeSessionsError(w, http.StatusBadRequest, "invalid_snapshot")
		return
	}
	if len(body) > sessiondata.MaxSnapshotBytes {
		writeSessionsError(w, http.StatusRequestEntityTooLarge, "snapshot_too_large")
		return
	}
	var snapshot sessiondata.SessionSnapshot
	if err := decodeStrictSessionSnapshot(body, &snapshot); err != nil || snapshot.Validate() != nil {
		writeSessionsError(w, http.StatusBadRequest, "invalid_snapshot")
		return
	}
	auth := GetAuthInfo(r)
	if auth != nil && !isAuthorizedForHost(auth, snapshot.Host, ds.cfg.Group) && !isLocalSystemForHost(r, auth, snapshot.Host) {
		writeSessionsError(w, http.StatusForbidden, "host_identity_mismatch")
		return
	}
	result, err := ds.ingestSessionSnapshot(r.Context(), snapshot)
	if err != nil {
		writeSessionsError(w, http.StatusServiceUnavailable, "storage_error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	response := map[string]any{"accepted": result.Accepted, "received_at_ms": result.ReceivedAtMS}
	if !result.Accepted {
		response["reason"] = "stale_snapshot"
	} else if result.Fatal {
		response["collection_error"] = snapshot.CollectionError.Code
	} else if result.Aggregates != nil {
		response["session_count"] = result.Aggregates.SessionCount
	}
	_ = json.NewEncoder(w).Encode(response)
}

func (ds *DashboardServer) ingestSessionSnapshot(ctx context.Context, snapshot sessiondata.SessionSnapshot) (telemetry.SnapshotApplyResult, error) {
	if ds.sessionSnapshots == nil {
		return telemetry.SnapshotApplyResult{}, errors.New("session snapshot store unavailable")
	}
	// A visibility transition must either purge an already accepted old-policy
	// snapshot or cause this ingest to read and persist with the new policy.
	ds.sessionPolicyMu.Lock()
	cfg, err := ds.sessionsConfig()
	if err != nil {
		ds.sessionPolicyMu.Unlock()
		return telemetry.SnapshotApplyResult{}, err
	}
	if !cfg.Enabled {
		ds.sessionPolicyMu.Unlock()
		return telemetry.SnapshotApplyResult{}, errors.New("sessions disabled")
	}
	ctx, cancel := context.WithTimeout(ctx, sessionStoreTimeout)
	defer cancel()
	result, err := ds.sessionSnapshots.Apply(ctx, snapshot, sessionPrivacy(cfg))
	ds.sessionPolicyMu.Unlock()
	if err != nil || !result.Accepted {
		return result, err
	}
	ds.broadcastSessionSnapshot(snapshot.Host)
	return result, nil
}

func (ds *DashboardServer) broadcastSessionSnapshot(host string) {
	if ds.broker == nil || ds.sessionQueries == nil {
		return
	}
	cfg, err := ds.sessionsConfig()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionStoreTimeout)
	defer cancel()
	detail, err := ds.sessionQueries.Detail(ctx, host, telemetry.SessionDetailQuery{Now: ds.clock(), HeartbeatInterval: time.Duration(ds.heartbeatIntervalNanos.Load()), IsHostOnline: ds.sessionOnline})
	if err != nil {
		return
	}
	actionsAvailable := cfg.Enabled && cfg.AllowActions && detail.CollectionStatus == telemetry.SessionCollectionOK && detail.Freshness == telemetry.SessionFreshnessFresh && detail.Capabilities != nil && detail.Capabilities.SessionActions
	var lastError *struct {
		Code string `json:"code"`
		AtMS int64  `json:"at_ms"`
	}
	if detail.CollectionErrorCode != nil {
		lastError = &struct {
			Code string `json:"code"`
			AtMS int64  `json:"at_ms"`
		}{Code: *detail.CollectionErrorCode, AtMS: detail.LatestAttemptReceivedAtMS}
	}
	data, err := json.Marshal(struct {
		Host                      string                            `json:"host"`
		LatestAttemptInstanceID   string                            `json:"latest_attempt_instance_id"`
		LatestAttemptSequence     string                            `json:"latest_attempt_sequence"`
		LatestAttemptObservedAtMS int64                             `json:"latest_attempt_observed_at_ms"`
		LatestAttemptReceivedAtMS int64                             `json:"latest_attempt_received_at_ms"`
		LastSuccessInstanceID     *string                           `json:"last_success_instance_id"`
		LastSuccessSequence       *string                           `json:"last_success_sequence"`
		LastSuccessObservedAtMS   *int64                            `json:"last_success_observed_at_ms"`
		LastSuccessReceivedAtMS   *int64                            `json:"last_success_received_at_ms"`
		Freshness                 telemetry.SessionFreshness        `json:"freshness"`
		SessionCount              int                               `json:"session_count"`
		ActiveCount               int                               `json:"active_count"`
		Capabilities              sessiondata.SessionCapabilities   `json:"capabilities"`
		ActionsAvailable          bool                              `json:"actions_available"`
		CollectionStatus          telemetry.SessionCollectionStatus `json:"collection_status"`
		LastError                 *struct {
			Code string `json:"code"`
			AtMS int64  `json:"at_ms"`
		} `json:"last_error"`
	}{Host: detail.Host, LatestAttemptInstanceID: detail.LatestAttemptInstanceID, LatestAttemptSequence: detail.LatestAttemptSequence, LatestAttemptObservedAtMS: detail.LatestAttemptObservedAtMS, LatestAttemptReceivedAtMS: detail.LatestAttemptReceivedAtMS, LastSuccessInstanceID: detail.LastSuccessInstanceID, LastSuccessSequence: detail.LastSuccessSequence, LastSuccessObservedAtMS: detail.LastSuccessObservedAtMS, LastSuccessReceivedAtMS: detail.LastSuccessReceivedAtMS, Freshness: detail.Freshness, SessionCount: intValue(detail.SessionCount), ActiveCount: intValue(detail.ActiveCount), Capabilities: capabilityValue(detail.Capabilities), ActionsAvailable: actionsAvailable, CollectionStatus: detail.CollectionStatus, LastError: lastError})
	if err != nil {
		return
	}
	payload, err := json.Marshal(SSEEvent{Type: "session_snapshot", Host: host, Data: data, Timestamp: ds.clock()})
	if err == nil {
		ds.broker.Broadcast(payload)
	}
}

func capabilityValue(value *sessiondata.SessionCapabilities) sessiondata.SessionCapabilities {
	if value == nil {
		return sessiondata.SessionCapabilities{}
	}
	return *value
}

func decodeStrictSessionSnapshot(body []byte, snapshot *sessiondata.SessionSnapshot) error {
	if !utf8.Valid(body) {
		return errors.New("invalid utf-8")
	}
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(snapshot); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := inspectJSONValue(decoder); err != nil {
		return err
	}
	_, err := decoder.Token()
	if err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func inspectJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("invalid object key")
			}
			if _, exists := keys[name]; exists {
				return errors.New("duplicate JSON key")
			}
			keys[name] = struct{}{}
			if err := inspectJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := inspectJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	end, err := decoder.Token()
	if err != nil || end != matchingJSONDelimiter(delim) {
		return errors.New("unclosed JSON value")
	}
	return nil
}

func matchingJSONDelimiter(start json.Delim) json.Delim {
	if start == '{' {
		return '}'
	}
	return ']'
}
