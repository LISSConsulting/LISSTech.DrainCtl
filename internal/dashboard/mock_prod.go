//go:build windows && !devmode

package dashboard

import "net/http"

// registerMockRoute is a no-op in production builds.
func registerMockRoute(_ *http.ServeMux) {}
