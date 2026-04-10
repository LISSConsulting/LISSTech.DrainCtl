//go:build windows

package logging

// ETWHandler implements slog.Handler by writing records to a manifest-based
// Windows ETW provider ("LISS Technologies-DrainCtl").
//
// Routing:
//   - DEBUG  → Debug channel   (channel 0x11, event 4000, verbose level)
//   - INFO   → Operational channel (channel 0x10, event 1099, info level)
//   - WARN   → Operational channel (channel 0x10, event 2099, warning level)
//   - ERROR  → Operational channel (channel 0x10, event 3099, error level)
//
// Callers can override the event ID by adding an integer slog attribute with
// key "event_id" (e.g. slog.Int("event_id", 1000)).  ETWHandler maps the
// attribute to the matching manifest event descriptor so Event Viewer shows
// the correct symbol name.
//
// EventEnabled is called before every write; when the Debug channel has no
// active ETW sessions the handler skips the write entirely (zero-cost).
//
// The provider GUID matches assets/drainctl.man.
// Channel values (0x10 / 0x11) must match the `value` attributes in the
// manifest channels.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ── ETW Win32 types ──────────────────────────────────────────────────────────

// eventDescriptor mirrors the Win32 EVENT_DESCRIPTOR structure.
type eventDescriptor struct {
	Id      uint16
	Version uint8
	Channel uint8
	Level   uint8
	Opcode  uint8
	Task    uint16
	Keyword uint64
}

// eventDataDescriptor mirrors the Win32 EVENT_DATA_DESCRIPTOR structure.
type eventDataDescriptor struct {
	Ptr      uint64
	Size     uint32
	Reserved uint32
}

// ETW level constants (WINEVENT_LEVEL_*).
const (
	etwLevelError   uint8 = 2
	etwLevelWarning uint8 = 3
	etwLevelInfo    uint8 = 4
	etwLevelVerbose uint8 = 5
)

// Channel IDs — must match the `value` attributes in assets/drainctl.man.
const (
	etwChannelOperational uint8 = 0x10 // LISS Technologies-DrainCtl/Operational
	etwChannelDebug       uint8 = 0x11 // LISS Technologies-DrainCtl/Debug
)

// providerGUID is the GUID for the "LISS Technologies-DrainCtl" ETW provider
// and must match the guid attribute in assets/drainctl.man.
var providerGUID = windows.GUID{
	Data1: 0x7A2C9D5E,
	Data2: 0x4B1F,
	Data3: 0x4E8D,
	Data4: [8]byte{0x9C, 0x3A, 0xF6, 0xB0, 0xD2, 0xE7, 0xA1, 0xC5},
}

// ── Lazy-loaded advapi32 ETW functions ────────────────────────────────────────

var (
	advapi32            = windows.NewLazySystemDLL("advapi32.dll")
	procEventRegister   = advapi32.NewProc("EventRegister")
	procEventUnregister = advapi32.NewProc("EventUnregister")
	procEventEnabled    = advapi32.NewProc("EventEnabled")
	procEventWrite      = advapi32.NewProc("EventWrite")
)

// ── Pre-built event descriptors for each manifest event ──────────────────────

// buildDesc constructs an eventDescriptor for the given manifest event.
func buildDesc(id uint16, channel, level uint8) eventDescriptor {
	return eventDescriptor{Id: id, Version: 0, Channel: channel, Level: level}
}

var (
	// Operational informational events
	descServiceStarted     = buildDesc(1000, etwChannelOperational, etwLevelInfo)
	descServiceStopped     = buildDesc(1001, etwChannelOperational, etwLevelInfo)
	descCheckHealthy       = buildDesc(1002, etwChannelOperational, etwLevelInfo)
	descConfigReloaded     = buildDesc(1003, etwChannelOperational, etwLevelInfo)
	descTransitionDetected = buildDesc(1004, etwChannelOperational, etwLevelInfo)
	descGenericInfo        = buildDesc(1099, etwChannelOperational, etwLevelInfo)

	// Operational warning events
	descCheckGrace     = buildDesc(2000, etwChannelOperational, etwLevelWarning)
	descGenericWarning = buildDesc(2099, etwChannelOperational, etwLevelWarning)

	// Operational error events
	descCheckAlert         = buildDesc(3000, etwChannelOperational, etwLevelError)
	descRegistryReadFailed = buildDesc(3001, etwChannelOperational, etwLevelError)
	descServiceError       = buildDesc(3002, etwChannelOperational, etwLevelError)
	descGenericError       = buildDesc(3099, etwChannelOperational, etwLevelError)

	// Debug channel event
	descGenericDebug = buildDesc(4000, etwChannelDebug, etwLevelVerbose)
)

// eventIDToDesc maps specific manifest event IDs to their descriptor.
var eventIDToDesc = map[int]eventDescriptor{
	1000: descServiceStarted,
	1001: descServiceStopped,
	1002: descCheckHealthy,
	1003: descConfigReloaded,
	1004: descTransitionDetected,
	1099: descGenericInfo,
	2000: descCheckGrace,
	2099: descGenericWarning,
	3000: descCheckAlert,
	3001: descRegistryReadFailed,
	3002: descServiceError,
	3099: descGenericError,
	4000: descGenericDebug,
}

// ── ETWHandler ────────────────────────────────────────────────────────────────

// ETWHandler is a slog.Handler that emits records through the Windows ETW
// manifest provider defined in assets/drainctl.man.
type ETWHandler struct {
	level     *slog.LevelVar
	regHandle atomic.Uintptr // REGHANDLE (0 = not registered / unavailable)
	attrs     []slog.Attr
	group     string
}

