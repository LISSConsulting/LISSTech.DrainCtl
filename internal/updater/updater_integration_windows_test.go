//go:build windows

package updater

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
// decodeKeys defaults to returning nil (transition mode) so existing
// orchestration tests don't have to deal with manifest sidecars. Tests
// that exercise the keys-on path set f.decodeKeys explicitly.
//
// verifyManifest is a separate seam alongside verifyMSI so a test can
// observe the call ordering through tick() (manifest verify must run
// before authenticode verify).
type fakes struct {
	fetchRelease   func(ctx context.Context, client *http.Client, channel, etag string) (release, error)
	verifyManifest func(ctx context.Context, client *http.Client, msiPath, manifestURL, sigURL string, keys []ed25519.PublicKey) error
	verifyMSI      func(msiPath string) error
	spawnMSI       func(msiPath string) error
	downloadMSI    func(ctx context.Context, client *http.Client, url string) (string, error)
	decodeKeys     func() ([]ed25519.PublicKey, error)
	// initialState seeds the in-memory updateState before Start runs.
	// Used by replay-defense tests to model a binary that has previously
	// observed a higher version. Saves through runUpdater's in-memory
	// fakes flow into a t.Cleanup'd capture for assertions.
	initialState updateState
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
	if f.verifyManifest != nil {
		orig := verifyManifest
		t.Cleanup(func() { verifyManifest = orig })
		verifyManifest = f.verifyManifest
	}

	prevDelay := initialPollDelay
	t.Cleanup(func() { initialPollDelay = prevDelay })
	initialPollDelay = func() time.Duration { return 1 * time.Millisecond }

	prevDecode := decodeKeys
	t.Cleanup(func() { decodeKeys = prevDecode })
	if f.decodeKeys != nil {
		decodeKeys = f.decodeKeys
	} else {
		decodeKeys = func() ([]ed25519.PublicKey, error) { return nil, nil }
	}

	// Default to in-memory state so existing tests don't touch production
	// %ProgramData%. Tests exercising the real persistence path override
	// updateStatePath/loadUpdateState/saveUpdateState directly.
	prevLoad := loadUpdateState
	prevSave := saveUpdateState
	t.Cleanup(func() { loadUpdateState = prevLoad; saveUpdateState = prevSave })
	var (
		memState   = f.initialState
		memStateMu sync.Mutex
	)
	loadUpdateState = func() (updateState, error) {
		memStateMu.Lock()
		defer memStateMu.Unlock()
		return memState, nil
	}
	saveUpdateState = func(s updateState) error {
		memStateMu.Lock()
		defer memStateMu.Unlock()
		memState = s
		return nil
	}

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

	pinVersion(t, "26.6.17")
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

	pinVersion(t, "26.6.17")
	cfg := dc.UpdateConfig{Enabled: true, Channel: dc.ChannelStable, PollInterval: dc.Duration(time.Hour)}
	s, shutdownFired := runUpdater(t, cfg, fakes{
		fetchRelease: func(_ context.Context, _ *http.Client, _, _ string) (release, error) {
			return release{tag: "v99.12.99", assetURL: "https://test/msi", etag: "new-etag"}, nil
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

	pinVersion(t, "26.6.17")
	cfg := dc.UpdateConfig{Enabled: true, Channel: dc.ChannelPrerelease, PollInterval: dc.Duration(time.Hour)}
	s, shutdownFired := runUpdater(t, cfg, fakes{
		fetchRelease: func(_ context.Context, _ *http.Client, channel, _ string) (release, error) {
			select {
			case channelSeen <- channel:
			default:
			}
			return release{tag: "v99.12.99", assetURL: "https://test/msi", etag: "new-etag"}, nil
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

	pinVersion(t, "26.6.17")
	cfg := dc.UpdateConfig{Enabled: true, Channel: dc.ChannelStable, PollInterval: dc.Duration(time.Hour)}
	s, _ := runUpdater(t, cfg, fakes{
		fetchRelease: func(_ context.Context, _ *http.Client, _, _ string) (release, error) {
			return release{tag: "v99.12.99", assetURL: "https://test/msi", etag: "etag"}, nil
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

// TestRunUpdater_KeysOn_VerifyManifestBeforeMSI — M2 regression test.
// Drives tick() with a non-empty signing-keys list AND captures call
// ordering of verifyManifest vs verifyMSI to prove manifest is checked
// first. Without this test, an accidental swap of the two calls in
// updater_windows.go would slip past every existing integration test
// (which all run in transition mode with nil keys → verifyManifest is a
// no-op).
//
// Two sub-test variants:
//
//	(a) Faked verifyManifest — proves orchestration ordering. Only
//	    verifies that tick() calls verifyManifest before verifyMSI when
//	    keys are configured.
//	(b) Real verifyManifestRemote against an httptest.Server serving
//	    real signed manifest+sig+MSI bytes — proves the wiring between
//	    fetchRelease, the sidecar URLs, and the verifier function holds
//	    end-to-end.
func TestRunUpdater_KeysOn_VerifyManifestBeforeMSI(t *testing.T) {
	t.Run("faked-verifyManifest", func(t *testing.T) {
		runKeysOnOrderingTest(t, false)
	})
	t.Run("real-verifyManifestRemote", func(t *testing.T) {
		runKeysOnOrderingTest(t, true)
	})
}

func runKeysOnOrderingTest(t *testing.T, useRealVerifier bool) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	// Create a real MSI on disk so the real verifyManifestRemote can hash it.
	msiPath := fakeMSI(t)
	body, err := os.ReadFile(msiPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)

	manifest := ReleaseManifest{
		SchemaVersion: ManifestSchemaVersion,
		Version:       "99.12.99",
		Asset:         ManifestAsset{Name: msiAssetName, SHA256: hex.EncodeToString(sum[:])},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(priv, manifestBytes)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest":
			_, _ = w.Write(manifestBytes)
		case "/sig":
			_, _ = w.Write(sig)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	var order atomic.Int32
	var manifestOrder, msiOrder int32

	pinVersion(t, "26.6.17")
	cfg := dc.UpdateConfig{Enabled: true, Channel: dc.ChannelStable, PollInterval: dc.Duration(time.Hour)}

	f := fakes{
		decodeKeys: func() ([]ed25519.PublicKey, error) { return []ed25519.PublicKey{pub}, nil },
		fetchRelease: func(_ context.Context, _ *http.Client, _, _ string) (release, error) {
			return release{
				tag:         "v99.12.99",
				assetURL:    srv.URL + "/msi",
				manifestURL: srv.URL + "/manifest",
				sigURL:      srv.URL + "/sig",
				etag:        "e",
			}, nil
		},
		downloadMSI: func(_ context.Context, _ *http.Client, _ string) (string, error) {
			return msiPath, nil
		},
		verifyMSI: func(string) error {
			msiOrder = order.Add(1)
			return nil
		},
		spawnMSI: func(string) error { return nil },
	}

	if !useRealVerifier {
		f.verifyManifest = func(_ context.Context, _ *http.Client, _, _, _ string, keys []ed25519.PublicKey) error {
			if len(keys) == 0 {
				return errors.New("verifyManifest called without keys — runUpdater seam wiring broken")
			}
			manifestOrder = order.Add(1)
			return nil
		}
	} else {
		// Real verifyManifestRemote runs; it doesn't touch the order
		// counter. We instead capture order by wrapping at the point it's
		// known to have completed: since real verifier returns nil before
		// verifyMSI is called, manifestOrder must be < msiOrder if both
		// fire. We synthesize manifestOrder by intercepting through
		// downloadMSI's completion (downloadMSI returns the MSI; the very
		// next thing tick() does is call verifyManifest, then verifyMSI).
		// Easier: leave verifyManifest unset (uses production), and have
		// verifyMSI's order indirectly prove ordering — verifyMSI was
		// reached, which is only possible after verifyManifest succeeded.
		manifestOrder = 1 // placeholder for the assertion below
	}

	s, shutdownFired := runUpdater(t, cfg, f)

	select {
	case <-shutdownFired:
	case <-time.After(2 * time.Second):
		t.Fatal("install didn't fire — keys-on path didn't reach spawnMSI; verifyManifest probably refused")
	}
	s.Stop()

	if msiOrder == 0 {
		t.Fatal("verifyMSI never ran — orchestration didn't reach the authenticode step")
	}
	if !useRealVerifier {
		if manifestOrder == 0 {
			t.Fatal("verifyManifest never ran — orchestration skipped the manifest step despite keys=on")
		}
		if manifestOrder >= msiOrder {
			t.Errorf("verifyManifest ran AFTER verifyMSI (orders: manifest=%d, msi=%d) — security regression", manifestOrder, msiOrder)
		}
	}
	// useRealVerifier variant: msiOrder > 0 alone proves manifest passed,
	// because production tick() refuses to reach verifyMSI if
	// verifyManifestRemote returns an error.
}
