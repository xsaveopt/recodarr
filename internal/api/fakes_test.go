package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fakeArrTag struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`
}

type fakeArr struct {
	apiKey   string
	tags     []fakeArrTag
	series   []map[string]any
	movies   []map[string]any
	files    map[string][]map[string]any
	failTags bool

	mu      sync.Mutex
	edits   []map[string]any
	editErr bool
}

func (f *fakeArr) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeArr) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Api-Key") != f.apiKey {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	path := r.URL.Path
	switch {
	case path == "/api/v3/system/status":
		_, _ = w.Write([]byte(`{"version":"4"}`))
	case path == "/api/v3/tag":
		if f.failTags {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeFake(w, f.tags)
	case path == "/api/v3/series":
		writeFake(w, f.series)
	case path == "/api/v3/movie":
		writeFake(w, f.movies)
	case path == "/api/v3/episodefile":
		writeFake(w, f.files[r.URL.Query().Get("seriesId")])
	case strings.HasPrefix(path, "/api/v3/movie/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(path, "/api/v3/movie/")
		for _, m := range f.movies {
			if jsonID(m["id"]) == id {
				writeFake(w, m)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	case path == "/api/v3/series/editor" || path == "/api/v3/movie/editor":
		if f.editErr {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.edits = append(f.edits, body)
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeArr) editCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.edits)
}

func jsonID(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func writeFake(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

type fakeQbit struct {
	password string
	torrents []map[string]any
	infoCode int
}

func (f *fakeQbit) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_ = r.ParseForm()
			if r.Form.Get("password") != f.password {
				_, _ = w.Write([]byte("Fails."))
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "s", Path: "/"})
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			if f.infoCode != 0 {
				w.WriteHeader(f.infoCode)
				return
			}
			out := []map[string]any{}
			want := r.URL.Query().Get("hashes")
			for _, tr := range f.torrents {
				if tr["hash"] == want {
					out = append(out, tr)
				}
			}
			writeFake(w, out)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}
