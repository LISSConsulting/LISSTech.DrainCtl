//go:build windows && !devmode

package dashboard

import (
	"context"
	"net/http"
)

// wrapAuth returns SSPI Negotiate middleware for production builds.
func wrapAuth(ctx context.Context, h http.Handler, _ string) http.Handler {
	return NegotiateMiddleware(ctx, h)
}

// wrapGroup returns SSPI Negotiate + AD group check middleware for production builds.
func wrapGroup(ctx context.Context, h http.Handler, group string) http.Handler {
	return NegotiateMiddleware(ctx, RequireGroup(group, h))
}
