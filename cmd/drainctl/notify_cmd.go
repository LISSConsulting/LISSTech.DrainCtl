//go:build windows

package main

import (
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
		Short: "Set webhook URL (empty to disable)",
		Long: `Set the webhook notification URL.

When called without optional flags, only the URL is updated and all other
settings (secret, triggers, repeat interval) are preserved.

Use --secret to configure HMAC-SHA256 request signing.
Use --triggers to filter which events fire this webhook.
Use --repeat-minutes to control how often repeated events notify (0=once).`,
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

			var ov notifyOverrides
			if cmd.Flags().Changed("secret") {
				s, _ := cmd.Flags().GetString("secret")
				ov.Secret = &s
			}
			if cmd.Flags().Changed("triggers") {
				raw, _ := cmd.Flags().GetString("triggers")
				triggers, err := parseTriggers(raw)
				if err != nil {
					return err
				}
				ov.Triggers = &triggers
			}
			if cmd.Flags().Changed("repeat-minutes") {
				m, _ := cmd.Flags().GetInt("repeat-minutes")
				if m < 0 || m > dc.MaxRepeatMinutes {
					return fmt.Errorf("repeat-minutes must be 0–%d", dc.MaxRepeatMinutes)
				}
				ov.RepeatMinutes = &m
			}

			return setNotifyTarget(fileCfg, "webhook", url, ov)
		},
	}
	c.Flags().String("secret", "", "HMAC-SHA256 signing secret (empty to clear)")
	c.Flags().String("triggers", "", fmt.Sprintf("Comma-separated events that fire this webhook (valid: %s)", triggerList()))
	c.Flags().Int("repeat-minutes", 0, fmt.Sprintf("Min minutes between repeated alert/session notifications (0=once, max %d)", dc.MaxRepeatMinutes))
	return c
}

func notifySetNtfyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "set-ntfy [url]",
		Short: "Set ntfy URL (empty to disable)",
		Long: `Set the ntfy.sh notification URL.

When called without optional flags, only the URL is updated and all other
settings (triggers, repeat interval) are preserved.

Use --triggers to filter which events fire this notification.
Use --repeat-minutes to control how often repeated events notify (0=once).`,
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

			var ov notifyOverrides
			if cmd.Flags().Changed("triggers") {
				raw, _ := cmd.Flags().GetString("triggers")
				triggers, err := parseTriggers(raw)
				if err != nil {
					return err
				}
				ov.Triggers = &triggers
			}
			if cmd.Flags().Changed("repeat-minutes") {
				m, _ := cmd.Flags().GetInt("repeat-minutes")
				if m < 0 || m > dc.MaxRepeatMinutes {
					return fmt.Errorf("repeat-minutes must be 0–%d", dc.MaxRepeatMinutes)
				}
				ov.RepeatMinutes = &m
			}

			return setNotifyTarget(fileCfg, "ntfy", url, ov)
		},
	}
	c.Flags().String("triggers", "", fmt.Sprintf("Comma-separated events that fire this notification (valid: %s)", triggerList()))
	c.Flags().Int("repeat-minutes", 0, fmt.Sprintf("Min minutes between repeated alert/session notifications (0=once, max %d)", dc.MaxRepeatMinutes))
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
	Triggers      *[]dc.Trigger
	RepeatMinutes *int
}

// setNotifyTarget sets or clears the first notification target of typ.
// If url is empty, all targets of typ are removed. Otherwise the first
// existing target of typ is updated (URL + any non-nil overrides), or a new
// one is appended when none exists.
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

	fileCfg.Notifications = upsertNotifyTarget(fileCfg.Notifications, typ, url)

	// Apply per-field overrides to the target we just upserted/updated.
	for i := range fileCfg.Notifications {
		if fileCfg.Notifications[i].Type == typ {
			if ov.Secret != nil {
				fileCfg.Notifications[i].Secret = *ov.Secret
			}
			if ov.Triggers != nil {
				fileCfg.Notifications[i].Triggers = *ov.Triggers
			}
			if ov.RepeatMinutes != nil {
				fileCfg.Notifications[i].RepeatMinutes = *ov.RepeatMinutes
			}
			break
		}
	}

	if err := dc.SaveConfig(fileCfg); err != nil {
		return err
	}
	if format, _ := getFormat(dc.FormatPlain); format == dc.FormatPlain {
		dc.PrintResult(os.Stdout, fmt.Sprintf("%s_url=%q", typ, url))
	}
	return nil
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
		hmacNote := ""
		if t.Type == "webhook" {
			if t.Secret != "" {
				hmacNote = " hmac_secret=set"
			} else {
				hmacNote = " hmac_secret=unset"
			}
		}
		slog.Info(fmt.Sprintf("target[%d] type=%s url=%q triggers=[%s]%s repeat_minutes=%d%s",
			i, t.Type, t.URL, strings.Join(triggers, ","), triggerNote, t.RepeatMinutes, hmacNote))
	}
	if hasTargets {
		if format, _ := getFormat(dc.FormatPlain); format == dc.FormatPlain {
			dc.PrintResult(os.Stdout, "notifications=enabled")
		}
	} else {
		slog.Warn("notifications=disabled (no targets with URLs configured)")
	}
}

// upsertNotifyTarget updates the URL of the first target of typ, or appends a
// new target when none exists. Used by both setNotifyTarget and runConfigureFlags.
func upsertNotifyTarget(targets []dc.NotificationTarget, typ, url string) []dc.NotificationTarget {
	for i := range targets {
		if targets[i].Type == typ {
			targets[i].URL = url
			return targets
		}
	}
	return append(targets, dc.NotificationTarget{
		Type:     typ,
		URL:      url,
		Triggers: dc.DefaultTriggers,
	})
}
