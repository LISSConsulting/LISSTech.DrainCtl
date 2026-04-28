//go:build windows

package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// legacyServersFilename is the pre-009 servers roster filename. After a
// successful import the file is renamed to <name>.migrated.<unix-ts> so
// subsequent boots see no candidate and skip the import.
const legacyServersFilename = "servers.json"

// legacyServerInfo is the pre-009 JSON shape of one entry in servers.json.
// We decode only the fields that map cleanly into telemetry.ServerInfo —
// LastResult is preserved as raw JSON so an old CheckResult schema doesn't
// block migration when fields drift.
type legacyServerInfo struct {
	Hostname     string          `json:"hostname"`
	RegisteredAt time.Time       `json:"registered_at"`
	LastSeen     time.Time       `json:"last_seen,omitempty"`
	LastResult   json.RawMessage `json:"last_result,omitempty"`
}

// MigrateLegacyServersJSON reads any servers.json left in dataDir by a pre-009
// binary, imports its rows into the `servers` SQLite table, and renames the
// file so the import is one-shot. Absent or unreadable files are silent
// no-ops — this boot helper never blocks dashboard startup.
func MigrateLegacyServersJSON(ctx context.Context, dataDir string, store serverReader) error {
	if dataDir == "" || store == nil {
		return nil
	}
	path := filepath.Join(dataDir, legacyServersFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}

	var rows []legacyServerInfo
	if err := json.Unmarshal(data, &rows); err != nil {
		// A corrupt legacy file must not block bringup — rename it aside so
		// the next boot doesn't re-trip the same decode error, then return.
		aside := fmt.Sprintf("%s.migration-failed.%d", path, time.Now().UTC().Unix())
		_ = os.Rename(path, aside)
		return fmt.Errorf("decode %s: %w (moved to %s)", path, err, aside)
	}

	infos := make([]telemetry.ServerInfo, 0, len(rows))
	for _, r := range rows {
		if r.Hostname == "" {
			continue
		}
		info := telemetry.ServerInfo{
			Hostname:     r.Hostname,
			RegisteredAt: r.RegisteredAt,
			LastSeen:     r.LastSeen,
		}
		if len(r.LastResult) > 0 && string(r.LastResult) != "null" {
			info.LastResultJSON = string(r.LastResult)
		}
		infos = append(infos, info)
	}

	inserted, err := store.Import(ctx, infos)
	if err != nil {
		return fmt.Errorf("import %s: %w", path, err)
	}

	migrated := fmt.Sprintf("%s.migrated.%d", path, time.Now().UTC().Unix())
	if err := os.Rename(path, migrated); err != nil {
		// Even if rename fails, the rows are already in SQLite and Import is
		// idempotent — so the worst case is a repeat import on next boot.
		slog.Warn("dashboard: legacy servers.json rename failed; subsequent boots will retry import",
			"path", path, "error", err)
		return nil
	}
	slog.Info("dashboard: migrated legacy servers.json",
		"path", path, "moved_to", migrated, "rows_read", len(rows), "rows_inserted", inserted)
	return nil
}
