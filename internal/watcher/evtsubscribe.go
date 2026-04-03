//go:build windows

package watcher

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// ── wevtapi.dll bindings ───────────────────────────────────────────────────

var (
	wevtapi          = windows.NewLazySystemDLL("wevtapi.dll")
	procEvtSubscribe = wevtapi.NewProc("EvtSubscribe")
	procEvtClose     = wevtapi.NewProc("EvtClose")
	procEvtRender    = wevtapi.NewProc("EvtRender")
)

// EvtSubscribe flags
const (
	evtSubscribeToFutureEvents = 1
	evtRenderEventXml          = 1
)

type evtHandle uintptr

// ── XML event types (local copy for parsing) ──────────────────────────────

type eventRec struct {
	System    eventSystem    `xml:"System"`
	EventData eventDataItems `xml:"EventData"`
}

type eventSystem struct {
	TimeCreated struct {
		SystemTime string `xml:"SystemTime,attr"`
	} `xml:"TimeCreated"`
}

type eventDataItems struct {
	Data []eventDataItem `xml:"Data"`
}

type eventDataItem struct {
	Name  string `xml:"Name,attr"`
	Value string `xml:",chardata"`
}

func (e *eventRec) dataValue(name string) string {
	for _, d := range e.EventData.Data {
		if d.Name == name {
			return d.Value
		}
	}
	return ""
}

// ── Attribution record ─────────────────────────────────────────────────────

// RegistryChangeAttribution holds info about who changed a registry value.
type RegistryChangeAttribution struct {
	Timestamp time.Time
	User      string // DOMAIN\user or just user
}

// ── Event subscriber ───────────────────────────────────────────────────────

// EventSubscriber subscribes to Security Event ID 4657 (registry value
// modified) and stores the most recent attribution for TSServerDrainMode
// changes. The service correlates this with RegNotifyChangeKeyValue.
type EventSubscriber struct {
	mu     sync.RWMutex
	latest *RegistryChangeAttribution
	log    dc.LogFunc

	subscription evtHandle
	signalEvent  windows.Handle
	cancelEvent  windows.Handle
	done         chan struct{}
}

// NewEventSubscriber creates a subscription to Security log Event ID 4657.
// Attribution events matching TSServerDrainMode are stored and retrievable
// via LatestAttribution. The subscriber runs until ctx is cancelled.
func NewEventSubscriber(ctx context.Context, log dc.LogFunc) (*EventSubscriber, error) {
	if log == nil {
		log = dc.DiscardLogger()
	}

	signalEvent, err := windows.CreateEvent(nil, 0, 0, nil) // auto-reset
	if err != nil {
		return nil, fmt.Errorf("create signal event: %w", err)
	}

	cancelEvent, err := windows.CreateEvent(nil, 1, 0, nil) // manual-reset
	if err != nil {
		_ = windows.CloseHandle(signalEvent)
		return nil, fmt.Errorf("create cancel event: %w", err)
	}

	// XPath query: Event ID 4657 in Security log.
	query, err := windows.UTF16PtrFromString(
		"*[System[EventID=4657]]",
	)
	if err != nil {
		_ = windows.CloseHandle(signalEvent)
		_ = windows.CloseHandle(cancelEvent)
		return nil, err
	}

	channel, err := windows.UTF16PtrFromString("Security")
	if err != nil {
		_ = windows.CloseHandle(signalEvent)
		_ = windows.CloseHandle(cancelEvent)
		return nil, err
	}

	// EvtSubscribe(Session, SignalEvent, ChannelPath, Query, Bookmark, Context, Callback, Flags)
	// We use signal-event mode (Callback=0) so we can wait with cancellation.
	r, _, e := procEvtSubscribe.Call(
		0, // session (local)
		uintptr(signalEvent),
		uintptr(unsafe.Pointer(channel)),
		uintptr(unsafe.Pointer(query)),
		0, // bookmark
		0, // context
		0, // callback (using signal mode)
		evtSubscribeToFutureEvents,
	)
	if r == 0 {
		_ = windows.CloseHandle(signalEvent)
		_ = windows.CloseHandle(cancelEvent)
		return nil, fmt.Errorf("EvtSubscribe: %w", e)
	}

	sub := &EventSubscriber{
		log:          log,
		subscription: evtHandle(r),
		signalEvent:  signalEvent,
		cancelEvent:  cancelEvent,
		done:         make(chan struct{}),
	}

	go sub.run(ctx)
	log(dc.LvlINF, "evt_subscriber=started", "event_id=4657")
	return sub, nil
}

// LatestAttribution returns the most recent TSServerDrainMode change
// attribution, or nil if none has been observed.
func (s *EventSubscriber) LatestAttribution() *RegistryChangeAttribution {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.latest
}

