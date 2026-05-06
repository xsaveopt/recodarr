package api

import "net/http"

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
