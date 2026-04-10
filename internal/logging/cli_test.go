//go:build windows

package logging

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestCLIHandler_LevelFiltering(t *testing.T) {
	cases := []struct {
		minLevel slog.Level
		emitAt   slog.Level
		want     bool
	}{
		{slog.LevelDebug, slog.LevelDebug, true},
		{slog.LevelDebug, slog.LevelInfo, true},
		{slog.LevelInfo, slog.LevelDebug, false},
		{slog.LevelInfo, slog.LevelInfo, true},
		{slog.LevelWarn, slog.LevelInfo, false},
		{slog.LevelWarn, slog.LevelWarn, true},
		{slog.LevelError, slog.LevelWarn, false},
		{slog.LevelError, slog.LevelError, true},
	}

	for _, tc := range cases {
		var buf strings.Builder
		lv := &slog.LevelVar{}
		lv.Set(tc.minLevel)
		h := NewCLIHandler(&buf, lv)
		logger := slog.New(h)

		switch tc.emitAt {
		case slog.LevelDebug:
			logger.Debug("test message")
		case slog.LevelInfo:
			logger.Info("test message")
		case slog.LevelWarn:
			logger.Warn("test message")
		case slog.LevelError:
			logger.Error("test message")
		}

		wrote := buf.Len() > 0
		if wrote != tc.want {
			t.Errorf("minLevel=%v emitAt=%v: wrote=%v want=%v", tc.minLevel, tc.emitAt, wrote, tc.want)
		}
	}
}

func TestCLIHandler_LevelTags(t *testing.T) {
	cases := []struct {
		level slog.Level
		tag   string
	}{
		{slog.LevelDebug, "[DBG]"},
		{slog.LevelInfo, "[INF]"},
		{slog.LevelWarn, "[WRN]"},
		{slog.LevelError, "[ERR]"},
	}

	for _, tc := range cases {
		var buf strings.Builder
		lv := &slog.LevelVar{}
		lv.Set(slog.LevelDebug)
		h := NewCLIHandler(&buf, lv)
		r := slog.NewRecord(time.Now(), tc.level, "msg", 0)
		_ = h.Handle(nil, r) //nolint:staticcheck
		if !strings.Contains(buf.String(), tc.tag) {
			t.Errorf("level %v: output %q does not contain tag %q", tc.level, buf.String(), tc.tag)
		}
	}
}

func TestCLIHandler_StructuredAttributes(t *testing.T) {
	var buf strings.Builder
	lv := &slog.LevelVar{}
	lv.Set(slog.LevelDebug)
	h := NewCLIHandler(&buf, lv)
	logger := slog.New(h)

	logger.Info("check", "host", "SERVER01", "drain_mode", "NoNewSessions")

	out := buf.String()
	if !strings.Contains(out, "host=SERVER01") {
		t.Errorf("output %q missing host=SERVER01", out)
	}
	if !strings.Contains(out, "drain_mode=NoNewSessions") {
		t.Errorf("output %q missing drain_mode=NoNewSessions", out)
	}
}

func TestCLIHandler_TimestampFormat(t *testing.T) {
	var buf strings.Builder
	lv := &slog.LevelVar{}
	lv.Set(slog.LevelDebug)
	h := NewCLIHandler(&buf, lv)
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "msg", 0)
	_ = h.Handle(nil, r) //nolint:staticcheck

	out := buf.String()
	// Timestamp should contain a T separator and an offset (+ or -).
	if !strings.Contains(out, "T") {
		t.Errorf("output %q missing T in timestamp", out)
	}
}

func TestCLIHandler_EmptyMessageWithAttrs(t *testing.T) {
	var buf strings.Builder
	lv := &slog.LevelVar{}
	lv.Set(slog.LevelDebug)
	h := NewCLIHandler(&buf, lv)
	logger := slog.New(h)

	logger.Info("", "key", "value")
	out := buf.String()
	if !strings.Contains(out, "key=value") {
		t.Errorf("output %q missing key=value", out)
	}
}

func TestCLIHandler_StringWithSpacesQuoted(t *testing.T) {
	var buf strings.Builder
	lv := &slog.LevelVar{}
	lv.Set(slog.LevelDebug)
	h := NewCLIHandler(&buf, lv)
	logger := slog.New(h)

	logger.Info("", "msg", "hello world")
	out := buf.String()
	if !strings.Contains(out, `msg="hello world"`) {
		t.Errorf("output %q missing quoted string", out)
	}
}
