//go:build windows

package telemetry

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
)

type SessionFreshness string

const (
	SessionFreshnessUnknown SessionFreshness = "unknown"
	SessionFreshnessFresh   SessionFreshness = "fresh"
	SessionFreshnessStale   SessionFreshness = "stale"
	SessionFreshnessOffline SessionFreshness = "offline"
)

type SessionCollectionStatus string

const (
	SessionCollectionOK    SessionCollectionStatus = "ok"
	SessionCollectionError SessionCollectionStatus = "error"
)

const maxFleetKnownHosts = 1000

// FleetKnownHost is the bounded roster context used to render the session
// fleet. Session snapshots only describe hosts that have reported; the roster
// is authoritative for which hosts belong in the fleet.
type FleetKnownHost struct {
	Host, Status, Mode, Collection string
}

type FleetSessionQuery struct {
	Query, State, Sort, Direction string
	Page, PageSize                int
	IdentityVisibility            sessiondata.Visibility
	Now                           time.Time
	HeartbeatInterval             time.Duration
	IsHostOnline                  func(string) bool
	KnownHosts                    []FleetKnownHost
}
type SessionDetailQuery struct {
	Query             string
	Page, PageSize    int
	Now               time.Time
	HeartbeatInterval time.Duration
	IsHostOnline      func(string) bool
}
type FleetSessionPage struct {
	Total int
	Items []FleetSessionSummary
}
type FleetSessionSummary struct {
	Host                                                               string
	HostStatus, HostMode, Collection                                   string
	Freshness                                                          SessionFreshness
	CollectionStatus                                                   SessionCollectionStatus
	CollectionErrorCode                                                *string
	LatestAttemptInstanceID                                            string
	LatestAttemptSequence                                              string
	LatestAttemptObservedAtMS, LatestAttemptReceivedAtMS               int64
	LastSuccessInstanceID, LastSuccessSequence                         *string
	LastSuccessObservedAtMS, LastSuccessReceivedAtMS                   *int64
	SessionCount, ActiveCount, IdleCount, DisconnectedCount, UserCount *int
	LastActivityAtMS                                                   *int64
	Capabilities                                                       *sessiondata.SessionCapabilities
}
type SessionDetail struct {
	FleetSessionSummary
	Sessions []sessiondata.SessionRecord
	Total    int
}

type SessionQueryStore struct{ db *DB }

func NewSessionQueryStore(db *DB) (*SessionQueryStore, error) {
	if db == nil || db.reader == nil {
		return nil, fmt.Errorf("telemetry: session query store requires a database")
	}
	return &SessionQueryStore{db: db}, nil
}

func (s *SessionQueryStore) Fleet(ctx context.Context, query FleetSessionQuery) (FleetSessionPage, error) {
	query = normalizeFleetQuery(query)
	if err := validateFleetQuery(query); err != nil {
		return FleetSessionPage{}, err
	}
	known, err := normalizeKnownHosts(query.KnownHosts)
	if err != nil {
		return FleetSessionPage{}, err
	}
	if len(known) == 0 {
		return FleetSessionPage{Items: []FleetSessionSummary{}}, nil
	}
	snapshots, err := s.loadFleetSnapshots(ctx, known, query)
	if err != nil {
		return FleetSessionPage{}, err
	}
	sessionMatches, err := s.loadFleetSessionMatches(ctx, known, query)
	if err != nil {
		return FleetSessionPage{}, err
	}
	all := make([]FleetSessionSummary, 0, len(known))
	for host, item := range known {
		if stored, ok := snapshots[host]; ok {
			item = stored
			item.HostStatus = known[host].HostStatus
			item.HostMode = known[host].HostMode
			item.Collection = known[host].Collection
		}
		if matchesFleet(item, query, sessionMatches[host]) {
			all = append(all, item)
		}
	}
	sortFleet(all, query.Sort, query.Direction)
	total := len(all)
	start := (query.Page - 1) * query.PageSize
	if start >= total {
		return FleetSessionPage{Total: total, Items: []FleetSessionSummary{}}, nil
	}
	end := min(start+query.PageSize, total)
	return FleetSessionPage{Total: total, Items: all[start:end]}, nil
}

