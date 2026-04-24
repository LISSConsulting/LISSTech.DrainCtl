# Codex Full-Codebase Review — Synthesis (2026-04-24)

**Scope:** full codebase on branch `008-sqlite-chart-consumers` (clean). Three parallel
codex.exe lenses: correctness, security, design. 17 raw claims. Every claim verified
against the code at HEAD (`d7257aa`).

Severity legend: **BLOCKER** (fix before next release), **HIGH** (fix this cycle),
**MEDIUM** (next sprint), **LOW** (nice-to-have).

---

## Confirmed issues

### Correctness

**C-A1 — evtspike: `loss` callback never invoked (HIGH)**
- `internal/evtspike/subscriber_windows.go:37,80`
- The Subscribe doc (lines 33–36) promises `loss` is called when EvtNext reports a
  terminal error so the supervisor can re-subscribe. The implementation at line 80
  discards the error return (`r, _, _ := procEvtNext.Call(...)`) and only
  `break`s on `r == 0 || returned == 0`. A broken subscription silently stops
  counting — the outer loop falls back to a 1s `WaitForSingleObject` wait that
  fires forever on a dead handle and the supervisor never fires a re-sub.
- **Evidence from code:** the `loss` parameter is accepted but never referenced in
  the function body. `grep -n 'loss(' internal/evtspike/subscriber_windows.go`
  returns nothing.
- **Fix direction:** call `loss(e)` and `return` when `r == 0 && windows.GetLastError() != ERROR_NO_MORE_ITEMS` (or equivalent transition/invalid-handle codes), then let the supervisor restart Subscribe.

**C-A2 — Config read-modify-write race across `Update*` helpers (HIGH)**
- `config.go:806, 818, 831, 844, 858` — all five scoped updaters call
  `LoadConfig()` then `saveConfigToFile()`.
- The named-mutex inside `saveConfigToFile` only serializes the write; the
  Load-then-Save is not atomic. Two concurrent dashboard handlers (or a CLI
  and a dashboard) can both Load, both mutate, and the second Save overwrites
  the first Save's intended delta.
- **Fix direction:** move the named-mutex acquisition up into the `Update*`
  helpers (or into a shared `readModifyWrite(f func(*Config) error)` helper) so
  the lock brackets the whole RMW cycle.

**C-A3 — Named-pipe request path ignores `ERROR_MORE_DATA` (HIGH)**
- `internal/pipe/pipe.go:100-103` — request side does a single `conn.Read(buf)`
  into a 4096-byte buffer and treats any error as fatal.
- The response path (`readPipeResponse`) already loops on ERROR_MORE_DATA. The
  pipe is created in message mode with 64KB buffers (`conn_windows.go:59,61,62`),
  so any request ≥4KB is silently truncated — the `register` command carries a
  dashboardURL that could trigger this, and any future verb with larger payloads
  is a latent bug.
- **Fix direction:** extract the response-side ERROR_MORE_DATA loop as a shared
  `readPipeMessage(conn)` helper and use it on both sides.

**C-A4 — Registry-change attribution uses wall-clock, not event SystemTime (MEDIUM)**
- `internal/watcher/evtsubscribe.go:263` stamps `Timestamp: time.Now()` on the
  attribution, and `WaitAttribution` at line 171 compares against `after` using
  that wall-clock stamp.
- Security event 4657 delivery is not strictly real-time (audit log can buffer),
  so a delayed older event arriving after `after` would be accepted and the
  wrong `ChangedBy` user attached to the current transition.
- **Fix direction:** parse and use `<TimeCreated SystemTime=...>` from the event
  XML (already populated by the XML parser). Set `Timestamp` from the event,
  not `time.Now()`.

### Security

**C-B1 — SMTP STARTTLS downgrade + cleartext AUTH PLAIN (HIGH, BLOCKER-candidate)**
- `email.go:326,332`
- `if ok, _ := c.Extension("STARTTLS"); ok { ... StartTLS ... }` — a MITM that
  strips STARTTLS from the EHLO response causes the code to proceed directly to
  `c.Auth(smtp.PlainAuth(...))` over plaintext, leaking SMTP credentials.
- **Fix direction:** when `target.Secret != ""`, require TLS. Either (a) fail if
  STARTTLS is not advertised, or (b) insist on `smtps://` for auth'd targets.
  At minimum: check `c.Extension("STARTTLS")` result and fail-closed when auth
  is present.

**C-B2 — Webhook/ntfy SSRF via URL-only validation (MEDIUM)**
- `notify.go:390,580`, `internal/dashboard/server.go:748,930`
- `sendWebhook`/`sendNtfy` pass the user-supplied URL straight to `httpClient.Do`
  with no RFC1918/metadata-IP screen. `handleNotifyTest` lets an authenticated
  dashboard operator test arbitrary URLs; `handlePutSettings` persists them.
