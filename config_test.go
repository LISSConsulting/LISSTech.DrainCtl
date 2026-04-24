//go:build windows

package drainctl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// ── ClampRetention ────────────────────────────────────────────────────────────

func TestClampRetention_BelowMin(t *testing.T) {
	result := ClampRetention(0)
	if result != MinRetentionDays {
		t.Errorf("ClampRetention(0) = %d, want %d", result, MinRetentionDays)
	}
}

func TestClampRetention_AboveMax(t *testing.T) {
	result := ClampRetention(999)
	if result != MaxRetentionDays {
		t.Errorf("ClampRetention(999) = %d, want %d", result, MaxRetentionDays)
	}
}

func TestClampRetention_WithinRange(t *testing.T) {
	for _, days := range []int{1, 30, 90, 180, 365} {
		got := ClampRetention(days)
		if got != days {
			t.Errorf("ClampRetention(%d) = %d, want %d", days, got, days)
		}
	}
}

func TestClampRetention_LogsWarning(t *testing.T) {
	// ClampRetention now logs via slog internally; verify the return value only.
	if got := ClampRetention(0); got != MinRetentionDays {
		t.Errorf("ClampRetention(0) = %d, want %d", got, MinRetentionDays)
	}
}

func TestClampRetention_LogsWarning_AboveMax(t *testing.T) {
	// ClampRetention now logs via slog internally; verify the return value only.
	if got := ClampRetention(MaxRetentionDays + 1); got != MaxRetentionDays {
		t.Errorf("ClampRetention(%d) = %d, want %d", MaxRetentionDays+1, got, MaxRetentionDays)
	}
}

// ── ClampEvtSpike ─────────────────────────────────────────────────────────────

func TestClampEvtSpike_NilSafe(t *testing.T) {
	ClampEvtSpike(nil)
}

func TestClampEvtSpike_ZeroValuePromotesDefaults(t *testing.T) {
	cfg := EvtSpikeConfig{}
	ClampEvtSpike(&cfg)

	if cfg.MinCount != DefaultEvtSpikeMinCount {
		t.Errorf("MinCount = %d, want %d", cfg.MinCount, DefaultEvtSpikeMinCount)
	}
	if cfg.Threshold != DefaultEvtSpikeThreshold {
		t.Errorf("Threshold = %g, want %g", cfg.Threshold, DefaultEvtSpikeThreshold)
	}
	if cfg.CooldownMinutes != DefaultEvtSpikeCooldownMinutes {
		t.Errorf("CooldownMinutes = %d, want %d", cfg.CooldownMinutes, DefaultEvtSpikeCooldownMinutes)
	}
	if cfg.SlotMaturityObservations != DefaultEvtSpikeSlotMaturityObservations {
		t.Errorf("SlotMaturityObservations = %d, want %d", cfg.SlotMaturityObservations, DefaultEvtSpikeSlotMaturityObservations)
	}
	if cfg.PersistIntervalSeconds != DefaultEvtSpikePersistIntervalSeconds {
		t.Errorf("PersistIntervalSeconds = %d, want %d", cfg.PersistIntervalSeconds, DefaultEvtSpikePersistIntervalSeconds)
	}
	if cfg.HalfLifeBuckets != DefaultEvtSpikeHalfLifeBuckets {
		t.Errorf("HalfLifeBuckets = %d, want %d", cfg.HalfLifeBuckets, DefaultEvtSpikeHalfLifeBuckets)
	}
	if cfg.PriorStrength != DefaultEvtSpikePriorStrength {
		t.Errorf("PriorStrength = %g, want %g", cfg.PriorStrength, DefaultEvtSpikePriorStrength)
	}
	if cfg.MeanPerBucketPrior != DefaultEvtSpikeMeanPerBucketPrior {
		t.Errorf("MeanPerBucketPrior = %g, want %g", cfg.MeanPerBucketPrior, DefaultEvtSpikeMeanPerBucketPrior)
	}
	if cfg.DisabledChannels == nil {
		t.Error("DisabledChannels = nil, want non-nil empty slice")
	}
	if cfg.AddedChannels == nil {
		t.Error("AddedChannels = nil, want non-nil empty slice")
	}
	if cfg.Enabled {
		t.Error("Enabled = true, want false (zero value)")
	}
}

func TestClampEvtSpike_BelowMinClampsToMin(t *testing.T) {
	cfg := EvtSpikeConfig{
		MinCount:                 -5,
		Threshold:                1e-12,
		CooldownMinutes:          -1,
		SlotMaturityObservations: -1,
		PersistIntervalSeconds:   30,
		HalfLifeBuckets:          10,
		PriorStrength:            0.1,
		MeanPerBucketPrior:       -1.0,
	}
	ClampEvtSpike(&cfg)

	if cfg.MinCount != MinEvtSpikeMinCount {
		t.Errorf("MinCount = %d, want %d", cfg.MinCount, MinEvtSpikeMinCount)
	}
	if cfg.Threshold != MinEvtSpikeThreshold {
		t.Errorf("Threshold = %g, want %g", cfg.Threshold, MinEvtSpikeThreshold)
	}
	if cfg.CooldownMinutes != MinEvtSpikeCooldownMinutes {
		t.Errorf("CooldownMinutes = %d, want %d", cfg.CooldownMinutes, MinEvtSpikeCooldownMinutes)
	}
	if cfg.SlotMaturityObservations != MinEvtSpikeSlotMaturityObservations {
		t.Errorf("SlotMaturityObservations = %d, want %d", cfg.SlotMaturityObservations, MinEvtSpikeSlotMaturityObservations)
	}
	if cfg.PersistIntervalSeconds != MinEvtSpikePersistIntervalSeconds {
		t.Errorf("PersistIntervalSeconds = %d, want %d", cfg.PersistIntervalSeconds, MinEvtSpikePersistIntervalSeconds)
	}
	if cfg.HalfLifeBuckets != MinEvtSpikeHalfLifeBuckets {
		t.Errorf("HalfLifeBuckets = %d, want %d", cfg.HalfLifeBuckets, MinEvtSpikeHalfLifeBuckets)
	}
	if cfg.PriorStrength != MinEvtSpikePriorStrength {
		t.Errorf("PriorStrength = %g, want %g", cfg.PriorStrength, MinEvtSpikePriorStrength)
	}
	if cfg.MeanPerBucketPrior != MinEvtSpikeMeanPerBucketPrior {
		t.Errorf("MeanPerBucketPrior = %g, want %g", cfg.MeanPerBucketPrior, MinEvtSpikeMeanPerBucketPrior)
	}
}

