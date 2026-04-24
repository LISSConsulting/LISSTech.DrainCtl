# Research: Security and Correctness Hardening (009)

All major scope decisions for this feature were made interactively during the codex
review synthesis (2026-04-24) and recorded in
`docs/reviews/codex-2026-04-24-fullcodebase-remediation-plan.md`. This file captures the
decisions that materially shaped the FR list, so `/speckit.tasks` can reference the
alternatives without re-reading the review transcript.

## Decision 1: TOFU remains acceptable; only harden after-pin tamper detection

- **Decision**: Keep the existing trust-on-first-use behavior for dashboard fingerprint
  pinning on first register. Add a mismatch-refusal branch to both CLI
  (`cmd/drainctl/register_cmd.go`) and service (`internal/svc/handler.go`
  `registerWithDashboard`) so that a non-empty saved fingerprint is immutable through
  the automatic re-register path.
- **Rationale**: Dashboard certs in this product are overwhelmingly self-signed by
  design (on-prem internal tools, no PKI dependency). Requiring system-root verification
  for the first-contact client would turn every agent deployment into a two-step
  operation (operator paste fingerprint out-of-band OR import cert to Windows trust
  store on every machine). The real exploitation window a pinned-fingerprint refusal
  closes is *multi-year*: silent cert swap post-deployment, where nothing alerts today.
  Narrow first-contact MITM protection costs permanent onboarding friction; after-pin
  tamper detection costs nothing once pinned.
- **Alternatives considered**:
  - Full-scope Step 1 (widen `newDashClient` + system-roots verification + `--tls-fingerprint` flag + `DRAINCTL_ALLOW_UNPINNED_DASHBOARD` escape hatch): rejected — L-effort opener with permanent onboarding tax.
  - Do nothing (full TOFU, silent re-pinning): rejected — leaves the multi-year silent-swap window open.
  - Require explicit `--auto-pin` even on first register: rejected — makes the common deploy flow worse without the operational tradeoff the pin-change refusal already delivers.

## Decision 2: Greenfield DPAPI — no migration, no fallback

- **Decision**: Add a fixed 35-byte entropy constant (`LISSTech.DrainCtl/v1/notify-secret`)
  as the `pOptionalEntropy` arg to `CryptProtectData`/`CryptUnprotectData` in `dpapi.go`.
  Do **not** implement a `decryptWithFallback` migration helper. Do **not** add a
  `LoadConfig` writeback step. Do **not** deep-copy the plaintext-bearing cfg before
  re-encryption.
- **Rationale**: Zero deployed agents hold legacy zero-entropy ciphertext. The migration
  machinery proposed in the original L-effort step was load-bearing *only* if operators
  existed whose secrets would otherwise blank on first load — they don't. The product is
  greenfield and this change must ship before first external release (captured in
  Assumption 1 of the spec).
- **Alternatives considered**:
  - Full migration writeback with `decryptWithFallback` + deep-copy re-encrypt + lock-
    holding raw save: rejected — complexity without benefit in a greenfield product.
  - Ship without entropy ("not exploitable today"): rejected — leaves cross-service
    DPAPI decrypt open by any SYSTEM/SERVICE process on the host, which is the
    documented threat (codex C-B3).
  - Use a random per-install entropy stored in the registry: rejected — buys nothing
    over a fixed constant against the in-scope threat, and adds install-time state.

## Decision 3: On-prem LAN webhooks are legitimate — no general SSRF guard

- **Decision**: Drop the general RFC1918 / link-local / ULA SSRF block from the
  proposed Step 8. Keep only a single literal rejection of `http://169.254.169.254`
  (cloud metadata IP) at the notify send-time and dashboard-UI save/test paths.
  No allowlist config field. No `internal/neturl` package. The existing scheme block
  (reject `file://`, `ftp://`, `gopher://`) stays in place.
