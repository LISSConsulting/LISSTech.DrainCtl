# Codex 3-Lens Review — 009 Branch Synthesis (2026-04-24)

**Scope**: `009-security-hardening` vs `trunk` at `fcdbc9f`, 14 commits. Three parallel codex lenses: correctness, security, design/dead-code. 11 raw claims. Every claim verified against HEAD.

Severity: **BLOCKER** | **HIGH** | **MEDIUM** | **LOW**.

---

## Confirmed / actionable

### Correctness

**C-1 — `AuditPath` normalizer drops custom directory (MEDIUM)**
- `config.go:388-397` — the legacy-`audit.jsonl` tail match rewrites to `DefaultDBPath()`, discarding the parent directory. An operator-custom path like `D:\DrainCtl\audit.jsonl` silently becomes `%ProgramData%\...\drainctl.db`.
- Practical blast radius: low because no known code path persists a custom `AuditPath` (CLI `--db` is runtime-only; registry migration and Validate both write `DefaultDBPath()`). But the rewrite is still too aggressive — it should preserve the directory and only update the filename.
- Fix: `c.AuditPath = filepath.Join(filepath.Dir(c.AuditPath), "drainctl.db")`.

**C-2 — `seedServerMetrics` overwrites fresher live SSE samples (LOW, pre-existing)**
- `frontend/src/lib/state.svelte.js:823-831` — `next.set(host, samples.slice(-MAX_METRICS))` unconditionally replaces each host's ring. If the SSE `server_update` handler (App.svelte:406-420) appends a fresh perf sample before the seed promise resolves, the seed write clobbers it.
- Genuinely racy but **pre-009**: the unconditional overwrite semantic predates US5's helper extraction. Codex flagged it because the prompt pointed it at state.svelte.js; this is out-of-scope for 009 cleanup.
- Defer to FEATURES.md or BUGS.md.

**C-3 — Stale `detectorStatuses` / `recentSpikes` after ServerTable delete (LOW, pre-existing)**
- `frontend/src/components/ServerTable.svelte:183-185` — `doRemoveServer` clears `serverMetrics` via `removeServerMetrics(host)` but leaves `detectorStatuses` and `recentSpikes` maps populated. The seeding effect at :56-65 skips REST seed when `detectorStatuses.has(host)` is true, so a re-registered host shows stale detector data.
- Also pre-009. Defer.

### Security

**B-1 — Cloud-metadata check bypassable via redirect / IPv6 / numeric variants (HIGH, but threat model marginal)**
- `notify.go:36` — `httpClient = &http.Client{Timeout: 5 * time.Second}` sets no `CheckRedirect`, so Go follows 302/307 by default. Attacker-controlled URL `https://evil.example/redirect-to-metadata` 302s to `http://169.254.169.254/latest/meta-data/iam/...` and the client fetches it.
- `notify.go:34-43` (`rejectCloudMetadata`) — `u.Hostname() == "169.254.169.254"` is exact-string. Bypasses: `[::ffff:169.254.169.254]`, `3232266746` (decimal IPv4), `0xA9.0xFE.0xA9.0xFE` (hex), `169.254.169.254.` (trailing dot), or any hostname that resolves to the metadata IP.
- Real threat only materializes on cloud-hosted builds (on-prem product charter explicitly out-of-scope per research.md §Decision 3). But the 009 check is advertised as closing this gate and it doesn't.
- Two honest options:
  - **(a)** Drop the check entirely. 009's research explicitly framed it as "a cheap hedge against a future cloud-deployed build." If it can be bypassed, the hedge value is zero.
  - **(b)** Harden: set `CheckRedirect` on `httpClient` that re-validates the target host on each hop; parse `u.Hostname()` via `net.ParseIP` and canonicalize IPv6 mapped addresses; optionally reject any hostname that resolves to `169.254.0.0/16`.
- Recommend (a) — matches the product charter and the on-prem LAN-webhook carveout. Keep the scheme allowlist (`http`/`https` only).

**B-2 — Metadata check skips email targets (LOW, LAN-SMTP-to-metadata is not a real vector)**
- `internal/dashboard/server.go:830-835` — only `t.Type == "webhook" || t.Type == "ntfy"` gates the check. `email.go:sendEmail` doesn't call `rejectCloudMetadata` at all.
- SMTP against the cloud metadata endpoint is nonsensical (metadata is HTTP-only). No real exploitation.
- Consistency wart; if we drop B-1, this evaporates too.

