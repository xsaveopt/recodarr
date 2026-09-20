package auth

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/xsaveopt/recodarr/internal/store"
)

func openTestDB(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func openTestAuth(t *testing.T) *Store {
	t.Helper()
	return New(openTestDB(t).DB)
}

func seedAdminRow(t *testing.T, st *store.Store) int64 {
	t.Helper()
	res, err := st.DB.Exec(
		`INSERT INTO admin_users (username, password_hash) VALUES ('admin', 'x')`)
	if err != nil {
		t.Fatalf("seed admin row: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("admin row id: %v", err)
	}
	return id
}

func TestHasAdminIsFalseOnAFreshDatabase(t *testing.T) {
	a := openTestAuth(t)
	has, err := a.HasAdmin(context.Background())
	if err != nil {
		t.Fatalf("has admin: %v", err)
	}
	if has {
		t.Fatal("a fresh database reports an admin")
	}
	name, err := a.AdminUsername(context.Background())
	if err != nil {
		t.Fatalf("admin username: %v", err)
	}
	if name != "" {
		t.Fatalf("got username %q, want empty", name)
	}
}

func TestCreateAdminRejectsBlankCredentials(t *testing.T) {
	a := openTestAuth(t)
	ctx := context.Background()
	for _, tc := range []struct{ user, pass string }{
		{"", "hunter2"},
		{"admin", ""},
		{"", ""},
	} {
		if err := a.CreateAdmin(ctx, tc.user, tc.pass); err == nil {
			t.Fatalf("CreateAdmin(%q, %q) was accepted", tc.user, tc.pass)
		}
	}
	has, _ := a.HasAdmin(ctx)
	if has {
		t.Fatal("a rejected setup still created an admin")
	}
}

func TestCreateAdminIsSingleAdminOnly(t *testing.T) {
	a := openTestAuth(t)
	ctx := context.Background()
	if err := a.CreateAdmin(ctx, "admin", "hunter2"); err != nil {
		t.Fatalf("first CreateAdmin: %v", err)
	}
	err := a.CreateAdmin(ctx, "second", "hunter2")
	if !errors.Is(err, ErrAlreadySetup) {
		t.Fatalf("got %v, want ErrAlreadySetup", err)
	}
	name, err := a.AdminUsername(ctx)
	if err != nil {
		t.Fatalf("admin username: %v", err)
	}
	if name != "admin" {
		t.Fatalf("got username %q, want the first admin", name)
	}
}

func TestPasswordIsStoredAsABcryptHash(t *testing.T) {
	st := openTestDB(t)
	a := New(st.DB)
	if err := a.CreateAdmin(context.Background(), "admin", "hunter2"); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	var hash string
	if err := st.DB.QueryRow(`SELECT password_hash FROM admin_users`).Scan(&hash); err != nil {
		t.Fatalf("read hash: %v", err)
	}
	if hash == "hunter2" {
		t.Fatal("the password was stored in the clear")
	}
	if len(hash) < 20 || hash[:4] != "$2a$" {
		t.Fatalf("got %q, want a bcrypt hash", hash)
	}
	if hash[4:7] != "12$" {
		t.Fatalf("got cost prefix %q, want cost 12", hash[4:7])
	}
}

func TestVerifyPassword(t *testing.T) {
	a := openTestAuth(t)
	ctx := context.Background()
	if err := a.CreateAdmin(ctx, "admin", "hunter2"); err != nil {
		t.Fatalf("create admin: %v", err)
	}

	id, err := a.VerifyPassword(ctx, "admin", "hunter2")
	if err != nil {
		t.Fatalf("correct password rejected: %v", err)
	}
	if id == 0 {
		t.Fatal("got user id 0 for a valid login")
	}

	if _, err := a.VerifyPassword(ctx, "admin", "wrong"); !errors.Is(err, ErrBadCredential) {
		t.Fatalf("got %v for a wrong password, want ErrBadCredential", err)
	}
	if _, err := a.VerifyPassword(ctx, "nobody", "hunter2"); !errors.Is(err, ErrBadCredential) {
		t.Fatalf("got %v for an unknown user, want ErrBadCredential", err)
	}
	if _, err := a.VerifyPassword(ctx, "ADMIN", "hunter2"); !errors.Is(err, ErrBadCredential) {
		t.Fatalf("got %v for a case-mismatched user, want ErrBadCredential", err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	st := openTestDB(t)
	a := New(st.DB)
	uid := seedAdminRow(t, st)
	ctx := context.Background()

	tok, exp, err := a.CreateSession(ctx, uid)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if len(tok) != 64 {
		t.Fatalf("got a %d-character token, want 64 hex characters", len(tok))
	}
	for _, c := range tok {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Fatalf("token %q is not lowercase hex", tok)
		}
	}
	if d := time.Until(exp); d < SessionLifetime-time.Minute || d > SessionLifetime+time.Minute {
		t.Fatalf("got expiry in %v, want roughly %v", d, SessionLifetime)
	}

	got, err := a.LookupSession(ctx, tok)
	if err != nil {
		t.Fatalf("lookup session: %v", err)
	}
	if got != uid {
		t.Fatalf("got user id %d, want %d", got, uid)
	}

	if err := a.DeleteSession(ctx, tok); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if _, err := a.LookupSession(ctx, tok); !errors.Is(err, ErrBadCredential) {
		t.Fatalf("got %v after delete, want ErrBadCredential", err)
	}
}

func TestSessionTokensAreDistinct(t *testing.T) {
	st := openTestDB(t)
	a := New(st.DB)
	uid := seedAdminRow(t, st)
	ctx := context.Background()
	seen := make(map[string]bool)
	for range 32 {
		tok, _, err := a.CreateSession(ctx, uid)
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		if seen[tok] {
			t.Fatalf("token %q was issued twice", tok)
		}
		seen[tok] = true
	}
}

func TestLookupSessionRejectsBlankAndUnknownTokens(t *testing.T) {
	a := openTestAuth(t)
	ctx := context.Background()
	for _, tok := range []string{"", "deadbeef", "not-a-token"} {
		if _, err := a.LookupSession(ctx, tok); !errors.Is(err, ErrBadCredential) {
			t.Fatalf("LookupSession(%q) returned %v, want ErrBadCredential", tok, err)
		}
	}
}

func TestExpiredSessionIsRejectedAndDeleted(t *testing.T) {
	st := openTestDB(t)
	a := New(st.DB)
	uid := seedAdminRow(t, st)
	ctx := context.Background()

	tok, _, err := a.CreateSession(ctx, uid)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := st.DB.ExecContext(ctx,
		`UPDATE sessions SET expires_at = ? WHERE token = ?`,
		time.Now().Add(-time.Hour), tok); err != nil {
		t.Fatalf("age the session: %v", err)
	}

	if _, err := a.LookupSession(ctx, tok); !errors.Is(err, ErrBadCredential) {
		t.Fatalf("got %v for an expired session, want ErrBadCredential", err)
	}
	var n int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE token = ?`, tok).Scan(&n); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if n != 0 {
		t.Fatal("an expired session survived the lookup that rejected it")
	}
}

func TestPurgeExpiredSessionsKeepsLiveOnes(t *testing.T) {
	st := openTestDB(t)
	a := New(st.DB)
	uid := seedAdminRow(t, st)
	ctx := context.Background()

	live, _, err := a.CreateSession(ctx, uid)
	if err != nil {
		t.Fatalf("create live session: %v", err)
	}
	stale, _, err := a.CreateSession(ctx, uid)
	if err != nil {
		t.Fatalf("create stale session: %v", err)
	}
	if _, err := st.DB.ExecContext(ctx,
		`UPDATE sessions SET expires_at = ? WHERE token = ?`,
		time.Now().Add(-time.Hour), stale); err != nil {
		t.Fatalf("age the session: %v", err)
	}

	if err := a.PurgeExpiredSessions(ctx); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, err := a.LookupSession(ctx, live); err != nil {
		t.Fatalf("the live session was purged: %v", err)
	}
	var n int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE token = ?`, stale).Scan(&n); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if n != 0 {
		t.Fatal("the expired session survived the purge")
	}
}

func TestResetAdminWipesAdminAndSessions(t *testing.T) {
	st := openTestDB(t)
	a := New(st.DB)
	ctx := context.Background()

	uid := seedAdminRow(t, st)
	tok, _, err := a.CreateSession(ctx, uid)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := a.ResetAdmin(ctx); err != nil {
		t.Fatalf("reset admin: %v", err)
	}
	has, err := a.HasAdmin(ctx)
	if err != nil {
		t.Fatalf("has admin: %v", err)
	}
	if has {
		t.Fatal("the admin survived the reset")
	}
	if _, err := a.LookupSession(ctx, tok); !errors.Is(err, ErrBadCredential) {
		t.Fatalf("got %v, want the session to be gone after a reset", err)
	}
	if err := a.CreateAdmin(ctx, "fresh", "hunter2"); err != nil {
		t.Fatalf("setup after reset was refused: %v", err)
	}
}

func TestSetAndClearSessionCookie(t *testing.T) {
	exp := time.Now().Add(time.Hour)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	SetSessionCookie(w, r, "tok", exp)
	c := readCookie(t, w)
	if c.Value != "tok" || c.Path != "/" {
		t.Fatalf("unexpected cookie %+v", c)
	}
	if !c.HttpOnly {
		t.Fatal("the session cookie is not HttpOnly")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("got SameSite %v, want Strict", c.SameSite)
	}
	if c.Secure {
		t.Fatal("Secure was set on a plain http request")
	}

	w = httptest.NewRecorder()
	ClearSessionCookie(w, r)
	c = readCookie(t, w)
	if c.Value != "" || c.MaxAge >= 0 {
		t.Fatalf("clearing produced %+v, want an empty expiring cookie", c)
	}
}

func TestSessionCookieIsSecureBehindTLSAndProxies(t *testing.T) {
	direct := httptest.NewRequest(http.MethodGet, "https://example.test/", nil)
	direct.TLS = &tls.ConnectionState{}

	fwd := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	fwd.Header.Set("X-Forwarded-Proto", "https")

	plain := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	plain.Header.Set("X-Forwarded-Proto", "http")

	for name, tc := range map[string]struct {
		r    *http.Request
		want bool
	}{
		"direct tls":      {direct, true},
		"forwarded https": {fwd, true},
		"forwarded plain": {plain, false},
	} {
		w := httptest.NewRecorder()
		SetSessionCookie(w, tc.r, "tok", time.Now().Add(time.Hour))
		if got := readCookie(t, w).Secure; got != tc.want {
			t.Fatalf("%s: got Secure=%v, want %v", name, got, tc.want)
		}
		w = httptest.NewRecorder()
		ClearSessionCookie(w, tc.r)
		if got := readCookie(t, w).Secure; got != tc.want {
			t.Fatalf("%s clear: got Secure=%v, want %v", name, got, tc.want)
		}
	}
}

func TestUserIDFromContext(t *testing.T) {
	if got := UserIDFromContext(context.Background()); got != 0 {
		t.Fatalf("got %d for a bare context, want 0", got)
	}
	ctx := context.WithValue(context.Background(), userIDKey, int64(42))
	if got := UserIDFromContext(ctx); got != 42 {
		t.Fatalf("got %d, want 42", got)
	}
	wrong := context.WithValue(context.Background(), userIDKey, "42")
	if got := UserIDFromContext(wrong); got != 0 {
		t.Fatalf("got %d for a non-int64 value, want 0", got)
	}
}

func TestMiddlewareGuardsHandlers(t *testing.T) {
	st := openTestDB(t)
	a := New(st.DB)
	uid := seedAdminRow(t, st)
	ctx := context.Background()
	tok, _, err := a.CreateSession(ctx, uid)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	var seen int64
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = UserIDFromContext(r.Context())
		w.WriteHeader(http.StatusTeapot)
	}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/jobs", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d without a cookie, want 401", w.Code)
	}

	w = httptest.NewRecorder()
	bad := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	bad.AddCookie(&http.Cookie{Name: CookieName, Value: "nope"})
	h.ServeHTTP(w, bad)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d for an unknown token, want 401", w.Code)
	}
	if len(w.Result().Cookies()) == 0 {
		t.Fatal("a rejected session did not clear the cookie")
	}

	w = httptest.NewRecorder()
	good := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	good.AddCookie(&http.Cookie{Name: CookieName, Value: tok})
	h.ServeHTTP(w, good)
	if w.Code != http.StatusTeapot {
		t.Fatalf("got %d for a valid session, want the handler to run", w.Code)
	}
	if seen != uid {
		t.Fatalf("handler saw user id %d, want %d", seen, uid)
	}
}

func TestRandomTokenLength(t *testing.T) {
	for _, n := range []int{1, 16, 32} {
		tok, err := randomToken(n)
		if err != nil {
			t.Fatalf("randomToken(%d): %v", n, err)
		}
		if len(tok) != n*2 {
			t.Fatalf("randomToken(%d) returned %d characters, want %d", n, len(tok), n*2)
		}
	}
}

func readCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	cookies := w.Result().Cookies()
	for _, c := range cookies {
		if c.Name == CookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in %v", CookieName, cookies)
	return nil
}
