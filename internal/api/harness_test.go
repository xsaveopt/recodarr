package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/xsaveopt/recodarr/internal/auth"
	"github.com/xsaveopt/recodarr/internal/health"
	"github.com/xsaveopt/recodarr/internal/job"
	"github.com/xsaveopt/recodarr/internal/store"
)

type stubWorker struct {
	encodingID  int64
	encodingIDs []int64
	lastTick    time.Time
	window      job.WindowStatus
	paused      bool
	pauseErr    error
	cancelled   []int64
	cancelOK    bool
}

func (s *stubWorker) CancelEncoding(jobID int64) bool {
	s.cancelled = append(s.cancelled, jobID)
	return s.cancelOK
}

func (s *stubWorker) EncodingJobID() int64               { return s.encodingID }
func (s *stubWorker) EncodingJobIDs() []int64            { return s.encodingIDs }
func (s *stubWorker) LastTickAt() time.Time              { return s.lastTick }
func (s *stubWorker) CurrentProgress() job.ProgressEvent { return job.ProgressEvent{} }
func (s *stubWorker) AllProgress() []job.ProgressEvent   { return nil }

func (s *stubWorker) Subscribe() (<-chan job.ProgressEvent, func()) {
	ch := make(chan job.ProgressEvent)
	return ch, func() { close(ch) }
}

func (s *stubWorker) WindowStatus(context.Context) job.WindowStatus { return s.window }

func (s *stubWorker) SetPaused(_ context.Context, paused bool) (int, error) {
	if s.pauseErr != nil {
		return 0, s.pauseErr
	}
	s.paused = paused
	return 0, nil
}

func (s *stubWorker) IsPaused(context.Context) bool { return s.paused }

type stubLogLevel struct{ level slog.Level }

func (s *stubLogLevel) SetAppLevel(l slog.Level) { s.level = l }

type testEnv struct {
	store  *store.Store
	worker *stubWorker
	logs   *stubLogLevel
	router http.Handler
	cookie *http.Cookie
}

func testAssets() fs.FS {
	return fstest.MapFS{
		"index.html":     {Data: []byte("<!doctype html><title>recodarr</title>")},
		"assets/app.js":  {Data: []byte("export const app = 1")},
		"assets/app.css": {Data: []byte(".app{}")},
	}
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	env := &testEnv{
		store:  st,
		worker: &stubWorker{},
		logs:   &stubLogLevel{},
	}

	a := auth.New(st.DB)
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)
	r.Route("/api", func(r chi.Router) {
		r.Use(requireCustomHeader)
		r.Use(maxBody(1 << 20))
		r.Route("/auth", func(r chi.Router) {
			registerAuthRoutes(r, a)
		})
		r.Group(func(r chi.Router) {
			r.Use(a.Middleware)
			registerAdminRoutes(r, st, env.worker, health.New(st), env.logs)
		})
	})
	r.Get("/health", healthHandler(st))
	r.Handle("/*", spaHandler(testAssets()))
	env.router = r
	return env
}

func (e *testEnv) login(t *testing.T) *testEnv {
	t.Helper()
	uid := seedAdmin(t, e.store)
	tok, exp, err := auth.New(e.store.DB).CreateSession(context.Background(), uid)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	e.cookie = &http.Cookie{Name: auth.CookieName, Value: tok, Expires: exp}
	return e
}

func seedAdmin(t *testing.T, st *store.Store) int64 {
	t.Helper()
	res, err := st.DB.Exec(
		`INSERT INTO admin_users (username, password_hash) VALUES ('admin', 'x')`)
	if err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("admin id: %v", err)
	}
	return id
}

func (e *testEnv) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, path, nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		r = httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("X-Recodarr", "1")
	if e.cookie != nil {
		r.AddCookie(e.cookie)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, r)
	return w
}

func decodeJSON[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return out
}

func wantStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("got %d, want %d: %s", w.Code, want, w.Body.String())
	}
}
