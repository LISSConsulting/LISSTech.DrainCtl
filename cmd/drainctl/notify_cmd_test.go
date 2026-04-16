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

// printNotifyTargets logs via slog internally; tests verify it does not panic
// and (where possible) inspect cfg state rather than log output.

func TestPrintNotifyTargets_EmptyTargets_NoPanic(t *testing.T) {
	// Should log a "notifications=disabled" warning without panicking.
	printNotifyTargets(nil, false)
}

// TestPrintNotifyTargets_WebhookWithSecret verifies no panic for webhook with secret.
func TestPrintNotifyTargets_WebhookWithSecret(t *testing.T) {
	targets := []dc.NotificationTarget{
		{Type: "webhook", URL: "https://hook.example.com/", Secret: "mysecret"},
	}
	printNotifyTargets(targets, true)
}

// TestPrintNotifyTargets_WebhookWithoutSecret verifies no panic for webhook without secret.
func TestPrintNotifyTargets_WebhookWithoutSecret(t *testing.T) {
	targets := []dc.NotificationTarget{
		{Type: "webhook", URL: "https://hook.example.com/"},
	}
	printNotifyTargets(targets, true)
}

// TestPrintNotifyTargets_NtfyNoHmac verifies no panic for ntfy target.
func TestPrintNotifyTargets_NtfyNoHmac(t *testing.T) {
	targets := []dc.NotificationTarget{
		{Type: "ntfy", URL: "https://ntfy.sh/alerts"},
	}
	printNotifyTargets(targets, true)
}

// TestPrintNotifyTargets_DefaultTriggers verifies no panic for target with nil Triggers.
func TestPrintNotifyTargets_DefaultTriggers(t *testing.T) {
	targets := []dc.NotificationTarget{
		{Type: "webhook", URL: "https://hook.example.com/", Triggers: nil},
	}
	printNotifyTargets(targets, true)
}

// TestPrintNotifyTargets_DisabledWhenNoURLs verifies no panic when hasTargets is false.
func TestPrintNotifyTargets_DisabledWhenNoURLs(t *testing.T) {
	targets := []dc.NotificationTarget{
		{Type: "webhook", URL: ""},
	}
	printNotifyTargets(targets, false)
}

// ── setNotifyTarget ───────────────────────────────────────────────────────────

