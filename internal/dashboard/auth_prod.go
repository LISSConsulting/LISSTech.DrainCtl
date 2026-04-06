//go:build windows && !devmode

package dashboard

import (
	"context"
	"net"
	"net/http"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// wrapAuth returns SSPI Negotiate middleware for production builds.
// Loopback requests bypass SSPI — the local service is inherently trusted.
func wrapAuth(h http.Handler, _ string, log dc.LogFunc) http.Handler {
	return loopbackBypass(h, NegotiateMiddleware(h, log), log)
}

// wrapGroup returns SSPI Negotiate + AD group check middleware for production builds.
// Loopback requests bypass both SSPI and group check.
func wrapGroup(h http.Handler, group string, log dc.LogFunc) http.Handler {
	return loopbackBypass(h, NegotiateMiddleware(RequireGroup(group, h, log), log), log)
}

// loopbackBypass serves the inner handler directly (skipping auth) if the request
// originates from localhost. This avoids Windows NTLM loopback issues when the
// DrainCtl service registers/reports to a dashboard on the same machine.
// For non-loopback requests, the full auth chain is used.
func loopbackBypass(inner, authed http.Handler, log dc.LogFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			dc.LogMsg(log, dc.LvlDBG, "auth: loopback bypass", "remote="+r.RemoteAddr)
			// Inject a synthetic AuthInfo so downstream handlers (RequireGroup) pass.
			info := &AuthInfo{Username: "SYSTEM (loopback)"}
			ctx := context.WithValue(r.Context(), authInfoKey, info)
			inner.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		authed.ServeHTTP(w, r)
	})
}
