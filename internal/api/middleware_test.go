package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestRequireCustomHeaderLetsSafeMethodsThrough(t *testing.T) {
	h := requireCustomHeader(okHandler())
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(m, "/api/jobs", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s without the header got %d, want 200", m, w.Code)
		}
	}
}

func TestRequireCustomHeaderBlocksMutationsWithoutTheHeader(t *testing.T) {
	h := requireCustomHeader(okHandler())
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(m, "/api/jobs", nil))
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s without the header got %d, want 403", m, w.Code)
		}

		w = httptest.NewRecorder()
		r := httptest.NewRequest(m, "/api/jobs", nil)
		r.Header.Set("X-Recodarr", "1")
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s with the header got %d, want 200", m, w.Code)
		}
	}
}

func TestRequireCustomHeaderAcceptsAnyNonEmptyValue(t *testing.T) {
	h := requireCustomHeader(okHandler())
	for _, v := range []string{"1", "true", "whatever"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/jobs", nil)
		r.Header.Set("X-Recodarr", v)
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("header %q got %d, want 200", v, w.Code)
		}
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/jobs", nil)
	r.Header.Set("X-Recodarr", "")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("an empty header got %d, want 403", w.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	securityHeaders(okHandler()).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	h := w.Header()
	for k, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := h.Get(k); got != want {
			t.Fatalf("%s = %q, want %q", k, got, want)
		}
	}
	csp := h.Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'self'",
		"script-src 'self'",
		"frame-ancestors 'none'",
		"base-uri 'self'",
		"form-action 'self'",
	} {
		if !strings.Contains(csp, want) {
			t.Fatalf("CSP %q is missing %q", csp, want)
		}
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Fatal("the CSP allows inline script")
	}
}

func TestMaxBodyRejectsOversizedPayloads(t *testing.T) {
	var readErr error
	h := maxBody(16)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader("tiny")))
	if readErr != nil {
		t.Fatalf("a small body failed to read: %v", readErr)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader(strings.Repeat("x", 64))))
	if readErr == nil {
		t.Fatal("an oversized body was read without error")
	}
}

func TestRequestLoggerFallsBackToTheDefaultLogger(t *testing.T) {
	var called bool
	h := requestLogger(nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusCreated)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/jobs", nil))
	if !called {
		t.Fatal("the wrapped handler did not run")
	}
	if w.Code != http.StatusCreated {
		t.Fatalf("got %d, want the handler status to pass through", w.Code)
	}
}

func TestRequestLoggerWritesAnAccessLine(t *testing.T) {
	var buf strings.Builder
	access := slog.New(slog.NewTextHandler(&sink{&buf}, nil))
	h := requestLogger(access)(okHandler())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/jobs", nil))
	line := buf.String()
	for _, want := range []string{"method=GET", "path=/api/jobs", "status=200"} {
		if !strings.Contains(line, want) {
			t.Fatalf("access line %q is missing %q", line, want)
		}
	}
}

type sink struct{ b *strings.Builder }

func (s *sink) Write(p []byte) (int, error) { return s.b.Write(p) }

func TestWriteJSONSetsContentTypeAndStatus(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusTeapot, map[string]int{"n": 1})
	if w.Code != http.StatusTeapot {
		t.Fatalf("got %d, want 418", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("got content type %q, want application/json", ct)
	}
	if got := strings.TrimSpace(w.Body.String()); got != `{"n":1}` {
		t.Fatalf("got body %q", got)
	}
}
