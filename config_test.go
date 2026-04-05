//go:build windows

package drainctl

import (
	"testing"
	"time"
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

// ── Validate — trigger handling ───────────────────────────────────────────────

func TestValidate_EmptyTriggersDefaulted(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = []NotificationTarget{
		{Type: "webhook", URL: "https://example.com/hook", Triggers: []Trigger{}},
	}
	cfg.Validate(nil)
	// Empty trigger list must be replaced by DefaultTriggers.
	if len(cfg.Notifications[0].Triggers) != len(DefaultTriggers) {
		t.Errorf("expected %d default triggers after Validate, got %d",
			len(DefaultTriggers), len(cfg.Notifications[0].Triggers))
	}
	for _, want := range DefaultTriggers {
		found := false
		for _, got := range cfg.Notifications[0].Triggers {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("default trigger %q missing after Validate", want)
		}
	}
}

func TestValidate_StripsUnknownTriggers(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = []NotificationTarget{
		{
			Type: "webhook",
			URL:  "https://example.com/hook",
			Triggers: []Trigger{
				TriggerAlert,
				Trigger("not_a_real_trigger"),
				TriggerHealthy,
				Trigger("also_bogus"),
			},
		},
	}
	var warned bool
	log := func(l Level, fields ...string) {
		if l == LvlWRN {
			warned = true
		}
	}
	cfg.Validate(log)

	triggers := cfg.Notifications[0].Triggers
	if len(triggers) != 2 {
		t.Errorf("expected 2 triggers after stripping unknowns, got %d: %v", len(triggers), triggers)
	}
	for _, tr := range triggers {
		if !ValidTriggers[tr] {
			t.Errorf("unknown trigger %q survived Validate", tr)
		}
	}
	if !warned {
		t.Error("expected warning log for unknown trigger, got none")
	}
}

func TestValidate_PreservesValidTriggers(t *testing.T) {
	cfg := DefaultConfig()
	allValid := []Trigger{
		TriggerDrainOn, TriggerDrainOff, TriggerGraceEntered,
		TriggerAlert, TriggerHealthy, TriggerSessionWarning,
	}
	cfg.Notifications = []NotificationTarget{
		{Type: "webhook", URL: "https://example.com/hook", Triggers: allValid},
	}
	cfg.Validate(nil)
	if len(cfg.Notifications[0].Triggers) != len(allValid) {
		t.Errorf("Validate stripped valid triggers: got %v", cfg.Notifications[0].Triggers)
	}
}

// ── Validate — dashboard port range ──────────────────────────────────────────

func TestValidate_DashboardPortClamped(t *testing.T) {
	for _, port := range []int{0, -1, 65536, 99999} {
		cfg := DefaultConfig()
		cfg.Dashboard.Port = port
		cfg.Validate(nil)
		if cfg.Dashboard.Port != DefaultDashboardPort {
			t.Errorf("port %d: expected default %d after Validate, got %d", port, DefaultDashboardPort, cfg.Dashboard.Port)
		}
	}
}

func TestValidate_DashboardPortPreservesValid(t *testing.T) {
	for _, port := range []int{1, 80, 443, 8080, DefaultDashboardPort, 65535} {
		cfg := DefaultConfig()
		cfg.Dashboard.Port = port
		cfg.Validate(nil)
		if cfg.Dashboard.Port != port {
			t.Errorf("port %d: expected port unchanged after Validate, got %d", port, cfg.Dashboard.Port)
		}
	}
}

// ── Validate — dashboard group whitespace ─────────────────────────────────────

func TestValidate_DashboardGroupTrimsWhitespace(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"  Domain Admins  ", "Domain Admins"},
		{"\tRDS Admins\t", "RDS Admins"},
		{"  \t  ", DefaultDashboardGroup}, // all-whitespace falls back to default
	}
	for _, tc := range cases {
		cfg := DefaultConfig()
		cfg.Dashboard.Group = tc.input
		cfg.Validate(nil)
		if cfg.Dashboard.Group != tc.want {
			t.Errorf("group %q: expected %q after Validate, got %q", tc.input, tc.want, cfg.Dashboard.Group)
		}
	}
}

