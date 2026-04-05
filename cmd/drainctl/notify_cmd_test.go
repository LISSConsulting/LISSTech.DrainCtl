//go:build windows

package main

import (
	"os"
	"strings"
	"testing"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// ── parseTriggers ─────────────────────────────────────────────────────────────

func TestParseTriggers_ValidList(t *testing.T) {
	triggers, err := parseTriggers("drain_on,drain_off")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(triggers) != 2 {
		t.Fatalf("len = %d, want 2", len(triggers))
	}
	if triggers[0] != dc.TriggerDrainOn {
		t.Errorf("triggers[0] = %q, want %q", triggers[0], dc.TriggerDrainOn)
	}
	if triggers[1] != dc.TriggerDrainOff {
		t.Errorf("triggers[1] = %q, want %q", triggers[1], dc.TriggerDrainOff)
	}
}

func TestParseTriggers_SingleTrigger(t *testing.T) {
	triggers, err := parseTriggers("alert")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(triggers) != 1 || triggers[0] != dc.TriggerAlert {
		t.Errorf("triggers = %v, want [alert]", triggers)
	}
}

func TestParseTriggers_TrimsWhitespace(t *testing.T) {
	triggers, err := parseTriggers(" drain_on , alert ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(triggers) != 2 {
		t.Fatalf("len = %d, want 2", len(triggers))
	}
}

// TestParseTriggers_SkipsEmptyParts verifies that adjacent commas or leading/
// trailing commas do not produce empty trigger entries.
func TestParseTriggers_SkipsEmptyParts(t *testing.T) {
	triggers, err := parseTriggers("drain_on,,drain_off")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(triggers) != 2 {
		t.Errorf("len = %d, want 2 (empty part should be skipped)", len(triggers))
	}
}

func TestParseTriggers_UnknownTrigger_ReturnsError(t *testing.T) {
	_, err := parseTriggers("drain_on,fake_event")
	if err == nil {
		t.Fatal("expected error for unknown trigger, got nil")
	}
	if !strings.Contains(err.Error(), "unknown trigger") {
		t.Errorf("error = %q, want 'unknown trigger' in message", err.Error())
	}
}

// TestParseTriggers_AllEmpty_ReturnsError covers the len(triggers)==0 guard that
// fires when the raw string contains only commas or whitespace.
func TestParseTriggers_AllEmpty_ReturnsError(t *testing.T) {
	_, err := parseTriggers(",, ,")
	if err == nil {
		t.Fatal("expected error for all-empty trigger list, got nil")
	}
	if !strings.Contains(err.Error(), "triggers list is empty") {
		t.Errorf("error = %q, want 'triggers list is empty'", err.Error())
	}
}

// ── triggerList ───────────────────────────────────────────────────────────────

// TestTriggerList_ContainsAllTriggers verifies every ValidTrigger name appears
// in the output.
func TestTriggerList_ContainsAllTriggers(t *testing.T) {
	list := triggerList()
	for tr := range dc.ValidTriggers {
		if !strings.Contains(list, string(tr)) {
			t.Errorf("triggerList() = %q does not contain trigger %q", list, tr)
		}
	}
}

// TestTriggerList_IsSorted verifies the output is lexicographically sorted so
// that help text and error messages are stable and deterministic.
func TestTriggerList_IsSorted(t *testing.T) {
	list := triggerList()
	parts := strings.Split(list, ", ")
	for i := 1; i < len(parts); i++ {
		if parts[i] < parts[i-1] {
			t.Errorf("triggerList() not sorted: %q comes before %q in %q",
				parts[i-1], parts[i], list)
		}
	}
}

// ── upsertNotifyTarget ────────────────────────────────────────────────────────

func TestUpsertNotifyTarget_UpdatesExistingURL(t *testing.T) {
	targets := []dc.NotificationTarget{
		{Type: "webhook", URL: "https://old.example.com/hook"},
	}
	result := upsertNotifyTarget(targets, "webhook", "https://new.example.com/hook")
	if len(result) != 1 {
		t.Fatalf("len = %d, want 1", len(result))
	}
	if result[0].URL != "https://new.example.com/hook" {
		t.Errorf("URL = %q, want new URL", result[0].URL)
	}
}

// TestUpsertNotifyTarget_AppendsWhenNoMatch verifies a new target is appended
// when the slice contains no target of the requested type.
func TestUpsertNotifyTarget_AppendsWhenNoMatch(t *testing.T) {
	targets := []dc.NotificationTarget{
		{Type: "ntfy", URL: "https://ntfy.sh/alerts"},
	}
	result := upsertNotifyTarget(targets, "webhook", "https://hook.example.com/")
	if len(result) != 2 {
		t.Fatalf("len = %d, want 2 (ntfy preserved + webhook appended)", len(result))
	}
	if result[1].Type != "webhook" || result[1].URL != "https://hook.example.com/" {
		t.Errorf("appended target = %+v", result[1])
	}
}

// TestUpsertNotifyTarget_PreservesOtherTypes verifies that targets of other types
// are left untouched when updating a specific type.
func TestUpsertNotifyTarget_PreservesOtherTypes(t *testing.T) {
	targets := []dc.NotificationTarget{
		{Type: "ntfy", URL: "https://ntfy.sh/alerts"},
		{Type: "webhook", URL: "https://old.example.com/"},
	}
	result := upsertNotifyTarget(targets, "webhook", "https://new.example.com/")
	if len(result) != 2 {
		t.Fatalf("len = %d, want 2", len(result))
	}
	if result[0].Type != "ntfy" || result[0].URL != "https://ntfy.sh/alerts" {
		t.Errorf("ntfy target was modified: %+v", result[0])
	}
}

// TestUpsertNotifyTarget_NewTargetHasDefaultTriggers verifies that a freshly
// appended target carries DefaultTriggers (not an empty slice) so notifications
// fire immediately without manual configuration.
func TestUpsertNotifyTarget_NewTargetHasDefaultTriggers(t *testing.T) {
	result := upsertNotifyTarget(nil, "webhook", "https://hook.example.com/")
	if len(result) != 1 {
		t.Fatalf("len = %d, want 1", len(result))
	}
	if len(result[0].Triggers) == 0 {
		t.Error("new target has empty Triggers, want DefaultTriggers")
	}
}

// ── printNotifyTargets ────────────────────────────────────────────────────────

// logCapture collects log lines for assertion.
type logCapture struct {
	lines []string
}

func (lc *logCapture) logFunc(l dc.Level, fields ...string) {
	parts := []string{string(l)}
	parts = append(parts, fields...)
	lc.lines = append(lc.lines, strings.Join(parts, " "))
}

func (lc *logCapture) contains(s string) bool {
	for _, l := range lc.lines {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

func TestPrintNotifyTargets_EmptyTargets_LogsWarning(t *testing.T) {
	var lc logCapture
	printNotifyTargets(nil, false, lc.logFunc)
	if !lc.contains("notifications=disabled") {
		t.Errorf("expected 'notifications=disabled', got %v", lc.lines)
	}
}

// TestPrintNotifyTargets_WebhookWithSecret verifies the hmac_secret=set indicator.
func TestPrintNotifyTargets_WebhookWithSecret(t *testing.T) {
	targets := []dc.NotificationTarget{
		{Type: "webhook", URL: "https://hook.example.com/", Secret: "mysecret"},
	}
	var lc logCapture
	printNotifyTargets(targets, true, lc.logFunc)
	if !lc.contains("hmac_secret=set") {
		t.Errorf("expected 'hmac_secret=set', got %v", lc.lines)
	}
}

// TestPrintNotifyTargets_WebhookWithoutSecret verifies the hmac_secret=unset indicator.
func TestPrintNotifyTargets_WebhookWithoutSecret(t *testing.T) {
	targets := []dc.NotificationTarget{
		{Type: "webhook", URL: "https://hook.example.com/"},
	}
	var lc logCapture
	printNotifyTargets(targets, true, lc.logFunc)
	if !lc.contains("hmac_secret=unset") {
		t.Errorf("expected 'hmac_secret=unset', got %v", lc.lines)
	}
}

// TestPrintNotifyTargets_NtfyNoHmac verifies ntfy targets don't show the
// hmac_secret field (it is webhook-only).
func TestPrintNotifyTargets_NtfyNoHmac(t *testing.T) {
	targets := []dc.NotificationTarget{
		{Type: "ntfy", URL: "https://ntfy.sh/alerts"},
	}
	var lc logCapture
	printNotifyTargets(targets, true, lc.logFunc)
	if lc.contains("hmac_secret") {
		t.Errorf("ntfy target should not show hmac_secret, got %v", lc.lines)
	}
}

// TestPrintNotifyTargets_DefaultTriggers verifies that an empty Triggers list is
// printed as "(default)" to avoid misleading users with an empty bracket.
func TestPrintNotifyTargets_DefaultTriggers(t *testing.T) {
	targets := []dc.NotificationTarget{
		{Type: "webhook", URL: "https://hook.example.com/", Triggers: nil},
	}
	var lc logCapture
	printNotifyTargets(targets, true, lc.logFunc)
	if !lc.contains("(default)") {
		t.Errorf("expected '(default)' for empty triggers, got %v", lc.lines)
	}
}

// TestPrintNotifyTargets_DisabledWhenNoURLs verifies the disabled message when
// hasTargets is false (all targets have empty URLs).
func TestPrintNotifyTargets_DisabledWhenNoURLs(t *testing.T) {
	targets := []dc.NotificationTarget{
		{Type: "webhook", URL: ""},
	}
	var lc logCapture
	printNotifyTargets(targets, false, lc.logFunc)
	if !lc.contains("disabled") {
		t.Errorf("expected 'disabled' when hasTargets=false, got %v", lc.lines)
	}
}

// ── setNotifyTarget ───────────────────────────────────────────────────────────

// TestSetNotifyTarget_SetsURL verifies that a non-empty URL is upserted and
// saved; the OK log line includes the URL.
func TestSetNotifyTarget_SetsURL(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	cfg := dc.DefaultConfig()
	var lc logCapture
	if err := setNotifyTarget(cfg, "webhook", "https://hook.example.com/", notifyOverrides{}, lc.logFunc); err != nil {
		t.Fatalf("setNotifyTarget: %v", err)
	}
	if !lc.contains("https://hook.example.com/") {
		t.Errorf("expected URL in log output, got %v", lc.lines)
	}
	if len(cfg.Notifications) == 0 {
		t.Fatal("expected at least one notification target in cfg")
	}
	if cfg.Notifications[0].URL != "https://hook.example.com/" {
		t.Errorf("URL = %q, want https://hook.example.com/", cfg.Notifications[0].URL)
	}
}

// TestSetNotifyTarget_AppliesSecret verifies that a non-nil Secret override is
// written to the target after upsert.
func TestSetNotifyTarget_AppliesSecret(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	cfg := dc.DefaultConfig()
	secret := "s3cr3t"
	ov := notifyOverrides{Secret: &secret}
	var lc logCapture
	if err := setNotifyTarget(cfg, "webhook", "https://hook.example.com/", ov, lc.logFunc); err != nil {
		t.Fatalf("setNotifyTarget: %v", err)
	}
	if cfg.Notifications[0].Secret != "s3cr3t" {
		t.Errorf("Secret = %q, want s3cr3t", cfg.Notifications[0].Secret)
	}
}

// TestSetNotifyTarget_AppliesTriggers verifies that a non-nil Triggers override
// replaces the target's trigger list.
func TestSetNotifyTarget_AppliesTriggers(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	cfg := dc.DefaultConfig()
	trigs := []dc.Trigger{dc.TriggerAlert}
	ov := notifyOverrides{Triggers: &trigs}
	var lc logCapture
	if err := setNotifyTarget(cfg, "webhook", "https://hook.example.com/", ov, lc.logFunc); err != nil {
		t.Fatalf("setNotifyTarget: %v", err)
	}
	if len(cfg.Notifications[0].Triggers) != 1 || cfg.Notifications[0].Triggers[0] != dc.TriggerAlert {
		t.Errorf("Triggers = %v, want [alert]", cfg.Notifications[0].Triggers)
	}
}

// TestSetNotifyTarget_AppliesRepeatMinutes verifies that a non-nil RepeatMinutes
// override is stored on the target.
func TestSetNotifyTarget_AppliesRepeatMinutes(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	cfg := dc.DefaultConfig()
	repeat := 30
	ov := notifyOverrides{RepeatMinutes: &repeat}
	var lc logCapture
	if err := setNotifyTarget(cfg, "webhook", "https://hook.example.com/", ov, lc.logFunc); err != nil {
		t.Fatalf("setNotifyTarget: %v", err)
	}
	if cfg.Notifications[0].RepeatMinutes != 30 {
		t.Errorf("RepeatMinutes = %d, want 30", cfg.Notifications[0].RepeatMinutes)
	}
}

// TestSetNotifyTarget_EmptyURL_RemovesTarget verifies that an empty URL removes
// all targets of the given type and logs a disabled message.
func TestSetNotifyTarget_EmptyURL_RemovesTarget(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	cfg := dc.DefaultConfig()
	cfg.Notifications = []dc.NotificationTarget{
		{Type: "webhook", URL: "https://old.example.com/"},
	}
	var lc logCapture
	if err := setNotifyTarget(cfg, "webhook", "", notifyOverrides{}, lc.logFunc); err != nil {
		t.Fatalf("setNotifyTarget: %v", err)
	}
	if len(cfg.Notifications) != 0 {
		t.Errorf("expected notifications to be empty, got %v", cfg.Notifications)
	}
	if !lc.contains("webhook=disabled") {
		t.Errorf("expected 'webhook=disabled' in log, got %v", lc.lines)
	}
}

// TestSetNotifyTarget_EmptyURL_PreservesOtherTypes verifies that clearing a type
// leaves other types' targets intact.
func TestSetNotifyTarget_EmptyURL_PreservesOtherTypes(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	cfg := dc.DefaultConfig()
	cfg.Notifications = []dc.NotificationTarget{
		{Type: "webhook", URL: "https://hook.example.com/"},
		{Type: "ntfy", URL: "https://ntfy.sh/alerts"},
	}
	var lc logCapture
	if err := setNotifyTarget(cfg, "webhook", "", notifyOverrides{}, lc.logFunc); err != nil {
		t.Fatalf("setNotifyTarget: %v", err)
	}
	if len(cfg.Notifications) != 1 || cfg.Notifications[0].Type != "ntfy" {
		t.Errorf("expected only ntfy target to remain, got %v", cfg.Notifications)
	}
}

// TestSetNotifyTarget_SaveError_EmptyURL_ReturnsError verifies that a SaveConfig
// failure on the empty-URL (remove) path is propagated as an error.
func TestSetNotifyTarget_SaveError_EmptyURL_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)
	if err := os.MkdirAll(dc.DefaultConfigPath()+".tmp", 0o755); err != nil {
		t.Fatalf("setup blocking dir: %v", err)
	}

	cfg := dc.DefaultConfig()
	cfg.Notifications = []dc.NotificationTarget{
		{Type: "webhook", URL: "https://hook.example.com/"},
	}
	if err := setNotifyTarget(cfg, "webhook", "", notifyOverrides{}, dc.DiscardLogger()); err == nil {
		t.Fatal("expected SaveConfig error, got nil")
	}
}

// TestSetNotifyTarget_SaveError_URL_ReturnsError verifies that a SaveConfig
// failure on the set-URL path is propagated as an error.
func TestSetNotifyTarget_SaveError_URL_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)
	if err := os.MkdirAll(dc.DefaultConfigPath()+".tmp", 0o755); err != nil {
		t.Fatalf("setup blocking dir: %v", err)
	}

	cfg := dc.DefaultConfig()
	if err := setNotifyTarget(cfg, "webhook", "https://hook.example.com/", notifyOverrides{}, dc.DiscardLogger()); err == nil {
		t.Fatal("expected SaveConfig error, got nil")
	}
}
