//go:build windows

package drainctl

import (
	"strings"
	"testing"
)

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

// TestAppendNotifyTarget_AppendsWebhook verifies the basic append path used
// by the new CLI 'add-webhook' command.
func TestAppendNotifyTarget_AppendsWebhook(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = nil

	err := AppendNotifyTarget(cfg, NotifyTargetUpdate{
		Type: "webhook",
		URL:  "https://hook.example.com/",
	})
	if err != nil {
		t.Fatalf("AppendNotifyTarget: %v", err)
	}
	if len(cfg.Notifications) != 1 {
		t.Fatalf("len = %d, want 1", len(cfg.Notifications))
	}
	if cfg.Notifications[0].Type != "webhook" || cfg.Notifications[0].URL != "https://hook.example.com/" {
		t.Errorf("target = %+v", cfg.Notifications[0])
	}
	if len(cfg.Notifications[0].Triggers) == 0 {
		t.Error("new target should have DefaultTriggers")
	}
}

// TestAppendNotifyTarget_EmailRequiresFrom verifies the upfront email gate so
// 'add-email' cannot create a half-configured target that SaveConfig would
// silently strip.
func TestAppendNotifyTarget_EmailRequiresFrom(t *testing.T) {
	cfg := DefaultConfig()
	to := []string{"ops@example.com"}
	err := AppendNotifyTarget(cfg, NotifyTargetUpdate{
		Type: "email", URL: "smtp://mail/:587", To: &to,
	})
	if err == nil || !strings.Contains(err.Error(), "from") {
		t.Errorf("err = %v, want missing-from error", err)
	}
	if len(cfg.Notifications) != 0 {
		t.Errorf("cfg should be unchanged on validation failure")
	}
}

// TestSetNotifyTarget_ErrorsWhenNoTargetAtIndex verifies that 'set-X' is
// strict — it will not silently create a target when the operator picked the
// wrong index. Forces them to use 'add-X' explicitly.
func TestSetNotifyTarget_ErrorsWhenNoTargetAtIndex(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = nil

	err := SetNotifyTarget(cfg, NotifyTargetUpdate{
		Type: "webhook", URL: "https://hook/",
	})
	if err == nil || !strings.Contains(err.Error(), "no webhook target") {
		t.Errorf("err = %v, want 'no webhook target' error", err)
	}
}

func TestSetNotifyTarget_ErrorsWhenIndexPastEnd(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = []NotificationTarget{
		{Type: "webhook", URL: "https://hook1/"},
	}
	err := SetNotifyTarget(cfg, NotifyTargetUpdate{
		Type: "webhook", URL: "https://hook2/", TargetIndex: 5,
	})
	if err == nil || !strings.Contains(err.Error(), "no webhook target at index 5") {
		t.Errorf("err = %v", err)
	}
	if len(cfg.Notifications) != 1 {
		t.Errorf("cfg should be unchanged, got %d targets", len(cfg.Notifications))
	}
}

// TestSetNotifyTarget_UpdatesExisting verifies that index 0 with one matching
// target updates that target's URL in place.
func TestSetNotifyTarget_UpdatesExisting(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = []NotificationTarget{
		{Type: "webhook", URL: "https://old.example.com/", Secret: "keep-me"},
	}

	err := SetNotifyTarget(cfg, NotifyTargetUpdate{
		Type: "webhook",
		URL:  "https://new.example.com/",
	})
	if err != nil {
		t.Fatalf("SetNotifyTarget: %v", err)
	}
	if len(cfg.Notifications) != 1 {
		t.Fatalf("len = %d, want 1 (should update, not append)", len(cfg.Notifications))
	}
	if cfg.Notifications[0].URL != "https://new.example.com/" {
		t.Errorf("URL = %q, want updated", cfg.Notifications[0].URL)
	}
	if cfg.Notifications[0].Secret != "keep-me" {
		t.Errorf("Secret was clobbered, got %q", cfg.Notifications[0].Secret)
	}
}

