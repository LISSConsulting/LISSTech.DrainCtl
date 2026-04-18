//go:build windows

package telemetry

import (
	"context"
	"testing"
	"time"
)

func newMaintenanceStore(t *testing.T) (*MaintenanceStore, *DB) {
	t.Helper()
	db := openTestDB(t)
	return NewMaintenanceStore(db), db
}

func TestUpsertJob_OverwritesOnRerun(t *testing.T) {
	store, _ := newMaintenanceStore(t)
	ctx := context.Background()

	first := Result{
		Started:      time.Unix(1000, 0).UTC(),
		Finished:     time.Unix(1005, 0).UTC(),
		Outcome:      "failure",
		Reason:       "disk full",
		RowsAffected: 0,
	}
	if err := store.UpsertJob(ctx, "retention", first); err != nil {
		t.Fatalf("first UpsertJob: %v", err)
	}

	second := Result{
		Started:      time.Unix(2000, 0).UTC(),
		Finished:     time.Unix(2010, 0).UTC(),
		Outcome:      "success",
		Reason:       "",
		RowsAffected: 42,
	}
	if err := store.UpsertJob(ctx, "retention", second); err != nil {
		t.Fatalf("second UpsertJob: %v", err)
	}

	jobs, err := store.ListJobs(ctx)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected one row after two upserts, got %d", len(jobs))
	}
	got := jobs[0]
	if got.Name != "retention" {
		t.Errorf("name = %q, want retention", got.Name)
	}
	if !got.Started.Equal(second.Started) {
		t.Errorf("started = %v, want %v", got.Started, second.Started)
	}
	if !got.Finished.Equal(second.Finished) {
		t.Errorf("finished = %v, want %v", got.Finished, second.Finished)
	}
	wantDurMs := second.Finished.Sub(second.Started).Milliseconds()
	if got.DurationMs != wantDurMs {
		t.Errorf("duration_ms = %d, want %d", got.DurationMs, wantDurMs)
	}
	if got.Outcome != "success" {
		t.Errorf("outcome = %q, want success", got.Outcome)
	}
	if got.Reason != "" {
		t.Errorf("reason = %q, want empty (second upsert blanked failure reason)", got.Reason)
	}
	if got.RowsAffected != 42 {
		t.Errorf("rows_affected = %d, want 42", got.RowsAffected)
	}
}

func TestListJobs_ReturnsAllNames(t *testing.T) {
	store, _ := newMaintenanceStore(t)
	ctx := context.Background()

	insertedOutOfOrder := []string{"retention", "aggregator_hourly", "aggregator_5min", "jsonl_migration", "drift_reconciliation"}
	start := time.Unix(1700000000, 0).UTC()
	for i, name := range insertedOutOfOrder {
		run := Result{
			Started:      start.Add(time.Duration(i) * time.Second),
			Finished:     start.Add(time.Duration(i)*time.Second + 10*time.Millisecond),
			Outcome:      "success",
			RowsAffected: int64(i),
		}
		if err := store.UpsertJob(ctx, name, run); err != nil {
			t.Fatalf("UpsertJob %q: %v", name, err)
		}
	}

	jobs, err := store.ListJobs(ctx)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != len(insertedOutOfOrder) {
		t.Fatalf("expected %d jobs, got %d", len(insertedOutOfOrder), len(jobs))
	}

	wantOrder := []string{"aggregator_5min", "aggregator_hourly", "drift_reconciliation", "jsonl_migration", "retention"}
	for i, want := range wantOrder {
		if jobs[i].Name != want {
			t.Errorf("jobs[%d].Name = %q, want %q", i, jobs[i].Name, want)
		}
	}
}

func TestOverdue_ComputedFromInterval(t *testing.T) {
	finished := time.Unix(1_700_000_000, 0).UTC()
	job := Job{Name: "retention", Finished: finished}

	tests := []struct {
		name     string
		interval int64
		now      time.Time
		want     bool
	}{
		{
			name:     "zero interval is one-shot, never overdue",
			interval: 0,
			now:      finished.Add(365 * 24 * time.Hour),
			want:     false,
		},
		{
			name:     "negative interval treated as one-shot",
			interval: -1,
			now:      finished.Add(time.Hour),
			want:     false,
		},
		{
			name:     "just under 2x interval is not overdue",
			interval: 60,
			now:      finished.Add(2*60*time.Second - time.Millisecond),
			want:     false,
		},
		{
			name:     "exactly 2x interval is not overdue (strict >)",
			interval: 60,
			now:      finished.Add(2 * 60 * time.Second),
			want:     false,
		},
		{
			name:     "just over 2x interval is overdue",
			interval: 60,
			now:      finished.Add(2*60*time.Second + time.Millisecond),
			want:     true,
		},
		{
			name:     "well past 2x interval is overdue",
			interval: 900, // 15-minute retention default
			now:      finished.Add(2 * time.Hour),
			want:     true,
		},
		{
			name:     "now before finished (clock skew) is not overdue",
			interval: 60,
			now:      finished.Add(-time.Hour),
			want:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := job.IsOverdue(tc.interval, tc.now)
			if got != tc.want {
				t.Errorf("IsOverdue(interval=%d, now-finished=%v) = %v, want %v",
					tc.interval, tc.now.Sub(finished), got, tc.want)
			}
		})
	}
}
