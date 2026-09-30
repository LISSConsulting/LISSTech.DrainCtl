// Package sessiondata defines the bounded, privacy-safe Fleet Sessions domain model.
package sessiondata

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	SnapshotSchema          = "drainctl.session-snapshot.v1"
	MaxSnapshotBytes        = 512 * 1024
	MaxSessions             = 500
	MaxProcesses            = 5
	MaxUnixMillis    int64  = 253402300799999
	MaxStoredBytes   uint64 = ^uint64(0) >> 1
)

// DecimalUint64 is encoded as a canonical unsigned decimal JSON string.
type DecimalUint64 uint64

func (d DecimalUint64) Uint64() uint64 { return uint64(d) }
func (d DecimalUint64) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatUint(uint64(d), 10))
}
func (d *DecimalUint64) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return fmt.Errorf("decimal uint64 must be a JSON string: %w", err)
	}
	value, err := ParseDecimalUint64(text)
	if err != nil {
		return err
	}
	*d = value
	return nil
}

func ParseDecimalUint64(text string) (DecimalUint64, error) {
	if text == "" {
		return 0, fmt.Errorf("decimal uint64 is empty")
	}
	if text == "0" {
		return 0, nil
	}
	if text[0] == '0' {
		return 0, fmt.Errorf("decimal uint64 is not canonical")
	}
	for i := range text {
		if text[i] < '0' || text[i] > '9' {
			return 0, fmt.Errorf("decimal uint64 is not canonical")
		}
	}
	value, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid decimal uint64: %w", err)
	}
	return DecimalUint64(value), nil
}

// UUIDv7 is a UUID version 7 encoded as its 16 RFC 9562 wire bytes.
type UUIDv7 [16]byte

// ParseUUIDv7 parses a canonical UUIDv7 string and verifies its RFC variant.
func ParseUUIDv7(text string) (UUIDv7, error) {
	var value UUIDv7
	if len(text) != 36 {
		return value, fmt.Errorf("UUIDv7 is not canonical")
	}
	byteIndex := 0
	for i := range text {
		switch i {
		case 8, 13, 18, 23:
			if text[i] != '-' {
				return value, fmt.Errorf("UUIDv7 is not canonical")
			}
		default:
			nibble, ok := parseUUIDHex(text[i])
			if !ok {
				return value, fmt.Errorf("UUIDv7 is not canonical")
			}
			if byteIndex%2 == 0 {
				value[byteIndex/2] = nibble << 4
			} else {
				value[byteIndex/2] |= nibble
			}
			byteIndex++
		}
	}
	if value[6]>>4 != 7 || value[8]&0xc0 != 0x80 {
		return UUIDv7{}, fmt.Errorf("UUID is not version 7 with RFC variant")
	}
	return value, nil
}

// String formats value as a canonical lowercase UUID string.
func (value UUIDv7) String() string {
	const hex = "0123456789abcdef"
	var text [36]byte
	for i, b := range value {
		textIndex := i * 2
		if i >= 4 {
			textIndex++
		}
		if i >= 6 {
			textIndex++
		}
		if i >= 8 {
			textIndex++
		}
		if i >= 10 {
			textIndex++
		}
		text[textIndex] = hex[b>>4]
		text[textIndex+1] = hex[b&0x0f]
	}
	text[8], text[13], text[18], text[23] = '-', '-', '-', '-'
	return string(text[:])
}

// CompareUUIDv7 compares UUID wire bytes in chronological UUIDv7 order.
func CompareUUIDv7(left, right UUIDv7) int {
	for i := range left {
		if left[i] < right[i] {
			return -1
		}
		if left[i] > right[i] {
			return 1
		}
	}
	return 0
}

func parseUUIDHex(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, true
	case value >= 'A' && value <= 'F':
		return value - 'A' + 10, true
	default:
		return 0, false
	}
}

