//go:build windows && devmode

package dashboard

import (
	"net/http"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// wrapAuth is a no-op in dev builds — skips SSPI authentication.
func wrapAuth(h http.Handler, _ string, log dc.LogFunc) http.Handler {
	return h
}

// wrapGroup is a no-op in dev builds — skips SSPI + group check.
func wrapGroup(h http.Handler, _ string, log dc.LogFunc) http.Handler {
	return h
}

func init() {
	// Dev mode warning printed once at package init.
	println("⚠️  DASHBOARD DEV MODE — SSPI authentication disabled. DO NOT use in production.")
}