// NewETWHandler registers the DrainCtl ETW provider and returns an ETWHandler.
// If registration fails (e.g. the manifest has not been installed yet), the
// handler is returned in a degraded state where all writes are silently
// dropped. The caller is responsible for calling Close() when done.
func NewETWHandler(level *slog.LevelVar) *ETWHandler {
	h := &ETWHandler{level: level}

	var handle uintptr
	r, _, _ := procEventRegister.Call(
		uintptr(unsafe.Pointer(&providerGUID)),
		0, // EnableCallback — nil
		0, // CallbackContext — nil
		uintptr(unsafe.Pointer(&handle)),
	)
	if r == 0 { // ERROR_SUCCESS
		h.regHandle.Store(handle)
	}
	// Non-zero r means registration failed (provider not installed yet).
	// We continue in degraded mode; writes are no-ops until Close/re-create.
	return h
}

// Close unregisters the ETW provider handle.
func (h *ETWHandler) Close() {
	handle := h.regHandle.Swap(0)
	if handle != 0 {
		_, _, _ = procEventUnregister.Call(handle)
	}
}

func (h *ETWHandler) Enabled(_ context.Context, level slog.Level) bool {
	if level < h.level.Level() {
		return false
	}
	handle := h.regHandle.Load()
	if handle == 0 {
		return false
	}
	desc := h.genericDesc(level, 0)
	ret, _, _ := procEventEnabled.Call(handle, uintptr(unsafe.Pointer(&desc)))
	return ret != 0
}

func (h *ETWHandler) Handle(_ context.Context, r slog.Record) error {
	handle := h.regHandle.Load()
	if handle == 0 {
		return nil
	}
	if r.Level < h.level.Level() {
		return nil
	}

	// Extract "event_id" attribute if present; collect remaining attrs for message.
	eventID := 0
	var buf strings.Builder

	if r.Message != "" {
		buf.WriteString(r.Message)
	}

	// Pre-computed attrs from WithAttrs.
	for _, a := range h.attrs {
		if a.Key == "event_id" {
			if a.Value.Kind() == slog.KindInt64 {
				eventID = int(a.Value.Int64())
			}
			continue
		}
		buf.WriteString(" ")
		buf.WriteString(a.Key)
		buf.WriteString("=")
		fmt.Fprintf(&buf, "%v", a.Value.Any())
	}

	// Record-level attrs.
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "event_id" {
			if a.Value.Kind() == slog.KindInt64 {
				eventID = int(a.Value.Int64())
			}
			return true
		}
		buf.WriteString(" ")
		buf.WriteString(a.Key)
		buf.WriteString("=")
		fmt.Fprintf(&buf, "%v", a.Value.Any())
		return true
	})

	desc := h.resolveDesc(r.Level, eventID)

	// Check EventEnabled with the resolved descriptor (cheap when channels are off).
	enabled, _, _ := procEventEnabled.Call(handle, uintptr(unsafe.Pointer(&desc)))
	if enabled == 0 {
		return nil
	}

	return etwWriteString(handle, &desc, buf.String())
}

func (h *ETWHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	n := &ETWHandler{
		level: h.level,
		group: h.group,
		attrs: make([]slog.Attr, len(h.attrs)+len(attrs)),
	}
	n.regHandle.Store(h.regHandle.Load())
	copy(n.attrs, h.attrs)
	copy(n.attrs[len(h.attrs):], attrs)
	return n
}

func (h *ETWHandler) WithGroup(name string) slog.Handler {
	prefix := name
	if h.group != "" {
		prefix = h.group + "." + name
	}
	n := &ETWHandler{
		level: h.level,
		attrs: h.attrs,
		group: prefix,
	}
	n.regHandle.Store(h.regHandle.Load())
	return n
}

// ── helpers ───────────────────────────────────────────────────────────────────

// resolveDesc returns the event descriptor for the given slog level and
// optional explicit eventID (0 = use generic fallback for the level).
func (h *ETWHandler) resolveDesc(level slog.Level, eventID int) eventDescriptor {
	if eventID != 0 {
		if desc, ok := eventIDToDesc[eventID]; ok {
			return desc
		}
	}
	return h.genericDesc(level, eventID)
}

// genericDesc returns a generic descriptor for the given level.
func (h *ETWHandler) genericDesc(level slog.Level, _ int) eventDescriptor {
	switch {
	case level >= slog.LevelError:
		return descGenericError
	case level >= slog.LevelWarn:
		return descGenericWarning
	case level >= slog.LevelInfo:
		return descGenericInfo
	default:
		return descGenericDebug
	}
}

// etwWriteString writes msg as a single UTF-16 string data field using
// EventWrite.  Using EventWrite (not EventWriteString) ensures proper
// channel routing via the manifest event descriptors.
func etwWriteString(handle uintptr, desc *eventDescriptor, msg string) error {
	ws := utf16.Encode([]rune(msg + "\x00"))
	if len(ws) == 0 {
		return nil
	}

	data := eventDataDescriptor{
		Ptr:  uint64(uintptr(unsafe.Pointer(&ws[0]))),
		Size: uint32(len(ws) * 2), // bytes
	}

	r, _, _ := procEventWrite.Call(
		handle,
		uintptr(unsafe.Pointer(desc)),
		1, // UserDataCount
		uintptr(unsafe.Pointer(&data)),
	)
	if r != 0 {
		return fmt.Errorf("EventWrite: %w", windows.Errno(r))
	}
	return nil
}
