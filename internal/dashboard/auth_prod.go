//go:build windows && !devmode

package dashboard

import (
	"net/http"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// wrapAuth returns SSPI Negotiate middleware for production builds.
func wrapAuth(h http.Handler, _ string, log dc.LogFunc) http.Handler {
	return NegotiateMiddleware(h, log)
}

// wrapGroup returns SSPI Negotiate + AD group check middleware for production builds.
func wrapGroup(h http.Handler, group string, log dc.LogFunc) http.Handler {
	return NegotiateMiddleware(RequireGroup(group, h, log), log)
}

func init() {
	// Production: no warning needed.
}
