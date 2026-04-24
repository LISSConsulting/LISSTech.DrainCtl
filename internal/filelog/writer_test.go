//go:build windows

package filelog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// dayClock is a controllable time source for rotation tests.
type dayClock struct{ t time.Time }

func (c *dayClock) now() time.Time          { return c.t }
func (c *dayClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestWriteAppendsToActiveFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	w, err := New(path, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello\n" {
		t.Errorf("content = %q, want %q", string(data), "hello\n")
	}
}

func TestRotatesOnDateChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	w, err := New(path, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	// Anchor the clock to a known day so the archive filename is predictable.
	day1 := time.Date(2026, 4, 22, 12, 0, 0, 0, time.Local)
	clock := &dayClock{t: day1}
	w.now = clock.now
	w.openedDay = day1.Format(dateLayout)

	if _, err := w.Write([]byte("day1\n")); err != nil {
		t.Fatal(err)
	}

	// Advance into the next day and write again — should rotate.
	clock.advance(24 * time.Hour)
	if _, err := w.Write([]byte("day2\n")); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(dir, "test-2026-04-22.log")
	if data, err := os.ReadFile(archive); err != nil {
		t.Fatalf("expected archive %s: %v", archive, err)
	} else if string(data) != "day1\n" {
		t.Errorf("archive content = %q, want %q", string(data), "day1\n")
	}

	if data, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	} else if string(data) != "day2\n" {
		t.Errorf("active content = %q, want %q", string(data), "day2\n")
	}
}

func TestPruneRemovesFilesOlderThanKeepDays(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	// Pre-seed the directory with mock-archived files spanning 14 days.
	today := time.Date(2026, 4, 23, 10, 0, 0, 0, time.Local)
	for d := 0; d < 14; d++ {
		day := today.AddDate(0, 0, -d).Format(dateLayout)
		stale := filepath.Join(dir, fmt.Sprintf("test-%s.log", day))
		if err := os.WriteFile(stale, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	w, err := New(path, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	clock := &dayClock{t: today}
	w.now = clock.now
	w.openedDay = today.AddDate(0, 0, -1).Format(dateLayout) // force rotation on next write

	// Trigger rotate → prune.
	if _, err := w.Write([]byte("trigger\n")); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "test-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		dateStr := strings.TrimSuffix(strings.TrimPrefix(name, "test-"), ".log")
		d, err := time.ParseInLocation(dateLayout, dateStr, time.Local)
		if err != nil {
			continue
		}
		if d.Before(today.AddDate(0, 0, -7)) {
			t.Errorf("expected %s pruned (older than 7 days), still exists", name)
		}
	}
}

func TestRotationSurvivesArchiveCollision(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	// Pre-create the archive that rotation would normally produce so the
	// rename has to fall through the collision-recovery branch.
	day := time.Date(2026, 4, 22, 0, 0, 0, 0, time.Local)
	preexisting := filepath.Join(dir, "test-2026-04-22.log")
	if err := os.WriteFile(preexisting, []byte("preexisting\n"), 0644); err != nil {
		t.Fatal(err)
	}

	w, err := New(path, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	clock := &dayClock{t: day}
	w.now = clock.now
	w.openedDay = day.Format(dateLayout)
	if _, err := w.Write([]byte("day1\n")); err != nil {
		t.Fatal(err)
	}
	clock.advance(24 * time.Hour)
	if _, err := w.Write([]byte("day2\n")); err != nil {
		t.Fatal(err)
	}

	// The pre-existing archive must still hold its original content.
	if data, err := os.ReadFile(preexisting); err != nil {
		t.Fatal(err)
	} else if string(data) != "preexisting\n" {
		t.Errorf("preexisting archive overwritten: got %q", string(data))
	}

	// And a separate suffixed archive must hold the rotated day1 content.
	entries, _ := os.ReadDir(dir)
	var collisionFile string
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, "test-2026-04-22.") && strings.HasSuffix(n, ".log") && n != "test-2026-04-22.log" {
			collisionFile = filepath.Join(dir, n)
			break
		}
	}
	if collisionFile == "" {
		t.Fatal("expected a suffixed collision archive, found none")
	}
	if data, err := os.ReadFile(collisionFile); err != nil {
		t.Fatal(err)
	} else if string(data) != "day1\n" {
		t.Errorf("collision archive content = %q, want %q", string(data), "day1\n")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	w, err := New(path, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}