- **Rationale**: Drainctl is an on-prem Windows Server product. LAN webhooks — internal
  Slack bridges, internal ntfy, internal Mattermost, internal monitoring — are the
  *primary legitimate use case*, not the exception. An RFC1918 block would break more
  operators than it would protect from a threat (authenticated-admin-to-private-IP
  pivot) that a compromised dashboard admin can already exceed via dozens of other
  paths. The `169.254.169.254` carveout is a cheap hedge against a future cloud-hosted
  build where the exploitation payoff (IAM credential exfiltration) is disproportionate
  to the check's cost.
- **Alternatives considered**:
  - Full SSRF guard with allowlist config: rejected — breaks common deployments; cost
    far exceeds benefit in the documented threat model.
  - Drop Step 8 entirely: considered; rejected because the metadata IP check is a
    single-line, high-signal, zero-friction guard against a specific future risk.
  - Also block `::1`/`127.0.0.0/8`: rejected — loopback notification targets are
    plausible for development and mock-server workflows.

## Decision 4: SMTP AUTH requires encrypted transport (STARTTLS or smtps://)

- **Decision**: When an SMTP notification target has a non-empty `Secret`, drainctl
  MUST refuse to send `AUTH` over a connection that has not completed a successful
  TLS handshake. Both STARTTLS (port 587 flow) and implicit TLS (`smtps://`, port 465)
  satisfy this requirement. For unauthenticated internal relays (empty `Secret`),
  opportunistic behavior is preserved with a `slog.Warn` naming the host.
- **Rationale**: A MITM that strips the STARTTLS line from the SMTP server's EHLO
  response causes the existing code to fall through to `c.Auth(smtp.PlainAuth(...))`
  over cleartext, leaking credentials reusable against whatever that relay touches.
  STARTTLS is not rare in 2026; port-587 authenticated submission is the dominant
  flow, and implicit TLS on 465 covers the remainder. Refusing AUTH on a cleartext
  session is the correct default.
- **Alternatives considered**:
  - Require `smtps://` only, deprecate STARTTLS support: rejected — STARTTLS is still
    the mainstream flow; refusing it would break more deployments than necessary.
  - Leave STARTTLS optional, log a warning when it fails: rejected — credentials
    would still leak in the silent-downgrade scenario.
  - Refuse even unauthenticated cleartext sends: rejected — plain internal relays
    (e.g., mailhog, internal Postfix on port 25 with no auth) are a legitimate use
    case; the threat addressed is credential leak, not content leak.

## Decision 5: Default pipe DACL is sufficient; SID check is per-verb defense-in-depth

- **Decision**: Do **not** narrow the named-pipe DACL to SYSTEM+Admins+SERVICE.
  Instead, keep the default DACL and add a per-verb authorization check inside
  `handlePipeConn` that verifies the caller SID (via `GetNamedPipeClientProcessId` +
  `OpenProcessToken` + `GetTokenInformation`) is `SYSTEM` or an Administrators group
  member before accepting `register`, `remove-server`, or `baseline-reset`. Read-only
  verbs (`status`, `history`, `servers`) remain accessible to any caller the OS
  allowed past the pipe DACL.
- **Rationale**: Narrowing the DACL would break existing non-admin CLI usage of the
  read-only verbs because the connection is rejected before any handler code runs.
  The default DACL on a SYSTEM-owned pipe is already restrictive in practice; the
  caller-SID check is defense-in-depth against a future DACL regression (someone
  relaxing the pipe SDDL without noticing it lowers the privilege bar silently).
- **Alternatives considered**:
  - Narrow DACL to SYSTEM+Admins+SERVICE: rejected — breaks non-admin read-only CLI.
  - Keep DACL, no per-verb check: rejected — leaves all pipe verbs at whatever the
    DACL default happens to grant; brittle.
  - Per-verb check + narrow DACL: rejected for now; flagged as a possible follow-up
    if a future audit shows interactive non-admin users can connect. Staging them
    separately preserves the ability to revert the DACL change without losing the
    per-verb guard.

## Decision 6: Event `SystemTime` is authoritative; wall-clock is fallback only

- **Decision**: `processEvent` in `internal/watcher/evtsubscribe.go` parses
  `evt.System.TimeCreated.SystemTime` via `time.Parse(time.RFC3339Nano, …)` and uses
  the parsed time as the `RegistryChangeAttribution.Timestamp`. On parse error, fall
  back to `time.Now()` and emit `slog.Warn("evtspike: unparseable SystemTime", "raw", raw)`.