// TestSetNotifyTarget_SetsURL verifies that a non-empty URL is upserted and saved.
func TestSetNotifyTarget_SetsURL(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	cfg := dc.DefaultConfig()
	if err := setNotifyTarget(cfg, "webhook", "https://hook.example.com/", notifyOverrides{}); err != nil {
		t.Fatalf("setNotifyTarget: %v", err)
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
	if err := setNotifyTarget(cfg, "webhook", "https://hook.example.com/", ov); err != nil {
		t.Fatalf("setNotifyTarget: %v", err)
	}
	// Validate() DPAPI-encrypts the secret; verify the round-trip via DecryptSecrets.
	if !strings.HasPrefix(cfg.Notifications[0].Secret, "dpapi:") {
		t.Fatalf("Secret should be DPAPI-encrypted, got %q", cfg.Notifications[0].Secret)
	}
	cfg.DecryptSecrets()
	if cfg.Notifications[0].Secret != "s3cr3t" {
		t.Errorf("Decrypted secret = %q, want s3cr3t", cfg.Notifications[0].Secret)
	}
}

// TestSetNotifyTarget_AppliesTriggers verifies that a non-nil Triggers override
// replaces the target's trigger list.
func TestSetNotifyTarget_AppliesTriggers(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	cfg := dc.DefaultConfig()
	trigs := []dc.Trigger{dc.TriggerAlert}
	ov := notifyOverrides{Triggers: &trigs}
	if err := setNotifyTarget(cfg, "webhook", "https://hook.example.com/", ov); err != nil {
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
	if err := setNotifyTarget(cfg, "webhook", "https://hook.example.com/", ov); err != nil {
		t.Fatalf("setNotifyTarget: %v", err)
	}
	if cfg.Notifications[0].RepeatMinutes != 30 {
		t.Errorf("RepeatMinutes = %d, want 30", cfg.Notifications[0].RepeatMinutes)
	}
}

// TestSetNotifyTarget_EmptyURL_RemovesTarget verifies that an empty URL removes
// all targets of the given type.
func TestSetNotifyTarget_EmptyURL_RemovesTarget(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	cfg := dc.DefaultConfig()
	cfg.Notifications = []dc.NotificationTarget{
		{Type: "webhook", URL: "https://old.example.com/"},
	}
	if err := setNotifyTarget(cfg, "webhook", "", notifyOverrides{}); err != nil {
		t.Fatalf("setNotifyTarget: %v", err)
	}
	if len(cfg.Notifications) != 0 {
		t.Errorf("expected notifications to be empty, got %v", cfg.Notifications)
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
	if err := setNotifyTarget(cfg, "webhook", "", notifyOverrides{}); err != nil {
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
	if err := setNotifyTarget(cfg, "webhook", "", notifyOverrides{}); err == nil {
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
	if err := setNotifyTarget(cfg, "webhook", "https://hook.example.com/", notifyOverrides{}); err == nil {
		t.Fatal("expected SaveConfig error, got nil")
	}
}

// ── new CLI behaviors: target-index, secret-env, set-email, remove ───────────

// TestSetNotifyTarget_TargetIndexAppends verifies that the CLI's TargetIndex
// override flows through to the shared API and addresses the Nth target of
// the requested type.
func TestSetNotifyTarget_TargetIndexAppends(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	cfg := dc.DefaultConfig()
	cfg.Notifications = []dc.NotificationTarget{
		{Type: "webhook", URL: "https://hook1/", Triggers: dc.DefaultTriggers},
	}
	ov := notifyOverrides{TargetIndex: 1}
	if err := setNotifyTarget(cfg, "webhook", "https://hook2/", ov); err != nil {
		t.Fatalf("setNotifyTarget: %v", err)
	}
	if len(cfg.Notifications) != 2 {
		t.Fatalf("len = %d, want 2", len(cfg.Notifications))
	}
	if cfg.Notifications[1].URL != "https://hook2/" {
		t.Errorf("appended target URL = %q", cfg.Notifications[1].URL)
	}
}

// TestSetNotifyTarget_EmailFields verifies that email From/To overrides reach
// the persisted target.
func TestSetNotifyTarget_EmailFields(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	cfg := dc.DefaultConfig()
	from := "alerts@example.com"
	to := []string{"ops@example.com"}
	ov := notifyOverrides{From: &from, To: &to}
	if err := setNotifyTarget(cfg, "email", "smtp://mail.example.com:587", ov); err != nil {
		t.Fatalf("setNotifyTarget: %v", err)
	}
	got := cfg.Notifications[0]
	if got.From != "alerts@example.com" || len(got.To) != 1 || got.To[0] != "ops@example.com" {
		t.Errorf("email target = %+v", got)
	}
}

// TestBuildOverrides_SecretEnv verifies the env-var resolution path.
func TestBuildOverrides_SecretEnv(t *testing.T) {
	t.Setenv("DRAINCTL_TEST_SECRET", "from-env")

	cmd := notifySetWebhookCmd()
	cmd.SetArgs([]string{"--secret-env", "DRAINCTL_TEST_SECRET"})
	if err := cmd.ParseFlags([]string{"--secret-env", "DRAINCTL_TEST_SECRET"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	ov, err := buildOverrides(cmd, true)
	if err != nil {
		t.Fatalf("buildOverrides: %v", err)
	}
	if ov.Secret == nil || *ov.Secret != "from-env" {
		t.Errorf("Secret = %v, want pointer to %q", ov.Secret, "from-env")
	}
}

// TestBuildOverrides_SecretEnvUnset_ReturnsError verifies the helper errors when
// the named env var is missing — RMM operators get a clear failure rather than
// silently writing an empty secret.
func TestBuildOverrides_SecretEnvUnset_ReturnsError(t *testing.T) {
	t.Setenv("DRAINCTL_TEST_SECRET_MISSING", "value")
	if err := os.Unsetenv("DRAINCTL_TEST_SECRET_MISSING"); err != nil {
		t.Fatalf("os.Unsetenv: %v", err)
	}

	cmd := notifySetWebhookCmd()
	if err := cmd.ParseFlags([]string{"--secret-env", "DRAINCTL_TEST_SECRET_MISSING"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	_, err := buildOverrides(cmd, true)
	if err == nil || !strings.Contains(err.Error(), "is not set") {
		t.Errorf("err = %v, want 'is not set'", err)
	}
}

// TestBuildOverrides_SecretMutex verifies --secret and --secret-env cannot both
// be supplied — operators must pick one to avoid ambiguity about the source.
func TestBuildOverrides_SecretMutex(t *testing.T) {
	cmd := notifySetWebhookCmd()
	if err := cmd.ParseFlags([]string{"--secret", "x", "--secret-env", "Y"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	_, err := buildOverrides(cmd, true)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("err = %v, want mutex error", err)
	}
}

// TestBuildOverrides_SecretEnvEmpty verifies that --secret-env with whitespace
// errors rather than reading an empty-named env var.
func TestBuildOverrides_SecretEnvEmpty(t *testing.T) {
	cmd := notifySetWebhookCmd()
	if err := cmd.ParseFlags([]string{"--secret-env", "  "}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	_, err := buildOverrides(cmd, true)
	if err == nil || !strings.Contains(err.Error(), "non-empty variable name") {
		t.Errorf("err = %v, want non-empty-name error", err)
	}
}

// TestSetEmailCmd_RequiresArgs verifies cobra rejects 'set-email' with no URL.
func TestSetEmailCmd_RequiresArgs(t *testing.T) {
	cmd := notifySetEmailCmd()
	cmd.SetArgs([]string{})
	if err := cmd.Args(cmd, []string{}); err == nil {
		t.Error("expected error for missing smtp-url")
	}
}

// TestNotifyRemove_RemovesByIndex verifies the remove subcommand surgically
// drops one target while leaving siblings intact.
func TestNotifyRemove_RemovesByIndex(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	cfg := dc.DefaultConfig()
	cfg.Notifications = []dc.NotificationTarget{
		{Type: "webhook", URL: "https://hook1/", Triggers: dc.DefaultTriggers},
		{Type: "webhook", URL: "https://hook2/", Triggers: dc.DefaultTriggers},
	}
	if err := dc.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	cmd := notifyRemoveCmd()
	if err := cmd.ParseFlags([]string{"--type", "webhook", "--target-index", "0"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	reloaded, err := dc.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(reloaded.Notifications) != 1 || reloaded.Notifications[0].URL != "https://hook2/" {
		t.Errorf("after remove: %+v", reloaded.Notifications)
	}
}
