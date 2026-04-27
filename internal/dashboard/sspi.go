//go:build windows

package dashboard

import (
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/etwids"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sspimetrics"
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

// sspiReleaser is the subset of *negotiate.ServerContext used by the
// pending-map machinery. Lets tests substitute a counter-based fake without
// constructing a real Win32 SSPI handle.
type sspiReleaser interface {
	Release() error
}

// pendingCtx holds an in-progress NTLM multi-leg server context.
// In production sc and rel point at the same *negotiate.ServerContext.
// In tests sc may be nil — the orphan/reaper paths only call rel.Release().
type pendingCtx struct {
	sc      *negotiate.ServerContext
	rel     sspiReleaser
	created time.Time
}

// releasePendingContext releases the kernel handle owned by pc and updates
// the live-context counter. Idempotent only at the level of "was this pc
// already released" — caller must not invoke twice on the same pc.
//
// The counters live in internal/sspimetrics (a leaf package both
// dashboard and selfmetrics import). Putting them there avoids the
// layering inversion that existed when selfmetrics imported dashboard
// just to read these atomics.
func releasePendingContext(pc *pendingCtx) {
	_ = pc.rel.Release()
	sspimetrics.Live.Add(-1)
}

// acquireServerCredentials is a package var so tests can inject a fake.
// Per-request invocation leaks LSASS-side state via Win32 AcquireCredentialsHandle;
// see sharedServerCred for the once-and-reuse contract.
var acquireServerCredentials = negotiate.AcquireServerCredentials

// sharedServerCred lazily acquires SSPI server credentials on first use and
// reuses the handle for every subsequent caller. Acquiring credentials per
// request is the documented anti-pattern that grows lsass.exe linearly.
// Errors are not cached: a failed acquire is retried on the next call so a
// transient LSASS hiccup at startup doesn't permanently disable auth.
type sharedServerCred struct {
	acquire func(string) (*sspi.Credentials, error)
	mu      sync.Mutex
	cred    *sspi.Credentials
}

func (s *sharedServerCred) get() (*sspi.Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cred != nil {
		return s.cred, nil
	}
	cred, err := s.acquire("")
	if err != nil {
		return nil, err
	}
	s.cred = cred
	return s.cred, nil
}

func (s *sharedServerCred) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cred != nil {
		_ = s.cred.Release()
		s.cred = nil
	}
}

// NegotiateMiddleware wraps an http.Handler with SSPI Negotiate (Kerberos/NTLM)
// authentication. Supports multi-leg NTLM by keying pending contexts on the
// TCP connection's remote address (preserved across HTTP/1.1 keep-alive).
// The ctx parameter controls the lifetime of the background reaper goroutine.
//
// SSPI server credentials are acquired once on first request and shared across
// every ServerContext for the lifetime of the middleware. Per-request acquisition
// leaks lsass.exe state at ~2.5 MB/h on hosts with active dashboard auth.
func NegotiateMiddleware(ctx context.Context, next http.Handler) http.Handler {
	creds := &sharedServerCred{acquire: acquireServerCredentials}
	var pending sync.Map // remoteAddr → *pendingCtx

	// Reap stale contexts; exits when ctx is cancelled.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				// Release all pending contexts and the shared cred on shutdown.
				// CompareAndDelete: a concurrent leg-2 handler may have won the
				// entry between Range yielding it and our cleanup. CAS-fail
				// means the handler owns it now; we must NOT release.
				pending.Range(func(key, value any) bool {
					if pending.CompareAndDelete(key, value) {
						releasePendingContext(value.(*pendingCtx))
						sspimetrics.Pending.Add(-1)
					}
					return true
				})
				creds.release()
				return
			case <-ticker.C:
				now := time.Now()
				pending.Range(func(key, value any) bool {
					pc := value.(*pendingCtx)
					if now.Sub(pc.created) > 60*time.Second {
						if pending.CompareAndDelete(key, value) {
							releasePendingContext(pc)
							sspimetrics.Pending.Add(-1)
						}
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
		var authDone bool
		var responseToken []byte

		if v, ok := pending.LoadAndDelete(connKey); ok {
			// Second leg: continue the existing context. Pending count drops;
			// live count is unchanged (the same sc that was counted at first leg).
			sspimetrics.Pending.Add(-1)
			pc := v.(*pendingCtx)
			sc = pc.sc
			authDone, responseToken, err = sc.Update(token)
			if err != nil {
				releasePendingContext(pc)
				slog.Warn("sspi: negotiate leg2 failed", "error", err)
				w.Header().Set("WWW-Authenticate", "Negotiate")
				http.Error(w, "authentication failed", http.StatusUnauthorized)
				return
			}
		} else {
			// First leg: create new context using the shared cred.
			cred, errCred := creds.get()
			if errCred != nil {
				slog.Error("sspi: acquire credentials failed", "error", errCred)
				http.Error(w, "server auth error", http.StatusInternalServerError)
				return
			}
			sc, authDone, responseToken, err = negotiate.NewServerContext(cred, token)
			if err != nil {
				slog.Warn("sspi: negotiate failed", "error", err)
				w.Header().Set("WWW-Authenticate", "Negotiate")
				http.Error(w, "authentication failed", http.StatusUnauthorized)
				return
			}
			sspimetrics.Live.Add(1)
		}

		if !authDone {
			// Multi-leg: store context for the next request on this connection.
			// Use Swap (not Store) to detect a duplicate first-leg arriving on
			// a connKey that already has a pending entry — without releasing
			// the displaced ServerContext, its kernel handle leaks forever
			// because the reaper only walks current map entries.
			newPC := &pendingCtx{sc: sc, rel: sc, created: time.Now()}
			if prev, loaded := pending.Swap(connKey, newPC); loaded {
				releasePendingContext(prev.(*pendingCtx))
			} else {
				sspimetrics.Pending.Add(1)
			}
			if len(responseToken) > 0 {
				w.Header().Set("WWW-Authenticate",
					"Negotiate "+base64.StdEncoding.EncodeToString(responseToken))
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		// Auth complete — release the per-request server context.
		defer func() {
			_ = sc.Release()
			sspimetrics.Live.Add(-1)
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
			slog.Warn("sspi: access denied", slog.Int("event_id", etwids.EvtAccessDenied), "user", auth.Username, "group", group)
			http.Error(w, "access denied: not a member of "+group, http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}
