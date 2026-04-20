//go:build windows

package dashboard

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/alexbrainman/sspi/negotiate"
)

// newDashClient creates an HTTP client that pins the dashboard TLS certificate.
// If fingerprint is empty, it falls back to InsecureSkipVerify (first-use trust).
func newDashClient(fingerprint string) *http.Client {
	tlsCfg := &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // verified in VerifyPeerCertificate below
	}

	if fingerprint != "" {
		verifyFP := func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return fmt.Errorf("dashboard: no TLS certificate presented")
			}
			h := sha256.Sum256(rawCerts[0])
			got := hex.EncodeToString(h[:])
			if got != fingerprint {
				return fmt.Errorf("dashboard: certificate fingerprint mismatch (got %s, want %s)", got, fingerprint)
			}
			return nil
		}
		tlsCfg.VerifyPeerCertificate = verifyFP
		// Also set VerifyConnection to prevent resumed sessions from bypassing
		// the fingerprint check (G123).
		tlsCfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("dashboard: no TLS certificate in connection state")
			}
			h := sha256.Sum256(cs.PeerCertificates[0].Raw)
			got := hex.EncodeToString(h[:])
			if got != fingerprint {
				return fmt.Errorf("dashboard: certificate fingerprint mismatch (got %s, want %s)", got, fingerprint)
			}
			return nil
		}
	}

	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}
}

// dashClientPtr holds the current HTTP client atomically. Replaced at service
// start when the config has a tls_fingerprint.
var dashClientPtr atomic.Pointer[http.Client]

func init() {
	dashClientPtr.Store(newDashClient(""))
}

// InitDashClient configures the dashboard HTTP client with certificate pinning.
// Called during service startup when the dashboard URL is configured.
func InitDashClient(fingerprint string) {
	dashClientPtr.Store(newDashClient(fingerprint))
}

// RegisterResult holds the response from a successful registration.
type RegisterResult struct {
	TLSFingerprint string `json:"tls_fingerprint,omitempty"`
}