func TestClampEvtSpike_AboveMaxClampsToMax(t *testing.T) {
	cfg := EvtSpikeConfig{
		MinCount:                 99999,
		Threshold:                0.5,
		CooldownMinutes:          99999,
		SlotMaturityObservations: 9999,
		PersistIntervalSeconds:   1_000_000,
		HalfLifeBuckets:          99999,
		PriorStrength:            1e9,
		MeanPerBucketPrior:       1e9,
	}
	ClampEvtSpike(&cfg)

	if cfg.MinCount != MaxEvtSpikeMinCount {
		t.Errorf("MinCount = %d, want %d", cfg.MinCount, MaxEvtSpikeMinCount)
	}
	if cfg.Threshold != MaxEvtSpikeThreshold {
		t.Errorf("Threshold = %g, want %g", cfg.Threshold, MaxEvtSpikeThreshold)
	}
	if cfg.CooldownMinutes != MaxEvtSpikeCooldownMinutes {
		t.Errorf("CooldownMinutes = %d, want %d", cfg.CooldownMinutes, MaxEvtSpikeCooldownMinutes)
	}
	if cfg.SlotMaturityObservations != MaxEvtSpikeSlotMaturityObservations {
		t.Errorf("SlotMaturityObservations = %d, want %d", cfg.SlotMaturityObservations, MaxEvtSpikeSlotMaturityObservations)
	}
	if cfg.PersistIntervalSeconds != MaxEvtSpikePersistIntervalSeconds {
		t.Errorf("PersistIntervalSeconds = %d, want %d", cfg.PersistIntervalSeconds, MaxEvtSpikePersistIntervalSeconds)
	}
	if cfg.HalfLifeBuckets != MaxEvtSpikeHalfLifeBuckets {
		t.Errorf("HalfLifeBuckets = %d, want %d", cfg.HalfLifeBuckets, MaxEvtSpikeHalfLifeBuckets)
	}
	if cfg.PriorStrength != MaxEvtSpikePriorStrength {
		t.Errorf("PriorStrength = %g, want %g", cfg.PriorStrength, MaxEvtSpikePriorStrength)
	}
	if cfg.MeanPerBucketPrior != MaxEvtSpikeMeanPerBucketPrior {
		t.Errorf("MeanPerBucketPrior = %g, want %g", cfg.MeanPerBucketPrior, MaxEvtSpikeMeanPerBucketPrior)
	}
}

func TestClampEvtSpike_WithinRangeIsUnchanged(t *testing.T) {
	cfg := EvtSpikeConfig{
		Enabled:                  true,
		MinCount:                 25,
		Threshold:                5e-3,
		CooldownMinutes:          30,
		SlotMaturityObservations: 5,
		PersistIntervalSeconds:   1800, // multiple of 900
		HalfLifeBuckets:          720,
		PriorStrength:            120,
		MeanPerBucketPrior:       0.5,
		DisabledChannels:         []string{"X"},
		AddedChannels:            []string{"Y"},
		SecurityChannelEnabled:   true,
	}
	want := cfg
	ClampEvtSpike(&cfg)

	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("ClampEvtSpike mutated in-range values:\n got=%+v\nwant=%+v", cfg, want)
	}
}

