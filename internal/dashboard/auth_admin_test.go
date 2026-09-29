//go:build windows

package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newAdminTestStore(t *testing.T) *SessionStore {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return NewSessionStore(ctx)
}

func TestHandleNegotiate_StoresGroupDerivedAdmin(t *testing.T) {
	store := newAdminTestStore(t)
	handler := handleNegotiate(store, "Domain Admins")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/negotiate", nil)
	req = req.WithContext(context.WithValue(req.Context(), authInfoKey, &AuthInfo{
		Username: `CONTOSO\alice`,
		Groups:   []string{`CONTOSO\domain admins`},
	}))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	var body struct {
		Username string `json:"username"`
		IsAdmin  bool   `json:"is_admin"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Username != `CONTOSO\alice` || !body.IsAdmin {
		t.Errorf("response = %+v, want authenticated admin", body)
	}
	cookies := res.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	sess := store.Get(cookies[0].Value)
	if sess == nil || !sess.IsAdmin {
		t.Errorf("stored session = %#v, want group-derived admin", sess)
	}
}

func TestHandleMe_ProjectsServerIssuedAdminState(t *testing.T) {
	store := newAdminTestStore(t)
	token, err := store.Create(&AuthInfo{Username: `CONTOSO\bob`, Groups: []string{"Operators"}})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: "drainctl_session", Value: token})
	// An injected role-like context cannot elevate the browser session; /me is
	// projected only from the server-issued Session.
	req = req.WithContext(context.WithValue(req.Context(), authInfoKey, &AuthInfo{
		Username: `CONTOSO\bob`, Groups: []string{"Domain Admins"},
	}))
	res := httptest.NewRecorder()
	handleMe(store).ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	var body struct {
		User    string `json:"user"`
		IsAdmin bool   `json:"is_admin"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.User != `CONTOSO\bob` || body.IsAdmin {
		t.Errorf("response = %+v, want authenticated non-admin", body)
	}
}

func TestRequireAdmin_RejectsBeforeHandler(t *testing.T) {
	store := newAdminTestStore(t)
	nonAdmin, err := store.Create(&AuthInfo{Username: "operator"})
	if err != nil {
		t.Fatalf("create non-admin session: %v", err)
	}
	admin, err := store.CreateWithAdmin(&AuthInfo{Username: "administrator"}, true)
	if err != nil {
		t.Fatalf("create admin session: %v", err)
	}

	tests := []struct {
		name       string
		token      string
		wantStatus int
		wantCall   bool
	}{
		{name: "missing session", wantStatus: http.StatusUnauthorized},
		{name: "authenticated non-admin", token: nonAdmin, wantStatus: http.StatusForbidden},
		{name: "server-issued admin", token: admin, wantStatus: http.StatusNoContent, wantCall: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			handler := requireAdmin(store)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
			if tt.token != "" {
				req.AddCookie(&http.Cookie{Name: "drainctl_session", Value: tt.token})
			}
			if tt.name == "authenticated non-admin" {
				// A role-like value from the request cannot override the session.
				req = req.WithContext(context.WithValue(req.Context(), authInfoKey, &AuthInfo{
					Username: "operator", Groups: []string{"Domain Admins"},
				}))
			}
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)

			if res.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", res.Code, tt.wantStatus)
			}
			if called != tt.wantCall {
				t.Errorf("handler called = %v, want %v", called, tt.wantCall)
			}
			if tt.wantStatus == http.StatusForbidden && res.Body.String() != `{"error":"admin_required"}` {
				t.Errorf("forbidden body = %q", res.Body.String())
			}
		})
	}
}
