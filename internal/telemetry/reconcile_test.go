//go:build windows

package telemetry

import (
	"context"
	"testing"
	"time"
)

// reconcileFixture spins up a telemetry DB + AuditStore for each test; caller
// uses the returned helpers to seed audit history and run Reconcile. A fresh
// DB per test isolates the maintenance_jobs row (name is a PK, so a leftover
// row from an earlier test in the same package would let an assertion pass
// vacuously).
func reconcileFixture(t *testing.T) (*AuditStore, *DB) {
	t.Helper()
	db := openTestDB(t)
	store, err := NewAuditStore(context.Background(), db)
	if err != nil {
		t.Fatalf("NewAuditStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, db
}

// readDriftJob returns the drift_reconciliation maintenance_jobs row or
// fails the test if the row is missing — Reconcile MUST upsert the row on
// every invocation whether or not it writes an audit row.
func readDriftJob(t *testing.T, db *DB) (outcome string, rowsAffected int64, startedMs, finishedMs, durationMs int64, reason string) {
	t.Helper()
	err := db.reader.QueryRow(
		`SELECT outcome, rows_affected, started_ts, finished_ts, duration_ms, reason
		 FROM maintenance_jobs WHERE name = ?`,
		driftJobName,
	).Scan(&outcome, &rowsAffected, &startedMs, &finishedMs, &durationMs, &reason)
	if err != nil {
		t.Fatalf("read drift_reconciliation maintenance_jobs row: %v", err)
	}
	return
}

// countAuditRows returns the total row count in audit for host. Used to verify
// that Reconcile either wrote exactly one reconciliation row or none at all.
func countAuditRows(t *testing.T, db *DB, host string) int {
	t.Helper()
	var n int
	if err := db.reader.QueryRow(
		`SELECT COUNT(*) FROM audit WHERE host = ?`, host,
	).Scan(&n); err != nil {
		t.Fatalf("count audit rows for %s: %v", host, err)
	}
	return n
}

func TestReconcile_NoDriftWritesNothing(t *testing.T) {
	store, db := reconcileFixture(t)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	keyMod := base.Add(-time.Minute) // registry older than last audit — no oscillation
	if err := store.Append(ctx, AuditRecord{
		Ts: base, Host: "SRV01", PrevState: 0, NewState: 2,
		ChangedBy: "alice", KeyModifiedTs: &keyMod,
	}); err != nil {
		t.Fatalf("seed audit: %v", err)
	}

	now := base.Add(time.Hour)
	if err := Reconcile(ctx, db, store,
		DrainProbe{Host: "SRV01", ModeValue: 2, KeyModified: keyMod},
		now,
	); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got := countAuditRows(t, db, "SRV01"); got != 1 {
		t.Errorf("audit rows for SRV01 = %d, want 1 (only the seed)", got)
	}
	outcome, rowsAffected, _, _, _, _ := readDriftJob(t, db)
	if outcome != "success" || rowsAffected != 0 {
		t.Errorf("drift job = (%s, %d), want (success, 0)", outcome, rowsAffected)
	}
}

func TestReconcile_DriftWritesOneRow(t *testing.T) {
	store, db := reconcileFixture(t)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	if err := store.Append(ctx, AuditRecord{
		Ts: base, Host: "SRV01", PrevState: 0, NewState: 2, ChangedBy: "alice",
	}); err != nil {
		t.Fatalf("seed audit: %v", err)
	}

	now := base.Add(time.Hour)
	keyMod := now.Add(-5 * time.Minute)
	if err := Reconcile(ctx, db, store,
		DrainProbe{Host: "SRV01", ModeValue: 5, KeyModified: keyMod},
		now,
	); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got := countAuditRows(t, db, "SRV01"); got != 2 {
		t.Errorf("audit rows for SRV01 = %d, want 2 (seed + reconciliation)", got)
	}

	records, _, err := store.QueryRange(ctx, QueryFilter{Host: "SRV01", Limit: 10})
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	var recon AuditRecord
	var found bool
	for _, r := range records {
		if r.Reconciliation {
			recon = r
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no reconciliation row emitted; records=%+v", records)
	}
	if recon.PrevState != 2 || recon.NewState != 5 {
		t.Errorf("reconciliation state = (%d -> %d), want (2 -> 5)", recon.PrevState, recon.NewState)
	}
	if !recon.Ts.Equal(now) {
		t.Errorf("reconciliation ts = %v, want %v", recon.Ts, now)
	}
	if recon.BeforeTs == nil || !recon.BeforeTs.Equal(base) {
		t.Errorf("reconciliation before_ts = %v, want %v", recon.BeforeTs, base)
	}
	if recon.KeyModifiedTs == nil || !recon.KeyModifiedTs.Equal(keyMod) {
		t.Errorf("reconciliation key_modified_ts = %v, want %v", recon.KeyModifiedTs, keyMod)
	}
	if recon.Reason != "service-downtime drift: last-known 2, observed 5" {
		t.Errorf("reconciliation reason = %q", recon.Reason)
	}

	_, rowsAffected, _, _, _, _ := readDriftJob(t, db)
	if rowsAffected != 1 {
		t.Errorf("drift job rows_affected = %d, want 1", rowsAffected)
	}
}

func TestReconcile_EmptyAuditTreatsCurrentAsKnown(t *testing.T) {
	store, db := reconcileFixture(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := Reconcile(ctx, db, store,
		DrainProbe{Host: "SRV-NEW", ModeValue: 1, KeyModified: now.Add(-time.Hour)},
		now,
	); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got := countAuditRows(t, db, "SRV-NEW"); got != 0 {
		t.Errorf("audit rows for SRV-NEW = %d, want 0 (empty audit => current state is the baseline)", got)
	}
	outcome, rowsAffected, _, _, _, _ := readDriftJob(t, db)
	if outcome != "success" || rowsAffected != 0 {
		t.Errorf("drift job = (%s, %d), want (success, 0)", outcome, rowsAffected)
	}
}

func TestReconcile_PerHostIndependent(t *testing.T) {
	store, db := reconcileFixture(t)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	// SRV-STABLE: last audit matches current state, no oscillation → no row.
	if err := store.Append(ctx, AuditRecord{
		Ts: base, Host: "SRV-STABLE", PrevState: 0, NewState: 1, ChangedBy: "alice",
	}); err != nil {
		t.Fatalf("seed STABLE: %v", err)
	}
	// SRV-DRIFT: last audit = 0, current mode = 3 → drift row expected.
	if err := store.Append(ctx, AuditRecord{
		Ts: base, Host: "SRV-DRIFT", PrevState: 0, NewState: 0, ChangedBy: "alice",
	}); err != nil {
		t.Fatalf("seed DRIFT: %v", err)
	}
	// SRV-EMPTY: no audit rows → no row on reconcile.

	now := base.Add(time.Hour)
	if err := Reconcile(ctx, db, store,
		DrainProbe{Host: "SRV-STABLE", ModeValue: 1, KeyModified: base.Add(-time.Minute)},
		now,
	); err != nil {
		t.Fatalf("Reconcile STABLE: %v", err)
	}
	if err := Reconcile(ctx, db, store,
		DrainProbe{Host: "SRV-DRIFT", ModeValue: 3, KeyModified: now.Add(-5 * time.Minute)},
		now,
	); err != nil {
		t.Fatalf("Reconcile DRIFT: %v", err)
	}
	if err := Reconcile(ctx, db, store,
		DrainProbe{Host: "SRV-EMPTY", ModeValue: 2, KeyModified: now.Add(-5 * time.Minute)},
		now,
	); err != nil {
		t.Fatalf("Reconcile EMPTY: %v", err)
	}

	if got := countAuditRows(t, db, "SRV-STABLE"); got != 1 {
		t.Errorf("STABLE audit rows = %d, want 1", got)
	}
	if got := countAuditRows(t, db, "SRV-DRIFT"); got != 2 {
		t.Errorf("DRIFT audit rows = %d, want 2", got)
	}
	if got := countAuditRows(t, db, "SRV-EMPTY"); got != 0 {
		t.Errorf("EMPTY audit rows = %d, want 0", got)
	}

	// Per-host reconcile runs share the single drift_reconciliation row
	// (upsert on PK). Last caller's row count wins — the SRV-EMPTY run, which
	// wrote no drift row.
	_, rowsAffected, _, _, _, _ := readDriftJob(t, db)
	if rowsAffected != 0 {
		t.Errorf("drift job rows_affected after final EMPTY run = %d, want 0", rowsAffected)
	}
}

func TestReconcile_OscillationDetectedByRegistryTimestamp(t *testing.T) {
	store, db := reconcileFixture(t)
	ctx := context.Background()

	// SRV01 went 1 → 0 → 1 while service was down: net-zero state match, but
	// registry LastWriteTime is newer than the last audit row.
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-2 * time.Hour)
	if err := store.Append(ctx, AuditRecord{
		Ts: base, Host: "SRV01", PrevState: 0, NewState: 1, ChangedBy: "alice",
	}); err != nil {
		t.Fatalf("seed audit: %v", err)
	}

	now := base.Add(2 * time.Hour)
	keyMod := base.Add(time.Hour) // registry touched AFTER last audit row
	if err := Reconcile(ctx, db, store,
		DrainProbe{Host: "SRV01", ModeValue: 1, KeyModified: keyMod},
		now,
	); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got := countAuditRows(t, db, "SRV01"); got != 2 {
		t.Fatalf("audit rows = %d, want 2 (seed + oscillation reconciliation)", got)
	}

	records, _, err := store.QueryRange(ctx, QueryFilter{Host: "SRV01", Limit: 10})
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	var recon AuditRecord
	var found bool
	for _, r := range records {
		if r.Reconciliation {
			recon = r
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("oscillation: no reconciliation row emitted")
	}
	if recon.PrevState != 1 || recon.NewState != 1 {
		t.Errorf("oscillation reconciliation state = (%d -> %d), want (1 -> 1)",
			recon.PrevState, recon.NewState)
	}
	if recon.KeyModifiedTs == nil || !recon.KeyModifiedTs.Equal(keyMod) {
		t.Errorf("oscillation key_modified_ts = %v, want %v", recon.KeyModifiedTs, keyMod)
	}

	_, rowsAffected, _, _, _, _ := readDriftJob(t, db)
	if rowsAffected != 1 {
		t.Errorf("drift job rows_affected = %d, want 1", rowsAffected)
	}
}

func TestReconcile_WritesMaintenanceJobsRow(t *testing.T) {
	store, db := reconcileFixture(t)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	if err := store.Append(ctx, AuditRecord{
		Ts: base, Host: "SRV01", PrevState: 0, NewState: 2, ChangedBy: "alice",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	before := time.Now().UTC().Add(-time.Millisecond).UnixMilli()
	now := base.Add(time.Hour)
	if err := Reconcile(ctx, db, store,
		DrainProbe{Host: "SRV01", ModeValue: 5, KeyModified: now.Add(-time.Minute)},
		now,
	); err != nil {
		t.Fatalf("Reconcile first: %v", err)
	}
	after := time.Now().UTC().Add(time.Millisecond).UnixMilli()

	outcome, rowsAffected, startedMs, finishedMs, durationMs, reason := readDriftJob(t, db)
	if outcome != "success" {
		t.Errorf("outcome = %q, want success (reason=%q)", outcome, reason)
	}
	if rowsAffected != 1 {
		t.Errorf("rows_affected = %d, want 1", rowsAffected)
	}
	if finishedMs < startedMs {
		t.Errorf("finished_ts (%d) < started_ts (%d)", finishedMs, startedMs)
	}
	// `started` is now.UTC() (the caller-supplied clock); `finished` is the
	// wall clock when the row is upserted. started can be earlier than the
	// `before` probe — assert only that finished is within the wall-clock
	// window and duration_ms is non-negative.
	if finishedMs < before || finishedMs > after {
		t.Errorf("finished_ts (%d) outside wall-clock window [%d, %d]",
			finishedMs, before, after)
	}
	if durationMs < 0 {
		t.Errorf("duration_ms = %d, want >= 0", durationMs)
	}

	// Second invocation with no drift must upsert the same row (PK=name) and
	// overwrite rows_affected — never accumulate.
	if err := Reconcile(ctx, db, store,
		DrainProbe{Host: "SRV01", ModeValue: 5, KeyModified: now.Add(-time.Minute)},
		now.Add(time.Minute),
	); err != nil {
		t.Fatalf("Reconcile second: %v", err)
	}

	var count int
	if err := db.reader.QueryRow(
		`SELECT COUNT(*) FROM maintenance_jobs WHERE name = ?`, driftJobName,
	).Scan(&count); err != nil {
		t.Fatalf("count drift rows: %v", err)
	}
	if count != 1 {
		t.Errorf("maintenance_jobs rows with name=%s = %d, want 1 (upsert)", driftJobName, count)
	}
	_, rowsAffected2, _, _, _, _ := readDriftJob(t, db)
	if rowsAffected2 != 0 {
		t.Errorf("rows_affected after no-drift rerun = %d, want 0 (upsert overwrites)", rowsAffected2)
	}
}

// TestReconcile_PrincipalIsEmpty pins the invariant that every reconciliation
// row is written with principal = reconciliationPrincipal (the empty string).
// The audit_principal partial index excludes empty-string principals, so a
// non-empty principal would silently start surfacing drift rows in
// principal-filtered queries (data-model.md, schema.go audit_principal index).
func TestReconcile_PrincipalIsEmpty(t *testing.T) {
	store, db := reconcileFixture(t)
	ctx := context.Background()

	if reconciliationPrincipal != "" {
		t.Fatalf("reconciliationPrincipal = %q, want \"\" — "+
			"audit_principal partial index WHERE principal <> '' would leak drift rows",
			reconciliationPrincipal)
	}

	base := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	hosts := []string{"SRV01", "SRV02"}
	for _, h := range hosts {
		if err := store.Append(ctx, AuditRecord{
			Ts: base, Host: h, PrevState: 0, NewState: 1, ChangedBy: "alice",
		}); err != nil {
			t.Fatalf("seed %s: %v", h, err)
		}
	}

	now := base.Add(time.Hour)
	for i, h := range hosts {
		if err := Reconcile(ctx, db, store,
			DrainProbe{Host: h, ModeValue: 4 + i, KeyModified: now.Add(-time.Minute)},
			now,
		); err != nil {
			t.Fatalf("Reconcile %s: %v", h, err)
		}
	}

	var reconCount int
	if err := db.reader.QueryRow(
		`SELECT COUNT(*) FROM audit WHERE reconciliation = 1`,
	).Scan(&reconCount); err != nil {
		t.Fatalf("count reconciliation rows: %v", err)
	}
	if reconCount != 2 {
		t.Fatalf("reconciliation rows = %d, want 2 (one per host)", reconCount)
	}

	var nonEmpty int
	if err := db.reader.QueryRow(
		`SELECT COUNT(*) FROM audit WHERE reconciliation = 1 AND principal <> ?`,
		reconciliationPrincipal,
	).Scan(&nonEmpty); err != nil {
		t.Fatalf("count non-empty principals: %v", err)
	}
	if nonEmpty != 0 {
		t.Errorf("reconciliation rows with principal <> %q: %d, want 0",
			reconciliationPrincipal, nonEmpty)
	}
}
