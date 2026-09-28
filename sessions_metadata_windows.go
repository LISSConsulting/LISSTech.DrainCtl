//go:build windows

package drainctl

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"

	"golang.org/x/sys/windows"
)

var (
	ErrWTSEnumeration = errors.New("WTS session enumeration failed")
	ErrWTSMetadata    = errors.New("WTS session metadata failed")
)

const (
	wtsInfo          = 24 // WTSInfoClass: WTSInfo
	wtsClientName    = 10 // WTSInfoClass: WTSClientName
	wtsClientAddress = 14 // WTSInfoClass: WTSClientAddress

	wtsAddressFamilyIPv4 = 2
	wtsAddressFamilyIPv6 = 23
	wtsClientAddressSize = uint32(24)

	// Raw WTS rows include non-reportable Services and listener entries. Keep a
	// separate finite resource cap so raw enumeration can never drive an
	// unbounded allocation. This deliberately leaves room for normal excluded
	// rows; MaxSessions applies only after those rows are filtered.
	maxRawWTSSessions = 4096
)

var (
	modWtsapi32                     = windows.NewLazySystemDLL("wtsapi32.dll")
	procWTSEnumerateSessionsW       = modWtsapi32.NewProc("WTSEnumerateSessionsW")
	procWTSQuerySessionInformationW = modWtsapi32.NewProc("WTSQuerySessionInformationW")
	procWTSFreeMemory               = modWtsapi32.NewProc("WTSFreeMemory")

	wtsEnumerateSessionsCall = procWTSEnumerateSessionsW.Call
	wtsQuerySessionCall      = procWTSQuerySessionInformationW.Call
	wtsFreeMemoryCall        = procWTSFreeMemory.Call
)

type wtsCall func(...uintptr) (uintptr, uintptr, error)

type wtsSessionEntry struct {
	SessionID uint32
	State     uint32
}

type wtsSessionMetadata struct {
	Info               wtsInfoW
	ClientName         string
	ClientAddress      wtsClientAddressW
	ClientAddressBytes uint32
}

type wtsSessionEnumerator func() ([]wtsSessionEntry, func(), error)

var (
	wtsEnumerateSessions wtsSessionEnumerator    = enumerateWTSRawSessions
	wtsQueryMetadata     wtsSessionMetadataQuery = queryWTSSessionMetadata
)

type wtsSessionMetadataQuery func(uint32) (wtsSessionMetadata, func(), error)

// wtsSessionInfoW matches WTS_SESSION_INFOW.
type wtsSessionInfoW struct {
	SessionID      uint32
	WinStationName *uint16
	State          uint32
}

// wtsFileTime matches FILETIME. WTSINFO uses LARGE_INTEGER values with this
// exact low/high DWORD representation.
type wtsFileTime struct {
	LowDateTime  uint32
	HighDateTime uint32
}

// wtsInfoW matches WTSINFOW. Its fixed UTF-16 arrays include their terminating
// NULs: WINSTATIONNAME_LENGTH + 1, DOMAIN_LENGTH + 1, and USERNAME_LENGTH + 1.
type wtsInfoW struct {
	State                   uint32
	SessionID               uint32
	IncomingBytes           uint32
	OutgoingBytes           uint32
	IncomingFrames          uint32
	OutgoingFrames          uint32
	IncomingCompressedBytes uint32
	OutgoingCompressedBytes uint32
	WinStationName          [33]uint16
	Domain                  [18]uint16
	UserName                [21]uint16
	ConnectTime             wtsFileTime
	DisconnectTime          wtsFileTime
	LastInputTime           wtsFileTime
	LogonTime               wtsFileTime
	CurrentTime             wtsFileTime
}

// wtsClientAddressW matches WTS_CLIENT_ADDRESS. IPv4 values occupy Address[2:6]
// and IPv6 values occupy Address[:16], as documented by WTSQuerySessionInformationW.
type wtsClientAddressW struct {
	AddressFamily uint32
	Address       [20]byte
}

// EnumerateSessionRecords returns the complete bounded current WTS session set.
// Enumeration or required metadata failures are fatal so callers can preserve a
// prior complete snapshot rather than replacing it with partial data.
func EnumerateSessionRecords() ([]sessiondata.SessionRecord, error) {
	entries, cleanup, err := wtsEnumerateSessions()
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrWTSEnumeration, err)
	}
	if len(entries) > maxRawWTSSessions {
		return nil, fmt.Errorf("%w: WTSEnumerateSessionsW returned %d raw sessions; limit is %d", ErrWTSEnumeration, len(entries), maxRawWTSSessions)
	}

	reportable := 0
	for _, entry := range entries {
		if entry.SessionID != 0 && entry.State != wtsListen {
			reportable++
		}
	}
	if reportable > sessiondata.MaxSessions {
		return nil, fmt.Errorf("%w: WTSEnumerateSessionsW returned %d reportable sessions; limit is %d", ErrWTSEnumeration, reportable, sessiondata.MaxSessions)
	}

	records := make([]sessiondata.SessionRecord, 0, reportable)
	for _, entry := range entries {
		if entry.SessionID == 0 || entry.State == wtsListen {
			continue
		}
		record, err := querySessionMetadata(entry.SessionID)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrWTSMetadata, err)
		}
		records = append(records, record)
	}
	return records, nil
}

