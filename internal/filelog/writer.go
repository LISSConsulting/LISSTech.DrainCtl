//go:build windows

package filelog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// dateLayout is the on-disk date stamp embedded in archived filenames.
// Local time, sortable, never collides with the active log's name.
const dateLayout = "2006-01-02"

// Writer is a thread-safe, date-rotating file writer. Each calendar day's
// output goes to its own file: the active day appends to `path` (e.g.
// drainctl.log); on the first write after a date change, the active file
// is renamed to `<base>-<previous-date>.log` and a fresh `path` is opened.
// Files older than keepDays are pruned at rotation time.
//
// Legacy size-rotated files (`<base>.N.log` from the prior implementation)
// are not touched — they don't accrete further but stay on disk until the
// operator removes them manually.
type Writer struct {
	mu        sync.Mutex
	path      string
	base      string // path without .log extension
	keepDays  int
	file      *os.File
	openedDay string           // YYYY-MM-DD for the active file
	now       func() time.Time // injectable for tests
}

// New creates a Writer that rotates at local midnight, keeping keepDays of
// archived files. The active file's "day" is inferred from its mtime when
// the file already exists with content, so a service restart picks up
// yesterday's leftovers and rotates them into yesterday's archive on the
// first write today.
func New(path string, keepDays int) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("filelog: open %s: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("filelog: stat %s: %w", path, err)
	}
	base := strings.TrimSuffix(path, ".log")
	openedDay := time.Now().Format(dateLayout)
	if info.Size() > 0 {
		openedDay = info.ModTime().Format(dateLayout)
	}
	return &Writer{
		path: path, base: base, keepDays: keepDays,
		file: f, openedDay: openedDay, now: time.Now,
	}, nil
}

// Write implements io.Writer. Safe for concurrent use.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, fmt.Errorf("filelog: closed")
	}
	today := w.now().Format(dateLayout)
	if today != w.openedDay {
		if err := w.rotate(today); err != nil {
			return 0, fmt.Errorf("filelog: rotate: %w", err)
		}
	}
	return w.file.Write(p)
}

// Close closes the underlying file. Safe to call multiple times.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *Writer) rotate(today string) error {
	_ = w.file.Close()
	archive := fmt.Sprintf("%s-%s.log", w.base, w.openedDay)
	// Collision recovery: if a same-day archive already exists (clock
	// jump back, manual file copy, prior crash mid-rotation), append a
	// nanosecond suffix so the rename never silently overwrites prior
	// log content.
	if _, err := os.Stat(archive); err == nil {
		archive = fmt.Sprintf("%s-%s.%d.log", w.base, w.openedDay, w.now().UnixNano())
	}
	_ = os.Rename(w.path, archive)
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	w.file = f
	w.openedDay = today
	w.prune()
	return nil
}

// prune removes archived files older than keepDays. Called from rotate
// under w.mu; safe because it only touches sibling files, never w.file.
func (w *Writer) prune() {
	if w.keepDays <= 0 {
		return
	}
	cutoff := w.now().AddDate(0, 0, -w.keepDays)
	dir, baseName := filepath.Split(w.base)
	if dir == "" {
		dir = "."
	}
	prefix := baseName + "-"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".log") {
			continue
		}
		dateStr := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".log")
		// Strip any collision suffix (drainctl-2026-04-22.NNNNN.log).
		if i := strings.IndexByte(dateStr, '.'); i > 0 {
			dateStr = dateStr[:i]
		}
		d, err := time.ParseInLocation(dateLayout, dateStr, time.Local)
		if err != nil {
			continue
		}
		if d.Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}