func TestClampEvtSpike_PersistIntervalNonMultipleOf900Warns(t *testing.T) {
	// We can't trivially intercept slog without rewiring; assert the value
	// stays put (it's in [60, 86400] and should not be re-clamped) so the
	// only behavioral effect is the warning, which the contract documents.
	cfg := EvtSpikeConfig{PersistIntervalSeconds: 450} // divisor of 900, in range
	ClampEvtSpike(&cfg)
	if cfg.PersistIntervalSeconds != 450 {
		t.Errorf("PersistIntervalSeconds = %d, want 450 (in range, only warned)", cfg.PersistIntervalSeconds)
	}

	cfg = EvtSpikeConfig{PersistIntervalSeconds: 1234} // arbitrary, in range
	ClampEvtSpike(&cfg)
	if cfg.PersistIntervalSeconds != 1234 {
		t.Errorf("PersistIntervalSeconds = %d, want 1234 (in range, only warned)", cfg.PersistIntervalSeconds)
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

// ── Validate: LogFileLevel / LogEventLevel ────────────────────────────────────

func TestValidate_LogFileLevelEmpty_UsesDefault(t *testing.T) {
	c := &Config{LogFileLevel: ""}
	c.Validate()
	if c.LogFileLevel != "info" {
		t.Errorf("LogFileLevel = %q, want \"info\"", c.LogFileLevel)
	}
}

func TestValidate_LogEventLevelEmpty_UsesDefault(t *testing.T) {
	c := &Config{LogEventLevel: ""}
	c.Validate()
	if c.LogEventLevel != "info" {
		t.Errorf("LogEventLevel = %q, want \"info\"", c.LogEventLevel)
	}
}

func TestValidate_LogFileLevelValid_Preserved(t *testing.T) {
	for _, lvl := range []string{"debug", "info", "warn", "error"} {
		c := &Config{LogFileLevel: lvl}
		c.Validate()
		if c.LogFileLevel != lvl {
			t.Errorf("LogFileLevel %q: got %q, want %q", lvl, c.LogFileLevel, lvl)
		}
	}
}

func TestValidate_LogEventLevelValid_Preserved(t *testing.T) {
	for _, lvl := range []string{"debug", "info", "warn", "error"} {
		c := &Config{LogEventLevel: lvl}
		c.Validate()
		if c.LogEventLevel != lvl {
			t.Errorf("LogEventLevel %q: got %q, want %q", lvl, c.LogEventLevel, lvl)
		}
	}
}

func TestValidate_LogFileLevelCaseInsensitive(t *testing.T) {
	c := &Config{LogFileLevel: "WARN"}
	c.Validate()
	if c.LogFileLevel != "warn" {
		t.Errorf("LogFileLevel = %q, want \"warn\"", c.LogFileLevel)
	}
}

func TestValidate_LogFileLevelInvalid_UsesDefault(t *testing.T) {
	c := &Config{LogFileLevel: "verbose"}
	c.Validate()
	if c.LogFileLevel != "info" {
		t.Errorf("LogFileLevel = %q, want \"info\" (default after invalid)", c.LogFileLevel)
	}
}

func TestValidate_LogEventLevelInvalid_UsesDefault(t *testing.T) {
	c := &Config{LogEventLevel: "trace"}
	c.Validate()
	if c.LogEventLevel != "info" {
		t.Errorf("LogEventLevel = %q, want \"info\" (default after invalid)", c.LogEventLevel)
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

	cfg.Validate()

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
	cfg.Validate()
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
	cfg.Validate()
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
	cfg.Validate()

	triggers := cfg.Notifications[0].Triggers
	if len(triggers) != 2 {
		t.Errorf("expected 2 triggers after stripping unknowns, got %d: %v", len(triggers), triggers)
	}
	for _, tr := range triggers {
		if !ValidTriggers[tr] {
			t.Errorf("unknown trigger %q survived Validate", tr)
		}
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
	cfg.Validate()
	if len(cfg.Notifications[0].Triggers) != len(allValid) {
		t.Errorf("Validate stripped valid triggers: got %v", cfg.Notifications[0].Triggers)
	}
}

// ── Validate — dashboard port range ──────────────────────────────────────────

func TestValidate_DashboardPortClamped(t *testing.T) {
	for _, port := range []int{0, -1, 65536, 99999} {
		cfg := DefaultConfig()
		cfg.Dashboard.Port = port
		cfg.Validate()
		if cfg.Dashboard.Port != DefaultDashboardPort {
			t.Errorf("port %d: expected default %d after Validate, got %d", port, DefaultDashboardPort, cfg.Dashboard.Port)
		}
	}
}

func TestValidate_DashboardPortPreservesValid(t *testing.T) {
	for _, port := range []int{1, 80, 443, 8080, DefaultDashboardPort, 65535} {
		cfg := DefaultConfig()
		cfg.Dashboard.Port = port
		cfg.Validate()
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
		cfg.Validate()
		if cfg.Dashboard.Group != tc.want {
			t.Errorf("group %q: expected %q after Validate, got %q", tc.input, tc.want, cfg.Dashboard.Group)
		}
	}
}

func TestValidate_DashboardGroupPreservesNormal(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Dashboard.Group = "RDS Admins"
	cfg.Validate()
	if cfg.Dashboard.Group != "RDS Admins" {
		t.Errorf("expected group unchanged, got %q", cfg.Dashboard.Group)
	}
}

// ── Validate — grace period range ────────────────────────────────────────────

func TestValidate_GracePeriodClamped(t *testing.T) {
	for _, gp := range []int{0, -1, 1441, 99999} {
		cfg := DefaultConfig()
		cfg.GracePeriod = gp
		cfg.Validate()
		if cfg.GracePeriod != DefaultGracePeriod {
			t.Errorf("grace_period %d: expected default %d after Validate, got %d", gp, DefaultGracePeriod, cfg.GracePeriod)
		}
	}
}

func TestValidate_GracePeriodPreservesValid(t *testing.T) {
	for _, gp := range []int{1, 60, DefaultGracePeriod, 720, 1440} {
		cfg := DefaultConfig()
		cfg.GracePeriod = gp
		cfg.Validate()
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
		{Type: "email", URL: "smtp://mail.example.com:587", From: "a@b.com", To: []string{"c@d.com"}},
		{Type: "", URL: "https://example.com/empty"}, // unknown (empty)
		{Type: "ntfy", URL: "https://ntfy.sh/topic"},
		{Type: "slack", URL: "https://hooks.slack.com/foo"}, // unknown
	}

	cfg.Validate()

	if len(cfg.Notifications) != 3 {
		t.Errorf("expected 3 valid notifications after Validate, got %d", len(cfg.Notifications))
	}
	for _, n := range cfg.Notifications {
		if n.Type != "webhook" && n.Type != "ntfy" && n.Type != "email" {
			t.Errorf("Validate kept notification with unknown type %q", n.Type)
		}
	}
}

func TestValidate_UnknownTypeLogsWarning(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = []NotificationTarget{
		{Type: "fax", URL: "https://example.com/fax"},
	}

	// Validate now logs via slog internally; verify the stripping behaviour only.
	cfg.Validate()

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

	cfg.Validate()

	if len(cfg.Notifications) != 2 {
		t.Errorf("expected both valid-type targets to survive Validate, got %d", len(cfg.Notifications))
	}
}

// ── Validate — poll interval range ───────────────────────────────────────────

func TestValidate_PollIntervalClamped(t *testing.T) {
	for _, interval := range []int{0, -1, 9, MaxPollInterval + 1, 999999} {
		cfg := DefaultConfig()
		cfg.PollInterval = interval
		cfg.Validate()
		if cfg.PollInterval != DefaultPollInterval {
			t.Errorf("poll_interval %d: expected default %d after Validate, got %d", interval, DefaultPollInterval, cfg.PollInterval)
		}
	}
}

func TestValidate_PollIntervalPreservesValid(t *testing.T) {
	for _, interval := range []int{10, 60, DefaultPollInterval, 3600, MaxPollInterval} {
		cfg := DefaultConfig()
		cfg.PollInterval = interval
		cfg.Validate()
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
	cfg.Validate()
	if cfg.Notifications[0].RepeatMinutes != 0 {
		t.Errorf("RepeatMinutes -1: expected 0 after Validate, got %d", cfg.Notifications[0].RepeatMinutes)
	}
}

func TestValidate_RepeatMinutesClampsAboveMax(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = []NotificationTarget{
		{Type: "webhook", URL: "https://example.com/hook", RepeatMinutes: MaxRepeatMinutes + 1},
	}
	cfg.Validate()
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
		cfg.Validate()
		if cfg.Notifications[0].RepeatMinutes != rm {
			t.Errorf("RepeatMinutes %d: expected unchanged after Validate, got %d", rm, cfg.Notifications[0].RepeatMinutes)
		}
	}
}

// ── SessionWarningThreshold clamping ─────────────────────────────────────────

func TestValidate_SessionWarningThresholdClampsNegative(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SessionWarningThreshold = -1
	cfg.Validate()
	if cfg.SessionWarningThreshold != 0 {
		t.Errorf("SessionWarningThreshold -1: expected 0 after Validate, got %d", cfg.SessionWarningThreshold)
	}
}

func TestValidate_SessionWarningThresholdClampsAbove100(t *testing.T) {
	for _, v := range []int{101, 200, 999} {
		cfg := DefaultConfig()
		cfg.SessionWarningThreshold = v
		cfg.Validate()
		if cfg.SessionWarningThreshold != 100 {
			t.Errorf("SessionWarningThreshold %d: expected 100 after Validate, got %d", v, cfg.SessionWarningThreshold)
		}
	}
}

func TestValidate_SessionWarningThresholdPreservesValid(t *testing.T) {
	for _, v := range []int{0, 1, 50, 80, 100} {
		cfg := DefaultConfig()
		cfg.SessionWarningThreshold = v
		cfg.Validate()
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
	cfg.Validate()
	if cfg.AuditPath != DefaultAuditPath() {
		t.Errorf("AuditPath = %q, want %q", cfg.AuditPath, DefaultAuditPath())
	}
}

func TestValidate_PreservesExistingAuditPath(t *testing.T) {
	// Non-legacy custom paths (e.g. an operator-configured SQLite DB location)
	// are preserved as-is by Validate.
	const custom = `C:\custom\drainctl.db`
	cfg := &Config{AuditPath: custom}
	cfg.Validate()
	if cfg.AuditPath != custom {
		t.Errorf("AuditPath = %q, want %q", cfg.AuditPath, custom)
	}
}

func TestValidate_RewritesLegacyJSONLAuditPath(t *testing.T) {
	// Pre-007 installs wrote audit.jsonl as the default AuditPath; that file
	// was retired when audit moved to SQLite in feature 007. Validate rewrites
	// any lingering audit.jsonl tail to the canonical DefaultDBPath so
	// operators don't see stale references to a file that no longer exists.
	const legacy = `C:\ProgramData\LISS Technologies\LISSTech DrainCtl\audit.jsonl`
	cfg := &Config{AuditPath: legacy}
	cfg.Validate()
	if cfg.AuditPath == legacy {
		t.Errorf("AuditPath = %q, want legacy path to be rewritten", cfg.AuditPath)
	}
	if cfg.AuditPath != DefaultDBPath() {
		t.Errorf("AuditPath = %q, want %q", cfg.AuditPath, DefaultDBPath())
	}
}

// ── Validate — URL scheme warning logging ────────────────────────────────────

// TestValidate_InvalidURLSchemeLogsWarning verifies that Validate emits a
// warning log when a notification target has an invalid URL scheme and a
// non-nil log function is provided.
func TestValidate_InvalidURLSchemeLogsWarning(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = []NotificationTarget{
		{Type: "webhook", URL: "ftp://bad.example.com"},
		{Type: "webhook", URL: "https://good.example.com"},
	}

	// Validate now logs via slog internally; verify stripping behaviour only.
	cfg.Validate()

	// The invalid-scheme target should be stripped; only the valid one survives.
	if len(cfg.Notifications) != 1 {
		t.Errorf("expected 1 notification after stripping invalid scheme, got %d", len(cfg.Notifications))
	}
}

// ── DefaultDataDir ────────────────────────────────────────────────────────────

// TestDefaultDataDir_FallsBackWhenProgramDataEmpty verifies that DefaultDataDir
// uses the hard-coded C:\ProgramData fallback when the ProgramData environment
// variable is not set.
func TestDefaultDataDir_FallsBackWhenProgramDataEmpty(t *testing.T) {
	t.Setenv("ProgramData", "")
	got := DefaultDataDir()
	want := `C:\ProgramData\LISS Technologies\LISSTech DrainCtl`
	if got != want {
		t.Errorf("DefaultDataDir() = %q, want %q", got, want)
	}
}

// ── DefaultConfigPath ─────────────────────────────────────────────────────────

func TestDefaultConfigPath_EndsWithConfigJSON(t *testing.T) {
	path := DefaultConfigPath()
	if !strings.HasSuffix(path, `\config.json`) {
		t.Errorf("DefaultConfigPath() = %q, want suffix '\\config.json'", path)
	}
}

func TestDefaultConfigPath_ContainsDataDir(t *testing.T) {
	path := DefaultConfigPath()
	dir := DefaultDataDir()
	if !strings.HasPrefix(path, dir) {
		t.Errorf("DefaultConfigPath() = %q, want prefix %q", path, dir)
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

// ── SaveConfig / LoadConfig ───────────────────────────────────────────────────

// TestSaveConfig_RoundTrip verifies that SaveConfig writes config.json and
// LoadConfig reads it back with all fields intact.
func TestSaveConfig_RoundTrip(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	cfg := DefaultConfig()
	cfg.GracePeriod = 45
	cfg.RetentionDays = 60
	cfg.Notifications = []NotificationTarget{
		{Type: "webhook", URL: "https://example.com/hook"},
	}

	if err := SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	got, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got.GracePeriod != 45 {
		t.Errorf("GracePeriod = %d, want 45", got.GracePeriod)
	}
	if got.RetentionDays != 60 {
		t.Errorf("RetentionDays = %d, want 60", got.RetentionDays)
	}
	if len(got.Notifications) != 1 || got.Notifications[0].URL != "https://example.com/hook" {
		t.Errorf("Notifications = %v, want 1 webhook target", got.Notifications)
	}
}

// TestSaveConfig_CreateDataDirError verifies that SaveConfig returns an error
// containing "create data dir" when os.MkdirAll cannot create the data directory.
// This is forced by placing a regular file at the first subdirectory component
// of the data path, so MkdirAll fails when trying to descend through it.
func TestSaveConfig_CreateDataDirError(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ProgramData", base)

	// DefaultDataDir() = base + `\LISS Technologies\LISSTech DrainCtl`
	// Block MkdirAll by placing a file where "LISS Technologies" would be.
	blocker := filepath.Join(base, "LISS Technologies")
	if err := os.WriteFile(blocker, []byte("blocked"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := SaveConfig(DefaultConfig())
	if err == nil {
		t.Fatal("expected error from SaveConfig when data dir cannot be created, got nil")
	}
	if !strings.Contains(err.Error(), "create data dir") {
		t.Errorf("error = %q, want 'create data dir' in message", err.Error())
	}
}

// TestSaveConfig_ValidatesBeforeSave verifies that SaveConfig clamps
// out-of-range values via Validate before writing.
func TestSaveConfig_ValidatesBeforeSave(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	cfg := DefaultConfig()
	cfg.GracePeriod = 99999 // out of range — will be clamped to default

	if err := SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	got, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got.GracePeriod == 99999 {
		t.Errorf("GracePeriod = 99999 survived SaveConfig — Validate not called")
	}
}

// TestLoadConfig_FreshInstall_ReturnsValidDefault verifies that LoadConfig
// returns a valid config (either from registry migration or a fresh default)
// when no config.json exists.
func TestLoadConfig_FreshInstall_ReturnsValidDefault(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	got, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got == nil {
		t.Fatal("LoadConfig returned nil config")
	}
	// A valid default must have a positive grace period.
	if got.GracePeriod < 1 {
		t.Errorf("GracePeriod = %d, want >= 1", got.GracePeriod)
	}
}

// TestLoadConfig_ParseError verifies that LoadConfig returns an error
// containing "parse" when config.json contains malformed JSON.
func TestLoadConfig_ParseError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)

	path := DefaultConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte("{invalid json}"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error = %q, want message containing 'parse'", err.Error())
	}
}

// TestLoadConfig_ReadError verifies that LoadConfig returns a non-nil error
// when the config path exists but cannot be read (directory where file expected).
func TestLoadConfig_ReadError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)

	// Create a directory at the config file path — ReadFile will fail with a
	// non-IsNotExist error, exercising the "read %s: %w" return.
	path := DefaultConfigPath()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected read error when config path is a directory, got nil")
	}
}

// ── UpdateNotifySettings ──────────────────────────────────────────────────────

// TestUpdateNotifySettings_AllNilIsNoOp verifies that passing all nil
// arguments leaves existing config values unchanged.
func TestUpdateNotifySettings_AllNilIsNoOp(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	cfg := DefaultConfig()
	cfg.GracePeriod = 30
	cfg.SessionWarningThreshold = 80
	if err := SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	if err := UpdateNotifySettings(nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("UpdateNotifySettings(nil,nil,nil): %v", err)
	}

	got, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got.GracePeriod != 30 {
		t.Errorf("GracePeriod = %d, want 30 (unchanged)", got.GracePeriod)
	}
	if got.SessionWarningThreshold != 80 {
		t.Errorf("SessionWarningThreshold = %d, want 80 (unchanged)", got.SessionWarningThreshold)
	}
}

// TestUpdateNotifySettings_UpdatesAllFields verifies that non-nil arguments
// update the respective fields in config.json.
func TestUpdateNotifySettings_UpdatesAllFields(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	if err := SaveConfig(DefaultConfig()); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	targets := []NotificationTarget{{Type: "webhook", URL: "https://example.com/hook"}}
	threshold := 60
	grace := 90

	if err := UpdateNotifySettings(&targets, &threshold, &grace, nil, nil); err != nil {
		t.Fatalf("UpdateNotifySettings: %v", err)
	}

	got, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(got.Notifications) != 1 || got.Notifications[0].URL != "https://example.com/hook" {
		t.Errorf("Notifications = %v, want 1 webhook", got.Notifications)
	}
	if got.SessionWarningThreshold != 60 {
		t.Errorf("SessionWarningThreshold = %d, want 60", got.SessionWarningThreshold)
	}
	if got.GracePeriod != 90 {
		t.Errorf("GracePeriod = %d, want 90", got.GracePeriod)
	}
}

func TestConfigRMW_NoInterleave(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	if err := SaveConfig(DefaultConfig()); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	values := make([]int, 20)
	for i := range values {
		values[i] = 10 + i
	}

	start := make(chan struct{})
	errCh := make(chan error, len(values))
	var wg sync.WaitGroup
	for _, pct := range values {
		pct := pct
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 10; i++ {
				if err := UpdateNotifySettings(nil, &pct, nil, nil, nil); err != nil {
					errCh <- err
					return
				}
			}
		}()
	}

	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("UpdateNotifySettings: %v", err)
		}
	}

	data, err := os.ReadFile(DefaultConfigPath())
	if err != nil {
		t.Fatalf("ReadFile(config.json): %v", err)
	}
	var got Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal(config.json): %v\n%s", err, string(data))
	}

	found := false
	for _, pct := range values {
		if got.SessionWarningThreshold == pct {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("SessionWarningThreshold = %d, want one of %v", got.SessionWarningThreshold, values)
	}
}

func TestConfigRMW_ConcurrentNotificationsAndGraceBothPersist(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	if err := SaveConfig(DefaultConfig()); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	wantTargets := []NotificationTarget{
		{Type: "webhook", URL: "https://example.com/hook"},
		{Type: "ntfy", URL: "https://ntfy.sh/drainctl"},
	}
	wantGrace := 135

	start := make(chan struct{})
	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 25; i++ {
			if err := UpdateNotifySettings(&wantTargets, nil, nil, nil, nil); err != nil {
				errCh <- err
				return
			}
		}
	}()

	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 25; i++ {
			if err := UpdateNotifySettings(nil, nil, &wantGrace, nil, nil); err != nil {
				errCh <- err
				return
			}
		}
	}()

	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent updater: %v", err)
		}
	}

	got, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !reflect.DeepEqual(got.Notifications, wantTargets) {
		t.Fatalf("Notifications = %#v, want %#v", got.Notifications, wantTargets)
	}
	if got.GracePeriod != wantGrace {
		t.Fatalf("GracePeriod = %d, want %d", got.GracePeriod, wantGrace)
	}
}

