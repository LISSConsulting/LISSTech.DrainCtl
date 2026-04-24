//go:build windows

package dashboard

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"unsafe"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/etwids"
	"golang.org/x/sys/windows"
)

const (
	logon32LogonNetwork    = 3
	logon32ProviderDefault = 0
)

var (
	modAdvapi32    = windows.NewLazySystemDLL("advapi32.dll")
	procLogonUserW = modAdvapi32.NewProc("LogonUserW")
)

// loginLogonUser validates username/password via Windows LogonUserW and returns
// the authenticated identity. The password is never written to any log.
// Accepts DOMAIN\user and user@domain.com formats.
func loginLogonUser(username, password string) (*AuthInfo, error) {
	var user, domain string
	if idx := strings.LastIndex(username, `\`); idx >= 0 {
		domain = username[:idx]
		user = username[idx+1:]
	} else {
		// UPN (user@domain) or bare username — pass as-is with NULL domain.
		user = username
	}

	userPtr, err := windows.UTF16PtrFromString(user)
	if err != nil {
		return nil, fmt.Errorf("encode username: %w", err)
	}
	passPtr, err := windows.UTF16PtrFromString(password)
	if err != nil {
		return nil, fmt.Errorf("encode password: %w", err)
	}

	// NULL domain pointer for UPN/bare format; explicit pointer for DOMAIN\user.
	var domainArg uintptr
	if domain != "" {
		domainPtr, err := windows.UTF16PtrFromString(domain)
		if err != nil {
			return nil, fmt.Errorf("encode domain: %w", err)
		}
		domainArg = uintptr(unsafe.Pointer(domainPtr))
	}

	var tokenHandle windows.Token
	r1, _, callErr := procLogonUserW.Call(
		uintptr(unsafe.Pointer(userPtr)),
		domainArg,
		uintptr(unsafe.Pointer(passPtr)),
		logon32LogonNetwork,
		logon32ProviderDefault,
		uintptr(unsafe.Pointer(&tokenHandle)),
	)
	if r1 == 0 {
		return nil, callErr
	}
	defer func() { _ = tokenHandle.Close() }()

	tokenUser, err := tokenHandle.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("GetTokenUser: %w", err)
	}
	userName, domainName, _, err := tokenUser.User.Sid.LookupAccount("")
	if err != nil {
		return nil, fmt.Errorf("LookupAccount: %w", err)
	}
	fullUsername := userName
	if domainName != "" {
		fullUsername = domainName + `\` + userName
	}

	tokenGroups, err := tokenHandle.GetTokenGroups()
	if err != nil {
		slog.Warn("login: get token groups failed", "user", fullUsername, "error", err)
		return &AuthInfo{Username: fullUsername}, nil
	}
	var groups []string
	for _, g := range tokenGroups.AllGroups() {
		name, dom, _, gErr := g.Sid.LookupAccount("")
		if gErr != nil {
			continue
		}
		if dom != "" {
			groups = append(groups, dom+`\`+name)
		} else {
			groups = append(groups, name)
		}
	}
	return &AuthInfo{Username: fullUsername, Groups: groups}, nil
}

// sessionCookie builds the drainctl_session cookie for Set-Cookie responses.
// MaxAge is set to 8 hours to match the server-side inactivity timeout so the
// browser does not hold a stale token after the server has already reaped it.
func sessionCookie(token string, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     "drainctl_session",
		Value:    token,
		Path:     "/",
		MaxAge:   8 * 3600,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   secure,
	}
}

// isMemberOf reports whether groups contains group (case-insensitive,
// with and without DOMAIN\ prefix).
func isMemberOf(groups []string, group string) bool {
	for _, g := range groups {
		if strings.EqualFold(g, group) {
			return true
		}
		parts := strings.SplitN(g, `\`, 2)
		if len(parts) == 2 && strings.EqualFold(parts[1], group) {
			return true
		}
	}
	return false
}

// handleNegotiate is called after NegotiateMiddleware succeeds. Checks group
// membership, creates a session, sets cookie, and returns 200 {"username":"…"}.
func handleNegotiate(store *SessionStore, group string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := GetAuthInfo(r)
		if info == nil {
			http.Error(w, "not authenticated", http.StatusUnauthorized)
			return
		}
		if !isMemberOf(info.Groups, group) {
			slog.Warn("negotiate: access denied", slog.Int("event_id", etwids.EvtAccessDenied),
				"user", info.Username, "group", group)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"Access denied — your account is not authorized."}`))
			return
		}
		token, err := store.Create(info)
		if err != nil {
			slog.Error("negotiate: create session failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		slog.Info("negotiate: session created", slog.Int("event_id", etwids.EvtDashboardAccess),
			"user", info.Username)
		http.SetCookie(w, sessionCookie(token, r.TLS != nil))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"username": info.Username})
	})
}

// handleLogin validates explicit username/password credentials and creates a
// session on success.
func handleLogin(store *SessionStore, group string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 512))
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"bad request"}`))
			return
		}
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if jsonErr := json.Unmarshal(body, &req); jsonErr != nil || req.Username == "" || req.Password == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"username and password are required"}`))
			return
		}

		info, err := loginLogonUser(req.Username, req.Password)
		if err != nil {
			// Do not log the error detail — it may contain hints about the credential format.
			slog.Warn("login: credentials rejected", "user", req.Username)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"Wrong username or password."}`))
			return
		}

		if !isMemberOf(info.Groups, group) {
			slog.Warn("login: access denied", slog.Int("event_id", etwids.EvtAccessDenied),
				"user", info.Username, "group", group)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"Access denied — your account is not authorized."}`))
			return
		}

		token, err := store.Create(info)
		if err != nil {
			slog.Error("login: create session failed", "error", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"Authentication service unavailable — try again shortly."}`))
			return
		}

		slog.Info("login: session created", slog.Int("event_id", etwids.EvtDashboardAccess),
			"user", info.Username)
		http.SetCookie(w, sessionCookie(token, r.TLS != nil))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"username": info.Username})
	}
}

// handleLogout invalidates the current session and clears the session cookie.
// Always responds 200 (idempotent — absent or unknown cookie is not an error).
func handleLogout(store *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie("drainctl_session"); err == nil && cookie.Value != "" {
			store.Delete(cookie.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name:     "drainctl_session",
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			Secure:   r.TLS != nil,
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
}
