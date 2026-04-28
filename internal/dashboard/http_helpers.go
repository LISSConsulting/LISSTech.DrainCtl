//go:build windows

package dashboard

import (
	"encoding/json"
	"net/http"
)

// writeJSONError writes a JSON body {"error":"<code>"} with the given HTTP status.
func writeJSONError(w http.ResponseWriter, code string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}
