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
	Timestamp   time.Time `json:"ts"`
	Host        string    `json:"host"`
	DrainMode   DrainMode `json:"mode"`
	DrainLabel  string    `json:"mode_label"`
	KeyModified time.Time `json:"key_modified,omitempty"`
	Changed     bool      `json:"changed,omitempty"`
	ChangedBy   string    `json:"changed_by,omitempty"`
	ExitCode    int       `json:"exit"`
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

	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal record: %w", err)
	}
	_, err = fmt.Fprintf(f, "%s\n", data)
	return err
}

// readAll parses every record from the JSONL file.
func (a *AuditStore) readAll() ([]AuditRecord, error) {
	f, err := os.Open(a.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open audit file: %w", err)
	}
	defer func() { _ = f.Close() }()

	var records []AuditRecord
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
		records = append(records, rec)
	}
	return records, scanner.Err()
}

// LastObservation returns the most recent record, or nil if the file is empty.
func (a *AuditStore) LastObservation() (*AuditRecord, error) {
	records, err := a.readAll()
	if err != nil || len(records) == 0 {
		return nil, err
	}
	return &records[len(records)-1], nil
}

// Prune removes records older than the retention period by rewriting the file.
func (a *AuditStore) Prune(retention time.Duration) (int64, error) {
	records, err := a.readAll()
	if err != nil || len(records) == 0 {
		return 0, err
	}

	cutoff := time.Now().Add(-retention)
	var kept []AuditRecord
	for _, r := range records {
		if !r.Timestamp.Before(cutoff) {
			kept = append(kept, r)
		}
	}

	pruned := int64(len(records) - len(kept))
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
func (a *AuditStore) History(n int) ([]AuditRecord, error) {
	records, err := a.readAll()
	if err != nil {
		return nil, err
	}

	for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
		records[i], records[j] = records[j], records[i]
	}

	if n > 0 && n < len(records) {
		records = records[:n]
	}
	return records, nil
}

// StateSince returns the timestamp when the current mode was first observed.
// It walks the audit trail backward to find the last transition into the given
// mode and returns the timestamp of that record. If no transition is found
// (i.e. every record has the same mode), it returns the oldest record's timestamp.
// Returns nil if the trail is empty.
func (a *AuditStore) StateSince(mode DrainMode) (*time.Time, error) {
	records, err := a.readAll()
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

// Changes returns only records where a state transition occurred, newest first.
func (a *AuditStore) Changes(n int) ([]AuditRecord, error) {
	records, err := a.readAll()
	if err != nil {
		return nil, err
	}

	var changes []AuditRecord
	for _, r := range records {
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
	return changes, nil
}
