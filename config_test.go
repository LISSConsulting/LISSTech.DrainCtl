//go:build windows

package drainctl

import (
	"testing"
)

// ── ClampRetention ────────────────────────────────────────────────────────────

func TestClampRetention_BelowMin(t *testing.T) {
	result := ClampRetention(0, nil)
	if result != MinRetentionDays {
		t.Errorf("ClampRetention(0) = %d, want %d", result, MinRetentionDays)
	}
}

func TestClampRetention_AboveMax(t *testing.T) {
	result := ClampRetention(999, nil)
	if result != MaxRetentionDays {
		t.Errorf("ClampRetention(999) = %d, want %d", result, MaxRetentionDays)
	}
}

func TestClampRetention_WithinRange(t *testing.T) {
	for _, days := range []int{1, 30, 90, 180, 365} {
		got := ClampRetention(days, nil)
		if got != days {
			t.Errorf("ClampRetention(%d) = %d, want %d", days, got, days)
		}
	}
}

func TestClampRetention_LogsWarning(t *testing.T) {
	var warned bool
	log := func(l Level, fields ...string) {
		if l == LvlWRN {
			warned = true
		}
	}
	ClampRetention(0, log)
	if !warned {
		t.Error("expected warning log for out-of-range retention, got none")
	}
}

// ── HasTrigger ────────────────────────────────────────────────────────────────

func TestHasTrigger_ExplicitMatch(t *testing.T) {
	target := NotificationTarget{
		Triggers: []Trigger{TriggerDrainOn, TriggerAlert},
	}
	if !target.HasTrigger(TriggerDrainOn) {
		t.Error("expected HasTrigger(drain_on) = true")
	}
	if !target.HasTrigger(TriggerAlert) {
		t.Error("expected HasTrigger(alert) = true")
	}
}

func TestHasTrigger_ExplicitMiss(t *testing.T) {
	target := NotificationTarget{
		Triggers: []Trigger{TriggerDrainOn},
	}
	if target.HasTrigger(TriggerAlert) {
		t.Error("expected HasTrigger(alert) = false when not in explicit list")
	}
}

func TestHasTrigger_EmptyTriggersUsesDefaults(t *testing.T) {
	target := NotificationTarget{} // empty Triggers → DefaultTriggers
	for _, tr := range DefaultTriggers {
		if !target.HasTrigger(tr) {
			t.Errorf("empty triggers: expected HasTrigger(%s) = true", tr)
		}
	}
}

func TestHasTrigger_EmptyTriggersExcludesNonDefault(t *testing.T) {
	target := NotificationTarget{} // empty Triggers → DefaultTriggers
	// TriggerGraceEntered is NOT in DefaultTriggers
	inDefaults := false
	for _, tr := range DefaultTriggers {
		if tr == TriggerGraceEntered {
			inDefaults = true
		}
	}
	if !inDefaults && target.HasTrigger(TriggerGraceEntered) {
		t.Error("TriggerGraceEntered should not fire when using DefaultTriggers")
	}
}

// ── ParseFormat ───────────────────────────────────────────────────────────────

func TestParseFormat_Valid(t *testing.T) {
	cases := []struct {
		input string
		want  OutputFormat
	}{
		{"plain", FormatPlain},
		{"", FormatPlain},
		{"table", FormatTable},
		{"csv", FormatCSV},
		{"json", FormatJSON},
	}
	for _, tc := range cases {
		got, err := ParseFormat(tc.input)
		if err != nil {
			t.Errorf("ParseFormat(%q) unexpected error: %v", tc.input, err)
		}
		if got != tc.want {
			t.Errorf("ParseFormat(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestParseFormat_Invalid(t *testing.T) {
	_, err := ParseFormat("xml")
	if err == nil {
		t.Error("ParseFormat(\"xml\") expected error, got nil")
	}
}

// ── Validate — URL scheme filtering ─────────────────────────────────────────

func TestValidate_StripsInvalidURLSchemes(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = []NotificationTarget{
		{Type: "webhook", URL: "https://example.com/hook"},
		{Type: "webhook", URL: "http://example.com/hook"},
		{Type: "webhook", URL: "ftp://bad.example.com"},
		{Type: "webhook", URL: "javascript:alert(1)"},
		{Type: "ntfy", URL: "https://ntfy.sh/topic"},
	}

	cfg.Validate(nil)

	for _, n := range cfg.Notifications {
		lower := n.URL
		if lower != "" &&
			!startsWith(lower, "http://") &&
			!startsWith(lower, "https://") {
			t.Errorf("Validate kept notification with invalid URL scheme: %s", n.URL)
		}
	}

	// Exactly 3 valid targets should remain (2 http/https webhooks + 1 ntfy).
	if len(cfg.Notifications) != 3 {
		t.Errorf("expected 3 valid notifications after Validate, got %d", len(cfg.Notifications))
	}
}

func TestValidate_PreservesEmptyURL(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = []NotificationTarget{
		{Type: "webhook", URL: ""},
	}
	cfg.Validate(nil)
	// Empty URL is not stripped by URL scheme validation (other logic handles it).
	if len(cfg.Notifications) != 1 {
		t.Errorf("expected empty-URL notification to survive Validate, got %d notifications", len(cfg.Notifications))
	}
}

// startsWith is a local helper to avoid importing strings in the test file.
func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// ── DefaultConfig ─────────────────────────────────────────────────────────────

func TestDefaultConfig_Defaults(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.GracePeriod != DefaultGracePeriod {
		t.Errorf("GracePeriod = %d, want %d", cfg.GracePeriod, DefaultGracePeriod)
	}
	if cfg.RetentionDays != DefaultRetentionDays {
		t.Errorf("RetentionDays = %d, want %d", cfg.RetentionDays, DefaultRetentionDays)
	}
	if cfg.PollInterval != DefaultPollInterval {
		t.Errorf("PollInterval = %d, want %d", cfg.PollInterval, DefaultPollInterval)
	}
	if cfg.Dashboard.Port != DefaultDashboardPort {
		t.Errorf("Dashboard.Port = %d, want %d", cfg.Dashboard.Port, DefaultDashboardPort)
	}
}
