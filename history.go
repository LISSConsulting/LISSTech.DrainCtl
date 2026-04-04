//go:build windows

package drainctl

import (
	"fmt"
	"time"
)

// HistoryOptions configures a history query.
type HistoryOptions struct {
	DBPath      string
	Limit       int
	ChangesOnly bool
	Since       *time.Time
	Until       *time.Time
}

// GetHistory returns audit records from the JSONL trail.
func GetHistory(opts HistoryOptions) ([]AuditRecord, error) {
	store, err := OpenAuditStore(opts.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open audit store: %w", err)
	}
	defer func() { _ = store.Close() }()

	if opts.ChangesOnly {
		return store.ChangesFiltered(opts.Limit, opts.Since, opts.Until)
	}
	return store.HistoryFiltered(opts.Limit, opts.Since, opts.Until)
}