func querySessionMetadata(sessionID uint32) (sessiondata.SessionRecord, error) {
	metadata, cleanup, err := wtsQueryMetadata(sessionID)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return sessiondata.SessionRecord{}, err
	}
	if metadata.Info.SessionID != sessionID {
		return sessiondata.SessionRecord{}, fmt.Errorf("WTSInfo session ID %d does not match enumerated session %d", metadata.Info.SessionID, sessionID)
	}
	clientAddress, err := decodeWTSClientAddress(metadata.ClientAddress, metadata.ClientAddressBytes)
	if err != nil {
		return sessiondata.SessionRecord{}, fmt.Errorf("WTSClientAddress for session %d: %w", sessionID, err)
	}
	clientName := optionalWTSString(metadata.ClientName, 256)
	if metadata.ClientName != "" && clientName == nil {
		return sessiondata.SessionRecord{}, fmt.Errorf("WTSQuerySessionInformationW(session=%d, class=%d) returned invalid text", sessionID, wtsClientName)
	}

	return sessiondata.SessionRecord{
		SessionID:      sessionID,
		LogonAtMS:      filetimeToUnixMS(metadata.Info.LogonTime),
		User:           optionalWTSString(utf16ArrayString(metadata.Info.UserName[:]), 256),
		Domain:         optionalWTSString(utf16ArrayString(metadata.Info.Domain[:]), 256),
		State:          sessiondata.SessionState(wtsSessionState(metadata.Info.State)),
		Station:        optionalWTSString(utf16ArrayString(metadata.Info.WinStationName[:]), 256),
		ClientName:     clientName,
		ClientAddress:  clientAddress,
		ConnectAtMS:    filetimeToUnixMS(metadata.Info.ConnectTime),
		DisconnectAtMS: filetimeToUnixMS(metadata.Info.DisconnectTime),
		IdleSinceMS:    filetimeToUnixMS(metadata.Info.LastInputTime),
		Processes:      []sessiondata.SessionProcess{},
	}, nil
}

func enumerateWTSRawSessions() ([]wtsSessionEntry, func(), error) {
	var buffer unsafe.Pointer
	var count uint32
	ret, _, err := wtsEnumerateSessionsCall(
		0, // WTS_CURRENT_SERVER_HANDLE
		0, // Reserved
		1, // Version
		uintptr(unsafe.Pointer(&buffer)),
		uintptr(unsafe.Pointer(&count)),
	)
	if ret == 0 {
		return nil, nil, fmt.Errorf("WTSEnumerateSessionsW: %w", err)
	}
	cleanup := func() { wtsFreeMemory(buffer) }
	if count > maxRawWTSSessions {
		cleanup()
		return nil, nil, fmt.Errorf("WTSEnumerateSessionsW returned %d raw sessions; limit is %d", count, maxRawWTSSessions)
	}

	if count != 0 && buffer == nil {
		cleanup()
		return nil, nil, errors.New("WTSEnumerateSessionsW returned a nil session buffer")
	}

	entries := make([]wtsSessionEntry, 0, count)
	entrySize := unsafe.Sizeof(wtsSessionInfoW{})
	for i := range count {
		entry := (*wtsSessionInfoW)(unsafe.Add(buffer, uintptr(i)*entrySize))
		entries = append(entries, wtsSessionEntry{SessionID: entry.SessionID, State: entry.State})
	}
	return entries, cleanup, nil
}

func queryWTSSessionMetadata(sessionID uint32) (wtsSessionMetadata, func(), error) {
	info, err := queryWTSInfo(sessionID)
	if err != nil {
		return wtsSessionMetadata{}, nil, err
	}
	clientName, err := queryWTSString(sessionID, wtsClientName, 256)
	if err != nil {
		return wtsSessionMetadata{}, nil, err
	}
	buffer, bytesReturned, err := queryWTSBuffer(sessionID, wtsClientAddress)
	if err != nil {
		return wtsSessionMetadata{}, nil, err
	}
	defer wtsFreeMemory(buffer)
	if bytesReturned < wtsClientAddressSize {
		return wtsSessionMetadata{}, nil, fmt.Errorf("WTSClientAddress for session %d returned %d bytes; need %d", sessionID, bytesReturned, wtsClientAddressSize)
	}
	return wtsSessionMetadata{
		Info:               info,
		ClientName:         stringValue(clientName),
		ClientAddress:      *(*wtsClientAddressW)(buffer),
		ClientAddressBytes: bytesReturned,
	}, nil, nil
}

func queryWTSInfo(sessionID uint32) (wtsInfoW, error) {
	buffer, bytesReturned, err := queryWTSBuffer(sessionID, wtsInfo)
	if err != nil {
		return wtsInfoW{}, err
	}
	defer wtsFreeMemory(buffer)
	if bytesReturned < uint32(unsafe.Sizeof(wtsInfoW{})) {
		return wtsInfoW{}, fmt.Errorf("WTSInfo for session %d returned %d bytes; need %d", sessionID, bytesReturned, unsafe.Sizeof(wtsInfoW{}))
	}
	return *(*wtsInfoW)(buffer), nil
}

