//go:build windows

package watcher

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func synthetic4657XML(systemTime string) string {
	return `<Event>
<System>
<TimeCreated SystemTime="` + systemTime + `"/>
</System>
<EventData>
<Data Name="ObjectName">\REGISTRY\MACHINE\SYSTEM\CurrentControlSet\Control\Terminal Server</Data>
<Data Name="ObjectValueName">TSServerDrainMode</Data>
<Data Name="SubjectUserName">alice</Data>
<Data Name="SubjectDomainName">CONTOSO</Data>
</EventData>
</Event>`
}

func TestProcessEvent_UsesSystemTime(t *testing.T) {
	sub := &EventSubscriber{}
	want, err := time.Parse(time.RFC3339Nano, "2026-04-24T10:00:00Z")
	if err != nil {
		t.Fatalf("time.Parse: %v", err)
	}

	sub.processEvent(synthetic4657XML("2026-04-24T10:00:00Z"))

	got := sub.LatestAttribution()
	if got == nil {
		t.Fatal("LatestAttribution() = nil, want attribution")
	}
	if !got.Timestamp.Equal(want) {
		t.Fatalf("Timestamp = %s, want %s", got.Timestamp.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	}
	if got.User != `CONTOSO\alice` {
		t.Fatalf("User = %q, want %q", got.User, `CONTOSO\alice`)
	}
}

func TestWaitAttribution_IgnoresOlderEvents(t *testing.T) {
	t0 := time.Date(2026, 4, 24, 10, 0, 0, 0, time.UTC)
	sub := &EventSubscriber{
		latest: &RegistryChangeAttribution{
			Timestamp: t0,
			User:      `CONTOSO\alice`,
		},
	}

	got := sub.WaitAttribution(t0.Add(5*time.Second), 100*time.Millisecond)
	if got != "" {
		t.Fatalf("WaitAttribution() = %q, want empty string", got)
	}
}

func TestProcessEvent_FallbackOnUnparseableSystemTime(t *testing.T) {
	var logBuf bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	sub := &EventSubscriber{}
	before := time.Now()
	sub.processEvent(synthetic4657XML("definitely-not-rfc3339"))
	after := time.Now()

	got := sub.LatestAttribution()
	if got == nil {
		t.Fatal("LatestAttribution() = nil, want attribution")
	}
	if got.Timestamp.Before(before) || got.Timestamp.After(after) {
		t.Fatalf("Timestamp = %s, want within [%s, %s]", got.Timestamp.Format(time.RFC3339Nano), before.Format(time.RFC3339Nano), after.Format(time.RFC3339Nano))
	}
	if !strings.Contains(logBuf.String(), "evtspike: unparseable SystemTime") {
		t.Fatalf("warn log missing unparseable SystemTime message: %s", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "raw=definitely-not-rfc3339") {
		t.Fatalf("warn log missing raw value: %s", logBuf.String())
	}
}
