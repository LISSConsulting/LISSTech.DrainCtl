//go:build windows && devmode

package dashboard

import (
	_ "embed"
	"net/http"
)

//go:embed testdata/mock.js
var mockJS []byte

// registerMockRoute adds a /mock.js handler that serves the embedded mock
// data fixture. Only compiled in devmode builds (-tags devmode).
func registerMockRoute(mux *http.ServeMux) {
	mux.HandleFunc("GET /mock.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(mockJS)
	})
}
