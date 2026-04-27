//go:build windows

package updater

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// fakes is the optional-fake bag for runUpdater. Each non-nil field
// replaces the same-named package-level seam for the duration of the
// test; the original value is restored via t.Cleanup. Nil fields keep
// production behavior.
//
// runUpdater also unconditionally swaps decodeKeys to return an empty
// list — the integration tests are about orchestration, not manifest
// verification, so they run in transition mode (nil keys). Tests that
// exercise manifest verification live in manifest_remote_windows_test.go
// and call verifyManifestRemote directly with a fake server + keys.
type fakes struct {
	fetchRelease func(ctx context.Context, client *http.Client, channel, etag string) (release, error)
	verifyMSI    func(msiPath string) error
	spawnMSI     func(msiPath string) error
	downloadMSI  func(ctx context.Context, client *http.Client, url string) (string, error)
}

// runUpdater wires the fakes in, drives initialPollDelay near zero, and
// returns a started Subsystem along with the shutdownFired channel that
// the test can wait on (closed by the wired shutdownService cancel func).
// Tests must call s.Stop themselves — runUpdater intentionally does not
// because several scenarios assert on Stop timing.
func runUpdater(t *testing.T, cfg dc.UpdateConfig, f fakes) (s *Subsystem, shutdownFired chan struct{}) {
	t.Helper()

	if f.fetchRelease != nil {
		orig := fetchRelease
		t.Cleanup(func() { fetchRelease = orig })
		fetchRelease = f.fetchRelease
	}
	if f.verifyMSI != nil {
		orig := verifyMSI
		t.Cleanup(func() { verifyMSI = orig })
		verifyMSI = f.verifyMSI
	}
	if f.spawnMSI != nil {
		orig := spawnMSI
		t.Cleanup(func() { spawnMSI = orig })
		spawnMSI = f.spawnMSI
	}
	if f.downloadMSI != nil {
		orig := downloadMSI
		t.Cleanup(func() { downloadMSI = orig })
		downloadMSI = f.downloadMSI
	}

	prevDelay := initialPollDelay
	t.Cleanup(func() { initialPollDelay = prevDelay })
	initialPollDelay = func() time.Duration { return 1 * time.Millisecond }

	prevDecode := decodeKeys
	t.Cleanup(func() { decodeKeys = prevDecode })
	decodeKeys = func() ([]ed25519.PublicKey, error) { return nil, nil }

	shutdownFired = make(chan struct{}, 1)
	var once sync.Once
	cancel := context.CancelFunc(func() {
		once.Do(func() { close(shutdownFired) })
	})

	s = New(cfg, cancel)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return s, shutdownFired
}

// pinVersion forces dc.Version to a parseable CalVer tag for the test's
// duration. The real injected version is "dev" under `go test`, which
// parseVersion rejects — so without this the up-to-date / newer-version
// branch in tick() would fall into bad_local_version and short-circuit.
func pinVersion(t *testing.T, v string) {
	t.Helper()
	prev := dc.Version
	t.Cleanup(func() { dc.Version = prev })
	dc.Version = v
}

// pinBackoffSchedule swaps the package-level backoffSchedule so a test
// that needs to drive a post-failure tick within milliseconds isn't
// blocked by the 5-minute first-failure delay. Restored via t.Cleanup.
func pinBackoffSchedule(t *testing.T, schedule []time.Duration) {
	t.Helper()
	prev := backoffSchedule
	t.Cleanup(func() { backoffSchedule = prev })
	backoffSchedule = schedule
}

// fakeMSI writes a small temp file the production code can stat and
// later os.Remove. Returns the path; t.Cleanup ensures it's gone even
// when a test fails before the Subsystem cleans it up.
func fakeMSI(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "drainctl-fake-update-*.msi")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if _, err := f.WriteString("not a real msi"); err != nil {
		_ = f.Close()
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	path := f.Name()
	t.Cleanup(func() { _ = os.Remove(path) })
	return path
}

// captureSlog redirects the default slog logger to a buffer for the
// test's lifetime. Returns the buffer; restoration is handled via
// t.Cleanup.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// waitFor spins until cond returns true or timeout elapses. Returns
// true if cond fired in time. Used in lieu of arbitrary sleeps.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

