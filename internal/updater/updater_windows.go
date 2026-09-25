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
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"sort"
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
//
// verifyManifest is the strong-binding check (Ed25519 signature over a
// JSON manifest plus SHA-256 over the downloaded MSI). It runs BEFORE
// verifyMSI so a forged-CN-but-correctly-Authenticode-signed MSI still
// fails. When releaseSigningKeys is empty the seam is a no-op — see the
// transition contract in keys_windows.go.
var (
	fetchRelease   = fetchLatestRelease
	verifyManifest = verifyManifestRemote
	verifyMSI      = verifyAuthenticode
	spawnMSI       = spawnInstall
	downloadMSI    = downloadToTemp
)

// decodeKeys is the seam used by Start so tests can inject a fake key
// list (or a fake decode error) without touching releaseSigningKeysB64.
var decodeKeys = func() ([]ed25519.PublicKey, error) {
	return decodeReleaseSigningKeys(releaseSigningKeysB64)
}

// manifestMaxBytes caps how large a signed manifest or signature blob can
// be before we refuse to read it. The manifest is a small JSON document
// (~200 bytes); the signature is exactly 64 bytes. Cap is generous to
// allow future schema growth, tight enough to bound memory on a
// poisoned-server response.
const manifestMaxBytes = 64 * 1024

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
	client *http.Client

	cfgMu sync.RWMutex
	cfg   dc.UpdateConfig

	// wakeCh signals the poll loop to abandon its current sleep and
	// immediately re-evaluate cfg + run a tick. Used by UpdateConfig so
	// an operator's enabled=false → true flip is picked up within
	// seconds rather than at the next poll_interval tick.
	wakeCh chan struct{}

	// runMu serializes periodic and forced checks. Both paths mutate ETag and
	// backoff state, and allowing them to overlap can launch two installers.
	runMu sync.Mutex

	// Mutable updater state. Access is serialized by runMu for both periodic
	// and operator-triggered checks.
	etag    string
	backoff backoff

	// signingKeys is the decoded form of keys_windows.go's
	// releaseSigningKeysB64. Populated by Start (not package init) so a
	// malformed entry is reported as a Start error per LCI rules. Empty
	// list means manifest verification is disabled (transition mode).
	signingKeys []ed25519.PublicKey

	// Lifecycle.
	wg       sync.WaitGroup
	ctx      context.Context
	cancel   context.CancelFunc
	stopOnce sync.Once
}

// New constructs the Subsystem. Does not launch any goroutines —
// goroutines are owned by Start per the LCI contract.
func New(cfg dc.UpdateConfig) *Subsystem {
	return &Subsystem{
		cfg:    cfg,
		client: &http.Client{Timeout: httpClientTimeout},
		wakeCh: make(chan struct{}, 1),
	}
}

// UpdateConfig replaces the in-memory cfg when the policy changed. Called by
// local config reloads and dashboard settings refreshes. A real change wakes
// the poll loop so enabled, channel, and cadence updates take effect promptly;
// an identical dashboard refresh is a no-op and does not cause an extra GitHub
// poll.
func (s *Subsystem) UpdateConfig(cfg dc.UpdateConfig) {
	s.cfgMu.Lock()
	if s.cfg == cfg {
		s.cfgMu.Unlock()
		return
	}
	s.cfg = cfg
	s.cfgMu.Unlock()
	// Non-blocking send: if the goroutine is mid-tick or already has a
	// pending wake (buffered 1), we drop this signal — the next tick
	// will see the new cfg either way.
	select {
	case s.wakeCh <- struct{}{}:
	default:
	}
}

