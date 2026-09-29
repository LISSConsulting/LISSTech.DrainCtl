package sessiondata

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func validSnapshot() SessionSnapshot {
	return SessionSnapshot{Schema: SnapshotSchema, Host: "rdsh-07.example.test", AgentInstanceID: "0195a584-5b25-7a00-91a5-7cbb4dac92d9", Sequence: 1, ObservedAtMS: 1, CollectorVersion: "1.8.0", LogicalCPUCount: 8, Sessions: []SessionRecord{}}
}

func TestDecimalUint64JSONCanonical(t *testing.T) {
	for _, value := range []DecimalUint64{0, 1, math.MaxUint64} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var decoded DecimalUint64
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded != value {
			t.Fatalf("round trip %d = %d", value, decoded)
		}
	}
	for _, input := range []string{`0`, `""`, `"00"`, `"01"`, `"+1"`, `" 1"`, `"1 "`, `"1.0"`, `"1e3"`, `"-1"`, `"18446744073709551616"`} {
		var value DecimalUint64
		if err := json.Unmarshal([]byte(input), &value); err == nil {
			t.Errorf("%s accepted", input)
		}
	}
}

func TestSessionSnapshotJSONRequiresEveryDeclaredProperty(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Sessions = []SessionRecord{{
		SessionID: 1,
		State:     SessionActive,
		Processes: []SessionProcess{{PID: 2, ImageName: "app.exe"}},
		RemoteFX:  &RemoteFXMetrics{},
	}}

	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var complete SessionSnapshot
	if err := json.Unmarshal(payload, &complete); err != nil {
		t.Fatalf("rejected complete snapshot with nullable nulls: %v", err)
	}
	if err := complete.Validate(); err != nil {
		t.Fatalf("complete snapshot is invalid: %v", err)
	}

	decode := func(t *testing.T, mutate func(map[string]json.RawMessage)) error {
		t.Helper()
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(payload, &fields); err != nil {
			t.Fatal(err)
		}
		mutate(fields)
		body, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		var decoded SessionSnapshot
		return json.Unmarshal(body, &decoded)
	}
	object := func(t *testing.T, data json.RawMessage) map[string]json.RawMessage {
		t.Helper()
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		return fields
	}
	storeObject := func(t *testing.T, fields map[string]json.RawMessage, name string, object map[string]json.RawMessage) {
		t.Helper()
		data, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		fields[name] = data
	}

	for _, field := range []string{
		"schema", "host", "agent_instance_id", "sequence", "observed_at_ms",
		"collector_version", "logical_cpu_count", "capabilities",
		"collection_error", "sessions",
	} {
		t.Run("top-level/"+field, func(t *testing.T) {
			if err := decode(t, func(fields map[string]json.RawMessage) { delete(fields, field) }); err == nil {
				t.Fatalf("accepted omitted %s", field)
			}
		})
	}
	for _, field := range []string{"session_actions", "processes", "input_delay", "remotefx"} {
		t.Run("capabilities/"+field, func(t *testing.T) {
			if err := decode(t, func(fields map[string]json.RawMessage) {
				capabilities := object(t, fields["capabilities"])
				delete(capabilities, field)
				storeObject(t, fields, "capabilities", capabilities)
			}); err == nil {
				t.Fatalf("accepted omitted capability %s", field)
			}
		})
	}
	for _, field := range []string{
		"session_id", "logon_at_ms", "user", "domain", "state", "station",
		"client_name", "client_address", "connect_at_ms", "disconnect_at_ms",
		"idle_since_ms", "cpu_percent", "working_set_bytes", "input_delay_ms",
		"remotefx", "processes",
	} {
		t.Run("session/"+field, func(t *testing.T) {
			if err := decode(t, func(fields map[string]json.RawMessage) {
				var sessions []json.RawMessage
				if err := json.Unmarshal(fields["sessions"], &sessions); err != nil {
					t.Fatal(err)
				}
				session := object(t, sessions[0])
				delete(session, field)
				data, err := json.Marshal(session)
				if err != nil {
					t.Fatal(err)
				}
				sessions[0] = data
				data, err = json.Marshal(sessions)
				if err != nil {
					t.Fatal(err)
				}
				fields["sessions"] = data
			}); err == nil {
				t.Fatalf("accepted omitted session %s", field)
			}
		})
	}
	for _, field := range []string{"pid", "image_name", "cpu_percent", "working_set_bytes"} {
		t.Run("process/"+field, func(t *testing.T) {
			if err := decode(t, func(fields map[string]json.RawMessage) {
				var sessions []json.RawMessage
				if err := json.Unmarshal(fields["sessions"], &sessions); err != nil {
					t.Fatal(err)
				}
				session := object(t, sessions[0])
				var processes []json.RawMessage
				if err := json.Unmarshal(session["processes"], &processes); err != nil {
					t.Fatal(err)
				}
				process := object(t, processes[0])
				delete(process, field)
				data, err := json.Marshal(process)
				if err != nil {
					t.Fatal(err)
				}
				processes[0] = data
				data, err = json.Marshal(processes)
				if err != nil {
					t.Fatal(err)
				}
				session["processes"] = data
				data, err = json.Marshal(session)
				if err != nil {
					t.Fatal(err)
				}
				sessions[0] = data
				data, err = json.Marshal(sessions)
				if err != nil {
					t.Fatal(err)
				}
				fields["sessions"] = data
			}); err == nil {
				t.Fatalf("accepted omitted process %s", field)
			}
		})
	}
	for _, field := range []string{
		"fps", "quality_percent", "encode_time_ms", "rtt_ms", "loss_percent",
		"server_skipped_fps", "network_skipped_fps",
	} {
		t.Run("remotefx/"+field, func(t *testing.T) {
			if err := decode(t, func(fields map[string]json.RawMessage) {
				var sessions []json.RawMessage
				if err := json.Unmarshal(fields["sessions"], &sessions); err != nil {
					t.Fatal(err)
				}
				session := object(t, sessions[0])
				metrics := object(t, session["remotefx"])
				delete(metrics, field)
				data, err := json.Marshal(metrics)
				if err != nil {
					t.Fatal(err)
				}
				session["remotefx"] = data
				data, err = json.Marshal(session)
				if err != nil {
					t.Fatal(err)
				}
				sessions[0] = data
				data, err = json.Marshal(sessions)
				if err != nil {
					t.Fatal(err)
				}
				fields["sessions"] = data
			}); err == nil {
				t.Fatalf("accepted omitted RemoteFX metric %s", field)
			}
		})
	}
	t.Run("collection error/code", func(t *testing.T) {
		if err := decode(t, func(fields map[string]json.RawMessage) {
			fields["collection_error"] = json.RawMessage(`{}`)
		}); err == nil {
			t.Fatal("accepted collection error without code")
		}
	})
}