- **Rationale**: Windows audit event 4657 delivery is not strictly real-time —
  buffered audit logs can deliver older events after a newer registry change has
  already occurred. Stamping every delivery with `time.Now()` risks attaching the
  wrong user to a transition. The event payload's own `SystemTime` is the canonical
  source; wall-clock is a last-resort fallback that preserves behavior on parse
  failure rather than dropping attribution entirely.
- **Alternatives considered**:
  - Use `time.Now()` when parse fails *without* a warn: rejected — silent data
    quality regression.
  - Drop attribution on parse failure: rejected — worst user outcome; better to
    attribute with wall-clock and log than to have no `ChangedBy` at all.
  - Add a strict mode where parse failure returns an error: rejected — no operator
    benefit; the event is already in hand and the caller can't do anything useful
    with a parse-failure error here.

## Decision 7: Four `Update*` helpers are dead code

- **Decision**: Delete `UpdateNotifications`, `UpdateSessionThreshold`,
  `UpdateGracePeriod`, `UpdatePerformanceConfig` and their dedicated tests. Keep
  `UpdateNotifySettings` as the sole public write path for notification config
  changes. Must land *after* Phase 2 Step 6 (which rewrites them onto the new
  `readModifyWrite` helper) to avoid merge churn.
- **Rationale**: Grep confirms the only callers of these four exports are their own
  tests. The user's standing feedback explicitly rejects orphan exported functions
  as "not my problem" — file, delete, or verify ticket. There's no ticket.
- **Alternatives considered**:
  - Keep the helpers in case "someone might need them": rejected — matches exactly
    the pattern the user has called out as a codebase tax.
  - Lowercase them to make unexported: rejected — the test coverage they provide
    disappears when tests disappear, and internal use of `UpdateNotifySettings`
    covers the RMW path for all real writers.

## Decision 8: Module boundaries — `internal/etwids/` and `handler.go` split

- **Decision**: Move ETW event-ID constants out of root `drainctl` into
  `internal/etwids/`. Split `internal/svc/handler.go` (1138 lines) into topically
  coherent subsystem-named files: `service.go` (Service struct + RunService + Windows
  service control-handler shim), `piperpc.go` (pipe request dispatch + per-verb
  handlers), `dashsync.go` (dashboard registration + periodic config pull),
  `perfsupervisor.go` (perf collector start/stop supervisor), `spikesupervisor.go`
  (evtspike config-reload loop). No new packages beyond `internal/etwids/`; no
  signature changes.
- **Rationale**: Root package was drifting into a shared-helpers namespace (codex C-C1);
  `handler.go` mixes pipe RPC, dashboard sync, perf collection, evtspike reloads, and
  service startup in one file where the next bug fix in any one subsystem will touch
  three others unrelated to it. Both are pure hygiene with no behavior impact.
- **Naming discipline**: the original plan proposed `handler_<subsystem>.go` filenames.
  Rejected during implementation: the `handler_` prefix is dead weight (every file in
  `package svc` is a "handler"), and `handler_lifecycle.go` would have fought with
  `handler.go` over the same subject (Windows service startup *is* the service run loop).
  Final names use subsystem nouns directly and collapse the lifecycle file into
  `service.go`.
- **Alternatives considered**:
  - Promote split handler pieces to new subpackages (`internal/svc/piperpc/`,
    `internal/svc/dashsync/`, etc.): **deferred to a later feature branch.** Would
    force promoting unexported symbols across a new package boundary, significantly
    widening the 009 diff and blending two distinct refactors. Revisit once the
    in-package split has settled.
  - Keep ETW IDs in root: rejected — they're only consumed by two internal packages
    and the comment at `drainctl.go:17` already concedes this is a layout smell.
  - Leave `handler.go` monolithic: rejected — cumulative tax on every future fix.
  - Use `handler_*.go` prefix: rejected — see Naming discipline note above.
