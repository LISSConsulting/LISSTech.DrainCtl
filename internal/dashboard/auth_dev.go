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

func init() {
	println("⚠️  DASHBOARD DEV MODE — SSPI authentication disabled. DO NOT use in production.")
}

// requireSession is a no-op in dev builds — skips session validation.
func requireSession(_ *SessionStore) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler { return h }
}

// requireMachineAccount is a no-op in dev builds — skips machine account check.
func requireMachineAccount(next http.Handler) http.Handler { return next }
