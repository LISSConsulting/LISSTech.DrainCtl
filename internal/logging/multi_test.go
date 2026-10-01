//go:build windows

package logging

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestMultiHandler_FanOut(t *testing.T) {
	var a, b strings.Builder
	lv := &slog.LevelVar{}
	lv.Set(slog.LevelDebug)

	mh := NewMultiHandler(
		NewCLIHandler(&a, lv),
		NewCLIHandler(&b, lv),
	)
	logger := slog.New(mh)
	logger.Info("broadcast")

	if !strings.Contains(a.String(), "broadcast") {
		t.Errorf("handler A did not receive the message: %q", a.String())
	}
	if !strings.Contains(b.String(), "broadcast") {
		t.Errorf("handler B did not receive the message: %q", b.String())
	}
}

func TestMultiHandler_PerHandlerLevelFiltering(t *testing.T) {
	var infoOut, debugOut strings.Builder

	infoLv := &slog.LevelVar{}
	infoLv.Set(slog.LevelInfo)

	debugLv := &slog.LevelVar{}
	debugLv.Set(slog.LevelDebug)

	mh := NewMultiHandler(
		NewCLIHandler(&infoOut, infoLv),
		NewCLIHandler(&debugOut, debugLv),
	)
	logger := slog.New(mh)

	logger.Debug("debug-only message")

	// The info-level handler must suppress the debug message.
	if strings.Contains(infoOut.String(), "debug-only message") {
		t.Errorf("info handler received debug message: %q", infoOut.String())
	}
	// The debug-level handler must pass the debug message.
	if !strings.Contains(debugOut.String(), "debug-only message") {
		t.Errorf("debug handler did not receive message: %q", debugOut.String())
	}
}

func TestMultiHandler_EnabledReturnsTrueIfAnyEnabled(t *testing.T) {
	infoLv := &slog.LevelVar{}
	infoLv.Set(slog.LevelInfo)

	errorLv := &slog.LevelVar{}
	errorLv.Set(slog.LevelError)

	mh := NewMultiHandler(
		NewCLIHandler(nil, infoLv),
		NewCLIHandler(nil, errorLv),
	)

	// Info level — the first handler is enabled.
	if !mh.Enabled(nil, slog.LevelInfo) { //nolint:staticcheck
		t.Error("Enabled(Info) should be true when at least one handler accepts Info")
	}
}

func TestMultiHandler_EnabledReturnsFalseIfNoneEnabled(t *testing.T) {
	errorLv := &slog.LevelVar{}
	errorLv.Set(slog.LevelError)

	mh := NewMultiHandler(
		NewCLIHandler(nil, errorLv),
	)

	if mh.Enabled(nil, slog.LevelDebug) { //nolint:staticcheck
		t.Error("Enabled(Debug) should be false when all handlers require Error+")
	}
}

func TestMultiHandler_HandleClonesRecord(t *testing.T) {
	// Verify that Handle does not share record state between handlers.
	var a, b strings.Builder
	lv := &slog.LevelVar{}
	lv.Set(slog.LevelDebug)

	mh := NewMultiHandler(
		NewCLIHandler(&a, lv),
		NewCLIHandler(&b, lv),
	)

	r := slog.NewRecord(time.Now(), slog.LevelInfo, "shared message", 0)
	r.AddAttrs(slog.String("key", "value"))
	_ = mh.Handle(nil, r) //nolint:staticcheck

	if !strings.Contains(a.String(), "key=value") {
		t.Errorf("handler A missing attr: %q", a.String())
	}
	if !strings.Contains(b.String(), "key=value") {
		t.Errorf("handler B missing attr: %q", b.String())
	}
}
