//go:build windows

package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/spf13/cobra"
)

func notifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Manage notification settings",
	}

	cmd.AddCommand(notifyListCmd())
	cmd.AddCommand(notifyAddWebhookCmd())
	cmd.AddCommand(notifyAddNtfyCmd())
	cmd.AddCommand(notifyAddEmailCmd())
	cmd.AddCommand(notifySetWebhookCmd())
	cmd.AddCommand(notifySetNtfyCmd())
	cmd.AddCommand(notifySetEmailCmd())
	cmd.AddCommand(notifyRemoveCmd())
	cmd.AddCommand(notifyTestCmd())

	return cmd
}

func notifyListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"status"},
		Short:   "List configured notification targets",
		Long: `List all notification targets. Use --format to control output:
  plain (default) — one INFO log line per target with per-type index
  table           — aligned columns, header row
  json            — array of target objects (secrets redacted)
  csv             — comma-separated, header row (secrets redacted)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fileCfg, err := dc.LoadConfig()
			if err != nil {
				return err
			}
			format, err := getFormat(dc.FormatPlain)
			if err != nil {
				return err
			}
			return writeNotifyList(os.Stdout, fileCfg.Notifications, format)
		},
	}
}

// ── add-X (append-only, validates everything) ───────────────────────────────

func notifyAddWebhookCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "add-webhook <url>",
		Short: "Append a new webhook notification target",
		Long: `Append a new webhook target. To update an existing target, use 'set-webhook'.

Use --secret or --secret-env to configure HMAC-SHA256 request signing:
  --secret <value>      pass directly (visible in shell history)
  --secret-env <NAME>   read from named env var (RMM-friendly)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ov, err := buildOverrides(cmd, true)
			if err != nil {
				return err
			}
			return addNotifyTarget(cmd, "webhook", args[0], ov)
		},
	}
	addCommonAddFlags(c, true)
	return c
}

func notifyAddNtfyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "add-ntfy <url>",
		Short: "Append a new ntfy.sh notification target",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ov, err := buildOverrides(cmd, false)
			if err != nil {
				return err
			}
			return addNotifyTarget(cmd, "ntfy", args[0], ov)
		},
	}
	addCommonAddFlags(c, false)
	return c
}

func notifyAddEmailCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "add-email <smtp-url>",
		Short: "Append a new SMTP email notification target",
		Long: `Append a new email target. URL must use smtp:// or smtps://.

Required: --from, --to (one or more comma-separated)
Optional: --secret or --secret-env for the SMTP password.

The plaintext password is DPAPI-encrypted before being written to config.json.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ov, err := buildOverrides(cmd, true)
			if err != nil {
				return err
			}
			if err := applyEmailFlags(cmd, &ov); err != nil {
				return err
			}
			return addNotifyTarget(cmd, "email", args[0], ov)
		},
	}
	addCommonAddFlags(c, true)
	addEmailFlags(c)
	return c
}

// ── set-X (in-place update, requires existing target at --target-index) ─────

func notifySetWebhookCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "set-webhook <url>",
		Short: "Update an existing webhook target by --target-index",
		Long: `Update an existing webhook target. Errors if no target at --target-index.
To create a new target, use 'add-webhook'.

Fields not supplied (--secret, --triggers, --repeat-minutes) are preserved.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ov, err := buildOverrides(cmd, true)
			if err != nil {
				return err
			}
			return updateNotifyTarget(cmd, "webhook", args[0], ov)
		},
	}
	addCommonSetFlags(c, true)
	return c
}

func notifySetNtfyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "set-ntfy <url>",
		Short: "Update an existing ntfy target by --target-index",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ov, err := buildOverrides(cmd, false)
			if err != nil {
				return err
			}
			return updateNotifyTarget(cmd, "ntfy", args[0], ov)
		},
	}
	addCommonSetFlags(c, false)
	return c
}

func notifySetEmailCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "set-email <smtp-url>",
		Short: "Update an existing email target by --target-index",
		Long: `Update an existing email target. URL is required; --from, --to, --secret are
preserved if not supplied. To create a new target, use 'add-email'.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ov, err := buildOverrides(cmd, true)
			if err != nil {
				return err
			}
			if err := applyEmailFlags(cmd, &ov); err != nil {
				return err
			}
			return updateNotifyTarget(cmd, "email", args[0], ov)
		},
	}
	addCommonSetFlags(c, true)
	addEmailFlags(c)
	return c
}

func notifyRemoveCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "remove",
		Short: "Remove a notification target by type and index",
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
			slog.Debug("removed notify target", "type", typ, "index", idx)
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
			results, sendErr := dc.SendTestNotification(fileCfg.Notifications)
			for _, r := range results {
				if r.OK {
					slog.Info("test=ok", "type", r.Type, "url", r.URL)
				} else {
					slog.Error("test=fail", "type", r.Type, "url", r.URL, "error", r.Error)
				}
			}
			return sendErr
		},
	}
}

// ── Flag helpers ────────────────────────────────────────────────────────────

func addCommonAddFlags(c *cobra.Command, allowSecret bool) {
	if allowSecret {
		c.Flags().String("secret", "", "Signing secret / SMTP password (DPAPI-encrypted on disk)")
		c.Flags().String("secret-env", "", "Name of env var to read the secret from (mutex with --secret)")
	}
	c.Flags().String("triggers", "", fmt.Sprintf("Comma-separated events that fire this target (valid: %s)", triggerList()))
	c.Flags().Int("repeat-minutes", 0, fmt.Sprintf("Min minutes between repeats (0=once, max %d)", dc.MaxRepeatMinutes))
}

func addCommonSetFlags(c *cobra.Command, allowSecret bool) {
	addCommonAddFlags(c, allowSecret)
	c.Flags().Int("target-index", 0, "0-based index among targets of this type (default 0 = first)")
}

func addEmailFlags(c *cobra.Command) {
	c.Flags().String("from", "", "Sender email address")
	c.Flags().String("to", "", "Comma-separated recipient email addresses")
}

func applyEmailFlags(cmd *cobra.Command, ov *notifyOverrides) error {
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
	return nil
}

// notifyOverrides collects per-flag overrides from the cobra command into the
// shared NotifyTargetUpdate fields.
type notifyOverrides struct {
	Secret        *string
	From          *string
	To            *[]string
	Triggers      *[]dc.Trigger
	RepeatMinutes *int
	TargetIndex   int
}

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

// addNotifyTarget appends a new target via the shared API.
func addNotifyTarget(_ *cobra.Command, typ, url string, ov notifyOverrides) error {
	fileCfg, err := dc.LoadConfig()
	if err != nil {
		return err
	}
	update := dc.NotifyTargetUpdate{
		Type: typ, URL: url,
		Secret: ov.Secret, From: ov.From, To: ov.To,
		Triggers: ov.Triggers, RepeatMinutes: ov.RepeatMinutes,
	}
	if err := dc.AppendNotifyTarget(fileCfg, update); err != nil {
		return err
	}
	if err := dc.SaveConfig(fileCfg); err != nil {
		return err
	}
	slog.Debug("notify target added", "type", typ, "url", url)
	return nil
}

// updateNotifyTarget updates an existing target at --target-index via the
// shared API. Errors clearly if no target exists at that index.
func updateNotifyTarget(_ *cobra.Command, typ, url string, ov notifyOverrides) error {
	fileCfg, err := dc.LoadConfig()
	if err != nil {
		return err
	}
	update := dc.NotifyTargetUpdate{
		Type: typ, URL: url, TargetIndex: ov.TargetIndex,
		Secret: ov.Secret, From: ov.From, To: ov.To,
		Triggers: ov.Triggers, RepeatMinutes: ov.RepeatMinutes,
	}
	if err := dc.SetNotifyTarget(fileCfg, update); err != nil {
		return err
	}
	if err := dc.SaveConfig(fileCfg); err != nil {
		return err
	}
	slog.Debug("notify target updated", "type", typ, "index", ov.TargetIndex, "url", url)
	return nil
}

// ── Output formatters ───────────────────────────────────────────────────────

// notifyListRow is the redacted view of a NotificationTarget for list output.
// Secrets are never serialized — only their presence as a boolean.
type notifyListRow struct {
	Index     int      `json:"index"`
	TypeIndex int      `json:"type_index"`
	Type      string   `json:"type"`
	URL       string   `json:"url"`
	Triggers  []string `json:"triggers"`
	Repeat    int      `json:"repeat_minutes"`
	HasSecret bool     `json:"has_secret"`
	From      string   `json:"from,omitempty"`
	To        []string `json:"to,omitempty"`
}

func toListRows(targets []dc.NotificationTarget) []notifyListRow {
	rows := make([]notifyListRow, 0, len(targets))
	counts := map[string]int{}
	for i, t := range targets {
		effective := t.Triggers
		if len(effective) == 0 {
			effective = dc.DefaultTriggers
		}
		trigStrs := make([]string, len(effective))
		for j, tr := range effective {
			trigStrs[j] = string(tr)
		}
		typeIdx := counts[t.Type]
		counts[t.Type]++
		rows = append(rows, notifyListRow{
			Index: i, TypeIndex: typeIdx, Type: t.Type, URL: t.URL,
			Triggers: trigStrs, Repeat: t.RepeatMinutes,
			HasSecret: t.Secret != "", From: t.From, To: append([]string{}, t.To...),
		})
	}
	return rows
}

func writeNotifyList(w *os.File, targets []dc.NotificationTarget, format dc.OutputFormat) error {
	if len(targets) == 0 {
		slog.Warn("no notification targets configured")
		return nil
	}
	rows := toListRows(targets)

	switch format {
	case dc.FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	case dc.FormatCSV:
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"index", "type_index", "type", "url", "triggers", "repeat_minutes", "has_secret", "from", "to"})
		for _, r := range rows {
			_ = cw.Write([]string{
				fmt.Sprint(r.Index), fmt.Sprint(r.TypeIndex), r.Type, r.URL,
				strings.Join(r.Triggers, "|"), fmt.Sprint(r.Repeat),
				fmt.Sprint(r.HasSecret), r.From, strings.Join(r.To, "|"),
			})
		}
		cw.Flush()
		return cw.Error()
	case dc.FormatTable:
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		if _, err := fmt.Fprintln(tw, "IDX\tTYPE[N]\tURL\tTRIGGERS\tREPEAT\tSECRET\tFROM\tTO"); err != nil {
			return err
		}
		for _, r := range rows {
			secret := "-"
			if r.HasSecret {
				secret = "set"
			}
			if _, err := fmt.Fprintf(tw, "%d\t%s[%d]\t%s\t%s\t%d\t%s\t%s\t%s\n",
				r.Index, r.Type, r.TypeIndex, r.URL,
				strings.Join(r.Triggers, ","), r.Repeat, secret,
				r.From, strings.Join(r.To, ",")); err != nil {
				return err
			}
		}
		return tw.Flush()
	default:
		// Plain — one slog.Info per row so output respects --log-level.
		for _, r := range rows {
			secret := ""
			switch r.Type {
			case "webhook":
				if r.HasSecret {
					secret = " hmac=set"
				} else {
					secret = " hmac=unset"
				}
			case "email":
				if r.HasSecret {
					secret = " smtp_password=set"
				} else {
					secret = " smtp_password=unset"
				}
			}
			slog.Info(fmt.Sprintf("%s[%d] url=%q triggers=[%s] repeat_minutes=%d%s",
				r.Type, r.TypeIndex, r.URL, strings.Join(r.Triggers, ","), r.Repeat, secret))
		}
		return nil
	}
}

// ── Helpers ─────────────────────────────────────────────────────────────────

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

func triggerList() string {
	names := make([]string, 0, len(dc.ValidTriggers))
	for tr := range dc.ValidTriggers {
		names = append(names, string(tr))
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

// printNotifyTargets is retained for configure_cmd.go which calls it directly.
// New CLI list output uses writeNotifyList.
func printNotifyTargets(targets []dc.NotificationTarget, hasTargets bool) {
	if !hasTargets {
		slog.Warn("notifications=disabled (no targets configured)")
		return
	}
	_ = writeNotifyList(os.Stdout, targets, dc.FormatPlain)
}

// upsertNotifyTarget retained as a thin wrapper for configure_cmd.go's
// existing call sites that walk the legacy Add/replace path.
func upsertNotifyTarget(targets []dc.NotificationTarget, typ, url string) []dc.NotificationTarget {
	cfg := &dc.Config{Notifications: targets}
	// Try update first (preserves existing fields), fall back to append.
	if err := dc.SetNotifyTarget(cfg, dc.NotifyTargetUpdate{Type: typ, URL: url}); err != nil {
		_ = dc.AppendNotifyTarget(cfg, dc.NotifyTargetUpdate{Type: typ, URL: url})
	}
	return cfg.Notifications
}
