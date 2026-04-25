# Contract: Notification URL Validation — **SUPERSEDED 2026-04-24**

> **Status: WITHDRAWN.** This contract specified an FR-009 cloud-metadata IP
> rejection (`169.254.169.254`). It was withdrawn during 009 codex post-review
> after the literal-string check was shown to be bypassable via IPv6-mapped
> addresses, decimal/hex IPv4 variants, trailing-dot, and 302 redirect (Go's
> default HTTP client follows redirects with no `CheckRedirect` set).
>
> The hedge value was zero while the spec implied a guarantee we couldn't
> deliver. Drained product charter remains: drainctl ships on-prem, RFC1918
> LAN webhooks are legitimate, no validation rule beyond the existing scheme
> allowlist (`http`/`https`). See
> `docs/reviews/codex-2026-04-24-009-branch-remediation.md` Step 1 and
> `docs/reviews/codex-2026-04-24-009-branch-synthesis.md` §B-1 for the full
> reasoning.
>
> The body below is preserved as historical record only.

---

## Scope

Defines the URL rejection rules applied to webhook and ntfy notification targets at
both send-time (`notify.go`) and at dashboard-UI persist/test paths
(`internal/dashboard/server.go`). SMTP targets are NOT covered by this contract —
their transport requirements are specified by the STARTTLS contract in the email
path (FR-002/FR-003).

## Validation rules (intersection of existing and new)

| Rule | Source | Applied to |
|------|--------|-----------|
| Scheme MUST be `http` or `https` | Existing | webhook, ntfy |
| Hostname MUST NOT be exactly `169.254.169.254` | **FR-009 (new)** | webhook, ntfy |
| URL MUST parse as a valid absolute URL | Existing | webhook, ntfy |

All other rules previously considered (RFC1918 block, link-local block, ULA block,
allowlist-based bypass) were rejected during research (see Decision 3 in `research.md`).

## Rationale for the metadata-IP check

`169.254.169.254` is the AWS / Azure / GCP cloud instance-metadata service endpoint.
Blocking it at the notification target layer forecloses a specific future failure mode
(a hypothetical cloud-hosted drainctl agent where an authenticated admin bug could
turn webhook dispatch into IAM-credential exfiltration). The guard is cheap and has
no realistic legitimate use case — `169.254.169.254` is never a valid notification
destination. On-prem Windows hosts cannot reach this IP anyway; the check is free
insurance.

## Enforcement sites

### Send-time (primary defense)

- `notify.sendWebhook(rawURL, ...)` — at function entry, call
  `rejectCloudMetadata(rawURL)`; return error before any network activity.
- `notify.sendNtfy(rawURL, ...)` — same pattern.

### Persist-time (secondary; UX improvement)

- `internal/dashboard/server.go` `handlePutSettings` — before writing updated notify
  targets to config, iterate each webhook/ntfy target and call `rejectCloudMetadata`.
  Any rejection aborts the entire PUT with 400 + error body.
- `internal/dashboard/server.go` `handleNotifyTest` — before dispatching the test
  payload, call `rejectCloudMetadata` on the target URL. Any rejection returns 400.

Persist-time is **in addition to** send-time, not instead of. Defense-in-depth.

## Error shape

```
notify: cloud metadata IP 169.254.169.254 is not a valid notification target
```

Returned as a `error` from send-time helpers and surfaced as the error message in
dashboard 400 responses.

## Helper function contract (implementation guidance)

```go
// In notify.go
func rejectCloudMetadata(rawURL string) error {
    u, err := url.Parse(rawURL)
    if err != nil {
        // Parse errors handled by existing validation layers; not our job.
        return nil
    }
    if u.Hostname() == "169.254.169.254" {
        return fmt.Errorf(
            "notify: cloud metadata IP 169.254.169.254 is not a valid notification target",
        )
    }
    return nil
}
```

- Single file, single function. No new `internal/neturl/` package.
- No allowlist parameter, no config field, no overridability.
- `u.Hostname()` is used (not raw string search) so `http://169.254.169.254:80/x`,
  `http://169.254.169.254/`, and variants all match; trailing-slash variations and
  ports do not bypass.

## Testing contract

- **Unit** (`notify_test.go`):
  - `rejectCloudMetadata("http://169.254.169.254/latest/meta-data/")` → non-nil error.
  - `rejectCloudMetadata("http://169.254.169.254:8080/x")` → non-nil error.
  - `rejectCloudMetadata("https://169.254.169.254/")` → non-nil error.
  - `rejectCloudMetadata("http://192.168.1.10/hook")` → nil (LAN allowed).
  - `rejectCloudMetadata("https://slack.com/hook")` → nil.
  - `rejectCloudMetadata("http://127.0.0.1:8080/x")` → nil (loopback allowed).
  - `rejectCloudMetadata("not a url")` → nil (parse failure, delegated elsewhere).
- **Integration** (dashboard-level): one POST to `/api/v1/notify/test` with a bad URL
  → 400 with the expected error string.
- **Manual**: via dashboard UI, add a webhook at `http://169.254.169.254/x`; confirm
  save fails with the documented error message. Add `http://192.168.1.10/x`; confirm
  save succeeds.

## Out-of-scope (explicitly)

- RFC1918 (`10/8`, `172.16/12`, `192.168/16`) — allowed, intentionally.
- Link-local (`169.254.0.0/16` except the metadata IP) — allowed.
- Loopback (`127.0.0.0/8`, `::1`) — allowed.
- IPv6 metadata addresses (`fd00:ec2::254`) — not currently blocked; revisit when the
  product has a cloud-deployment path.
- Hostnames that resolve to `169.254.169.254` via DNS — not checked. The rule operates
  on the URL hostname as typed, not on DNS resolution.
