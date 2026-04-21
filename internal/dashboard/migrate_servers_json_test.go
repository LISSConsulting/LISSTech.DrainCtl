//go:build windows

package dashboard

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// openStoreIn opens a ServerStore rooted at dir for migration tests.
func openStoreIn(t *testing.T, dir string) (*telemetry.ServerStore, *telemetry.DB) {
	t.Helper()
	db, err := telemetry.Open(dir)
	if err != nil {
		t.Fatalf("telemetry.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return telemetry.NewServerStore(db), db
}

// writeLegacyFile emits a servers.json with the pre-009 shape.
func writeLegacyFile(t *testing.T, dir string, rows []ServerInfo) {
	t.Helper()
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatalf("marshal legacy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, legacyServersFilename), data, 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
}

func TestMigrateLegacyServersJSON_NoFile_Noop(t *testing.T) {
	dir := t.TempDir()
	store, _ := openStoreIn(t, dir)

	if err := MigrateLegacyServersJSON(context.Background(), dir, store); err != nil {
		t.Fatalf("migrate with no file: %v", err)
	}
	infos, _ := store.All(context.Background())
	if len(infos) != 0 {
		t.Errorf("store non-empty after no-file migration: %+v", infos)
	}
}

func TestMigrateLegacyServersJSON_ImportsAndRenames(t *testing.T) {
	dir := t.TempDir()
	store, _ := openStoreIn(t, dir)

	now := time.Now().UTC().Truncate(time.Millisecond)
	legacy := []ServerInfo{
		{Hostname: "ALPHA", RegisteredAt: now.Add(-24 * time.Hour), LastSeen: now.Add(-time.Minute),
			LastResult: &dc.CheckResult{Host: "ALPHA", Status: "Healthy"}},
		{Hostname: "BRAVO", RegisteredAt: now.Add(-48 * time.Hour)},
	}
	writeLegacyFile(t, dir, legacy)

	if err := MigrateLegacyServersJSON(context.Background(), dir, store); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	infos, err := store.All(context.Background())
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("imported rows = %d, want 2", len(infos))
	}

	// Legacy file must be renamed with .migrated.<ts> suffix.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var foundMigrated bool
	for _, e := range entries {
		if e.Name() == legacyServersFilename {
			t.Error("legacy servers.json still present after migration")
		}
		if strings.HasPrefix(e.Name(), legacyServersFilename+".migrated.") {
			foundMigrated = true
		}
	}
	if !foundMigrated {
		t.Error("expected servers.json.migrated.<ts>, not found")
	}

	// Verify ALPHA's LastResult round-tripped.
	alpha, _ := store.Get(context.Background(), "ALPHA")
	if alpha == nil {
		t.Fatal("ALPHA missing")
	}
	if alpha.LastResultJSON == "" {
		t.Error("ALPHA LastResultJSON empty after migration")
	}
	var cr dc.CheckResult
	if err := json.Unmarshal([]byte(alpha.LastResultJSON), &cr); err != nil {
		t.Fatalf("unmarshal ALPHA LastResult: %v", err)
	}
	if cr.Status != "Healthy" {
		t.Errorf("ALPHA LastResult.Status = %q, want Healthy", cr.Status)
	}
}

func TestMigrateLegacyServersJSON_IdempotentAcrossBoots(t *testing.T) {
	dir := t.TempDir()
	store, _ := openStoreIn(t, dir)

	writeLegacyFile(t, dir, []ServerInfo{
		{Hostname: "ALPHA", RegisteredAt: time.Now().UTC()},
	})

	// First boot imports and renames the file.
	if err := MigrateLegacyServersJSON(context.Background(), dir, store); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	// Second boot sees no servers.json (already renamed) → no-op.
	if err := MigrateLegacyServersJSON(context.Background(), dir, store); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	infos, _ := store.All(context.Background())
	if len(infos) != 1 {
		t.Errorf("rows = %d after two migrations, want 1 (idempotent)", len(infos))
	}
}

func TestMigrateLegacyServersJSON_CorruptFile_MovesAside(t *testing.T) {
	dir := t.TempDir()
	store, _ := openStoreIn(t, dir)

	path := filepath.Join(dir, legacyServersFilename)
	if err := os.WriteFile(path, []byte("not valid json"), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}

	err := MigrateLegacyServersJSON(context.Background(), dir, store)
	if err == nil {
		t.Error("expected error for corrupt file, got nil")
	}

	// Corrupt file must be moved aside so subsequent boots don't re-trip the error.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("corrupt servers.json still present — should have been moved to .migration-failed.<ts>")
	}
}
