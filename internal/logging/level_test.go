//go:build windows

package logging

import (
	"log/slog"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := []struct {
		input   string
		want    slog.Level
		wantErr bool
	}{
		// Valid — lowercase
		{"debug", slog.LevelDebug, false},
		{"info", slog.LevelInfo, false},
		{"warn", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		// Valid — uppercase
		{"DEBUG", slog.LevelDebug, false},
		{"INFO", slog.LevelInfo, false},
		{"WARN", slog.LevelWarn, false},
		{"ERROR", slog.LevelError, false},
		// Valid — mixed case
		{"Debug", slog.LevelDebug, false},
		{"Info", slog.LevelInfo, false},
		{"Warn", slog.LevelWarn, false},
		{"Error", slog.LevelError, false},
		// Invalid
		{"", slog.LevelInfo, true},
		{"verbose", slog.LevelInfo, true},
		{"trace", slog.LevelInfo, true},
		{"warning", slog.LevelInfo, true},
		{"err", slog.LevelInfo, true},
		{"dbg", slog.LevelInfo, true},
		{"42", slog.LevelInfo, true},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParseLevel(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("ParseLevel(%q) = %v, nil error; want error", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Errorf("ParseLevel(%q) returned unexpected error: %v", tc.input, err)
				return
			}
			if got != tc.want {
				t.Errorf("ParseLevel(%q) = %v; want %v", tc.input, got, tc.want)
			}
		})
	}
}