func TestSessionSnapshotJSONAcceptsExplicitNullsAndRequiredEmptyArrays(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Capabilities = SessionCapabilities{}
	snapshot.Sessions = []SessionRecord{{
		SessionID: 1,
		State:     SessionActive,
		Processes: []SessionProcess{},
	}}

	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var decoded SessionSnapshot
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("rejected explicit nullable nulls and empty arrays: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded snapshot is invalid: %v", err)
	}
	if decoded.Sessions == nil || decoded.Sessions[0].Processes == nil {
		t.Fatal("required empty arrays decoded as null")
	}
}

func TestSessionSnapshotJSONRejectsUnknownAndDuplicateProperties(t *testing.T) {
	snapshot := validSnapshot()
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range [][]byte{
		append(append([]byte{}, payload[:len(payload)-1]...), []byte(`,"unknown":true}`)...),
		append(append([]byte{}, payload[:len(payload)-1]...), []byte(`,"host":"rdsh-07.example.test"}`)...),
	} {
		var decoded SessionSnapshot
		if err := json.Unmarshal(body, &decoded); err == nil {
			t.Fatalf("accepted invalid properties: %s", body)
		}
	}
}
func TestUUIDv7ParseFormatCompare(t *testing.T) {
	const firstText = "0195a584-5b25-7a00-91a5-7cbb4dac92d9"
	const secondText = "0195a584-5b26-7fff-bfff-ffffffffffff"

	first, err := ParseUUIDv7(firstText)
	if err != nil {
		t.Fatalf("parse first UUIDv7: %v", err)
	}
	if got := first.String(); got != firstText {
		t.Fatalf("round trip UUIDv7 = %q, want %q", got, firstText)
	}
	second, err := ParseUUIDv7(secondText)
	if err != nil {
		t.Fatalf("parse second UUIDv7: %v", err)
	}
	if got := CompareUUIDv7(first, second); got >= 0 {
		t.Fatalf("ordered UUIDv7 comparison = %d, want negative", got)
	}
	if got := CompareUUIDv7(second, first); got <= 0 {
		t.Fatalf("reversed UUIDv7 comparison = %d, want positive", got)
	}
	if got := CompareUUIDv7(first, first); got != 0 {
		t.Fatalf("equal UUIDv7 comparison = %d, want zero", got)
	}
}