func TestValidate_DashboardGroupPreservesNormal(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Dashboard.Group = "RDS Admins"
	cfg.Validate(nil)
	if cfg.Dashboard.Group != "RDS Admins" {
		t.Errorf("expected group unchanged, got %q", cfg.Dashboard.Group)
	}
}

// ── Validate — grace period range ────────────────────────────────────────────

func TestValidate_GracePeriodClamped(t *testing.T) {
	for _, gp := range []int{0, -1, 1441, 99999} {
		cfg := DefaultConfig()
		cfg.GracePeriod = gp
		cfg.Validate(nil)
		if cfg.GracePeriod != DefaultGracePeriod {
			t.Errorf("grace_period %d: expected default %d after Validate, got %d", gp, DefaultGracePeriod, cfg.GracePeriod)
		}
	}
}

func TestValidate_GracePeriodPreservesValid(t *testing.T) {
	for _, gp := range []int{1, 60, DefaultGracePeriod, 720, 1440} {
		cfg := DefaultConfig()
		cfg.GracePeriod = gp
		cfg.Validate(nil)
		if cfg.GracePeriod != gp {
			t.Errorf("grace_period %d: expected value unchanged after Validate, got %d", gp, cfg.GracePeriod)
		}
	}
}

// ── Validate — type validation ───────────────────────────────────────────────

func TestValidate_StripsUnknownType(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = []NotificationTarget{
		{Type: "webhook", URL: "https://example.com/hook"},
		{Type: "email", URL: "https://example.com/email"}, // unknown
		{Type: "", URL: "https://example.com/empty"},      // unknown (empty)
		{Type: "ntfy", URL: "https://ntfy.sh/topic"},
		{Type: "slack", URL: "https://hooks.slack.com/foo"}, // unknown
	}

	cfg.Validate(nil)

	if len(cfg.Notifications) != 2 {
		t.Errorf("expected 2 valid notifications after Validate, got %d", len(cfg.Notifications))
	}
	for _, n := range cfg.Notifications {
		if n.Type != "webhook" && n.Type != "ntfy" {
			t.Errorf("Validate kept notification with unknown type %q", n.Type)
		}
	}
}

func TestValidate_UnknownTypeLogsWarning(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = []NotificationTarget{
		{Type: "fax", URL: "https://example.com/fax"},
	}

	var warned bool
	cfg.Validate(func(l Level, fields ...string) {
		if l == LvlWRN {
			warned = true
		}
	})

	if !warned {
		t.Error("expected Validate to log a warning for unknown notification type")
	}
	if len(cfg.Notifications) != 0 {
		t.Errorf("expected 0 notifications after stripping unknown type, got %d", len(cfg.Notifications))
	}
}

func TestValidate_PreservesValidTypes(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = []NotificationTarget{
		{Type: "webhook", URL: "https://example.com/hook"},
		{Type: "ntfy", URL: "https://ntfy.sh/topic"},
	}

	cfg.Validate(nil)

	if len(cfg.Notifications) != 2 {
		t.Errorf("expected both valid-type targets to survive Validate, got %d", len(cfg.Notifications))
	}
}

// ── Validate — poll interval range ───────────────────────────────────────────

func TestValidate_PollIntervalClamped(t *testing.T) {
	for _, interval := range []int{0, -1, 9, MaxPollInterval + 1, 999999} {
		cfg := DefaultConfig()
		cfg.PollInterval = interval
		cfg.Validate(nil)
		if cfg.PollInterval != DefaultPollInterval {
			t.Errorf("poll_interval %d: expected default %d after Validate, got %d", interval, DefaultPollInterval, cfg.PollInterval)
		}
	}
}

func TestValidate_PollIntervalPreservesValid(t *testing.T) {
	for _, interval := range []int{10, 60, DefaultPollInterval, 3600, MaxPollInterval} {
		cfg := DefaultConfig()
		cfg.PollInterval = interval
		cfg.Validate(nil)
		if cfg.PollInterval != interval {
			t.Errorf("poll_interval %d: expected value unchanged after Validate, got %d", interval, cfg.PollInterval)
		}
	}
}

// ── RepeatMinutes clamping ────────────────────────────────────────────────────

