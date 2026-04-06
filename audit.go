//go:build windows

package drainctl

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// AuditRecord represents a single point-in-time observation, serialized as
// one JSON object per line in the audit trail file.
type AuditRecord struct {
	Timestamp            time.Time `json:"ts"`
	Host                 string    `json:"host"`
	DrainMode            DrainMode `json:"mode"`
	DrainLabel           string    `json:"mode_label"`
	KeyModified          time.Time `json:"key_modified,omitempty"`
	Changed              bool      `json:"changed,omitempty"`
	ChangedBy            string    `json:"changed_by,omitempty"`
	ActiveSessions       int       `json:"active_sessions,omitempty"`
	DisconnectedSessions int       `json:"disconnected_sessions,omitempty"`
	TotalSessions        int       `json:"total_sessions,omitempty"`
	MaxSessions          int       `json:"max_sessions,omitempty"`
	ExitCode             int       `json:"exit"`
}

// AuditStore manages the JSONL-based audit trail.
type AuditStore struct {
	path string
}

// OpenAuditStore ensures the directory exists and returns a handle to the
// JSONL audit file.
func OpenAuditStore(path string) (*AuditStore, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create audit directory: %w", err)
	}
	return &AuditStore{path: path}, nil
}

// Close is a no-op (satisfies the pattern from the old DB interface).
func (a *AuditStore) Close() error { return nil }

// Record appends a single observation to the JSONL file.
func (a *AuditStore) Record(rec *AuditRecord) error {
	f, err := os.OpenFile(a.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open audit file: %w", err)
	}
	defer func() { _ = f.Close() }()

	// AuditRecord contains only primitive-typed fields; Marshal cannot fail.
	data, _ := json.Marshal(rec)
	_, err = fmt.Fprintf(f, "%s\n", data)
	return err
}

// scanRecords iterates every record in the JSONL file from oldest to newest,
// calling fn for each parsed record. Scanning stops early when fn returns
// false or EOF is reached. Malformed lines are silently skipped.
func (a *AuditStore) scanRecords(fn func(AuditRecord) bool) error {
	f, err := os.Open(a.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open audit file: %w", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec AuditRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if !fn(rec) {
			break
		}
	}
	return scanner.Err()
}

// LastObservation returns the most recent record, or nil if the file is empty.
// Retains only a single record in memory regardless of file size.
func (a *AuditStore) LastObservation() (*AuditRecord, error) {
	var last AuditRecord
	found := false
	err := a.scanRecords(func(rec AuditRecord) bool {
		last = rec
		found = true
		return true
	})
	if err != nil || !found {
		return nil, err
	}
	return &last, nil
}

// Prune removes records older than the retention period by rewriting the file.
func (a *AuditStore) Prune(retention time.Duration) (int64, error) {
	cutoff := time.Now().Add(-retention)

	var kept []AuditRecord
	var total int64
	err := a.scanRecords(func(rec AuditRecord) bool {
		total++
		if !rec.Timestamp.Before(cutoff) {
			kept = append(kept, rec)
		}
		return true
	})
	if err != nil {
		return 0, err
	}

	pruned := total - int64(len(kept))
	if pruned == 0 {
		return 0, nil
	}

	tmp := a.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, fmt.Errorf("create temp file: %w", err)
	}

	w := bufio.NewWriter(f)
	for _, r := range kept {
		// AuditRecord contains only primitive-typed fields; Marshal cannot fail.
		data, _ := json.Marshal(r)
		_, _ = fmt.Fprintf(w, "%s\n", data)
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return 0, fmt.Errorf("flush temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return 0, fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmp, a.path); err != nil {
		_ = os.Remove(tmp)
		return 0, fmt.Errorf("rename temp file: %w", err)
	}

	return pruned, nil
}

