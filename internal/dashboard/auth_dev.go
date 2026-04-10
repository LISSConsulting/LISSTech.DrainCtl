//go:build windows && devmode

package dashboard

import (
	"context"
	"net/http"
)

// wrapAuth is a no-op in dev builds — skips SSPI authentication.
func wrapAuth(_ context.Context, h http.Handler, _ string) http.Handler {
	return h
}

// wrapGroup is a no-op in dev builds — skips SSPI + group check.
func wrapGroup(_ context.Context, h http.Handler, _ string) http.Handler {
	return h
}

func init() {
	println("⚠️  DASHBOARD DEV MODE — SSPI authentication disabled. DO NOT use in production.")
}
