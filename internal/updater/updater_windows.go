//go:build windows

// Package updater implements the agent self-poll auto-update subsystem
// per spec 010. See specs/010-auto-update/ for the full feature contract.
//
// The package satisfies internal/lifecycle.Subsystem. It is wired into
// the service's subsystem list at construction time; Start launches the
// poll goroutine; Stop drains it.
package updater

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"sync"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/lifecycle"
)

// Compile-time assertion: *Subsystem satisfies lifecycle.Subsystem.
var _ lifecycle.Subsystem = (*Subsystem)(nil)

// httpClientTimeout bounds every poll's HTTP exchange. Generous (GitHub
// typically responds in <500ms) but bounded so a wedged TCP connection
// can't pin a poll-loop iteration forever.
const httpClientTimeout = 30 * time.Second

// Test seams. Production code calls these; integration tests in Commit 10
// swap them with fakes (counting wrappers, fixture-returning fakes, etc.)
// using t.Cleanup-restored assignment.
//
// downloadToTemp is the package-level function form of the download step
// so the seam is a function var, not an awkward method-pointer var.
var (
	fetchRelease = fetchLatestRelease
	verifyMSI    = verifyAuthenticode
	spawnMSI     = spawnInstall
	downloadMSI  = downloadToTemp
)

// initialPollDelay returns the jittered first-poll delay (5–15 min per
// FR-004). A package var so tests can swap it for a near-zero delay
// when driving the poll loop deterministically.
var initialPollDelay = func() time.Duration {
	return 5*time.Minute + time.Duration(rand.Int64N(int64(10*time.Minute))) //nolint:gosec // jitter, not security-sensitive
}

// Subsystem is the auto-updater. Construct via New, register with the
// service alongside other lifecycle.Subsystems, call Start with a
// service-scoped ctx, call Stop on shutdown.
//
// All mutable state (etag, backoff, cfg) is owned by the poll goroutine
// after Start returns; the goroutine is single-threaded so no internal
// locking is needed for state it touches alone. The cfg field is
// re-readable mid-run via UpdateConfig (mutex-guarded) so an operator's
// config edit can re-enable a disabled updater or change channel without
// restart.
type Subsystem struct {
	shutdownService context.CancelFunc
	client          *http.Client

	cfgMu sync.RWMutex
	cfg   dc.UpdateConfig

	// Mutable state held by the poll goroutine only — no synchronization.
	etag    string
	backoff backoff

	// Lifecycle.
	wg       sync.WaitGroup
	ctx      context.Context
	cancel   context.CancelFunc
	stopOnce sync.Once
}

// New constructs the Subsystem. Does not launch any goroutines —
// goroutines are owned by Start per the LCI contract.
func New(cfg dc.UpdateConfig, shutdownService context.CancelFunc) *Subsystem {
	return &Subsystem{
		cfg:             cfg,
		shutdownService: shutdownService,
		client:          &http.Client{Timeout: httpClientTimeout},
	}
}

// UpdateConfig replaces the in-memory cfg with a new one. Called by the
// config-watcher path on a config.json change so an operator can flip
// Enabled or change Channel without a service restart. PollInterval
// changes do NOT take effect until next Subsystem reconstruction —
// the loop's ticker is bound to the value present at Start.
func (s *Subsystem) UpdateConfig(cfg dc.UpdateConfig) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	s.cfg = cfg
}

// snapshot returns a value copy of the current cfg. Held briefly under
// the read lock; the value copy is safe to inspect without further
// synchronization.
func (s *Subsystem) snapshot() dc.UpdateConfig {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

// Start implements lifecycle.Subsystem. If Enabled is false at Start
// time, the subsystem is a no-op — Stop will return immediately. If the
// operator flips Enabled=true post-Start via UpdateConfig, that change
// is NOT picked up; we'd need to start a goroutine retroactively. The
// expected operational pattern is: edit config to enabled=true BEFORE
// service start (or accept a service restart on the rare opt-in event).
func (s *Subsystem) Start(ctx context.Context) error {
	cfg := s.snapshot()
	if !cfg.Enabled {
		slog.Debug("update=disabled at_start")
		return nil
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.wg.Add(1)
	go s.run()
	slog.Info("update=started", "channel", cfg.Channel, "poll_interval", time.Duration(cfg.PollInterval).String())
	return nil
}

// Stop implements lifecycle.Subsystem. Idempotent.
func (s *Subsystem) Stop() {
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		s.wg.Wait()
	})
}

// run is the poll loop. Lives in a single goroutine for the lifetime
// of an enabled Subsystem.
func (s *Subsystem) run() {
	defer s.wg.Done()

	// Initial delay (5–15 min per FR-004). Then forever, every tick:
	// poll, decide, sleep until next tick.
	if !s.sleepCtx(initialPollDelay()) {
		return
	}
	for {
		s.tick()
		// Determine the next-poll delay: cfg.PollInterval ± jitter, plus
		// any backoff. cfg may have changed mid-run; reread.
		cfg := s.snapshot()
		base := time.Duration(cfg.PollInterval)
		if base <= 0 {
			base = dc.DefaultUpdatePollInterval
		}
		delay := jitter(base)
		if s.backoff.failureCount() > 0 {
			delay += backoffSchedule[min(s.backoff.failureCount()-1, len(backoffSchedule)-1)]
		}
		if !s.sleepCtx(delay) {
			return
		}
	}
}