// TestSetNotifyTarget_TargetIndexAddressesNthOfType verifies that target_index
// counts within the matching type only — not across all targets.
func TestSetNotifyTarget_TargetIndexAddressesNthOfType(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = []NotificationTarget{
		{Type: "ntfy", URL: "https://ntfy.sh/a"},
		{Type: "webhook", URL: "https://hook1.example.com/"},
		{Type: "ntfy", URL: "https://ntfy.sh/b"},
		{Type: "webhook", URL: "https://hook2.example.com/"},
	}

	err := SetNotifyTarget(cfg, NotifyTargetUpdate{
		Type:        "webhook",
		URL:         "https://hook2-updated.example.com/",
		TargetIndex: 1,
	})
	if err != nil {
		t.Fatalf("SetNotifyTarget: %v", err)
	}
	if cfg.Notifications[3].URL != "https://hook2-updated.example.com/" {
		t.Errorf("webhook[1] (slice idx 3) URL = %q", cfg.Notifications[3].URL)
	}
	if cfg.Notifications[1].URL != "https://hook1.example.com/" {
		t.Errorf("webhook[0] was modified: %q", cfg.Notifications[1].URL)
	}
	if cfg.Notifications[0].URL != "https://ntfy.sh/a" || cfg.Notifications[2].URL != "https://ntfy.sh/b" {
		t.Errorf("ntfy targets modified: %+v %+v", cfg.Notifications[0], cfg.Notifications[2])
	}
}

// (past-end-appends behavior removed — see TestSetNotifyTarget_ErrorsWhenIndexPastEnd)

// TestSetNotifyTarget_NilFieldsPreserve verifies that nil pointers leave existing
// values untouched while only the URL changes.
func TestSetNotifyTarget_NilFieldsPreserve(t *testing.T) {
	cfg := DefaultConfig()
	repeat := 15
	cfg.Notifications = []NotificationTarget{
		{
			Type:          "webhook",
			URL:           "https://hook.example.com/",
			Secret:        "existing-secret",
			Triggers:      []Trigger{TriggerAlert},
			RepeatMinutes: repeat,
		},
	}

	err := SetNotifyTarget(cfg, NotifyTargetUpdate{
		Type: "webhook",
		URL:  "https://hook-new.example.com/",
	})
	if err != nil {
		t.Fatalf("SetNotifyTarget: %v", err)
	}
	got := cfg.Notifications[0]
	if got.Secret != "existing-secret" {
		t.Errorf("Secret = %q, want preserved", got.Secret)
	}
	if len(got.Triggers) != 1 || got.Triggers[0] != TriggerAlert {
		t.Errorf("Triggers = %v, want preserved [alert]", got.Triggers)
	}
	if got.RepeatMinutes != 15 {
		t.Errorf("RepeatMinutes = %d, want 15", got.RepeatMinutes)
	}
}

// TestSetNotifyTarget_OverwritesProvidedFields verifies that non-nil pointer
// fields replace the existing values.
func TestSetNotifyTarget_OverwritesProvidedFields(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = []NotificationTarget{
		{Type: "webhook", URL: "https://hook.example.com/", Secret: "old"},
	}

	newTrigs := []Trigger{TriggerDrainOn}
	err := SetNotifyTarget(cfg, NotifyTargetUpdate{
		Type:          "webhook",
		URL:           "https://hook.example.com/",
		Secret:        strPtr("new"),
		Triggers:      &newTrigs,
		RepeatMinutes: intPtr(45),
	})
	if err != nil {
		t.Fatalf("SetNotifyTarget: %v", err)
	}
	got := cfg.Notifications[0]
	if got.Secret != "new" {
		t.Errorf("Secret = %q, want 'new'", got.Secret)
	}
	if len(got.Triggers) != 1 || got.Triggers[0] != TriggerDrainOn {
		t.Errorf("Triggers = %v, want [drain_on]", got.Triggers)
	}
	if got.RepeatMinutes != 45 {
		t.Errorf("RepeatMinutes = %d, want 45", got.RepeatMinutes)
	}
}