func (s *SessionQueryStore) loadFleetSnapshots(ctx context.Context, known map[string]FleetSessionSummary, query FleetSessionQuery) (map[string]FleetSessionSummary, error) {
	hostJSON, err := fleetHostJSON(known)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.reader.QueryContext(ctx, `SELECT canonical_host, latest_attempt_instance_id, latest_attempt_sequence, latest_attempt_observed_at_ms, latest_attempt_received_at_ms,
		last_success_instance_id, last_success_sequence, last_success_observed_at_ms, last_success_received_at_ms,
		session_count, active_count, idle_count, disconnected_count, user_count, last_activity_at_ms,
		capability_actions, capability_processes, capability_input_delay, capability_remotefx, latest_attempt_error_code
		FROM session_snapshots WHERE canonical_host IN (SELECT value FROM json_each(?))`, hostJSON)
	if err != nil {
		return nil, fmt.Errorf("telemetry: session fleet query: %w", err)
	}
	result := make(map[string]FleetSessionSummary, len(known))
	for rows.Next() {
		item, err := scanSessionSummary(rows, query.Now, query.HeartbeatInterval, query.IsHostOnline)
		if err != nil {
			if closeErr := closeSessionRows(rows, "telemetry: session fleet rows"); closeErr != nil {
				return nil, errors.Join(err, closeErr)
			}
			return nil, err
		}
		result[item.Host] = item
	}
	if err := rows.Err(); err != nil {
		if closeErr := closeSessionRows(rows, "telemetry: session fleet rows"); closeErr != nil {
			return nil, errors.Join(fmt.Errorf("telemetry: session fleet rows: %w", err), closeErr)
		}
		return nil, fmt.Errorf("telemetry: session fleet rows: %w", err)
	}
	if err := closeSessionRows(rows, "telemetry: session fleet rows"); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *SessionQueryStore) loadFleetSessionMatches(ctx context.Context, known map[string]FleetSessionSummary, query FleetSessionQuery) (map[string]bool, error) {
	if query.Query == "" {
		return nil, nil
	}
	hostJSON, err := fleetHostJSON(known)
	if err != nil {
		return nil, err
	}
	needle := "%" + strings.ToLower(query.Query) + "%"
	var sqlQuery string
	var args []any
	if query.IdentityVisibility == sessiondata.VisibilityFull {
		sqlQuery = `SELECT DISTINCT canonical_host FROM session_latest
			WHERE canonical_host IN (SELECT value FROM json_each(?))
				AND (CAST(session_id AS TEXT) LIKE ?
					OR lower(COALESCE(user_name,'')) LIKE ?
					OR lower(COALESCE(domain_name,'')) LIKE ?)`
		args = []any{hostJSON, needle, needle, needle}
	} else {
		sqlQuery = `SELECT DISTINCT canonical_host FROM session_latest
			WHERE canonical_host IN (SELECT value FROM json_each(?))
				AND CAST(session_id AS TEXT) LIKE ?`
		args = []any{hostJSON, needle}
	}
	rows, err := s.db.reader.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("telemetry: session fleet search: %w", err)
	}
	result := make(map[string]bool)
	for rows.Next() {
		var host string
		if err := rows.Scan(&host); err != nil {
			if closeErr := closeSessionRows(rows, "telemetry: session fleet search rows"); closeErr != nil {
				return nil, errors.Join(fmt.Errorf("telemetry: session fleet search row: %w", err), closeErr)
			}
			return nil, fmt.Errorf("telemetry: session fleet search row: %w", err)
		}
		result[host] = true
	}
	if err := rows.Err(); err != nil {
		if closeErr := closeSessionRows(rows, "telemetry: session fleet search rows"); closeErr != nil {
			return nil, errors.Join(fmt.Errorf("telemetry: session fleet search rows: %w", err), closeErr)
		}
		return nil, fmt.Errorf("telemetry: session fleet search rows: %w", err)
	}
	if err := closeSessionRows(rows, "telemetry: session fleet search rows"); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *SessionQueryStore) Detail(ctx context.Context, host string, query SessionDetailQuery) (SessionDetail, error) {
	if err := sessiondata.ValidateCanonicalHost(host); err != nil {
		return SessionDetail{}, fmt.Errorf("telemetry: invalid session detail host: %w", err)
	}
	query = normalizeDetailQuery(query)
	if err := validateDetailQuery(query); err != nil {
		return SessionDetail{}, err
	}
	tx, err := s.db.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SessionDetail{}, fmt.Errorf("telemetry: begin session detail read: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	row := tx.QueryRowContext(ctx, `SELECT canonical_host, latest_attempt_instance_id, latest_attempt_sequence, latest_attempt_observed_at_ms, latest_attempt_received_at_ms,
		last_success_instance_id, last_success_sequence, last_success_observed_at_ms, last_success_received_at_ms,
		session_count, active_count, idle_count, disconnected_count, user_count, last_activity_at_ms,
		capability_actions, capability_processes, capability_input_delay, capability_remotefx, latest_attempt_error_code FROM session_snapshots WHERE canonical_host=?`, host)
	summary, err := scanSessionSummary(row, query.Now, query.HeartbeatInterval, query.IsHostOnline)
	if err != nil {
		return SessionDetail{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT session_id, logon_at_ms, user_name, domain_name, state, station, client_name, client_address, connect_at_ms, disconnect_at_ms, idle_since_ms, cpu_percent, working_set_bytes, input_delay_ms, remotefx_fps, remotefx_quality_pct, remotefx_encode_ms, remotefx_rtt_ms, remotefx_loss_pct, remotefx_server_skip, remotefx_network_skip, processes_json FROM session_latest WHERE canonical_host=?`, host)
	if err != nil {
		return SessionDetail{}, fmt.Errorf("telemetry: session detail rows: %w", err)
	}
	var sessions []sessiondata.SessionRecord
	for rows.Next() {
		record, err := scanSessionRecord(rows)
		if err != nil {
			if closeErr := closeSessionRows(rows, "telemetry: session detail rows"); closeErr != nil {
				return SessionDetail{}, errors.Join(err, closeErr)
			}
			return SessionDetail{}, err
		}
		if matchesDetail(record, query.Query) {
			sessions = append(sessions, record)
		}
	}
	if err := rows.Err(); err != nil {
		if closeErr := closeSessionRows(rows, "telemetry: session detail rows"); closeErr != nil {
			return SessionDetail{}, errors.Join(fmt.Errorf("telemetry: session detail iteration: %w", err), closeErr)
		}
		return SessionDetail{}, fmt.Errorf("telemetry: session detail iteration: %w", err)
	}
	if err := closeSessionRows(rows, "telemetry: session detail rows"); err != nil {
		return SessionDetail{}, err
	}
	sort.Slice(sessions, func(i, j int) bool {
		if a, b := sessiondata.StateRank(sessions[i].State), sessiondata.StateRank(sessions[j].State); a != b {
			return a < b
		}
		return sessions[i].SessionID < sessions[j].SessionID
	})
	total := len(sessions)
	start := (query.Page - 1) * query.PageSize
	if start >= total {
		sessions = []sessiondata.SessionRecord{}
	} else {
		sessions = sessions[start:min(start+query.PageSize, total)]
	}
	if err := tx.Commit(); err != nil {
		return SessionDetail{}, fmt.Errorf("telemetry: commit session detail read: %w", err)
	}
	return SessionDetail{FleetSessionSummary: summary, Sessions: sessions, Total: total}, nil
}