func TestValidate_RepeatMinutesClampsNegative(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = []NotificationTarget{
		{Type: "webhook", URL: "https://example.com/hook", RepeatMinutes: -1},
	}
	cfg.Validate(nil)
	if cfg.Notifications[0].RepeatMinutes != 0 {
		t.Errorf("RepeatMinutes -1: expected 0 after Validate, got %d", cfg.Notifications[0].RepeatMinutes)
	}
}

func TestValidate_RepeatMinutesClampsAboveMax(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = []NotificationTarget{
		{Type: "webhook", URL: "https://example.com/hook", RepeatMinutes: MaxRepeatMinutes + 1},
	}
	cfg.Validate(nil)
	if cfg.Notifications[0].RepeatMinutes != MaxRepeatMinutes {
		t.Errorf("RepeatMinutes %d: expected %d after Validate, got %d", MaxRepeatMinutes+1, MaxRepeatMinutes, cfg.Notifications[0].RepeatMinutes)
	}
}

func TestValidate_RepeatMinutesPreservesValid(t *testing.T) {
	for _, rm := range []int{0, 1, 60, 1440, MaxRepeatMinutes} {
		cfg := DefaultConfig()
		cfg.Notifications = []NotificationTarget{
			{Type: "webhook", URL: "https://example.com/hook", RepeatMinutes: rm},
		}
		cfg.Validate(nil)
		if cfg.Notifications[0].RepeatMinutes != rm {
			t.Errorf("RepeatMinutes %d: expected unchanged after Validate, got %d", rm, cfg.Notifications[0].RepeatMinutes)
		}
	}
}

// ── SessionWarningThreshold clamping ─────────────────────────────────────────

func TestValidate_SessionWarningThresholdClampsNegative(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SessionWarningThreshold = -1
	cfg.Validate(nil)
	if cfg.SessionWarningThreshold != 0 {
		t.Errorf("SessionWarningThreshold -1: expected 0 after Validate, got %d", cfg.SessionWarningThreshold)
	}
}

func TestValidate_SessionWarningThresholdClampsAbove100(t *testing.T) {
	for _, v := range []int{101, 200, 999} {
		cfg := DefaultConfig()
		cfg.SessionWarningThreshold = v
		cfg.Validate(nil)
		if cfg.SessionWarningThreshold != 100 {
			t.Errorf("SessionWarningThreshold %d: expected 100 after Validate, got %d", v, cfg.SessionWarningThreshold)
		}
	}
}

func TestValidate_SessionWarningThresholdPreservesValid(t *testing.T) {
	for _, v := range []int{0, 1, 50, 80, 100} {
		cfg := DefaultConfig()
		cfg.SessionWarningThreshold = v
		cfg.Validate(nil)
		if cfg.SessionWarningThreshold != v {
			t.Errorf("SessionWarningThreshold %d: expected unchanged after Validate, got %d", v, cfg.SessionWarningThreshold)
		}
	}
}

// ── HasTargets ────────────────────────────────────────────────────────────────

func TestHasTargets_NoTargets(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.HasTargets() {
		t.Error("HasTargets: expected false for config with no notifications")
	}
}

func TestHasTargets_TargetWithURL(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = []NotificationTarget{
		{Type: "webhook", URL: "https://example.com/hook"},
	}
	if !cfg.HasTargets() {
		t.Error("HasTargets: expected true for config with a webhook URL")
	}
}

func TestHasTargets_TargetWithEmptyURL(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = []NotificationTarget{
		{Type: "webhook", URL: ""},
	}
	if cfg.HasTargets() {
		t.Error("HasTargets: expected false for config with empty URL")
	}
}

func TestHasTargets_MixedURLs(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = []NotificationTarget{
		{Type: "webhook", URL: ""},
		{Type: "ntfy", URL: "https://ntfy.sh/my-alerts"},
	}
	if !cfg.HasTargets() {
		t.Error("HasTargets: expected true when at least one target has a URL")
	}
}

// ── ToServiceConfig ───────────────────────────────────────────────────────────

