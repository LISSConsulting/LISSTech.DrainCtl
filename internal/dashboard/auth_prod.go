//go:build windows && !devmode

package dashboard

import (
	"context"
	"net/http"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// wrapAuth returns SSPI Negotiate middleware for production builds.
func wrapAuth(ctx context.Context, h http.Handler, _ string, log dc.LogFunc) http.Handler {
	return NegotiateMiddleware(ctx, h, log)
}

// wrapGroup returns SSPI Negotiate + AD group check middleware for production builds.
func wrapGroup(ctx context.Context, h http.Handler, group string, log dc.LogFunc) http.Handler {
	return NegotiateMiddleware(ctx, RequireGroup(group, h, log), log)
}
