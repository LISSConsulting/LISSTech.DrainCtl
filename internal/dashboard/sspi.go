//go:build windows

package dashboard

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
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
func NegotiateMiddleware(next http.Handler, log dc.LogFunc) http.Handler {
	var pending sync.Map // remoteAddr → *pendingCtx

	// Reap stale contexts every 30 seconds.
	go func() {
		for {
			time.Sleep(30 * time.Second)
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
			dc.LogMsg(log, dc.LvlWRN, "sspi: bad base64 token", fmt.Sprintf("error=%q", err))
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
				dc.LogMsg(log, dc.LvlWRN, "sspi: negotiate leg2 failed", fmt.Sprintf("error=%q", err))
				w.Header().Set("WWW-Authenticate", "Negotiate")
				http.Error(w, "authentication failed", http.StatusUnauthorized)
				return
			}
		} else {
			// First leg: create new context.
			cred, err = negotiate.AcquireServerCredentials("")
			if err != nil {
				dc.LogMsg(log, dc.LvlERR, "sspi: acquire credentials failed", fmt.Sprintf("error=%q", err))
				http.Error(w, "server auth error", http.StatusInternalServerError)
				return
			}
			sc, authDone, responseToken, err = negotiate.NewServerContext(cred, token)
			if err != nil {
				_ = cred.Release()
				dc.LogMsg(log, dc.LvlWRN, "sspi: negotiate failed", fmt.Sprintf("error=%q", err))
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

		username, err := sc.GetUsername()
		if err != nil {
			dc.LogMsg(log, dc.LvlWRN, "sspi: get username failed", fmt.Sprintf("error=%q", err))
			http.Error(w, "auth error", http.StatusUnauthorized)
			return
		}
		dc.LogMsg(log, dc.LvlDBG, "sspi: negotiate complete", fmt.Sprintf("user=%s", username))

		if len(responseToken) > 0 {
			w.Header().Set("WWW-Authenticate",
				"Negotiate "+base64.StdEncoding.EncodeToString(responseToken))
		}

		groups := extractGroups(sc, log, username)

		info := &AuthInfo{Username: username, Groups: groups}
		ctx := context.WithValue(r.Context(), authInfoKey, info)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// extractGroups impersonates the SSPI client on the current OS thread,
// reads the token's group SIDs, reverts, and returns group names.
func extractGroups(sc *negotiate.ServerContext, log dc.LogFunc, username string) []string {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := sc.ImpersonateUser(); err != nil {
		dc.LogMsg(log, dc.LvlWRN, "sspi: impersonate failed",
			fmt.Sprintf("user=%s error=%q", username, err))
		return nil
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
		dc.LogMsg(log, dc.LvlWRN, "sspi: open thread token failed",
			fmt.Sprintf("user=%s error=%q", username, err))
		return nil
	}
	defer func() { _ = token.Close() }()

	tokenGroups, err := token.GetTokenGroups()
	if err != nil {
		dc.LogMsg(log, dc.LvlWRN, "sspi: get token groups failed",
			fmt.Sprintf("user=%s error=%q", username, err))
		return nil
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
	return groups
}

// RequireGroup wraps an http.Handler and rejects requests where the
// authenticated user is not a member of the specified Windows group.
func RequireGroup(group string, next http.Handler, log dc.LogFunc) http.Handler {
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
			dc.LogMsg(log, dc.LvlWRN, "sspi: access denied",
				fmt.Sprintf("user=%s group=%s", auth.Username, group))
			http.Error(w, "access denied: not a member of "+group, http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}
