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

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/etwids"
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
				slog.Int("event_id", etwids.EvtAccessDenied), "user", auth.Username)
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
// against the session store. Missing, invalid, and expired sessions all return
// the same generic 401 JSON response without WWW-Authenticate.
func requireSession(store *SessionStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie("drainctl_session")
			if err != nil || cookie.Value == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}
			sess := store.Get(cookie.Value)
			if sess == nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}
			info := &AuthInfo{Username: sess.Username, Groups: sess.Groups}
			ctx := context.WithValue(r.Context(), authInfoKey, info)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

type investigationSessionError string

const (
	investigationSessionExpired investigationSessionError = "session_expired"
	investigationAccessDenied   investigationSessionError = "access_denied"
)

// requireInvestigationDashboardSession authorizes feature routes from an
// existing session's login-time group snapshot. It deliberately does not
// consult the directory: membership changes take effect on reauthentication.
func requireInvestigationDashboardSession(store *SessionStore, currentGroup func() string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie("drainctl_session")
			if err != nil {
				writeInvestigationSessionError(w, http.StatusUnauthorized, investigationSessionExpired)
				return
			}
			sess, code := investigationDashboardSession(store, cookie.Value, currentGroup)
			if code != "" {
				status := http.StatusUnauthorized
				if code == investigationAccessDenied {
					status = http.StatusForbidden
				}
				writeInvestigationSessionError(w, status, code)
				return
			}
			info := &AuthInfo{Username: sess.Username, Groups: sess.Groups}
			ctx := context.WithValue(r.Context(), authInfoKey, info)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// investigationDashboardSession checks only in-memory session state and its
// login-time group snapshot against the configured group snapshot.
func investigationDashboardSession(store *SessionStore, token string, currentGroup func() string) (*Session, investigationSessionError) {
	if store == nil || token == "" {
		return nil, investigationSessionExpired
	}
	sess := store.Get(token)
	if sess == nil {
		return nil, investigationSessionExpired
	}
	if strings.HasSuffix(strings.TrimSpace(sess.Username), "$") {
		return nil, investigationAccessDenied
	}
	if !isMemberOf(sess.Groups, currentGroup()) {
		return nil, investigationAccessDenied
	}
	return sess, ""
}

// writeInvestigationSessionError is intentionally feature-local: established
// route middleware retains its original response bodies.
func writeInvestigationSessionError(w http.ResponseWriter, status int, code investigationSessionError) {
	var body string
	switch code {
	case investigationSessionExpired:
		body = `{"error":{"code":"session_expired"}}`
	case investigationAccessDenied:
		body = `{"error":{"code":"access_denied"}}`
	default:
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
