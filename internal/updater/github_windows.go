//go:build windows

package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// msiAssetName is the exact (case-sensitive) asset filename the updater
// expects on every release. The contract in
// specs/010-auto-update/contracts/github-releases-api.md forbids any
// prefix/suffix tolerance.
const msiAssetName = "LISSTech.DrainCtl.msi"

// apiBaseURL is the GitHub REST base for the LISSTech.DrainCtl repo.
// It's a var (not const) so unit tests can swap it for an httptest server
// via t.Cleanup-restored assignment. No production code mutates it.
var apiBaseURL = "https://api.github.com/repos/LISSConsulting/LISSTech.DrainCtl"

// release is the small contract surface fetchLatestRelease exposes to the
// caller. Callers branch on notModified and noReleases before reading
// tag/assetURL/etag.
type release struct {
	tag, assetURL, etag string
	notModified         bool // true on 304
	noReleases          bool // true on 404 from a release-listing endpoint
}

// fetchLatestRelease performs a single GET against the GitHub releases API
// for the configured channel. It does not implement retry/backoff — that's
// the caller's responsibility. Caller cancellation propagates via ctx; the
// caller is also responsible for any per-request timeout (typically via
// http.Client.Timeout).
func fetchLatestRelease(ctx context.Context, client *http.Client, channel, ifNoneMatch string) (release, error) {
	endpoint, err := buildEndpoint(channel)
	if err != nil {
		return release{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return release{}, fmt.Errorf("github: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "drainctld/"+dc.Version)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}

	resp, err := client.Do(req)
	if err != nil {
		return release{}, fmt.Errorf("github: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNotModified: // 304
		// GitHub returns no body on 304. Drain the (likely empty) reader
		// up to a tiny cap so the underlying TCP connection can be reused
		// by the next poll. Don't allocate into a buffer.
		_, _ = io.CopyN(io.Discard, resp.Body, 1024)
		return release{notModified: true}, nil

	case http.StatusOK: // 200
		return parseReleaseBody(resp, channel)

	case http.StatusNotFound: // 404
		// Per contract: 404 from one of the two release-listing endpoints
		// means "no stable releases yet" / "no releases at all" — a
		// steady-state idle, not an error. Any other 404 (wrong path,
		// repo renamed) is a poll failure.
		if isReleaseListingPath(req.URL) {
			return release{noReleases: true}, nil
		}
		return release{}, fmt.Errorf("github: unexpected 404 at %s", req.URL.Path)

	case http.StatusForbidden: // 403 — almost always rate-limit
		return release{}, fmt.Errorf("github: 403 forbidden (likely rate-limited)")

	default:
		if resp.StatusCode >= 500 && resp.StatusCode < 600 {
			return release{}, fmt.Errorf("github: server error %d", resp.StatusCode)
		}
		return release{}, fmt.Errorf("github: unexpected status %d", resp.StatusCode)
	}
}

// buildEndpoint assembles the absolute URL we hit for a given channel,
// preserving any path components already present on apiBaseURL (so test
// servers like httptest.Server with a non-root path work).
func buildEndpoint(channel string) (string, error) {
	base, err := url.Parse(apiBaseURL)
	if err != nil {
		return "", fmt.Errorf("github: parse apiBaseURL: %w", err)
	}
	if channel == dc.ChannelPrerelease {
		base.Path = strings.TrimRight(base.Path, "/") + "/releases"
		base.RawQuery = "per_page=1"
	} else {
		base.Path = strings.TrimRight(base.Path, "/") + "/releases/latest"
		base.RawQuery = ""
	}
	return base.String(), nil
}

// isReleaseListingPath reports whether the request URL targeted one of the
// two release endpoints we know about. Used to distinguish a "no releases"
// 404 from any other 404.
func isReleaseListingPath(u *url.URL) bool {
	p := strings.TrimRight(u.Path, "/")
	return strings.HasSuffix(p, "/releases/latest") || strings.HasSuffix(p, "/releases")
}

// parseReleaseBody handles the 200 path for both endpoints.
func parseReleaseBody(resp *http.Response, channel string) (release, error) {
	type asset struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	}
	type ghRelease struct {
		TagName string  `json:"tag_name"`
		Assets  []asset `json:"assets"`
	}

	dec := json.NewDecoder(resp.Body)

	var rel ghRelease
	if channel == dc.ChannelPrerelease {
		// Collection endpoint — array, take [0].
		var arr []ghRelease
		if err := dec.Decode(&arr); err != nil {
			return release{}, fmt.Errorf("github: decode releases array: %w", err)
		}
		if len(arr) == 0 {
			return release{}, fmt.Errorf("github: prerelease endpoint returned empty array")
		}
		rel = arr[0]
	} else {
		if err := dec.Decode(&rel); err != nil {
			return release{}, fmt.Errorf("github: decode release: %w", err)
		}
	}

	if rel.TagName == "" {
		return release{}, fmt.Errorf("github: release missing tag_name")
	}

	var assetURL string
	for _, a := range rel.Assets {
		if a.Name == msiAssetName {
			assetURL = a.BrowserDownloadURL
			break
		}
	}
	if assetURL == "" {
		return release{}, fmt.Errorf("github: release %s has no asset named %q", rel.TagName, msiAssetName)
	}

	return release{
		tag:      rel.TagName,
		assetURL: assetURL,
		etag:     resp.Header.Get("ETag"),
	}, nil
}
