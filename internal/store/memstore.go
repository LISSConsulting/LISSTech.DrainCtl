//go:build windows

package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// MemAuditStore is the service-mode audit store: in-memory records backed
// by an exclusively-locked JSONL file. All read methods are lock-free for
// concurrent pipe clients; writes are serialized.
type MemAuditStore struct {
	mu      sync.RWMutex
	records []dc.AuditRecord
	dirty   int // count of unflushed records
	file    *os.File
	handle  windows.Handle
	path    string
}

// OpenMemAuditStore opens the JSONL file with an exclusive lock, loads all
// records into memory, and returns the store. If the file doesn't exist it
// is created. Returns an error if another process holds the lock.
func OpenMemAuditStore(path string) (*MemAuditStore, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create audit directory: %w", err)
	}

	// Open with exclusive write access (others can read but not write).
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}

	h, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ, // others can read, not write
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open audit file (exclusive): %w", err)
	}

	// Wrap the handle in an os.File for Go I/O.
	f := os.NewFile(uintptr(h), path)

	m := &MemAuditStore{
		file:   f,
		handle: h,
		path:   path,
	}

	// Load existing records.
	if err := m.load(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("load audit file: %w", err)
	}

	slog.Info("memstore=open", "records", len(m.records), "path", path)
	return m, nil
}

// load reads all JSONL records from the file into memory.
func (m *MemAuditStore) load() error {
	// Seek to start.
	if _, err := m.file.Seek(0, 0); err != nil {
		return err
	}

	scanner := bufio.NewScanner(m.file)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec dc.AuditRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue // skip corrupted lines
		}
		m.records = append(m.records, rec)
	}
	return scanner.Err()
}

// Append adds a record to the in-memory store and marks it dirty.
func (m *MemAuditStore) Append(rec *dc.AuditRecord) {
	m.mu.Lock()
	m.records = append(m.records, *rec)
	m.dirty++
	m.mu.Unlock()
}

// LastObservation returns the most recent record, or nil if empty.
func (m *MemAuditStore) LastObservation() *dc.AuditRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.records) == 0 {
		return nil
	}
	r := m.records[len(m.records)-1]
	return &r
}

// History returns the most recent n records, newest first.
func (m *MemAuditStore) History(n int) []dc.AuditRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Copy and reverse.
	out := make([]dc.AuditRecord, len(m.records))
	copy(out, m.records)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}

	if n > 0 && n < len(out) {
		out = out[:n]
	}
	return out
}

// Changes returns only transition records, newest first.
func (m *MemAuditStore) Changes(n int) []dc.AuditRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var changes []dc.AuditRecord
	for _, r := range m.records {
		if r.Changed {
			changes = append(changes, r)
		}
	}

	for i, j := 0, len(changes)-1; i < j; i, j = i+1, j-1 {
		changes[i], changes[j] = changes[j], changes[i]
	}

	if n > 0 && n < len(changes) {
		changes = changes[:n]
	}
	return changes
}

// StateSince returns when the given mode was first observed in a continuous
// run. Returns nil if the store is empty.
func (m *MemAuditStore) StateSince(mode dc.DrainMode) *time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.records) == 0 {
		return nil
	}

	for i := len(m.records) - 1; i >= 0; i-- {
		if m.records[i].DrainMode != mode {
			if i+1 < len(m.records) {
				t := m.records[i+1].Timestamp
				return &t
			}
			return nil
		}
	}

	t := m.records[0].Timestamp
	return &t
}

// Flush writes unflushed records to disk by appending to the file.
func (m *MemAuditStore) Flush() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.flushLocked()
}

// FlushIfDirty calls Flush only if there are unflushed records.
func (m *MemAuditStore) FlushIfDirty() error {
	m.mu.RLock()
	dirty := m.dirty
	m.mu.RUnlock()
	if dirty == 0 {
		return nil
	}
	return m.Flush()
}

func (m *MemAuditStore) flushLocked() error {
	if m.dirty == 0 {
		return nil
	}

	// Seek to end of file.
	if _, err := m.file.Seek(0, 2); err != nil {
		return fmt.Errorf("seek to end: %w", err)
	}

	w := bufio.NewWriter(m.file)
	start := len(m.records) - m.dirty
	for i := start; i < len(m.records); i++ {
		// AuditRecord contains only primitive-typed fields; Marshal cannot fail.
		data, _ := json.Marshal(m.records[i])
		_, _ = fmt.Fprintf(w, "%s\n", data)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flush: %w", err)
	}

	m.dirty = 0
	slog.Info("memstore=flushed", "records", len(m.records)-start)
	return nil
}

// Prune removes records older than the retention period, rewrites the file,
// and updates the in-memory slice.
func (m *MemAuditStore) Prune(retention time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.records) == 0 {
		return 0, nil
	}

	cutoff := time.Now().Add(-retention)
	var kept []dc.AuditRecord
	for _, r := range m.records {
		if !r.Timestamp.Before(cutoff) {
			kept = append(kept, r)
		}
	}

	pruned := int64(len(m.records) - len(kept))
	if pruned == 0 {
		return 0, nil
	}

	// Rewrite the entire file.
	if _, err := m.file.Seek(0, 0); err != nil {
		return 0, fmt.Errorf("seek: %w", err)
	}
	if err := m.file.Truncate(0); err != nil {
		return 0, fmt.Errorf("truncate: %w", err)
	}

	w := bufio.NewWriter(m.file)
	for _, r := range kept {
		// AuditRecord contains only primitive-typed fields; Marshal cannot fail.
		data, _ := json.Marshal(r)
		_, _ = fmt.Fprintf(w, "%s\n", data)
	}
	if err := w.Flush(); err != nil {
		return 0, fmt.Errorf("flush after prune: %w", err)
	}

	m.records = kept
	m.dirty = 0
	slog.Info("memstore=pruned", "removed", pruned, "remaining", len(kept))
	return pruned, nil
}

// Close flushes any dirty records and closes the file (releasing the lock).
func (m *MemAuditStore) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.file == nil {
		return nil
	}

	flushErr := m.flushLocked()
	closeErr := m.file.Close()
	m.file = nil
	if flushErr != nil {
		return fmt.Errorf("flush on close: %w", flushErr)
	}
	return closeErr
}