// History returns the most recent n records, newest first.
// Uses a rolling window of size n to avoid loading the entire file into memory
// when only a small number of recent records is needed. When n <= 0, all
// records are returned.
func (a *AuditStore) History(n int) ([]AuditRecord, error) {
	var buf []AuditRecord
	if n > 0 {
		buf = make([]AuditRecord, 0, n)
	}

	err := a.scanRecords(func(rec AuditRecord) bool {
		if n <= 0 {
			buf = append(buf, rec)
			return true
		}
		if len(buf) < n {
			buf = append(buf, rec)
		} else {
			// Slide oldest off the front, append newest at the end.
			copy(buf, buf[1:])
			buf[n-1] = rec
		}
		return true
	})
	if err != nil {
		return nil, err
	}

	// Reverse to newest-first order.
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return buf, nil
}

// StateSince returns the timestamp when the current mode was first observed.
// It walks the audit trail backward to find the last transition into the given
// mode and returns the timestamp of that record. If no transition is found
// (i.e. every record has the same mode), it returns the oldest record's timestamp.
// Returns nil if the trail is empty.
func (a *AuditStore) StateSince(mode DrainMode) (*time.Time, error) {
	// Must examine every record to walk backward — collect all then scan in reverse.
	var records []AuditRecord
	err := a.scanRecords(func(rec AuditRecord) bool {
		records = append(records, rec)
		return true
	})
	if err != nil || len(records) == 0 {
		return nil, err
	}

	// Walk backward from newest. The first record with a different mode
	// means the record after it is when the current state started.
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].DrainMode != mode {
			if i+1 < len(records) {
				t := records[i+1].Timestamp
				return &t, nil
			}
			return nil, nil
		}
	}

	// All records have the same mode — state predates the audit trail.
	t := records[0].Timestamp
	return &t, nil
}

// HistoryFiltered returns records in the optional [since, until] window, newest first.
// When both since and until are nil it behaves identically to History(n).
// Limit n applies after filtering (n<=0 = unlimited).
func (a *AuditStore) HistoryFiltered(n int, since, until *time.Time) ([]AuditRecord, error) {
	var buf []AuditRecord
	if n > 0 {
		buf = make([]AuditRecord, 0, n)
	}
	err := a.scanRecords(func(rec AuditRecord) bool {
		if since != nil && rec.Timestamp.Before(*since) {
			return true
		}
		if until != nil && rec.Timestamp.After(*until) {
			return true
		}
		if n <= 0 {
			buf = append(buf, rec)
			return true
		}
		if len(buf) < n {
			buf = append(buf, rec)
		} else {
			copy(buf, buf[1:])
			buf[n-1] = rec
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return buf, nil
}

// ChangesFiltered returns only transition records in the optional [since, until] window,
// newest first. When both since and until are nil it behaves identically to Changes(n).
func (a *AuditStore) ChangesFiltered(n int, since, until *time.Time) ([]AuditRecord, error) {
	var buf []AuditRecord
	if n > 0 {
		buf = make([]AuditRecord, 0, n)
	}
	err := a.scanRecords(func(rec AuditRecord) bool {
		if !rec.Changed {
			return true
		}
		if since != nil && rec.Timestamp.Before(*since) {
			return true
		}
		if until != nil && rec.Timestamp.After(*until) {
			return true
		}
		if n <= 0 {
			buf = append(buf, rec)
			return true
		}
		if len(buf) < n {
			buf = append(buf, rec)
		} else {
			copy(buf, buf[1:])
			buf[n-1] = rec
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return buf, nil
}

// Changes returns only records where a state transition occurred, newest first.
// Uses a rolling window of size n to avoid loading more changes than requested.
// When n <= 0, all change records are returned.
func (a *AuditStore) Changes(n int) ([]AuditRecord, error) {
	var changes []AuditRecord
	if n > 0 {
		changes = make([]AuditRecord, 0, n)
	}

	err := a.scanRecords(func(rec AuditRecord) bool {
		if !rec.Changed {
			return true
		}
		if n > 0 && len(changes) == n {
			// Ring: drop oldest, keep newest n.
			copy(changes, changes[1:])
			changes[n-1] = rec
		} else {
			changes = append(changes, rec)
		}
		return true
	})
	if err != nil {
		return nil, err
	}

	// Reverse to newest-first order.
	for i, j := 0, len(changes)-1; i < j; i, j = i+1, j-1 {
		changes[i], changes[j] = changes[j], changes[i]
	}
	return changes, nil
}
