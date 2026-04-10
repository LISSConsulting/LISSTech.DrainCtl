//go:build windows

package logging

import (
	"context"
	"log/slog"
)

// MultiHandler fans out slog records to a slice of slog.Handler values.
// Each handler performs its own Enabled() check so per-sink level filtering
// works correctly even when handlers have different minimum levels.
type MultiHandler struct {
	handlers []slog.Handler
}

// NewMultiHandler returns a MultiHandler that writes to all provided handlers.
func NewMultiHandler(handlers ...slog.Handler) *MultiHandler {
	return &MultiHandler{handlers: handlers}
}

// Enabled reports true if at least one child handler is enabled for level.
func (m *MultiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

// Handle fans the record out to every child handler whose Enabled() returns
// true for the record's level. The first non-nil error is returned.
func (m *MultiHandler) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// WithAttrs returns a new MultiHandler whose children have all been called
// with WithAttrs.
func (m *MultiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		out[i] = h.WithAttrs(attrs)
	}
	return &MultiHandler{handlers: out}
}

// WithGroup returns a new MultiHandler whose children have all been called
// with WithGroup.
func (m *MultiHandler) WithGroup(name string) slog.Handler {
	out := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		out[i] = h.WithGroup(name)
	}
	return &MultiHandler{handlers: out}
}
