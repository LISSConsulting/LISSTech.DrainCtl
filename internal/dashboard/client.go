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
		tlsCfg.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
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
func Register(dashboardURL string, log dc.LogFunc) (*RegisterResult, error) {
	hostname, err := hostName()
	if err != nil {
		dc.LogMsg(log, dc.LvlWRN, "dashboard: cannot get hostname", fmt.Sprintf("error=%q", err))
		return nil, err
	}

	payload, _ := json.Marshal(map[string]string{"hostname": hostname})
	resp, err := negotiateRequest(http.MethodPost, dashboardURL+"/api/v1/register", payload)
	if err != nil {
		dc.LogMsg(log, dc.LvlWRN, "dashboard: register failed", fmt.Sprintf("error=%q", err))
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		dc.LogMsg(log, dc.LvlWRN, "dashboard: register rejected",
			fmt.Sprintf("status=%d", resp.StatusCode))
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
		log(dc.LvlINF, "dashboard=registered", fmt.Sprintf("url=%s (fingerprint unavailable)", dashboardURL))
		return &RegisterResult{}, nil
	}

	if result.TLSFingerprint != "" {
		log(dc.LvlINF, "dashboard=registered", fmt.Sprintf("url=%s fingerprint=%s", dashboardURL, result.TLSFingerprint))
	} else {
		log(dc.LvlINF, "dashboard=registered", fmt.Sprintf("url=%s", dashboardURL))
	}
	return &RegisterResult{TLSFingerprint: result.TLSFingerprint}, nil
}

// ReportState sends the latest CheckResult to the dashboard server.
// Errors are logged but never crash the service.
func ReportState(dashboardURL string, result *dc.CheckResult, log dc.LogFunc) {
	payload, err := json.Marshal(result)
	if err != nil {
		dc.LogMsg(log, dc.LvlWRN, "dashboard: marshal failed", fmt.Sprintf("error=%q", err))
		return
	}

	resp, err := negotiateRequest(http.MethodPost, dashboardURL+"/api/v1/report", payload)
	if err != nil {
		dc.LogMsg(log, dc.LvlWRN, "dashboard: report failed", fmt.Sprintf("error=%q", err))
		return
	}
	defer func() { _ = resp.Body.Close() }()

	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		dc.LogMsg(log, dc.LvlWRN, "dashboard: report rejected",
			fmt.Sprintf("status=%d host=%s", resp.StatusCode, result.Host))
		return
	}
	log(dc.LvlINF, "dashboard=reported", fmt.Sprintf("host=%s status=%s", result.Host, result.Status))
}

// negotiateRequest performs an HTTP request with SSPI Negotiate authentication.
// Makes an initial request, and if a 401 is returned, acquires an SSPI client
// token and retries with the Authorization header.
func negotiateRequest(method, rawURL string, body []byte) (*http.Response, error) {
	rawURL = rewriteLoopback(rawURL)
	req, err := http.NewRequest(method, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "DrainCtl/"+dc.Version)

	resp, err := dashClientPtr.Load().Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	cred, err := negotiate.AcquireCurrentUserCredentials()
	if err != nil {
		return nil, fmt.Errorf("sspi acquire credentials: %w", err)
	}
	defer func() { _ = cred.Release() }()

	spn := targetSPN(rawURL)
	secCtx, token, err := negotiate.NewClientContext(cred, spn)
	if err != nil {
		return nil, fmt.Errorf("sspi client context: %w", err)
	}
	defer func() { _ = secCtx.Release() }()

	req, err = http.NewRequest(method, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create retry request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "DrainCtl/"+dc.Version)
	req.Header.Set("Authorization", "Negotiate "+base64.StdEncoding.EncodeToString(token))

	return dashClientPtr.Load().Do(req)
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

// RemoteNotifyConfig holds notification configuration fetched from the dashboard.
type RemoteNotifyConfig struct {
	Notifications           []dc.NotificationTarget `json:"notifications"`
	SessionWarningThreshold int                     `json:"session_warning_threshold"`
	GracePeriod             int                     `json:"grace_period"`
}

// FetchNotifyConfig retrieves the notification configuration from the dashboard.
// Uses SSPI Negotiate auth and TLS pinning (same as Register/Report).
func FetchNotifyConfig(dashboardURL string, log dc.LogFunc) (*RemoteNotifyConfig, error) {
	resp, err := negotiateRequest(http.MethodGet, dashboardURL+"/api/v1/notify-config", nil)
	if err != nil {
		dc.LogMsg(log, dc.LvlWRN, "dashboard: fetch notify config failed", fmt.Sprintf("error=%q", err))
		return nil, fmt.Errorf("fetch notify config: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		dc.LogMsg(log, dc.LvlWRN, "dashboard: fetch notify config rejected",
			fmt.Sprintf("status=%d", resp.StatusCode))
		return nil, fmt.Errorf("fetch notify config: status %d", resp.StatusCode)
	}

	var cfg RemoteNotifyConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("decode notify config: %w", err)
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
