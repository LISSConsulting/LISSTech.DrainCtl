//go:build windows

package drainctl

import (
	"errors"
	"testing"
	"unicode/utf16"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
)

func TestWTSStateMapping_AllStatesAndUnknown(t *testing.T) {
	cases := []struct {
		value uint32
		want  sessiondata.SessionState
	}{
		{wtsActive, sessiondata.SessionActive}, {wtsConnected, sessiondata.SessionConnected},
		{wtsConnectQuery, sessiondata.SessionConnectQuery}, {wtsShadow, sessiondata.SessionShadow},
		{wtsDisconnected, sessiondata.SessionDisconnected}, {wtsIdle, sessiondata.SessionIdle},
		{wtsListen, sessiondata.SessionListen}, {wtsReset, sessiondata.SessionReset},
		{wtsDown, sessiondata.SessionDown}, {wtsInit, sessiondata.SessionInit}, {99, sessiondata.SessionUnknown},
	}
	for _, tc := range cases {
		if got := sessiondata.SessionState(wtsSessionState(tc.value)); got != tc.want {
			t.Errorf("state %d = %q, want %q", tc.value, got, tc.want)
		}
	}
}

func TestFiletimeToUnixMS_BoundsAndPrecision(t *testing.T) {
	cases := []struct {
		name string
		in   wtsFileTime
		want *int64
	}{
		{"zero absent", wtsFileTime{}, nil},
		{"before unix epoch absent", wtsFileTime{LowDateTime: 1}, nil},
		{"unix epoch", filetimeFromUnixMS(0), new(int64(0))},
		{"millisecond precision", filetimeFromUnixMS(1769990123456), new(int64(1769990123456))},
		{"beyond contract range absent", wtsFileTime{LowDateTime: 0xffffffff, HighDateTime: 0xffffffff}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filetimeToUnixMS(tc.in)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("filetimeToUnixMS() = %d, want nil", *got)
				}
				return
			}
			if got == nil || *got != *tc.want {
				t.Fatalf("filetimeToUnixMS() = %v, want %d", got, *tc.want)
			}
		})
	}
}

func TestDecodeWTSClientAddress_IPv4IPv6AbsentAndBounds(t *testing.T) {
	cases := []struct {
		name    string
		address wtsClientAddressW
		bytes   uint32
		want    string
		wantErr bool
	}{
		{"IPv4", wtsClientAddressW{AddressFamily: wtsAddressFamilyIPv4, Address: [20]byte{0, 0, 192, 0, 2, 17}}, wtsClientAddressSize, "192.0.2.17", false},
		{"IPv6", wtsClientAddressW{AddressFamily: wtsAddressFamilyIPv6, Address: [20]byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}}, wtsClientAddressSize, "2001:db8::1", false},
		{"absent", wtsClientAddressW{}, wtsClientAddressSize, "", false},
		{"short buffer", wtsClientAddressW{}, wtsClientAddressSize - 1, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeWTSClientAddress(tc.address, tc.bytes)
			if (err != nil) != tc.wantErr {
				t.Fatalf("decodeWTSClientAddress() error = %v, want error %t", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if tc.want == "" {
				if got != nil {
					t.Fatalf("address = %q, want nil", *got)
				}
			} else if got == nil || *got != tc.want {
				t.Fatalf("address = %v, want %q", got, tc.want)
			}
		})
	}
}

func TestEnumerateSessionRecords_CompleteMetadataAndReleasesAllBuffers(t *testing.T) {
	entries := []wtsSessionEntry{{SessionID: 0, State: wtsActive}, {SessionID: 4, State: wtsActive}, {SessionID: 5, State: wtsListen}}
	info := wtsInfoW{SessionID: 4, State: wtsActive, ConnectTime: filetimeFromUnixMS(100), LogonTime: filetimeFromUnixMS(90), LastInputTime: filetimeFromUnixMS(110)}
	copy(info.UserName[:], utf16.Encode([]rune("alice")))
	copy(info.Domain[:], utf16.Encode([]rune("CONTOSO")))
	copy(info.WinStationName[:], utf16.Encode([]rune("RDP-Tcp#4")))
	freeCalls := 0
	withWTSSeams(t,
		func() ([]wtsSessionEntry, func(), error) {
			return entries, func() { freeCalls++ }, nil
		},
		func(sessionID uint32) (wtsSessionMetadata, func(), error) {
			if sessionID != 4 {
				t.Fatalf("metadata requested for session %d", sessionID)
			}
			return wtsSessionMetadata{
				Info:               info,
				ClientName:         "WS-17",
				ClientAddress:      wtsClientAddressW{AddressFamily: wtsAddressFamilyIPv4, Address: [20]byte{0, 0, 192, 0, 2, 17}},
				ClientAddressBytes: wtsClientAddressSize,
			}, func() { freeCalls += 3 }, nil
		},
	)

	records, err := EnumerateSessionRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	record := records[0]
	if record.SessionID != 4 || record.State != sessiondata.SessionActive || stringValue(record.User) != "alice" || stringValue(record.Domain) != "CONTOSO" || stringValue(record.Station) != "RDP-Tcp#4" || stringValue(record.ClientName) != "WS-17" || stringValue(record.ClientAddress) != "192.0.2.17" {
		t.Fatalf("unexpected record: %#v", record)
	}
	for name, got := range map[string]*int64{"logon": record.LogonAtMS, "connect": record.ConnectAtMS, "last input": record.IdleSinceMS} {
		if got == nil {
			t.Fatalf("%s time is nil", name)
		}
		want := map[string]int64{"logon": 90, "connect": 100, "last input": 110}[name]
		if *got != want {
			t.Fatalf("%s = %d, want %d", name, *got, want)
		}
	}
	if record.Processes == nil {
		t.Fatal("record processes must be an empty array when process collection has not run")
	}
	if freeCalls != 4 {
		t.Fatalf("buffer cleanup calls = %d, want 4", freeCalls)
	}
}

