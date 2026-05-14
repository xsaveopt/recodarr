package api

import (
	"io"
	"io/fs"
	"net/http"
	"strings"
)

// spaHandler serves files from the embedded SPA bundle.
// Falls back to index.html for any path without a matching asset, so the Vue router can take over.
// Unknown /api/* and /webhook/* paths return 404 instead of the SPA shell — otherwise a typo'd
// API call would land on index.html with HTTP 200, which is invisible to clients.
func spaHandler(assets fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/webhook/") {
			http.NotFound(w, r)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			serveIndex(w, assets)
			return
		}
		if _, err := fs.Stat(assets, path); err != nil {
			serveIndex(w, assets)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, assets fs.FS) {
	f, err := assets.Open("index.html")
	if err != nil {
		http.Error(w, "index not found", http.StatusNotFound)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, f)
}