type SessionSnapshot struct {
	Schema           string              `json:"schema"`
	Host             string              `json:"host"`
	AgentInstanceID  string              `json:"agent_instance_id"`
	Sequence         DecimalUint64       `json:"sequence"`
	ObservedAtMS     int64               `json:"observed_at_ms"`
	CollectorVersion string              `json:"collector_version"`
	LogicalCPUCount  uint16              `json:"logical_cpu_count"`
	Capabilities     SessionCapabilities `json:"capabilities"`
	CollectionError  *CollectionError    `json:"collection_error"`
	Sessions         []SessionRecord     `json:"sessions"`
}

type SessionCapabilities struct {
	SessionActions bool `json:"session_actions"`
	Processes      bool `json:"processes"`
	InputDelay     bool `json:"input_delay"`
	RemoteFX       bool `json:"remotefx"`
}

type CollectionError struct {
	Code string `json:"code"`
}

const (
	CollectionErrorWTSEnumerationFailed = "wts_enumeration_failed"
	CollectionErrorWTSMetadataFailed    = "wts_metadata_failed"
	CollectionErrorTimeout              = "collector_timeout"
)

type SessionState string

const (
	SessionActive       SessionState = "active"
	SessionConnected    SessionState = "connected"
	SessionConnectQuery SessionState = "connect_query"
	SessionShadow       SessionState = "shadow"
	SessionDisconnected SessionState = "disconnected"
	SessionIdle         SessionState = "idle"
	SessionListen       SessionState = "listen"
	SessionReset        SessionState = "reset"
	SessionDown         SessionState = "down"
	SessionInit         SessionState = "init"
	SessionUnknown      SessionState = "unknown"
)

// StateRank puts unknown after every recognized state for deterministic presentation.
func StateRank(state SessionState) int {
	switch state {
	case SessionActive:
		return 0
	case SessionConnected:
		return 1
	case SessionConnectQuery:
		return 2
	case SessionShadow:
		return 3
	case SessionDisconnected:
		return 4
	case SessionIdle:
		return 5
	case SessionListen:
		return 6
	case SessionReset:
		return 7
	case SessionDown:
		return 8
	case SessionInit:
		return 9
	default:
		return 10
	}
}

func (s SessionState) Valid() bool { return StateRank(s) != 10 || s == SessionUnknown }

type SessionRecord struct {
	SessionID       uint32           `json:"session_id"`
	LogonAtMS       *int64           `json:"logon_at_ms"`
	User            *string          `json:"user"`
	Domain          *string          `json:"domain"`
	State           SessionState     `json:"state"`
	Station         *string          `json:"station"`
	ClientName      *string          `json:"client_name"`
	ClientAddress   *string          `json:"client_address"`
	ConnectAtMS     *int64           `json:"connect_at_ms"`
	DisconnectAtMS  *int64           `json:"disconnect_at_ms"`
	IdleSinceMS     *int64           `json:"idle_since_ms"`
	CPUPercent      *float64         `json:"cpu_percent"`
	WorkingSetBytes *DecimalUint64   `json:"working_set_bytes"`
	InputDelayMS    *uint32          `json:"input_delay_ms"`
	RemoteFX        *RemoteFXMetrics `json:"remotefx"`
	Processes       []SessionProcess `json:"processes"`
}

type RemoteFXMetrics struct {
	FPS               *float64 `json:"fps"`
	QualityPercent    *float64 `json:"quality_percent"`
	EncodeTimeMS      *float64 `json:"encode_time_ms"`
	RTTMS             *float64 `json:"rtt_ms"`
	LossPercent       *float64 `json:"loss_percent"`
	ServerSkippedFPS  *float64 `json:"server_skipped_fps"`
	NetworkSkippedFPS *float64 `json:"network_skipped_fps"`
}

type SessionProcess struct {
	PID             uint32        `json:"pid"`
	ImageName       string        `json:"image_name"`
	CPUPercent      *float64      `json:"cpu_percent"`
	WorkingSetBytes DecimalUint64 `json:"working_set_bytes"`
}

