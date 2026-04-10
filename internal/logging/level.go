//go:build windows

package logging

import (
	"fmt"
	"log/slog"
	"strings"
)

// ParseLevel maps a level string to a slog.Level.
// Valid inputs: "debug", "info", "warn", "error" (case-insensitive).
// Returns an error for invalid input.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("invalid log level %q; valid levels: debug, info, warn, error", s)
	}
}
