package sessiondata

import (
	"fmt"
	"math"
	"net"
	"strings"
	"unicode/utf8"
)

func ValidateCanonicalHost(host string) error {
	if len(host) < 1 || len(host) > 253 || host != strings.ToLower(host) || strings.TrimSpace(host) != host {
		return fmt.Errorf("host is not canonical")
	}
	if net.ParseIP(host) != nil {
		return fmt.Errorf("host must be an RFC 1123 name")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("host is not an RFC 1123 name")
		}
		for i := range label {
			if label[i] == '-' {
				continue
			}
			if (label[i] < 'a' || label[i] > 'z') && (label[i] < '0' || label[i] > '9') {
				return fmt.Errorf("host is not an RFC 1123 name")
			}
		}
	}
	return nil
}

func ValidateSnapshot(snapshot SessionSnapshot) error { return snapshot.Validate() }
func (snapshot SessionSnapshot) Validate() error {
	if snapshot.Schema != SnapshotSchema {
		return fmt.Errorf("schema is invalid")
	}
	if err := ValidateCanonicalHost(snapshot.Host); err != nil {
		return err
	}
	if _, err := ParseUUIDv7(snapshot.AgentInstanceID); err != nil {
		return fmt.Errorf("agent instance ID is invalid: %w", err)
	}
	if err := validateTimestamp(snapshot.ObservedAtMS); err != nil {
		return fmt.Errorf("observed time: %w", err)
	}
	if err := validateBoundedText(snapshot.CollectorVersion, 1, 64, false); err != nil {
		return fmt.Errorf("collector version: %w", err)
	}
	if snapshot.LogicalCPUCount < 1 || snapshot.LogicalCPUCount > 1024 {
		return fmt.Errorf("logical CPU count is invalid")
	}
	if len(snapshot.Sessions) > MaxSessions {
		return fmt.Errorf("too many sessions")
	}
	if snapshot.CollectionError != nil {
		if !IsSafeCollectionErrorCode(snapshot.CollectionError.Code) {
			return fmt.Errorf("collection error code is invalid")
		}
		if len(snapshot.Sessions) != 0 || snapshot.Capabilities != (SessionCapabilities{}) {
			return fmt.Errorf("fatal snapshot contains data")
		}
		return nil
	}
	seen := make(map[uint32]struct{}, len(snapshot.Sessions))
	for i := range snapshot.Sessions {
		session := &snapshot.Sessions[i]
		if _, duplicate := seen[session.SessionID]; duplicate {
			return fmt.Errorf("duplicate session ID")
		}
		seen[session.SessionID] = struct{}{}
		if err := session.validate(snapshot.ObservedAtMS, snapshot.LogicalCPUCount); err != nil {
			return fmt.Errorf("session %d: %w", session.SessionID, err)
		}
	}
	return nil
}