// UnmarshalJSON requires every snapshot transport property to be explicitly
// represented. Pointer fields may be present with a JSON null value.
func (snapshot *SessionSnapshot) UnmarshalJSON(data []byte) error {
	fields, err := requiredJSONObject(data, "snapshot", []string{
		"schema", "host", "agent_instance_id", "sequence", "observed_at_ms",
		"collector_version", "logical_cpu_count", "capabilities",
		"collection_error", "sessions",
	})
	if err != nil {
		return err
	}
	var decoded SessionSnapshot
	if err := decodeRequiredJSONField(fields, "schema", &decoded.Schema); err != nil {
		return err
	}
	if err := decodeRequiredJSONField(fields, "host", &decoded.Host); err != nil {
		return err
	}
	if err := decodeRequiredJSONField(fields, "agent_instance_id", &decoded.AgentInstanceID); err != nil {
		return err
	}
	if err := decodeRequiredJSONField(fields, "sequence", &decoded.Sequence); err != nil {
		return err
	}
	if err := decodeRequiredJSONField(fields, "observed_at_ms", &decoded.ObservedAtMS); err != nil {
		return err
	}
	if err := decodeRequiredJSONField(fields, "collector_version", &decoded.CollectorVersion); err != nil {
		return err
	}
	if err := decodeRequiredJSONField(fields, "logical_cpu_count", &decoded.LogicalCPUCount); err != nil {
		return err
	}
	if err := decodeRequiredJSONField(fields, "capabilities", &decoded.Capabilities); err != nil {
		return err
	}
	if err := decodeNullableJSONField(fields, "collection_error", &decoded.CollectionError); err != nil {
		return err
	}
	if err := decodeRequiredJSONArray(fields, "sessions", &decoded.Sessions); err != nil {
		return err
	}
	*snapshot = decoded
	return nil
}

func (capabilities *SessionCapabilities) UnmarshalJSON(data []byte) error {
	fields, err := requiredJSONObject(data, "capabilities", []string{
		"session_actions", "processes", "input_delay", "remotefx",
	})
	if err != nil {
		return err
	}
	var decoded SessionCapabilities
	for _, field := range []struct {
		name   string
		target *bool
	}{
		{"session_actions", &decoded.SessionActions},
		{"processes", &decoded.Processes},
		{"input_delay", &decoded.InputDelay},
		{"remotefx", &decoded.RemoteFX},
	} {
		if err := decodeRequiredJSONField(fields, field.name, field.target); err != nil {
			return err
		}
	}
	*capabilities = decoded
	return nil
}

func (errorValue *CollectionError) UnmarshalJSON(data []byte) error {
	fields, err := requiredJSONObject(data, "collection error", []string{"code"})
	if err != nil {
		return err
	}
	var decoded CollectionError
	if err := decodeRequiredJSONField(fields, "code", &decoded.Code); err != nil {
		return err
	}
	*errorValue = decoded
	return nil
}

func (session *SessionRecord) UnmarshalJSON(data []byte) error {
	fields, err := requiredJSONObject(data, "session", []string{
		"session_id", "logon_at_ms", "user", "domain", "state", "station",
		"client_name", "client_address", "connect_at_ms", "disconnect_at_ms",
		"idle_since_ms", "cpu_percent", "working_set_bytes", "input_delay_ms",
		"remotefx", "processes",
	})
	if err != nil {
		return err
	}
	var decoded SessionRecord
	for _, field := range []struct {
		name   string
		target any
	}{
		{"session_id", &decoded.SessionID},
		{"state", &decoded.State},
	} {
		if err := decodeRequiredJSONField(fields, field.name, field.target); err != nil {
			return err
		}
	}
	for _, field := range []struct {
		name   string
		target any
	}{
		{"logon_at_ms", &decoded.LogonAtMS},
		{"user", &decoded.User},
		{"domain", &decoded.Domain},
		{"station", &decoded.Station},
		{"client_name", &decoded.ClientName},
		{"client_address", &decoded.ClientAddress},
		{"connect_at_ms", &decoded.ConnectAtMS},
		{"disconnect_at_ms", &decoded.DisconnectAtMS},
		{"idle_since_ms", &decoded.IdleSinceMS},
		{"cpu_percent", &decoded.CPUPercent},
		{"working_set_bytes", &decoded.WorkingSetBytes},
		{"input_delay_ms", &decoded.InputDelayMS},
		{"remotefx", &decoded.RemoteFX},
	} {
		if err := decodeNullableJSONField(fields, field.name, field.target); err != nil {
			return err
		}
	}
	if err := decodeRequiredJSONArray(fields, "processes", &decoded.Processes); err != nil {
		return err
	}
	*session = decoded
	return nil
}