func scanSessionSummary(scanner interface{ Scan(...any) error }, now time.Time, heartbeat time.Duration, online func(string) bool) (FleetSessionSummary, error) {
	var item FleetSessionSummary
	var latestSeq []byte
	var successID sql.NullString
	var successSeq []byte
	var successObserved, successReceived sql.NullInt64
	var count, active, idle, disconnected, users sql.NullInt64
	var activity sql.NullInt64
	var actions, processes, input, rfx sql.NullInt64
	var errorCode sql.NullString
	err := scanner.Scan(&item.Host, &item.LatestAttemptInstanceID, &latestSeq, &item.LatestAttemptObservedAtMS, &item.LatestAttemptReceivedAtMS, &successID, &successSeq, &successObserved, &successReceived, &count, &active, &idle, &disconnected, &users, &activity, &actions, &processes, &input, &rfx, &errorCode)
	if err != nil {
		return item, err
	}
	item.LatestAttemptSequence = sequenceString(latestSeq)
	if successID.Valid {
		item.LastSuccessInstanceID = &successID.String
		seq := sequenceString(successSeq)
		item.LastSuccessSequence = &seq
		successObservedAtMS := successObserved.Int64
		item.LastSuccessObservedAtMS = &successObservedAtMS
		successReceivedAtMS := successReceived.Int64
		item.LastSuccessReceivedAtMS = &successReceivedAtMS
		item.SessionCount = intPtr(count)
		item.ActiveCount = intPtr(active)
		item.IdleCount = intPtr(idle)
		item.DisconnectedCount = intPtr(disconnected)
		item.UserCount = intPtr(users)
		if activity.Valid {
			lastActivityAtMS := activity.Int64
			item.LastActivityAtMS = &lastActivityAtMS
		}
		item.Capabilities = &sessiondata.SessionCapabilities{SessionActions: actions.Int64 != 0, Processes: processes.Int64 != 0, InputDelay: input.Int64 != 0, RemoteFX: rfx.Int64 != 0}
	}
	if errorCode.Valid {
		item.CollectionStatus = SessionCollectionError
		item.CollectionErrorCode = &errorCode.String
	} else {
		item.CollectionStatus = SessionCollectionOK
	}
	item.Freshness = freshness(item.LastSuccessReceivedAtMS, now, heartbeat, item.Host, online)
	return item, nil
}
func scanSessionRecord(scanner interface{ Scan(...any) error }) (sessiondata.SessionRecord, error) {
	var result sessiondata.SessionRecord
	var working sql.NullInt64
	var fps, quality, encode, rtt, loss, serverSkip, networkSkip sql.NullFloat64
	var processes string
	err := scanner.Scan(&result.SessionID, &result.LogonAtMS, &result.User, &result.Domain, &result.State, &result.Station, &result.ClientName, &result.ClientAddress, &result.ConnectAtMS, &result.DisconnectAtMS, &result.IdleSinceMS, &result.CPUPercent, &working, &result.InputDelayMS, &fps, &quality, &encode, &rtt, &loss, &serverSkip, &networkSkip, &processes)
	if err != nil {
		return result, err
	}
	if working.Valid {
		value := sessiondata.DecimalUint64(working.Int64)
		result.WorkingSetBytes = &value
	}
	if err := json.Unmarshal([]byte(processes), &result.Processes); err != nil {
		return result, fmt.Errorf("telemetry: decode persisted session processes: %w", err)
	}
	if fps.Valid || quality.Valid || encode.Valid || rtt.Valid || loss.Valid || serverSkip.Valid || networkSkip.Valid {
		result.RemoteFX = &sessiondata.RemoteFXMetrics{FPS: floatPtr(fps), QualityPercent: floatPtr(quality), EncodeTimeMS: floatPtr(encode), RTTMS: floatPtr(rtt), LossPercent: floatPtr(loss), ServerSkippedFPS: floatPtr(serverSkip), NetworkSkippedFPS: floatPtr(networkSkip)}
	}
	return result, nil
}
func freshness(success *int64, now time.Time, heartbeat time.Duration, host string, online func(string) bool) SessionFreshness {
	if success == nil {
		return SessionFreshnessUnknown
	}
	if online != nil && !online(host) {
		return SessionFreshnessOffline
	}
	age := now.UTC().UnixMilli() - *success
	interval := heartbeat.Milliseconds()
	if interval <= 0 {
		return SessionFreshnessUnknown
	}
	if age > 10*interval {
		return SessionFreshnessOffline
	}
	if age > 3*interval {
		return SessionFreshnessStale
	}
	return SessionFreshnessFresh
}
func normalizeFleetQuery(q FleetSessionQuery) FleetSessionQuery {
	if q.State == "" {
		q.State = "all"
	}
	if q.Sort == "" {
		q.Sort = "host"
	}
	if q.Direction == "" {
		q.Direction = "asc"
	}
	if q.Page == 0 {
		q.Page = 1
	}
	if q.PageSize == 0 {
		q.PageSize = 30
	}
	if q.Now.IsZero() {
		q.Now = time.Now()
	}
	return q
}
func normalizeDetailQuery(q SessionDetailQuery) SessionDetailQuery {
	if q.Page == 0 {
		q.Page = 1
	}
	if q.PageSize == 0 {
		q.PageSize = 30
	}
	if q.Now.IsZero() {
		q.Now = time.Now()
	}
	return q
}
func validateFleetQuery(q FleetSessionQuery) error {
	if len(q.Query) > 128 || q.Page < 1 || q.Page > 10000 || (q.PageSize != 15 && q.PageSize != 30 && q.PageSize != 50) || !oneOf(q.State, "all", "active", "disconnected", "idle") || !oneOf(q.Sort, "host", "status", "mode", "sessions", "active", "idle", "disconnected", "users", "last_activity") || !oneOf(q.Direction, "asc", "desc") {
		return fmt.Errorf("telemetry: invalid sessions query")
	}
	return nil
}
func validateDetailQuery(q SessionDetailQuery) error {
	if len(q.Query) > 128 || q.Page < 1 || q.Page > 100 || (q.PageSize != 15 && q.PageSize != 30 && q.PageSize != 50) {
		return fmt.Errorf("telemetry: invalid session detail query")
	}
	return nil
}
func matchesFleet(item FleetSessionSummary, q FleetSessionQuery, sessionMatch bool) bool {
	needle := strings.ToLower(q.Query)
	if needle != "" && !strings.Contains(strings.ToLower(item.Host), needle) && !sessionMatch {
		return false
	}
	switch q.State {
	case "active":
		return item.ActiveCount != nil && *item.ActiveCount > 0
	case "idle":
		return item.IdleCount != nil && *item.IdleCount > 0
	case "disconnected":
		return item.DisconnectedCount != nil && *item.DisconnectedCount > 0
	}
	return true
}

