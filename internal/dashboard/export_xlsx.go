//go:build windows

package dashboard

import (
	"io"

	excelize "github.com/xuri/excelize/v2"
)

// WriteXLSX serializes the snapshot to w as a true .xlsx file using
// excelize. Two sheets: Data (observations) and a self-describing Context
// (graph, scope, time range, coverage, series, generator).
//
// T023 lands the real implementation; this stub keeps the dispatcher
// (T011) buildable during Phase 2. The stub also pulls excelize through
// the compile graph so the dependency stays pinned.
func WriteXLSX(w io.Writer, snap Snapshot) error {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	_, err := f.WriteTo(w)
	return err
}