// 1. Steady state on the stable channel: remote tag == current version.
// fetchRelease returns the same dc.Version, so tick takes the up-to-date
// branch — no download, no verify, no spawn.
func TestUpdater_SteadyState_StableChannel_NoNewerVersion(t *testing.T) {
	var verifyCalls, spawnCalls, downloadCalls atomic.Int32

	pinVersion(t, "26.116.17")
	cfg := dc.UpdateConfig{Enabled: true, Channel: dc.ChannelStable, PollInterval: dc.Duration(time.Hour)}
	s, _ := runUpdater(t, cfg, fakes{
		fetchRelease: func(_ context.Context, _ *http.Client, _, _ string) (release, error) {
			return release{tag: dc.Version, assetURL: "https://example/asset.msi", etag: "stable-etag"}, nil
		},
		verifyMSI: func(string) error {
			verifyCalls.Add(1)
			return nil
		},
		spawnMSI: func(string) error {
			spawnCalls.Add(1)
			return nil
		},
		downloadMSI: func(_ context.Context, _ *http.Client, _ string) (string, error) {
			downloadCalls.Add(1)
			return "", errors.New("should not be called")
		},
	})
	// Give the first tick time to land.
	time.Sleep(50 * time.Millisecond)
	s.Stop()

	if v := verifyCalls.Load(); v != 0 {
		t.Errorf("verifyMSI called %d times on steady state, want 0", v)
	}
	if v := spawnCalls.Load(); v != 0 {
		t.Errorf("spawnMSI called %d times on steady state, want 0", v)
	}
	if v := downloadCalls.Load(); v != 0 {
		t.Errorf("downloadMSI called %d times on steady state, want 0", v)
	}
}

// 2. Newer version on the stable channel: full happy path runs through
// download → verify → spawn → shutdown. We assert the spawn count, the
// shutdownService cancel firing, and the slog "update=installing" line.
func TestUpdater_NewerVersion_StableChannel_SpawnsInstaller(t *testing.T) {
	var spawnCalls atomic.Int32
	buf := captureSlog(t)

	pinVersion(t, "26.116.17")
	cfg := dc.UpdateConfig{Enabled: true, Channel: dc.ChannelStable, PollInterval: dc.Duration(time.Hour)}
	s, shutdownFired := runUpdater(t, cfg, fakes{
		fetchRelease: func(_ context.Context, _ *http.Client, _, _ string) (release, error) {
			return release{tag: "v99.99.99", assetURL: "https://test/msi", etag: "new-etag"}, nil
		},
		downloadMSI: func(_ context.Context, _ *http.Client, _ string) (string, error) {
			return fakeMSI(t), nil
		},
		verifyMSI: func(string) error { return nil },
		spawnMSI: func(string) error {
			spawnCalls.Add(1)
			return nil
		},
	})

	select {
	case <-shutdownFired:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdownService cancel did not fire after install")
	}
	s.Stop()

	if v := spawnCalls.Load(); v != 1 {
		t.Errorf("spawnMSI called %d times, want 1", v)
	}
	if !strings.Contains(buf.String(), "update=installing") {
		t.Errorf("missing slog line update=installing; got:\n%s", buf.String())
	}
}