// tick runs one poll. Mutations to s.etag and s.backoff happen here.
func (s *Subsystem) tick() {
	cfg := s.snapshot()
	if !cfg.Enabled {
		slog.Debug("update=disabled tick_skip")
		return
	}

	slog.Debug("update=poll_start", "current", dc.Version, "channel", cfg.Channel)

	rel, err := fetchRelease(s.ctx, s.client, cfg.Channel, s.etag)
	if err != nil {
		// Don't backoff on ctx cancel — the goroutine is exiting.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		delay := s.backoff.recordFailure()
		slog.Warn("update=poll_failed", "error", err.Error(), "backoff", delay.String(), "failures", s.backoff.failureCount())
		return
	}

	switch {
	case rel.notModified:
		slog.Debug("update=not_modified", "etag", s.etag)
		s.backoff.recordSuccess()
		return
	case rel.noReleases:
		slog.Info("update=no_stable_release", "channel", cfg.Channel)
		s.backoff.recordSuccess()
		return
	}

	// 200 path. Capture the new ETag before any decision so a later poll
	// can short-circuit even if this one ends in a refusal.
	s.etag = rel.etag

	remote, err := parseVersion(rel.tag)
	if err != nil {
		slog.Warn("update=bad_tag", "tag", rel.tag, "error", err.Error())
		s.backoff.recordSuccess() // not a network failure
		return
	}
	current, err := parseVersion(dc.Version)
	if err != nil {
		// Should never happen — dc.Version comes from build-time ldflags.
		// Treat as up-to-date to avoid an install loop on a malformed
		// local version.
		slog.Warn("update=bad_local_version", "version", dc.Version, "error", err.Error())
		s.backoff.recordSuccess()
		return
	}
	if !current.less(remote) {
		slog.Info("update=up_to_date", "current", current.String(), "remote", remote.String())
		s.backoff.recordSuccess()
		return
	}

	// Newer available. Download → verify → spawn → shutdown.
	tempPath, err := downloadMSI(s.ctx, s.client, rel.assetURL)
	if err != nil {
		delay := s.backoff.recordFailure()
		slog.Warn("update=download_failed", "url", rel.assetURL, "error", err.Error(), "backoff", delay.String())
		return
	}

	if err := verifyMSI(tempPath); err != nil {
		// Verification failure is NOT a transient network issue — could
		// be deliberate poisoning. Don't escalate backoff; stay on the
		// configured cadence.
		slog.Warn("update=refused", "path", tempPath, "error", err.Error())
		_ = os.Remove(tempPath)
		s.backoff.recordSuccess()
		return
	}

	// FR-011 v1: durable persistence deferred. The slog.Info line below
	// is the v1 record of the version transition; the file log keeps
	// 7 days of rotation.
	slog.Info("update=installing", "old", current.String(), "new", remote.String(), "path", tempPath)

	if err := spawnMSI(tempPath); err != nil {
		delay := s.backoff.recordFailure()
		slog.Warn("update=spawn_failed", "path", tempPath, "error", err.Error(), "backoff", delay.String())
		_ = os.Remove(tempPath)
		return
	}

	slog.Info("update=installed_pending_restart", "new", remote.String())
	triggerSelfShutdown(s.shutdownService)
	// Don't return from run() here — Stop() will drain when the service
	// ctx fires. The loop's next sleepCtx will see ctx.Done() and exit.
}

// downloadToTemp streams the asset to %TEMP%\drainctl-update-<random>.msi
// and returns the path. On any error, the partial file is cleaned up
// before returning.
func downloadToTemp(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("download: build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download: unexpected status %d", resp.StatusCode)
	}

	f, err := os.CreateTemp("", "drainctl-update-*.msi")
	if err != nil {
		return "", fmt.Errorf("download: CreateTemp: %w", err)
	}
	path := f.Name()
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("download: stream: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("download: close temp: %w", err)
	}
	return path, nil
}

// sleepCtx sleeps for d or until ctx is cancelled. Returns true if the
// full duration elapsed, false if ctx fired first (the caller should
// then exit the loop).
func (s *Subsystem) sleepCtx(d time.Duration) bool {
	if d <= 0 {
		return s.ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-s.ctx.Done():
		return false
	}
}

// jitter returns base ± rand([0, base/12]) so the spread is roughly
// ±8% of base (24h ± 2h for a 24h base). Per FR-003.
func jitter(base time.Duration) time.Duration {
	half := base / 12
	if half <= 0 {
		return base
	}
	offset := rand.Int64N(int64(2*half)) - int64(half) //nolint:gosec // jitter, not security-sensitive
	return base + time.Duration(offset)
}
