# Contract: GitHub Releases API (consumed by 010 updater)

The updater depends on a small subset of the public GitHub REST API. This file documents the exact request and the exact response fields the implementation may rely on. Any GitHub-side change that breaks this subset is a P1 incident for the updater; mitigation is to disable `update.enabled` in config.json on affected hosts and ship a fix in the next release.

## Request — stable channel (`channel="stable"`)

```
GET https://api.github.com/repos/LISSConsulting/LISSTech.DrainCtl/releases/latest
Headers:
  Accept: application/vnd.github+json
  User-Agent: drainctld/<version>
  X-GitHub-Api-Version: 2022-11-28
  If-None-Match: <last-etag>     // optional; omitted on first poll after Start
```

GitHub's `/releases/latest` skips drafts and prereleases. If the repo has zero non-prerelease releases (which is the state through 010), this endpoint returns 404 — see "Response — 404 Not Found" below.

## Request — prerelease channel (`channel="prerelease"`)

```
GET https://api.github.com/repos/LISSConsulting/LISSTech.DrainCtl/releases?per_page=1
Headers:                          // same headers as above
```

The collection endpoint returns releases in reverse-chronological order regardless of the prerelease flag. With `per_page=1` we get only the most recent release as a single-element array. The implementation parses `[0]` and proceeds identically to the stable-channel path.

## User-Agent

The `User-Agent` field is required by the GitHub API; requests without it fail with 403. The version we send is `dc.Version` (current binary's CalVer) — it doubles as a fleet-fingerprint metric for us if we ever want to count agents.

## Response — 200 OK

The contract surface is two fields:

```json
{
  "tag_name": "v26.6.17",
  "assets": [
    {
      "name": "LISSTech.DrainCtl.msi",
      "browser_download_url": "https://github.com/LISSConsulting/LISSTech.DrainCtl/releases/download/v26.6.17/LISSTech.DrainCtl.msi",
      "...": "..."
    }
  ],
  "...": "..."
}
```

The implementation:
- Parses `tag_name`, strips a leading `v`, and feeds the remainder to the version comparator (FR-005).
- Walks `assets[]` looking for an entry whose `name` equals exactly `LISSTech.DrainCtl.msi` (no prefix/suffix tolerance — case-sensitive match).
- Uses that asset's `browser_download_url` as the download target.

Other fields in the response are ignored. New fields added by GitHub are tolerated by Go's JSON unmarshal.

The `ETag` response header is captured for the next poll's `If-None-Match`.

## Response — 304 Not Modified

Returned when the request includes `If-None-Match` matching the current release's ETag. No body. The updater logs `update=not_modified etag=…` and returns without further action. Backoff counter is reset (304 is a successful poll).

## Response — 403 Forbidden (rate-limited)

Returned when the unauthenticated rate limit (60 requests per hour per source IP) is exceeded. Body includes `{"message": "API rate limit exceeded for ..."}`. The updater treats this as a poll failure (increments `consecutiveFailures`, applies backoff). At a 24h-±-2h cadence per host, exceeding the limit requires either an extreme fleet size from a single egress IP or a regression that polls more aggressively than configured — both are operator-visible.

## Response — 5xx

Treated as a poll failure. Backoff applies. The updater MUST NOT distinguish 500/502/503/504 in behavior; any 5xx is "GitHub is unhappy, try later."

## Response — 404 Not Found

On `/releases/latest` (stable channel), 404 is the expected response when the repo has releases but ALL of them are flagged prerelease — exactly the state of this repo through 010. The updater logs `update=no_stable_release` and reschedules at the configured interval. **It does NOT increment the failure counter or apply backoff** — this is a steady-state idle, not a network-level failure. The operator's choice to set `channel="stable"` is the operator saying "I want stable releases only"; the updater honors that by being patient.

On `/releases?per_page=1` (prerelease channel), 404 means no releases exist at all (drafts only, or empty repo). Same handling: log, reschedule, no backoff.

For any other 404 (e.g., wrong path, repo renamed), the updater treats it as a poll failure with backoff per FR-012. Distinguish the "no releases" case by checking the path of the URL the updater issued.

## v1.1 contract addition — signed manifest sidecars

As of v1.1 (see [spec.md §"v1.1 — Signed Release Manifest Verification"](../spec.md#v11--signed-release-manifest-verification)), every release MUST also include two assets when the binary embeds release-signing pubkeys:

- `release.json` — the `ReleaseManifest` JSON (binds asset name + SHA-256).
- `release.json.sig` — raw 64-byte Ed25519 signature over the EXACT bytes of `release.json`.

The contract for these two assets is:
- Names are case-sensitive equality (no prefix/suffix tolerance, same as `LISSTech.DrainCtl.msi`).
- `release.json` MUST be UTF-8 with no BOM. Signature is computed over bytes-as-published; the verifier MUST verify before parsing JSON.
- Order of upload does not matter; the verifier downloads each by name independently.
- A release with neither asset is acceptable when no fielded binary embeds keys (transition mode); a release missing only one of the two is a contract violation.

Updaters with at least one embedded pubkey REFUSE to install when either sidecar is absent (`update=refused stage=manifest`). The `just sign-release-manifest` and `just publish` recipes enforce this from the publishing side.

## What we explicitly DO NOT depend on

- The shape of `assets[].uploader`, `assets[].digest`, or any other GitHub-emitted asset metadata. Only `name` and `browser_download_url`. Note: our v1.1 manifest carries its OWN `asset.sha256` field, which IS load-bearing — but that's our published JSON, not GitHub's `assets[].digest`. The two are intentionally separate.
- Pagination on `assets[]`. The MSI is always present in the first page; if GitHub ever paginates this endpoint we re-evaluate.
- The semantics of GitHub's `prerelease` boolean BEYOND what's stated above. We rely on `/releases/latest` skipping prereleases (filter to stable) and on `/releases` not skipping anything (collection in reverse-chronological order). The `channel` config field selects which endpoint we use; we never inspect the `prerelease` boolean in the response body — the endpoint choice is what enforces the channel.
- HTTP/2 specifics. Go's net/http picks the protocol; we don't pin.
- The download URL's stability across a release re-publish. If a maintainer deletes and re-uploads an asset, the URL is regenerated; the next poll picks up the new URL. Polls are idempotent.

## Stability monitoring

Once landed, the codebase grows a quarterly review item: re-read this contract against the current GitHub API behavior. Sources of drift:
- GitHub deprecates a header or response shape.
- The repo moves to a different org name.
- Authentication becomes required for unauthenticated public access.

Each of those is a planned breakage; the response is a small, focused PR that updates the contract and the implementation together.
