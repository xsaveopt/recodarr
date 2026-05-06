// Package auth implements single-admin-user authentication for Recodarr.
//
// Storage: admin_users (one row, by design) + sessions (random opaque tokens, server-side
// lookup, no JWT). Sessions live in SQLite so revocation is trivial — delete the row.
//
// Cookie: HttpOnly, SameSite=Lax, Secure if request was HTTPS. Path=/. The cookie value is
// 32 random bytes hex-encoded; never the user id, never bcrypt output.
package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	CookieName      = "recodarr_session"
	SessionLifetime = 30 * 24 * time.Hour // 30 days
	bcryptCost      = 12
)

var (
	ErrNoAdmin       = errors.New("no admin user")
	ErrBadCredential = errors.New("bad credentials")
	ErrAlreadySetup  = errors.New("admin already exists")
)

type Store struct {
	DB *sql.DB
}

func New(db *sql.DB) *Store { return &Store{DB: db} }

// HasAdmin reports whether an admin user exists. Used by the SPA to decide between
// login screen and first-run setup screen.
func (s *Store) HasAdmin(ctx context.Context) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_users`).Scan(&n)
	return n > 0, err
}

// CreateAdmin creates the admin user. Fails if any admin already exists — the reset
// flow (CLI subcommand) must wipe the row first.
func (s *Store) CreateAdmin(ctx context.Context, username, password string) error {
	if username == "" || password == "" {
		return errors.New("username and password required")
	}
	exists, err := s.HasAdmin(ctx)
	if err != nil {
		return err
	}
	if exists {
		return ErrAlreadySetup
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO admin_users (username, password_hash) VALUES (?, ?)`,
		username, string(hash))
	return err
}

// VerifyPassword checks credentials. Returns the user id on success.
// Constant-time bcrypt compare; do not short-circuit on missing user — still hash a
// dummy to avoid trivial username-enumeration via timing.
func (s *Store) VerifyPassword(ctx context.Context, username, password string) (int64, error) {
	var id int64
	var hash string
	err := s.DB.QueryRowContext(ctx,
		`SELECT id, password_hash FROM admin_users WHERE username = ?`, username).
		Scan(&id, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$12$" /* invalid, forces full-cost hash */), []byte(password))
		return 0, ErrBadCredential
	}
	if err != nil {
		return 0, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return 0, ErrBadCredential
	}
	return id, nil
}

// AdminUsername returns the (only) admin's username, or "" if none.
func (s *Store) AdminUsername(ctx context.Context) (string, error) {
	var u string
	err := s.DB.QueryRowContext(ctx, `SELECT username FROM admin_users LIMIT 1`).Scan(&u)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return u, err
}

// CreateSession issues a fresh session token bound to userID and inserts it.
func (s *Store) CreateSession(ctx context.Context, userID int64) (string, time.Time, error) {
	tok, err := randomToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	exp := time.Now().Add(SessionLifetime)
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)`,
		tok, userID, exp)
	if err != nil {
		return "", time.Time{}, err
	}
	return tok, exp, nil
}

// LookupSession returns the user id for a valid, non-expired session token.
func (s *Store) LookupSession(ctx context.Context, token string) (int64, error) {
	if token == "" {
		return 0, ErrBadCredential
	}
	var userID int64
	var exp time.Time
	err := s.DB.QueryRowContext(ctx,
		`SELECT user_id, expires_at FROM sessions WHERE token = ?`, token).
		Scan(&userID, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrBadCredential
	}
	if err != nil {
		return 0, err
	}
	if time.Now().After(exp) {
		_, _ = s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token)
		return 0, ErrBadCredential
	}
	return userID, nil
}

// DeleteSession invalidates a single session (logout).
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token)
	return err
}

// PurgeExpiredSessions removes expired rows. Cheap; safe to call on startup.
func (s *Store) PurgeExpiredSessions(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, time.Now())
	return err
}

// ResetAdmin nukes admin user(s) and all sessions. Used by the `reset-admin` CLI.
func (s *Store) ResetAdmin(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM admin_users`); err != nil {
		return err
	}
	return tx.Commit()
}

func randomToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// SetSessionCookie writes the session cookie on the response. Secure flag is set when
// the request came in over HTTPS (TLS-terminating proxy must set X-Forwarded-Proto for
// chi/middleware.RealIP-style flows; we look at r.TLS as the conservative default).
func SetSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteStrictMode,
	})
}

// ClearSessionCookie expires the cookie client-side (defense in depth alongside the
// server-side row delete).
func ClearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteStrictMode,
	})
}

type ctxKey int

const userIDKey ctxKey = 1

// UserIDFromContext returns the authenticated user id, or 0 if none.
func UserIDFromContext(ctx context.Context) int64 {
	if v, ok := ctx.Value(userIDKey).(int64); ok {
		return v
	}
	return 0
}

// Middleware enforces a valid session cookie. Returns 401 on failure (the SPA
// router-guard turns that into a redirect to /login).
func (s *Store) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(CookieName)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		uid, err := s.LookupSession(r.Context(), c.Value)
		if err != nil {
			ClearSessionCookie(w, r)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), userIDKey, uid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