// TestAppendNotifyTarget_EmailFields verifies that email-specific From/To fields
// flow through to the appended target.
func TestAppendNotifyTarget_EmailFields(t *testing.T) {
	cfg := DefaultConfig()
	to := []string{"ops@example.com", "oncall@example.com"}

	err := AppendNotifyTarget(cfg, NotifyTargetUpdate{
		Type:   "email",
		URL:    "smtp://mail.example.com:587",
		From:   strPtr("alerts@example.com"),
		To:     &to,
		Secret: strPtr("smtppass"),
	})
	if err != nil {
		t.Fatalf("AppendNotifyTarget: %v", err)
	}
	got := cfg.Notifications[len(cfg.Notifications)-1]
	if got.From != "alerts@example.com" {
		t.Errorf("From = %q", got.From)
	}
	if len(got.To) != 2 || got.To[0] != "ops@example.com" || got.To[1] != "oncall@example.com" {
		t.Errorf("To = %v", got.To)
	}
	if got.Secret != "smtppass" {
		t.Errorf("Secret = %q (note: pre-Save, plaintext expected)", got.Secret)
	}
}

// TestAppendNotifyTarget_DPAPIRoundTrip verifies that a plaintext secret set
// via the shared API gets DPAPI-encrypted on SaveConfig and decrypts back to
// the original value.
func TestAppendNotifyTarget_DPAPIRoundTrip(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	cfg := DefaultConfig()

	err := AppendNotifyTarget(cfg, NotifyTargetUpdate{
		Type:   "webhook",
		URL:    "https://hook.example.com/",
		Secret: strPtr("hunter2"),
	})
	if err != nil {
		t.Fatalf("SetNotifyTarget: %v", err)
	}
	if err := SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if !strings.HasPrefix(cfg.Notifications[0].Secret, dpapiPrefix) {
		t.Fatalf("Secret should be DPAPI-encrypted after Save, got %q", cfg.Notifications[0].Secret)
	}
	cfg.DecryptSecrets()
	if cfg.Notifications[0].Secret != "hunter2" {
		t.Errorf("decrypted secret = %q, want hunter2", cfg.Notifications[0].Secret)
	}
}

func TestSetNotifyTarget_InvalidType(t *testing.T) {
	cfg := DefaultConfig()
	err := SetNotifyTarget(cfg, NotifyTargetUpdate{Type: "smoke-signal", URL: "x"})
	if err == nil || !strings.Contains(err.Error(), "invalid type") {
		t.Errorf("err = %v, want invalid-type error", err)
	}
}

func TestSetNotifyTarget_EmptyURL(t *testing.T) {
	cfg := DefaultConfig()
	err := SetNotifyTarget(cfg, NotifyTargetUpdate{Type: "webhook", URL: "  "})
	if err == nil || !strings.Contains(err.Error(), "url is required") {
		t.Errorf("err = %v, want url-required error", err)
	}
}

func TestSetNotifyTarget_NegativeIndex(t *testing.T) {
	cfg := DefaultConfig()
	err := SetNotifyTarget(cfg, NotifyTargetUpdate{
		Type: "webhook", URL: "https://x/", TargetIndex: -1,
	})
	if err == nil || !strings.Contains(err.Error(), "target_index") {
		t.Errorf("err = %v, want target_index error", err)
	}
}

func TestSetNotifyTarget_NilCfg(t *testing.T) {
	if err := SetNotifyTarget(nil, NotifyTargetUpdate{Type: "webhook", URL: "x"}); err == nil {
		t.Error("expected error for nil cfg")
	}
}

// ── email validation ─────────────────────────────────────────────────────────

