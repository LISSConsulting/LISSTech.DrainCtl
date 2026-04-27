//go:build windows

package updater

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// withAPIBase swaps apiBaseURL for the duration of the test and restores
// it via t.Cleanup. Note: tests that exercise apiBaseURL must not run in
// parallel — they all share this package-level var.
func withAPIBase(t *testing.T, base string) {
	t.Helper()
	prev := apiBaseURL
	apiBaseURL = base
	t.Cleanup(func() { apiBaseURL = prev })
}

const stableFixtureBody = `{
  "tag_name": "v26.116.17",
  "assets": [
    {"name": "checksums.txt", "browser_download_url": "https://example.com/checksums.txt"},
    {"name": "LISSTech.DrainCtl.msi", "browser_download_url": "https://example.com/LISSTech.DrainCtl.msi"}
  ]
}`

const prereleaseFixtureBody = `[
  {
    "tag_name": "v26.117.1",
    "assets": [
      {"name": "LISSTech.DrainCtl.msi", "browser_download_url": "https://example.com/pre/LISSTech.DrainCtl.msi"}
    ]
  }
]`

const missingAssetBody = `{
  "tag_name": "v26.116.17",
  "assets": [
    {"name": "something-else.zip", "browser_download_url": "https://example.com/x.zip"}
  ]
}`

// captured records what the test handler observed about the inbound request,
// so individual subtests can make precise assertions.
type captured struct {
	method      string
	pathQuery   string
	userAgent   string
	accept      string
	apiVersion  string
	ifNoneMatch string
}

func TestFetchLatestRelease_StableOK(t *testing.T) {
	var got captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.pathQuery = r.URL.Path
		if r.URL.RawQuery != "" {
			got.pathQuery += "?" + r.URL.RawQuery
		}
		got.userAgent = r.Header.Get("User-Agent")
		got.accept = r.Header.Get("Accept")
		got.apiVersion = r.Header.Get("X-GitHub-Api-Version")
		got.ifNoneMatch = r.Header.Get("If-None-Match")

		w.Header().Set("ETag", `"abc123"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stableFixtureBody))
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL+"/repos/LISSConsulting/LISSTech.DrainCtl")

	rel, err := fetchLatestRelease(context.Background(), srv.Client(), dc.ChannelStable, "")
	if err != nil {
		t.Fatalf("fetchLatestRelease: %v", err)
	}
	if rel.notModified || rel.noReleases {
		t.Fatalf("unexpected status flags: %+v", rel)
	}
	if rel.tag != "v26.116.17" {
		t.Errorf("tag = %q, want v26.116.17", rel.tag)
	}
	if rel.assetURL != "https://example.com/LISSTech.DrainCtl.msi" {
		t.Errorf("assetURL = %q", rel.assetURL)
	}
	if rel.etag != `"abc123"` {
		t.Errorf("etag = %q, want \"abc123\"", rel.etag)
	}
	if !strings.HasSuffix(got.pathQuery, "/repos/LISSConsulting/LISSTech.DrainCtl/releases/latest") {
		t.Errorf("request URL = %q, want suffix /releases/latest", got.pathQuery)
	}
	if got.accept != "application/vnd.github+json" {
		t.Errorf("Accept = %q", got.accept)
	}
	if got.apiVersion != "2022-11-28" {
		t.Errorf("X-GitHub-Api-Version = %q", got.apiVersion)
	}
}

func TestFetchLatestRelease_PrereleaseOK(t *testing.T) {
	var got captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.pathQuery = r.URL.Path
		if r.URL.RawQuery != "" {
			got.pathQuery += "?" + r.URL.RawQuery
		}
		w.Header().Set("ETag", `"pre1"`)
		_, _ = w.Write([]byte(prereleaseFixtureBody))
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL+"/repos/LISSConsulting/LISSTech.DrainCtl")

	rel, err := fetchLatestRelease(context.Background(), srv.Client(), dc.ChannelPrerelease, "")
	if err != nil {
		t.Fatalf("fetchLatestRelease: %v", err)
	}
	if rel.tag != "v26.117.1" {
		t.Errorf("tag = %q, want v26.117.1", rel.tag)
	}
	if rel.assetURL != "https://example.com/pre/LISSTech.DrainCtl.msi" {
		t.Errorf("assetURL = %q", rel.assetURL)
	}
	if !strings.HasSuffix(got.pathQuery, "/releases?per_page=1") {
		t.Errorf("request URL = %q, want suffix /releases?per_page=1", got.pathQuery)
	}
}

func TestFetchLatestRelease_Stable304(t *testing.T) {
	var got captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.ifNoneMatch = r.Header.Get("If-None-Match")
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL+"/repos/LISSConsulting/LISSTech.DrainCtl")

	rel, err := fetchLatestRelease(context.Background(), srv.Client(), dc.ChannelStable, `"abc123"`)
	if err != nil {
		t.Fatalf("fetchLatestRelease: %v", err)
	}
	if !rel.notModified {
		t.Errorf("notModified = false, want true")
	}
	if got.ifNoneMatch != `"abc123"` {
		t.Errorf("If-None-Match = %q, want \"abc123\"", got.ifNoneMatch)
	}
}

func TestFetchLatestRelease_Stable404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL+"/repos/LISSConsulting/LISSTech.DrainCtl")

	rel, err := fetchLatestRelease(context.Background(), srv.Client(), dc.ChannelStable, "")
	if err != nil {
		t.Fatalf("fetchLatestRelease returned error on stable 404: %v", err)
	}
	if !rel.noReleases {
		t.Errorf("noReleases = false, want true")
	}
}

func TestFetchLatestRelease_Prerelease404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL+"/repos/LISSConsulting/LISSTech.DrainCtl")

	rel, err := fetchLatestRelease(context.Background(), srv.Client(), dc.ChannelPrerelease, "")
	if err != nil {
		t.Fatalf("fetchLatestRelease returned error on prerelease 404: %v", err)
	}
	if !rel.noReleases {
		t.Errorf("noReleases = false, want true")
	}
}

func TestFetchLatestRelease_403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for ..."}`))
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL+"/repos/LISSConsulting/LISSTech.DrainCtl")

	_, err := fetchLatestRelease(context.Background(), srv.Client(), dc.ChannelStable, "")
	if err == nil {
		t.Fatal("fetchLatestRelease: want error on 403, got nil")
	}
}