**B-3 — `config.json` readable window + `icacls` fail-open (MEDIUM)**
- `config.go:727` writes the tmp file with `os.WriteFile(tmpPath, data, 0o600)` — but on Windows, Go's CreateFile ignores the mode bits; the tmp file inherits ACL from `%ProgramData%\LISS Technologies\LISSTech DrainCtl\`, which inherits from `%ProgramData%` (grants `Users` read-and-execute).
- After `MoveFileEx` (:734) the file sits at the final path with inherited ACL until `restrictConfigACL(path)` runs (:742). Window is small but real — a local Users-group process polling ProgramData can read notification secrets.
- Worse, `restrictConfigACL` (:766-776) runs `icacls` via `winexec.Command(...).Run()` and **swallows the error** (`_ = ...`). If icacls fails for any reason, the file keeps inherited ACLs permanently.
- Fix:
  - Apply `restrictConfigACL(tmpPath)` **before** `MoveFileEx`. NTFS rename preserves the source file's explicit ACL.
  - Surface `icacls` failures via `slog.Error` so operators can diagnose; retry once; optionally fail the save on persistent failure (with the named mutex held, a fail is safer than silently leaving secrets world-readable).

### Design / Dead code

**C-4 — Split-brain `slot_maturity_observations` default (MEDIUM)**
- `config.go:70` `DefaultEvtSpikeSlotMaturityObservations = 7`
- `internal/evtspike/detector.go:37` `defaultSlotMaturityObservations = 90` with comment explicitly calling 7 "far too permissive for an anomaly baseline."
- Detector only falls back to 90 when config passes 0; ClampEvtSpike promotes 0 → 7, so the effective default for service-constructed detectors is 7.
- Operator-visible mismatch. Pick one value. Recommend 90 (matches the detector comment's reasoning).

**C-5 — `spec.md` SC-011 misrepresents handler.go split status (LOW)**
- `specs/009-security-hardening/spec.md:208` — SC-011 claims `handler.go` drops to files under 400 lines. Actual status: T084-T086 deferred, file is still 1149 lines (pre-009: 1138).
- Spec success-criteria read as factual claims in retrospect. Update to reference F5.

**C-6 — README `--db` default shows legacy `audit.jsonl` (MEDIUM)**
- `README.md:213` — `--db` default documented as `%ProgramData%\...\audit.jsonl`.
- Actual: `cmd/drainctl/main.go:45` defaults to `DefaultDBPath()` → `drainctl.db`.
- Fix: update README table.

**C-7 — README evtspike config table is broadly wrong on defaults (HIGH for docs accuracy)**
- `README.md:557-568` defaults that don't match code:
  - `cooldown_minutes` says `15`, code is `10`.
  - `slot_maturity_observations` says `90`, code is `7` (this is what should BE 90 — see C-4).
  - `half_life_buckets` says `8640`, code is `360`.
  - `prior_strength` says `1.0`, code is `60.0`.
  - `mean_per_bucket_prior` says `0.5`, code is `0.1`.
- Plus the incorrect statement that `evtspike.enabled=false` means the subsystem is "never constructed" — it's constructed but quiescent.
- This is the operator's primary configuration reference. Fix.

**C-8 — Function-pointer package globals as test seams (LOW, accept)**
- `internal/evtspike/subscriber_windows.go:21-27` — six package-level function-pointer vars (`createEvent`, `closeHandle`, `waitForSingleObject`, `evtSubscribeCall`, `evtNextCall`, `evtCloseCall`) wired to `windows.*` only so tests can monkey-patch.
- Same pattern at `internal/pipe/pipe.go:56` (`callerIsPrivilegedFunc`).
- Production ships with mutable globals. Common Go test idiom (vs. interface-based DI). Accept for now; note that tests are responsible for cleanup.

---

## Rejected / cleanly passed

- **Fingerprint-mismatch bypass** — codex confirmed the pinning client fails mismatched HTTPS certs at transport time before the offered fingerprint is even read. No bypass.
- **STARTTLS refusal edge cases** — codex looked at partial-advertise scenarios and found no concrete hole.
- **html/template escaping** — codex didn't identify any un-escaped field.
- **DPAPI entropy** — codex noted the constant is in the binary (no opacity) but that's the documented threat model (cross-process scoping, not cryptographic opacity). As designed.
- **Pipe SID check** — codex noted the inherent PID-reuse race in any PID-based auth but didn't find a practical exploit given `GetNamedPipeClientProcessId` + immediate `OpenProcess`. Accept as inherent limitation.

---

## Meta

- All three codex lenses ran `git diff trunk...HEAD` and opened the cited files directly. No hallucinated paths or invented APIs across any of the 11 claims.
- Codex's best finding: **B-3 config.json readable window + icacls fail-open**. I reviewed this area during 009 and missed both the write-then-ACL ordering and the swallowed icacls error. High-value external catch.
- Codex's best judgment call: disclaiming STARTTLS edge cases, html/template, and DPAPI entropy as non-bugs rather than padding the report. Restraint earned trust on the claims it did make.
- Codex missed: the `ServerTable` stale detector-status map on re-register is pre-existing; it flagged correctly but attributed to 009 scope. I'm triaging it out of this batch.
