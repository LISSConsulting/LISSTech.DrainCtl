# Data Model: Security and Correctness Hardening (009)

This feature modifies existing runtime structures rather than introducing new persisted
entities. The entries below capture the **contract-level changes** to each affected
struct, constant, or configuration surface — what the implementation tasks must honor.

## 1. DashboardConfig.TLSFingerprint (behavioral contract change)

- **Location**: `config.go` — existing field on the `DashboardConfig` struct.
- **Type**: `string` (SHA-256 hex of the dashboard TLS certificate, empty when unpinned).
- **Existing behavior**: Populated on first successful `register`; overwritten on every
  subsequent register when `Dashboard.AutoPin == true` or implicitly when the server
  offers a fingerprint the client does not have one for.
- **New behavior (FR-001)**: Once non-empty, the value is immutable via the automatic
  register path. Both `cmd/drainctl/register_cmd.go` and `internal/svc/handler.go`
  `registerWithDashboard` MUST compare saved vs. offered fingerprint; if both are
  non-empty and they differ, the operation returns an error and does NOT modify the
  on-disk config.
- **Unchanged**: First-register behavior (empty → offered) still pins automatically if
  `Dashboard.AutoPin == true` (or via existing implicit pinning path, unchanged).
- **Rotation contract**: Legitimate cert rotation requires the operator to manually
  clear the field in `config.json` before re-registering. Documented in release notes.

## 2. DPAPI entropy constant (new package-level value)

- **Location**: `dpapi.go` — new unexported `var dpapiEntropy = []byte("LISSTech.DrainCtl/v1/notify-secret")`.
- **Type**: `[]byte` (35 bytes).
- **Purpose**: Passed as `pOptionalEntropy` to `CryptProtectData` and `CryptUnprotectData`
  so ciphertext produced by drainctl cannot be decrypted by another process on the same
  host running as SYSTEM or SERVICE without knowing the same constant.
- **Not a secret**: The constant is embedded in the binary and in source. Its protective
  value is scoping cross-process DPAPI decrypts, not cryptographic opacity.
- **Lifetime**: Process-constant. Version-qualified (`v1`) so a future rotation can
  introduce `v2` alongside.

## 3. NotifyTarget.URL (validation unchanged from pre-009)

- **Location**: `config.go` — existing field on `NotifyTarget`.
- **Type**: `string` (HTTP(S) URL for webhook / ntfy; SMTP server for email).
- **Validation**: existing scheme check (`http`, `https`, `smtp`, `smtps`) stays. No new
  validation rule under 009. An earlier 009 revision proposed an FR-009
  cloud-metadata IP rejection (literal `169.254.169.254`); withdrawn during 009 codex
  post-review as bypass-prone. See `docs/reviews/codex-2026-04-24-009-branch-remediation.md`
  Step 1 for the full reasoning.

## 4. NotifyTarget.Secret — transport requirement for SMTP AUTH

- **Location**: `config.go` — existing field on `NotifyTarget`.
- **Type**: `string` (DPAPI-encrypted on disk; plaintext after `DecryptSecrets`).
- **Existing behavior**: When non-empty on an SMTP target, `email.sendSMTPStartTLS`
  opportunistically attempts STARTTLS and falls through to `c.Auth(smtp.PlainAuth(...))`
  whether or not STARTTLS completed successfully.
- **New behavior (FR-002)**: When non-empty AND the server either does not advertise
  STARTTLS or STARTTLS fails, drainctl MUST refuse to transmit `AUTH` and return a
  specific error naming the host. `smtps://` scheme (implicit TLS) remains supported.
- **Unchanged (FR-003)**: When empty (unauthenticated relay), opportunistic behavior
  is preserved with a `slog.Warn` naming the host when STARTTLS is unavailable.

## 5. Pipe verb privilege classes (new authorization contract)

- **Location**: `internal/pipe/pipe.go` — `handlePipeConn` — new SID check branch.
- **Classes**:
  - **Read-only** (unchanged): `status`, `history`, `servers`. Any OS-permitted caller.
  - **Privileged** (new gate): `register`, `remove-server`, `baseline-reset`. Caller SID
    MUST be `S-1-5-18` (LocalSystem) or an `S-1-5-32-544` (Administrators) group member
    per `windows.Token.IsMember`.
