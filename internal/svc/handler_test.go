//go:build windows

package svc

import (
	"path/filepath"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/store"
)

// newHandlerStore opens a MemAuditStore at a temp path and returns a
// serviceHandler backed by it. The caller must close the store when done.
func newHandlerStore(t *testing.T) (*serviceHandler, *store.MemAuditStore) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	st, err := store.OpenMemAuditStore(path, dc.DiscardLogger())
	if err != nil {
		t.Fatalf("OpenMemAuditStore: %v", err)
	}
	h := &serviceHandler{store: st}
	cfg := dc.DefaultConfig().ToServiceConfig()
	h.cfg.Store(&cfg)
	return h, st
}

// ── HandleHistory ─────────────────────────────────────────────────────────────

// TestHandleHistory_AllRecords verifies that changesOnly=false returns all
// appended records, not just transition records.
func TestHandleHistory_AllRecords(t *testing.T) {
	h, st := newHandlerStore(t)
	defer func() { _ = st.Close() }()

	base := time.Now()
	st.Append(&dc.AuditRecord{Timestamp: base, Host: "srv", DrainMode: dc.AllowAll, Changed: false})
	st.Append(&dc.AuditRecord{Timestamp: base.Add(time.Second), Host: "srv", DrainMode: dc.PreventNewLogon, Changed: true})
	st.Append(&dc.AuditRecord{Timestamp: base.Add(2 * time.Second), Host: "srv", DrainMode: dc.PreventNewLogon, Changed: false})

	got := h.HandleHistory(0, false)
	if len(got) != 3 {
		t.Errorf("HandleHistory(0, false) returned %d records, want 3", len(got))
	}
}

// TestHandleHistory_ChangesOnly verifies that changesOnly=true filters to only
// records where Changed==true (i.e. state transitions).
func TestHandleHistory_ChangesOnly(t *testing.T) {
	h, st := newHandlerStore(t)
	defer func() { _ = st.Close() }()

	base := time.Now()
	st.Append(&dc.AuditRecord{Timestamp: base, Host: "srv", DrainMode: dc.AllowAll, Changed: false})
	st.Append(&dc.AuditRecord{Timestamp: base.Add(time.Second), Host: "srv", DrainMode: dc.PreventNewLogon, Changed: true})
	st.Append(&dc.AuditRecord{Timestamp: base.Add(2 * time.Second), Host: "srv", DrainMode: dc.PreventNewLogon, Changed: false})
	st.Append(&dc.AuditRecord{Timestamp: base.Add(3 * time.Second), Host: "srv", DrainMode: dc.AllowAll, Changed: true})

	got := h.HandleHistory(0, true)
	if len(got) != 2 {
		t.Errorf("HandleHistory(0, true) returned %d records, want 2 (transitions only)", len(got))
	}
	for _, r := range got {
		if !r.Changed {
			t.Errorf("changesOnly=true returned record with Changed=false: %+v", r)
		}
	}
}