func TestEnumerateSessionRecords_MetadataErrorIsFatalAndReleasesBuffers(t *testing.T) {
	freeCalls := 0
	withWTSSeams(t,
		func() ([]wtsSessionEntry, func(), error) {
			return []wtsSessionEntry{{SessionID: 4, State: wtsActive}}, func() { freeCalls++ }, nil
		},
		func(uint32) (wtsSessionMetadata, func(), error) {
			return wtsSessionMetadata{}, func() { freeCalls++ }, errors.New("metadata unavailable")
		},
	)
	if records, err := EnumerateSessionRecords(); err == nil || records != nil {
		t.Fatalf("EnumerateSessionRecords() = %#v, %v; want fatal error", records, err)
	} else if !errors.Is(err, ErrWTSMetadata) {
		t.Fatalf("error = %v, want ErrWTSMetadata", err)
	}
	if freeCalls != 2 {
		t.Fatalf("buffer cleanup calls = %d, want 2", freeCalls)
	}
}

func TestEnumerateSessionRecords_EnumerationFailureIsFatal(t *testing.T) {
	withWTSSeams(t, func() ([]wtsSessionEntry, func(), error) {
		return nil, nil, errors.New("access denied")
	}, nil)
	if records, err := EnumerateSessionRecords(); err == nil || records != nil {
		t.Fatalf("EnumerateSessionRecords() = %#v, %v; want fatal error", records, err)
	} else if !errors.Is(err, ErrWTSEnumeration) {
		t.Fatalf("error = %v, want ErrWTSEnumeration", err)
	}
}

func TestEnumerateSessionRecords_AppliesEmissionCapAfterFiltering(t *testing.T) {
	entries := make([]wtsSessionEntry, 0, sessiondata.MaxSessions+65)
	for range sessiondata.MaxSessions + 64 {
		entries = append(entries, wtsSessionEntry{SessionID: 0, State: wtsListen})
	}
	entries = append(entries, wtsSessionEntry{SessionID: 4, State: wtsActive})
	withWTSSeams(t,
		func() ([]wtsSessionEntry, func(), error) { return entries, nil, nil },
		func(sessionID uint32) (wtsSessionMetadata, func(), error) {
			if sessionID != 4 {
				t.Fatalf("metadata requested for excluded session %d", sessionID)
			}
			return wtsSessionMetadata{
				Info:               wtsInfoW{SessionID: 4, State: wtsActive},
				ClientAddressBytes: wtsClientAddressSize,
			}, nil, nil
		},
	)
	records, err := EnumerateSessionRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].SessionID != 4 {
		t.Fatalf("records = %#v, want only session 4", records)
	}
}

func TestEnumerateSessionRecords_RejectsOverEmittedLimitAndReleasesBuffer(t *testing.T) {
	freeCalls := 0
	entries := make([]wtsSessionEntry, sessiondata.MaxSessions+1)
	for i := range entries {
		entries[i] = wtsSessionEntry{SessionID: uint32(i + 1), State: wtsActive}
	}
	withWTSSeams(t, func() ([]wtsSessionEntry, func(), error) {
		return entries, func() { freeCalls++ }, nil
	}, nil)
	if records, err := EnumerateSessionRecords(); err == nil || records != nil {
		t.Fatalf("EnumerateSessionRecords() = %#v, %v; want fatal bound error", records, err)
	} else if !errors.Is(err, ErrWTSEnumeration) {
		t.Fatalf("error = %v, want ErrWTSEnumeration", err)
	}
	if freeCalls != 1 {
		t.Fatalf("buffer cleanup calls = %d, want 1", freeCalls)
	}
}

func TestEnumerateSessionRecords_RejectsRawResourceLimit(t *testing.T) {
	withWTSSeams(t, func() ([]wtsSessionEntry, func(), error) {
		return make([]wtsSessionEntry, maxRawWTSSessions+1), nil, nil
	}, nil)
	if records, err := EnumerateSessionRecords(); err == nil || records != nil {
		t.Fatalf("EnumerateSessionRecords() = %#v, %v; want fatal raw-limit error", records, err)
	} else if !errors.Is(err, ErrWTSEnumeration) {
		t.Fatalf("error = %v, want ErrWTSEnumeration", err)
	}
}

func withWTSSeams(t *testing.T, enumerate wtsSessionEnumerator, query wtsSessionMetadataQuery) {
	t.Helper()
	oldEnumerate, oldQuery := wtsEnumerateSessions, wtsQueryMetadata
	if enumerate != nil {
		wtsEnumerateSessions = enumerate
	}
	if query != nil {
		wtsQueryMetadata = query
	}
	t.Cleanup(func() {
		wtsEnumerateSessions, wtsQueryMetadata = oldEnumerate, oldQuery
	})
}

func filetimeFromUnixMS(milliseconds int64) wtsFileTime {
	value := uint64(milliseconds)*10000 + 116444736000000000
	return wtsFileTime{LowDateTime: uint32(value), HighDateTime: uint32(value >> 32)}
}
