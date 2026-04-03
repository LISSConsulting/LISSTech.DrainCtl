//go:build windows

package drainctl

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// Level represents a structured log severity.
type Level string

const (
	LvlINF Level = "INF"
	LvlWRN Level = "WRN"
	LvlERR Level = "ERR"
	LvlOK  Level = "OK "
)

// LogFunc emits a single structured log line.
type LogFunc func(l Level, fields ...string)

// DefaultLogger returns a LogFunc that writes timestamped structured lines to w.
// If quiet is true, only OK and ERR levels are emitted.
func DefaultLogger(w io.Writer, quiet bool) LogFunc {
	return func(l Level, fields ...string) {
		if quiet && l != LvlOK && l != LvlERR {
			return
		}
		ts := time.Now().Format(time.RFC3339)
		_, _ = fmt.Fprintf(w, "%s [%s] %s\n", ts, l, strings.Join(fields, " "))
	}
}

// DiscardLogger returns a LogFunc that drops everything.
func DiscardLogger() LogFunc {
	return func(Level, ...string) {}
}

// LogMsg is a convenience for a level + freeform message + optional kv pairs.
func LogMsg(log LogFunc, l Level, msg string, fields ...string) {
	all := append([]string{fmt.Sprintf("msg=%q", msg)}, fields...)
	log(l, all...)
}