func (metrics *RemoteFXMetrics) UnmarshalJSON(data []byte) error {
	fields, err := requiredJSONObject(data, "RemoteFX metrics", []string{
		"fps", "quality_percent", "encode_time_ms", "rtt_ms", "loss_percent",
		"server_skipped_fps", "network_skipped_fps",
	})
	if err != nil {
		return err
	}
	var decoded RemoteFXMetrics
	for _, field := range []struct {
		name   string
		target any
	}{
		{"fps", &decoded.FPS},
		{"quality_percent", &decoded.QualityPercent},
		{"encode_time_ms", &decoded.EncodeTimeMS},
		{"rtt_ms", &decoded.RTTMS},
		{"loss_percent", &decoded.LossPercent},
		{"server_skipped_fps", &decoded.ServerSkippedFPS},
		{"network_skipped_fps", &decoded.NetworkSkippedFPS},
	} {
		if err := decodeNullableJSONField(fields, field.name, field.target); err != nil {
			return err
		}
	}
	*metrics = decoded
	return nil
}

func (process *SessionProcess) UnmarshalJSON(data []byte) error {
	fields, err := requiredJSONObject(data, "process", []string{
		"pid", "image_name", "cpu_percent", "working_set_bytes",
	})
	if err != nil {
		return err
	}
	var decoded SessionProcess
	if err := decodeRequiredJSONField(fields, "pid", &decoded.PID); err != nil {
		return err
	}
	if err := decodeRequiredJSONField(fields, "image_name", &decoded.ImageName); err != nil {
		return err
	}
	if err := decodeNullableJSONField(fields, "cpu_percent", &decoded.CPUPercent); err != nil {
		return err
	}
	if err := decodeRequiredJSONField(fields, "working_set_bytes", &decoded.WorkingSetBytes); err != nil {
		return err
	}
	*process = decoded
	return nil
}

func requiredJSONObject(data []byte, name string, required []string) (map[string]json.RawMessage, error) {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil, fmt.Errorf("%s must not be null", name)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("%s must be an object: %w", name, err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, fmt.Errorf("%s must be an object", name)
	}

	fields := make(map[string]json.RawMessage, len(required))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("%s is invalid: %w", name, err)
		}
		field, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("%s is invalid", name)
		}
		if _, exists := fields[field]; exists {
			return nil, fmt.Errorf("%s.%s is duplicated", name, field)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("%s.%s is invalid: %w", name, field, err)
		}
		fields[field] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("%s is invalid: %w", name, err)
	}
	if token, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("%s is invalid: %w", name, err)
		}
		return nil, fmt.Errorf("%s has trailing JSON after %v", name, token)
	}

	for _, field := range required {
		if _, ok := fields[field]; !ok {
			return nil, fmt.Errorf("%s.%s is required", name, field)
		}
	}
	allowed := make(map[string]struct{}, len(required))
	for _, field := range required {
		allowed[field] = struct{}{}
	}
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			return nil, fmt.Errorf("%s.%s is unknown", name, field)
		}
	}
	return fields, nil
}

func decodeRequiredJSONField(fields map[string]json.RawMessage, name string, target any) error {
	data := fields[name]
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("%s must not be null", name)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("%s is invalid: %w", name, err)
	}
	return nil
}

