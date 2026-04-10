//go:build windows

package dashboard

import (
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/alexbrainman/sspi"
	"github.com/alexbrainman/sspi/negotiate"
	"golang.org/x/sys/windows"
)

// AuthInfo holds the authenticated user's identity extracted from SSPI.
type AuthInfo struct {
	Username string
	Groups   []string
}

type contextKey string

const authInfoKey contextKey = "authInfo"

// GetAuthInfo retrieves the AuthInfo stored in the request context by
// NegotiateMiddleware. Returns nil if the request is not authenticated.
func GetAuthInfo(r *http.Request) *AuthInfo {
	if v := r.Context().Value(authInfoKey); v != nil {
		return v.(*AuthInfo)
	}
	return nil
}

// pendingCtx holds an in-progress NTLM multi-leg server context.
type pendingCtx struct {
	cred    *sspi.Credentials
	sc      *negotiate.ServerContext
	created time.Time
}

// NegotiateMiddleware wraps an http.Handler with SSPI Negotiate (Kerberos/NTLM)
// authentication. Supports multi-leg NTLM by keying pending contexts on the
// TCP connection's remote address (preserved across HTTP/1.1 keep-alive).
// The ctx parameter controls the lifetime of the background reaper goroutine.
func NegotiateMiddleware(ctx context.Context, next http.Handler) http.Handler {
	var pending sync.Map // remoteAddr → *pendingCtx

	// Reap stale contexts; exits when ctx is cancelled.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				// Release all pending contexts on shutdown.
				pending.Range(func(key, value any) bool {
					pc := value.(*pendingCtx)
					_ = pc.sc.Release()
					_ = pc.cred.Release()
					pending.Delete(key)
					return true
				})
				return
			case <-ticker.C:
				now := time.Now()
				pending.Range(func(key, value any) bool {
					pc := value.(*pendingCtx)
					if now.Sub(pc.created) > 60*time.Second {
						_ = pc.sc.Release()
						_ = pc.cred.Release()
						pending.Delete(key)
					}
					return true
				})
			}
		}
	}()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Negotiate ") {
			w.Header().Set("WWW-Authenticate", "Negotiate")
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}

		tokenB64 := strings.TrimPrefix(authHeader, "Negotiate ")
		token, err := base64.StdEncoding.DecodeString(tokenB64)
		if err != nil {
			slog.Warn("sspi: bad base64 token", "error", err)
			http.Error(w, "invalid token", http.StatusBadRequest)
			return
		}

		connKey := r.RemoteAddr

		// Check for an existing multi-leg context from a previous 401 exchange.
		var sc *negotiate.ServerContext
		var cred *sspi.Credentials
		var authDone bool
		var responseToken []byte

		if v, ok := pending.LoadAndDelete(connKey); ok {
			// Second leg: continue the existing context.
			pc := v.(*pendingCtx)
			cred = pc.cred
			sc = pc.sc
			authDone, responseToken, err = sc.Update(token)
			if err != nil {
				_ = sc.Release()
				_ = cred.Release()
				slog.Warn("sspi: negotiate leg2 failed", "error", err)
				w.Header().Set("WWW-Authenticate", "Negotiate")
				http.Error(w, "authentication failed", http.StatusUnauthorized)
				return
			}
		} else {
			// First leg: create new context.
			cred, err = negotiate.AcquireServerCredentials("")
			if err != nil {
				slog.Error("sspi: acquire credentials failed", "error", err)
				http.Error(w, "server auth error", http.StatusInternalServerError)
				return
			}
			sc, authDone, responseToken, err = negotiate.NewServerContext(cred, token)
			if err != nil {
				_ = cred.Release()
				slog.Warn("sspi: negotiate failed", "error", err)
				w.Header().Set("WWW-Authenticate", "Negotiate")
				http.Error(w, "authentication failed", http.StatusUnauthorized)
				return
			}
		}

		if !authDone {
			// Multi-leg: store context for the next request on this connection.
			pending.Store(connKey, &pendingCtx{cred: cred, sc: sc, created: time.Now()})
			if len(responseToken) > 0 {
				w.Header().Set("WWW-Authenticate",
					"Negotiate "+base64.StdEncoding.EncodeToString(responseToken))
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		// Auth complete — clean up.
		defer func() {
			_ = sc.Release()
			_ = cred.Release()
		}()

		if len(responseToken) > 0 {
			w.Header().Set("WWW-Authenticate",
				"Negotiate "+base64.StdEncoding.EncodeToString(responseToken))
		}

		// Extract username and groups by impersonating. GetUsername() doesn't
		// work for NTLM contexts, so we always use the impersonation path.
		username, groups := extractIdentity(sc)
		if username == "" {
			http.Error(w, "auth error", http.StatusUnauthorized)
			return
		}
		slog.Debug("sspi: negotiate complete", "user", username)

		info := &AuthInfo{Username: username, Groups: groups}
		ctx := context.WithValue(r.Context(), authInfoKey, info)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// extractIdentity impersonates the SSPI client on the current OS thread,
// reads the username and group memberships from the thread token, then reverts.
// Works for both Kerberos and NTLM contexts.
func extractIdentity(sc *negotiate.ServerContext) (string, []string) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := sc.ImpersonateUser(); err != nil {
		slog.Warn("sspi: impersonate failed", "error", err)
		return "", nil
	}
	defer func() { _ = sc.RevertToSelf() }()

	var token windows.Token
	err := windows.OpenThreadToken(
		windows.CurrentThread(),
		windows.TOKEN_QUERY,
		false,
		&token,
	)
	if err != nil {
		slog.Warn("sspi: open thread token failed", "error", err)
		return "", nil
	}
	defer func() { _ = token.Close() }()

	// Get username from the token's user SID.
	tokenUser, err := token.GetTokenUser()
	if err != nil {
		slog.Warn("sspi: get token user failed", "error", err)
		return "", nil
	}
	userName, domainName, _, err := tokenUser.User.Sid.LookupAccount("")
	if err != nil {
		slog.Warn("sspi: lookup account failed", "error", err)
		return "", nil
	}
	username := userName
	if domainName != "" {
		username = domainName + `\` + userName
	}

	// Get groups from the token.
	tokenGroups, err := token.GetTokenGroups()
	if err != nil {
		slog.Warn("sspi: get token groups failed", "user", username, "error", err)
		return username, nil
	}

	var groups []string
	for _, g := range tokenGroups.AllGroups() {
		name, domain, _, err := g.Sid.LookupAccount("")
		if err != nil {
			continue
		}
		if domain != "" {
			groups = append(groups, domain+`\`+name)
		} else {
			groups = append(groups, name)
		}
	}
	return username, groups
}

// RequireGroup wraps an http.Handler and rejects requests where the
// authenticated user is not a member of the specified Windows group.
func RequireGroup(group string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := GetAuthInfo(r)
		if auth == nil {
			http.Error(w, "not authenticated", http.StatusUnauthorized)
			return
		}

		found := false
		for _, g := range auth.Groups {
			if strings.EqualFold(g, group) {
				found = true
				break
			}
			parts := strings.SplitN(g, `\`, 2)
			if len(parts) == 2 && strings.EqualFold(parts[1], group) {
				found = true
				break
			}
		}

		if !found {
			slog.Warn("sspi: access denied", "user", auth.Username, "group", group)
			http.Error(w, "access denied: not a member of "+group, http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}
