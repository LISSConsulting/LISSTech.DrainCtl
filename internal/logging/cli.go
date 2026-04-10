//go:build windows

package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// CLIHandler is a slog.Handler that writes human-readable log lines to an
// io.Writer (typically os.Stderr).
//
// Output format: <timestamp> [<LVL>] <msg> <key=value ...>\n
//
// Timestamps are in local time with UTC offset. Level tags are [DBG], [INF],
// [WRN], [ERR]. Records below the configurable minimum level are suppressed.
type CLIHandler struct {
	w     io.Writer
	level *slog.LevelVar
	mu    sync.Mutex
	attrs []slog.Attr
	group string // current group prefix
}

// NewCLIHandler returns a CLIHandler writing to w with the given minimum level.
func NewCLIHandler(w io.Writer, level *slog.LevelVar) *CLIHandler {
	return &CLIHandler{w: w, level: level}
}

func (h *CLIHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *CLIHandler) Handle(_ context.Context, r slog.Record) error {
	ts := r.Time.Local().Format(time.RFC3339Nano)
	// Trim nanoseconds to milliseconds and keep offset.
	if len(ts) > 23 {
		// e.g. "2006-01-02T15:04:05.999999999-07:00" → "2006-01-02T15:04:05.999-07:00"
		ts = ts[:23] + ts[len(ts)-6:]
	}

	var buf strings.Builder
	buf.WriteString(ts)
	buf.WriteString(" ")
	buf.WriteString(cliLevelTag(r.Level))

	if r.Message != "" {
		buf.WriteString(" ")
		buf.WriteString(r.Message)
	}

	// Pre-computed attrs from WithAttrs.
	for _, a := range h.attrs {
		appendAttr(&buf, a, h.group)
	}

	// Record-level attrs.
	r.Attrs(func(a slog.Attr) bool {
		appendAttr(&buf, a, h.group)
		return true
	})

	buf.WriteString("\n")

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, buf.String())
	return err
}

func (h *CLIHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	n := &CLIHandler{
		w:     h.w,
		level: h.level,
		group: h.group,
		attrs: make([]slog.Attr, len(h.attrs)+len(attrs)),
	}
	copy(n.attrs, h.attrs)
	copy(n.attrs[len(h.attrs):], attrs)
	return n
}

func (h *CLIHandler) WithGroup(name string) slog.Handler {
	prefix := name
	if h.group != "" {
		prefix = h.group + "." + name
	}
	return &CLIHandler{
		w:     h.w,
		level: h.level,
		attrs: h.attrs,
		group: prefix,
	}
}

// cliLevelTag returns the bracketed level string for the given level.
func cliLevelTag(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "[ERR]"
	case l >= slog.LevelWarn:
		return "[WRN]"
	case l >= slog.LevelInfo:
		return "[INF]"
	default:
		return "[DBG]"
	}
}

// appendAttr writes " key=value" to buf. Groups are flattened inline.
func appendAttr(buf *strings.Builder, a slog.Attr, group string) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		prefix := a.Key
		if group != "" {
			prefix = group + "." + a.Key
		}
		for _, ga := range a.Value.Group() {
			appendAttr(buf, ga, prefix)
		}
		return
	}

	key := a.Key
	if group != "" {
		key = group + "." + key
	}

	buf.WriteString(" ")
	buf.WriteString(key)
	buf.WriteString("=")
	writeAttrValue(buf, a.Value)
}

// writeAttrValue formats a slog.Value as a bare value or quoted string.
func writeAttrValue(buf *strings.Builder, v slog.Value) {
	switch v.Kind() {
	case slog.KindString:
		s := v.String()
		if strings.ContainsAny(s, " \t\n\"") {
			fmt.Fprintf(buf, "%q", s)
		} else {
			buf.WriteString(s)
		}
	case slog.KindTime:
		buf.WriteString(v.Time().Local().Format(time.RFC3339))
	case slog.KindDuration:
		fmt.Fprintf(buf, "%s", v.Duration())
	default:
		fmt.Fprintf(buf, "%v", v.Any())
	}
}