// TestUpdateNotifySettings_InvalidThreshold verifies that an out-of-range
// session threshold returns a descriptive validation error.
func TestUpdateNotifySettings_InvalidThreshold(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	if err := SaveConfig(DefaultConfig()); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	for _, pct := range []int{-1, 101} {
		v := pct
		err := UpdateNotifySettings(nil, &v, nil, nil, nil)
		if err == nil {
			t.Errorf("UpdateNotifySettings(threshold=%d): expected error, got nil", pct)
			continue
		}
		if !strings.Contains(err.Error(), "threshold") {
			t.Errorf("UpdateNotifySettings(threshold=%d): error = %q, want message containing 'threshold'", pct, err.Error())
		}
	}
}

// TestUpdateNotifySettings_InvalidGracePeriod verifies that an out-of-range
// grace period returns a descriptive validation error.
func TestUpdateNotifySettings_InvalidGracePeriod(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	if err := SaveConfig(DefaultConfig()); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	for _, m := range []int{0, 1441} {
		v := m
		err := UpdateNotifySettings(nil, nil, &v, nil, nil)
		if err == nil {
			t.Errorf("UpdateNotifySettings(grace=%d): expected error, got nil", m)
			continue
		}
		if !strings.Contains(err.Error(), "grace period") {
			t.Errorf("UpdateNotifySettings(grace=%d): error = %q, want message containing 'grace period'", m, err.Error())
		}
	}
}