- **Check implementation** (FR-010): `GetNamedPipeClientProcessId` →
  `windows.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION)` → `windows.OpenProcessToken` →
  `Token.IsMember(adminSID)` plus LocalSystem SID compare.
- **Deny shape**: Return `PipeResponse{OK: false, Error: "access denied"}` and emit
  `slog.Warn("pipe=access_denied", "cmd", req.Cmd, "sid", sidStr)` plus an
  `EvtAccessDenied` (event ID 5004, already defined) audit event.

## 6. RegistryChangeAttribution.Timestamp (provenance change)

- **Location**: `internal/watcher/evtsubscribe.go` — existing struct.
- **Type**: `time.Time`.
- **Existing behavior**: Stamped to `time.Now()` when `processEvent` records the
  attribution.
- **New behavior (FR-012)**: Parsed from `evt.System.TimeCreated.SystemTime` via
  `time.Parse(time.RFC3339Nano, …)`. On parse failure, fall back to `time.Now()` and
  emit `slog.Warn("evtspike: unparseable SystemTime", "raw", raw)`.
- **Consumer impact**: `WaitAttribution` already compares against this field; the
  existing `after` parameter semantics are preserved but newly accurate for delayed
  audit-log deliveries.

## 7. HistoryOptions.DBPath (semantic widening)

- **Location**: `history.go` — existing field on `HistoryOptions`.
- **Type**: `string`.
- **Existing behavior**: Interpreted as "a file path whose parent directory is the
  telemetry store root." `GetHistory` calls `filepath.Dir(opts.DBPath)` and passes the
  parent to the telemetry store open call.
- **New behavior (FR-015)**: `GetHistory` calls `os.Stat(opts.DBPath)`. If the path is
  a directory, use it as-is; if it is a file, use its parent; if it is empty, default
  to `DefaultDBDir()`. This preserves every current call site (all pass file paths)
  while letting future callers pass directory paths directly.

## 8. Root package ETW event-ID exports (moved, not removed immediately)

- **Location (before)**: `drainctl.go:17` — exported constants `EvtDashboardAccess`,
  `EvtDashboardConfigChange`, `EvtServerRegistered`, `EvtServerRemoved`,
  `EvtAccessDenied`, `EvtGenericAudit`.
- **Location (after)**: `internal/etwids/etwids.go` — same names, same values,
  `internal` package.
- **Consumer impact** (FR-014): `internal/svc` and `internal/dashboard` update their
  imports. No external consumer depends on these constants; the DLL (`cmd/cshared/`)
  does not reference them.

## 9. Root package public surface shrinkage (FR-013)

- **Removed exports**: `UpdateNotifications`, `UpdateSessionThreshold`,
  `UpdateGracePeriod`, `UpdatePerformanceConfig` — confirmed dead (only self-tests call
  them). Companion tests deleted.
- **Retained exports**: `UpdateNotifySettings` (primary write path),
  `UpdateEvtSpikeEnabled`, `InstallCertificate`.
- **Deprecation**: `DefaultAuditPath` becomes a `// Deprecated:` alias for
  `DefaultDBPath()`; hard removal scheduled for a later feature cycle.

## 10. Atomic config read-modify-write primitive (FR-007)

- **Location**: `config.go` — new unexported `readModifyWrite(f func(*Config) error) error`.
- **Purpose**: Brackets the full Load → mutate → Save cycle under the existing named
  mutex so two concurrent writers cannot interleave their reads and writes.
- **Refactored callers**: `UpdateNotifySettings`, `UpdateEvtSpikeEnabled`,
  `InstallCertificate`, plus (until Phase 4 deletes them) `UpdateNotifications`,
  `UpdateSessionThreshold`, `UpdateGracePeriod`, `UpdatePerformanceConfig`.
- **Implementation contract**: `LoadConfig` is split into an exported lock-taking
  wrapper and an unexported `loadConfigLocked` that the primitive uses; similarly for
  save. External callers of `LoadConfig` (read-only path) remain unchanged.