func normalizeKnownHosts(hosts []FleetKnownHost) (map[string]FleetSessionSummary, error) {
	if len(hosts) > maxFleetKnownHosts {
		return nil, fmt.Errorf("telemetry: too many known session hosts")
	}
	result := make(map[string]FleetSessionSummary, len(hosts))
	for _, known := range hosts {
		host := strings.ToLower(strings.TrimSpace(known.Host))
		if err := sessiondata.ValidateCanonicalHost(host); err != nil {
			return nil, fmt.Errorf("telemetry: invalid known session host: %w", err)
		}
		if _, duplicate := result[host]; duplicate {
			return nil, fmt.Errorf("telemetry: duplicate known session host")
		}
		result[host] = FleetSessionSummary{
			Host: host, HostStatus: known.Status, HostMode: known.Mode, Collection: known.Collection,
			Freshness: SessionFreshnessUnknown,
		}
	}
	return result, nil
}

func fleetHostJSON(known map[string]FleetSessionSummary) (string, error) {
	hostNames := make([]string, 0, len(known))
	for host := range known {
		hostNames = append(hostNames, host)
	}
	encoded, err := json.Marshal(hostNames)
	if err != nil {
		return "", fmt.Errorf("telemetry: encode known session hosts: %w", err)
	}
	return string(encoded), nil
}