func TestUUIDv7RejectsMalformedVersionAndVariant(t *testing.T) {
	for _, text := range []string{
		"",
		"0195a5845b257a0091a57cbb4dac92d9",
		"0195a584-5b25-6a00-91a5-7cbb4dac92d9",
		"0195a584-5b25-7a00-71a5-7cbb4dac92d9",
		"0195a584-5b25-7a00-91a5-7cbb4dac92dz",
	} {
		if _, err := ParseUUIDv7(text); err == nil {
			t.Errorf("accepted invalid UUIDv7 %q", text)
		}
	}

	snapshot := validSnapshot()
	snapshot.AgentInstanceID = "0195a584-5b25-4a00-91a5-7cbb4dac92d9"
	if err := snapshot.Validate(); err == nil {
		t.Fatal("accepted non-v7 agent instance ID")
	}
	snapshot.AgentInstanceID = "0195a584-5b25-7a00-71a5-7cbb4dac92d9"
	if err := snapshot.Validate(); err == nil {
		t.Fatal("accepted non-RFC-variant agent instance ID")
	}
}

func TestSnapshotValidationBoundaries(t *testing.T) {
	snapshot := validSnapshot()
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("valid snapshot: %v", err)
	}
	snapshot.Sessions = make([]SessionRecord, MaxSessions+1)
	if err := snapshot.Validate(); err == nil {
		t.Fatal("accepted too many sessions")
	}
	snapshot = validSnapshot()
	snapshot.LogicalCPUCount = 0
	if err := snapshot.Validate(); err == nil {
		t.Fatal("accepted zero CPU count")
	}
	snapshot = validSnapshot()
	snapshot.CollectionError = &CollectionError{Code: CollectionErrorTimeout}
	snapshot.Capabilities.InputDelay = true
	if err := snapshot.Validate(); err == nil {
		t.Fatal("accepted fatal capability")
	}
	snapshot.Capabilities = SessionCapabilities{}
	snapshot.Sessions = []SessionRecord{{SessionID: 1}}
	if err := snapshot.Validate(); err == nil {
		t.Fatal("accepted fatal sessions")
	}
}

func TestSessionValidationRejectsDuplicateAndInvalidMetrics(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.ObservedAtMS = 10
	snapshot.Sessions = []SessionRecord{{SessionID: 1, State: SessionActive, CPUPercent: new(800.0), Processes: []SessionProcess{{PID: 2, ImageName: "app.exe", WorkingSetBytes: DecimalUint64(MaxStoredBytes)}}, LogonAtMS: new(int64(10))}, {SessionID: 1, State: SessionUnknown}}
	if err := snapshot.Validate(); err == nil {
		t.Fatal("accepted duplicate session IDs")
	}
	snapshot.Sessions[1].SessionID = 3
	snapshot.Sessions[0].Processes = append(snapshot.Sessions[0].Processes, SessionProcess{PID: 2, ImageName: "other.exe"})
	if err := snapshot.Validate(); err == nil {
		t.Fatal("accepted duplicate PIDs")
	}
	snapshot.Sessions[0].Processes = snapshot.Sessions[0].Processes[:1]
	snapshot.Sessions[0].CPUPercent = new(math.NaN())
	if err := snapshot.Validate(); err == nil {
		t.Fatal("accepted NaN CPU")
	}
	snapshot.Sessions[0].CPUPercent = new(math.Inf(1))
	if err := snapshot.Validate(); err == nil {
		t.Fatal("accepted infinite CPU")
	}
	snapshot.Sessions[0].CPUPercent = new(800.0001)
	if err := snapshot.Validate(); err == nil {
		t.Fatal("accepted CPU above logical limit")
	}
}