// ── UpdateNotifySettings_LoadError ────────────────────────────────────────────

// blockConfigRead sets up a temp ProgramData and places a directory at the
// config.json path so that os.ReadFile returns a non-IsNotExist error.
func blockConfigRead(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)
	path := DefaultConfigPath()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("blockConfigRead: MkdirAll: %v", err)
	}
}

// TestUpdateNotifySettings_LoadError verifies that UpdateNotifySettings
// propagates a LoadConfig error.
func TestUpdateNotifySettings_LoadError(t *testing.T) {
	blockConfigRead(t)
	if err := UpdateNotifySettings(nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected error from UpdateNotifySettings when LoadConfig fails, got nil")
	}
}

// ── LoadConfig_FreshInstall_WriteDefaultError ─────────────────────────────────

// TestLoadConfig_FreshInstall_WriteDefaultError verifies that LoadConfig returns
// a "write default config" error when no config.json exists but the temp file
// write in saveConfigToFile is blocked (a directory placed at the tmp path).
func TestLoadConfig_FreshInstall_WriteDefaultError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)

	path := DefaultConfigPath()
	// Create the data directory so os.ReadFile gets IsNotExist on config.json
	// and os.MkdirAll in saveConfigToFile succeeds.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Place a directory at the tmp file path so os.WriteFile fails.
	tmpPath := path + ".tmp"
	if err := os.MkdirAll(tmpPath, 0o755); err != nil {
		t.Fatalf("MkdirAll tmpPath: %v", err)
	}

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected error when default config write fails, got nil")
	}
	if !strings.Contains(err.Error(), "write default config") {
		t.Errorf("error = %q, want message containing 'write default config'", err.Error())
	}
}