func queryWTSString(sessionID, infoClass uint32, maxBytes int) (*string, error) {
	buffer, _, err := queryWTSBuffer(sessionID, infoClass)
	if err != nil {
		return nil, err
	}
	defer wtsFreeMemory(buffer)
	value := windows.UTF16PtrToString((*uint16)(buffer))
	result := optionalWTSString(value, maxBytes)
	if value != "" && result == nil {
		return nil, fmt.Errorf("WTSQuerySessionInformationW(session=%d, class=%d) returned invalid text", sessionID, infoClass)
	}
	return result, nil
}

func decodeWTSClientAddress(address wtsClientAddressW, bytesReturned uint32) (*string, error) {
	if bytesReturned < wtsClientAddressSize {
		return nil, fmt.Errorf("returned %d bytes; need %d", bytesReturned, wtsClientAddressSize)
	}
	switch address.AddressFamily {
	case 0:
		return nil, nil
	case wtsAddressFamilyIPv4:
		ip := netip.AddrFrom4([4]byte(address.Address[2:6]))
		value := ip.String()
		return &value, nil
	case wtsAddressFamilyIPv6:
		ip := netip.AddrFrom16([16]byte(address.Address[:16]))
		value := ip.String()
		return &value, nil
	default:
		return nil, fmt.Errorf("unsupported address family %d", address.AddressFamily)
	}
}

func queryWTSBuffer(sessionID, infoClass uint32) (unsafe.Pointer, uint32, error) {
	var buffer unsafe.Pointer
	var bytesReturned uint32
	ret, _, err := wtsQuerySessionCall(
		0, // WTS_CURRENT_SERVER_HANDLE
		uintptr(sessionID),
		uintptr(infoClass),
		uintptr(unsafe.Pointer(&buffer)),
		uintptr(unsafe.Pointer(&bytesReturned)),
	)
	if ret == 0 {
		return nil, 0, fmt.Errorf("WTSQuerySessionInformationW(session=%d, class=%d): %w", sessionID, infoClass, err)
	}
	if buffer == nil {
		return nil, 0, fmt.Errorf("WTSQuerySessionInformationW(session=%d, class=%d) returned a nil buffer", sessionID, infoClass)
	}
	return buffer, bytesReturned, nil
}

func wtsFreeMemory(pointer unsafe.Pointer) {
	if pointer != nil {
		_, _, _ = wtsFreeMemoryCall(uintptr(pointer))
	}
}

func filetimeToUnixMS(value wtsFileTime) *int64 {
	const (
		unixEpochFiletime = uint64(116444736000000000)
		filetimePerMS     = uint64(10000)
		maxUnixMS         = int64(253402300799999)
	)
	filetime := uint64(value.HighDateTime)<<32 | uint64(value.LowDateTime)
	if filetime < unixEpochFiletime {
		return nil
	}
	milliseconds := (filetime - unixEpochFiletime) / filetimePerMS
	if milliseconds > uint64(maxUnixMS) {
		return nil
	}
	result := int64(milliseconds)
	return &result
}

func utf16ArrayString(value []uint16) string {
	for i, unit := range value {
		if unit == 0 {
			value = value[:i]
			break
		}
	}
	return string(utf16.Decode(value))
}

func optionalWTSString(value string, maxBytes int) *string {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n\t") {
		return nil
	}
	return &value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func wtsSessionState(value uint32) string {
	switch value {
	case wtsActive:
		return "active"
	case wtsConnected:
		return "connected"
	case wtsConnectQuery:
		return "connect_query"
	case wtsShadow:
		return "shadow"
	case wtsDisconnected:
		return "disconnected"
	case wtsIdle:
		return "idle"
	case wtsListen:
		return "listen"
	case wtsReset:
		return "reset"
	case wtsDown:
		return "down"
	case wtsInit:
		return "init"
	default:
		return "unknown"
	}
}

func wtsStateNameFromSessionState(state string) string {
	switch state {
	case "active":
		return "Active"
	case "connected":
		return "Connected"
	case "connect_query":
		return "ConnectQuery"
	case "shadow":
		return "Shadow"
	case "disconnected":
		return "Disconnected"
	case "idle":
		return "Idle"
	case "listen":
		return "Listen"
	case "reset":
		return "Reset"
	case "down":
		return "Down"
	case "init":
		return "Init"
	default:
		return "Unknown"
	}
}

func wtsStateValue(state string) uint32 {
	switch state {
	case "active":
		return wtsActive
	case "connected":
		return wtsConnected
	case "connect_query":
		return wtsConnectQuery
	case "shadow":
		return wtsShadow
	case "disconnected":
		return wtsDisconnected
	case "idle":
		return wtsIdle
	case "listen":
		return wtsListen
	case "reset":
		return wtsReset
	case "down":
		return wtsDown
	case "init":
		return wtsInit
	default:
		return ^uint32(0)
	}
}