// 3. Prerelease channel hits the prerelease endpoint. Asserts via the
// channel arg captured from fetchRelease.
func TestUpdater_NewerVersion_PrereleaseChannel_HitsPrereleaseEndpoint(t *testing.T) {
	var spawnCalls atomic.Int32
	channelSeen := make(chan string, 1)

	pinVersion(t, "26.116.17")
	cfg := dc.UpdateConfig{Enabled: true, Channel: dc.ChannelPrerelease, PollInterval: dc.Duration(time.Hour)}
	s, shutdownFired := runUpdater(t, cfg, fakes{
		fetchRelease: func(_ context.Context, _ *http.Client, channel, _ string) (release, error) {
			select {
			case channelSeen <- channel:
			default:
			}
			return release{tag: "v99.99.99", assetURL: "https://test/msi", etag: "new-etag"}, nil
		},
		downloadMSI: func(_ context.Context, _ *http.Client, _ string) (string, error) {
			return fakeMSI(t), nil
		},
		verifyMSI: func(string) error { return nil },
		spawnMSI: func(string) error {
			spawnCalls.Add(1)
			return nil
		},
	})

	select {
	case got := <-channelSeen:
		if got != dc.ChannelPrerelease {
			t.Errorf("fetchRelease called with channel=%q, want %q", got, dc.ChannelPrerelease)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fetchRelease was not called")
	}
	select {
	case <-shutdownFired:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdownService cancel did not fire on prerelease install")
	}
	s.Stop()

	if v := spawnCalls.Load(); v != 1 {
		t.Errorf("spawnMSI called %d times, want 1", v)
	}
}

// 4. 404 / no-stable-release path is treated as steady-state idle:
// success-recorded, no verify/spawn, backoff stays at 0.
func TestUpdater_NoStableRelease_404IsSteadyState(t *testing.T) {
	var verifyCalls, spawnCalls atomic.Int32
	var fetchCalls atomic.Int32

	cfg := dc.UpdateConfig{Enabled: true, Channel: dc.ChannelStable, PollInterval: dc.Duration(50 * time.Millisecond)}
	s, _ := runUpdater(t, cfg, fakes{
		fetchRelease: func(_ context.Context, _ *http.Client, _, _ string) (release, error) {
			fetchCalls.Add(1)
			return release{noReleases: true}, nil
		},
		verifyMSI: func(string) error {
			verifyCalls.Add(1)
			return nil
		},
		spawnMSI: func(string) error {
			spawnCalls.Add(1)
			return nil
		},
	})

	// Wait for at least one fetchRelease call.
	if !waitFor(2*time.Second, func() bool { return fetchCalls.Load() >= 1 }) {
		t.Fatal("fetchRelease never ran")
	}
	// Allow a second tick so we can confirm there's no escalation.
	time.Sleep(120 * time.Millisecond)
	s.Stop()

	if v := verifyCalls.Load(); v != 0 {
		t.Errorf("verifyMSI called %d times on 404, want 0", v)
	}
	if v := spawnCalls.Load(); v != 0 {
		t.Errorf("spawnMSI called %d times on 404, want 0", v)
	}
	if got := s.backoff.failureCount(); got != 0 {
		t.Errorf("backoff.failureCount = %d after 404 polls, want 0", got)
	}
}

// 5. Disabled: no goroutine, no fetch.
func TestUpdater_Disabled_NoFetch(t *testing.T) {
	var fetchCalls atomic.Int32

	cfg := dc.UpdateConfig{Enabled: false, Channel: dc.ChannelStable, PollInterval: dc.Duration(time.Hour)}
	s, _ := runUpdater(t, cfg, fakes{
		fetchRelease: func(_ context.Context, _ *http.Client, _, _ string) (release, error) {
			fetchCalls.Add(1)
			return release{notModified: true}, nil
		},
	})

	time.Sleep(50 * time.Millisecond)
	s.Stop()

	if v := fetchCalls.Load(); v != 0 {
		t.Errorf("fetchRelease called %d times on disabled Subsystem, want 0", v)
	}
}

// 6. Mid-run channel flip via UpdateConfig. The first tick must observe
// channel=stable, a subsequent tick after UpdateConfig must observe
// channel=prerelease.
func TestUpdater_MidRunChannelFlip(t *testing.T) {
	var (
		mu       sync.Mutex
		channels []string
	)

	cfg := dc.UpdateConfig{Enabled: true, Channel: dc.ChannelStable, PollInterval: dc.Duration(50 * time.Millisecond)}
	s, _ := runUpdater(t, cfg, fakes{
		fetchRelease: func(_ context.Context, _ *http.Client, channel, _ string) (release, error) {
			mu.Lock()
			channels = append(channels, channel)
			mu.Unlock()
			// noReleases keeps backoff at 0 and exercises the success path.
			return release{noReleases: true}, nil
		},
	})

	// Wait for the first call (channel=stable).
	if !waitFor(2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(channels) >= 1 && channels[0] == dc.ChannelStable
	}) {
		mu.Lock()
		got := append([]string(nil), channels...)
		mu.Unlock()
		s.Stop()
		t.Fatalf("first fetchRelease did not see channel=stable; saw %v", got)
	}

	// Flip to prerelease; keep PollInterval short so the next tick fires
	// promptly.
	s.UpdateConfig(dc.UpdateConfig{Enabled: true, Channel: dc.ChannelPrerelease, PollInterval: dc.Duration(50 * time.Millisecond)})

	// Wait for a second call carrying channel=prerelease.
	ok := waitFor(2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range channels[1:] {
			if c == dc.ChannelPrerelease {
				return true
			}
		}
		return false
	})
	mu.Lock()
	got := append([]string(nil), channels...)
	mu.Unlock()
	s.Stop()

	if !ok {
		t.Fatalf("second-or-later fetchRelease never saw channel=prerelease; saw %v", got)
	}
}

// 7. Transient error followed by 304/notModified: backoff should reset
// to 0. We drive two ticks and read the counter after Stop (safe — the
// goroutine is gone).
func TestUpdater_NotModified_304_ResetsBackoff(t *testing.T) {
	var calls atomic.Int32

	// The production schedule starts at 5min, which would make the
	// post-failure tick land far outside the test's deadline. Swap it for
	// a millisecond-scale schedule so the second tick is observable.
	pinBackoffSchedule(t, []time.Duration{1 * time.Millisecond, 1 * time.Millisecond})
	cfg := dc.UpdateConfig{Enabled: true, Channel: dc.ChannelStable, PollInterval: dc.Duration(30 * time.Millisecond)}
	s, _ := runUpdater(t, cfg, fakes{
		fetchRelease: func(_ context.Context, _ *http.Client, _, _ string) (release, error) {
			n := calls.Add(1)
			if n == 1 {
				return release{}, errors.New("network borked")
			}
			return release{notModified: true}, nil
		},
	})

	if !waitFor(3*time.Second, func() bool { return calls.Load() >= 2 }) {
		s.Stop()
		t.Fatalf("expected at least 2 fetchRelease calls, got %d", calls.Load())
	}
	s.Stop()

	if got := s.backoff.failureCount(); got != 0 {
		t.Errorf("backoff.failureCount = %d after a 304-following-failure, want 0", got)
	}
}

