# Contract: Dashboard Register Fingerprint Refusal

## Scope

Defines the CLI and service behavior when `drainctl register` (or the service's
background re-register path) encounters a dashboard TLS fingerprint that differs from
the one already stored in `config.json`.

## Preconditions

- `DashboardConfig.TLSFingerprint` — existing field, SHA-256 hex string or empty.
- `regResult.TLSFingerprint` — fingerprint returned by the dashboard during the
  register handshake.
- `DashboardConfig.AutoPin` — existing bool, unchanged by this contract.

## Decision matrix

| Saved fingerprint | Offered fingerprint | `AutoPin` | Outcome |
|-------------------|---------------------|-----------|---------|
| empty | empty | any | Existing behavior: register continues without pinning. |
| empty | non-empty | `true` | Existing behavior: save offered fingerprint (TOFU pin). |
| empty | non-empty | `false` | Existing behavior: register continues; operator must set fingerprint manually. |
| non-empty | empty | any | Existing behavior: saved fingerprint retained; no change. |
| non-empty | non-empty (equal) | any | Existing behavior: no change; register continues. |
| **non-empty** | **non-empty (differ)** | **any** | **FR-001: register FAILS with mismatch error; config unchanged.** |

Only the bottom row is new behavior.

## Error shape (mismatch path)

### CLI (`cmd/drainctl/register_cmd.go`)

- `runRegister` returns `error` whose `.Error()` string contains the literal substring
  `fingerprint mismatch`.
- Message format:
  ```
  dashboard fingerprint mismatch: saved=<saved-hex>  offered=<offered-hex>
  refusing to overwrite. Clear Dashboard.TLSFingerprint in config.json before re-registering.
  ```
- Exit code: non-zero. Cobra converts the returned error to a non-zero exit.
- `config.json` MUST NOT be modified on this path.

### Service (`internal/svc/handler.go` `registerWithDashboard`)

- Function returns `false`.
- `slog.Error("dashboard=fingerprint-mismatch", "saved", savedHex, "offered", offeredHex)`.
- `dashCfg.TLSFingerprint` MUST remain unchanged.
- Dashboard client MUST NOT be re-initialized with the offered fingerprint.
- Subsequent auto-retry schedule is unchanged — the service will keep trying, and keep
  logging the mismatch, until the operator intervenes.

## Testing contract

- **CLI unit test** (`cmd/drainctl/register_cmd_test.go` subtest):
  stub a Register function that returns `TLSFingerprint: "B"` while stored config has
  `TLSFingerprint: "A"`. Assert:
  1. `runRegister` returns non-nil error.
  2. Error message contains `fingerprint mismatch`.
  3. On-disk `TLSFingerprint` (read via a fresh `LoadConfig`) equals `"A"`.
- **Service unit test**: factor the fingerprint-compare into a small testable helper
  (`shouldRefuseFingerprint(saved, offered string) bool` or similar). Table test
  covering all six decision-matrix rows. Assert the mismatch branch does not mutate
  `dashCfg.TLSFingerprint`.
- **Manual walkthrough**:
  1. Fresh install. Run `drainctl register https://dash.example` — succeeds, fingerprint pinned.
  2. Rotate the dashboard cert (new self-signed cert on the dashboard host).
  3. Run `drainctl register https://dash.example` again — assert error contains
     `fingerprint mismatch`, both hexes in the output, and that `config.json`'s
     `TLSFingerprint` field is the original (rotation 1) value.
  4. Manually edit `config.json` to clear `TLSFingerprint`. Re-run register — succeeds
     and re-pins to the rotated cert.

## Compatibility notes

- Pre-009 behavior silently re-pinned on any re-register. Operators who relied on this
  for automatic rotation MUST now clear the stored fingerprint before re-register.
  Documented in release notes.
- `AutoPin=true` no longer implies "accept any fingerprint the server offers";
  it now means only "auto-pin on first contact (when saved is empty)."
