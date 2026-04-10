//go:build windows

package logging

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
)

// FileHandler is a slog.Handler that writes structured log lines to an
// io.Writer (typically a rotating file log writer).
//
// Output format: <timestamp> <LVL> <msg> <key=value ...>\n
//
// Timestamps are in local time with UTC offset (FR-011). Level tags are
// DBG, INF, WRN, ERR (without brackets). Records below the configurable
// minimum level are suppressed.
type FileHandler struct {
	w     io.Writer
	level *slog.LevelVar
	mu    sync.Mutex
	attrs []slog.Attr
	group string
}

// NewFileHandler returns a FileHandler writing to w with the given minimum level.
func NewFileHandler(w io.Writer, level *slog.LevelVar) *FileHandler {
	return &FileHandler{w: w, level: level}
}

func (h *FileHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *FileHandler) Handle(_ context.Context, r slog.Record) error {
	// Local time with UTC offset, millisecond precision.
	ts := r.Time.Local().Format("2006-01-02T15:04:05.000-07:00")

	var buf strings.Builder
	buf.WriteString(ts)
	buf.WriteString(" ")
	buf.WriteString(fileLevelTag(r.Level))

	if r.Message != "" {
		buf.WriteString(" ")
		buf.WriteString(r.Message)
	}

	for _, a := range h.attrs {
		appendAttr(&buf, a, h.group)
	}

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

func (h *FileHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	n := &FileHandler{
		w:     h.w,
		level: h.level,
		group: h.group,
		attrs: make([]slog.Attr, len(h.attrs)+len(attrs)),
	}
	copy(n.attrs, h.attrs)
	copy(n.attrs[len(h.attrs):], attrs)
	return n
}

func (h *FileHandler) WithGroup(name string) slog.Handler {
	prefix := name
	if h.group != "" {
		prefix = h.group + "." + name
	}
	return &FileHandler{
		w:     h.w,
		level: h.level,
		attrs: h.attrs,
		group: prefix,
	}
}

// fileLevelTag returns the bare (un-bracketed) level string.
func fileLevelTag(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "ERR"
	case l >= slog.LevelWarn:
		return "WRN"
	case l >= slog.LevelInfo:
		return "INF"
	default:
		return "DBG"
	}
}