func closeSessionRows(rows interface{ Close() error }, operation string) error {
	if err := rows.Close(); err != nil {
		return fmt.Errorf("%s close: %w", operation, err)
	}
	return nil
}
func oneOf(v string, allowed ...string) bool {
	for _, x := range allowed {
		if v == x {
			return true
		}
	}
	return false
}
func matchesDetail(r sessiondata.SessionRecord, q string) bool {
	q = strings.ToLower(q)
	if q == "" {
		return true
	}
	return strings.Contains(strings.ToLower(fmt.Sprint(r.SessionID)), q) || r.User != nil && strings.Contains(strings.ToLower(*r.User), q) || r.Domain != nil && strings.Contains(strings.ToLower(*r.Domain), q)
}
func sortFleet(items []FleetSessionSummary, field, direction string) {
	desc := direction == "desc"
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		cmp := compareFleet(a, b, field)
		if cmp == 0 {
			return strings.Compare(a.Host, b.Host) < 0
		}
		if desc {
			return cmp > 0
		}
		return cmp < 0
	})
}

func compareFleet(a, b FleetSessionSummary, field string) int {
	if field == "host" {
		return strings.Compare(a.Host, b.Host)
	}
	if field == "status" {
		return compareInt(hostStatusRank(a.HostStatus), hostStatusRank(b.HostStatus))
	}
	if field == "mode" {
		return strings.Compare(strings.ToLower(a.HostMode), strings.ToLower(b.HostMode))
	}
	return compareInt64(fleetValue(a, field), fleetValue(b, field))
}

// hostStatusRank defines the fleet presentation order independently of session
// collection outcomes: unhealthy hosts precede healthy and unknown hosts.
func hostStatusRank(status string) int {
	switch strings.ToLower(status) {
	case "alert":
		return 0
	case "warning":
		return 1
	case "grace":
		return 2
	case "healthy", "ok":
		return 3
	default:
		return 4
	}
}

func compareInt(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func compareInt64(a, b int64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func fleetValue(item FleetSessionSummary, field string) int64 {
	value := func(p *int) int64 {
		if p == nil {
			return -1
		}
		return int64(*p)
	}
	switch field {
	case "sessions":
		return value(item.SessionCount)
	case "active":
		return value(item.ActiveCount)
	case "idle":
		return value(item.IdleCount)
	case "disconnected":
		return value(item.DisconnectedCount)
	case "users":
		return value(item.UserCount)
	case "last_activity":
		if item.LastActivityAtMS != nil {
			return *item.LastActivityAtMS
		}
		return -1
	}
	return 0
}
func sequenceString(raw []byte) string {
	if len(raw) != 8 {
		return "0"
	}
	return fmt.Sprintf("%d", binary.BigEndian.Uint64(raw))
}
func intPtr(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	result := int(value.Int64)
	return &result
}
func floatPtr(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	result := value.Float64
	return &result
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