// snapshot returns a value copy of the current cfg. Held briefly under
// the read lock; the value copy is safe to inspect without further
// synchronization.
func (s *Subsystem) snapshot() dc.UpdateConfig {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

const (
	forceUpdateIdempotencyWindow = 24 * time.Hour
	forceUpdateCommandLedgerCap  = 256
)

func (s *Subsystem) TriggerCheckNow(ctx context.Context, commandID string) tickOutcome {
	s.runMu.Lock()
	defer s.runMu.Unlock()

	now := time.Now().UTC()
	state, err := loadUpdateState()
	if err != nil {
		return tickOutcome{Decision: "error", Reason: "read_command_ledger:" + err.Error()}
	}
	pruneForceUpdateCommands(&state, now)
	if _, exists := state.ForceUpdateCommands[commandID]; exists {
		return tickOutcome{Decision: "duplicate", Reason: "idempotent_replay"}
	}
	state.ForceUpdateCommands[commandID] = now
	if err := saveUpdateState(state); err != nil {
		return tickOutcome{Decision: "error", Reason: "record_command:" + err.Error()}
	}
	return s.runTick(ctx, true)
}

func pruneForceUpdateCommands(state *updateState, now time.Time) {
	if state.ForceUpdateCommands == nil {
		state.ForceUpdateCommands = make(map[string]time.Time)
		return
	}
	cutoff := now.Add(-forceUpdateIdempotencyWindow)
	for id, seenAt := range state.ForceUpdateCommands {
		if seenAt.Before(cutoff) {
			delete(state.ForceUpdateCommands, id)
		}
	}
	if len(state.ForceUpdateCommands) < forceUpdateCommandLedgerCap {
		return
	}
	ids := make([]string, 0, len(state.ForceUpdateCommands))
	for id := range state.ForceUpdateCommands {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		left, right := state.ForceUpdateCommands[ids[i]], state.ForceUpdateCommands[ids[j]]
		return left.Before(right) || (left.Equal(right) && ids[i] < ids[j])
	})
	for _, id := range ids[:len(ids)-forceUpdateCommandLedgerCap+1] {
		delete(state.ForceUpdateCommands, id)
	}
}

