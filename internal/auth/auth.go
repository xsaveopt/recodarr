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
	SessionLifetime = 30 * 24 * time.Hour
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

func (s *Store) HasAdmin(ctx context.Context) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_users`).Scan(&n)
	return n > 0, err
}

func (s *Store) CreateAdmin(ctx context.Context, username, password string) error {
	if username == "" || password == "" {
		return errors.New("username and password required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO admin_users (username, password_hash)
		 SELECT ?, ? WHERE NOT EXISTS (SELECT 1 FROM admin_users)`,
		username, string(hash))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrAlreadySetup
	}
	return nil
}

func (s *Store) VerifyPassword(ctx context.Context, username, password string) (int64, error) {
	var id int64
	var hash string
	err := s.DB.QueryRowContext(ctx,
		`SELECT id, password_hash FROM admin_users WHERE username = ?`, username).
		Scan(&id, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$12$"), []byte(password))
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

func (s *Store) AdminUsername(ctx context.Context) (string, error) {
	var u string
	err := s.DB.QueryRowContext(ctx, `SELECT username FROM admin_users LIMIT 1`).Scan(&u)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return u, err
}

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

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token)
	return err
}

func (s *Store) PurgeExpiredSessions(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, time.Now())
	return err
}

func (s *Store) ResetAdmin(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
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

func UserIDFromContext(ctx context.Context) int64 {
	if v, ok := ctx.Value(userIDKey).(int64); ok {
		return v
	}
	return 0
}

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
