# Data Model: Agent Self-Poll Auto-Update (010)

This feature adds one new config struct. Durable persistence of version transitions to the audit store was originally specced here but is **deferred** — see [§2](#2-audit-event-string-auto_update_install-new-value-no-schema-change) for the rationale.

## 1. UpdateConfig (new struct on root `Config`)

- **Location**: `config.go` — new exported struct `UpdateConfig`, new field `Update UpdateConfig` on the root `Config`.
- **Fields**:
  - `Enabled bool` — JSON `enabled`. **Default `false` (opt-in).** When false, the updater Subsystem's Start is a no-op (no goroutine, no http.Client, no GitHub call). Operators must edit `config.json` to flip this to `true`.
  - `Channel string` — JSON `channel`. Recognized values `"stable"` and `"prerelease"`. Default `"stable"`. Empty string (or absent field) is treated as `"stable"`. Any other value is clamped to `"stable"` in `LoadConfig` with a `slog.Warn` naming both the configured and clamped values. When `"stable"`, the updater polls `/releases/latest` and ignores prereleases. When `"prerelease"`, the updater polls `/releases?per_page=1` and accepts the most recent release whether prerelease or not. **Operationally**: every drainctl release through 010 ships with the GitHub `--prerelease` flag; until a non-prerelease release is published, `channel="prerelease"` is required for any auto-update activity at all. Documented in [spec.md Assumption 5](./spec.md#assumptions).
  - `PollInterval Duration` — JSON `poll_interval`, accepting Go duration strings (`"24h"`, `"6h30m"`). Default `24h`. Minimum permitted value `1h`; below that, `LoadConfig` clamps to `1h` and emits a `slog.Warn` naming the configured value and the clamped value.
- **Defaults**: Zero-value initialization matches the documented defaults for `Enabled` (Go's bool zero is `false`). `Channel` and `PollInterval` need explicit defaulting in `LoadConfig` because Go's zero values (`""`, `0`) differ from the documented defaults (`"stable"`, `24h`). Missing `update` object in `config.json` → `Enabled=false, Channel="stable", PollInterval=24h`. Missing `channel` field → `"stable"`. Missing `poll_interval` field → `24h`. The opt-in design means an operator who wants auto-update writes `{"update": {"enabled": true, "channel": "prerelease"}}` (today) or `{"update": {"enabled": true}}` (post-stable-graduation, defaulting to stable).
- **Mutation contract**: Operators edit by hand (or via dashboard config UI in a future feature). Atomic config save (already in place via the named-mutex + MoveFileEx path) preserves the rest of the file.
- **Migration**: None. Greenfield from this release forward; pre-010 `config.json` files Just Work because Go's JSON unmarshal tolerates missing fields.

```go
// UpdateConfig controls the agent's self-poll auto-update behavior. The
// updater subsystem (internal/updater) reads this config at Start and
// re-reads Enabled and Channel on every poll tick so an operator can
// disable or change channel mid-run without restart.
//
// Defaults: Enabled=false (opt-in), Channel="stable", PollInterval=24h.
// LoadConfig replaces empty Channel with "stable" and zero PollInterval
// with 24h; unknown Channel values are clamped to "stable" with a
// slog.Warn. A fresh install does not contact GitHub until the operator
// explicitly flips Enabled=true.
type UpdateConfig struct {
    Enabled      bool     `json:"enabled"`
    Channel      string   `json:"channel"`
    PollInterval Duration `json:"poll_interval"`
}

// Channel string constants — exported so callers and tests can reference
// the recognized values without string-literal duplication.
const (
    ChannelStable     = "stable"
    ChannelPrerelease = "prerelease"
)
```

## 2. Audit event string `auto_update_install` (DEFERRED — see status below)

**STATUS: deferred to a follow-up spec.** The original draft of this file claimed `AuditStore.Append` accepts a free-form `event string` — that was wrong. The actual `internal/telemetry/audit.go` table is purpose-built for drain-mode transitions (`prev_state`/`new_state` int columns, no `event` or `metadata` columns), and a clean integration for `auto_update_install` requires a separate decision: extend `AuditRecord` with optional event+metadata fields, add new columns to the audit table, or stand up a sibling `events` table. None of those decisions belong inside the 010 PR. v1 records version transitions via `slog.Info` only — durable for 7 days via the daily file-log rotation. A follow-up spec will revisit durable persistence once the audit-table redesign is decided.

## 3. In-memory state (not persisted)

The updater Subsystem holds these internally:

- `etag string` — last `ETag` returned by GitHub on a 200 response. Sent as `If-None-Match` on the next request. Empty on Start.
- `consecutiveFailures int` — count of poll attempts (DNS error, HTTP error, 5xx response) since the last success. Drives the backoff state machine (FR-012). Reset to 0 on every successful 200 or 304.
- `nextPollAt time.Time` — wall-clock time of the next scheduled poll. Set to `now + delay` after each tick where `delay = max(pollInterval ± jitter, backoffFor(consecutiveFailures))`. Read by an internal ticker.
- `client *http.Client` — single `Timeout: 30s` client per Subsystem.

None of these survive a service restart. FR-015 documents the explicit choice to keep the persistence surface zero.

## 4. GitHub releases JSON (consumed but not stored)

We depend on the subset of `/repos/.../releases/latest` documented in [contracts/github-releases-api.md](./contracts/github-releases-api.md). The JSON body is parsed into a temporary struct, the relevant fields are extracted (`tag_name` and the matching asset's `browser_download_url`), and the body is discarded. No part of this JSON is persisted or logged at full fidelity (we log just `tag_name` and the chosen download URL).

## 5. Temp-file path for the downloaded MSI

- **Location**: `os.TempDir() + "drainctl-update-<random>.msi"` where `<random>` is a `crypto/rand` 8-byte hex string. (Not `time.Now().UnixNano()` — predictable filenames are an attack surface for a privileged process.)
- **Lifetime**: From the start of the streamed download to the post-install cleanup. Deleted on every error path (download failure, signature verification failure, msiexec spawn failure). On a successful spawn, the file persists until msiexec finishes consuming it; we do NOT delete it ourselves because msiexec may still be reading. The OS reclaims the temp dir on next reboot if msiexec leaves it behind.
- **ACL**: Inherits the temp directory's ACL. Writable by the service account (SYSTEM); not readable by other unprivileged users. We do not tighten further — the file is in-flight only during the install window.

## What is NOT in the data model

- No `last_poll_at` field on Config. (FR-015.)
- No release-notes cache, no asset-list cache, no install-log table.
- No `update_pending: bool` flag — there is no "pending" state; the updater either spawns msiexec immediately upon decision or it doesn't.
- No new ETW event IDs in `internal/etwids` for updater events. The slog signals are sufficient for operator visibility in v1; ETW IDs are reserved for state transitions the dashboard's event stream cares about. (A future durable-audit follow-up may add ETW IDs alongside the audit-row write — see §2.)