// Register notifies the dashboard server that this host exists.
// On success it returns the dashboard's TLS certificate fingerprint
// (if the dashboard is running HTTPS) so the caller can enable pinning.
func Register(dashboardURL string) (*RegisterResult, error) {
	hostname, err := hostName()
	if err != nil {
		slog.Warn("dashboard: cannot get hostname", "error", err)
		return nil, err
	}

	payload, _ := json.Marshal(map[string]string{"hostname": hostname})
	resp, err := negotiateRequest(http.MethodPost, dashboardURL+"/api/v1/register", payload)
	if err != nil {
		slog.Warn("dashboard: register failed", "error", err)
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		slog.Warn("dashboard: register rejected", "status", resp.StatusCode)
		return nil, fmt.Errorf("register: status %d", resp.StatusCode)
	}

	var result struct {
		OK             bool   `json:"ok"`
		TLSFingerprint string `json:"tls_fingerprint,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		// Non-fatal: registration succeeded even if we can't parse the fingerprint.
		// Drain the body so the HTTP transport can reuse the connection.
		_, _ = io.Copy(io.Discard, resp.Body)
		slog.Info("dashboard=registered", "url", dashboardURL)
		return &RegisterResult{}, nil
	}

	if result.TLSFingerprint != "" {
		slog.Info("dashboard=registered", "url", dashboardURL, "fingerprint", result.TLSFingerprint)
	} else {
		slog.Info("dashboard=registered", "url", dashboardURL)
	}
	return &RegisterResult{TLSFingerprint: result.TLSFingerprint}, nil
}

// ReportSpike pushes a single confirmed SpikePayload to the dashboard server
// so the central Recent Spikes ring renders spikes from remote agents. The
// local-dashboard code path (Subsystem.OnSpike → state.OnEvtSpikeIngest)
// handles same-process spikes already, so callers should skip this when
// dashState is non-nil. Errors are logged and swallowed — the agent also
// delivers the spike via notifications, so a transient dashboard outage never
// suppresses the alert itself.
func ReportSpike(dashboardURL string, spike *dc.SpikePayload) {
	payload, err := json.Marshal(spike)
	if err != nil {
		slog.Warn("dashboard: spike marshal failed", "error", err)
		return
	}
	resp, err := negotiateRequest(http.MethodPost, dashboardURL+"/api/v1/spike", payload)
	if err != nil {
		slog.Warn("dashboard: spike report failed", "error", err, "host", spike.Host)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Warn("dashboard: spike report rejected",
			"status", resp.StatusCode, "host", spike.Host, "channel", spike.Channel)
		return
	}
	slog.Info("dashboard=spike_reported",
		"host", spike.Host, "channel", spike.Channel,
		"observed", spike.Observed, "expected", spike.Expected)
}

// ReportState sends the latest CheckResult to the dashboard server.
// Errors are logged but never crash the service.
func ReportState(dashboardURL string, result *dc.CheckResult) {
	payload, err := json.Marshal(result)
	if err != nil {
		slog.Warn("dashboard: marshal failed", "error", err)
		return
	}

	resp, err := negotiateRequest(http.MethodPost, dashboardURL+"/api/v1/report", payload)
	if err != nil {
		slog.Warn("dashboard: report failed", "error", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Warn("dashboard: report rejected", "status", resp.StatusCode, "host", result.Host)
		return
	}
	slog.Info("dashboard=reported", "host", result.Host, "status", result.Status)
}

// negotiateRequest performs an HTTP request with SSPI Negotiate authentication.
// Makes an initial request, and if a 401 is returned, acquires an SSPI client
// token and retries with the Authorization header.
func negotiateRequest(method, rawURL string, body []byte) (*http.Response, error) {
	logDebug := func(msg string) {
		slog.Debug("negotiate: " + msg)
	}

	rawURL = rewriteLoopback(rawURL)
	logDebug(fmt.Sprintf("step=create_request method=%s url=%s", method, rawURL))
	req, err := http.NewRequest(method, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "DrainCtl/"+dc.Version)

	logDebug("step=initial_request")
	resp, err := dashClientPtr.Load().Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	logDebug(fmt.Sprintf("step=initial_response status=%d", resp.StatusCode))
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	logDebug("step=sspi_acquire_credentials")
	cred, err := negotiate.AcquireCurrentUserCredentials()
	if err != nil {
		return nil, fmt.Errorf("sspi acquire credentials: %w", err)
	}
	defer func() {
		logDebug("step=sspi_release_credentials")
		_ = cred.Release()
	}()

	spn := targetSPN(rawURL)
	logDebug(fmt.Sprintf("step=sspi_new_client_context spn=%s", spn))
	secCtx, token, err := negotiate.NewClientContext(cred, spn)
	if err != nil {
		return nil, fmt.Errorf("sspi client context: %w", err)
	}
	defer func() {
		logDebug("step=sspi_release_context")
		_ = secCtx.Release()
	}()

	logDebug(fmt.Sprintf("step=retry_request token_len=%d", len(token)))
	req, err = http.NewRequest(method, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create retry request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "DrainCtl/"+dc.Version)
	req.Header.Set("Authorization", "Negotiate "+base64.StdEncoding.EncodeToString(token))

	logDebug("step=retry_do")
	resp2, err := dashClientPtr.Load().Do(req)
	if err != nil {
		return nil, fmt.Errorf("http retry request: %w", err)
	}
	logDebug(fmt.Sprintf("step=retry_response status=%d", resp2.StatusCode))
	return resp2, nil
}

// targetSPN derives the HTTP service SPN from a URL, e.g. "HTTP/server.domain.com".
func targetSPN(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return "HTTP/" + u.Hostname()
}

// rewriteLoopback rewrites a URL to use 127.0.0.1 if the target hostname
// matches the local machine. This avoids IPv6 link-local resolution and
// enables NTLM loopback authentication via the same-machine SSPI path.
func rewriteLoopback(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	local, err := os.Hostname()
	if err != nil {
		return rawURL
	}
	if !strings.EqualFold(u.Hostname(), local) {
		return rawURL
	}
	port := u.Port()
	if port != "" {
		u.Host = "127.0.0.1:" + port
	} else {
		u.Host = "127.0.0.1"
	}
	return u.String()
}

// hostName returns the local machine hostname.
func hostName() (string, error) {
	return os.Hostname()
}

// RemoteSettings holds dashboard settings fetched by the service agent.
type RemoteSettings struct {
	Notifications           []dc.NotificationTarget `json:"notifications"`
	SessionWarningThreshold int                     `json:"session_warning_threshold"`
	GracePeriod             int                     `json:"grace_period"`
	Performance             *dc.PerformanceConfig   `json:"performance,omitempty"`
}

// FetchSettings retrieves dashboard settings via the agent config endpoint.
// Uses SSPI Negotiate auth and TLS pinning (same as Register/Report).
func FetchSettings(dashboardURL string) (*RemoteSettings, error) {
	resp, err := negotiateRequest(http.MethodGet, dashboardURL+"/api/v1/config", nil)
	if err != nil {
		slog.Warn("dashboard: fetch settings failed", "error", err)
		return nil, fmt.Errorf("fetch settings: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		slog.Warn("dashboard: fetch settings rejected", "status", resp.StatusCode)
		return nil, fmt.Errorf("fetch settings: status %d", resp.StatusCode)
	}

	var cfg RemoteSettings
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("decode settings: %w", err)
	}
	return &cfg, nil
}

// FetchServers queries the dashboard API for all registered servers.
// Used by the CLI's "dashboard list-servers" command (localhost only, no SSPI needed).
func FetchServers(url string) ([]ServerInfo, error) {
	resp, err := dashClientPtr.Load().Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var servers []ServerInfo
	if err := json.NewDecoder(resp.Body).Decode(&servers); err != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, err
	}
	return servers, nil
}

// RemoveServer sends a DELETE request to remove a server from the dashboard.
// Used by the CLI's "dashboard remove-server" command (localhost only).
func RemoveServer(url string) error {
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	resp, err := dashClientPtr.Load().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 && resp.StatusCode != 204 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
