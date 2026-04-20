//go:build windows

package dashboard

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// wrapAuth returns SSPI Negotiate middleware for production builds.
func wrapAuth(ctx context.Context, h http.Handler, _ string) http.Handler {
	return NegotiateMiddleware(ctx, h)
}

// requireMachineAccount rejects requests where the authenticated SSPI principal
// is not a Windows machine account. Machine accounts have a sAMAccountName
// ending with '$' (e.g. CST\CST-ISLAB-PC3$). Human user accounts do not.
func requireMachineAccount(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := GetAuthInfo(r)
		if auth == nil {
			http.Error(w, "not authenticated", http.StatusUnauthorized)
			return
		}
		name := auth.Username
		if idx := strings.LastIndex(name, `\`); idx >= 0 {
			name = name[idx+1:]
		}
		if !strings.HasSuffix(name, "$") {
			if isLocalSystemLoopback(r, auth) {
				next.ServeHTTP(w, r)
				return
			}
			slog.Warn("sspi: agent route rejected non-machine account",
				slog.Int("event_id", dc.EvtAccessDenied), "user", auth.Username)
			msg, _ := json.Marshal(map[string]string{
				"error": "machine account required: this endpoint accepts only COMPUTERNAME$ principals; " + auth.Username + " is a human user account",
			})
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write(msg)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLocalSystemLoopback(r *http.Request, auth *AuthInfo) bool {
	if auth == nil || !strings.EqualFold(auth.Username, `NT AUTHORITY\SYSTEM`) {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isLocalSystemForHost(r *http.Request, auth *AuthInfo, hostname string) bool {
	if !isLocalSystemLoopback(r, auth) {
		return false
	}
	local, err := os.Hostname()
	if err != nil {
		return false
	}
	return strings.EqualFold(local, hostname)
}

// requireSession returns middleware that validates the drainctl_session cookie
// against the session store. Returns 401 JSON on missing or expired sessions.
// No WWW-Authenticate header is set — prevents the browser credential dialog.
func requireSession(store *SessionStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie("drainctl_session")
			if err != nil || cookie.Value == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"session expired"}`))
				return
			}
			sess := store.Get(cookie.Value)
			if sess == nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"session expired"}`))
				return
			}
			info := &AuthInfo{Username: sess.Username, Groups: sess.Groups}
			ctx := context.WithValue(r.Context(), authInfoKey, info)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
