//go:build windows

package dashboard

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"unsafe"

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

		// Set response token if present.
		if len(responseToken) > 0 {
			w.Header().Set("WWW-Authenticate",
				"Negotiate "+base64.StdEncoding.EncodeToString(responseToken))
		}

		info := &AuthInfo{Username: username}
		ctx := context.WithValue(r.Context(), authInfoKey, info)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireGroup wraps an http.Handler and rejects requests where the
// authenticated user is not a member of the specified Windows group.
// The caller must wrap with NegotiateMiddleware first.
func RequireGroup(group string, next http.Handler, log dc.LogFunc) http.Handler {
	// Resolve the group SID once at setup time.
	groupSID, _, _, err := windows.LookupSID("", group)
	if err != nil {
		dc.LogMsg(log, dc.LvlERR, "sspi: cannot resolve group SID",
			fmt.Sprintf("group=%q error=%q", group, err))
		// Return a handler that always rejects — broken config.
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "server configuration error", http.StatusInternalServerError)
		})
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := GetAuthInfo(r)
		if auth == nil {
			http.Error(w, "not authenticated", http.StatusUnauthorized)
			return
		}

		member, err := isGroupMember(groupSID)
		if err != nil {
			dc.LogMsg(log, dc.LvlWRN, "sspi: group check failed",
				fmt.Sprintf("user=%s group=%s error=%q", auth.Username, group, err))
			http.Error(w, "access denied", http.StatusForbidden)
			return
		}
		if !member {
			dc.LogMsg(log, dc.LvlWRN, "sspi: access denied",
				fmt.Sprintf("user=%s group=%s", auth.Username, group))
			http.Error(w, "access denied: not a member of "+group, http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// isGroupMember checks whether the current thread token (after SSPI
// impersonation) is a member of the specified group SID.
func isGroupMember(groupSID *windows.SID) (bool, error) {
	var token windows.Token
	err := windows.OpenThreadToken(
		windows.CurrentThread(),
		windows.TOKEN_QUERY,
		true, // open as self
		&token,
	)
	if err != nil {
		// Fall back to process token if no thread token.
		err = windows.OpenProcessToken(
			windows.CurrentProcess(),
			windows.TOKEN_QUERY,
			&token,
		)
		if err != nil {
			return false, fmt.Errorf("open token: %w", err)
		}
	}
	defer func() { _ = token.Close() }()

	var isMember int32
	err = checkTokenMembership(token, groupSID, &isMember)
	if err != nil {
		return false, fmt.Errorf("CheckTokenMembership: %w", err)
	}
	return isMember != 0, nil
}

var modAdvapi32 = windows.NewLazySystemDLL("advapi32.dll")
var procCheckTokenMembership = modAdvapi32.NewProc("CheckTokenMembership")

func checkTokenMembership(token windows.Token, sidToCheck *windows.SID, isMember *int32) error {
	r1, _, err := procCheckTokenMembership.Call(
		uintptr(token),
		uintptr(unsafe.Pointer(sidToCheck)),
		uintptr(unsafe.Pointer(isMember)),
	)
	if r1 == 0 {
		return err
	}
	return nil
}
