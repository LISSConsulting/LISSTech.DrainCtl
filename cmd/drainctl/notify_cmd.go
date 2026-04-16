//go:build windows

package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/spf13/cobra"
)

func notifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Manage notification settings",
	}

	cmd.AddCommand(notifyStatusCmd())
	cmd.AddCommand(notifySetWebhookCmd())
	cmd.AddCommand(notifySetNtfyCmd())
	cmd.AddCommand(notifySetEmailCmd())
	cmd.AddCommand(notifyRemoveCmd())
	cmd.AddCommand(notifyTestCmd())

	return cmd
}

func notifyStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current notification configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			fileCfg, err := dc.LoadConfig()
			if err != nil {
				return err
			}
			printNotifyTargets(fileCfg.Notifications, fileCfg.HasTargets())
			return nil
		},
	}
}

func notifySetWebhookCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "set-webhook [url]",
		Short: "Set webhook URL (empty to remove all webhook targets)",
		Long: `Set a webhook notification URL.

When called without optional flags, only the URL is updated and all other
settings (secret, triggers, repeat interval) are preserved.

Use --secret or --secret-env to configure HMAC-SHA256 request signing.
  --secret <value>         pass the secret directly (visible in shell history)
  --secret-env <NAME>      read the secret from the named environment variable
                           (RMM/script-friendly; secret never lands on disk)

Use --target-index N to address the Nth webhook target (default 0). An index
past the end appends a new target. Use --triggers and --repeat-minutes to
control which events fire and how often.

Pass an empty URL ("") to remove ALL webhook targets at once. To remove a
single target, use 'drainctl notify remove --type webhook --target-index N'.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fileCfg, err := dc.LoadConfig()
			if err != nil {
				return err
			}
			url := ""
			if len(args) > 0 {
				url = args[0]
			}

			ov, err := buildOverrides(cmd, true)
			if err != nil {
				return err
			}

			return setNotifyTarget(fileCfg, "webhook", url, ov)
		},
	}
	c.Flags().String("secret", "", "HMAC-SHA256 signing secret (empty to clear)")
	c.Flags().String("secret-env", "", "Name of env var to read the secret from (mutex with --secret)")
	c.Flags().String("triggers", "", fmt.Sprintf("Comma-separated events that fire this webhook (valid: %s)", triggerList()))
	c.Flags().Int("repeat-minutes", 0, fmt.Sprintf("Min minutes between repeated alert/session notifications (0=once, max %d)", dc.MaxRepeatMinutes))
	c.Flags().Int("target-index", 0, "0-based index among webhook targets (past end → append)")
	return c
}

func notifySetNtfyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "set-ntfy [url]",
		Short: "Set ntfy URL (empty to remove all ntfy targets)",
		Long: `Set an ntfy.sh notification URL.

When called without optional flags, only the URL is updated and all other
settings (triggers, repeat interval) are preserved.

Use --target-index N to address the Nth ntfy target (default 0). An index
past the end appends a new target.

Pass an empty URL ("") to remove ALL ntfy targets at once.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fileCfg, err := dc.LoadConfig()
			if err != nil {
				return err
			}
			url := ""
			if len(args) > 0 {
				url = args[0]
			}

			ov, err := buildOverrides(cmd, false)
			if err != nil {
				return err
			}

			return setNotifyTarget(fileCfg, "ntfy", url, ov)
		},
	}
	c.Flags().String("triggers", "", fmt.Sprintf("Comma-separated events that fire this notification (valid: %s)", triggerList()))
	c.Flags().Int("repeat-minutes", 0, fmt.Sprintf("Min minutes between repeated alert/session notifications (0=once, max %d)", dc.MaxRepeatMinutes))
	c.Flags().Int("target-index", 0, "0-based index among ntfy targets (past end → append)")
	return c
}

func notifySetEmailCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "set-email <smtp-url>",
		Short: "Set an SMTP email notification target",
		Long: `Configure an SMTP email notification target.

The URL must use the smtp:// or smtps:// scheme (e.g., smtp://mail.example.com:587).

Required on first creation:
  --from <addr>            sender address
  --to <addr1,addr2,...>   one or more recipient addresses

Use --secret or --secret-env to set the SMTP password:
  --secret <value>         pass the password directly (visible in shell history)
  --secret-env <NAME>      read the password from the named environment variable
                           (RMM/script-friendly; password never lands on disk)

The plaintext password is DPAPI-encrypted before being written to config.json.
Use --target-index N to address the Nth email target (default 0). An index
past the end appends a new target.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fileCfg, err := dc.LoadConfig()
			if err != nil {
				return err
			}
			url := args[0]
			if strings.TrimSpace(url) == "" {
				return errors.New("smtp-url is required")
			}

			ov, err := buildOverrides(cmd, true)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("from") {
				s, _ := cmd.Flags().GetString("from")
				ov.From = &s
			}
			if cmd.Flags().Changed("to") {
				raw, _ := cmd.Flags().GetString("to")
				to := splitCSV(raw)
				if len(to) == 0 {
					return errors.New("--to must contain at least one address")
				}
				ov.To = &to
			}

			return setNotifyTarget(fileCfg, "email", url, ov)
		},
	}
	c.Flags().String("from", "", "Sender email address")
	c.Flags().String("to", "", "Comma-separated recipient email addresses")
	c.Flags().String("secret", "", "SMTP password (DPAPI-encrypted on disk)")
	c.Flags().String("secret-env", "", "Name of env var to read the password from (mutex with --secret)")
	c.Flags().String("triggers", "", fmt.Sprintf("Comma-separated events that fire this notification (valid: %s)", triggerList()))
	c.Flags().Int("repeat-minutes", 0, fmt.Sprintf("Min minutes between repeated alert/session notifications (0=once, max %d)", dc.MaxRepeatMinutes))
	c.Flags().Int("target-index", 0, "0-based index among email targets (past end → append)")
	return c
}

func notifyRemoveCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "remove",
		Short: "Remove a single notification target by type and index",
		Long: `Remove the Nth notification target of a given type.

Use 'drainctl notify status' to see the index of each target.