func (session SessionRecord) validate(observedAtMS int64, cpuCount uint16) error {
	if !session.State.Valid() {
		return fmt.Errorf("state is invalid")
	}
	if err := validateNullableTimestamp(session.LogonAtMS); err != nil {
		return fmt.Errorf("logon time: %w", err)
	}
	if session.LogonAtMS != nil && *session.LogonAtMS > observedAtMS {
		return fmt.Errorf("logon time is after observed time")
	}
	for _, timestamp := range []struct {
		name  string
		value *int64
	}{
		{"connect time", session.ConnectAtMS},
		{"disconnect time", session.DisconnectAtMS},
		{"idle time", session.IdleSinceMS},
	} {
		if err := validateNullableTimestamp(timestamp.value); err != nil {
			return fmt.Errorf("%s: %w", timestamp.name, err)
		}
		if timestamp.value != nil && *timestamp.value > observedAtMS {
			return fmt.Errorf("%s is after observed time", timestamp.name)
		}
	}
	for name, value := range map[string]*string{"user": session.User, "domain": session.Domain, "station": session.Station, "client name": session.ClientName} {
		if value != nil {
			if err := validateBoundedText(*value, 0, 256, false); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	if session.ClientAddress != nil {
		if err := validateBoundedText(*session.ClientAddress, 0, 128, false); err != nil {
			return fmt.Errorf("client address: %w", err)
		}
	}
	if err := validatePercent(session.CPUPercent, 0, float64(cpuCount)*100); err != nil {
		return fmt.Errorf("CPU percent: %w", err)
	}
	if session.WorkingSetBytes != nil && uint64(*session.WorkingSetBytes) > MaxStoredBytes {
		return fmt.Errorf("working set is above SQLite maximum")
	}
	if session.InputDelayMS != nil && *session.InputDelayMS > 600000 {
		return fmt.Errorf("input delay is invalid")
	}
	if session.RemoteFX != nil {
		if err := session.RemoteFX.validate(); err != nil {
			return err
		}
	}
	if len(session.Processes) > MaxProcesses {
		return fmt.Errorf("too many processes")
	}
	seen := make(map[uint32]struct{}, len(session.Processes))
	for _, process := range session.Processes {
		if process.PID == 0 {
			return fmt.Errorf("process PID is zero")
		}
		if _, duplicate := seen[process.PID]; duplicate {
			return fmt.Errorf("duplicate process PID")
		}
		seen[process.PID] = struct{}{}
		if err := process.validate(cpuCount); err != nil {
			return err
		}
	}
	return nil
}
func (metrics RemoteFXMetrics) validate() error {
	for _, check := range []struct {
		name         string
		value        *float64
		min, max     float64
		exclusiveMin bool
	}{{"fps", metrics.FPS, 0, 240, true}, {"quality", metrics.QualityPercent, 0, 100, true}, {"encode", metrics.EncodeTimeMS, 0, 60000, false}, {"rtt", metrics.RTTMS, 0, 60000, false}, {"loss", metrics.LossPercent, 0, 100, false}, {"server skipped", metrics.ServerSkippedFPS, 0, 1000000, false}, {"network skipped", metrics.NetworkSkippedFPS, 0, 1000000, false}} {
		if check.value != nil {
			if math.IsNaN(*check.value) || math.IsInf(*check.value, 0) || *check.value > check.max || *check.value < check.min || (check.exclusiveMin && *check.value == check.min) {
				return fmt.Errorf("RemoteFX %s is invalid", check.name)
			}
		}
	}
	return nil
}
func (process SessionProcess) validate(cpuCount uint16) error {
	if err := validateBoundedText(process.ImageName, 1, 260, false); err != nil {
		return fmt.Errorf("image name: %w", err)
	}
	if strings.ContainsAny(process.ImageName, `/\\:`) {
		return fmt.Errorf("image name is not a base filename")
	}
	if uint64(process.WorkingSetBytes) > MaxStoredBytes {
		return fmt.Errorf("process working set is above SQLite maximum")
	}
	return validatePercent(process.CPUPercent, 0, float64(cpuCount)*100)
}
func validatePercent(value *float64, min, max float64) error {
	if value == nil {
		return nil
	}
	if math.IsNaN(*value) || math.IsInf(*value, 0) || *value < min || *value > max {
		return fmt.Errorf("percentage is invalid")
	}
	return nil
}
func validateTimestamp(value int64) error {
	if value < 0 || value > MaxUnixMillis {
		return fmt.Errorf("timestamp is invalid")
	}
	return nil
}
func validateNullableTimestamp(value *int64) error {
	if value == nil {
		return nil
	}
	return validateTimestamp(*value)
}
func validateBoundedText(value string, min, max int, allowNewline bool) error {
	if !utf8.ValidString(value) || len(value) < min || len(value) > max {
		return fmt.Errorf("text length or UTF-8 is invalid")
	}
	for _, rune := range value {
		if rune == 0 || rune == 0x7f || rune < 0x20 && (!allowNewline || rune != '\n') {
			return fmt.Errorf("text contains a control character")
		}
	}
	return nil
}
func validateActionMessage(message string) error {
	if !utf8.ValidString(message) || utf8.RuneCountInString(message) < 1 || utf8.RuneCountInString(message) > 256 {
		return fmt.Errorf("message length or UTF-8 is invalid")
	}
	for _, rune := range message {
		if rune == 0 || rune == 0x7f || rune < 0x20 && rune != '\n' {
			return fmt.Errorf("message contains a control character")
		}
	}
	return nil
}
func IsSafeCollectionErrorCode(code string) bool {
	switch code {
	case CollectionErrorWTSEnumerationFailed, CollectionErrorWTSMetadataFailed, CollectionErrorTimeout:
		return true
	default:
		return false
	}
}
func IsSafeActionResultCode(code string) bool {
	switch code {
	case "completed", "failed", "expired", "session_changed", "unsupported", "duplicate", "privacy_policy_changed":
		return true
	default:
		return false
	}
}
func (state SessionActionState) Valid() bool {
	switch state {
	case SessionActionQueued, SessionActionDelivered, SessionActionCompleted, SessionActionFailed, SessionActionExpired, SessionActionSessionChanged, SessionActionUnsupported:
		return true
	default:
		return false
	}
}
func (outcome SessionActionOutcome) Valid() bool {
	return IsSafeActionResultCode(string(outcome)) && outcome != "privacy_policy_changed"
}
