//go:build windows

package telemetry

import (
	"context"
	"strings"
	"testing"
	"time"
)

func newAuditStore(t *testing.T) (*AuditStore, *DB) {
	t.Helper()
	db := openTestDB(t)
	store, err := NewAuditStore(context.Background(), db)
	if err != nil {
		t.Fatalf("NewAuditStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, db
}

func TestAppend_RoundTrip(t *testing.T) {
	store, _ := newAuditStore(t)
	ctx := context.Background()

	ts := time.Now().UTC().Truncate(time.Millisecond)
	keyMod := ts.Add(-time.Second)
	beforeTs := ts.Add(-time.Hour)

	rec := AuditRecord{
		Ts:             ts,
		Host:           "SRV01",
		PrevState:      0,
		NewState:       1,
		Principal:      "DOMAIN\\alice",
		ChangedBy:      "alice",
		Reason:         "scheduled maintenance",
		KeyModifiedTs:  &keyMod,
		Reconciliation: true,
		BeforeTs:       &beforeTs,
	}
	if err := store.Append(ctx, rec); err != nil {
		t.Fatalf("Append: %v", err)
	}

	records, cursor, err := store.QueryRange(ctx, QueryFilter{Host: "SRV01"})
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if cursor != "" {
		t.Errorf("unexpected cursor on single-row result: %q", cursor)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	got := records[0]
	if !got.Ts.Equal(ts) {
		t.Errorf("Ts = %v, want %v", got.Ts, ts)
	}
	if got.Host != "SRV01" || got.PrevState != 0 || got.NewState != 1 {
		t.Errorf("state round-trip wrong: %+v", got)
	}
	if got.Principal != "DOMAIN\\alice" || got.ChangedBy != "alice" || got.Reason != "scheduled maintenance" {
		t.Errorf("text fields wrong: %+v", got)
	}
	if !got.Reconciliation {
		t.Errorf("Reconciliation = false, want true")
	}
	if got.KeyModifiedTs == nil || !got.KeyModifiedTs.Equal(keyMod) {
		t.Errorf("KeyModifiedTs = %v, want %v", got.KeyModifiedTs, keyMod)
	}
	if got.BeforeTs == nil || !got.BeforeTs.Equal(beforeTs) {
		t.Errorf("BeforeTs = %v, want %v", got.BeforeTs, beforeTs)
	}
}

func TestAppend_DuplicateKeyIgnored(t *testing.T) {
	store, db := newAuditStore(t)
	ctx := context.Background()

	ts := time.Now().UTC().Truncate(time.Millisecond)
	rec := AuditRecord{
		Ts: ts, Host: "SRV01", PrevState: 0, NewState: 1, ChangedBy: "alice",
	}
	if err := store.Append(ctx, rec); err != nil {
		t.Fatalf("first Append: %v", err)
	}

	// Same PK (ts, host, new_state) but mutated payload fields — INSERT ON
	// CONFLICT DO NOTHING means the second write is silently dropped.
	rec2 := rec
	rec2.ChangedBy = "bob"
	rec2.Reason = "should not overwrite"
	if err := store.Append(ctx, rec2); err != nil {
		t.Fatalf("duplicate Append returned error, want nil: %v", err)
	}

	var count int
	if err := db.reader.QueryRow(
		`SELECT COUNT(*) FROM audit WHERE host=? AND new_state=?`, "SRV01", 1,
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want 1 (ON CONFLICT DO NOTHING)", count)
	}

	records, _, err := store.QueryRange(ctx, QueryFilter{Host: "SRV01"})
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if len(records) != 1 || records[0].ChangedBy != "alice" || records[0].Reason != "" {
		t.Errorf("conflict overwrote original row: %+v", records)
	}
}

func TestQueryRange_OrdersDesc(t *testing.T) {
	store, _ := newAuditStore(t)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Millisecond)
	for i := 0; i < 5; i++ {
		if err := store.Append(ctx, AuditRecord{
			Ts:        base.Add(time.Duration(i) * time.Second),
			Host:      "SRV01",
			PrevState: 0,
			NewState:  i%2 + 1,
			ChangedBy: "alice",
		}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	records, _, err := store.QueryRange(ctx, QueryFilter{Host: "SRV01"})
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if len(records) != 5 {
		t.Fatalf("records = %d, want 5", len(records))
	}
	for i := 1; i < len(records); i++ {
		if !records[i-1].Ts.After(records[i].Ts) {
			t.Errorf("ordering broken at idx %d: %v !> %v",
				i, records[i-1].Ts, records[i].Ts)
		}
	}
}

func TestQueryRange_CursorPaginates(t *testing.T) {
	store, _ := newAuditStore(t)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Millisecond)
	const total = 7
	for i := 0; i < total; i++ {
		if err := store.Append(ctx, AuditRecord{
			Ts:        base.Add(time.Duration(i) * time.Second),
			Host:      "SRV01",
			PrevState: 0,
			NewState:  1,
			ChangedBy: "alice",
		}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	var all []AuditRecord
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatalf("pagination did not terminate after %d pages", pages)
		}
		got, next, err := store.QueryRange(ctx, QueryFilter{
			Host: "SRV01", Limit: 3, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		all = append(all, got...)
		if next == "" {
			break
		}
		cursor = next
	}

	if len(all) != total {
		t.Fatalf("paginated total = %d, want %d", len(all), total)
	}
	// Page boundaries preserve DESC order across the whole sequence.
	for i := 1; i < len(all); i++ {
		if !all[i-1].Ts.After(all[i].Ts) {
			t.Errorf("paginated order broken at idx %d: %v !> %v",
				i, all[i-1].Ts, all[i].Ts)
		}
	}
	// No row appears twice (cursor advances strictly past the last tuple).
	seen := make(map[int64]bool, total)
	for _, r := range all {
		if seen[r.Ts.UnixMilli()] {
			t.Errorf("duplicate row in paginated results: ts=%v", r.Ts)
		}
		seen[r.Ts.UnixMilli()] = true
	}

	if _, _, err := store.QueryRange(ctx, QueryFilter{Cursor: "not-base64-!!!"}); err != ErrInvalidCursor {
		t.Errorf("invalid cursor: err = %v, want ErrInvalidCursor", err)
	}
}

func TestQueryRange_FilterByActor(t *testing.T) {
	store, _ := newAuditStore(t)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Millisecond)
	actors := []string{"alice", "bob", "alice", "carol"}
	for i, who := range actors {
		if err := store.Append(ctx, AuditRecord{
			Ts:        base.Add(time.Duration(i) * time.Second),
			Host:      "SRV01",
			PrevState: 0,
			NewState:  1,
			ChangedBy: who,
		}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	records, _, err := store.QueryRange(ctx, QueryFilter{Actor: "alice"})
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2 alice rows", len(records))
	}
	for _, r := range records {
		if r.ChangedBy != "alice" {
			t.Errorf("actor filter leaked: %+v", r)
		}
	}

	records, _, err = store.QueryRange(ctx, QueryFilter{Actor: "nobody"})
	if err != nil {
		t.Fatalf("QueryRange unknown actor: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("unknown actor returned rows: %+v", records)
	}
}

func TestLatestByHost_AllHosts(t *testing.T) {
	store, _ := newAuditStore(t)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Millisecond)
	type sample struct {
		host     string
		offsetS  int
		newState int
	}
	samples := []sample{
		{"SRV01", 0, 1},
		{"SRV01", 10, 0},
		{"SRV01", 20, 1}, // latest for SRV01
		{"SRV02", 5, 1},
		{"SRV02", 15, 0}, // latest for SRV02
		{"SRV03", 0, 1},  // only row for SRV03
	}
	for _, s := range samples {
		if err := store.Append(ctx, AuditRecord{
			Ts:        base.Add(time.Duration(s.offsetS) * time.Second),
			Host:      s.host,
			PrevState: 0,
			NewState:  s.newState,
			ChangedBy: "alice",
		}); err != nil {
			t.Fatalf("Append %+v: %v", s, err)
		}
	}

	latest, err := store.LatestByHost(ctx)
	if err != nil {
		t.Fatalf("LatestByHost: %v", err)
	}
	if len(latest) != 3 {
		t.Errorf("hosts = %d, want 3: %+v", len(latest), latest)
	}

	wantTs := map[string]time.Time{
		"SRV01": base.Add(20 * time.Second),
		"SRV02": base.Add(15 * time.Second),
		"SRV03": base,
	}
	wantState := map[string]int{"SRV01": 1, "SRV02": 0, "SRV03": 1}
	for host, ts := range wantTs {
		got, ok := latest[host]
		if !ok {
			t.Errorf("host %s missing from LatestByHost", host)
			continue
		}
		if !got.Ts.Equal(ts) {
			t.Errorf("%s: ts = %v, want %v", host, got.Ts, ts)
		}
		if got.NewState != wantState[host] {
			t.Errorf("%s: new_state = %d, want %d", host, got.NewState, wantState[host])
		}
	}

	// Tie-break: two rows on the same host at the same ts with different new_state
	// — LatestByHost must pick MAX(new_state) to match QueryRange's PK ordering.
	tieTs := base.Add(time.Hour)
	for _, ns := range []int{0, 1} {
		if err := store.Append(ctx, AuditRecord{
			Ts: tieTs, Host: "SRV04", PrevState: 0, NewState: ns, ChangedBy: "alice",
		}); err != nil {
			t.Fatalf("tie seed: %v", err)
		}
	}
	latest, err = store.LatestByHost(ctx)
	if err != nil {
		t.Fatalf("LatestByHost tie: %v", err)
	}
	if latest["SRV04"].NewState != 1 {
		t.Errorf("tie-break new_state = %d, want 1 (MAX)", latest["SRV04"].NewState)
	}
}

// TestAudit_DedicatedConnectionHasSynchronousFull pins the FR-001b guarantee
// that audit commits are fsync-durable. Acquires the store's pinned connection
// and confirms PRAGMA synchronous returns 2 (FULL), not 1 (NORMAL inherited
// from the writer pool).
func TestAudit_DedicatedConnectionHasSynchronousFull(t *testing.T) {
	store, _ := newAuditStore(t)
	ctx := context.Background()

	if store.conn == nil {
		t.Fatal("AuditStore.conn is nil; expected pinned write connection")
	}
	var sync int
	if err := store.conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync); err != nil {
		t.Fatalf("PRAGMA synchronous: %v", err)
	}
	if sync != 2 {
		t.Errorf("synchronous = %d, want 2 (FULL)", sync)
	}
}

// TestAuditAppend_DiskFullIsGracefullyHandled simulates an I/O failure on the
// audit write path by closing the pinned connection. Append must return a
// wrapped error that identifies the call site, not panic or hang. Name
// disambiguated from the metrics-side TestAppend_DiskFullIsGracefullyHandled
// — Go requires unique test names per package.
func TestAuditAppend_DiskFullIsGracefullyHandled(t *testing.T) {
	db := openTestDB(t)
	store, err := NewAuditStore(context.Background(), db)
	if err != nil {
		t.Fatalf("NewAuditStore: %v", err)
	}

	// Close the pinned connection so the next ExecContext fails immediately.
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Null out cleanly — Append guards against a nil conn separately; re-point
	// to a freshly-closed real conn to exercise the ExecContext failure path.
	c, err := db.auditDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire replacement conn: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close replacement conn: %v", err)
	}
	store.conn = c

	err = store.Append(context.Background(), AuditRecord{
		Ts: time.Now().UTC(), Host: "SRV01", PrevState: 0, NewState: 1, ChangedBy: "alice",
	})
	if err == nil {
		t.Fatal("Append on closed conn returned nil error")
	}
	if !strings.Contains(err.Error(), "audit Append") {
		t.Errorf("error does not cite call site 'audit Append': %v", err)
	}
	if !strings.Contains(err.Error(), "SRV01") {
		t.Errorf("error does not cite host: %v", err)
	}
}
