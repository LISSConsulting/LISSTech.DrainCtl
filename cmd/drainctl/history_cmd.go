//go:build windows

package main

import (
	"fmt"
	"os"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/pipe"
	"github.com/spf13/cobra"
)

func historyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Display audit trail",
		RunE:  runHistory,
	}

	cmd.Flags().Int("limit", 50, "Maximum records to display")
	cmd.Flags().Bool("changes-only", false, "Show only state transitions")
	cmd.Flags().String("since", "", "Show records at or after this time (RFC3339, e.g. 2026-01-15T09:00:00Z)")
	cmd.Flags().String("until", "", "Show records at or before this time (RFC3339, e.g. 2026-01-15T18:00:00Z)")

	return cmd
}

func filterByTime(recs []dc.HistoryRecord, since, until *time.Time) []dc.HistoryRecord {
	if since == nil && until == nil {
		return recs
	}
	out := recs[:0]
	for _, r := range recs {
		ts, err := time.Parse(time.RFC3339, r.Timestamp)
		if err != nil {
			// Keep unparseable records rather than silently dropping them.
			out = append(out, r)
			continue
		}
		if since != nil && ts.Before(*since) {
			continue
		}
		if until != nil && ts.After(*until) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func runHistory(cmd *cobra.Command, args []string) error {
	format, err := getFormat(dc.FormatTable)
	if err != nil {
		return err
	}

	limit, _ := cmd.Flags().GetInt("limit")
	changesOnly, _ := cmd.Flags().GetBool("changes-only")
	sinceStr, _ := cmd.Flags().GetString("since")
	untilStr, _ := cmd.Flags().GetString("until")

	var since, until *time.Time
	if sinceStr != "" {
		t, err := time.Parse(time.RFC3339, sinceStr)
		if err != nil {
			return fmt.Errorf("--since: %w", err)
		}
		since = &t
	}
	if untilStr != "" {
		t, err := time.Parse(time.RFC3339, untilStr)
		if err != nil {
			return fmt.Errorf("--until: %w", err)
		}
		until = &t
	}

	// Try the service pipe first.
	if histRecs, err := pipe.HistoryViaPipe(limit, changesOnly); err == nil {
		// Apply time filter to pipe results.
		histRecs = filterByTime(histRecs, since, until)
		if len(histRecs) == 0 {
			fmt.Println("No records found.")
			return nil
		}
		dc.WriteHistoryRecords(os.Stdout, histRecs, format)
		return nil
	}

	// Fallback: direct file-based audit store.
	records, err := dc.GetHistory(dc.HistoryOptions{
		DBPath:      cfg.DB,
		Limit:       limit,
		ChangesOnly: changesOnly,
		Since:       since,
		Until:       until,
	})
	if err != nil {
		return fmt.Errorf("query history: %w", err)
	}

	if len(records) == 0 {
		fmt.Println("No records found.")
		return nil
	}

	dc.WriteHistory(os.Stdout, records, format)
	return nil
}