func decodeNullableJSONField(fields map[string]json.RawMessage, name string, target any) error {
	if err := json.Unmarshal(fields[name], target); err != nil {
		return fmt.Errorf("%s is invalid: %w", name, err)
	}
	return nil
}

func decodeRequiredJSONArray(fields map[string]json.RawMessage, name string, target any) error {
	data := bytes.TrimSpace(fields[name])
	if len(data) == 0 || data[0] != '[' {
		return fmt.Errorf("%s must be an array", name)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("%s is invalid: %w", name, err)
	}
	return nil
}

type Visibility string

const (
	VisibilityFull   Visibility = "full"
	VisibilityMasked Visibility = "masked"
	VisibilityHidden Visibility = "hidden"
)

type PrivacyPolicy struct {
	Identity Visibility
	Client   Visibility
	Process  Visibility
}

type ProjectedSessionRecord = SessionRecord
type ProjectedSessionProcess = SessionProcess

// ProjectSnapshot applies privacy before data is persisted or serialized elsewhere.
func ProjectSnapshot(snapshot SessionSnapshot, policy PrivacyPolicy) SessionSnapshot {
	projected := snapshot
	projected.Sessions = make([]SessionRecord, len(snapshot.Sessions))
	for i, session := range snapshot.Sessions {
		projected.Sessions[i] = ProjectSession(session, policy)
	}
	return projected
}

// CompactSnapshotForWire makes an owned snapshot fit the decoded-body limit
// without omitting a session. It first removes optional, variable-size
// enrichment. It preserves the session ID, logon time, and state; if those
// details are still too large, identity remains present as a bounded prefix.
// No successful compaction omits a session row.
//
// Callers retain ownership of the supplied snapshot's Sessions backing array;
// this function may reuse it while compacting.
func CompactSnapshotForWire(snapshot SessionSnapshot) (SessionSnapshot, error) {
	if len(snapshot.Sessions) > MaxSessions {
		return SessionSnapshot{}, fmt.Errorf("snapshot has %d sessions; limit is %d", len(snapshot.Sessions), MaxSessions)
	}
	if payload, err := json.Marshal(snapshot); err != nil {
		return SessionSnapshot{}, fmt.Errorf("marshal snapshot: %w", err)
	} else if len(payload) <= MaxSnapshotBytes {
		return snapshot, nil
	}

	for _, clear := range []func(*SessionRecord){
		func(session *SessionRecord) { session.Processes = []SessionProcess{} },
		func(session *SessionRecord) { session.RemoteFX = nil },
		func(session *SessionRecord) { session.CPUPercent = nil },
		func(session *SessionRecord) { session.WorkingSetBytes = nil },
		func(session *SessionRecord) { session.InputDelayMS = nil },
		func(session *SessionRecord) { session.Station = nil },
		func(session *SessionRecord) { session.ClientName = nil },
		func(session *SessionRecord) { session.ClientAddress = nil },
		func(session *SessionRecord) { session.ConnectAtMS = nil },
		func(session *SessionRecord) { session.DisconnectAtMS = nil },
		func(session *SessionRecord) { session.IdleSinceMS = nil },
	} {
		for i := range snapshot.Sessions {
			clear(&snapshot.Sessions[i])
		}
		payload, err := json.Marshal(snapshot)
		if err != nil {
			return SessionSnapshot{}, fmt.Errorf("marshal compacted snapshot: %w", err)
		}
		if len(payload) <= MaxSnapshotBytes {
			return snapshot, nil
		}
	}
	for i := range snapshot.Sessions {
		snapshot.Sessions[i].User = truncateSnapshotText(snapshot.Sessions[i].User, 32)
		snapshot.Sessions[i].Domain = truncateSnapshotText(snapshot.Sessions[i].Domain, 32)
	}
	if payload, err := json.Marshal(snapshot); err != nil {
		return SessionSnapshot{}, fmt.Errorf("marshal compacted snapshot: %w", err)
	} else if len(payload) <= MaxSnapshotBytes {
		return snapshot, nil
	}
	return SessionSnapshot{}, fmt.Errorf("snapshot exceeds %d-byte limit after compaction", MaxSnapshotBytes)
}