// WaitAttribution waits up to timeout for a new attribution event that
// occurred after `after`. Returns the user string or "" if timeout.
func (s *EventSubscriber) WaitAttribution(after time.Time, timeout time.Duration) string {
	deadline := time.After(timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			return ""
		case <-ticker.C:
			if attr := s.LatestAttribution(); attr != nil && attr.Timestamp.After(after) {
				return attr.User
			}
		}
	}
}

// Done returns a channel that is closed when the subscriber exits.
func (s *EventSubscriber) Done() <-chan struct{} {
	return s.done
}

func (s *EventSubscriber) run(ctx context.Context) {
	defer close(s.done)
	defer s.close()

	// Bridge context cancellation to Windows event.
	go func() {
		<-ctx.Done()
		_ = windows.SetEvent(s.cancelEvent)
	}()

	handles := []windows.Handle{s.signalEvent, s.cancelEvent}

	// We also need EvtNext to pull events after the signal fires.
	procEvtNext := wevtapi.NewProc("EvtNext")

	for {
		idx, err := windows.WaitForMultipleObjects(handles, false, windows.INFINITE)
		if err != nil || idx != windows.WAIT_OBJECT_0 {
			return // cancelled or error
		}

		// Pull all available events.
		for {
			var evtHandles [8]uintptr
			var returned uint32

			// EvtNext(ResultSet, EventsSize, Events, Timeout, Flags, Returned)
			r, _, _ := procEvtNext.Call(
				uintptr(s.subscription),
				uintptr(len(evtHandles)),
				uintptr(unsafe.Pointer(&evtHandles[0])),
				0, // no timeout (non-blocking since signal fired)
				0, // flags
				uintptr(unsafe.Pointer(&returned)),
			)
			if r == 0 || returned == 0 {
				break // no more events
			}

			for i := uint32(0); i < returned; i++ {
				s.processEvent(evtHandle(evtHandles[i]))
				_, _, _ = procEvtClose.Call(evtHandles[i])
			}
		}
	}
}

func (s *EventSubscriber) processEvent(h evtHandle) {
	xmlStr := s.renderEventXML(h)
	if xmlStr == "" {
		return
	}

	var evt eventRec
	if err := xml.Unmarshal([]byte(xmlStr), &evt); err != nil {
		return
	}

	// Filter: must be TSServerDrainMode under Terminal Server key.
	objectName := strings.ToLower(evt.dataValue("ObjectName"))
	valueName := evt.dataValue("ObjectValueName")

	const tsSuffix = `\control\terminal server`
	if !strings.HasSuffix(objectName, tsSuffix) || !strings.EqualFold(valueName, "TSServerDrainMode") {
		return
	}

	subjectUser := evt.dataValue("SubjectUserName")
	subjectDomain := evt.dataValue("SubjectDomainName")

	if subjectUser == "" {
		return
	}

	user := subjectUser
	if subjectDomain != "" && strings.ToUpper(subjectDomain) != "NT AUTHORITY" {
		user = subjectDomain + `\` + subjectUser
	}

	attr := &RegistryChangeAttribution{
		Timestamp: time.Now(),
		User:      user,
	}

	s.mu.Lock()
	s.latest = attr
	s.mu.Unlock()

	s.log(dc.LvlINF, fmt.Sprintf("evt4657=received user=%s", user))
}

func (s *EventSubscriber) renderEventXML(h evtHandle) string {
	// First call to get required buffer size.
	var bufSize uint32
	var propCount uint32
	_, _, _ = procEvtRender.Call(
		0, // context
		uintptr(h),
		evtRenderEventXml,
		0,
		0,
		uintptr(unsafe.Pointer(&bufSize)),
		uintptr(unsafe.Pointer(&propCount)),
	)
	if bufSize == 0 {
		return ""
	}

	buf := make([]uint16, bufSize/2)
	r, _, _ := procEvtRender.Call(
		0,
		uintptr(h),
		evtRenderEventXml,
		uintptr(bufSize),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&bufSize)),
		uintptr(unsafe.Pointer(&propCount)),
	)
	if r == 0 {
		return ""
	}

	return windows.UTF16ToString(buf)
}

func (s *EventSubscriber) close() {
	if s.subscription != 0 {
		_, _, _ = procEvtClose.Call(uintptr(s.subscription))
		s.subscription = 0
	}
	if s.signalEvent != 0 {
		_ = windows.CloseHandle(s.signalEvent)
		s.signalEvent = 0
	}
	if s.cancelEvent != 0 {
		_ = windows.CloseHandle(s.cancelEvent)
		s.cancelEvent = 0
	}
}