// ── InstallCertificate ────────────────────────────────────────────────────────

// TestInstallCertificate_HappyPath verifies that InstallCertificate copies the
// source cert and key into the data directory and updates config.json with the
// new TLSCert and TLSKey paths.
func TestInstallCertificate_HappyPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)

	// Bootstrap a default config.json — this also creates the data directory.
	if err := SaveConfig(DefaultConfig()); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	srcCert := filepath.Join(dir, "src.crt")
	srcKey := filepath.Join(dir, "src.key")
	if err := os.WriteFile(srcCert, []byte("CERT DATA"), 0o644); err != nil {
		t.Fatalf("WriteFile cert: %v", err)
	}
	if err := os.WriteFile(srcKey, []byte("KEY DATA"), 0o600); err != nil {
		t.Fatalf("WriteFile key: %v", err)
	}

	if err := InstallCertificate(srcCert, srcKey); err != nil {
		t.Fatalf("InstallCertificate: %v", err)
	}

	// Config must have TLSCert and TLSKey populated.
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig after install: %v", err)
	}
	if cfg.Dashboard.TLSCert == "" {
		t.Error("TLSCert not set after InstallCertificate")
	}
	if cfg.Dashboard.TLSKey == "" {
		t.Error("TLSKey not set after InstallCertificate")
	}

	// Destination files must exist in the data directory.
	dataDir := DefaultDataDir()
	for _, name := range []string{`dashboard-tls.crt`, `dashboard-tls.key`} {
		if _, err := os.Stat(dataDir + `\` + name); err != nil {
			t.Errorf("expected %s in data dir: %v", name, err)
		}
	}
}

// TestInstallCertificate_MissingCert verifies that InstallCertificate returns a
// "file not found" error when the source certificate file does not exist.
func TestInstallCertificate_MissingCert(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)

	srcKey := filepath.Join(dir, "src.key")
	if err := os.WriteFile(srcKey, []byte("KEY DATA"), 0o600); err != nil {
		t.Fatalf("WriteFile key: %v", err)
	}

	err := InstallCertificate(filepath.Join(dir, "nonexistent.crt"), srcKey)
	if err == nil {
		t.Fatal("expected error for missing cert, got nil")
	}
	if !strings.Contains(err.Error(), "file not found") {
		t.Errorf("error = %q, want 'file not found' in message", err.Error())
	}
}

// TestInstallCertificate_MissingKey verifies that InstallCertificate returns a
// "file not found" error when the source key file does not exist.
func TestInstallCertificate_MissingKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)

	srcCert := filepath.Join(dir, "src.crt")
	if err := os.WriteFile(srcCert, []byte("CERT DATA"), 0o644); err != nil {
		t.Fatalf("WriteFile cert: %v", err)
	}

	err := InstallCertificate(srcCert, filepath.Join(dir, "nonexistent.key"))
	if err == nil {
		t.Fatal("expected error for missing key, got nil")
	}
	if !strings.Contains(err.Error(), "file not found") {
		t.Errorf("error = %q, want 'file not found' in message", err.Error())
	}
}

// TestInstallCertificate_WriteCertError verifies that InstallCertificate returns
// a "write cert" error when the destination cert path is blocked by a directory.
func TestInstallCertificate_WriteCertError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)

	srcCert := filepath.Join(dir, "src.crt")
	srcKey := filepath.Join(dir, "src.key")
	if err := os.WriteFile(srcCert, []byte("CERT DATA"), 0o644); err != nil {
		t.Fatalf("WriteFile cert: %v", err)
	}
	if err := os.WriteFile(srcKey, []byte("KEY DATA"), 0o600); err != nil {
		t.Fatalf("WriteFile key: %v", err)
	}

	// Create the data dir, then block the cert destination.
	dataDir := DefaultDataDir()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("MkdirAll dataDir: %v", err)
	}
	dstCert := dataDir + `\dashboard-tls.crt`
	if err := os.MkdirAll(dstCert, 0o755); err != nil {
		t.Fatalf("MkdirAll dstCert block: %v", err)
	}

	err := InstallCertificate(srcCert, srcKey)
	if err == nil {
		t.Fatal("expected error when cert destination is blocked, got nil")
	}
	if !strings.Contains(err.Error(), "write cert") {
		t.Errorf("error = %q, want 'write cert' in message", err.Error())
	}
}

// TestInstallCertificate_WriteKeyError verifies that InstallCertificate returns
// a "write key" error when the destination key path is blocked by a directory.
func TestInstallCertificate_WriteKeyError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)

	srcCert := filepath.Join(dir, "src.crt")
	srcKey := filepath.Join(dir, "src.key")
	if err := os.WriteFile(srcCert, []byte("CERT DATA"), 0o644); err != nil {
		t.Fatalf("WriteFile cert: %v", err)
	}
	if err := os.WriteFile(srcKey, []byte("KEY DATA"), 0o600); err != nil {
		t.Fatalf("WriteFile key: %v", err)
	}

	// Create the data dir, then block the key destination.
	dataDir := DefaultDataDir()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("MkdirAll dataDir: %v", err)
	}
	dstKey := dataDir + `\dashboard-tls.key`
	if err := os.MkdirAll(dstKey, 0o755); err != nil {
		t.Fatalf("MkdirAll dstKey block: %v", err)
	}

	err := InstallCertificate(srcCert, srcKey)
	if err == nil {
		t.Fatal("expected error when key destination is blocked, got nil")
	}
	if !strings.Contains(err.Error(), "write key") {
		t.Errorf("error = %q, want 'write key' in message", err.Error())
	}
}

// TestInstallCertificate_ReadCertError verifies that InstallCertificate returns
// a "read cert" error when the source cert path passes Stat but cannot be read
// — triggered by placing a directory at certPath (os.Stat on a directory
// succeeds; os.ReadFile on a directory fails).
func TestInstallCertificate_ReadCertError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)

	// Place a directory at certPath: Stat succeeds, ReadFile fails.
	srcCert := filepath.Join(dir, "src.crt")
	if err := os.MkdirAll(srcCert, 0o755); err != nil {
		t.Fatalf("MkdirAll srcCert: %v", err)
	}
	srcKey := filepath.Join(dir, "src.key")
	if err := os.WriteFile(srcKey, []byte("KEY DATA"), 0o600); err != nil {
		t.Fatalf("WriteFile key: %v", err)
	}

	err := InstallCertificate(srcCert, srcKey)
	if err == nil {
		t.Fatal("expected error when cert source is a directory, got nil")
	}
	if !strings.Contains(err.Error(), "read cert") {
		t.Errorf("error = %q, want 'read cert' in message", err.Error())
	}
}

// TestInstallCertificate_ReadKeyError verifies that InstallCertificate returns
// a "read key" error when the source key path passes Stat but cannot be read
// — triggered by placing a directory at keyPath after certPath is valid.
func TestInstallCertificate_ReadKeyError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)

	srcCert := filepath.Join(dir, "src.crt")
	if err := os.WriteFile(srcCert, []byte("CERT DATA"), 0o644); err != nil {
		t.Fatalf("WriteFile cert: %v", err)
	}
	// Place a directory at keyPath: Stat succeeds, ReadFile fails.
	srcKey := filepath.Join(dir, "src.key")
	if err := os.MkdirAll(srcKey, 0o755); err != nil {
		t.Fatalf("MkdirAll srcKey: %v", err)
	}

	// Ensure the data dir exists so WriteFile(dstCert) can proceed first.
	if err := os.MkdirAll(DefaultDataDir(), 0o755); err != nil {
		t.Fatalf("MkdirAll dataDir: %v", err)
	}

	err := InstallCertificate(srcCert, srcKey)
	if err == nil {
		t.Fatal("expected error when key source is a directory, got nil")
	}
	if !strings.Contains(err.Error(), "read key") {
		t.Errorf("error = %q, want 'read key' in message", err.Error())
	}
}

// TestInstallCertificate_LoadConfigError verifies that InstallCertificate
// returns a "load config" error when LoadConfig fails after the cert and key
// files have been written successfully. The config.json path is blocked by
// placing a directory there so os.ReadFile returns a non-IsNotExist error.
func TestInstallCertificate_LoadConfigError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)

	srcCert := filepath.Join(dir, "src.crt")
	srcKey := filepath.Join(dir, "src.key")
	if err := os.WriteFile(srcCert, []byte("CERT DATA"), 0o644); err != nil {
		t.Fatalf("WriteFile cert: %v", err)
	}
	if err := os.WriteFile(srcKey, []byte("KEY DATA"), 0o600); err != nil {
		t.Fatalf("WriteFile key: %v", err)
	}

	// Create the data dir so dstCert/dstKey writes succeed.
	if err := os.MkdirAll(DefaultDataDir(), 0o755); err != nil {
		t.Fatalf("MkdirAll dataDir: %v", err)
	}

	// Block config.json by placing a directory at that path so LoadConfig
	// returns a non-IsNotExist read error.
	configPath := DefaultConfigPath()
	if err := os.MkdirAll(configPath, 0o755); err != nil {
		t.Fatalf("MkdirAll configPath: %v", err)
	}

	err := InstallCertificate(srcCert, srcKey)
	if err == nil {
		t.Fatal("expected error when LoadConfig fails, got nil")
	}
	if !strings.Contains(err.Error(), "load config") {
		t.Errorf("error = %q, want 'load config' in message", err.Error())
	}
}

// TestLoadConfig_FreshInstall_WriteError verifies that LoadConfig returns an
// error containing "write default config" when no config.json exists and
// saveConfigToFile fails (data directory cannot be created).
func TestLoadConfig_FreshInstall_WriteError(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ProgramData", base)

	// Block MkdirAll by placing a regular file where "LISS Technologies"
	// would be — mirrors TestSaveConfig_CreateDataDirError but exercises
	// the "write default config" wrapper in LoadConfig.
	blocker := filepath.Join(base, "LISS Technologies")
	if err := os.WriteFile(blocker, []byte("blocked"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected error when saveConfigToFile fails during fresh install, got nil")
	}
	if !strings.Contains(err.Error(), "write default config") {
		t.Errorf("error = %q, want 'write default config' in message", err.Error())
	}
}

// TestSaveConfig_WriteTempError verifies that SaveConfig returns an error
// containing "write temp config" when the .tmp path is blocked by a directory.
func TestSaveConfig_WriteTempError(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ProgramData", base)

	// Create the data directory so MkdirAll succeeds.
	dataDir := DefaultDataDir()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("MkdirAll dataDir: %v", err)
	}

	// Block config.json.tmp with a directory — os.WriteFile will fail.
	tmpPath := DefaultConfigPath() + ".tmp"
	if err := os.MkdirAll(tmpPath, 0o755); err != nil {
		t.Fatalf("MkdirAll tmpPath: %v", err)
	}

	err := SaveConfig(DefaultConfig())
	if err == nil {
		t.Fatal("expected error when tmpPath is a directory, got nil")
	}
	if !strings.Contains(err.Error(), "write temp config") {
		t.Errorf("error = %q, want 'write temp config' in message", err.Error())
	}
}

// TestSaveConfig_RenameError verifies that SaveConfig returns an error
// containing "rename config" when both windows.MoveFileEx and os.Rename fail.
// This is forced by holding config.json open without FILE_SHARE_DELETE so that
// MoveFileEx cannot replace it and the os.Rename fallback also fails.
func TestSaveConfig_RenameError(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ProgramData", base)

	// Bootstrap a valid config.json so the destination file already exists.
	if err := SaveConfig(DefaultConfig()); err != nil {
		t.Fatalf("SaveConfig (setup): %v", err)
	}

	// Hold config.json open without FILE_SHARE_DELETE — prevents both
	// windows.MoveFileEx (MOVEFILE_REPLACE_EXISTING) and os.Rename from
	// replacing it, because Windows cannot delete a file whose delete access
	// is not shared.
	configPath := DefaultConfigPath()
	pathPtr, err := windows.UTF16PtrFromString(configPath)
	if err != nil {
		t.Fatalf("UTF16PtrFromString: %v", err)
	}
	h, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ, // intentionally omit FILE_SHARE_DELETE
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatalf("CreateFile exclusive: %v", err)
	}
	defer func() { _ = windows.CloseHandle(h) }()

	err = SaveConfig(DefaultConfig())
	if err == nil {
		t.Fatal("expected error from SaveConfig when rename fails, got nil")
	}
	if !strings.Contains(err.Error(), "rename config") {
		t.Errorf("error = %q, want 'rename config' in message", err.Error())
	}
}