func TestToServiceConfig_ConvertsGracePeriodToMinutes(t *testing.T) {
	cfg := DefaultConfig()
	cfg.GracePeriod = 30 // 30 minutes
	svc := cfg.ToServiceConfig()
	want := 30 * time.Minute
	if svc.GracePeriod != want {
		t.Errorf("GracePeriod = %v, want %v", svc.GracePeriod, want)
	}
}

func TestToServiceConfig_ConvertsPollIntervalToSeconds(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PollInterval = 60 // 60 seconds
	svc := cfg.ToServiceConfig()
	want := 60 * time.Second
	if svc.PollInterval != want {
		t.Errorf("PollInterval = %v, want %v", svc.PollInterval, want)
	}
}

func TestToServiceConfig_CopiesFields(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RetentionDays = 42
	cfg.SessionWarningThreshold = 75
	cfg.AuditPath = `C:\some\path.jsonl`
	svc := cfg.ToServiceConfig()
	if svc.RetentionDays != 42 {
		t.Errorf("RetentionDays = %d, want 42", svc.RetentionDays)
	}
	if svc.SessionWarningThreshold != 75 {
		t.Errorf("SessionWarningThreshold = %d, want 75", svc.SessionWarningThreshold)
	}
	if svc.AuditPath != `C:\some\path.jsonl` {
		t.Errorf("AuditPath = %q, want C:\\some\\path.jsonl", svc.AuditPath)
	}
}

// ── ToDashboardConfig ─────────────────────────────────────────────────────────

func TestToDashboardConfig_NilAutoPinIsFalse(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Dashboard.AutoPin = nil
	dash := cfg.ToDashboardConfig()
	if dash.AutoPin {
		t.Error("AutoPin: expected false when Dashboard.AutoPin is nil")
	}
}

func TestToDashboardConfig_TrueAutoPin(t *testing.T) {
	cfg := DefaultConfig()
	v := true
	cfg.Dashboard.AutoPin = &v
	dash := cfg.ToDashboardConfig()
	if !dash.AutoPin {
		t.Error("AutoPin: expected true when Dashboard.AutoPin is &true")
	}
}

func TestToDashboardConfig_FalseAutoPin(t *testing.T) {
	cfg := DefaultConfig()
	v := false
	cfg.Dashboard.AutoPin = &v
	dash := cfg.ToDashboardConfig()
	if dash.AutoPin {
		t.Error("AutoPin: expected false when Dashboard.AutoPin is &false")
	}
}

func TestToDashboardConfig_CopiesFields(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Dashboard.Enabled = true
	cfg.Dashboard.Port = 12345
	cfg.Dashboard.Group = "RDS Admins"
	cfg.Dashboard.URL = "https://dash:49470"
	cfg.Dashboard.TLSFingerprint = "aa:bb:cc"
	dash := cfg.ToDashboardConfig()
	if !dash.Enabled {
		t.Error("Enabled: expected true")
	}
	if dash.Port != 12345 {
		t.Errorf("Port = %d, want 12345", dash.Port)
	}
	if dash.Group != "RDS Admins" {
		t.Errorf("Group = %q, want \"RDS Admins\"", dash.Group)
	}
	if dash.URL != "https://dash:49470" {
		t.Errorf("URL = %q, want \"https://dash:49470\"", dash.URL)
	}
	if dash.TLSFingerprint != "aa:bb:cc" {
		t.Errorf("TLSFingerprint = %q, want \"aa:bb:cc\"", dash.TLSFingerprint)
	}
}

// ── Validate AuditPath ────────────────────────────────────────────────────────

func TestValidate_SetsDefaultAuditPath(t *testing.T) {
	cfg := &Config{AuditPath: ""}
	cfg.Validate(nil)
	if cfg.AuditPath != DefaultAuditPath() {
		t.Errorf("AuditPath = %q, want %q", cfg.AuditPath, DefaultAuditPath())
	}
}

func TestValidate_PreservesExistingAuditPath(t *testing.T) {
	const custom = `C:\custom\audit.jsonl`
	cfg := &Config{AuditPath: custom}
	cfg.Validate(nil)
	if cfg.AuditPath != custom {
		t.Errorf("AuditPath = %q, want %q", cfg.AuditPath, custom)
	}
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