func TestFetchLatestRelease_5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL+"/repos/LISSConsulting/LISSTech.DrainCtl")

	_, err := fetchLatestRelease(context.Background(), srv.Client(), dc.ChannelStable, "")
	if err == nil {
		t.Fatal("fetchLatestRelease: want error on 502, got nil")
	}
}

func TestFetchLatestRelease_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{this isn't json`))
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL+"/repos/LISSConsulting/LISSTech.DrainCtl")

	_, err := fetchLatestRelease(context.Background(), srv.Client(), dc.ChannelStable, "")
	if err == nil {
		t.Fatal("fetchLatestRelease: want error on malformed JSON, got nil")
	}
}

func TestFetchLatestRelease_NoMatchingAsset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(missingAssetBody))
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL+"/repos/LISSConsulting/LISSTech.DrainCtl")

	_, err := fetchLatestRelease(context.Background(), srv.Client(), dc.ChannelStable, "")
	if err == nil {
		t.Fatal("fetchLatestRelease: want error when MSI asset is absent, got nil")
	}
	if !strings.Contains(err.Error(), msiAssetName) {
		t.Errorf("error %q should mention %q", err, msiAssetName)
	}
}

func TestFetchLatestRelease_UserAgent(t *testing.T) {
	var got captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.userAgent = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(stableFixtureBody))
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL+"/repos/LISSConsulting/LISSTech.DrainCtl")

	if _, err := fetchLatestRelease(context.Background(), srv.Client(), dc.ChannelStable, ""); err != nil {
		t.Fatalf("fetchLatestRelease: %v", err)
	}
	if !strings.HasPrefix(got.userAgent, "drainctld/") {
		t.Errorf("User-Agent = %q, want prefix drainctld/", got.userAgent)
	}
}

func TestFetchLatestRelease_CtxCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the client cancels.
		<-r.Context().Done()
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL+"/repos/LISSConsulting/LISSTech.DrainCtl")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		_, err := fetchLatestRelease(ctx, srv.Client(), dc.ChannelStable, "")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("fetchLatestRelease: want error from ctx cancel, got nil")
		}
		if !errors.Is(err, context.Canceled) {
			t.Logf("error chain: %v (want context.Canceled in chain)", err)
			// Tolerate net wrapper errors that wrap context.Canceled — Go's
			// http transport returns *url.Error wrapping context.Canceled,
			// and errors.Is should walk that. If it doesn't, the message
			// must at least mention "canceled".
			if !strings.Contains(err.Error(), "canceled") {
				t.Errorf("err = %v, want context.Canceled or message containing 'canceled'", err)
			}
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("fetchLatestRelease did not return within 500ms after ctx cancel")
	}
}
