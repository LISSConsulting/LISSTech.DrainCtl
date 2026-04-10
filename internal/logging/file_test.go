//go:build windows

package logging

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestFileHandler_LevelFiltering(t *testing.T) {
	cases := []struct {
		minLevel slog.Level
		emitAt   slog.Level
		want     bool
	}{
		{slog.LevelDebug, slog.LevelDebug, true},
		{slog.LevelInfo, slog.LevelDebug, false},
		{slog.LevelWarn, slog.LevelInfo, false},
		{slog.LevelError, slog.LevelWarn, false},
		{slog.LevelError, slog.LevelError, true},
	}

	for _, tc := range cases {
		var buf strings.Builder
		lv := &slog.LevelVar{}
		lv.Set(tc.minLevel)
		h := NewFileHandler(&buf, lv)
		logger := slog.New(h)

		switch tc.emitAt {
		case slog.LevelDebug:
			logger.Debug("msg")
		case slog.LevelInfo:
			logger.Info("msg")
		case slog.LevelWarn:
			logger.Warn("msg")
		case slog.LevelError:
			logger.Error("msg")
		}

		wrote := buf.Len() > 0
		if wrote != tc.want {
			t.Errorf("minLevel=%v emitAt=%v: wrote=%v want=%v", tc.minLevel, tc.emitAt, wrote, tc.want)
		}
	}
}

func TestFileHandler_LevelTags(t *testing.T) {
	cases := []struct {
		level slog.Level
		tag   string
	}{
		{slog.LevelDebug, "DBG"},
		{slog.LevelInfo, "INF"},
		{slog.LevelWarn, "WRN"},
		{slog.LevelError, "ERR"},
	}

	for _, tc := range cases {
		var buf strings.Builder
		lv := &slog.LevelVar{}
		lv.Set(slog.LevelDebug)
		h := NewFileHandler(&buf, lv)
		r := slog.NewRecord(time.Now(), tc.level, "msg", 0)
		_ = h.Handle(nil, r) //nolint:staticcheck
		out := buf.String()
		// Should contain the bare tag without brackets.
		if !strings.Contains(out, " "+tc.tag+" ") && !strings.Contains(out, " "+tc.tag+"\n") {
			t.Errorf("level %v: output %q does not contain tag %q", tc.level, out, tc.tag)
		}
	}
}

func TestFileHandler_LocalTimestampWithOffset(t *testing.T) {
	var buf strings.Builder
	lv := &slog.LevelVar{}
	lv.Set(slog.LevelDebug)
	h := NewFileHandler(&buf, lv)

	r := slog.NewRecord(time.Now(), slog.LevelInfo, "msg", 0)
	_ = h.Handle(nil, r) //nolint:staticcheck

	out := buf.String()

	// Timestamp must contain T separator (RFC3339 format).
	if !strings.Contains(out, "T") {
		t.Errorf("output %q missing T in timestamp (not RFC3339 format)", out)
	}

	// Must NOT end timestamp with Z (UTC suffix) — we want local time with offset.
	// Find the timestamp portion (up to the first space after T).
	parts := strings.SplitN(out, " ", 2)
	if len(parts) > 0 {
		ts := parts[0]
		if strings.HasSuffix(ts, "Z") {
			t.Errorf("timestamp %q ends with Z (UTC); want local time with offset", ts)
		}
	}
}

func TestFileHandler_StructuredAttributes(t *testing.T) {
	var buf strings.Builder
	lv := &slog.LevelVar{}
	lv.Set(slog.LevelDebug)
	h := NewFileHandler(&buf, lv)
	logger := slog.New(h)

	logger.Info("check", "host", "SERVER01", "exit", 0)

	out := buf.String()
	if !strings.Contains(out, "host=SERVER01") {
		t.Errorf("output %q missing host=SERVER01", out)
	}
	if !strings.Contains(out, "exit=0") {
		t.Errorf("output %q missing exit=0", out)
	}
}
