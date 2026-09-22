package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xsaveopt/recodarr/internal/auth"
)

func (e *testEnv) postFrom(t *testing.T, path, remote string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(http.MethodPost, path, nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		r = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	}
	r.RemoteAddr = remote + ":1234"
	r.Header.Set("X-Recodarr", "1")
	if e.cookie != nil {
		r.AddCookie(e.cookie)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, r)
	return w
}

func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CookieName {
			return c
		}
	}
	t.Fatalf("no session cookie in the response")
	return nil
}

func TestAuthStatusOnAFreshInstall(t *testing.T) {
	env := newTestEnv(t)
	w := env.do(t, http.MethodGet, "/api/auth/status", nil)
	wantStatus(t, w, http.StatusOK)
	got := decodeJSON[authStatusDTO](t, w)
	if got.Setup || got.Authed || got.Username != "" {
		t.Fatalf("got %+v, want an unconfigured install", got)
	}
}

func TestAuthStatusIsReachableWithoutACookie(t *testing.T) {
	env := newTestEnv(t).login(t)
	env.cookie = nil
	w := env.do(t, http.MethodGet, "/api/auth/status", nil)
	wantStatus(t, w, http.StatusOK)
	got := decodeJSON[authStatusDTO](t, w)
	if !got.Setup {
		t.Fatal("got setup=false with an admin present")
	}
	if got.Authed {
		t.Fatal("got authed=true without a session cookie")
	}
}

func TestSetupCreatesTheAdminAndLogsIn(t *testing.T) {
	env := newTestEnv(t)
	w := env.do(t, http.MethodPost, "/api/auth/setup",
		credsDTO{Username: "admin", Password: "hunter2"})
	wantStatus(t, w, http.StatusOK)
	c := sessionCookie(t, w)
	if c.Value == "" || !c.HttpOnly {
		t.Fatalf("setup returned %+v, want a live HttpOnly session", c)
	}

	env.cookie = c
	status := decodeJSON[authStatusDTO](t, env.do(t, http.MethodGet, "/api/auth/status", nil))
	if !status.Setup || !status.Authed || status.Username != "admin" {
		t.Fatalf("got %+v, want a logged-in admin right after setup", status)
	}
}

func TestSetupIsRefusedTwice(t *testing.T) {
	env := newTestEnv(t)
	wantStatus(t, env.do(t, http.MethodPost, "/api/auth/setup",
		credsDTO{Username: "admin", Password: "hunter2"}), http.StatusOK)
	w := env.do(t, http.MethodPost, "/api/auth/setup",
		credsDTO{Username: "attacker", Password: "hunter2"})
	wantStatus(t, w, http.StatusConflict)
}

func TestSetupRejectsBlankCredentialsAndBadJSON(t *testing.T) {
	env := newTestEnv(t)
	wantStatus(t, env.do(t, http.MethodPost, "/api/auth/setup",
		credsDTO{Username: "", Password: "hunter2"}), http.StatusBadRequest)

	r := httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewReader([]byte("not json")))
	r.Header.Set("X-Recodarr", "1")
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, r)
	wantStatus(t, w, http.StatusBadRequest)
}

func TestSetupRequiresTheCsrfHeader(t *testing.T) {
	env := newTestEnv(t)
	raw, err := json.Marshal(credsDTO{Username: "admin", Password: "hunter2"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewReader(raw))
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, r)
	wantStatus(t, w, http.StatusForbidden)

	has, err := auth.New(env.store.DB).HasAdmin(t.Context())
	if err != nil {
		t.Fatalf("has admin: %v", err)
	}
	if has {
		t.Fatal("a request without the CSRF header still created the admin")
	}
}

func TestLoginAcceptsTheRightPasswordAndRejectsTheWrongOne(t *testing.T) {
	env := newTestEnv(t)
	wantStatus(t, env.postFrom(t, "/api/auth/setup", "10.1.0.1",
		credsDTO{Username: "admin", Password: "hunter2"}), http.StatusOK)
	env.cookie = nil

	bad := env.postFrom(t, "/api/auth/login", "10.1.0.1",
		credsDTO{Username: "admin", Password: "wrong"})
	wantStatus(t, bad, http.StatusUnauthorized)
	if body := bad.Body.String(); !bytes.Contains([]byte(body), []byte("invalid credentials")) {
		t.Fatalf("got %q, want a generic rejection", body)
	}

	good := env.postFrom(t, "/api/auth/login", "10.1.0.1",
		credsDTO{Username: "admin", Password: "hunter2"})
	wantStatus(t, good, http.StatusOK)
	if sessionCookie(t, good).Value == "" {
		t.Fatal("a successful login issued no session")
	}
}

