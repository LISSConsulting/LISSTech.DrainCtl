//go:build windows

package main

import (
	"testing"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// ── getFormat ─────────────────────────────────────────────────────────────────

// TestGetFormat_EmptyFormatReturnsDefault verifies that when cfg.Format is
// empty the caller-supplied default is returned unchanged.
func TestGetFormat_EmptyFormatReturnsDefault(t *testing.T) {
	cfg.Format = ""
	got, err := getFormat(dc.FormatTable)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != dc.FormatTable {
		t.Errorf("got = %q, want %q (default)", got, dc.FormatTable)
	}
}

// TestGetFormat_AllValidFormats verifies that every valid format string is
// accepted and maps to the correct OutputFormat constant.
func TestGetFormat_AllValidFormats(t *testing.T) {
	cases := []struct {
		input string
		want  dc.OutputFormat
	}{
		{"plain", dc.FormatPlain},
		{"table", dc.FormatTable},
		{"csv", dc.FormatCSV},
		{"json", dc.FormatJSON},
	}
	for _, tc := range cases {
		cfg.Format = tc.input
		got, err := getFormat(dc.FormatPlain) // default is irrelevant when input is set
		if err != nil {
			t.Errorf("getFormat(%q): unexpected error: %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("getFormat(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
	cfg.Format = ""
}

// TestGetFormat_ValidFormatOverridesDefault verifies that a non-empty
// cfg.Format overrides the default passed by the caller.
func TestGetFormat_ValidFormatOverridesDefault(t *testing.T) {
	cfg.Format = "json"
	defer func() { cfg.Format = "" }()
	got, err := getFormat(dc.FormatTable)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != dc.FormatJSON {
		t.Errorf("got = %q, want %q", got, dc.FormatJSON)
	}
}

// TestGetFormat_InvalidFormatReturnsError verifies that an unrecognised format
// string causes ParseFormat to return an error.
func TestGetFormat_InvalidFormatReturnsError(t *testing.T) {
	cfg.Format = "bogus"
	defer func() { cfg.Format = "" }()
	_, err := getFormat(dc.FormatTable)
	if err == nil {
		t.Error("expected an error for invalid format string, got nil")
	}
}