- Impact is bounded by NTLM auth (only admin-group members can reach these
  handlers), so this is lateral/defence-in-depth not remote-unauthenticated —
  but it's the kind of bug that turns a credential theft into fleet-pivot.
- **Fix direction:** resolve the URL host, reject private/link-local ranges
  (`169.254.0.0/16`, `127.0.0.0/8`, `10/8`, `172.16/12`, `192.168/16`, `::1`,
  `fc00::/7`) unless an explicit allowlist flag is set. Block `file://`, `ftp://`,
  `gopher://`, only permit `http(s)://` (already partially done via scheme check).

**C-B3 — Cross-service secret exposure: SERVICE SID + no-entropy DPAPI (HIGH)**
- `config.go:768-772` grants `S-1-5-6` (SERVICE, the local-services group) Modify
  to `config.json`. Every service on the host — including third-party services —
  can read and overwrite the file.
- `dpapi.go:40-48, 71-79` use `cryptprotectLocalMachine` with `0` entropy. Any
  process on the machine (certainly any service) can `CryptUnprotectData` the
  ciphertext.
- Composed: any service on the host can read drainctl's config and decrypt the
  webhook/SMTP secrets without any extra authorization.
- **Fix direction:** (a) drop SERVICE from the ACL, grant only `SYSTEM:(F)` and
  `Administrators:(F)`; the service runs as LocalSystem so it doesn't need the
  SERVICE grant. (b) Pass a non-empty entropy blob (e.g., a fixed 32-byte
  constant stored in the binary) to `CryptProtectData`/`CryptUnprotectData` —
  this doesn't defeat a determined attacker but eliminates drive-by cross-service
  decryption.

**C-B4 — Named-pipe server: no caller-identity check (MEDIUM, defence-in-depth)**
- `internal/pipe/conn_windows.go:56-65` — `CreateNamedPipe` passes `nil` for the
  security descriptor, relying on the default DACL inherited from the creator
  token.
- `internal/pipe/pipe.go:93-162` accepts `register`, `remove-server`,
  `baseline-reset` with no post-connect SID check.
- Under the current default DACL the pipe is already restricted, but any future
  hardening regression (e.g., someone adding a wider SDDL) silently lowers the
  bar because the server has zero defence-in-depth.
- **Fix direction:** (a) pass an explicit SDDL that grants only
  `SYSTEM`+`Administrators`+`SERVICE` (the dashboard/service caller chain), and
  (b) in `handlePipeConn`, call `GetNamedPipeClientProcessId` / impersonate and
  verify the caller SID is admin or SYSTEM before accepting privileged verbs.

**C-B5 — HTML email renders notification fields via text/template (LOW-MEDIUM)**
- `email.go:14` imports `text/template`; `email.go:21` builds the template with it.
  `email_template.html:85,197,199,126,138,...` embeds `{{.Subject}}, {{.Message}},
  {{.Host}}, {{.ChangedBy}}, {{.SpikeChannel}}, ...` into HTML without escaping.
- Most fields are constrained (Mode is enum, Host is hostname). But `Message`,
  `SpikeChannel`, `ChangedBy` (from Windows event log `SubjectUserName`) carry
  user-ish values; a crafted event channel name or registry audit subject could
  inject HTML into an admin's inbox.
- **Fix direction:** import `html/template` and swap `template.Must(template.New...)`
  for the html equivalent. No other changes needed — same API, correct
  contextual escaping.

**C-B6 — Dashboard client: InsecureSkipVerify until first pin (HIGH)**
- `internal/dashboard/client.go:29-32,72-74`
- `newDashClient("")` unconditionally sets `InsecureSkipVerify: true`. The
  `init()` stores that client as the default. First registration — before any
  fingerprint is pinned — uses this client against the dashboard over HTTPS via
  Negotiate. A MITM can pin its own cert during TOFU.
- This is classic TOFU; it's a deliberate trade-off, but the window is
  exploitable and nothing alerts if the fingerprint ever changes.
- **Fix direction:** (a) document TOFU explicitly and require an out-of-band
  fingerprint supplied by `drainctl register --tls-fingerprint=<hex>` for
  production installs; (b) persist the observed fingerprint on first register
  and refuse silently-changed fingerprints on future registers (treat
  fingerprint change as an error, not a reset).

### Design

**C-C1 — Root `drainctl` package drifting into shared namespace (MEDIUM)**
- `drainctl.go:17` exposes ETW event-ID constants consumed only by `internal/svc`
  and `internal/dashboard` — the comment on line 18 literally explains they live
  here because two internal packages need them.
- `config.go:957 MigrateFromRegistry`, `config.go:1056 WriteDefaultParameters`,
  `eventlog.go:55` expose helpers with one or two internal callers each.
- The public surface is acting as an internal `shared` package.
- **Fix direction:** move the ETW IDs to a new `internal/etwids` (or
  `internal/audit`) package; move `MigrateFromRegistry` and `WriteDefaultParameters`
  to `internal/installer` or keep in root but clearly marked as installer-only
  via doc comment + `// Deprecated:` tag if the installer can't call internal/.

