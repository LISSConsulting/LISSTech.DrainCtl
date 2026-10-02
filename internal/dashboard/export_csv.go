//go:build windows

package dashboard

import "io"

// WriteCSV serializes the snapshot's data rows to w as CSV (UTF-8, comma
// separator, single header row). The fixed header and column order match
// the Excel Data sheet so cross-format parity holds (per FR-012).
//
// T015 lands the real implementation; this stub keeps the dispatcher
// (T011) buildable during Phase 2.
func WriteCSV(w io.Writer, snap Snapshot) error {
	// Real implementation lives in T015 (Phase 3 / US1).
	return writeCSVScaffold(w, snap)
}

func writeCSVScaffold(w io.Writer, snap Snapshot) error {
	_, err := w.Write([]byte("snapshot_rows=" + itoa(len(snap.Rows)) + "\n"))
	return err
}

// itoa is a tiny int-to-string helper kept inline to avoid an import that
// gets dropped by `go mod tidy` until the real writer lands.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