// 8. Verification refusal: spawn never runs, backoff is NOT escalated
// (verification refusal is a security event, not a network issue), and
// the temp file is removed.
func TestUpdater_Refusal_VerifyFailureDoesNotEscalate(t *testing.T) {
	var spawnCalls atomic.Int32
	tempPathCh := make(chan string, 1)

	pinVersion(t, "26.116.17")
	cfg := dc.UpdateConfig{Enabled: true, Channel: dc.ChannelStable, PollInterval: dc.Duration(time.Hour)}
	s, _ := runUpdater(t, cfg, fakes{
		fetchRelease: func(_ context.Context, _ *http.Client, _, _ string) (release, error) {
			return release{tag: "v99.99.99", assetURL: "https://test/msi", etag: "etag"}, nil
		},
		downloadMSI: func(_ context.Context, _ *http.Client, _ string) (string, error) {
			f, err := os.CreateTemp("", "drainctl-fake-update-*.msi")
			if err != nil {
				return "", err
			}
			if _, err := f.WriteString("payload"); err != nil {
				_ = f.Close()
				_ = os.Remove(f.Name())
				return "", err
			}
			if err := f.Close(); err != nil {
				_ = os.Remove(f.Name())
				return "", err
			}
			path := f.Name()
			// t.Cleanup as a belt-and-braces: the Subsystem should remove
			// it, but a test failure shouldn't leak a file.
			t.Cleanup(func() { _ = os.Remove(path) })
			select {
			case tempPathCh <- path:
			default:
			}
			return path, nil
		},
		verifyMSI: func(string) error { return errSignatureSubjectMismatch },
		spawnMSI: func(string) error {
			spawnCalls.Add(1)
			return nil
		},
	})

	var tempPath string
	select {
	case tempPath = <-tempPathCh:
	case <-time.After(2 * time.Second):
		s.Stop()
		t.Fatal("downloadMSI was never called")
	}
	// Wait for the verifier-refusal cleanup to land. The Subsystem
	// removes the temp file inside tick() before recording success, so
	// once the file is gone we know the refusal path completed.
	if !waitFor(2*time.Second, func() bool {
		_, err := os.Stat(tempPath)
		return errors.Is(err, os.ErrNotExist)
	}) {
		s.Stop()
		t.Errorf("temp file %s was not removed after verify refusal", tempPath)
	}
	s.Stop()

	if v := spawnCalls.Load(); v != 0 {
		t.Errorf("spawnMSI called %d times after verify refusal, want 0", v)
	}
	if got := s.backoff.failureCount(); got != 0 {
		t.Errorf("backoff.failureCount = %d after verify refusal, want 0", got)
	}
	if _, err := os.Stat(tempPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("os.Stat(%s) after Stop: err = %v, want ErrNotExist", tempPath, err)
	}
}

// 9. Stop returns promptly even when fetchRelease is mid-flight. The
// fake blocks on ctx.Done, so Stop's cancel must propagate within ~200ms.
func TestUpdater_Stop_ReturnsPromptlyMidPoll(t *testing.T) {
	entered := make(chan struct{}, 1)

	cfg := dc.UpdateConfig{Enabled: true, Channel: dc.ChannelStable, PollInterval: dc.Duration(time.Hour)}
	s, _ := runUpdater(t, cfg, fakes{
		fetchRelease: func(ctx context.Context, _ *http.Client, _, _ string) (release, error) {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return release{}, ctx.Err()
		},
	})

	// Wait for the fake to be in-flight before Stop.
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		s.Stop()
		t.Fatal("fetchRelease was never invoked")
	}

	stopDone := make(chan struct{})
	start := time.Now()
	go func() { s.Stop(); close(stopDone) }()
	select {
	case <-stopDone:
		if d := time.Since(start); d > 200*time.Millisecond {
			t.Errorf("Stop took %v mid-poll, want <200ms — ctx cancel did not propagate to fetchRelease", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return — Subsystem leaked a goroutine while fetchRelease was in-flight")
	}
}
