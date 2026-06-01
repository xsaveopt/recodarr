package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/sratabix/recodarr/internal/auth"
)

var loginLimiter = auth.NewLoginLimiter()

func registerAuthRoutes(r chi.Router, a *auth.Store) {
	r.Get("/status", authStatus(a))
	r.Post("/setup", authSetup(a))
	r.Post("/login", authLogin(a))
	r.Post("/logout", authLogout(a))
}

type authStatusDTO struct {
	Setup    bool   `json:"setup"`
	Authed   bool   `json:"authed"`
	Username string `json:"username"`
}

func authStatus(a *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hasAdmin, err := a.HasAdmin(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := authStatusDTO{Setup: hasAdmin}
		if hasAdmin {
			if c, err := r.Cookie(auth.CookieName); err == nil {
				if _, err := a.LookupSession(r.Context(), c.Value); err == nil {
					out.Authed = true
					out.Username, _ = a.AdminUsername(r.Context())
				}
			}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

type credsDTO struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func authSetup(a *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var d credsDTO
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		if err := a.CreateAdmin(r.Context(), d.Username, d.Password); err != nil {
			if errors.Is(err, auth.ErrAlreadySetup) {
				http.Error(w, "already set up", http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		uid, err := a.VerifyPassword(r.Context(), d.Username, d.Password)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		tok, exp, err := a.CreateSession(r.Context(), uid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		auth.SetSessionCookie(w, r, tok, exp)
		writeJSON(w, http.StatusOK, map[string]string{"username": d.Username})
	}
}

func authLogin(a *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var d credsDTO
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		key := clientIP(r)
		if ok, retry := loginLimiter.Allow(key); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
			http.Error(w, "too many attempts; try again later", http.StatusTooManyRequests)
			return
		}
		uid, err := a.VerifyPassword(r.Context(), d.Username, d.Password)
		if err != nil {
			loginLimiter.RegisterFailure(key)
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}
		loginLimiter.Reset(key)
		tok, exp, err := a.CreateSession(r.Context(), uid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		auth.SetSessionCookie(w, r, tok, exp)
		writeJSON(w, http.StatusOK, map[string]string{"username": d.Username})
	}
}

func authLogout(a *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(auth.CookieName); err == nil {
			_ = a.DeleteSession(r.Context(), c.Value)
		}
		auth.ClearSessionCookie(w, r)
		w.WriteHeader(http.StatusNoContent)
	}
}

func clientIP(r *http.Request) string {
	if ip := middleware.GetClientIP(r.Context()); ip != "" {
		return ip
	}
	addr := r.RemoteAddr
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i]
		}
	}
	return addr
}
