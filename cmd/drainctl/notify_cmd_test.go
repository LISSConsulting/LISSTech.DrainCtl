//go:build windows

package main

import (
	"bytes"
	"encoding/json"
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
	if triggers[0] != dc.TriggerDrainOn || triggers[1] != dc.TriggerDrainOff {
		t.Errorf("triggers = %v", triggers)
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

func TestParseTriggers_SkipsEmptyParts(t *testing.T) {
	triggers, err := parseTriggers("drain_on,,drain_off")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(triggers) != 2 {
		t.Errorf("len = %d, want 2", len(triggers))
	}
}

func TestParseTriggers_UnknownTrigger_ReturnsError(t *testing.T) {
	_, err := parseTriggers("drain_on,fake_event")
	if err == nil || !strings.Contains(err.Error(), "unknown trigger") {
		t.Errorf("err = %v, want unknown-trigger error", err)
	}
}

func TestParseTriggers_AllEmpty_ReturnsError(t *testing.T) {
	_, err := parseTriggers(",, ,")
	if err == nil || !strings.Contains(err.Error(), "triggers list is empty") {
		t.Errorf("err = %v", err)
	}
}

func TestTriggerList_ContainsAllTriggers(t *testing.T) {
	list := triggerList()
	for tr := range dc.ValidTriggers {
		if !strings.Contains(list, string(tr)) {
			t.Errorf("triggerList() = %q does not contain %q", list, tr)
		}
	}
}

// ── splitCSV ──────────────────────────────────────────────────────────────────

func TestSplitCSV_TrimsAndDropsEmpty(t *testing.T) {
	got := splitCSV(" a@b , , c@d  ,")
	if len(got) != 2 || got[0] != "a@b" || got[1] != "c@d" {
		t.Errorf("got %v, want [a@b c@d]", got)
	}
}

// ── buildOverrides — secret resolution ───────────────────────────────────────

func TestBuildOverrides_SecretEnv(t *testing.T) {
	t.Setenv("DRAINCTL_TEST_SECRET", "from-env")
	cmd := notifyAddWebhookCmd()
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

func TestBuildOverrides_SecretEnvUnset_ReturnsError(t *testing.T) {
	t.Setenv("DRAINCTL_TEST_MISSING", "x")
	if err := os.Unsetenv("DRAINCTL_TEST_MISSING"); err != nil {
		t.Fatalf("Unsetenv: %v", err)
	}
	cmd := notifyAddWebhookCmd()
	if err := cmd.ParseFlags([]string{"--secret-env", "DRAINCTL_TEST_MISSING"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	_, err := buildOverrides(cmd, true)
	if err == nil || !strings.Contains(err.Error(), "is not set") {
		t.Errorf("err = %v", err)
	}
}

func TestBuildOverrides_SecretMutex(t *testing.T) {
	cmd := notifyAddWebhookCmd()
	if err := cmd.ParseFlags([]string{"--secret", "x", "--secret-env", "Y"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	_, err := buildOverrides(cmd, true)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("err = %v", err)
	}
}

func TestBuildOverrides_SecretEnvEmpty(t *testing.T) {
	cmd := notifyAddWebhookCmd()
	if err := cmd.ParseFlags([]string{"--secret-env", "  "}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	_, err := buildOverrides(cmd, true)
	if err == nil || !strings.Contains(err.Error(), "non-empty variable name") {
		t.Errorf("err = %v", err)
	}
}

// ── add-X / set-X end-to-end via cobra commands ──────────────────────────────

func TestAddWebhook_PersistsAndDPAPI(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	cmd := notifyAddWebhookCmd()
	if err := cmd.ParseFlags([]string{"--secret", "hunter2"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if err := cmd.RunE(cmd, []string{"https://hook.example.com/"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	// Read the file directly — LoadConfig auto-decrypts, so we read raw to
	// confirm what actually landed on disk.
	raw, err := os.ReadFile(dc.DefaultConfigPath())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(raw), `"dpapi:`) {
		t.Errorf("on-disk config does not contain a DPAPI prefix:\n%s", string(raw))
	}
	if strings.Contains(string(raw), `"hunter2"`) {
		t.Errorf("on-disk config leaks plaintext secret:\n%s", string(raw))
	}

	// And confirm the round-trip decrypts back to plaintext.
	cfg, err := dc.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Notifications[0].Secret != "hunter2" {
		t.Errorf("after LoadConfig+Decrypt, secret = %q, want hunter2", cfg.Notifications[0].Secret)
	}
}

func TestAddEmail_RejectsMissingFrom(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	cmd := notifyAddEmailCmd()
	if err := cmd.ParseFlags([]string{"--to", "ops@x.com"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	err := cmd.RunE(cmd, []string{"smtp://mail:587"})
	if err == nil || !strings.Contains(err.Error(), "from") {
		t.Errorf("err = %v, want missing-from error", err)
	}
}

func TestAddEmail_RejectsBadScheme(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	cmd := notifyAddEmailCmd()
	if err := cmd.ParseFlags([]string{"--from", "a@b", "--to", "c@d"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	err := cmd.RunE(cmd, []string{"https://mail/"})
	if err == nil || !strings.Contains(err.Error(), "smtp") {
		t.Errorf("err = %v, want bad-scheme error", err)
	}
}

func TestSetWebhook_RejectsWhenNoTarget(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	cmd := notifySetWebhookCmd()
	err := cmd.RunE(cmd, []string{"https://hook/"})
	if err == nil || !strings.Contains(err.Error(), "no webhook target") {
		t.Errorf("err = %v, want 'no webhook target' error", err)
	}
}

func TestSetEmail_PreservesFromToOnUrlOnlyUpdate(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	// Seed: add an email target via the add command.
	addCmd := notifyAddEmailCmd()
	if err := addCmd.ParseFlags([]string{"--from", "alerts@x.com", "--to", "ops@x.com", "--secret", "pw"}); err != nil {
		t.Fatalf("ParseFlags add: %v", err)
	}
	if err := addCmd.RunE(addCmd, []string{"smtp://old:587"}); err != nil {
		t.Fatalf("add RunE: %v", err)
	}

	// Update URL only.
	setCmd := notifySetEmailCmd()
	if err := setCmd.RunE(setCmd, []string{"smtp://new:587"}); err != nil {
		t.Fatalf("set RunE: %v", err)
	}

	cfg, err := dc.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	got := cfg.Notifications[0]
	if got.URL != "smtp://new:587" {
		t.Errorf("URL = %q, want updated", got.URL)
	}
	if got.From != "alerts@x.com" || len(got.To) != 1 || got.To[0] != "ops@x.com" {
		t.Errorf("From/To not preserved: from=%q to=%v", got.From, got.To)
	}
}

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

// ── notify list --format ─────────────────────────────────────────────────────

func TestNotifyListJSON_RedactsSecrets(t *testing.T) {
	targets := []dc.NotificationTarget{
		{Type: "webhook", URL: "https://hook/", Secret: "dpapi:abc123", Triggers: dc.DefaultTriggers},
	}
	var buf bytes.Buffer
	rows := toListRows(targets)
	if err := json.NewEncoder(&buf).Encode(rows); err != nil {
		t.Fatalf("encode: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "abc123") || strings.Contains(out, "dpapi:") {
		t.Errorf("JSON output leaks secret: %s", out)
	}
	if !strings.Contains(out, `"has_secret":true`) {
		t.Errorf("JSON output missing has_secret flag: %s", out)
	}
}

// ── upsertNotifyTarget back-compat for configure_cmd ─────────────────────────

func TestUpsertNotifyTarget_UpdatesExistingURL(t *testing.T) {
	targets := []dc.NotificationTarget{
		{Type: "webhook", URL: "https://old/"},
	}
	result := upsertNotifyTarget(targets, "webhook", "https://new/")
	if len(result) != 1 || result[0].URL != "https://new/" {
		t.Errorf("got %+v, want single updated target", result)
	}
}

func TestUpsertNotifyTarget_AppendsWhenNoMatch(t *testing.T) {
	targets := []dc.NotificationTarget{
		{Type: "ntfy", URL: "https://ntfy.sh/a"},
	}
	result := upsertNotifyTarget(targets, "webhook", "https://hook/")
	if len(result) != 2 || result[1].Type != "webhook" {
		t.Errorf("got %+v, want ntfy + appended webhook", result)
	}
}