**C-C2 — Dead exported `Update*` helpers (HIGH for hygiene, LOW blast radius)**
- `config.go:806 UpdateNotifications`, `:818 UpdateSessionThreshold`,
  `:831 UpdateGracePeriod`, `:844 UpdatePerformanceConfig` are called only by
  their own tests. Production write path is `UpdateNotifySettings` (line 858).
- This matches the user's standing feedback: orphan exported functions are
  never "not my problem" — file, delete, or verify ticket.
- **Fix direction:** delete `UpdateNotifications`, `UpdateSessionThreshold`,
  `UpdateGracePeriod`, `UpdatePerformanceConfig` and their tests. Keep
  `UpdateNotifySettings` as the sole public write path. If any of them is a
  genuinely-planned future API, add a doc comment saying so.

**C-C3 — `internal/svc/handler.go` god-file (MEDIUM)**
- 1138 lines. Mixes pipe RPC handlers, dashboard registration/config sync, perf
  collector lifecycle, evtspike reloads, service startup.
- **Fix direction:** split into
  `handler_pipe.go` / `handler_dashboard.go` / `handler_perf.go` / `handler_evtspike.go` / `handler_lifecycle.go` (or
  promote some to new `internal/svc/{pipe,perfsync,spikesync}` subpackages).
  Not urgent, but the next bug fix in this file will touch three unrelated
  subsystems. L-effort.

**C-C4 — `App.svelte` poll vs SSE duplication (MEDIUM)**
- `frontend/src/App.svelte:192-216,284-302,386-411,414-431`
- Status-transition logging and per-host sparkline append appear in two
  orchestration paths. Any future semantic change (new metric dimension, new
  event type) must be applied in both.
- **Fix direction:** extract two helpers in `frontend/src/lib/state.svelte.js`:
  `logStatusTransition(evtTime, prevStates, sv)` and `appendPerfToRingBuffer(sv, ts)`.
  Call from both the poll loop and the SSE handler.

**C-C5 — Stale "JSONL audit trail" public surface (MEDIUM)**
- `drainctl.go:37-40 DefaultAuditPath` still returns `audit.jsonl` path; the
  comment calls it "the default path for the JSONL audit trail".
- `history.go:25` acknowledges the lie in its doc: the path's parent directory
  is used and the filename ignored.
- Audit has been SQLite-only since feature 007; the JSONL trail is retired.
- **Fix direction:** rename to `DefaultDBPath()` returning `drainctl.db`;
  update `HistoryOptions.DBPath` doc; remove the "legacy JSONL" language.
  Callers: CLI `cmd/drainctl/main.go:45`, tests. S-effort.

**C-C7 — Orphan `deriveP95`/`deriveP50` in `state.svelte.js` (LOW)**
- `frontend/src/lib/state.svelte.js:425,432` — exported, not called anywhere
  in `frontend/src/`.
- **Fix direction:** delete both. If a future consumer needs them, re-add with
  the caller.

---

## Partial / downgraded

**C-C6 — `store.go` mixing settings-projection with server roster (LOW)**
- `internal/dashboard/store.go:193 GetSettings` reshapes config into a DTO
  inside the same file as the server-roster store. Codex flagged this as
  weak cohesion.
- In practice `GetSettings` is small and pure projection; splitting it into a
  new `settings.go` file in the same package is fine but not load-bearing.
- **Fix direction:** optional — move to `internal/dashboard/settings.go` when
  next touching that area. No standalone ticket warranted.

---

## Rejected / cleanly passed

Codex produced no findings that had to be rejected outright. Negative findings
it annotated (confirmed clean):
- SMTP header injection — mitigated by `sanitizeHeader`.
- DPAPI failure-path plaintext fallback — not present; on failure the secret is
  dropped, not written in the clear.
- Missing `//go:build windows` — zero violations.
- `structuredClone()` on Svelte `$state` — not present.
- `viper` re-introduction — not present.
- Dashboard cookie flags: `HttpOnly`, `SameSite=Strict`, `Secure` all set.
- Path traversal in static-asset handlers — not present (embed.FS only).
- `govulncheck ./...` — clean.
- SQL injection — queries parameterized; only internal-constant table-name
  selection uses string interpolation (not attacker-controlled).
- `.gitignore` — covers `*.out`, `*.cov`, coverage HTML.

---

## Meta

- Codex (all three lenses) grounded every finding in real file:line citations.
  No hallucinated paths. One finding (B4 pipe ACL) appropriately downgraded to
  medium because runtime DACL couldn't be observed from the source alone.
- Largest gap the external review surfaced that in-house work had missed:
  **C-A1 (evtspike loss callback never invoked)** — the function's doc
  advertises a recovery path its implementation doesn't deliver. That's a
  subtle comment-vs-code mismatch easy to miss without a targeted read.