Examples:
  drainctl notify remove --type webhook --target-index 1
  drainctl notify remove --type email   --target-index 0`,
		RunE: func(cmd *cobra.Command, args []string) error {
			typ, _ := cmd.Flags().GetString("type")
			idx, _ := cmd.Flags().GetInt("target-index")

			fileCfg, err := dc.LoadConfig()
			if err != nil {
				return err
			}
			if err := dc.RemoveNotifyTarget(fileCfg, typ, idx); err != nil {
				return err
			}
			if err := dc.SaveConfig(fileCfg); err != nil {
				return err
			}
			slog.Info(fmt.Sprintf("removed %s[%d]", typ, idx))
			return nil
		},
	}
	c.Flags().String("type", "", "Target type: webhook, ntfy, or email (required)")
	c.Flags().Int("target-index", 0, "0-based index among targets of the chosen type")
	_ = c.MarkFlagRequired("type")
	return c
}

func notifyTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test",
		Short: "Send a test notification to all configured backends",
		RunE: func(cmd *cobra.Command, args []string) error {
			fileCfg, err := dc.LoadConfig()
			if err != nil {
				return err
			}
			return dc.SendTestNotification(fileCfg.Notifications)
		},
	}
}

// notifyOverrides holds optional per-field overrides for a notification target.
// A nil pointer means "preserve the existing value".
type notifyOverrides struct {
	Secret        *string
	From          *string
	To            *[]string
	Triggers      *[]dc.Trigger
	RepeatMinutes *int
	TargetIndex   int
}

// buildOverrides reads --secret/--secret-env/--triggers/--repeat-minutes/
// --target-index from cmd. allowSecret controls whether --secret/--secret-env
// are consulted (ntfy doesn't accept a secret).
func buildOverrides(cmd *cobra.Command, allowSecret bool) (notifyOverrides, error) {
	var ov notifyOverrides

	if allowSecret {
		secretChanged := cmd.Flags().Changed("secret")
		secretEnvChanged := cmd.Flags().Changed("secret-env")
		if secretChanged && secretEnvChanged {
			return ov, errors.New("--secret and --secret-env are mutually exclusive")
		}
		if secretChanged {
			s, _ := cmd.Flags().GetString("secret")
			ov.Secret = &s
		} else if secretEnvChanged {
			name, _ := cmd.Flags().GetString("secret-env")
			if strings.TrimSpace(name) == "" {
				return ov, errors.New("--secret-env requires a non-empty variable name")
			}
			val, ok := os.LookupEnv(name)
			if !ok {
				return ov, fmt.Errorf("environment variable %q is not set", name)
			}
			ov.Secret = &val
		}
	}
	if cmd.Flags().Changed("triggers") {
		raw, _ := cmd.Flags().GetString("triggers")
		triggers, err := parseTriggers(raw)
		if err != nil {
			return ov, err
		}
		ov.Triggers = &triggers
	}
	if cmd.Flags().Changed("repeat-minutes") {
		m, _ := cmd.Flags().GetInt("repeat-minutes")
		if m < 0 || m > dc.MaxRepeatMinutes {
			return ov, fmt.Errorf("repeat-minutes must be 0–%d", dc.MaxRepeatMinutes)
		}
		ov.RepeatMinutes = &m
	}
	if cmd.Flags().Changed("target-index") {
		ov.TargetIndex, _ = cmd.Flags().GetInt("target-index")
	}
	return ov, nil
}

// setNotifyTarget upserts or removes notification targets of typ.
//
// Empty url removes ALL targets of typ (legacy CLI semantic preserved for
// scripts). Non-empty url funnels through dc.SetNotifyTarget so the CLI and
// the DLL share one mutation path.
func setNotifyTarget(fileCfg *dc.Config, typ, url string, ov notifyOverrides) error {
	if url == "" {
		filtered := fileCfg.Notifications[:0]
		for _, t := range fileCfg.Notifications {
			if t.Type != typ {
				filtered = append(filtered, t)
			}
		}
		fileCfg.Notifications = filtered
		if err := dc.SaveConfig(fileCfg); err != nil {
			return err
		}
		slog.Info(typ + "=disabled")
		return nil
	}

	update := dc.NotifyTargetUpdate{
		Type:          typ,
		URL:           url,
		TargetIndex:   ov.TargetIndex,
		Secret:        ov.Secret,
		From:          ov.From,
		To:            ov.To,
		Triggers:      ov.Triggers,
		RepeatMinutes: ov.RepeatMinutes,
	}
	if err := dc.SetNotifyTarget(fileCfg, update); err != nil {
		return err
	}
	if err := dc.SaveConfig(fileCfg); err != nil {
		return err
	}
	if format, _ := getFormat(dc.FormatPlain); format == dc.FormatPlain {
		dc.PrintResult(os.Stdout, fmt.Sprintf("%s_url=%q", typ, url))
	}
	return nil
}

// splitCSV splits a comma-separated string and trims whitespace, dropping empties.
func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseTriggers splits a comma-separated trigger list and validates each name.
func parseTriggers(raw string) ([]dc.Trigger, error) {
	parts := strings.Split(raw, ",")
	triggers := make([]dc.Trigger, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		tr := dc.Trigger(p)
		if !dc.ValidTriggers[tr] {
			return nil, fmt.Errorf("unknown trigger %q (valid: %s)", p, triggerList())
		}
		triggers = append(triggers, tr)
	}
	if len(triggers) == 0 {
		return nil, fmt.Errorf("triggers list is empty")
	}
	return triggers, nil
}

// triggerList returns a human-readable comma-separated list of all valid trigger names.
func triggerList() string {
	names := make([]string, 0, len(dc.ValidTriggers))
	for tr := range dc.ValidTriggers {
		names = append(names, string(tr))
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

// printNotifyTargets logs each notification target and the overall enabled/disabled
// status. Used by both "notify status" and "configure show".
func printNotifyTargets(targets []dc.NotificationTarget, hasTargets bool) {
	if len(targets) == 0 {
		slog.Warn("notifications=disabled (no targets configured)")
		return
	}
	// Per-type counters so each line shows the index a user would pass to
	// --target-index when editing or removing it.
	counts := map[string]int{}
	for i, t := range targets {
		effectiveTriggers := t.Triggers
		triggerNote := ""
		if len(effectiveTriggers) == 0 {
			effectiveTriggers = dc.DefaultTriggers
			triggerNote = " (default)"
		}
		triggers := make([]string, len(effectiveTriggers))
		for j, tr := range effectiveTriggers {
			triggers[j] = string(tr)
		}
		secretNote := ""
		switch t.Type {
		case "webhook":
			if t.Secret != "" {
				secretNote = " hmac_secret=set"
			} else {
				secretNote = " hmac_secret=unset"
			}
		case "email":
			if t.Secret != "" {
				secretNote = " smtp_password=set"
			} else {
				secretNote = " smtp_password=unset"
			}
		}
		typeIdx := counts[t.Type]
		counts[t.Type]++
		slog.Info(fmt.Sprintf("target[%d] %s[%d] type=%s url=%q triggers=[%s]%s repeat_minutes=%d%s",
			i, t.Type, typeIdx, t.Type, t.URL, strings.Join(triggers, ","), triggerNote, t.RepeatMinutes, secretNote))
	}
	if hasTargets {
		if format, _ := getFormat(dc.FormatPlain); format == dc.FormatPlain {
			dc.PrintResult(os.Stdout, "notifications=enabled")
		}
	} else {
		slog.Warn("notifications=disabled (no targets with URLs configured)")
	}
}

// upsertNotifyTarget retained as a thin shim over dc.SetNotifyTarget so existing
// callers in configure_cmd.go and tests keep working.
func upsertNotifyTarget(targets []dc.NotificationTarget, typ, url string) []dc.NotificationTarget {
	cfg := &dc.Config{Notifications: targets}
	_ = dc.SetNotifyTarget(cfg, dc.NotifyTargetUpdate{Type: typ, URL: url})
	return cfg.Notifications
}