func TestSessionValidationBoundaries(t *testing.T) {
	snapshot := validSnapshot()
	session := SessionRecord{SessionID: 0, State: SessionUnknown, InputDelayMS: new(uint32(600000)), WorkingSetBytes: new(DecimalUint64(MaxStoredBytes)), Processes: []SessionProcess{{PID: math.MaxUint32, ImageName: strings.Repeat("a", 260), WorkingSetBytes: DecimalUint64(MaxStoredBytes)}}}
	snapshot.Sessions = []SessionRecord{session}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("valid extremes: %v", err)
	}
	snapshot.Sessions[0].Processes[0].ImageName += "a"
	if err := snapshot.Validate(); err == nil {
		t.Fatal("accepted long image")
	}
	snapshot.Sessions[0].Processes[0].ImageName = "a/b"
	if err := snapshot.Validate(); err == nil {
		t.Fatal("accepted path image")
	}
	snapshot.Sessions[0].Processes[0].ImageName = "app.exe"
	snapshot.Sessions[0].InputDelayMS = new(uint32(600001))
	if err := snapshot.Validate(); err == nil {
		t.Fatal("accepted long input delay")
	}
}

func TestSnapshotRejectsTimeTextAndRemoteFXBoundaryViolations(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Sessions = []SessionRecord{{SessionID: 1, State: SessionActive, LogonAtMS: new(int64(2))}}
	if err := snapshot.Validate(); err == nil {
		t.Fatal("accepted logon after observed time")
	}
	snapshot.ObservedAtMS = MaxUnixMillis
	snapshot.Sessions[0].LogonAtMS = new(MaxUnixMillis)
	snapshot.Sessions[0].User = new(strings.Repeat("a", 257))
	if err := snapshot.Validate(); err == nil {
		t.Fatal("accepted oversized user")
	}
	snapshot.Sessions[0].User = new("ok\x01")
	if err := snapshot.Validate(); err == nil {
		t.Fatal("accepted user control character")
	}
	snapshot.Sessions[0].User = nil
	snapshot.Sessions[0].RemoteFX = &RemoteFXMetrics{FPS: new(0.0)}
	if err := snapshot.Validate(); err == nil {
		t.Fatal("accepted zero RemoteFX FPS")
	}
	snapshot.Sessions[0].RemoteFX.FPS = new(240.0)
	snapshot.Sessions[0].RemoteFX.QualityPercent = new(100.0)
	snapshot.Sessions[0].RemoteFX.LossPercent = new(0.0)
	snapshot.Sessions[0].RemoteFX.EncodeTimeMS = new(60000.0)
	snapshot.Sessions[0].RemoteFX.RTTMS = new(60000.0)
	snapshot.Sessions[0].RemoteFX.ServerSkippedFPS = new(1000000.0)
	snapshot.Sessions[0].RemoteFX.NetworkSkippedFPS = new(1000000.0)
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("rejected RemoteFX limits: %v", err)
	}
	snapshot.Sessions[0].RemoteFX.LossPercent = new(100.01)
	if err := snapshot.Validate(); err == nil {
		t.Fatal("accepted RemoteFX loss over maximum")
	}
}

