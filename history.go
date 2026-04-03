//go:build windows

package drainctl

import "fmt"

// HistoryOptions configures a history query.
type HistoryOptions struct {
	DBPath      string
	Limit       int
	ChangesOnly bool
}

// GetHistory returns audit records from the JSONL trail.
func GetHistory(opts HistoryOptions) ([]AuditRecord, error) {
	store, err := OpenAuditStore(opts.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open audit store: %w", err)
	}
	defer func() { _ = store.Close() }()

	if opts.ChangesOnly {
		return store.Changes(opts.Limit)
	}
	return store.History(opts.Limit)
}
