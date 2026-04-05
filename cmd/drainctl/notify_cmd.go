//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/spf13/cobra"
)

func notifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Manage notification settings",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show current notification configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			log := dc.DefaultLogger(os.Stdout, cfg.Quiet)
			fileCfg, err := dc.LoadConfig(log)
			if err != nil {
				return err
			}
			if len(fileCfg.Notifications) == 0 {
				log(dc.LvlWRN, "notifications=disabled (no targets configured)")
				return nil
			}
			for i, t := range fileCfg.Notifications {
				triggers := make([]string, len(t.Triggers))
				for j, tr := range t.Triggers {
					triggers[j] = string(tr)
				}
				log(dc.LvlINF, fmt.Sprintf("target[%d] type=%s url=%q triggers=[%s] repeat_minutes=%d",
					i, t.Type, t.URL, strings.Join(triggers, ","), t.RepeatMinutes))
			}
			if fileCfg.HasTargets() {
				log(dc.LvlOK, "notifications=enabled")
			} else {
				log(dc.LvlWRN, "notifications=disabled (no targets with URLs configured)")
			}
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "set-webhook [url]",
		Short: "Set webhook URL (empty to disable)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			log := dc.DefaultLogger(os.Stdout, cfg.Quiet)
			fileCfg, err := dc.LoadConfig(log)
			if err != nil {
				return err
			}
			url := ""
			if len(args) > 0 {
				url = args[0]
			}
			return setNotifyTarget(fileCfg, "webhook", url, log)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "set-ntfy [url]",
		Short: "Set ntfy URL (empty to disable)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			log := dc.DefaultLogger(os.Stdout, cfg.Quiet)
			fileCfg, err := dc.LoadConfig(log)
			if err != nil {
				return err
			}
			url := ""
			if len(args) > 0 {
				url = args[0]
			}
			return setNotifyTarget(fileCfg, "ntfy", url, log)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "test",
		Short: "Send a test notification to all configured backends",
		RunE: func(cmd *cobra.Command, args []string) error {
			log := dc.DefaultLogger(os.Stdout, cfg.Quiet)
			fileCfg, err := dc.LoadConfig(log)
			if err != nil {
				return err
			}
			return dc.SendTestNotification(fileCfg.Notifications, log)
		},
	})

	return cmd
}

// setNotifyTarget sets or clears the first notification target of typ.
// If url is empty, all targets of typ are removed. Otherwise the first
// existing target of typ is updated, or a new one appended.
func setNotifyTarget(fileCfg *dc.Config, typ, url string, log dc.LogFunc) error {
	if url == "" {
		filtered := fileCfg.Notifications[:0]
		for _, t := range fileCfg.Notifications {
			if t.Type != typ {
				filtered = append(filtered, t)
			}
		}
		fileCfg.Notifications = filtered
		if err := dc.SaveConfig(fileCfg, log); err != nil {
			return err
		}
		log(dc.LvlINF, typ+"=disabled")
		return nil
	}

	fileCfg.Notifications = upsertNotifyTarget(fileCfg.Notifications, typ, url)
	if err := dc.SaveConfig(fileCfg, log); err != nil {
		return err
	}
	log(dc.LvlOK, fmt.Sprintf("%s_url=%q", typ, url))
	return nil
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