// Start implements lifecycle.Subsystem. Always launches the poll
// goroutine. When cfg.Enabled is false, the goroutine sleeps and
// re-checks on every tick — it never makes a GitHub call until the
// operator flips Enabled=true via UpdateConfig (file-watcher path) or
// service restart with the new config. Operators get "edit config.json
// and reload within seconds" semantics that match the documentation,
// at the cost of one always-parked goroutine on opt-out hosts.
//
// Synchronous init failure (malformed embedded signing key) is returned
// before any goroutine launches per LCI §"Errors".
func (s *Subsystem) Start(ctx context.Context) error {
	keys, err := decodeKeys()
	if err != nil {
		return fmt.Errorf("updater: decode release signing keys: %w", err)
	}
	s.signingKeys = keys

	// Seed the replay-defense pin from the running binary's version.
	// Best-effort: failure here is logged inside seedHighestSeenFromVersion
	// and never blocks Start.
	seedHighestSeenFromVersion()

	s.ctx, s.cancel = context.WithCancel(ctx)
	s.wg.Add(1)
	go s.run()
	cfg := s.snapshot()
	slog.Info("update=started",
		"enabled", cfg.Enabled,
		"channel", cfg.Channel,
		"poll_interval", time.Duration(cfg.PollInterval).String(),
		"signing_keys", len(s.signingKeys))
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

// tickOutcome is the structured result of a single tick. Returned by
// the synchronous TriggerCheckNow path so the service loop can post a
// completion report back to the dashboard without parsing slog lines.
// The decision string mirrors the human-readable decision messages
// already used in the slog logging (e.g. "up_to_date", "not_modified",
// "installer_spawned", "no_stable_release", "refused"); the "error" decision
// is reserved for transport / verification failures and the err field carries
// the underlying message.
type tickOutcome struct {
	Decision string // up_to_date | not_modified | no_stable_release | installer_spawned | refused | error | disabled | duplicate
	Reason   string // free-form: slog message, error text, refusal stage
	OldVer   string // empty when not applicable
	NewVer   string // empty when not applicable
}

// TickOutcome completes a tick() that ran successfully with one of the
// terminal decisions (not a transport error). Used by TriggerCheckNow
// to bridge the updater's decision vocabulary into the dashboard's
// ForceUpdateCompletion outcome vocabulary.
//
// As of this commit the public TriggerCheckNow path wraps the
// decision in the dashboard-side ForceUpdateCompletionOutcome string,
// so TickOutcome is consumed indirectly. Kept exported so callers (and
// tests) can introspect the raw decision before the wrapper applies.
func (o tickOutcome) String() string {
	if o.Reason == "" {
		return o.Decision
	}
	return o.Decision + ":" + o.Reason
}

// runTick runs one poll synchronously against the supplied context and
// returns the structured outcome. Exposed via TriggerCheckNow for the
// dashboard's force-update path; the periodic background loop also
// calls it (after the initial sleep) so both paths share one
// implementation. runTick takes a context so the caller (the periodic
// loop's goroutine OR the synchronous trigger path) controls the
// deadline; the background goroutine passes s.ctx, the dashboard path
// passes its own ctx (typically the parent svc ctx with a tighter bound).
func (s *Subsystem) runTick(ctx context.Context, forced bool) tickOutcome {
	cfg := s.snapshot()
	if !forced && !cfg.Enabled {
		slog.Debug("update=disabled tick_skip")
		return tickOutcome{Decision: "disabled"}
	}

	slog.Debug("update=poll_start", "current", dc.Version, "channel", cfg.Channel)

	rel, err := fetchRelease(ctx, s.client, cfg.Channel, s.etag)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return tickOutcome{Decision: "cancelled"}
		}
		delay := s.backoff.recordFailure()
		slog.Warn("update=poll_failed", "error", err.Error(), "backoff", delay.String(), "failures", s.backoff.failureCount())
		return tickOutcome{Decision: "error", Reason: err.Error()}
	}

	switch {
	case rel.notModified:
		slog.Debug("update=not_modified", "etag", s.etag)
		s.backoff.recordSuccess()
		return tickOutcome{Decision: "not_modified"}
	case rel.noReleases:
		slog.Info("update=no_stable_release", "channel", cfg.Channel)
		s.backoff.recordSuccess()
		return tickOutcome{Decision: "no_stable_release"}
	}

	s.etag = rel.etag

	remote, err := parseVersion(rel.tag)
	if err != nil {
		slog.Warn("update=bad_tag", "tag", rel.tag, "error", err.Error())
		s.backoff.recordSuccess()
		return tickOutcome{Decision: "refused", Reason: "bad_tag:" + err.Error()}
	}
	current, err := parseVersion(dc.Version)
	if err != nil {
		slog.Warn("update=bad_local_version", "version", dc.Version, "error", err.Error())
		s.backoff.recordSuccess()
		return tickOutcome{Decision: "refused", Reason: "bad_local_version"}
	}

	if state, _ := loadUpdateState(); state.HighestSeenVersion != "" {
		if highSeen, err := parseVersion(state.HighestSeenVersion); err == nil {
			if remote.less(highSeen) && !remote.equals(highSeen) {
				slog.Warn("update=refused",
					"stage", "replay",
					"highest_seen", highSeen.String(),
					"remote", remote.String())
				s.backoff.recordSuccess()
				return tickOutcome{Decision: "refused", Reason: "replay_fence"}
			}
		}
	}

	if !current.less(remote) {
		slog.Info("update=up_to_date", "current", current.String(), "remote", remote.String())
		s.backoff.recordSuccess()
		return tickOutcome{Decision: "up_to_date", OldVer: current.String(), NewVer: remote.String()}
	}

	// Newer available. Download → verify → spawn. The MSI owns service restart.
	tempPath, err := downloadMSI(ctx, s.client, rel.assetURL)
	if err != nil {
		delay := s.backoff.recordFailure()
		slog.Warn("update=download_failed", "url", rel.assetURL, "error", err.Error(), "backoff", delay.String())
		return tickOutcome{Decision: "error", Reason: "download:" + err.Error()}
	}

	if err := verifyManifest(ctx, s.client, tempPath, rel.manifestURL, rel.sigURL, s.signingKeys); err != nil {
		slog.Warn("update=refused", "path", tempPath, "stage", "manifest", "error", err.Error())
		_ = os.Remove(tempPath)
		s.backoff.recordSuccess()
		return tickOutcome{Decision: "refused", Reason: "manifest:" + err.Error()}
	}

	if err := verifyMSI(tempPath); err != nil {
		slog.Warn("update=refused", "path", tempPath, "stage", "authenticode", "error", err.Error())
		_ = os.Remove(tempPath)
		s.backoff.recordSuccess()
		return tickOutcome{Decision: "refused", Reason: "authenticode:" + err.Error()}
	}

	slog.Info("update=installing", "old", current.String(), "new", remote.String(), "path", tempPath)

	if err := spawnMSI(tempPath); err != nil {
		delay := s.backoff.recordFailure()
		slog.Warn("update=spawn_failed", "path", tempPath, "error", err.Error(), "backoff", delay.String())
		_ = os.Remove(tempPath)
		return tickOutcome{Decision: "error", Reason: "spawn:" + err.Error()}
	}

	// A successful process spawn is not a successful installation. Keep the
	// replay-defense pin at the running binary's version and clear the ETag so
	// a still-running old service can fetch and retry this release later.
	s.etag = ""
	s.backoff.recordSuccess()
	slog.Info("update=installer_spawned", "new", remote.String())
	return tickOutcome{
		Decision: "installer_spawned",
		OldVer:   current.String(),
		NewVer:   remote.String(),
	}
}

