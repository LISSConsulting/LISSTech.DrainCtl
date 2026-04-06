//go:build windows

package dashboard

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"runtime"
	"strings"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
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

// NegotiateMiddleware wraps an http.Handler with SSPI Negotiate (Kerberos/NTLM)
// authentication. Unauthenticated requests receive a 401 with a Negotiate challenge.
func NegotiateMiddleware(next http.Handler, log dc.LogFunc) http.Handler {
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

		cred, err := negotiate.AcquireServerCredentials("")
		if err != nil {
			dc.LogMsg(log, dc.LvlERR, "sspi: acquire credentials failed", fmt.Sprintf("error=%q", err))
			http.Error(w, "server auth error", http.StatusInternalServerError)
			return
		}
		defer func() { _ = cred.Release() }()

		sc, authDone, responseToken, err := negotiate.NewServerContext(cred, token)
		if err != nil {
			dc.LogMsg(log, dc.LvlWRN, "sspi: negotiate failed", fmt.Sprintf("error=%q", err))
			w.Header().Set("WWW-Authenticate", "Negotiate")
			http.Error(w, "authentication failed", http.StatusUnauthorized)
			return
		}
		defer func() { _ = sc.Release() }()

		if !authDone {
			// Multi-leg negotiation: send challenge back.
			if len(responseToken) > 0 {
				w.Header().Set("WWW-Authenticate",
					"Negotiate "+base64.StdEncoding.EncodeToString(responseToken))
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		username, err := sc.GetUsername()
		if err != nil {
			dc.LogMsg(log, dc.LvlWRN, "sspi: get username failed", fmt.Sprintf("error=%q", err))
			http.Error(w, "auth error", http.StatusUnauthorized)
			return
		}
		dc.LogMsg(log, dc.LvlDBG, "sspi: negotiate complete", fmt.Sprintf("user=%s", username))

		// Set response token if present.
		if len(responseToken) > 0 {
			w.Header().Set("WWW-Authenticate",
				"Negotiate "+base64.StdEncoding.EncodeToString(responseToken))
		}

		// Extract group memberships while sc is alive by impersonating on
		// this OS thread. LockOSThread ensures the impersonation token stays
		// on the thread that calls CheckTokenMembership.
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
		false, // open as client, not self
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
// The caller must wrap with NegotiateMiddleware first.
// Group membership is checked against AuthInfo.Groups (populated by
// NegotiateMiddleware), not via thread impersonation.
func RequireGroup(group string, next http.Handler, log dc.LogFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := GetAuthInfo(r)
		if auth == nil {
			http.Error(w, "not authenticated", http.StatusUnauthorized)
			return
		}

		// Check group membership from the pre-extracted groups list.
		// Match on "DOMAIN\Group" or just "Group" (case-insensitive).
		found := false
		for _, g := range auth.Groups {
			if strings.EqualFold(g, group) {
				found = true
				break
			}
			// Also match the bare group name (without domain prefix).
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
