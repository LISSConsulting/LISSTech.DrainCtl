//go:build windows && !devmode

package dashboard

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"sync"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// internalToken is a random secret generated at process start. The service's
// own HTTP client sends this in the X-DrainCtl-Internal header to bypass SSPI
// when talking to the dashboard in the same process. This is NOT an IP-based
// bypass — the token is never exposed outside the process.
var (
	internalToken     string
	internalTokenOnce sync.Once
)

// InternalToken returns the process-scoped secret token. Safe for concurrent use.
func InternalToken() string {
	internalTokenOnce.Do(func() {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		internalToken = hex.EncodeToString(b)
	})
	return internalToken
}

const internalHeader = "X-DrainCtl-Internal"

// wrapAuth returns SSPI Negotiate middleware for production builds.
// Requests bearing the internal process token bypass SSPI.
func wrapAuth(h http.Handler, _ string, log dc.LogFunc) http.Handler {
	return internalBypass(h, NegotiateMiddleware(h, log))
}

// wrapGroup returns SSPI Negotiate + AD group check middleware for production builds.
// Requests bearing the internal process token bypass auth entirely.
func wrapGroup(h http.Handler, group string, log dc.LogFunc) http.Handler {
	return internalBypass(h, NegotiateMiddleware(RequireGroup(group, h, log), log))
}

// internalBypass checks for the process-scoped internal token header.
// If present and valid, the request is served directly (no SSPI, no group check).
// Otherwise the full auth chain runs.
func internalBypass(inner, authed http.Handler) http.Handler {
	token := InternalToken()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get(internalHeader); h != "" &&
			subtle.ConstantTimeCompare([]byte(h), []byte(token)) == 1 {
			inner.ServeHTTP(w, r)
			return
		}
		authed.ServeHTTP(w, r)
	})
}