func TestSnapshotRejectsFutureLifecycleTimes(t *testing.T) {
	for _, test := range []struct {
		name  string
		set   func(*SessionRecord, *int64)
		value *int64
		want  bool
	}{
		{name: "null connect", set: func(session *SessionRecord, value *int64) { session.ConnectAtMS = value }, value: nil, want: true},
		{name: "connect boundary", set: func(session *SessionRecord, value *int64) { session.ConnectAtMS = value }, value: new(int64(1)), want: true},
		{name: "future connect", set: func(session *SessionRecord, value *int64) { session.ConnectAtMS = value }, value: new(int64(2))},
		{name: "null disconnect", set: func(session *SessionRecord, value *int64) { session.DisconnectAtMS = value }, value: nil, want: true},
		{name: "disconnect boundary", set: func(session *SessionRecord, value *int64) { session.DisconnectAtMS = value }, value: new(int64(1)), want: true},
		{name: "future disconnect", set: func(session *SessionRecord, value *int64) { session.DisconnectAtMS = value }, value: new(int64(2))},
		{name: "null idle", set: func(session *SessionRecord, value *int64) { session.IdleSinceMS = value }, value: nil, want: true},
		{name: "idle boundary", set: func(session *SessionRecord, value *int64) { session.IdleSinceMS = value }, value: new(int64(1)), want: true},
		{name: "future idle", set: func(session *SessionRecord, value *int64) { session.IdleSinceMS = value }, value: new(int64(2))},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := validSnapshot()
			session := SessionRecord{SessionID: 1, State: SessionActive}
			test.set(&session, test.value)
			snapshot.Sessions = []SessionRecord{session}

			err := snapshot.Validate()
			if (err == nil) != test.want {
				t.Fatalf("Validate() error = %v, want accepted=%t", err, test.want)
			}
		})
	}
}

func TestSafeErrorCodeVocabularies(t *testing.T) {
	for _, code := range []string{CollectionErrorWTSEnumerationFailed, CollectionErrorWTSMetadataFailed, CollectionErrorTimeout} {
		if !IsSafeCollectionErrorCode(code) {
			t.Fatalf("safe collection code %q rejected", code)
		}
	}
	if IsSafeCollectionErrorCode("raw WTS error") || !IsSafeActionResultCode("privacy_policy_changed") || IsSafeActionResultCode("raw error") {
		t.Fatal("safe code filtering is incorrect")
	}
}

func TestStateRankPlacesUnknownLast(t *testing.T) {
	if StateRank(SessionUnknown) <= StateRank(SessionInit) {
		t.Fatalf("unknown rank %d must follow init %d", StateRank(SessionUnknown), StateRank(SessionInit))
	}
	if SessionState("unmapped").Valid() {
		t.Fatal("unmapped state is valid")
	}
}

func TestAggregateSemantics(t *testing.T) {
	one, two, four := int64(1), int64(2), int64(4)
	user, domain, sameDomain, other := "a", "d", "d", "b"
	aggregates := CalculateAggregates([]SessionRecord{{State: SessionActive, User: &user, Domain: &domain, LogonAtMS: &one}, {State: SessionConnected, User: &user, Domain: &sameDomain, ConnectAtMS: &two}, {State: SessionIdle, User: &other, Domain: &domain, IdleSinceMS: &four}, {State: SessionDisconnected, User: &user}})
	if aggregates.SessionCount != 4 || aggregates.ActiveCount != 2 || aggregates.IdleCount != 1 || aggregates.DisconnectedCount != 1 || aggregates.UserCount != 2 || aggregates.LastActivityAtMS == nil || *aggregates.LastActivityAtMS != 4 {
		t.Fatalf("unexpected aggregates: %#v", aggregates)
	}
}

func TestAggregateIdentityVisibility(t *testing.T) {
	alice, bob, domain := "alice", "bob", "CONTOSO"
	sessions := []SessionRecord{
		{User: &alice, Domain: &domain},
		{User: &bob, Domain: &domain},
		{User: &alice, Domain: &domain},
	}

	for _, test := range []struct {
		name     string
		identity Visibility
		want     int
	}{
		{name: "full", identity: VisibilityFull, want: 2},
		{name: "masked", identity: VisibilityMasked, want: 2},
		{name: "hidden", identity: VisibilityHidden, want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			aggregates := CalculateAggregatesForIdentity(sessions, test.identity)
			if aggregates.UserCount != test.want {
				t.Fatalf("UserCount=%d, want %d", aggregates.UserCount, test.want)
			}
		})
	}
}

