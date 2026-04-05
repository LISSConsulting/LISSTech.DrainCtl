//go:build windows

package main

import (
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// fmtRFC3339 formats t as RFC3339, matching the format filterByTime parses.
func fmtRFC3339(t time.Time) string {
	return t.Format(time.RFC3339)
}

// ── filterByTime ──────────────────────────────────────────────────────────────

// TestFilterByTime_NilBothBoundsReturnsAll verifies that passing nil for both
// since and until returns the original slice without any allocation or iteration.
func TestFilterByTime_NilBothBoundsReturnsAll(t *testing.T) {
	recs := []dc.HistoryRecord{
		{Timestamp: fmtRFC3339(time.Now().Add(-2 * time.Hour))},
		{Timestamp: fmtRFC3339(time.Now().Add(-1 * time.Hour))},
		{Timestamp: fmtRFC3339(time.Now())},
	}
	got := filterByTime(recs, nil, nil)
	if len(got) != 3 {
		t.Errorf("len = %d, want 3 (both bounds nil must return all records)", len(got))
	}
}

// TestFilterByTime_EmptyInputReturnsEmpty verifies the zero-input case.
func TestFilterByTime_EmptyInputReturnsEmpty(t *testing.T) {
	since := time.Now()
	got := filterByTime(nil, &since, nil)
	if len(got) != 0 {
		t.Errorf("len = %d, want 0 for empty input", len(got))
	}
}

// TestFilterByTime_SinceExcludesOlderRecords verifies that records whose
// timestamps fall strictly before the since bound are dropped.
func TestFilterByTime_SinceExcludesOlderRecords(t *testing.T) {
	base := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	recs := []dc.HistoryRecord{
		{Timestamp: fmtRFC3339(base.Add(-3 * time.Hour)), Host: "old"},
		{Timestamp: fmtRFC3339(base.Add(-1 * time.Hour)), Host: "mid"},
		{Timestamp: fmtRFC3339(base), Host: "now"},
	}
	since := base.Add(-2 * time.Hour)
	got := filterByTime(recs, &since, nil)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (only mid and now pass since filter)", len(got))
	}
	if got[0].Host != "mid" || got[1].Host != "now" {
		t.Errorf("hosts = [%q, %q], want [mid, now]", got[0].Host, got[1].Host)
	}
}

// TestFilterByTime_UntilExcludesNewerRecords verifies that records whose
// timestamps fall strictly after the until bound are dropped.
func TestFilterByTime_UntilExcludesNewerRecords(t *testing.T) {
	base := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	recs := []dc.HistoryRecord{
		{Timestamp: fmtRFC3339(base.Add(-3 * time.Hour)), Host: "old"},
		{Timestamp: fmtRFC3339(base.Add(-1 * time.Hour)), Host: "mid"},
		{Timestamp: fmtRFC3339(base), Host: "now"},
	}
	until := base.Add(-2 * time.Hour)
	got := filterByTime(recs, nil, &until)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (only old passes until filter)", len(got))
	}
	if got[0].Host != "old" {
		t.Errorf("host = %q, want \"old\"", got[0].Host)
	}
}

// TestFilterByTime_BothBoundsWindowFilter verifies that only records within
// [since, until] (inclusive both ends) are retained.
func TestFilterByTime_BothBoundsWindowFilter(t *testing.T) {
	base := time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC)
	recs := []dc.HistoryRecord{
		{Timestamp: fmtRFC3339(base.Add(-1 * time.Hour)), Host: "before"},
		{Timestamp: fmtRFC3339(base), Host: "start"},
		{Timestamp: fmtRFC3339(base.Add(1 * time.Hour)), Host: "middle"},
		{Timestamp: fmtRFC3339(base.Add(2 * time.Hour)), Host: "end"},
		{Timestamp: fmtRFC3339(base.Add(3 * time.Hour)), Host: "after"},
	}
	since := base
	until := base.Add(2 * time.Hour)
	got := filterByTime(recs, &since, &until)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (start, middle, end — both bounds inclusive)", len(got))
	}
	if got[0].Host != "start" || got[1].Host != "middle" || got[2].Host != "end" {
		t.Errorf("hosts = [%q, %q, %q], want [start, middle, end]",
			got[0].Host, got[1].Host, got[2].Host)
	}
}

// TestFilterByTime_UnparseableTimestampIsKept verifies that records with
// malformed timestamps are kept (not silently dropped) even when a since/until
// bound would exclude all parseable records.
func TestFilterByTime_UnparseableTimestampIsKept(t *testing.T) {
	future := time.Now().Add(1 * time.Hour)
	recs := []dc.HistoryRecord{
		{Timestamp: "not-a-valid-rfc3339-timestamp", Host: "bad"},
		{Timestamp: fmtRFC3339(time.Now()), Host: "good"},
	}
	// since = 1h in the future → "good" (parseable, in the past) is excluded;
	// "bad" (unparseable) must be kept.
	got := filterByTime(recs, &future, nil)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (unparseable record must be kept)", len(got))
	}
	if got[0].Host != "bad" {
		t.Errorf("host = %q, want \"bad\"", got[0].Host)
	}
}

// TestFilterByTime_SinceBoundaryIsInclusive verifies that a record whose
// timestamp exactly equals since is included (not excluded as "before").
func TestFilterByTime_SinceBoundaryIsInclusive(t *testing.T) {
	exact := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	recs := []dc.HistoryRecord{
		{Timestamp: fmtRFC3339(exact), Host: "exact"},
		{Timestamp: fmtRFC3339(exact.Add(-time.Second)), Host: "before"},
	}
	got := filterByTime(recs, &exact, nil)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (exact since boundary must be inclusive)", len(got))
	}
	if got[0].Host != "exact" {
		t.Errorf("host = %q, want \"exact\"", got[0].Host)
	}
}

// TestFilterByTime_UntilBoundaryIsInclusive verifies that a record whose
// timestamp exactly equals until is included (not excluded as "after").
func TestFilterByTime_UntilBoundaryIsInclusive(t *testing.T) {
	exact := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	recs := []dc.HistoryRecord{
		{Timestamp: fmtRFC3339(exact), Host: "exact"},
		{Timestamp: fmtRFC3339(exact.Add(time.Second)), Host: "after"},
	}
	got := filterByTime(recs, nil, &exact)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (exact until boundary must be inclusive)", len(got))
	}
	if got[0].Host != "exact" {
		t.Errorf("host = %q, want \"exact\"", got[0].Host)
	}
}