// truncateSnapshotText keeps the beginning of a UTF-8 value without splitting
// a code point. It is used only after all non-core session detail is removed.
func truncateSnapshotText(value *string, maxBytes int) *string {
	if value == nil || len(*value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && (*value)[end]&0xc0 == 0x80 {
		end--
	}
	if end == 0 {
		return nil
	}
	result := (*value)[:end]
	return &result
}
func ProjectSession(session SessionRecord, policy PrivacyPolicy) SessionRecord {
	projected := session
	projected.User, projected.Domain = projectOptionalPair(session.User, session.Domain, policy.Identity)
	projected.Station = projectOptional(session.Station, policy.Client)
	projected.ClientName = projectOptional(session.ClientName, policy.Client)
	projected.ClientAddress = projectOptional(session.ClientAddress, policy.Client)
	if policy.Process == VisibilityHidden {
		projected.Processes = []SessionProcess{}
		return projected
	}
	projected.Processes = make([]SessionProcess, len(session.Processes))
	copy(projected.Processes, session.Processes)
	if policy.Process == VisibilityMasked {
		for i := range projected.Processes {
			projected.Processes[i].ImageName = "***"
		}
	}
	return projected
}
func projectOptional(value *string, visibility Visibility) *string {
	if value == nil || visibility == VisibilityHidden {
		return nil
	}
	if visibility == VisibilityMasked {
		masked := "***"
		return &masked
	}
	return value
}
func projectOptionalPair(first, second *string, visibility Visibility) (*string, *string) {
	return projectOptional(first, visibility), projectOptional(second, visibility)
}

type SessionAggregates struct {
	SessionCount, ActiveCount, IdleCount, DisconnectedCount, UserCount int
	LastActivityAtMS                                                   *int64
}

func CalculateAggregates(sessions []SessionRecord) SessionAggregates {
	result := SessionAggregates{SessionCount: len(sessions)}
	users := make(map[string]struct{})
	for _, session := range sessions {
		switch session.State {
		case SessionActive, SessionConnected:
			result.ActiveCount++
		case SessionIdle:
			result.IdleCount++
		case SessionDisconnected:
			result.DisconnectedCount++
		}
		if session.User != nil && session.Domain != nil {
			users[*session.User+"\x00"+*session.Domain] = struct{}{}
		}
		for _, time := range []*int64{session.IdleSinceMS, session.ConnectAtMS, session.LogonAtMS} {
			if time != nil && (result.LastActivityAtMS == nil || *time > *result.LastActivityAtMS) {
				value := *time
				result.LastActivityAtMS = &value
			}
		}
	}
	result.UserCount = len(users)
	return result
}

// CalculateAggregatesForIdentity preserves aggregate user counts for full and
// masked identities, while never reporting an identity-derived count when
// identities are hidden. Callers must pass unprojected session records.
func CalculateAggregatesForIdentity(sessions []SessionRecord, identity Visibility) SessionAggregates {
	aggregates := CalculateAggregates(sessions)
	if identity == VisibilityHidden {
		aggregates.UserCount = 0
	}
	return aggregates
}

type SessionActionType string

const (
	SessionActionLogoff     SessionActionType = "logoff"
	SessionActionMessage    SessionActionType = "message"
	SessionActionDisconnect SessionActionType = "disconnect"
)

type SessionActionState string

const (
	SessionActionQueued         SessionActionState = "queued"
	SessionActionDelivered      SessionActionState = "delivered"
	SessionActionCompleted      SessionActionState = "completed"
	SessionActionFailed         SessionActionState = "failed"
	SessionActionExpired        SessionActionState = "expired"
	SessionActionSessionChanged SessionActionState = "session_changed"
	SessionActionUnsupported    SessionActionState = "unsupported"
)

type SessionAction struct {
	ActionID           string
	CanonicalHost      string
	SessionID          uint32
	ExpectedLogonAtMS  int64
	Type               SessionActionType
	State              SessionActionState
	CreatedAtMS        int64
	ExpiresAtMS        int64
	RequestedBy        string
	IdempotencyKey     string
	RequestFingerprint []byte
}
type SessionActionStatus struct {
	ActionID          string             `json:"action_id"`
	Type              SessionActionType  `json:"type"`
	CanonicalHost     string             `json:"host"`
	SessionID         uint32             `json:"session_id"`
	ExpectedLogonAtMS int64              `json:"expected_logon_at_ms"`
	State             SessionActionState `json:"state"`
	CreatedAtMS       int64              `json:"created_at_ms"`
	ExpiresAtMS       int64              `json:"expires_at_ms"`
	CompletedAtMS     *int64             `json:"completed_at_ms"`
	ResultCode        *string            `json:"result_code"`
}
type SessionActionLedgerState string

const (
	SessionActionLedgerClaimed  SessionActionLedgerState = "claimed"
	SessionActionLedgerTerminal SessionActionLedgerState = "terminal"
)

type SessionActionOutcome string

const (
	SessionActionOutcomeCompleted      SessionActionOutcome = "completed"
	SessionActionOutcomeFailed         SessionActionOutcome = "failed"
	SessionActionOutcomeExpired        SessionActionOutcome = "expired"
	SessionActionOutcomeSessionChanged SessionActionOutcome = "session_changed"
	SessionActionOutcomeUnsupported    SessionActionOutcome = "unsupported"
	SessionActionOutcomeDuplicate      SessionActionOutcome = "duplicate"
)

type SessionActionLedgerEntry struct {
	ActionID      string
	State         SessionActionLedgerState
	Outcome       *SessionActionOutcome
	ClaimedAtMS   int64
	CompletedAtMS *int64
	ExpiresAtMS   int64
}

type ActionRequest struct {
	Type              SessionActionType `json:"type"`
	ExpectedLogonAtMS int64             `json:"expected_logon_at_ms"`
	Message           *string           `json:"message"`
}
type NormalizedActionRequest struct {
	Type              SessionActionType
	ExpectedLogonAtMS int64
	Message           string
}

func NormalizeActionRequest(request ActionRequest) (NormalizedActionRequest, error) {
	if request.ExpectedLogonAtMS < 0 || request.ExpectedLogonAtMS > MaxUnixMillis {
		return NormalizedActionRequest{}, fmt.Errorf("expected logon time is invalid")
	}
	result := NormalizedActionRequest{Type: request.Type, ExpectedLogonAtMS: request.ExpectedLogonAtMS}
	switch request.Type {
	case SessionActionLogoff, SessionActionDisconnect:
		if request.Message != nil && *request.Message != "" {
			return result, fmt.Errorf("%s message must be empty", request.Type)
		}
		return result, nil
	case SessionActionMessage:
		if request.Message == nil {
			return result, fmt.Errorf("message is required")
		}
		result.Message = strings.TrimSpace(*request.Message)
		if err := validateActionMessage(result.Message); err != nil {
			return result, err
		}
		return result, nil
	default:
		return result, fmt.Errorf("action type is invalid")
	}
}
func (request NormalizedActionRequest) Fingerprint() [sha256.Size]byte {
	// Length prefixes make different field boundaries unambiguous.
	payload := make([]byte, 0, len(request.Type)+len(request.Message)+32)
	for _, value := range []string{string(request.Type), strconv.FormatInt(request.ExpectedLogonAtMS, 10), request.Message} {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(value)))
		payload = append(payload, length[:]...)
		payload = append(payload, value...)
	}
	return sha256.Sum256(payload)
}
func ShadowCommand(host string, sessionID uint32) (string, error) {
	if err := ValidateCanonicalHost(host); err != nil {
		return "", err
	}
	return "mstsc.exe /v:" + host + " /shadow:" + strconv.FormatUint(uint64(sessionID), 10) + " /control", nil
}
