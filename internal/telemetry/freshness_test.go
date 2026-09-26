//go:build windows

package telemetry

import (
	"context"
	"testing"
	"time"
)

func TestFreshnessMarkOfflineIsOneShotAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	epoch := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	now := epoch.Add(3 * time.Minute)

	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	store := NewFreshnessStore(db)
	if transitioned, err := store.MarkOffline(ctx, "srv-01", epoch, now); err != nil || !transitioned {
		t.Fatalf("first MarkOffline = (%v, %v), want (true, nil)", transitioned, err)
	}
	if transitioned, err := store.MarkOffline(ctx, "srv-01", epoch, now.Add(time.Minute)); err != nil || transitioned {
		t.Fatalf("duplicate MarkOffline = (%v, %v), want (false, nil)", transitioned, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db, err = Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if transitioned, err := NewFreshnessStore(db).MarkOffline(ctx, "SRV-01", epoch, now.Add(2*time.Minute)); err != nil || transitioned {
		t.Fatalf("post-restart MarkOffline = (%v, %v), want (false, nil)", transitioned, err)
	}
}

func TestFreshnessRecoveryAllowsNextEpochOffline(t *testing.T) {
	ctx := context.Background()
	store := NewFreshnessStore(openTestDB(t))
	epoch1 := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	epoch2 := epoch1.Add(time.Minute)

	if recovered, err := store.MarkFresh(ctx, "SRV-01", epoch1); err != nil || recovered {
		t.Fatalf("initial MarkFresh = (%v, %v), want (false, nil)", recovered, err)
	}
	if transitioned, err := store.MarkOffline(ctx, "SRV-01", epoch1, epoch1.Add(3*time.Minute)); err != nil || !transitioned {
		t.Fatalf("first MarkOffline = (%v, %v), want (true, nil)", transitioned, err)
	}
	if recovered, err := store.MarkFresh(ctx, "SRV-01", epoch2); err != nil || !recovered {
		t.Fatalf("recovery MarkFresh = (%v, %v), want (true, nil)", recovered, err)
	}
	if recovered, err := store.MarkFresh(ctx, "SRV-01", epoch2); err != nil || recovered {
		t.Fatalf("duplicate MarkFresh = (%v, %v), want (false, nil)", recovered, err)
	}
	if transitioned, err := store.MarkOffline(ctx, "SRV-01", epoch2, epoch2.Add(3*time.Minute)); err != nil || !transitioned {
		t.Fatalf("next-epoch MarkOffline = (%v, %v), want (true, nil)", transitioned, err)
	}
}

func TestFreshnessStaleEpochCannotOverwriteNewerState(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	store := NewFreshnessStore(db)
	epoch1 := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	epoch2 := epoch1.Add(time.Minute)

	if _, err := store.MarkFresh(ctx, "SRV-01", epoch2); err != nil {
		t.Fatalf("MarkFresh current epoch: %v", err)
	}
	if transitioned, err := store.MarkOffline(ctx, "SRV-01", epoch1, epoch1.Add(3*time.Minute)); err != nil || transitioned {
		t.Fatalf("stale MarkOffline = (%v, %v), want (false, nil)", transitioned, err)
	}
	if recovered, err := store.MarkFresh(ctx, "SRV-01", epoch1); err != nil || recovered {
		t.Fatalf("stale MarkFresh = (%v, %v), want (false, nil)", recovered, err)
	}

	var storedEpoch, offlineAt int64
	if err := db.reader.QueryRowContext(ctx,
		`SELECT report_epoch_ms, COALESCE(offline_emitted_at_ms, 0) FROM host_freshness WHERE host = ?`, "SRV-01").
		Scan(&storedEpoch, &offlineAt); err != nil {
		t.Fatalf("query freshness state: %v", err)
	}
	if storedEpoch != epoch2.UnixMilli() || offlineAt != 0 {
		t.Fatalf("freshness state = (epoch=%d, offline=%d), want (%d, 0)", storedEpoch, offlineAt, epoch2.UnixMilli())
	}
}

func TestFreshnessCanonicalizationAndRemove(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	store := NewFreshnessStore(db)
	epoch := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

	if _, err := store.MarkFresh(ctx, "  srv-01  ", epoch); err != nil {
		t.Fatalf("MarkFresh: %v", err)
	}
	if transitioned, err := store.MarkOffline(ctx, "sRv-01", epoch, epoch.Add(3*time.Minute)); err != nil || !transitioned {
		t.Fatalf("MarkOffline case variant = (%v, %v), want (true, nil)", transitioned, err)
	}
	var host string
	if err := db.reader.QueryRowContext(ctx, `SELECT host FROM host_freshness`).Scan(&host); err != nil {
		t.Fatalf("query canonical host: %v", err)
	}
	if host != "SRV-01" {
		t.Errorf("stored host = %q, want %q", host, "SRV-01")
	}
	if removed, err := store.Remove(ctx, "srv-01"); err != nil || !removed {
		t.Fatalf("Remove = (%v, %v), want (true, nil)", removed, err)
	}
	if removed, err := store.Remove(ctx, "SRV-01"); err != nil || removed {
		t.Fatalf("second Remove = (%v, %v), want (false, nil)", removed, err)
	}
}
