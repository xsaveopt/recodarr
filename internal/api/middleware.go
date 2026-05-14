package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// requestLogger logs every HTTP request through slog so they show up next to
// the rest of the application logs (and in `docker logs`). Wraps the response
// writer to capture the actual status code we sent back.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		next.ServeHTTP(ww, r)
		slog.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"bytes", ww.BytesWritten(),
			"dur_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
			"ua", r.UserAgent(),
		)
	})
}

// securityHeaders sets a conservative baseline of security-relevant response headers.
// CSP is strict but allows the inline styles PrimeVue injects at runtime.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"font-src 'self' data:; "+
				"connect-src 'self'; "+
				"frame-ancestors 'none'; "+
				"base-uri 'self'; "+
				"form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

// maxBody caps r.Body to n bytes. Decoders see io.ErrUnexpectedEOF when the
// limit is hit and respond with a 4xx; without this, an attacker (or a bug)
// could OOM the process by streaming a huge body. 1 MiB is plenty for our
// JSON DTOs and *arr webhook payloads.
func maxBody(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, n)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// requireCustomHeader rejects mutating requests that don't carry a custom header.
// Browsers won't send custom headers cross-origin without a CORS preflight (which
// we don't allow), so this defeats classic form-based CSRF.
//
// The SPA fetcher always sends X-Recodarr; webhooks come from *arr (server-side,
// no browser cookies) and have their own per-instance token, so they're exempt.
func requireCustomHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("X-Recodarr") == "" {
			http.Error(w, "missing X-Recodarr header", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
