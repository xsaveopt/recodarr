package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSpaHandlerServesIndexAtTheRoot(t *testing.T) {
	h := spaHandler(testAssets())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "recodarr") {
		t.Fatalf("got body %q, want index.html", w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("got Cache-Control %q, want no-store", got)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("got content type %q, want html", ct)
	}
}

func TestSpaHandlerFallsBackToIndexForClientRoutes(t *testing.T) {
	h := spaHandler(testAssets())
	for _, path := range []string{"/login", "/setup", "/jobs/123", "/settings/mappings"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s got %d, want the SPA fallback", path, w.Code)
		}
		if !strings.Contains(w.Body.String(), "recodarr") {
			t.Fatalf("%s did not serve index.html", path)
		}
	}
}

func TestSpaHandlerServesRealAssets(t *testing.T) {
	h := spaHandler(testAssets())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "export const app") {
		t.Fatalf("got body %q, want the asset", w.Body.String())
	}
}

func TestSpaHandlerNeverSwallowsApiPaths(t *testing.T) {
	h := spaHandler(testAssets())
	for _, path := range []string{"/api/", "/api/nope", "/api/jobs/1"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s got %d, want 404 rather than the SPA shell", path, w.Code)
		}
		if strings.Contains(w.Body.String(), "recodarr") {
			t.Fatalf("%s served the SPA shell to an API client", path)
		}
	}
}

func TestSpaHandlerReportsAMissingIndex(t *testing.T) {
	h := spaHandler(fstest.MapFS{})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404 when index.html is absent", w.Code)
	}
}