func (s *Subsystem) tick() {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	_ = s.runTick(s.ctx, false)
}

// verifyManifestRemote enforces Ed25519-signed manifest + SHA-256 hash
// binding. It is a no-op when keys is empty (transition mode: no keys
// embedded in keys_windows.go yet).
//
// When keys is non-empty, every release MUST publish a release.json and
// release.json.sig sidecar; absence of either is treated as a refusal,
// not a downgrade. That's the whole point of the strong-binding check —
// being optional defeats it.
func verifyManifestRemote(ctx context.Context, client *http.Client, msiPath, manifestURL, sigURL string, keys []ed25519.PublicKey) error {
	if len(keys) == 0 {
		return nil
	}
	if manifestURL == "" || sigURL == "" {
		return fmt.Errorf("verifyManifest: release missing %q and/or %q sidecar", manifestAssetName, sigAssetName)
	}
	manifestBytes, err := downloadAssetBytes(ctx, client, manifestURL)
	if err != nil {
		return fmt.Errorf("verifyManifest: fetch manifest: %w", err)
	}
	sigBytes, err := downloadAssetBytes(ctx, client, sigURL)
	if err != nil {
		return fmt.Errorf("verifyManifest: fetch sig: %w", err)
	}
	m, err := VerifyReleaseManifest(manifestBytes, sigBytes, keys)
	if err != nil {
		return err
	}
	if err := VerifyAssetMatchesManifest(msiPath, msiAssetName, m); err != nil {
		return err
	}
	return nil
}

// downloadAssetBytes fetches a small release sidecar (manifest or sig)
// fully into memory. Caps the read at manifestMaxBytes to bound a
// poisoned-server response.
func downloadAssetBytes(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, manifestMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(b) > manifestMaxBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", manifestMaxBytes)
	}
	return b, nil
}

// downloadToTemp streams the asset to the updates subdirectory beneath the
// canonical ProgramData root and returns the path. On any error, the partial
// file is cleaned up before returning.
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

	if err := os.MkdirAll(dc.DefaultUpdatesDir(), 0o755); err != nil {
		return "", fmt.Errorf("download: create updates dir: %w", err)
	}
	f, err := os.CreateTemp(dc.DefaultUpdatesDir(), "drainctl-update-*.msi")
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

// sleepCtx sleeps for d, returning early on ctx cancel OR a wakeCh
// signal from UpdateConfig. Returns true if the timer fired or wakeCh
// fired (caller should proceed with the next tick), false if ctx fired
// (caller should exit the loop).
func (s *Subsystem) sleepCtx(d time.Duration) bool {
	if d <= 0 {
		return s.ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-s.wakeCh:
		// UpdateConfig flipped Enabled or Channel; abandon the rest of
		// this sleep and re-evaluate immediately.
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