func TestNormalizedActionFingerprint(t *testing.T) {
	message := " \nHello\n "
	normalized, err := NormalizeActionRequest(ActionRequest{Type: SessionActionMessage, ExpectedLogonAtMS: 4, Message: &message})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Message != "Hello" {
		t.Fatalf("normalized %q", normalized.Message)
	}
	equivalent := NormalizedActionRequest{Type: SessionActionMessage, ExpectedLogonAtMS: 4, Message: "Hello"}
	if normalized.Fingerprint() != equivalent.Fingerprint() {
		t.Fatal("equivalent action fingerprint differs")
	}
	if normalized.Fingerprint() == (NormalizedActionRequest{Type: SessionActionMessage, ExpectedLogonAtMS: 5, Message: "Hello"}).Fingerprint() {
		t.Fatal("different action fingerprint matches")
	}
	if _, err := NormalizeActionRequest(ActionRequest{Type: SessionActionLogoff, ExpectedLogonAtMS: 1, Message: &message}); err == nil {
		t.Fatal("nonempty logoff message accepted")
	}
}

func TestNormalizeActionRequest_DisconnectRejectsMessage(t *testing.T) {
	normalized, err := NormalizeActionRequest(ActionRequest{Type: SessionActionDisconnect, ExpectedLogonAtMS: 4})
	if err != nil || normalized.Message != "" {
		t.Fatalf("normalize disconnect = %#v, %v", normalized, err)
	}
	message := "must not be sent"
	if _, err := NormalizeActionRequest(ActionRequest{Type: SessionActionDisconnect, ExpectedLogonAtMS: 4, Message: &message}); err == nil {
		t.Fatal("disconnect accepted message")
	}
}

func TestShadowCommandSafety(t *testing.T) {
	command, err := ShadowCommand("hosta.example.test", 42)
	if err != nil || command != "mstsc.exe /v:hosta.example.test /shadow:42 /control" {
		t.Fatalf("command %q, %v", command, err)
	}
	if _, err := ShadowCommand("host;calc", 42); err == nil {
		t.Fatal("unsafe host accepted")
	}
}

func TestCompactSnapshotForWire_PreservesAllWorstCaseSessions(t *testing.T) {
	snapshot := validSnapshot()
	text := strings.Repeat("<", 256)
	truncatedText := strings.Repeat("<", 32)
	imageName := strings.Repeat("<", 256) + ".exe"
	snapshot.Sessions = make([]SessionRecord, MaxSessions)
	for i := range snapshot.Sessions {
		session := &snapshot.Sessions[i]
		session.SessionID = uint32(i + 1)
		session.State = SessionActive
		session.User = &text
		session.Domain = &text
		session.Station = &text
		session.ClientName = &text
		session.ClientAddress = &text
		session.Processes = make([]SessionProcess, MaxProcesses)
		for j := range session.Processes {
			session.Processes[j] = SessionProcess{PID: uint32(i*MaxProcesses + j + 1), ImageName: imageName}
		}
	}
	before, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) <= MaxSnapshotBytes {
		t.Fatalf("worst-case payload = %d bytes, want more than %d", len(before), MaxSnapshotBytes)
	}

	compacted, err := CompactSnapshotForWire(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(compacted)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > MaxSnapshotBytes {
		t.Fatalf("compacted payload = %d bytes, want at most %d", len(payload), MaxSnapshotBytes)
	}
	if len(compacted.Sessions) != MaxSessions {
		t.Fatalf("sessions = %d, want %d", len(compacted.Sessions), MaxSessions)
	}
	for i, session := range compacted.Sessions {
		if session.SessionID != uint32(i+1) || session.State != SessionActive || session.LogonAtMS != nil || session.User == nil || *session.User != truncatedText || session.Domain == nil || *session.Domain != truncatedText || session.Processes == nil || len(session.Processes) != 0 {
			t.Fatalf("compacted session %d = %#v", i, session)
		}
	}
}

func TestCompactSnapshotForWire_RejectsTooManySessions(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Sessions = make([]SessionRecord, MaxSessions+1)
	for i := range snapshot.Sessions {
		snapshot.Sessions[i] = SessionRecord{SessionID: uint32(i + 1), State: SessionActive}
	}
	if _, err := CompactSnapshotForWire(snapshot); err == nil {
		t.Fatal("CompactSnapshotForWire accepted more than MaxSessions")
	}
}