// TestAppendNotifyTarget_EmailMissingTo verifies that the upfront email gate
// rejects --to omission on AppendNotifyTarget too.
func TestAppendNotifyTarget_EmailMissingTo(t *testing.T) {
	cfg := DefaultConfig()
	from := "alerts@example.com"
	err := AppendNotifyTarget(cfg, NotifyTargetUpdate{
		Type: "email", URL: "smtp://mail/:587", From: &from,
	})
	if err == nil || !strings.Contains(err.Error(), "to") {
		t.Errorf("err = %v, want missing-to error", err)
	}
	if len(cfg.Notifications) != 0 {
		t.Errorf("cfg should be unchanged")
	}
}

func TestAppendNotifyTarget_EmailBadScheme(t *testing.T) {
	cfg := DefaultConfig()
	from := "alerts@example.com"
	to := []string{"ops@example.com"}
	err := AppendNotifyTarget(cfg, NotifyTargetUpdate{
		Type: "email", URL: "https://mail/", From: &from, To: &to,
	})
	if err == nil || !strings.Contains(err.Error(), "smtp") {
		t.Errorf("err = %v, want bad-scheme error", err)
	}
	if len(cfg.Notifications) != 0 {
		t.Errorf("cfg should be unchanged")
	}
}

// TestSetNotifyTarget_EmailUpdatePreservesExisting verifies that updating an
// already-valid email target without re-supplying From/To succeeds (the
// preserved values satisfy validation).
func TestSetNotifyTarget_EmailUpdatePreservesExisting(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = []NotificationTarget{
		{
			Type: "email", URL: "smtp://old.example.com:587",
			From: "alerts@example.com", To: []string{"ops@example.com"},
			Triggers: DefaultTriggers,
		},
	}
	err := SetNotifyTarget(cfg, NotifyTargetUpdate{
		Type: "email",
		URL:  "smtp://new.example.com:587",
	})
	if err != nil {
		t.Fatalf("SetNotifyTarget: %v", err)
	}
	if cfg.Notifications[0].URL != "smtp://new.example.com:587" {
		t.Errorf("URL = %q, want updated", cfg.Notifications[0].URL)
	}
	if cfg.Notifications[0].From != "alerts@example.com" {
		t.Errorf("From was lost: %q", cfg.Notifications[0].From)
	}
}

// ── RemoveNotifyTarget ────────────────────────────────────────────────────────

func TestRemoveNotifyTarget_RemovesByIndex(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = []NotificationTarget{
		{Type: "webhook", URL: "https://a/"},
		{Type: "webhook", URL: "https://b/"},
		{Type: "ntfy", URL: "https://ntfy.sh/c"},
	}

	if err := RemoveNotifyTarget(cfg, "webhook", 0); err != nil {
		t.Fatalf("RemoveNotifyTarget: %v", err)
	}
	if len(cfg.Notifications) != 2 {
		t.Fatalf("len = %d, want 2", len(cfg.Notifications))
	}
	if cfg.Notifications[0].URL != "https://b/" || cfg.Notifications[1].URL != "https://ntfy.sh/c" {
		t.Errorf("remaining = %+v", cfg.Notifications)
	}
}

func TestRemoveNotifyTarget_OutOfRange(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = []NotificationTarget{{Type: "webhook", URL: "https://a/"}}

	err := RemoveNotifyTarget(cfg, "webhook", 5)
	if err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Errorf("err = %v, want out-of-range error", err)
	}
}

func TestRemoveNotifyTarget_InvalidType(t *testing.T) {
	cfg := DefaultConfig()
	if err := RemoveNotifyTarget(cfg, "carrier-pigeon", 0); err == nil {
		t.Error("expected error for invalid type")
	}
}

func TestRemoveNotifyTarget_NegativeIndex(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Notifications = []NotificationTarget{{Type: "webhook", URL: "https://a/"}}
	if err := RemoveNotifyTarget(cfg, "webhook", -1); err == nil {
		t.Error("expected error for negative index")
	}
}

func TestRemoveNotifyTarget_NilCfg(t *testing.T) {
	if err := RemoveNotifyTarget(nil, "webhook", 0); err == nil {
		t.Error("expected error for nil cfg")
	}
}
