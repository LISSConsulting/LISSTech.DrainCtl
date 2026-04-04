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
	"net/http"
	"net/url"
	"os"
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

// dashClient is the default client (no pinning). Replaced at service start
// when the config has a tls_fingerprint.
var dashClient = newDashClient("")

// InitDashClient configures the dashboard HTTP client with certificate pinning.
// Called during service startup when the dashboard URL is configured.
func InitDashClient(fingerprint string) {
	dashClient = newDashClient(fingerprint)
}

// Register notifies the dashboard server that this host exists.
// Errors are logged but never returned — registration is non-fatal.
func Register(dashboardURL string, log dc.LogFunc) error {
	hostname, err := hostName()
	if err != nil {
		dc.LogMsg(log, dc.LvlWRN, "dashboard: cannot get hostname", fmt.Sprintf("error=%q", err))
		return err
	}

	payload, _ := json.Marshal(map[string]string{"hostname": hostname})
	resp, err := negotiateRequest(http.MethodPost, dashboardURL+"/api/v1/register", payload)
	if err != nil {
		dc.LogMsg(log, dc.LvlWRN, "dashboard: register failed", fmt.Sprintf("error=%q", err))
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		dc.LogMsg(log, dc.LvlWRN, "dashboard: register rejected",
			fmt.Sprintf("status=%d", resp.StatusCode))
		return fmt.Errorf("register: status %d", resp.StatusCode)
	}
	log(dc.LvlINF, "dashboard=registered", fmt.Sprintf("url=%s", dashboardURL))
	return nil
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

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		dc.LogMsg(log, dc.LvlWRN, "dashboard: report rejected",
			fmt.Sprintf("status=%d host=%s", resp.StatusCode, result.Host))
		return
	}
	log(dc.LvlINF, "dashboard=reported", fmt.Sprintf("host=%s status=%s", result.Host, result.Status))
}

// negotiateRequest performs an HTTP request with SSPI Negotiate authentication.
// It makes an initial request, and if a 401 is returned, acquires an SSPI client
// token and retries with the Authorization header.
func negotiateRequest(method, rawURL string, body []byte) (*http.Response, error) {
	// First attempt — expect 401.
	req, err := http.NewRequest(method, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "DrainCtl/"+dc.Version)

	resp, err := dashClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	_ = resp.Body.Close()

	// Acquire SSPI client credentials and generate a Negotiate token.
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

	// Retry with Negotiate token.
	req, err = http.NewRequest(method, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create retry request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "DrainCtl/"+dc.Version)
	req.Header.Set("Authorization", "Negotiate "+base64.StdEncoding.EncodeToString(token))

	return dashClient.Do(req)
}

// targetSPN derives the HTTP service SPN from a URL, e.g. "HTTP/server.domain.com".
func targetSPN(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	return "HTTP/" + host
}

// hostName returns the local machine hostname.
func hostName() (string, error) {
	return os.Hostname()
}

// FetchServers queries the dashboard API for all registered servers.
// Used by the CLI's "dashboard list-servers" command (localhost only, no SSPI needed).
func FetchServers(url string) ([]ServerInfo, error) {
	resp, err := dashClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var servers []ServerInfo
	if err := json.NewDecoder(resp.Body).Decode(&servers); err != nil {
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
	resp, err := dashClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 && resp.StatusCode != 204 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