func TestLoginThrottlesAfterRepeatedFailures(t *testing.T) {
	env := newTestEnv(t)
	wantStatus(t, env.postFrom(t, "/api/auth/setup", "10.2.0.1",
		credsDTO{Username: "admin", Password: "hunter2"}), http.StatusOK)
	env.cookie = nil

	for range 4 {
		w := env.postFrom(t, "/api/auth/login", "10.2.0.1",
			credsDTO{Username: "admin", Password: "wrong"})
		wantStatus(t, w, http.StatusUnauthorized)
	}
	w := env.postFrom(t, "/api/auth/login", "10.2.0.1",
		credsDTO{Username: "admin", Password: "hunter2"})
	wantStatus(t, w, http.StatusTooManyRequests)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("a throttled login carried no Retry-After header")
	}

	other := env.postFrom(t, "/api/auth/login", "10.2.0.9",
		credsDTO{Username: "admin", Password: "hunter2"})
	wantStatus(t, other, http.StatusOK)
}

func TestSuccessfulLoginClearsTheThrottle(t *testing.T) {
	env := newTestEnv(t)
	wantStatus(t, env.postFrom(t, "/api/auth/setup", "10.3.0.1",
		credsDTO{Username: "admin", Password: "hunter2"}), http.StatusOK)
	env.cookie = nil

	for range 3 {
		wantStatus(t, env.postFrom(t, "/api/auth/login", "10.3.0.1",
			credsDTO{Username: "admin", Password: "wrong"}), http.StatusUnauthorized)
	}
	wantStatus(t, env.postFrom(t, "/api/auth/login", "10.3.0.1",
		credsDTO{Username: "admin", Password: "hunter2"}), http.StatusOK)

	for range 3 {
		wantStatus(t, env.postFrom(t, "/api/auth/login", "10.3.0.1",
			credsDTO{Username: "admin", Password: "wrong"}), http.StatusUnauthorized)
	}
	wantStatus(t, env.postFrom(t, "/api/auth/login", "10.3.0.1",
		credsDTO{Username: "admin", Password: "hunter2"}), http.StatusOK)
}

func TestLogoutInvalidatesTheSession(t *testing.T) {
	env := newTestEnv(t).login(t)
	before := env.do(t, http.MethodGet, "/api/stats", nil)
	wantStatus(t, before, http.StatusOK)

	w := env.do(t, http.MethodPost, "/api/auth/logout", nil)
	wantStatus(t, w, http.StatusNoContent)
	if c := sessionCookie(t, w); c.Value != "" || c.MaxAge >= 0 {
		t.Fatalf("logout returned %+v, want the cookie cleared", c)
	}

	after := env.do(t, http.MethodGet, "/api/stats", nil)
	wantStatus(t, after, http.StatusUnauthorized)
}

func TestLogoutWithoutASessionIsHarmless(t *testing.T) {
	env := newTestEnv(t)
	wantStatus(t, env.do(t, http.MethodPost, "/api/auth/logout", nil), http.StatusNoContent)
}

func TestProtectedRoutesRejectAnonymousCallers(t *testing.T) {
	env := newTestEnv(t)
	for _, path := range []string{"/api/stats", "/api/jobs", "/api/settings/", "/api/profiles/"} {
		w := env.do(t, http.MethodGet, path, nil)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s got %d, want 401 for an anonymous caller", path, w.Code)
		}
	}
}

func TestHealthEndpointIsOpen(t *testing.T) {
	env := newTestEnv(t)
	w := env.do(t, http.MethodGet, "/health", nil)
	wantStatus(t, w, http.StatusOK)
	if got := w.Body.String(); got != "up" {
		t.Fatalf("got %q, want up", got)
	}
}

func TestClientIPFallsBackToRemoteAddr(t *testing.T) {
	for _, tc := range []struct{ remote, want string }{
		{"10.0.0.7:1234", "10.0.0.7"},
		{"[::1]:9999", "[::1]"},
		{"10.0.0.7", "10.0.0.7"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = tc.remote
		if got := clientIP(r); got != tc.want {
			t.Fatalf("clientIP(%q) = %q, want %q", tc.remote, got, tc.want)
		}
	}
}
